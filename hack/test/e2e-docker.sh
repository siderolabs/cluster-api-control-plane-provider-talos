#!/bin/bash

# This Source Code Form is subject to the terms of the Mozilla Public
# License, v. 2.0. If a copy of the MPL was not distributed with this
# file, You can obtain one at http://mozilla.org/MPL/2.0/.

set -eou pipefail

TMP="/tmp/cacppt-e2e"
mkdir -p "${TMP}"

TAG="${TAG:-$(git describe --tag --always --dirty)}"
PLATFORM=$(uname -s | tr "[:upper:]" "[:lower:]")
TALOS_VERSION="${TALOS_DEFAULT:-v1.13.0-rc.0}" # NOTE: Talos version for the management cluster
K8S_VERSION="${K8S_VERSION:-v1.34.6}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.35.3}"
KUBECONFIG=

# CAPD — Cluster API Provider Docker (no cloud credentials needed)
export PROVIDER=docker:v1.12.2

# Defaults matching the Makefile so the script also works when invoked directly.
REGISTRY_AND_USERNAME="${REGISTRY_AND_USERNAME:-ghcr.io/siderolabs}"
NAME="${NAME:-cluster-api-control-plane-talos-controller}"

# Resource knobs — can be overridden for constrained environments.
CPUS_CONTROLPLANES="${CPUS_CONTROLPLANES:-8.0}"
MEMORY_CONTROLPLANES="${MEMORY_CONTROLPLANES:-8GiB}"

CREATED_CLUSTER=""
LOCAL_REGISTRY_CONTAINER="cacppt-e2e-registry"
LOCAL_REGISTRY_STARTED=false

TALOSCTL_PATH="${TMP}/talosctl"
# TALOSCTL is a bash array; declared globally for set -u safety,
# populated in cluster() once talosctl is downloaded.
declare -a TALOSCTL=()

KUSTOMIZE="${TMP}/kustomize"
TEARDOWN_CLUSTER="${TEARDOWN_CLUSTER:-true}"
KUBECTL="${TMP}/kubectl"

# Registry mirror flags — declared globally so set -u never fires on empty expansion.
declare -a REGISTRY_MIRROR_FLAGS=()

# ---------------------------------------------------------------------------
# Tooling bootstrap
# ---------------------------------------------------------------------------
curl -sSfLo "${KUBECTL}" \
  "https://dl.k8s.io/release/$(curl -sSfL https://dl.k8s.io/release/stable.txt)/bin/${PLATFORM}/amd64/kubectl"
chmod +x "${KUBECTL}"

# ---------------------------------------------------------------------------
# Cleanup
# ---------------------------------------------------------------------------
cleanup() {
  local exit_code="${1:-0}"

  if [[ "${exit_code}" != "0" ]]; then
    if [[ -n "${KUBECONFIG}" ]]; then
      "${KUBECTL}" delete cluster --all --ignore-not-found || true
      "${KUBECTL}" logs -n capd-system  deployment/capd-controller-manager  manager || true
      "${KUBECTL}" logs -n cacppt-system deployment/cacppt-controller-manager       || true
    fi
  fi

  if [[ -n "${CREATED_CLUSTER}" ]] && [[ "${TEARDOWN_CLUSTER}" == "true" ]]; then
    echo "Destroying management cluster ${CREATED_CLUSTER}..."
    rm -rf ~/.talos/clusters/"${CREATED_CLUSTER}"
    "${TALOSCTL_PATH}" cluster destroy --name="${CREATED_CLUSTER}" || true
  fi

  # Remove the ephemeral local registry only if this run started it.
  if [[ "${LOCAL_REGISTRY_STARTED}" == "true" ]]; then
    echo "Removing ephemeral local registry..."
    docker rm -f "${LOCAL_REGISTRY_CONTAINER}" 2>/dev/null || true
  fi

  if [[ "${TEARDOWN_CLUSTER}" == "true" ]]; then
    rm -rf "${TMP}"
  fi

  trap - EXIT
}

trap 'cleanup $?' INT TERM EXIT

