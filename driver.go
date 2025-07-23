package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"

	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"
)

const (
	kubeletPluginRegistryPath = "/var/lib/kubelet/plugins_registry"
	kubeletPluginPath         = "/var/lib/kubelet/plugins"
)

const (
	// maxAttempts indicates the number of times the driver will try to recover itself before failing
	maxAttempts = 5
)

// NetworkDriver implements the kubeletplugin.Driver interface
// It manages network devices and integrates with the Dynamic Resource Allocation (DRA) framework.
// It also implements the NRI plugin to manage network resources for pods.
type NetworkDriver struct {
	driverName string
	nodeName   string
	kubeClient kubernetes.Interface
	draPlugin  *kubeletplugin.Helper
	nriPlugin  stub.Stub

	// podDeviceConfig is a map that stores allocated network devices for each pod identified by its UID.
	// The context of the map is to allow fast access to the allocated devices
	// during pod lifecycle events, such as RunPodSandbox and StopPodSandbox.
	mu              sync.Mutex                      // protects podDeviceConfig
	podDeviceConfig map[types.UID][]AllocatedDevice // maps pod UID to allocated devices with attributes
}

// AllocatedDevice represents a network device allocated to a pod with its configuration
type AllocatedDevice struct {
	Name       string
	Attributes map[string]string
	PoolName   string
	Request    string
}

type Option func(*NetworkDriver)

func Start(ctx context.Context, driverName string, kubeClient kubernetes.Interface, nodeName string, opts ...Option) (*NetworkDriver, error) {
	registerMetrics()

	networkDriver := &NetworkDriver{
		driverName:      driverName,
		nodeName:        nodeName,
		kubeClient:      kubeClient,
		podDeviceConfig: make(map[types.UID][]AllocatedDevice),
	}

	for _, option := range opts {
		option(networkDriver)
	}

	driverPluginPath := filepath.Join(kubeletPluginPath, driverName)
	err := os.MkdirAll(driverPluginPath, 0750)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %v", driverPluginPath, err)
	}

	kubeletOptions := []kubeletplugin.Option{
		kubeletplugin.DriverName(driverName),
		kubeletplugin.NodeName(nodeName),
		kubeletplugin.KubeClient(kubeClient),
	}
	draHelper, err := kubeletplugin.Start(ctx, networkDriver, kubeletOptions...)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	networkDriver.draPlugin = draHelper

	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		registrationStatus := networkDriver.draPlugin.RegistrationStatus()
		if registrationStatus == nil {
			return false, nil
		}
		return registrationStatus.PluginRegistered, nil
	})
	if err != nil {
		return nil, err
	}

	// register the NRI plugin
	nriOptions := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			klog.Infof("%s NRI plugin closed", driverName)
		}),
	}
	nriStub, err := stub.New(networkDriver, nriOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %v", err)
	}
	networkDriver.nriPlugin = nriStub

	go func() {
		for attemptCount := 0; attemptCount < maxAttempts; attemptCount++ {
			err = networkDriver.nriPlugin.Run(ctx)
			if err != nil {
				klog.Infof("NRI plugin failed with error %v", err)
			}
			select {
			case <-ctx.Done():
				return
			default:
				klog.Infof("Restarting NRI plugin %d out of %d", attemptCount, maxAttempts)
			}
		}
		klog.Fatalf("NRI plugin failed for %d times to be restarted", maxAttempts)
	}()

	// publish available resources
	go networkDriver.PublishResources(ctx)

	return networkDriver, nil
}

func (nd *NetworkDriver) Stop() {
	klog.Info("Stopping network driver...")

	// Stop NRI plugin first
	if nd.nriPlugin != nil {
		nd.nriPlugin.Stop()
	}

	// Stop DRA plugin
	if nd.draPlugin != nil {
		nd.draPlugin.Stop()
	}

	klog.Info("Network driver stopped")
}

func (nd *NetworkDriver) Shutdown(_ context.Context) {
	klog.Info("Runtime shutting down...")
}

// DRA hooks exposes Network Devices to Kubernetes
// and allows the kubelet to discover and use them as resources for pods.

