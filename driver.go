package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
)

const (
	kubeletPluginRegistryPath = "/var/lib/kubelet/plugins_registry"
	kubeletPluginPath         = "/var/lib/kubelet/plugins"
)

const (
	// maxAttempts indicates the number of times the driver will try to recover itself before failing
	maxAttempts = 5
)

type NetworkDriver struct {
	driverName string
	nodeName   string
	kubeClient kubernetes.Interface
	draPlugin  *kubeletplugin.Helper
	nriPlugin  stub.Stub
}

type Option func(*NetworkDriver)

func Start(ctx context.Context, driverName string, kubeClient kubernetes.Interface, nodeName string, opts ...Option) (*NetworkDriver, error) {
	registerMetrics()

	plugin := &NetworkDriver{
		driverName: driverName,
		nodeName:   nodeName,
		kubeClient: kubeClient,
	}

	for _, o := range opts {
		o(plugin)
	}

	driverPluginPath := filepath.Join(kubeletPluginPath, driverName)
	err := os.MkdirAll(driverPluginPath, 0750)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %v", driverPluginPath, err)
	}

	kubeletOpts := []kubeletplugin.Option{
		kubeletplugin.DriverName(driverName),
		kubeletplugin.NodeName(nodeName),
		kubeletplugin.KubeClient(kubeClient),
	}
	d, err := kubeletplugin.Start(ctx, plugin, kubeletOpts...)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	plugin.draPlugin = d
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := plugin.draPlugin.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return nil, err
	}

	// register the NRI plugin
	nriOpts := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			klog.Infof("%s NRI plugin closed", driverName)
		}),
	}
	stub, err := stub.New(plugin, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %v", err)
	}
	plugin.nriPlugin = stub

	go func() {
		for i := 0; i < maxAttempts; i++ {
			err = plugin.nriPlugin.Run(ctx)
			if err != nil {
				klog.Infof("NRI plugin failed with error %v", err)
			}
			select {
			case <-ctx.Done():
				return
			default:
				klog.Infof("Restarting NRI plugin %d out of %d", i, maxAttempts)
			}
		}
		klog.Fatalf("NRI plugin failed for %d times to be restarted", maxAttempts)
	}()

	// publish available resources
	go plugin.PublishResources(ctx)

	return plugin, nil
}

func (np *NetworkDriver) Stop() {
	np.nriPlugin.Stop()
	np.draPlugin.Stop()
}

func (np *NetworkDriver) Shutdown(_ context.Context) {
	klog.Info("Runtime shutting down...")
}

func (np *NetworkDriver) PublishResources(ctx context.Context) {
	klog.V(2).Infof("Publishing resources")
	for {
		select {
		case <-ctx.Done():
			klog.Error(ctx.Err(), "context canceled")
			return
		default:
		}

		devices := []resourceapi.Device{}
		resources := resourceslice.DriverResources{
			Pools: map[string]resourceslice.Pool{
				np.nodeName: {Slices: []resourceslice.Slice{{Devices: devices}}}},
		}
		err := np.draPlugin.PublishResources(ctx, resources)
		if err != nil {
			klog.Error(err, "unexpected error trying to publish resources")
		}

		// poor man rate limit
		time.Sleep(3 * time.Second)
	}
}

func (np *NetworkDriver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	klog.V(2).Infof("PrepareResourceClaims is called: number of claims: %d", len(claims))

	nodePrepareRequestsTotal.Inc()

	if len(claims) == 0 {
		return nil, nil
	}
	result := make(map[types.UID]kubeletplugin.PrepareResult)

	for _, claim := range claims {
		klog.V(2).Infof("NodePrepareResources: Claim Request %s/%s", claim.Namespace, claim.Name)
		result[claim.UID] = np.prepareResourceClaim(ctx, claim)
	}
	return result, nil
}

