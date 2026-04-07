#!/bin/bash

set -eou pipefail

TMP="/tmp/cacppt-e2e"
mkdir -p "${TMP}"

TAG="${TAG:-$(git describe --tag --always --dirty)}"
PLATFORM=$(uname -s | tr "[:upper:]" "[:lower:]")
TALOS_VERSION="${TALOS_DEFAULT:-v1.12.0}" # NOTE: this is Talos version for the test environment
K8S_VERSION="${K8S_VERSION:-v1.34.3}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.35.0}"
KUBECONFIG=
# Changement du provider AWS vers Docker (CAPD)
export PROVIDER=docker:v1.9.0

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
    echo "🚨 ÉCHEC DU TEST : Récupération de l'état du cluster 🚨"
    if [[ ! -z ${KUBECONFIG} ]]; then
      echo -e "\n--- ÉTAT DES PODS ---"
      ${KUBECTL} get pods -A || true
      
      echo -e "\n--- DÉTAILS DU POD CACCPT ---"
      ${KUBECTL} describe pods -n cacppt-system || true
      
      echo -e "\n--- DÉTAILS DU POD CAPD ---"
      ${KUBECTL} describe pods -n capd-system || true
      
      echo -e "\n--- ÉVÉNEMENTS (50 derniers) ---"
      ${KUBECTL} get events -A --sort-by='.metadata.creationTimestamp' | tail -n 50 || true
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

function docker_setup {
  # Pour Docker (CAPD), très peu de variables d'environnement externes sont nécessaires
  # contrairement à AWS. On s'assure juste du namespace.
  export NAMESPACE=default
}

function tests {
  export WORKLOAD_TALOS_VERSION=${TALOS_VERSION}
  ./_out/integration.test -test.v
}

build_registry_mirrors
config
cluster
docker_setup
tests