// PublishResources periodically discovers network devices and publishes them as resources
// to the DRA plugin.
func (nd *NetworkDriver) PublishResources(ctx context.Context) {
	klog.V(2).Infof("Publishing resources")
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			klog.Error(ctx.Err(), "context canceled")
			return
		case <-ticker.C:
			devices, err := nd.getNetworkDevices()
			if err != nil {
				klog.Error(err, "failed to get network devices")
				continue
			}

			resources := resourceslice.DriverResources{
				Pools: map[string]resourceslice.Pool{
					nd.nodeName: {Slices: []resourceslice.Slice{{Devices: devices}}},
				},
			}

			if err := nd.draPlugin.PublishResources(ctx, resources); err != nil {
				klog.Error(err, "unexpected error trying to publish resources")
			} else {
				klog.V(2).Infof("Published %d network devices", len(devices))
			}
		}
	}
}

// getNetworkDevices discovers all network interfaces on the host and returns them as DRA devices.
// This is an example implementation that retrieves network interfaces,
// filters out loopback and down interfaces, and skips common virtual interfaces used by containers.
// It returns a slice of resourceapi.Device representing the available network devices.
// Each device includes attributes like interface name, MAC address, MTU, and IP addresses.
func (nd *NetworkDriver) getNetworkDevices() ([]resourceapi.Device, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("failed to get network interfaces: %w", err)
	}

	var devices []resourceapi.Device

	for _, iface := range interfaces {
		// Skip loopback and down interfaces
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}

		// Skip virtual interfaces commonly used by containers/kubernetes
		if nd.shouldSkipInterface(iface.Name) {
			continue
		}

		device := resourceapi.Device{
			Name: iface.Name,
			Basic: &resourceapi.BasicDevice{
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					"interface-name": {
						StringValue: &iface.Name,
					},
					"mac-address": {
						StringValue: ptr.To(iface.HardwareAddr.String()),
					},
					"mtu": {
						IntValue: func() *int64 {
							mtu := int64(iface.MTU)
							return &mtu
						}(),
					},
					"flags": {
						StringValue: func() *string {
							flags := iface.Flags.String()
							return &flags
						}(),
					},
				},
			},
		}

		// Add IP addresses if available
		addrs, err := iface.Addrs()
		if err == nil && len(addrs) > 0 {
			var ipAddresses []string
			for _, addr := range addrs {
				ipAddresses = append(ipAddresses, addr.String())
			}
			if len(ipAddresses) > 0 {
				ipList := fmt.Sprintf("%v", ipAddresses)
				device.Basic.Attributes["ip-addresses"] = resourceapi.DeviceAttribute{
					StringValue: &ipList,
				}
			}
		}

		devices = append(devices, device)
		klog.V(4).Infof("Discovered network device: %s (MAC: %s, MTU: %d)",
			iface.Name, iface.HardwareAddr.String(), iface.MTU)
	}

	return devices, nil
}

// shouldSkipInterface determines if a network interface should be skipped
func (nd *NetworkDriver) shouldSkipInterface(name string) bool {
	// Skip common virtual interfaces
	skipPrefixes := []string{
		"docker",
		"br-",
		"veth",
		"cni",
		"flannel",
		"weave",
		"cali",
		"kube",
		"virbr",
		"vmnet",
	}

	for _, prefix := range skipPrefixes {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}

	return false
}

// The hooks NodePrepareResources and NodeUnprepareResources are needed to collect the necessary
// information so the NRI hooks can perform the configuration and attachment of Pods at runtime.

// PrepareResourceClaims is called by the kubelet to prepare resource claims for pods.
func (nd *NetworkDriver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	klog.V(2).Infof("PrepareResourceClaims is called: number of claims: %d", len(claims))

	nodePrepareRequestsTotal.Inc()

	if len(claims) == 0 {
		return nil, nil
	}
	result := make(map[types.UID]kubeletplugin.PrepareResult)

	for _, claim := range claims {
		klog.V(2).Infof("NodePrepareResources: Claim Request %s/%s", claim.Namespace, claim.Name)
		result[claim.UID] = nd.prepareResourceClaim(ctx, claim)
	}
	return result, nil
}

