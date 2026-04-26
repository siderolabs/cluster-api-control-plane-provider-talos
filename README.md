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

|                                              | v1alpha3 (v0.3) | v1alpha4 (v0.4) | v1beta1 (v1.x) | v1beta2 (v1.12+) |
| -------------------------------------------- | --------------- | --------------- | -------------- | ---------------- |
| Control Plane Provider Talos v1alpha3 (v0.2) | ✓               |                 |                |                  |
| Control Plane Provider Talos v1alpha3 (v0.3) |                 | ✓               |                |                  |
| Control Plane Provider Talos v1alpha3 (v0.4) |                 |                 | ✓              |                  |
| Control Plane Provider Talos v1alpha3 (v0.5) |                 |                 | ✓              |                  |
| Control Plane Provider Talos v1alpha3 (v0.6) |                 |                 |                | ✓                |

The `v0.6.x` release series targets the Cluster API `v1beta2` contract (CAPI core `v1.12+`, currently tested with `v1.13.0`).
Released `v0.5.x` artifacts remain on `v1beta1`; `config/metadata/metadata.yaml` advertises the `v0.6` series as `v1beta2`.

This provider's versions are able to install and manage the following versions of Kubernetes:

|                                              | v1.16 | v1.17 | v1.18 | v1.19 | v1.20 | v1.21 | v1.22 | v1.23 | v1.24 | v1.25 | v1.26 | v1.27 | v1.28 | v1.29 | v1.30 | v1.31 | v1.32 | v1.33 | v1.34 | v1.35 | v1.36 |
| -------------------------------------------  | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- |
| Control Plane Provider Talos v1alpha3 (v0.2) | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.4) |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.5) |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| Control Plane Provider Talos v1alpha3 (v0.6) |       |        |       |       |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |

This provider's versions are compatible with the following versions of Talos:

|                                              | v0.11 | v0.12  | v0.13 | v0.14 | v1.0  | v1.1  | v1.2  | v1.3  | v1.4  | v1.5  | v1.6  | v1.7  | v1.8  | v1.9  | v1.10 | v1.11 | v1.12 | v1.13 |
| -------------------------------------------- | ----- | ------ | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- | ----- |
| Control Plane Provider Talos v1alpha3 (v0.2) | ✓     | ✓      |       |       |       |       |       |       |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      | ✓     |       |       |       |       |       |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.4) | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |       |       |       |       |       |       |       |       |       |       |
| Control Plane Provider Talos v1alpha3 (v0.5) |       |        |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     | ✓     |
| Control Plane Provider Talos v1alpha3 (v0.6) |       |        |       |       |       |       |       |       |       |       |       |       |       |       | ✓     | ✓     | ✓     | ✓     | ✓     |

## Building and Installing

This control plane provider can be installed with clusterctl:

```bash
clusterctl init -c talos -b talos -i <infra-provider-of-choice>
```

If you encounter the following error, this is caused by a rename of our GitHub org from `talos-systems` to `siderolabs`.

```bash
$ clusterctl init -b talos -c talos -i sidero
Fetching providers
Error: failed to get provider components for the "talos" provider: target namespace can't be defaulted. Please specify a target namespace
```

This can be worked around by adding the following to `~/.cluster-api/clusterctl.yaml` and rerunning the init command:

```yaml
providers:
  - name: "talos"
    url: "https://github.com/siderolabs/cluster-api-bootstrap-provider-talos/releases/latest/bootstrap-components.yaml"
    type: "BootstrapProvider"
  - name: "talos"
    url: "https://github.com/siderolabs/cluster-api-control-plane-provider-talos/releases/latest/control-plane-components.yaml"
    type: "ControlPlaneProvider"
  - name: "sidero"
    url: "https://github.com/siderolabs/sidero/releases/latest/infrastructure-components.yaml"
    type: "InfrastructureProvider"
```

