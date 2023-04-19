## [CAPI Control Plane Provider Talos 0.5.0-alpha.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.5.0-alpha.0) (2023-04-19)

Welcome to the v0.5.0-alpha.0 release of CAPI Control Plane Provider Talos!  
*This is a pre-release of CAPI Control Plane Provider Talos*



Please try out the release binaries and report any issues at
https://github.com/talos-systems/cluster-api-control-plane-provider-talos/issues.

### Contributors

* Andrey Smirnov
* Artem Chernyshev
* Spencer Smith
* Benjamin Gentil
* Damiano Donati
* Gerard de Leeuw
* Noel Georgi
* Steve Francis
* i.kvasov

### Changes
<details><summary>31 commits</summary>
<p>

* [`feaa35f`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/feaa35f3f3aed7ece7a8ec3369e5739b1d74d9af) chore: bump deps, implement unit tests
* [`e9b6948`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/e9b694840da093249f139cba82ff536d28b7e5c7) fix: nil check replicas ptr before de-referencing
* [`b10e2e7`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/b10e2e771483ef374e539a0d69b3a2176cc6547b) fix: properly write desired replicas count in scale conditions
* [`4bdb103`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/4bdb10379ab2ef3e39a3397ace714beb68e4e991) feat: add Tilt support
* [`d105ecc`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/d105ecc9e9dd3862d2bb708df1d9fe9de60d8398) feat: update for Talos 1.3.0
* [`051fad9`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/051fad9cbed443dadc5adef8f364adb74e872183) fix: regenerate kubeconfig on expiration
* [`b5a5fc6`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/b5a5fc6e54d3da9ac9af55bfa556dd74b641553c) feat: update to Talos 1.2.0
* [`6fdde72`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/6fdde729b8551eef897756f42f9bfc0030855623) fix: use 'control-plane' Kubernetes node label
* [`ac90f86`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/ac90f865c3c21c71716a51c4be5138aeb86a813a) fix: stop reporting negative unavailable replicas
* [`678aad5`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/678aad59da46722d3676321d1e37e12484c79e1f) feat: introduce 'OnDelete' rollout strategy type
* [`f3ff7ad`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/f3ff7ad82e541aedea34b49cf29bae8b68938631) fix: fallback to ExternalIP for boostrap if no InternalIP is found
* [`d8b6d34`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/d8b6d3441b58a557fadc3a64e8e6fc44382272fd) feat: update CABPT to 0.5.4, Talos to 1.1.0
* [`86d8ebf`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/86d8ebf64b7b66da81edf3c2aeb2eb716b0f6c82) fix: tcp webhook resource name and version
* [`466b501`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/466b5011f1cec48514a2f2317c0de4dce8c007fd) docs: add top level CAPI diagram with CACPPT role in it
* [`3cdfa0e`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/3cdfa0eff73e7a23363699263bc276260255ea48) fix: mark control plane as initialized as soon as endpoints are ready
* [`04b0570`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/04b05702289ac99f72964f170f77e935ef0b7f3c) feat: support `TalosControlPlane` rolling upgrade
* [`40a0174`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/40a0174b19bbc3a84129636cb1e2fd2b43f85f77) fix: skip nodes with empty hostname on etcd audit
* [`f530a1e`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/f530a1ea27e1c84711444c11d341ab989ed36618) refactor: use cached client tracker in the provider
* [`e1bf749`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/e1bf7495f90de6723faf349754f194c4b7201b1a) feat: update for Talos 1.0
* [`7d43ba8`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/7d43ba8954f05cf1f4db4111031b6504c58ce47f) docs: add note for clusterctl rename bug
* [`7a0436d`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/7a0436d3e24b2241d048620fe10c1008d6906cc8) chore: rename github organization to siderolabs
* [`6f1b876`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/6f1b87654f67e0a4f4b68fafeac04cc481c2459b) docs: update README.md
* [`a0b8ea4`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/a0b8ea4e74575067047e32b1c4ec3149e8fd5daf) fix: get talosconfig from secrets instead of talosconfig resources
* [`d6d9c02`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/d6d9c022a5f9253f201e17717d1abc703076da19) chore: bump cert-manager to v1
* [`da3b925`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/da3b92568827f56894c7835817cabf5f2f97da2c) feat: update CABPT to 0.5.2
* [`6c6b810`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/6c6b810b5f6884e7aba7426677b3c967063087c5) fix: fall back to old scheme of getting talsoconfig for older templates
* [`f3cba54`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/f3cba54e1f3f62116e728023731e0f4a6ad5a6a1) refactor: change reconcile loop flow
* [`c2d7edf`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/c2d7edfa0b63ca222d38f7f0827a59229340a0ba) fix: avoid long backoff when trying to bootstrap the cluster
* [`698e669`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/698e6690ba8ca2a19b807ed4d451ab718ee13889) fix: patch the status and use APIReader to get resource
* [`e0041f6`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/e0041f6567a61a388f5e69769fe28924e4f4fe7f) fix: ensure that bootstrap is called only a single time
* [`65043b7`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/65043b7cf85407e9e4683dbda1d96083d9e1de45) test: update templates to v1beta1
</p>
</details>