// prepareResourceClaim gets all the configuration required to be applied at runtime and passes it downs to the handlers.
// This happens in the kubelet so it can be a "slow" operation, so we can execute fast in RunPodsandbox, that happens in the
// container runtime and has strong expectactions to be executed fast (default hook timeout is 2 seconds).
func (nd *NetworkDriver) prepareResourceClaim(ctx context.Context, resourceClaim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	klog.V(2).Infof("PrepareResourceClaim Claim %s/%s", resourceClaim.Namespace, resourceClaim.Name)
	startTime := time.Now()
	defer func() {
		klog.V(2).Infof("PrepareResourceClaim Claim %s/%s took %v", resourceClaim.Namespace, resourceClaim.Name, time.Since(startTime))
	}()

	// Extract pod UIDs that this claim is reserved for
	allocatedPodUIDs := []types.UID{}
	for _, reservedReference := range resourceClaim.Status.ReservedFor {
		if reservedReference.Resource != "pods" || reservedReference.APIGroup != "" {
			klog.Infof("Driver only supports Pods, unsupported reference %#v", reservedReference)
			continue
		}
		allocatedPodUIDs = append(allocatedPodUIDs, reservedReference.UID)
	}

	if len(allocatedPodUIDs) == 0 {
		klog.Infof("no pods allocated to claim %s/%s", resourceClaim.Namespace, resourceClaim.Name)
		return kubeletplugin.PrepareResult{}
	}

	if resourceClaim.Status.Allocation == nil ||
		len(resourceClaim.Status.Allocation.Devices.Results) == 0 {
		klog.Infof("claim %s/%s has no allocated devices", resourceClaim.Namespace, resourceClaim.Name)
		return kubeletplugin.PrepareResult{}
	}

	var processingErrors []error
	var allocatedDevicesForClaim []AllocatedDevice

	// Process each allocated device
	for _, allocationResult := range resourceClaim.Status.Allocation.Devices.Results {
		if allocationResult.Driver != nd.driverName {
			continue
		}

		// Get device attributes from published resources
		deviceAttributes, err := nd.getDeviceAttributes(allocationResult.Device)
		if err != nil {
			processingErrors = append(processingErrors, fmt.Errorf("failed to get attributes for device %s: %w", allocationResult.Device, err))
			continue
		}

		allocatedDevice := AllocatedDevice{
			Name:       allocationResult.Device,
			Attributes: deviceAttributes,
			PoolName:   allocationResult.Pool,
			Request:    allocationResult.Request,
		}

		allocatedDevicesForClaim = append(allocatedDevicesForClaim, allocatedDevice)
		klog.V(2).Infof("Prepared device %s for claim %s/%s with attributes: %v",
			allocationResult.Device, resourceClaim.Namespace, resourceClaim.Name, deviceAttributes)
	}

	// Store device configuration for all allocated pods
	nd.mu.Lock()
	for _, podUID := range allocatedPodUIDs {
		if nd.podDeviceConfig[podUID] == nil {
			nd.podDeviceConfig[podUID] = []AllocatedDevice{}
		}
		nd.podDeviceConfig[podUID] = append(nd.podDeviceConfig[podUID], allocatedDevicesForClaim...)
		klog.V(2).Infof("Stored device configuration for pod UID %s: %d devices", podUID, len(allocatedDevicesForClaim))
	}
	nd.mu.Unlock()

	if len(processingErrors) > 0 {
		klog.Infof("claim %s contains errors: %v", resourceClaim.UID, errors.Join(processingErrors...))
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s contains errors: %w", resourceClaim.UID, errors.Join(processingErrors...)),
		}
	}

	return kubeletplugin.PrepareResult{}
}

// getDeviceAttributes retrieves device attributes for a given device name
func (nd *NetworkDriver) getDeviceAttributes(deviceName string) (map[string]string, error) {
	// Get fresh device list to find attributes
	availableDevices, err := nd.getNetworkDevices()
	if err != nil {
		return nil, fmt.Errorf("failed to get network devices: %w", err)
	}

	for _, device := range availableDevices {
		if device.Name == deviceName {
			deviceAttributes := make(map[string]string)
			for attributeName, attributeValue := range device.Basic.Attributes {
				if attributeValue.StringValue != nil {
					deviceAttributes[string(attributeName)] = *attributeValue.StringValue
				} else if attributeValue.IntValue != nil {
					deviceAttributes[string(attributeName)] = fmt.Sprintf("%d", *attributeValue.IntValue)
				}
			}
			return deviceAttributes, nil
		}
	}

	return nil, fmt.Errorf("device %s not found in available devices", deviceName)
}

func (nd *NetworkDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.V(2).Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))

	if len(claims) == 0 {
		return nil, nil
	}

	result := make(map[types.UID]error)

	for _, claim := range claims {
		klog.V(2).Infof("UnprepareResourceClaim: Claim UID %s", claim.UID)
		result[claim.UID] = nd.unprepareResourceClaim(ctx, claim)
	}

	return result, nil
}