# ---------------------------------------------------------------------------
# find_free_cidr — returns the first 10.X.0.0/24 not overlapping any
# existing Docker network.
# ---------------------------------------------------------------------------
function find_free_cidr {
  local used_cidrs
  used_cidrs=$(docker network ls -q \
    | xargs -r docker network inspect \
        --format '{{range .IPAM.Config}}{{.Subnet}}{{"\n"}}{{end}}' 2>/dev/null \
    | grep -v '^$' || true)

  for octet in $(seq 5 254); do
    local candidate="10.${octet}.0.0/24"
    local conflict=false

    while IFS= read -r used; do
      [[ -z "${used}" ]] && continue
      if python3 - <<EOF 2>/dev/null
import ipaddress, sys
a = ipaddress.ip_network('${candidate}', strict=False)
b = ipaddress.ip_network('${used}',      strict=False)
sys.exit(0 if a.overlaps(b) else 1)
EOF
      then
        conflict=true
        break
      fi
    done <<< "${used_cidrs}"

    if [[ "${conflict}" == "false" ]]; then
      echo "${candidate}"
      return 0
    fi
  done

  echo "ERROR: no free /24 subnet found in range 10.5-254.0.0/24" >&2
  exit 1
}

# ---------------------------------------------------------------------------
# build_registry_mirrors — populate REGISTRY_MIRROR_FLAGS for CI
# ---------------------------------------------------------------------------
function build_registry_mirrors {
  if [[ "${CI:-false}" == "true" ]]; then
    for registry in docker.io registry.k8s.io quay.io gcr.io ghcr.io; do
      local service="registry-${registry//./-}.ci.svc"
      local addr
      addr=$(python3 -c "import socket; print(socket.gethostbyname('${service}'))")
      REGISTRY_MIRROR_FLAGS+=("--registry-mirror=${registry}=http://${addr}:5000")
    done
  fi
}

# ---------------------------------------------------------------------------
# ensure_local_registry — local development only (CI skips this).
#
# Starts an ephemeral registry:2 container on port 5000, pushes the
# controller image to it, and adds a --registry-mirror flag so the Talos
# management cluster can pull the image without needing a remote registry.
#
# The gateway IP (first .1 address of the chosen subnet) is used as the
# registry address because Docker's -p 5000:5000 binds to all host
# interfaces, including the one that will be created for the new network.
# ---------------------------------------------------------------------------
function ensure_local_registry {
  local gateway_ip="$1"

  # In CI the image has already been pushed; nothing to do.
  if [[ "${CI:-false}" == "true" ]]; then
    return 0
  fi

  # Extract the registry hostname from REGISTRY_AND_USERNAME
  # e.g. "ghcr.io/siderolabs" → "ghcr.io"
  local registry_host
  registry_host=$(echo "${REGISTRY_AND_USERNAME}" | cut -d'/' -f1)

  # (Re-)start the ephemeral registry.
  # If the container already exists from a previous failed run, remove it first.
  if docker inspect "${LOCAL_REGISTRY_CONTAINER}" &>/dev/null; then
    echo "Removing stale local registry container..."
    docker rm -f "${LOCAL_REGISTRY_CONTAINER}" || true
  fi

  echo "Starting ephemeral local registry on port 5000..."
  docker run -d \
    --name "${LOCAL_REGISTRY_CONTAINER}" \
    -p 127.0.0.1:5000:5000 \
    registry:2
  LOCAL_REGISTRY_STARTED=true

  # Give the registry a moment to start.
  sleep 2

  # Tag the controller image and push to the local registry.
  local src_image="${REGISTRY_AND_USERNAME}/${NAME}:${TAG}"
  local dst_image="localhost:5000/${NAME}:${TAG}"

  echo "Pushing ${src_image} → ${dst_image}..."
  docker tag  "${src_image}" "${dst_image}"
  docker push "${dst_image}"

  # Tell the Talos cluster to mirror requests for the original registry
  # through the local registry, reachable at the subnet gateway.
  REGISTRY_MIRROR_FLAGS+=("--registry-mirror=${registry_host}=http://${gateway_ip}:5000")
  echo "Registry mirror configured: ${registry_host} → http://${gateway_ip}:5000"
}