// prepareResourceClaim gets all the configuration required to be applied at runtime and passes it downs to the handlers.
// This happens in the kubelet so it can be a "slow" operation, so we can execute fast in RunPodsandbox, that happens in the
// container runtime and has strong expectactions to be executed fast (default hook timeout is 2 seconds).
func (np *NetworkDriver) prepareResourceClaim(ctx context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	klog.V(2).Infof("PrepareResourceClaim Claim %s/%s", claim.Namespace, claim.Name)
	start := time.Now()
	defer func() {
		klog.V(2).Infof("PrepareResourceClaim Claim %s/%s  took %v", claim.Namespace, claim.Name, time.Since(start))
	}()
	// TODO: shared devices may allocate the same device to multiple pods, i.e. macvlan, ipvlan, ...
	podUIDs := []types.UID{}
	for _, reserved := range claim.Status.ReservedFor {
		if reserved.Resource != "pods" || reserved.APIGroup != "" {
			klog.Infof("Driver only supports Pods, unsupported reference %#v", reserved)
			continue
		}
		podUIDs = append(podUIDs, reserved.UID)
	}
	if len(podUIDs) == 0 {
		klog.Infof("no pods allocated to claim %s/%s", claim.Namespace, claim.Name)
		return kubeletplugin.PrepareResult{}
	}

	var errorList []error

	if len(errorList) > 0 {
		klog.Infof("claim %s contain errors: %v", claim.UID, errors.Join(errorList...))
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s contain errors: %w", claim.UID, errors.Join(errorList...)),
		}
	}
	return kubeletplugin.PrepareResult{}
}

func (np *NetworkDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.V(2).Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))
	if len(claims) == 0 {
		return nil, nil
	}

	result := make(map[types.UID]error)
	for _, claim := range claims {
		err := np.unprepareResourceClaim(ctx, claim)
		result[claim.UID] = err
		if err != nil {
			klog.Infof("error unpreparing ressources for claim %s/%s : %v", claim.Namespace, claim.Name, err)
		}
	}
	return result, nil
}

func (np *NetworkDriver) unprepareResourceClaim(_ context.Context, claim kubeletplugin.NamespacedObject) error {
	return nil
}

// NRI hooks into the container runtime, the lifecycle of the Pod seen here is local to the runtime
// and is not the same as the Pod lifecycle for kubernetes, per example, a Pod that can fail to start
// is retried locally multiple times, so the hooks need to be idempotent to all operations on the Pod.
// The NRI hooks are time sensitive, any slow operation needs to be added on the DRA hooks and only
// the information necessary should passed to the NRI hooks via the np.podConfigStore so it can be executed
// quickly.

func (np *NetworkDriver) Synchronize(_ context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("Synchronized state with the runtime (%d pods, %d containers)...",
		len(pods), len(containers))

	for _, pod := range pods {
		klog.Infof("Synchronize Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
		klog.V(2).Infof("pod %s/%s: namespace=%s ips=%v", pod.GetNamespace(), pod.GetName(), getNetworkNamespace(pod), pod.GetIps())
		// get the pod network namespace
		ns := getNetworkNamespace(pod)
		// host network pods are skipped
		if ns != "" {
			// store the Pod metadata in the db
		}
	}

	return nil, nil
}

// CreateContainer handles container creation requests.
func (np *NetworkDriver) CreateContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	klog.V(2).Infof("CreateContainer Pod %s/%s UID %s Container %s", pod.Namespace, pod.Name, pod.Uid, ctr.Name)

	return nil, nil, nil
}

func (np *NetworkDriver) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("RunPodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	start := time.Now()
	defer func() {
		klog.V(2).Infof("RunPodSandbox Pod %s/%s UID %s took %v", pod.Namespace, pod.Name, pod.Uid, time.Since(start))
	}()
	ns := getNetworkNamespace(pod)
	// host network pods can not allocate network devices because it impact the host
	if ns == "" {
		return fmt.Errorf("RunPodSandbox pod %s/%s using host network can not claim host devices", pod.Namespace, pod.Name)
	}

	return nil
}

// StopPodSandbox tries to move back the devices to the rootnamespace but does not fail
// to avoid disrupting the pod shutdown. The kernel will do the cleanup once the namespace
// is deleted.
func (np *NetworkDriver) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("StopPodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	start := time.Now()
	defer func() {
		klog.V(2).Infof("StopPodSandbox Pod %s/%s UID %s took %v", pod.Namespace, pod.Name, pod.Uid, time.Since(start))
	}()
	// get the pod network namespace
	ns := getNetworkNamespace(pod)
	if ns == "" {
		// some version of containerd does not send the network namespace information on this hook so
		// we workaround it using the local copy we have in the db to associate interfaces with Pods via
		// the network namespace id.
	}

	return nil
}

func (np *NetworkDriver) RemovePodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.V(2).Infof("RemovePodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
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