// unprepareResourceClaim cleans up resources allocated by a claim
func (nd *NetworkDriver) unprepareResourceClaim(ctx context.Context, claim kubeletplugin.NamespacedObject) error {
	klog.V(2).Infof("UnprepareResourceClaim Claim UID %s", claim.UID)
	startTime := time.Now()
	defer func() {
		klog.V(2).Infof("UnprepareResourceClaim Claim UID %s took %v", claim.UID, time.Since(startTime))
	}()

	// Find and remove any pod device configurations associated with this claim
	nd.mu.Lock()
	defer nd.mu.Unlock()

	var removedDevices int
	for podUID, devices := range nd.podDeviceConfig {
		// Note: In a real implementation, you'd need to track which claim
		// allocated which devices. For now, we'll just log this operation.
		klog.V(2).Infof("Pod UID %s has %d allocated devices", podUID, len(devices))
		// In practice, you might filter devices by claim or other criteria here
	}

	klog.V(2).Infof("UnprepareResourceClaim completed for claim UID %s, removed %d device allocations", claim.UID, removedDevices)

	return nil
}

// NRI hooks into the container runtime, the lifecycle of the Pod seen here is local to the runtime
// and is not the same as the Pod lifecycle for kubernetes, per example, a Pod that can fail to start
// is retried locally multiple times, so the hooks need to be idempotent to all operations on the Pod.
// The NRI hooks are time sensitive, any slow operation needs to be added on the DRA hooks and only
// the information necessary should passed to the NRI hooks via the np.podConfigStore so it can be executed
// quickly.
// Synchronize is called to synchronize the state of pods and containers with the runtime.
// It is called when the runtime starts or when the kubelet restarts.
// It should not perform any operations that take a long time, as it is expected to return quickly.
// Instead, it should only gather the necessary information to be used later in the lifecycle of the Pod.
// This is a good place to store the Pod metadata in the np.podConfigStore so it can be used later in the lifecycle of the Pod.
func (np *NetworkDriver) Synchronize(_ context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("Synchronized state with the runtime (%d pods, %d containers)...",
		len(pods), len(containers))

	for _, pod := range pods {
		klog.Infof("Synchronize Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
		// get the pod network namespace
		ns := getNetworkNamespace(pod)
		// host network pods are skipped
		if ns != "" {
			klog.V(2).Infof("pod %s/%s: namespace=%s ips=%v", pod.GetNamespace(), pod.GetName(), ns, pod.GetIps())
		}
	}

	return nil, nil
}

// CreateContainer handles container creation requests.
func (np *NetworkDriver) CreateContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	klog.V(2).Infof("CreateContainer Pod %s/%s UID %s Container %s", pod.Namespace, pod.Name, pod.Uid, ctr.Name)

	return nil, nil, nil
}

// RunPodSandbox is called when a pod sandbox is started.
// It configures the allocated network devices for the pod based on its network namespace.
func (nd *NetworkDriver) RunPodSandbox(ctx context.Context, podSandbox *api.PodSandbox) error {
	klog.V(2).Infof("RunPodSandbox Pod %s/%s UID %s", podSandbox.Namespace, podSandbox.Name, podSandbox.Uid)
	startTime := time.Now()
	defer func() {
		klog.V(2).Infof("RunPodSandbox Pod %s/%s UID %s took %v", podSandbox.Namespace, podSandbox.Name, podSandbox.Uid, time.Since(startTime))
	}()

	networkNamespace := getNetworkNamespace(podSandbox)
	// host network pods cannot allocate network devices because it impacts the host
	if networkNamespace == "" {
		return fmt.Errorf("RunPodSandbox pod %s/%s using host network cannot claim host devices", podSandbox.Namespace, podSandbox.Name)
	}

	// Get allocated devices for this pod
	podUID := types.UID(podSandbox.Uid)
	nd.mu.Lock()
	allocatedDevices := nd.podDeviceConfig[podUID]
	nd.mu.Unlock()

	if len(allocatedDevices) == 0 {
		klog.V(2).Infof("No network devices allocated to pod %s/%s", podSandbox.Namespace, podSandbox.Name)
		return nil
	}

	klog.V(2).Infof("Processing %d allocated network devices for pod %s/%s",
		len(allocatedDevices), podSandbox.Namespace, podSandbox.Name)

	// Process each allocated device
	for _, allocatedDevice := range allocatedDevices {
		err := nd.configureDeviceForPod(allocatedDevice, networkNamespace, podSandbox)
		if err != nil {
			return fmt.Errorf("failed to configure device %s for pod %s/%s: %w",
				allocatedDevice.Name, podSandbox.Namespace, podSandbox.Name, err)
		}

		klog.V(2).Infof("Successfully configured device %s for pod %s/%s in namespace %s",
			allocatedDevice.Name, podSandbox.Namespace, podSandbox.Name, networkNamespace)
	}

	return nil
}

