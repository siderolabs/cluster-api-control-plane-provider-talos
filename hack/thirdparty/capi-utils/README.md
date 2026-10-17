# capi-utils (patched copy)

This is a trimmed, patched copy of [github.com/siderolabs/capi-utils](https://github.com/siderolabs/capi-utils)
at `v0.0.0-20251124160722-4ee8a1b7d4d0` (commit `4ee8a1b7d4d0`), the version required in the root `go.mod`.
The root `go.mod` uses it through `replace github.com/siderolabs/capi-utils => ./hack/thirdparty/capi-utils`.
Only the integration tests (`internal/integration`) import it.

Only the Go packages, `go.mod`, `go.sum` and `LICENSE` are kept. The upstream CLI (`cmd/`), build files and
CI configuration were removed. `go.mod` still lists the CLI dependencies, so that the root module graph does not change.

Remove this copy and the `replace` once the patches below are upstream.

## Local patches

- `go.mod`, `go.sum`: dependencies bumped to `sigs.k8s.io/cluster-api` v1.12.2, `sigs.k8s.io/controller-runtime` v0.22.5
  and `k8s.io/*` v0.34.3. The `github.com/google/cel-go` replace for kube-apiserver v0.32.3 was dropped.
- `pkg/capi/capi.go`, `pkg/capi/cluster.go`: `getRef` also accepts the CAPI v1beta2 `ContractVersionedObjectReference`
  (`name`, `kind`, `apiGroup`) in `spec.controlPlaneRef`. The namespace defaults to the namespace of the referring object,
  and the version is resolved through discovery (`preferredVersion`).
- `pkg/capi/capi.go`: `InstallCore` checks the clusterctl provider inventory (`providers.clusterctl.cluster.x-k8s.io`)
  for each requested core, bootstrap and control plane provider and passes only the missing ones to `clusterctl init`.
  Upstream skips the whole step as soon as the `capi-system` namespace exists. When waiting for providers is enabled, it
  also checks that the controller Deployments of the requested providers are available. `InstallProvider` skips
  infrastructure providers that are already in the inventory. This makes it safe to retry `Install` after a partially
  failed first install.
- `pkg/capi/check.go`: `CheckClusterReady` works with the CAPI v1beta2 contract:
  - It accepts the Cluster `Available` condition as well as `Ready`.
  - It requires the Cluster `ControlPlaneInitialized` and `ControlPlaneAvailable` conditions to be `True`.
  - It requires the control plane `status.initialization.controlPlaneInitialized` to be `true`.
  - It requires the control plane `status.replicas`, `status.availableReplicas` and `status.upToDateReplicas` to equal
    `spec.replicas`.

  The v1beta1 checks (`status.ready`, `status.initialized`, `readyReplicas == replicas`) are kept.
