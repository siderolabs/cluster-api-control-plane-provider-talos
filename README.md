# cluster-api-control-plane-provider-talos

## Intro

The Cluster API Control Plane Provider Talos (CACPPT) is a project by [Sidero Labs](https://www.SideroLabs.com/) that provides a [Cluster API](https://github.com/kubernetes-sigs/cluster-api)(CAPI) control plane provider for use in deploying Talos Linux-based Kubernetes nodes across any environment.
Given some basic info, this provider will generate control plane configurations for a given cluster and reconcile the necessary custom resources for CAPI to pick up the generated data.

## Corequisites

There are a few corequisites and assumptions that go into using this project:

- [Cluster API](https://github.com/kubernetes-sigs/cluster-api)
- [Cluster API Bootstrap Provider Talos](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos)
- [Cluster API Provider Metal](https://github.com/talos-systems/cluster-api-provider-metal) (optional)

## Compatibility with Cluster API and Kubernetes Versions

This provider's versions are compatible with the following versions of Cluster API:

|                                              | v1alpha3 (v0.3) | v1alpha4 (v0.4) | v1beta1 (v1.0) |
| -------------------------------------------- | --------------- | --------------- | -------------- |
| Control Plane Provider Talos v1alpha3 (v0.2) | ✓               |                 |                |
| Control Plane Provider Talos v1alpha3 (v0.3) |                 | ✓               |                |
| Control Plane Provider Talos v1alpha3 (v0.4) |                 |                 | ✓              |


This provider's versions are able to install and manage the following versions of Kubernetes:

|                                              | v1.16 | v 1.17 | v1.18 | v1.19 | v1.20 | v1.21 | v1.22 |
| -------------------------------------------  | ----- | ------ | ----- | ----- | ----- | ----- | ----- |
| Control Plane Provider Talos v1alpha3 (v0.2) | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     |       |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     |       |
| Control Plane Provider Talos v1alpha3 (v0.4) |       |        |       | ✓     | ✓     | ✓     | ✓     |

This provider's versions are compatible with the following versions of Talos:

|                                              | v0.11 | v 0.12 | v0.13 | v0.14 |
| -------------------------------------------- | ----- | ------ | ----- | ----- |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      |       |       |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      | ✓     |       |
| Control Plane Provider Talos v1alpha3 (v0.4) | ✓     | ✓      | ✓     | ✓     |

## Building and Installing

This control plane provider can be installed with clusterctl:

```bash
clusterctl init -c talos -b talos -i <infra-provider-of-choice>
```

If you are going to use this provider as part of Sidero management plane, please refer to [Sidero Docs](https://www.sidero.dev/docs/v0.4/getting-started/install-clusterapi/)
on how to install and configure it.

This project can be built simply by running `make release` from the root directory.
Doing so will create a file called `_out/control-plane-components.yaml`.
If you wish, you can tweak settings by editing the release yaml.
This file can then be installed into your management cluster with `kubectl apply -f _out/control-plane-components.yaml`.

Note that CACPPT should be deployed as part of a set of controllers for Cluster API.
You will need at least the upstream CAPI components, the Talos bootstrap provider, and an infrastructure provider for v1alpha3 CAPI capabilities.

## Usage

### Supported Templates

You can use recommended [Cluster API templates](https://github.com/talos-systems/cluster-api-templates) provided by Sidero Labs.

It contains templates for `AWS` and `GCP`, which are verified by the integration tests.

### Creating Your Own Templates

If you wish to craft your own manifests, here is some important info.

CACPPT supports a single API type, a TalosControlPlane.
You can create YAML definitions of a TalosControlPlane and `kubectl apply` them as part of a larger CAPI cluster deployment.
Below is a bare-minimum example.

A basic config:

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: TalosControlPlane
metadata:
  name: talos-cp
spec:
  version: v1.18.1
  replicas: 1
  infrastructureTemplate:
    kind: MetalMachineTemplate
    apiVersion: infrastructure.cluster.x-k8s.io/v1alpha3
    name: talos-cp
  controlPlaneConfig:
    controlplane:
      generateType: controlplane
```

Note you must provide an infrastructure template for your control plane.
See your infrastructure provider for how to craft that.

Note the generateType mentioned above.
This is a required value in the spec for both controlplane and worker ("join") nodes.
For a no-frills control plane config, you can simply specify `controlplane` depending on each config section.
When creating a TalosControlPlane this way, you can then retrieve the talosconfig file that allows for osctl interaction with your nodes by doing something like `kubectl get talosconfig -o yaml talos-cp-xxxx -o jsonpath='{.status.talosConfig}'` after creation.

If you wish to do something more complex, we allow for the ability to supply an entire Talos machine config file to the resource.
This can be done by setting the generateType to `none` and specifying a `data` field.
This config file can be generated with `talosctl config generate` and the edited to supply the various options you may desire.
This full config is blindly copied from the `data` section of the spec and presented under `.status.controlPlaneData` so that the upstream CAPI controllers can see it and make use.

An example of a more complex config:

```yaml
apiVersion: control-plane.cluster.x-k8s.io/v1alpha2
kind: TalosControlPlane
metadata:
  name: talos-0
  labels:
    cluster.x-k8s.io/cluster-name: talos
spec:
  controlPlaneConfig:
    init:
        generateType: none
        data: |
            version: v1alpha1
            machine:
            type: controlplane
            token: xxxxxx
            ...
            ...
            ...
  ...
  ...
```

Note that specifying the full config above removes the ability for our control plane provider to generate a talosconfig for use.
As such, you should keep track of the talosconfig that's generated when running `talosctl config generate`.