// configureDeviceForPod configures a network device for use by a pod
func (nd *NetworkDriver) configureDeviceForPod(device AllocatedDevice, networkNamespace string, podSandbox *api.PodSandbox) error {
	// TODO: Implement actual device configuration logic here
	// This could include:
	// - Moving the device to the pod's network namespace
	// - Setting up VLAN/MACVLAN/etc. based on device attributes
	// - Configuring IP addresses if needed
	// - Setting up routing rules

	klog.V(2).Infof("Configuring device %s with attributes %v for pod %s/%s in namespace %s",
		device.Name, device.Attributes, podSandbox.Namespace, podSandbox.Name, networkNamespace)

	// Example configuration based on device attributes:
	if macAddress, exists := device.Attributes["mac-address"]; exists {
		klog.V(2).Infof("Device %s has MAC address: %s", device.Name, macAddress)
	}

	if mtu, exists := device.Attributes["mtu"]; exists {
		klog.V(2).Infof("Device %s has MTU: %s", device.Name, mtu)
	}

	return nil
}

// StopPodSandbox is called when a pod sandbox is stopped.
// It cleans up the allocated network devices for the pod.
func (nd *NetworkDriver) StopPodSandbox(ctx context.Context, podSandbox *api.PodSandbox) error {
	klog.V(2).Infof("StopPodSandbox Pod %s/%s UID %s", podSandbox.Namespace, podSandbox.Name, podSandbox.Uid)

	networkNamespace := getNetworkNamespace(podSandbox)
	if networkNamespace == "" {
		klog.V(2).Infof("StopPodSandbox Pod %s/%s UID %s Network Namespace %s",
			podSandbox.Namespace, podSandbox.Name, podSandbox.Uid, networkNamespace)
	}

	// Get allocated devices for cleanup
	podUID := types.UID(podSandbox.Uid)
	nd.mu.Lock()
	allocatedDevices := nd.podDeviceConfig[podUID]
	nd.mu.Unlock()

	if len(allocatedDevices) > 0 {
		klog.V(2).Infof("Cleaning up %d network devices for pod %s/%s",
			len(allocatedDevices), podSandbox.Namespace, podSandbox.Name)

		for _, device := range allocatedDevices {
			err := nd.cleanupDeviceForPod(device, networkNamespace, podSandbox)
			if err != nil {
				// Log error but don't fail - kernel will cleanup when namespace is deleted
				klog.Errorf("Failed to cleanup device %s for pod %s/%s: %v",
					device.Name, podSandbox.Namespace, podSandbox.Name, err)
			}
		}
	}

	return nil
}

// RemovePodSandbox is called when a pod sandbox is removed.
func (nd *NetworkDriver) RemovePodSandbox(_ context.Context, podSandbox *api.PodSandbox) error {
	klog.V(2).Infof("RemovePodSandbox Pod %s/%s UID %s", podSandbox.Namespace, podSandbox.Name, podSandbox.Uid)

	// Clean up stored device configuration
	podUID := types.UID(podSandbox.Uid)
	nd.mu.Lock()
	delete(nd.podDeviceConfig, podUID)
	nd.mu.Unlock()

	klog.V(2).Infof("Removed device configuration for pod %s/%s", podSandbox.Namespace, podSandbox.Name)
	return nil
}

// cleanupDeviceForPod cleans up a network device when a pod is stopped
func (nd *NetworkDriver) cleanupDeviceForPod(device AllocatedDevice, networkNamespace string, podSandbox *api.PodSandbox) error {
	// TODO: Implement actual device cleanup logic here
	// This could include:
	// - Moving the device back to the host network namespace
	// - Removing VLAN/MACVLAN interfaces
	// - Cleaning up routing rules

	klog.V(2).Infof("Cleaning up device %s for pod %s/%s",
		device.Name, podSandbox.Namespace, podSandbox.Name)

	return nil
}

func getNetworkNamespace(pod *api.PodSandbox) string {
	// get the pod network namespace
	for _, namespace := range pod.Linux.GetNamespaces() {
		if namespace.Type == "network" {
			return namespace.Path
		}
	}
	return ""
}
