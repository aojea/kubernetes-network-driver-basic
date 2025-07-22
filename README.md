# kubernetes-network-driver-basic

A basic example implementation of a Kubernetes DRA (Dynamic Resource Allocation) Network Driver that can be used as a template for creating more sophisticated network drivers.

## Overview

This repository provides a complete, working example of a DRA network driver that discovers and manages network interfaces on a Kubernetes node. It demonstrates the integration between the DRA API and NRI (Node Resource Interface) to provide network devices to pods.

## Repository Structure

The repository contains the following golang files:

- `main.go`: Command-line flag parsing and driver initialization
- `driver.go`: Complete driver implementation including:
  - DRA plugin initialization and resource publishing
  - NRI plugin integration for runtime hooks
  - Pod lifecycle management and device allocation
  - Network interface discovery and filtering
  - Resource claim preparation and cleanup

Additional files:

- `driver_test.go`: Comprehensive unit tests for the driver implementation
- `metrics.go`: Prometheus metrics for monitoring driver operations
- `Makefile`: Automation for common development tasks
- `Dockerfile`: Container image build configuration
- `install.yaml`: Kubernetes manifest to deploy the driver as a DaemonSet
- `kind.yaml`: KIND cluster configuration with DRA support enabled

## Features

This example driver demonstrates:

### ✅ Network Device Discovery
- Automatic discovery of host network interfaces
- Filtering of virtual and container interfaces (docker, veth, cni, etc.)
- Device attribute extraction (MAC address, MTU, IP addresses, interface flags)
- Periodic resource publishing to the DRA framework

### ✅ Resource Management
- Resource claim preparation and validation
- Device allocation tracking per pod
- Cleanup on pod termination
- Thread-safe device configuration storage

### ✅ Pod Integration
- NRI hooks for container runtime integration
- Network namespace detection and validation
- Device configuration during pod startup
- Cleanup during pod shutdown

### ✅ Observability
- Comprehensive logging with different verbosity levels
- Prometheus metrics for monitoring
- Performance timing for operations
- Error tracking and reporting

## Usage as a Template

This implementation serves as a template for creating custom network drivers. To adapt it for your use case:

### 1. Modify Device Discovery
Update the `getNetworkDevices()` function to discover your specific network resources:

```go
func (nd *NetworkDriver) getNetworkDevices() ([]resourceapi.Device, error) {
    // Replace with your device discovery logic
    // Examples: SR-IOV VFs, DPDK devices, specialized NICs
}
```

### 2. Implement Device Configuration
Enhance the `configureDeviceForPod()` function to configure your devices:

```go
func (nd *NetworkDriver) configureDeviceForPod(device AllocatedDevice, networkNamespace string, podSandbox *api.PodSandbox) error {
    // Add your device configuration logic:
    // - Move devices to pod namespace
    // - Configure VLANs, VFs, or other features
    // - Set up device-specific networking
}
```

### 3. Customize Filtering
Modify the `shouldSkipInterface()` function for your device types:

```go
func (nd *NetworkDriver) shouldSkipInterface(name string) bool {
    // Add logic to identify your manageable devices
    // Return false for devices your driver should manage
}
```

### 4. Add Device-Specific Attributes
Extend the device attributes in `getNetworkDevices()`:

```go
device.Basic.Attributes["driver.mydriver/custom-attribute"] = resourceapi.DeviceAttribute{
    StringValue: &customValue,
}
```

## Architecture

The driver implements a dual-API architecture:

### DRA API Integration
- **Resource Publishing**: Periodically discovers and publishes available network devices
- **Claim Preparation**: Validates and prepares resource claims, storing device configuration
- **Claim Cleanup**: Removes device allocations when claims are deleted

### NRI Integration  
- **Pod Lifecycle**: Hooks into container runtime for fast device configuration
- **Network Namespace Management**: Configures devices in pod network namespaces
- **Runtime Cleanup**: Removes device configuration when pods terminate

## Quick Start

### 1. Build and Deploy
```bash
# Build container image
make image

# Deploy to Kubernetes cluster with DRA support
kubectl apply -f install.yaml
```

### 2. Create a ResourceClass
```yaml
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClass
metadata:
  name: network-devices
spec:
  driver: network.example.com
  parametersRef:
    apiVersion: resource.k8s.io/v1beta1
    kind: ResourceClassParameters
    name: basic-network-config
```

### 3. Use in a Pod
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: network-pod
spec:
  resourceClaims:
  - name: network-device
    resourceClaimTemplateName: network-claim-template
  containers:
  - name: app
    image: busybox
    command: ["sleep", "3600"]
---
apiVersion: resource.k8s.io/v1beta1
kind: ResourceClaimTemplate
metadata:
  name: network-claim-template
spec:
  spec:
    devices:
      requests:
      - name: network-device
        deviceClassName: network-devices
```

## Development

### Running Tests
```bash
# Run all tests
go test ./...

# Run with coverage
go test -cover ./...

# Run benchmarks
go test -bench=.
```

### Local Development with KIND
```bash
# Create KIND cluster with DRA support
make kind-cluster

# Build and load image into KIND
make kind-image

# Deploy driver
kubectl apply -f install.yaml
```

## Monitoring

The driver exposes Prometheus metrics at `/metrics`:

- `network_driver_node_prepare_requests_total`: Total number of prepare requests
- `network_driver_node_unprepare_requests_total`: Total number of unprepare requests  
- `network_driver_published_devices`: Number of currently published devices

## Requirements

- Kubernetes 1.31+ with DRA feature gate enabled
- Container runtime with NRI support (containerd, CRI-O)
- Go 1.21+ for development

## License

Apache License 2.0

## Contributing

This is an example implementation. For production use cases, consider:

- Adding comprehensive device validation
- Implementing device health monitoring  
- Adding support for device-specific configuration
- Enhancing error handling and recovery
- Adding integration tests with real workloads

Feel free to use this as a starting point for your own DRA network driver implementations!