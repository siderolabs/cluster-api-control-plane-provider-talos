# cluster-api-control-plane-provider-talos

## Intro

The Cluster API Control Plane Provider Talos (CACPPT) is a project by [Talos Systems](https://www.talos-systems.com/) that provides a [Cluster API](https://github.com/kubernetes-sigs/cluster-api)(CAPI) control plane provider for use in deploying Talos-based Kubernetes nodes across any environment.
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

|                                              | v0.11 | v 0.12 | v0.13 |
| -------------------------------------------- | ----- | ------ | ----- |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      |       |
| Control Plane Provider Talos v1alpha3 (v0.3) | ✓     | ✓      | ✓     |
| Control Plane Provider Talos v1alpha3 (v0.4) | ✓     | ✓      | ✓     |

## Building and Installing

This control plane provider can be installed with clusterctl:

```bash
clusterctl init -c talos -b talos
```

This project can be built simply by running `make release` from the root directory.
Doing so will create a file called `_out/control-plane-components.yaml`.
If you wish, you can tweak settings by editing the release yaml.
This file can then be installed into your management cluster with `kubectl apply -f _out/control-plane-components.yaml`.

Note that CACPPT should be deployed as part of a set of controllers for Cluster API.
You will need at least the upstream CAPI components, the Talos bootstrap provider, and an infrastructure provider for v1alpha3 CAPI capabilities.

## Usage

You can use recommended [Cluster API templates](https://github.com/talos-systems/cluster-api-templates) provided by Sidero Labs.
It contains templates for `AWS` and `GCP`, which are verified by the integration tests.

If you are going to use this provider as part of Sidero management plane, please refer to [Sidero Docs](https://www.sidero.dev/docs/v0.4/getting-started/install-clusterapi/)
on how to install and configure it.