If you are going to use this provider as part of Sidero management plane, please refer to [Sidero Docs](https://www.sidero.dev/docs/v0.4/getting-started/install-clusterapi/)
on how to install and configure it.

This project can be built simply by running `make release` from the root directory.
Building requires Go 1.26+ (the `v0.6.x` series depends on CAPI `v1.13.0` / controller-runtime `v0.23`).
Doing so will create a file called `_out/control-plane-components.yaml`.
If you wish, you can tweak settings by editing the release yaml.
This file can then be installed into your management cluster with `kubectl apply -f _out/control-plane-components.yaml`.

Note that CACPPT should be deployed as part of a set of controllers for Cluster API.
You will need at least the upstream CAPI components, the Talos bootstrap provider, and an infrastructure provider for v1beta2 CAPI capabilities.

CACPPT plays the following role in the whole Cluster API architecture:

![Cluster API CACPPT](/docs/images/cacppt.png)

## Usage

### Supported Templates

You can use recommended [Cluster API templates](https://github.com/talos-systems/cluster-api-templates) provided by Sidero Labs.

It contains templates for `AWS` and `GCP`, which are verified by the integration tests.

### Creating Your Own Templates

If you wish to craft your own manifests, here is some important info.

CACPPT supports two API types:

- `TalosControlPlane` for direct control plane resources.
- `TalosControlPlaneTemplate` for ClusterClass / managed topology.

You can create YAML definitions of a `TalosControlPlane` and `kubectl apply` them as part of a larger CAPI cluster deployment.
Below is a bare-minimum example.

A basic config:

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: TalosControlPlane
metadata:
  name: talos-cp
spec:
  version: v1.31.0
  replicas: 1
  machineNamingStrategy:
    template: "{{ .talosControlPlane.name }}-{{ .random }}"
  machineTemplate:
    spec:
      infrastructureRef:
        apiGroup: infrastructure.cluster.x-k8s.io
        kind: DockerMachineTemplate
        name: talos-cp-machine-template
  controlPlaneConfig:
    controlplane:
      generateType: controlplane
      strategicPatches:
        - |
          machine:
            install:
              disk: /dev/sda
```

Direct `TalosControlPlane` resources must reference an infrastructure machine template via
`spec.machineTemplate.spec.infrastructureRef`.
See your infrastructure provider for how to craft the referenced machine template.

`spec.machineNamingStrategy` controls the names used for control plane `Machine` objects.
The corresponding infrastructure machine and Talos bootstrap config reuse the same name.
If you omit the field, the default template is `{{ .talosControlPlane.name }}-{{ .random }}`.
Custom templates must include `{{ .random }}` and can use:

- `.cluster.name`
- `.talosControlPlane.name`
- `.random`

`strategicPatches` is an array of strings.
Each patch must be passed as a YAML string, typically via a block scalar (`- |`), not as an inline object.

Note the generateType mentioned above.
This is a required value in the spec for both controlplane and worker ("join") nodes.
For a no-frills control plane config, you can simply specify `controlplane` depending on each config section.
When creating a `TalosControlPlane` this way, you can retrieve the generated Talos client config from the corresponding `TalosConfig` object after creation, for example with `kubectl get talosconfig talos-cp-xxxx -o jsonpath='{.status.talosConfig}'`.

If you wish to do something more complex, we allow for the ability to supply an entire Talos machine config file to the resource.
This can be done by setting `controlPlaneConfig.controlplane.generateType` to `none` and specifying a `data` field.
This config file can be generated with `talosctl gen config` and then edited to supply the various options you may desire.
When you provide `data` this way, the bootstrap provider uses the supplied Talos machine configuration as-is instead of generating one for you.

An example of a more complex config:

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: TalosControlPlane
metadata:
  name: talos-0
  labels:
    cluster.x-k8s.io/cluster-name: talos
spec:
  controlPlaneConfig:
    controlplane:
      generateType: none
      data: |
        version: v1alpha1
        machine:
          type: controlplane
        cluster:
          token: xxxxxx
        ...
```

When you manage the full machine configuration yourself, you should also keep track of the Talos client configuration you generated alongside it.


### ClusterClass / managed topology

For managed topology, define a `TalosControlPlaneTemplate` and reference it from a `ClusterClass`.
These examples use `cluster.x-k8s.io/v1beta2`, which matches the contract targeted by the `v0.6.x` release series.
The template example below intentionally omits `machineTemplate.spec.infrastructureRef`; for ClusterClass-managed topology, that reference comes from `ClusterClass.spec.controlPlane.machineInfrastructure.templateRef`.
`TalosControlPlaneTemplate.spec.template.spec` is immutable after creation, so create a new template resource when you need to roll out a spec change.

```yaml
apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
kind: TalosControlPlaneTemplate
metadata:
  name: talos-cp-template
spec:
  template:
    spec:
      machineNamingStrategy:
        template: "{{ .talosControlPlane.name }}-{{ .random }}"
      machineTemplate:
        metadata:
          labels:
            example.siderolabs.dev/control-plane: "true"
      controlPlaneConfig:
        controlplane:
          generateType: controlplane
          strategicPatches:
            - |
              machine:
                install:
                  disk: /dev/sda
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: ClusterClass
metadata:
  name: talos-quickstart
spec:
  controlPlane:
    naming:
      template: "{{ .cluster.name }}-control-plane"
    templateRef:
      apiVersion: controlplane.cluster.x-k8s.io/v1alpha3
      kind: TalosControlPlaneTemplate
      name: talos-cp-template
    machineInfrastructure:
      templateRef:
        apiVersion: infrastructure.cluster.x-k8s.io/v1beta2
        kind: DockerMachineTemplate
        name: talos-cp-machine-template
---
apiVersion: cluster.x-k8s.io/v1beta2
kind: Cluster
metadata:
  name: talos-topology
spec:
  topology:
    classRef:
      name: talos-quickstart
    version: v1.31.0
    controlPlane:
      replicas: 3
```

For ClusterClass / topology, the infrastructure machine template is supplied via
`ClusterClass.spec.controlPlane.machineInfrastructure.templateRef`, and the topology controller
populates `TalosControlPlane.spec.machineTemplate.spec.infrastructureRef` on the generated
concrete control plane object.

For naming in managed topology there are two levels:

- `ClusterClass.spec.controlPlane.naming.template` controls the generated `TalosControlPlane` name.
- `TalosControlPlaneTemplate.spec.template.spec.machineNamingStrategy` controls the generated control plane `Machine`, infrastructure machine, and Talos bootstrap config names.

With the example above, a cluster named `talos-topology` produces a concrete `TalosControlPlane`
named `talos-topology-control-plane`, and control plane machines named
`talos-topology-control-plane-xxxxx`.

See `config/samples/topology_v1alpha3_clusterclass_with_taloscontrolplanetemplate.yaml` for a ClusterClass / Cluster fragment including workers.
That sample assumes the referenced infrastructure and worker bootstrap templates already exist.
