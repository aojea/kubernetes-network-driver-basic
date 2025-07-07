# kubernetes-network-driver-basic

A simple Kubernetes DRA Network Drivers that can be reused to implement more complex ones.

The repository contains the following golang files:

- `main.go`: flag parsing and initialization code
- `driver.go`: internal implementation details.
  - Initialize the dra and nri plugins
  - Hook on the Pod and DRA lifecycle and preprocess the ResourceClaims data to make it available to the developer hooks
  - Define the discovery logic to publish the resources/devices available on the Node.
  - Define the executiong logic on the Pod.

- `metrics.go`: define the metrics for the driver name.
  
In addition:

- `Makefile` to automate some common tasks.
- `Dockerfile` to build a container image with the driver code, use `make image` (the output image can be defined via env variable)
- `install.yaml` manifest to install the dra driver (it uses the default image name)
- `kind.yaml` allows to create a `KIND` cluster with the configuration required for DRA in Kubernetes 1.33
  - `make kind-image` builds an image with the latest code and loads into the `KIND` cluster.

## Anatomy of a Networking DRA Driver

The networking DRA drivers uses GRPC to communicate with:

- the Kubelet via the [DRA API](https://github.com/kubernetes/kubernetes/tree/master/staging/src/k8s.io/kubelet/pkg/apis/dra/v1alpha4)

- the Container Runtime via [NRI](https://github.com/containerd/nri).

This architecture facilitates the supportability and reduces the complexity of the existing solutions, without having to deploy a third component and is also fully compatible and agnostic of the existing CNI plugins in the cluster.

Networking DRA drivers authors need to define two business logic:

- publishing node network devices: discovery the local resources on the node that the driver should announce with its attributes and capabilities.

- attaching the network devices: the Network Driver, before the Pod start to be created, will receive a GRPC call from the Kubelet using the DRA API with the details of the request associated to a Pod via a ResourceClaim object. Once the Pod network namespaces has been created, the driver will receive a GRPC call from the Container Runtime via NRI to execute the corresponding configuration. A more detailed diagram can be found in:

### Pod creation

[![](https://mermaid.ink/img/pako:eNp9UstuwyAQ_JUVp1ZNfoBDpMi-WFXdyLn6gs0mQTXgLtCHovx714nTWoobDgiW2dlhNEfReo1CioDvCV2LuVF7UrZ2wEul6F2yDdLl_pwa7DAul6vVU4nx09Mb5NUacjIfSBJK5toQ9oqwwuATtRgeHi-9pY8InmEw1_naRGUcxAPCtTPrlLF8Y10hgnIaMu92Zj_S3ZAMqpajwvtSrt_gXzDlMBhJS6iS23i95UmN_7pi_wADf1YWEniDdZ6P72VxfpjwMEmxCXPts55VBRy8f5sff981xoMb605ZDL1qGd4jqWi8C_esmiqGG7FTK2eF_eNhRqgi_lbCjI1T6lu4WAiLZJXRHMrj0FwLToXFWkg-atyp1MVa1O7E0CGg22_XChkp4UKkXjPfmGEhd6oLXEVtoqeXS9DPeT_9ABUC_8M?type=png)](https://mermaid.live/edit#pako:eNp9UstuwyAQ_JUVp1ZNfoBDpMi-WFXdyLn6gs0mQTXgLtCHovx714nTWoobDgiW2dlhNEfReo1CioDvCV2LuVF7UrZ2wEul6F2yDdLl_pwa7DAul6vVU4nx09Mb5NUacjIfSBJK5toQ9oqwwuATtRgeHi-9pY8InmEw1_naRGUcxAPCtTPrlLF8Y10hgnIaMu92Zj_S3ZAMqpajwvtSrt_gXzDlMBhJS6iS23i95UmN_7pi_wADf1YWEniDdZ6P72VxfpjwMEmxCXPts55VBRy8f5sff981xoMb605ZDL1qGd4jqWi8C_esmiqGG7FTK2eF_eNhRqgi_lbCjI1T6lu4WAiLZJXRHMrj0FwLToXFWkg-atyp1MVa1O7E0CGg22_XChkp4UKkXjPfmGEhd6oLXEVtoqeXS9DPeT_9ABUC_8M)

### Pod deletion

TODO


## Dynamic Resource Allocation Feedback (before beta)

- [ ] Driver MUST be able to report Status on the ResourceClaim
  - [ ] Operation was successful or failed or ...
  - [ ] Metadata associated to the operation: IP addresses, ...
- [ ] Driver MUST not need to connect to the apiserver
  - Right now the Claim is fetched from the apiserver to deal with skew versions problems, this behavior is undesired as the Pod creation operation must be atomic with the Kubelet "state of the world" informarion, once the API get more stable it should not be needed to fetch.

## References

- [WG Device Management](https://github.com/kubernetes-sigs/wg-device-management)
- [Kubernetes Network Drivers](https://docs.google.com/presentation/d/1Vdr7BhbYXeWjwmLjGmqnUkvJr_eOUdU0x-JxfXWxUT8/edit?usp=sharing)
