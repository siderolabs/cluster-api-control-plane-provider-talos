#!/bin/bash

set -eou pipefail

TMP="/tmp/cacppt-e2e"
mkdir -p "${TMP}"

# OPENSTACK_CLOUD_YAML is expected to be the raw (not base64) contents of a
# clouds.yaml file granting access to an existing OpenStack cloud. It is used
# both to install CAPO in the management cluster and to build the workload
# cluster's cloud-config Secret.
OPENSTACK_CLOUD_YAML_PATH=${OPENSTACK_CLOUD_YAML_PATH:-}
OPENSTACK_CACERT_PATH=${OPENSTACK_CACERT_PATH:-}

if [[ -z ${OPENSTACK_CLOUD_YAML_B64:-} ]]; then
  if [[ -n ${OPENSTACK_CLOUD_YAML_PATH} && -f ${OPENSTACK_CLOUD_YAML_PATH} ]]; then
    OPENSTACK_CLOUD_YAML_B64=$(cat "${OPENSTACK_CLOUD_YAML_PATH}" | base64 -w0)
  elif [ -f ~/.config/openstack/clouds.yaml ]; then
    OPENSTACK_CLOUD_YAML_B64=$(cat ~/.config/openstack/clouds.yaml | base64 -w0)
  else
    echo "either OPENSTACK_CLOUD_YAML_B64 or OPENSTACK_CLOUD_YAML_PATH (pointing at a clouds.yaml) must be defined to run this test"

    exit 1
  fi
fi

if [[ -z ${OPENSTACK_CACERT_B64:-} ]]; then
  if [[ -n ${OPENSTACK_CACERT_PATH} && -f ${OPENSTACK_CACERT_PATH} ]]; then
    OPENSTACK_CACERT_B64=$(cat "${OPENSTACK_CACERT_PATH}" | base64 -w0)
  else
    # no private CA - empty cacert is fine, OpenStackCluster's identityRef
    # secret just won't populate the cacert key with anything meaningful.
    OPENSTACK_CACERT_B64=$(echo -n "" | base64 -w0)
  fi
fi

TAG="${TAG:-$(git describe --tag --always --dirty)}"
PLATFORM=$(uname -s | tr "[:upper:]" "[:lower:]")
TALOS_VERSION="${TALOS_DEFAULT:-v1.12.0}" # NOTE: this is Talos version for the test environment, not Talos version for CAPI templates (see capi-utils)
K8S_VERSION="${K8S_VERSION:-v1.34.3}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.35.0}"
KUBECONFIG=
# NOTE: pinned to v0.12.7 (last v0.12.x patch release) because this repo's CAPI core
# is v1.10.x. CAPO's compatibility matrix requires CAPI>=v1.9 for the v0.12 line
# (v0.13 requires CAPI>=v1.11, v0.14 requires CAPI>=v1.12 -- both too new). v0.12.7's
# OpenStackCluster/OpenStackMachineTemplate CRDs serve v1beta1, which the templates use.
export PROVIDER=openstack:v0.12.7

CREATED_CLUSTER=""
TALOSCTL_PATH="${TMP}/talosctl"
TALOSCTL="${TALOSCTL_PATH} --talosconfig=${TMP}/talosconfig"
KUSTOMIZE="${TMP}/kustomize"
TEARDOWN_CLUSTER=${TEARDOWN_CLUSTER:-true}
KUBECTL="${TMP}/kubectl"

curl -Lo ${KUBECTL} "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/${PLATFORM}/amd64/kubectl"
chmod +x ${KUBECTL}

cleanup() {
  if [ "$1" != "0" ]; then
    # gather container logs
    if [[ ! -z ${KUBECONFIG} ]]; then
      ${KUBECTL} delete cluster --all || true
      ${KUBECTL} logs -n capo-system deployment/capo-controller-manager manager || true
      ${KUBECTL} logs -n cacppt-system deployment/cacppt-controller-manager || true
    fi
  fi

  # delete deployed cluster
  if [[ ! -z ${CREATED_CLUSTER} ]] && [ "${TEARDOWN_CLUSTER}" = true ]; then
    echo "destroying deployed cluster"
    rm -rf ~/.talos/clusters/${CREATED_CLUSTER}
    ${TALOSCTL_PATH} cluster destroy --name=${CREATED_CLUSTER} || true
  fi

  if [ "${TEARDOWN_CLUSTER}" = true ]; then
    rm -rf ${TMP}
  fi
  trap - EXIT
}

