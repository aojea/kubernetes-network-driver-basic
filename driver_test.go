package main

import (
	"context"
	"testing"

	"github.com/containerd/nri/pkg/api"
	resourceapi "k8s.io/api/resource/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

func TestNetworkDriver_shouldSkipInterface(t *testing.T) {
	driver := &NetworkDriver{}

	tests := []struct {
		name  string
		iface string
		want  bool
	}{
		{"skip docker interface", "docker0", true},
		{"skip bridge interface", "br-12345", true},
		{"skip veth interface", "veth1234", true},
		{"skip cni interface", "cni0", true},
		{"skip flannel interface", "flannel.1", true},
		{"skip weave interface", "weave", true},
		{"skip calico interface", "cali1234", true},
		{"skip kube interface", "kube-bridge", true},
		{"skip virbr interface", "virbr0", true},
		{"skip vmnet interface", "vmnet8", true},
		{"keep physical interface", "eth0", false},
		{"keep wireless interface", "wlan0", false},
		{"keep enp interface", "enp0s3", false},
		{"keep physical interface with number", "ens33", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := driver.shouldSkipInterface(tt.iface); got != tt.want {
				t.Errorf("shouldSkipInterface() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNetworkDriver_prepareResourceClaim(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name    string
		claim   *resourceapi.ResourceClaim
		wantErr bool
	}{
		{
			name: "valid claim with pod reservation",
			claim: &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim",
					Namespace: "default",
					UID:       "claim-uid-123",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{
							Resource: "pods",
							APIGroup: "",
							UID:      "pod-uid-123",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "claim with no pod reservations",
			claim: &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim-no-pods",
					Namespace: "default",
					UID:       "claim-uid-456",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{},
				},
			},
			wantErr: false,
		},
		{
			name: "claim with unsupported resource type",
			claim: &resourceapi.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-claim-unsupported",
					Namespace: "default",
					UID:       "claim-uid-789",
				},
				Status: resourceapi.ResourceClaimStatus{
					ReservedFor: []resourceapi.ResourceClaimConsumerReference{
						{
							Resource: "deployments",
							APIGroup: "apps",
							UID:      "deployment-uid-123",
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			result := driver.prepareResourceClaim(ctx, tt.claim)

			if (result.Err != nil) != tt.wantErr {
				t.Errorf("prepareResourceClaim() error = %v, wantErr %v", result.Err, tt.wantErr)
			}
		})
	}
}

func TestNetworkDriver_PrepareResourceClaims(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name    string
		claims  []*resourceapi.ResourceClaim
		wantLen int
		wantErr bool
	}{
		{
			name:    "empty claims",
			claims:  []*resourceapi.ResourceClaim{},
			wantLen: 0,
			wantErr: false,
		},
		{
			name: "single claim",
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-claim",
						Namespace: "default",
						UID:       "claim-uid-123",
					},
				},
			},
			wantLen: 1,
			wantErr: false,
		},
		{
			name: "multiple claims",
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-claim-1",
						Namespace: "default",
						UID:       "claim-uid-123",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-claim-2",
						Namespace: "default",
						UID:       "claim-uid-456",
					},
				},
			},
			wantLen: 2,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			result, err := driver.PrepareResourceClaims(ctx, tt.claims)

			if (err != nil) != tt.wantErr {
				t.Errorf("PrepareResourceClaims() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantLen == 0 && result != nil {
				t.Errorf("PrepareResourceClaims() expected nil result for empty claims, got %v", result)
				return
			}

			if tt.wantLen > 0 && len(result) != tt.wantLen {
				t.Errorf("PrepareResourceClaims() result length = %v, want %v", len(result), tt.wantLen)
			}
		})
	}
}

func TestNetworkDriver_UnprepareResourceClaims(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name    string
		claims  []kubeletplugin.NamespacedObject
		wantLen int
		wantErr bool
	}{
		{
			name:    "empty claims",
			claims:  []kubeletplugin.NamespacedObject{},
			wantLen: 0,
			wantErr: false,
		},
		{
			name: "single claim",
			claims: []kubeletplugin.NamespacedObject{
				{
					UID: "claim-uid-123",
				},
			},
			wantLen: 1,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			result, err := driver.UnprepareResourceClaims(ctx, tt.claims)

			if (err != nil) != tt.wantErr {
				t.Errorf("UnprepareResourceClaims() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantLen == 0 && result != nil {
				t.Errorf("UnprepareResourceClaims() expected nil result for empty claims, got %v", result)
				return
			}

			if tt.wantLen > 0 && len(result) != tt.wantLen {
				t.Errorf("UnprepareResourceClaims() result length = %v, want %v", len(result), tt.wantLen)
			}
		})
	}
}