# ---------------------------------------------------------------------------
# config — build the control-plane provider components via kustomize
# ---------------------------------------------------------------------------
function config {
  curl -sSfLo "${TMP}/kustomize.tar.gz" \
    "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.1.0/kustomize_v4.1.0_${PLATFORM}_amd64.tar.gz"
  tar -xf "${TMP}/kustomize.tar.gz" -C "${TMP}" && rm "${TMP}/kustomize.tar.gz"

  # Always use a pinned fake version tag so clusterctl can resolve the file.
  export CONTROL_PLANE_PROVIDER_COMPONENTS="${TMP}/control-plane-talos/v0.4.0/control-plane-components.yaml"
  mkdir -p "$(dirname "${CONTROL_PLANE_PROVIDER_COMPONENTS}")"

  cp -rf config "${TMP}/config"

  cd "${TMP}/config/manager"
  "${KUSTOMIZE}" edit set image "controller=${REGISTRY_AND_USERNAME}/${NAME}:${TAG}"
  cd -
  "${KUSTOMIZE}" build "${TMP}/config/default" > "${CONTROL_PLANE_PROVIDER_COMPONENTS}"
  cp "${TMP}/config/metadata/metadata.yaml" "${TMP}/control-plane-talos/v0.4.0/"
}

# ---------------------------------------------------------------------------
# cluster — create the Talos-on-Docker management cluster
# ---------------------------------------------------------------------------
function cluster {
  curl -sSfLo "${TALOSCTL_PATH}" \
    "https://github.com/siderolabs/talos/releases/download/${TALOS_VERSION}/talosctl-${PLATFORM}-amd64"
  chmod +x "${TALOSCTL_PATH}"

  # Populate the global TALOSCTL array now that the binary is present.
  TALOSCTL=("${TALOSCTL_PATH}" "--talosconfig=${TMP}/talosconfig")

  CREATED_CLUSTER="cacppt-test-$(echo $RANDOM | md5sum | head -c 10)"

  if [[ ! -f "${TMP}/kubeconfig" ]]; then
    # Resolve a non-overlapping subnet and derive IP addresses.
    local network_cidr node_ip gateway_ip
    network_cidr=$(find_free_cidr)
    node_ip=$(echo    "${network_cidr}" | sed 's|\.0/24|.2|')
    gateway_ip=$(echo "${network_cidr}" | sed 's|\.0/24|.1|')
    echo "Network CIDR: ${network_cidr}  node: ${node_ip}  gateway: ${gateway_ip}"

    # For local dev: start the ephemeral registry and add the mirror flag.
    ensure_local_registry "${gateway_ip}"

    echo "Creating management cluster ${CREATED_CLUSTER} (Docker/Talos)..."
    TAG="${TALOS_VERSION}" "${TALOSCTL_PATH}" cluster create docker \
        --name="${CREATED_CLUSTER}" \
        --talosconfig-destination="${TMP}/talosconfig" \
        --kubernetes-version="${K8S_VERSION}" \
        --config-patch-controlplanes '{"cluster": {"allowSchedulingOnControlPlanes": true}}' \
        --mtu=1450 \
        --memory-controlplanes="${MEMORY_CONTROLPLANES}" \
        --cpus-controlplanes="${CPUS_CONTROLPLANES}" \
        --workers=0 \
        --subnet="${network_cidr}" \
        ${REGISTRY_MIRROR_FLAGS[@]+"${REGISTRY_MIRROR_FLAGS[@]}"}

    "${TALOSCTL[@]}" config nodes "${node_ip}"
    "${TALOSCTL[@]}" kubeconfig -f "${TMP}/kubeconfig"
  fi

  export KUBECONFIG="${TMP}/kubeconfig"
}

# ---------------------------------------------------------------------------
# tests — run the compiled integration test binary
# ---------------------------------------------------------------------------
function tests {
  export WORKLOAD_TALOS_VERSION="${TALOS_VERSION}"
  ./_out/integration.test -test.v
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
build_registry_mirrors
config
cluster
tests