trap 'cleanup $?' INT TERM EXIT

function build_registry_mirrors {
  if [[ "${CI:-false}" == "true" ]]; then
    REGISTRY_MIRROR_FLAGS=()

    for registry in docker.io registry.k8s.io quay.io gcr.io ghcr.io; do
      local service="registry-${registry//./-}.ci.svc"
      addr=$(python3 -c "import socket; print(socket.gethostbyname('${service}'))")

      REGISTRY_MIRROR_FLAGS+=("--registry-mirror=${registry}=http://${addr}:5000")
    done
  fi
}

function config {
  curl -Lo ${TMP}/kustomize.tar.gz https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.1.0/kustomize_v4.1.0_${PLATFORM}_amd64.tar.gz
  tar -xf ${TMP}/kustomize.tar.gz -C ${TMP} && rm ${TMP}/kustomize.tar.gz

  # always use fake version tag here
  export CONTROL_PLANE_PROVIDER_COMPONENTS=${TMP}/control-plane-talos/v0.4.0/control-plane-components.yaml
  mkdir -p $(dirname ${CONTROL_PLANE_PROVIDER_COMPONENTS})

  cp -rf config ${TMP}/config

  cd ${TMP}/config/manager
  ${KUSTOMIZE} edit set image controller=${REGISTRY_AND_USERNAME}/${NAME}:${TAG}
  cd -
  ${KUSTOMIZE} build ${TMP}/config/default >${CONTROL_PLANE_PROVIDER_COMPONENTS}
  cp ${TMP}/config/metadata/metadata.yaml ${TMP}/control-plane-talos/v0.4.0/
}

# the management cluster (running CAPI/CACPPT/CABPT/CAPO controllers) stays a
# local Docker-based Talos cluster - this is unaffected by the workload
# cluster's infrastructure provider and remains free / self-hosted-runner
# friendly.
function cluster {
  curl -Lo ${TALOSCTL_PATH} https://github.com/talos-systems/talos/releases/download/${TALOS_VERSION}/talosctl-${PLATFORM}-amd64

  chmod +x ${TALOSCTL_PATH}

  CREATED_CLUSTER="cacppt-test-$(echo $RANDOM | md5sum | head -c 10)"

  if [[ ! -f "${TMP}/kubeconfig" ]]; then
    echo "creating cluster ${CREATED_CLUSTER}"
    TAG="${TALOS_VERSION}" ${TALOSCTL_PATH} cluster create docker \
        --name=${CREATED_CLUSTER} \
        --talosconfig-destination=${TMP}/talosconfig \
        "${REGISTRY_MIRROR_FLAGS[@]}" \
        --kubernetes-version=${K8S_VERSION} \
        --config-patch-controlplanes '{"cluster": {"allowSchedulingOnControlPlanes": true}}' \
        --mtu=1450 \
        --memory-controlplanes=8GiB \
        --cpus-controlplanes=8 \
        --workers=0

    ${TALOSCTL} config nodes 10.5.0.2
    ${TALOSCTL} kubeconfig -f ${TMP}/kubeconfig
  fi

  export KUBECONFIG=${TMP}/kubeconfig
}

# openstack-resource-controller (ORC) provides CRDs (e.g. Image.openstack.k-orc.cloud)
# that CAPO >=v0.12 watches for image resolution. It isn't a clusterctl provider,
# so `manager.Install()` (clusterctl-based) never installs it. Without these CRDs
# present *before* CAPO starts, its OpenStackServer controller fails to start an
# informer on the Image kind and never starts its reconcile workers -- meaning no
# OpenStack VM ever gets created, even though OpenStackCluster/network/LB resources
# reconcile fine. Install it here, directly into the management cluster, before CAPO
# gets installed by the test binary.
ORC_VERSION="${ORC_VERSION:-v2.6.0}"