### Dependency Changes

* **github.com/coreos/go-semver**                                 v0.3.0 -> v0.3.1
* **github.com/go-logr/logr**                                     v0.4.0 -> v1.2.3
* **github.com/gobuffalo/flect**                                  v1.0.2 **_new_**
* **github.com/onsi/gomega**                                      v1.16.0 -> v1.27.5
* **github.com/siderolabs/capi-utils**                            835519e95d9c **_new_**
* **github.com/siderolabs/cluster-api-bootstrap-provider-talos**  v0.5.6 **_new_**
* **github.com/siderolabs/crypto**                                v0.4.0 **_new_**
* **github.com/siderolabs/go-retry**                              v0.3.2 **_new_**
* **github.com/siderolabs/talos**                                 v1.3.5 **_new_**
* **github.com/siderolabs/talos/pkg/machinery**                   v1.4.0 **_new_**
* **github.com/stretchr/testify**                                 v1.7.0 -> v1.8.2
* **google.golang.org/grpc**                                      v1.41.0 -> v1.54.0
* **gopkg.in/typ.v4**                                             v4.2.0 **_new_**
* **gopkg.in/yaml.v3**                                            496545a6307b -> v3.0.1
* **k8s.io/api**                                                  v0.22.2 -> v0.26.1
* **k8s.io/apiextensions-apiserver**                              v0.26.1 **_new_**
* **k8s.io/apimachinery**                                         v0.22.2 -> v0.26.1
* **k8s.io/apiserver**                                            v0.22.2 -> v0.26.1
* **k8s.io/client-go**                                            v0.22.2 -> v0.26.1
* **k8s.io/klog/v2**                                              v2.90.1 **_new_**
* **k8s.io/utils**                                                cb0fa318a74b -> a36077c30491
* **sigs.k8s.io/cluster-api**                                     v1.0.0 -> v1.4.1
* **sigs.k8s.io/controller-runtime**                              v0.10.2 -> v0.14.6

Previous release can be found at [v0.4.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.4.0)

## [CAPI Control Plane Provider Talos 0.4.0-alpha.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.4.0-alpha.0) (2021-11-10)

Welcome to the v0.4.0-alpha.0 release of CAPI Control Plane Provider Talos!  
*This is a pre-release of CAPI Control Plane Provider Talos*



Please try out the release binaries and report any issues at
https://github.com/talos-systems/cluster-api-control-plane-provider-talos/issues.

### CAPI v1beta1

This release of CACPPT brings compatibility with CAPI v1beta1.


### Contributors

* Artem Chernyshev
* Andrey Smirnov
* Spencer Smith

### Changes
<details><summary>3 commits</summary>
<p>

* [`bbe8822`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/bbe88224359300a829aae84cd842d0be5ab7d372) release(v0.4.0-alpha.0): prepare release
* [`b8db449`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/b8db4492d55f910e8a7d2a3b69ab08740963683e) fix: properly pick talos client configuration
* [`61fb582`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/61fb5826391e4434b64619f0590683f7fa7b82b6) feat: support clusterapi v1beta1
</p>
</details>

### Changes from talos-systems/capi-utils
<details><summary>5 commits</summary>
<p>

