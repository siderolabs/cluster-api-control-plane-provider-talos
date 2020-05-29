# cluster-api-control-plane-provider-talos

## Intro

The Cluster API Control Plane Provider Talos (CACPPT) is a project by [Talos Systems](https://www.talos-systems.com/) that provides a [Cluster API](https://github.com/kubernetes-sigs/cluster-api)(CAPI) control plane provider for use in deploying Talos-based Kubernetes nodes across any environment.
Given some basic info, this provider will generate control plane configurations for a given cluster and reconcile the necessary custom resources for CAPI to pick up the generated data.

## Corequisites

There are a few corequisites and assumptions that go into using this project:

- [Cluster API](https://github.com/kubernetes-sigs/cluster-api)
- [Cluster API Bootstrap Provider Talos](https://github.com/talos-systems/cluster-api-bootstrap-provider-talos)
- [Cluster API Provider Metal](https://github.com/talos-systems/cluster-api-provider-metal) (optional)

## Building and Installing

This control plane provider can be installed with clusterctl.
Add the following to your clusterctl.yaml:

```yaml
providers:
  - name: "talos"
    url: "https://github.com/talos-systems/cluster-api-bootstrap-provider-talos/releases/latest/bootstrap-components.yaml"
    type: "BootstrapProvider"
  - name: "talos"
    url: "https://github.com/rsmitty/cluster-api-control-plane-provider-talos/releases/latest/controlplane-components.yaml"
    type: "ControlPlaneProvider"
```

You can then install with `clusterctl init --control-plane "talos" --bootstrap "talos" ...`.

This project can be built simply by running `make release` from the root directory.
Doing so will create a file called `_out/control-plane-components.yaml`.
If you wish, you can tweak settings by editing the release yaml.
This file can then be installed into your management cluster with `kubectl apply -f _out/control-plane-components.yaml`.

Note that CACPPT should be deployed as part of a set of controllers for Cluster API.
You will need at least the upstream CAPI components, the Talos bootstrap provider, and an infrastructure provider for v1alpha3 CAPI capabilities.

## Usage

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
    init:
      generateType: init
    controlplane:
      generateType: controlplane
```

Note you must provide an infrastructure template for your control plane.
See your infrastructure provider for how to craft that.

Note the generateType mentioned above.
This is a required value in the spec for both init and controlplane nodes.
For a no-frills control plane config, you can simply specify `init` or `controlplane` depending on each config section.
When creating a TalosControlPlane this way, you can then retrieve the talosconfig file that allows for osctl interaction with your nodes by doing something like `kubectl get talosconfig -o yaml talos-cp-xxxx -o jsonpath='{.status.talosConfig}'` after creation.

If you wish to do something more complex, we allow for the ability to supply an entire Talos config file to the resource.
This can be done by setting the generateType to `none` and specifying a `data` field.
This config file can be generated with `osctl config generate` and the edited to supply the various options you may desire.
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
            type: init
            token: xxxxxx
            ...
            ...
            ...
  ...
  ...
```

Note that specifying the full config above removes the ability for our control plane provider to generate a talosconfig for use.
As such, you should keep track of the talosconfig that's generated when running `osctl config generate`.
