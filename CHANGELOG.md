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