* [`144451c`](https://github.com/talos-systems/capi-utils/commit/144451cdef39bf6aed0cf1395ff69f9ce0496243) feat: switch to CAPI v1beta1
* [`151aac2`](https://github.com/talos-systems/capi-utils/commit/151aac243655ecf5ac82fde99db1d11795f4c14c) fix: properly define calico version
* [`658f48a`](https://github.com/talos-systems/capi-utils/commit/658f48a2034f991278ba7eeebccb3519dc1ee30a) feat: support getting cluster template files by http urls
* [`e0cadf5`](https://github.com/talos-systems/capi-utils/commit/e0cadf51e3dec7f7af7acfc533233365e01860a1) feat: add method to fetch a k8s client
* [`b018ea2`](https://github.com/talos-systems/capi-utils/commit/b018ea29c13a09ae2fdb2a071c5b7c8bd626bb50) feat: add ability to pass custom `Proxy` implementation in clusterapi
</p>
</details>

### Changes from talos-systems/cluster-api-bootstrap-provider-talos
<details><summary>6 commits</summary>
<p>

* [`2a4115f`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/2a4115f1211a20e5058a7b0430c4dc4081acfcfe) release(v0.5.0-alpha.0): prepare release
* [`d124c07`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/d124c072c9db8d402b353a73646d2d197bae76a4) docs: update README with usage and compatibility matrix
* [`20792f3`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/20792f345b7ff3c8ffa9d65c9ca8dcab1932f49e) feat: generate talosconfig as a secret with proper endpoints
* [`abd206f`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/abd206fd8a98f5478f8ffd0f8686e32be3b7defe) feat: update to CAPI v1.0.x contract (v1beta1)
* [`b7faf9e`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/b7faf9e730b7c9f50ffa94be194ddcf908708a2c) feat: update Talos machinery to 0.13.0
* [`04742b9`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/04742b96bf757413c88d0f15bee91679644f0337) feat: import fixes/updates from kubeadm bootstrap provider
</p>
</details>

### Dependency Changes

* **github.com/onsi/gomega**                                         v1.15.0 -> v1.16.0
* **github.com/talos-systems/capi-utils**                            b2f8f83d3df6 -> 144451cdef39
* **github.com/talos-systems/cluster-api-bootstrap-provider-talos**  v0.4.0-alpha.0 -> v0.5.0-alpha.0
* **google.golang.org/grpc**                                         v1.40.0 -> v1.41.0
* **k8s.io/api**                                                     v0.22.1 -> v0.22.2
* **k8s.io/apimachinery**                                            v0.22.1 -> v0.22.2
* **k8s.io/apiserver**                                               v0.22.1 -> v0.22.2
* **k8s.io/client-go**                                               v0.22.1 -> v0.22.2
* **k8s.io/utils**                                                   bdf08cb9a70a -> cb0fa318a74b
* **sigs.k8s.io/cluster-api**                                        v0.4.3 -> v1.0.0
* **sigs.k8s.io/controller-runtime**                                 v0.9.7 -> v0.10.2

Previous release can be found at [v0.3.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.3.0)

## [CAPI Control Plane Provider Talos 0.3.0-alpha.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.3.0-alpha.0) (2021-10-01)

Welcome to the v0.3.0-alpha.0 release of CAPI Control Plane Provider Talos!  
*This is a pre-release of CAPI Control Plane Provider Talos*



Please try out the release binaries and report any issues at
https://github.com/talos-systems/cluster-api-control-plane-provider-talos/issues.

### CAPI v1alpha4

This release of CACPPT brings compatibility with CAPI v1alpha4.


### Contributors

* Andrey Smirnov
* Artem Chernyshev
* Gerard de Leeuw
* Spencer Smith

### Changes
<details><summary>1 commit</summary>
<p>

* [`48d834b`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/48d834b5dfb364b8e9ae2269771e41a2dc646692) feat: support CAPI v1alpha4
</p>
</details>

### Changes since v0.3.0
<details><summary>1 commit</summary>
<p>

* [`48d834b`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/48d834b5dfb364b8e9ae2269771e41a2dc646692) feat: support CAPI v1alpha4
</p>
</details>

### Changes from talos-systems/capi-utils
<details><summary>2 commits</summary>
<p>

* [`b2f8f83`](https://github.com/talos-systems/capi-utils/commit/b2f8f83d3df6a7cd0308ae724d7423280c6924a8) feat: update cluster API library to the latest version
* [`f2a34fd`](https://github.com/talos-systems/capi-utils/commit/f2a34fdddec066097e346c144bb8660398a5e69d) chore: do not rely on ENV variables to configure CAPI client
</p>
</details>

### Changes from talos-systems/cluster-api-bootstrap-provider-talos
<details><summary>5 commits</summary>
<p>

* [`548b7fb`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/548b7fbd38b89b9790a0daa2380fddb34157cdd5) release(v0.4.0-alpha.0): prepare release
* [`442ee41`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/442ee41bafb2a912e49928c5d61f52c4c61a2593) test: don't set the talosconfig owner ref to the machine
* [`8c7fec8`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/8c7fec8e373bd12609f6274d79ca07d187212d91) fix: don't write incomplete `<cluster>-ca` secret for configtype none
* [`f46c83d`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/f46c83d328ee44db2ccb5eef67b366cc73c13319) feat: bump Talos machinery to 0.12.3
* [`7b760cf`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/7b760cf69ecab93200821dded931171657a5dedc) feat: support CAPI v1alpha4
</p>
</details>

### Dependency Changes

* **github.com/onsi/gomega**                                         v1.14.0 -> v1.15.0
* **github.com/talos-systems/capi-utils**                            9587089e8425 -> b2f8f83d3df6
* **github.com/talos-systems/cluster-api-bootstrap-provider-talos**  v0.3.0 -> v0.4.0-alpha.0
* **github.com/talos-systems/talos/pkg/machinery**                   7e63e43eb399 -> v0.12.3
* **k8s.io/api**                                                     v0.17.9 -> v0.21.4
* **k8s.io/apimachinery**                                            v0.17.9 -> v0.21.4
* **k8s.io/apiserver**                                               v0.17.9 -> v0.21.4
* **k8s.io/client-go**                                               v0.17.9 -> v0.21.4
* **k8s.io/utils**                                                   6e3d28b6ed19 -> bdf08cb9a70a
* **sigs.k8s.io/cluster-api**                                        v0.3.23 -> v0.4.3
* **sigs.k8s.io/controller-runtime**                                 v0.5.14 -> v0.9.7

Previous release can be found at [v0.2.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.2.0)

## [CAPI Control Plane Provider Talos 0.2.0-alpha.0](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.2.0-alpha.0) (2021-09-23)

Welcome to the v0.2.0-alpha.0 release of CAPI Control Plane Provider Talos!  
*This is a pre-release of CAPI Control Plane Provider Talos*



Please try out the release binaries and report any issues at
https://github.com/talos-systems/cluster-api-control-plane-provider-talos/issues.

### CAPI v1alpha3

This release of CACPPT is compatible with CAPI v1alpha3 (v0.3.x).
Next release of CACPPT will bring compatibility with CAPI v1alpha4 (v0.4.x).


### Scaling Fixes

Control plane scaling up and down now runs slower but is more reliable.


### Contributors

* Artem Chernyshev
* Alexey Palazhchenko
* Andrey Smirnov
* Andrey Smirnov
* Spencer Smith
* Alexey Palazhchenko
* Andrey Smirnov
* Spencer Smith

### Changes
<details><summary>9 commits</summary>
<p>

* [`701511f`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/701511f2539bfb653e42295b08b05ddc49ae36b1) release(v0.2.0-alpha.0): prepare release
* [`8b52b8a`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/8b52b8addd9fa4235c542b0b8554a76f5c76a643) chore: update go to 1.17
* [`86d679a`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/86d679a44e543789474c0b8edaf435a764f7dd2e) chore: update cabpt to v0.3.0
* [`a616f4b`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/a616f4b4bd3b208595cd102eb9e32c8a31b95e18) test: add machine removal test
* [`6ad6aac`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/6ad6aac1315ad5bc8e1264af6162863418cdb280) test: implement scale up and down tests and fix found issues
* [`9435b12`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/9435b1247f010bee00b4a8e4dc592121a0eb2449) chore: add e2e test running on AWS infra
* [`4c7d42c`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/4c7d42caf79ca209f5cda84db2eb712433d3c68b) chore: update bootstrap provider
* [`119b969`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/119b969be2fe152a0e8a63d189563deed55110b4) fix: clean up couple small issues in the etcd member audit code
* [`9be7b88`](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/commit/9be7b88bf4a14aec584fe68561c3fda3fbeaf990) chore: update bootstrap provider to stable release
</p>
</details>

### Changes from talos-systems/capi-utils
<details><summary>15 commits</summary>
<p>

* [`9587089`](https://github.com/talos-systems/capi-utils/commit/9587089e8425e11ef34d00c33b38b1d3c1710b42) feat: add API method to get CAPI version
* [`3053852`](https://github.com/talos-systems/capi-utils/commit/3053852b107c9dd0a82340accc98b798abd6160c) chore: update go mod to remove requires
* [`2e0c2fe`](https://github.com/talos-systems/capi-utils/commit/2e0c2fe20b78c10d8af6f62662063ae2c41124c9) feat: allow for specifying namespace in infra providers
* [`e5fdc2a`](https://github.com/talos-systems/capi-utils/commit/e5fdc2a068ac8bfed8effdd33e717aa3f97a62a9) feat: enable builds of darwin/windows
* [`028c7d3`](https://github.com/talos-systems/capi-utils/commit/028c7d3c025764260fb37ae5618c59122027640d) fix: call sync until number of replicas != actual replicas
* [`0fbad9a`](https://github.com/talos-systems/capi-utils/commit/0fbad9a4d06661e7fc8816d98663a349f4bde936) fix: sync talos config and nodes list after scaling
* [`c1830ba`](https://github.com/talos-systems/capi-utils/commit/c1830ba4aada30e9b968b456620526bf35c73190) feat: support scaling cluster nodes up and down
* [`5e78193`](https://github.com/talos-systems/capi-utils/commit/5e78193aff23909ec8516fad8a02f077d57d5ad5) feat: add ability to detect CAPI version and installed infra providers
* [`c20b1a8`](https://github.com/talos-systems/capi-utils/commit/c20b1a80b4277c1729d0b5d4972aa2794203e83c) fix: do CAPI init once if several infra providers are defined
* [`83353b6`](https://github.com/talos-systems/capi-utils/commit/83353b6b16d0ebc813ac43f45f2a18a1f451e016) fix: remove lots of unused indirect dependencies
* [`9a6b78a`](https://github.com/talos-systems/capi-utils/commit/9a6b78a78edbbcb662f349ede1d66c0b1326a4d0) chore: move provider creation code to the common method
* [`c2adaee`](https://github.com/talos-systems/capi-utils/commit/c2adaee0629a0b73565a0a67ccb4b393c32f6063) feat: add `DestroyCluster` function
* [`81aabe0`](https://github.com/talos-systems/capi-utils/commit/81aabe04803fa529ce73c2dbf49dc3f83394c66d) feat: support bootstrapping AWS clusters
* [`64a30e7`](https://github.com/talos-systems/capi-utils/commit/64a30e7fcd5f6fc488f70b8f8b08548a1a959199) feat: add the code for bootstrapping CAPI using kubeconfig
* [`6f52762`](https://github.com/talos-systems/capi-utils/commit/6f527622e0ae356ddbc59622bd673a8071650304) Initial commit
</p>
</details>

### Changes from talos-systems/cluster-api-bootstrap-provider-talos
<details><summary>20 commits</summary>
<p>

* [`1122f4c`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/1122f4c4e47ccf1f83ebccf24daf98e9f2124335) release(v0.3.0): prepare release
* [`3147ba4`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/3147ba4fe57b88975133c598c226ff4e397efb44) release(v0.3.0-alpha.1): prepare release
* [`977121a`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/977121ad14dc0637f7c4282e69a4ee26e28372d4) fix: construct properly data secret name
* [`f8c75c8`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/f8c75c89c4653de30165fb1538e906256a4eec66) fix: update metadata.yaml for v0.3 of CABPT
* [`db60f9e`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/db60f9eb0697c4949be9c00cf8dc7787d383bad2) release(v0.3.0-alpha.0): prepare release
* [`755a2dd`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/755a2dd90c3668db89f8eae14f60db4564764475) fix: update Talos machinery to 0.12, fix secrets persistence
* [`f91b032`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/f91b032935776c1224f824cc860bfa4df5e220b1) fix: use bootstrap data secret names
* [`6bff239`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/6bff2393840655c2361def455b601511b86ba71f) chore: use Go 1.17
* [`56fb73b`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/56fb73b53f41b91b12ba2b3c331d7a04b7263a17) test: add test for the second machine
* [`e5b7738`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/e5b773833120fdd7ca4d57e0a0a4fe781495bf7e) test: add more tests
* [`bc4105d`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/bc4105d9e8366d4e840705a6cecfbc81bdcca00a) test: wait for CAPI availability
* [`c82b8ab`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/c82b8ab47bca5313cb96df1b70de0914da285331) chore: make versions configurable
* [`5594c96`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/5594c96daa55fb9fc9af585e8f2fc26551ce9bb5) chore: use codecov uploader from build-container
* [`cced038`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/cced038257d3eec5b7c48bc524de5165b5734496) chore: fix license headers
* [`7b5dc51`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/7b5dc51e83a54a1f5fa707c66a296ca9514c8722) chore: do not run tests on ARM
* [`d6258cf`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/d6258cf21778149a254d9669b03ac10bae9e0955) chore: improve tests runner
* [`c6ce363`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/c6ce36375ef145760647c632d64a9a3c93574e4b) chore: sign Drone CI configuration
* [`ad592d1`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/ad592d16fa8397f88a28e6a4151bc64b0a1c097d) chore: add basic integration test
* [`9fb0d07`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/9fb0d07ca4d2e8333b0b61ee0fe0ba3e6660489f) chore: add missing LICENSE file
* [`acf18d2`](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/commit/acf18d2bb09aab64687c1fccf1e628ef76e9cff8) chore: update machinery to v0.11.3
</p>
</details>

### Changes from talos-systems/go-retry
<details><summary>8 commits</summary>
<p>

* [`c78cc95`](https://github.com/talos-systems/go-retry/commit/c78cc953d9e95992575305b4e8648392c6c9b9e6) fix: implement `errors.Is` for all errors in the set
* [`7885e16`](https://github.com/talos-systems/go-retry/commit/7885e16b2cb0267bcc8b07cdd0eced14e8005864) feat: add ExpectedErrorf
* [`3d83f61`](https://github.com/talos-systems/go-retry/commit/3d83f6126c1a3a238d1d1d59bfb6273e4087bdac) feat: deprecate UnexpectedError
* [`b9dc1a9`](https://github.com/talos-systems/go-retry/commit/b9dc1a990133dd3399549b4ea199759bdfe58bb8) feat: add support for `context.Context` in Retry
* [`8c63d29`](https://github.com/talos-systems/go-retry/commit/8c63d290a6884095ea2e754c52e575603abe4bc0) fix: correctly implement error interfaces on wrapped errors
* [`752f081`](https://github.com/talos-systems/go-retry/commit/752f081252cfef6106151dc285fcbe4849ab0a0c) feat: add an option to log errors being retried
* [`073067b`](https://github.com/talos-systems/go-retry/commit/073067bd95a70e9b0a2a8d07d33311be69c24923) feat: copy initial version from talos-systems/talos
* [`c7968c5`](https://github.com/talos-systems/go-retry/commit/c7968c54b4b1743d14dedce51431bf6e79a67a4f) Initial commit
</p>
</details>

### Dependency Changes

* **github.com/coreos/go-semver**                                    v0.3.0 **_new_**
* **github.com/go-logr/logr**                                        v0.1.0 -> v0.4.0
* **github.com/google/uuid**                                         v1.1.2 **_new_**
* **github.com/onsi/ginkgo**                                         v1.15.0 -> v1.16.4
* **github.com/onsi/gomega**                                         v1.10.1 -> v1.14.0
* **github.com/stretchr/testify**                                    v1.7.0 **_new_**
* **github.com/talos-systems/capi-utils**                            9587089e8425 **_new_**
* **github.com/talos-systems/cluster-api-bootstrap-provider-talos**  v0.2.0 -> v0.3.0
* **github.com/talos-systems/go-retry**                              v0.3.1 **_new_**
* **github.com/talos-systems/talos/pkg/machinery**                   828772cec9a3 -> 7e63e43eb399
* **google.golang.org/grpc**                                         v1.40.0 **_new_**
* **gopkg.in/yaml.v3**                                               496545a6307b **_new_**
* **sigs.k8s.io/cluster-api**                                        v0.3.12 -> v0.3.23

Previous release can be found at [v0.1.1](https://github.com/talos-systems/cluster-api-control-plane-provider-talos/releases/tag/v0.1.1)


<a name="v0.1.0-alpha.13"></a>
## [v0.1.0-alpha.13](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.12...v0.1.0-alpha.13) (2021-05-14)

### Chore

* rework build, move to ghcr.io, build for arm64/amd64

### Fix

* back down resource requests


<a name="v0.1.0-alpha.12"></a>
## [v0.1.0-alpha.12](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.11...v0.1.0-alpha.12) (2021-02-18)

### Fix

* update resources for deployment
* use Talos API client correctly (wrapped version)

### Release

* **v0.1.0-alpha.12:** prepare release


<a name="v0.1.0-alpha.11"></a>
## [v0.1.0-alpha.11](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.10...v0.1.0-alpha.11) (2021-02-17)

### Feat

* support talosVersion in TalosControlPlane CRD

### Release

* **v0.1.0-alpha.11:** prepare release


<a name="v0.1.0-alpha.10"></a>
## [v0.1.0-alpha.10](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.9...v0.1.0-alpha.10) (2020-12-10)

### Fix

* update initialization for CAPI v0.3.11

### Release

* **v0.1.0-alpha.10:** prepare release


<a name="v0.1.0-alpha.9"></a>
## [v0.1.0-alpha.9](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.8...v0.1.0-alpha.9) (2020-12-03)

### Feat

* update imported bootstrap provider package

### Release

* **v0.1.0-alpha.9:** prepare release


<a name="v0.1.0-alpha.8"></a>
## [v0.1.0-alpha.8](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.7...v0.1.0-alpha.8) (2020-10-20)

### Chore

* update talos machinery v0.7.0-alpha.7

### Release

* **v0.1.0-alpha.8:** prepare release


<a name="v0.1.0-alpha.7"></a>
## [v0.1.0-alpha.7](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.6...v0.1.0-alpha.7) (2020-10-16)

### Fix

* address scale down issues

### Release

* **v0.1.0-alpha.7:** prepare release


<a name="v0.1.0-alpha.6"></a>
## [v0.1.0-alpha.6](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.5...v0.1.0-alpha.6) (2020-10-13)

### Fix

* ensure we handle finalizer removal properly

### Release

* **v0.1.0-alpha.6:** prepare release


<a name="v0.1.0-alpha.5"></a>
## [v0.1.0-alpha.5](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.4...v0.1.0-alpha.5) (2020-10-08)

### Chore

* update image pull policy

### Fix

* leave etcd on scale down
* remove requeue time
* scale down the control plane

### Release

* **v0.1.0-alpha.5:** prepare release


<a name="v0.1.0-alpha.4"></a>
## [v0.1.0-alpha.4](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.3...v0.1.0-alpha.4) (2020-09-23)

### Fix

* ensure talosconfigs are created w/o controller=true in ownerref

### Release

* **v0.1.0-alpha.4:** prepare release


<a name="v0.1.0-alpha.3"></a>
## [v0.1.0-alpha.3](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.2...v0.1.0-alpha.3) (2020-09-22)

### Fix

* ensure cleanup happens properly

### Release

* **v0.1.0-alpha.3:** prepare release


<a name="v0.1.0-alpha.2"></a>
## [v0.1.0-alpha.2](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.1...v0.1.0-alpha.2) (2020-07-21)

### Fix

* ensure we select a failure domain for clusters that support it

### Release

* **v0.1.0-alpha.2:** prepare release


<a name="v0.1.0-alpha.1"></a>
## [v0.1.0-alpha.1](https://github.com/talos-systems/talos/compare/v0.1.0-alpha.0...v0.1.0-alpha.1) (2020-07-17)

### Chore

* add readme info

### Fix

* ensure machines get proper labels
* ensure infra templates get labeled

### Release

* **v0.1.0-alpha.1:** prepare release