function install_orc {
  if ${KUBECTL} get ns orc-system >/dev/null 2>&1; then
    echo "openstack-resource-controller already installed, skipping"
    return
  fi

  echo "installing openstack-resource-controller ${ORC_VERSION}"
  ${KUBECTL} apply --server-side -f "https://github.com/k-orc/openstack-resource-controller/releases/download/${ORC_VERSION}/install.yaml"

  ${KUBECTL} wait --for=condition=Available --timeout=120s -n orc-system deployment/orc-controller-manager
}

# openstack_setup configures the workload cluster's target: an existing
# OpenStack cloud reachable via clouds.yaml. Unlike AWS, no AMI lookup is
# needed here - OPENSTACK_IMAGE_NAME just names an already-imported Talos
# Glance image.
function openstack_setup {
  if [[ -z ${OPENSTACK_IMAGE_NAME:-} ]]; then
    echo "OPENSTACK_IMAGE_NAME (name of an existing Talos Glance image, e.g. imported from" \
         "https://factory.talos.dev/ openstack-amd64.raw.gz) must be defined to run this test"

    exit 1
  fi

  export NAMESPACE=default

  ## Cluster-wide vars
  export OPENSTACK_CLOUD=${OPENSTACK_CLOUD:-openstack}
  export OPENSTACK_CLOUD_YAML_B64
  export OPENSTACK_CLOUD_CACERT_B64=${OPENSTACK_CACERT_B64}
  export OPENSTACK_EXTERNAL_NETWORK_ID=${OPENSTACK_EXTERNAL_NETWORK_ID:-}
  export OPENSTACK_FAILURE_DOMAIN=${OPENSTACK_FAILURE_DOMAIN:-}
  export OPENSTACK_DNS_NAMESERVERS=${OPENSTACK_DNS_NAMESERVERS:-8.8.8.8}
  export OPENSTACK_NODE_CIDR=${OPENSTACK_NODE_CIDR:-10.6.0.0/24}

  ## Control plane / worker vars
  export OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR=${OPENSTACK_CONTROL_PLANE_MACHINE_FLAVOR:-m1.large}
  export OPENSTACK_NODE_MACHINE_FLAVOR=${OPENSTACK_NODE_MACHINE_FLAVOR:-m1.large}

  ## Optional Cinder-backed root volumes. Leaving the *_VOLUME_SIZE_GIB vars
  ## unset (the default) keeps machines on ephemeral (hypervisor-local)
  ## storage. Set e.g. OPENSTACK_CONTROL_PLANE_VOLUME_SIZE_GIB=15 and
  ## OPENSTACK_CONTROL_PLANE_VOLUME_TYPE=<cinder-backend-name> to boot control
  ## plane machines from a Cinder volume on that backend instead; same for
  ## workers via the OPENSTACK_NODE_VOLUME_* variables.
  export OPENSTACK_CONTROL_PLANE_VOLUME_TYPE=${OPENSTACK_CONTROL_PLANE_VOLUME_TYPE:-}
  export OPENSTACK_CONTROL_PLANE_VOLUME_SIZE_GIB=${OPENSTACK_CONTROL_PLANE_VOLUME_SIZE_GIB:-}
  export OPENSTACK_CONTROL_PLANE_VOLUME_AVAILABILITY_ZONE=${OPENSTACK_CONTROL_PLANE_VOLUME_AVAILABILITY_ZONE:-}
  export OPENSTACK_NODE_VOLUME_TYPE=${OPENSTACK_NODE_VOLUME_TYPE:-}
  export OPENSTACK_NODE_VOLUME_SIZE_GIB=${OPENSTACK_NODE_VOLUME_SIZE_GIB:-}
  export OPENSTACK_NODE_VOLUME_AVAILABILITY_ZONE=${OPENSTACK_NODE_VOLUME_AVAILABILITY_ZONE:-}
}

function tests {
  export WORKLOAD_TALOS_VERSION=${TALOS_VERSION}
  ./_out/integration.test -test.v
}

build_registry_mirrors
config
cluster
install_orc
openstack_setup
tests