func TestNetworkDriver_Synchronize(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name       string
		pods       []*api.PodSandbox
		containers []*api.Container
		wantErr    bool
	}{
		{
			name:       "empty pods and containers",
			pods:       []*api.PodSandbox{},
			containers: []*api.Container{},
			wantErr:    false,
		},
		{
			name: "single pod",
			pods: []*api.PodSandbox{
				{
					Id:        "pod-123",
					Name:      "test-pod",
					Namespace: "default",
					Uid:       "pod-uid-123",
					Linux: &api.LinuxPodSandbox{
						Namespaces: []*api.LinuxNamespace{
							{
								Type: "network",
								Path: "/proc/123/ns/net",
							},
						},
					},
				},
			},
			containers: []*api.Container{},
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			updates, err := driver.Synchronize(ctx, tt.pods, tt.containers)

			if (err != nil) != tt.wantErr {
				t.Errorf("Synchronize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if updates != nil {
				t.Errorf("Synchronize() expected nil updates, got %v", updates)
			}
		})
	}
}

func TestNetworkDriver_RunPodSandbox(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	tests := []struct {
		name    string
		pod     *api.PodSandbox
		wantErr bool
	}{
		{
			name: "pod with network namespace",
			pod: &api.PodSandbox{
				Id:        "pod-123",
				Name:      "test-pod",
				Namespace: "default",
				Uid:       "pod-uid-123",
				Linux: &api.LinuxPodSandbox{
					Namespaces: []*api.LinuxNamespace{
						{
							Type: "network",
							Path: "/proc/123/ns/net",
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "pod with host network",
			pod: &api.PodSandbox{
				Id:        "pod-456",
				Name:      "host-pod",
				Namespace: "default",
				Uid:       "pod-uid-456",
				Linux: &api.LinuxPodSandbox{
					Namespaces: []*api.LinuxNamespace{},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			err := driver.RunPodSandbox(ctx, tt.pod)

			if (err != nil) != tt.wantErr {
				t.Errorf("RunPodSandbox() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNetworkDriver_CreateContainer(t *testing.T) {
	driver := &NetworkDriver{
		driverName: "test-driver",
		nodeName:   "test-node",
		kubeClient: fake.NewSimpleClientset(),
	}

	pod := &api.PodSandbox{
		Id:        "pod-123",
		Name:      "test-pod",
		Namespace: "default",
		Uid:       "pod-uid-123",
	}

	container := &api.Container{
		Id:   "container-123",
		Name: "test-container",
	}

	ctx := context.Background()
	adjustment, updates, err := driver.CreateContainer(ctx, pod, container)

	if err != nil {
		t.Errorf("CreateContainer() error = %v, want nil", err)
	}

	if adjustment != nil {
		t.Errorf("CreateContainer() adjustment = %v, want nil", adjustment)
	}

	if updates != nil {
		t.Errorf("CreateContainer() updates = %v, want nil", updates)
	}
}

func TestGetNetworkNamespace(t *testing.T) {
	tests := []struct {
		name string
		pod  *api.PodSandbox
		want string
	}{
		{
			name: "pod with network namespace",
			pod: &api.PodSandbox{
				Linux: &api.LinuxPodSandbox{
					Namespaces: []*api.LinuxNamespace{
						{
							Type: "network",
							Path: "/proc/123/ns/net",
						},
						{
							Type: "pid",
							Path: "/proc/123/ns/pid",
						},
					},
				},
			},
			want: "/proc/123/ns/net",
		},
		{
			name: "pod without network namespace",
			pod: &api.PodSandbox{
				Linux: &api.LinuxPodSandbox{
					Namespaces: []*api.LinuxNamespace{
						{
							Type: "pid",
							Path: "/proc/123/ns/pid",
						},
					},
				},
			},
			want: "",
		},
		{
			name: "pod with empty namespaces",
			pod: &api.PodSandbox{
				Linux: &api.LinuxPodSandbox{
					Namespaces: []*api.LinuxNamespace{},
				},
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := getNetworkNamespace(tt.pod); got != tt.want {
				t.Errorf("getNetworkNamespace() = %v, want %v", got, tt.want)
			}
		})
	}
}
