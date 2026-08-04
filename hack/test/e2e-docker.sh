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
# Workload cluster nodes use kindest/node — versions must match available tags
# on Docker Hub (https://hub.docker.com/r/kindest/node/tags).
K8S_VERSION="${K8S_VERSION:-v1.34.8}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.35.5}"
KUBECONFIG=

# CAPD — Cluster API Provider Docker (no cloud credentials needed)
# Must stay in sync with the CAPI version in go.mod (currently v1.10.x → v1beta1 contract).
# CAPD v1.11+ uses v1beta2 which is incompatible with the clusterctl embedded in CAPI v1.10.x.
export PROVIDER=docker:v1.10.10

# ---------------------------------------------------------------------------
# Registry configuration
#
# REGISTRY_AND_USERNAME  registry + org used to BUILD and name the image
#                        (default: ghcr.io/siderolabs)
#
# PUSH_REGISTRY          where to push the controller image before running
#                        tests.  Defaults to REGISTRY_AND_USERNAME.
#                        Override to redirect to a private registry:
#                          export PUSH_REGISTRY=myregistry.example.com/myorg
#
# PULL_REGISTRY          registry the Talos cluster will pull the image from.
#                        Defaults to PUSH_REGISTRY.
#                        Must be reachable from inside the Talos node network.
#
# REGISTRY_USERNAME      optional credentials for the push/pull registry
# REGISTRY_PASSWORD      optional credentials for the push/pull registry
#
# Examples:
#   # Private registry without auth (e.g. local Harbor, Nexus, Gitea):
#   export PUSH_REGISTRY=harbor.corp.example.com/cacppt
#   export PULL_REGISTRY=harbor.corp.example.com/cacppt
#
#   # Private registry with auth:
#   export PUSH_REGISTRY=registry.example.com/myorg
#   export PULL_REGISTRY=registry.example.com/myorg
#   export REGISTRY_USERNAME=myuser
#   export REGISTRY_PASSWORD=mypassword
# ---------------------------------------------------------------------------
REGISTRY_AND_USERNAME="${REGISTRY_AND_USERNAME:-ghcr.io/siderolabs}"
NAME="${NAME:-cluster-api-control-plane-talos-controller}"
PUSH_REGISTRY="${PUSH_REGISTRY:-${REGISTRY_AND_USERNAME}}"
PULL_REGISTRY="${PULL_REGISTRY:-${PUSH_REGISTRY}}"
REGISTRY_USERNAME="${REGISTRY_USERNAME:-}"
REGISTRY_PASSWORD="${REGISTRY_PASSWORD:-}"

# Resource knobs — can be overridden for constrained environments.
CPUS_CONTROLPLANES="${CPUS_CONTROLPLANES:-8.0}"
MEMORY_CONTROLPLANES="${MEMORY_CONTROLPLANES:-8GiB}"

CREATED_CLUSTER=""

TALOSCTL_PATH="${TALOSCTL_PATH:-$(which talosctl 2>/dev/null || echo "${TMP}/talosctl")}"
declare -a TALOSCTL=()

KUSTOMIZE="${KUSTOMIZE:-$(which kustomize 2>/dev/null || echo "${TMP}/kustomize")}"
TEARDOWN_CLUSTER="${TEARDOWN_CLUSTER:-true}"
KUBECTL="${KUBECTL:-$(which kubectl 2>/dev/null || echo "${TMP}/kubectl")}"

# Registry mirror flags injected into the Talos cluster config.
declare -a REGISTRY_MIRROR_FLAGS=()

# ---------------------------------------------------------------------------
# Tooling bootstrap — use pre-installed binaries if available, download otherwise.
# ---------------------------------------------------------------------------
if [[ "${KUBECTL}" == "${TMP}/kubectl" ]]; then
  echo "kubectl not found in PATH, downloading..."
  curl -sSfLo "${KUBECTL}" \
    "https://dl.k8s.io/release/$(curl -sSfL https://dl.k8s.io/release/stable.txt)/bin/${PLATFORM}/amd64/kubectl"
  chmod +x "${KUBECTL}"
else
  echo "Using kubectl:    ${KUBECTL}"
fi

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
# push_controller_image — tag and push the controller image to PUSH_REGISTRY.
#
# Skipped when PUSH_REGISTRY == REGISTRY_AND_USERNAME (image already pushed
# by the 'all' make target, e.g. in CI or when targeting ghcr.io directly).
# ---------------------------------------------------------------------------
function push_controller_image {
  local src_image="${REGISTRY_AND_USERNAME}/${NAME}:${TAG}"
  local dst_image="${PUSH_REGISTRY}/${NAME}:${TAG}"

  if [[ "${PUSH_REGISTRY}" == "${REGISTRY_AND_USERNAME}" ]]; then
    echo "PUSH_REGISTRY == REGISTRY_AND_USERNAME — skipping re-push (image already at ${src_image})"
    return 0
  fi

  if [[ -n "${REGISTRY_USERNAME}" ]]; then
    local push_host
    push_host=$(echo "${PUSH_REGISTRY}" | cut -d'/' -f1)
    echo "Logging in to ${push_host}..."
    echo "${REGISTRY_PASSWORD}" | docker login "${push_host}" -u "${REGISTRY_USERNAME}" --password-stdin
  fi

  echo "Pushing ${src_image} → ${dst_image}..."
  docker tag "${src_image}" "${dst_image}"
  docker push "${dst_image}"
}

# ---------------------------------------------------------------------------
# build_registry_mirrors — add REGISTRY_MIRROR_FLAGS for the Talos cluster.
#
# In CI: mirror all public registries through the in-cluster registry cache.
# With a custom PULL_REGISTRY: mirror ghcr.io through it so the controller
#   image is pulled from the private registry instead of ghcr.io.
# ---------------------------------------------------------------------------
function build_registry_mirrors {
  if [[ "${CI:-false}" == "true" ]]; then
    # Standard CI registry mirrors (in-cluster registry cache services).
    for registry in docker.io registry.k8s.io quay.io gcr.io ghcr.io; do
      local service="registry-${registry//./-}.ci.svc"
      local addr
      addr=$(python3 -c "import socket; print(socket.gethostbyname('${service}'))")
      REGISTRY_MIRROR_FLAGS+=("--config-patch={\"machine\":{\"registries\":{\"mirrors\":{\"${registry}\":{\"endpoints\":[\"http://${addr}:5000\"],\"overridePath\":true}}}}}")
    done
    return 0
  fi

  # When a custom pull registry is set, mirror ghcr.io through it.
  if [[ "${PULL_REGISTRY}" != "${REGISTRY_AND_USERNAME}" ]]; then
    local pull_host
    pull_host=$(echo "${PULL_REGISTRY}" | cut -d'/' -f1)
    local src_host
    src_host=$(echo "${REGISTRY_AND_USERNAME}" | cut -d'/' -f1)

    # Determine protocol (http vs https) based on whether the registry host
    # is on a private/local network (RFC-1918) or a well-known public host.
    local protocol="https"
    if python3 -c "
import ipaddress, socket, sys
try:
    ip = ipaddress.ip_address(socket.gethostbyname('${pull_host}'))
    sys.exit(0 if ip.is_private else 1)
except Exception:
    sys.exit(1)
" 2>/dev/null; then
      protocol="http"
    fi

    local mirror_patch
    if [[ -n "${REGISTRY_USERNAME}" ]]; then
      # Registry with authentication — pass credentials in the Talos config.
      mirror_patch="{\"machine\":{\"registries\":{\"mirrors\":{\"${src_host}\":{\"endpoints\":[\"${protocol}://${pull_host}\"],\"overridePath\":true}},\"config\":{\"${protocol}://${pull_host}\":{\"auth\":{\"username\":\"${REGISTRY_USERNAME}\",\"password\":\"${REGISTRY_PASSWORD}\"}}}}}"
    else
      mirror_patch="{\"machine\":{\"registries\":{\"mirrors\":{\"${src_host}\":{\"endpoints\":[\"${protocol}://${pull_host}\"],\"overridePath\":true}}}}}"
    fi

    REGISTRY_MIRROR_FLAGS+=("--config-patch=${mirror_patch}")
    echo "Registry mirror configured: ${src_host} → ${protocol}://${pull_host}"
  fi
}

# ---------------------------------------------------------------------------
# config — build the control-plane provider components via kustomize.
#
# The kustomize image is set to PULL_REGISTRY so the Talos cluster pulls
# from the right place.
# ---------------------------------------------------------------------------
function config {
  if [[ "${KUSTOMIZE}" == "${TMP}/kustomize" ]]; then
    echo "kustomize not found in PATH, downloading..."
    curl -sSfLo "${TMP}/kustomize.tar.gz" \
      "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2Fv4.1.0/kustomize_v4.1.0_${PLATFORM}_amd64.tar.gz"
    tar -xf "${TMP}/kustomize.tar.gz" -C "${TMP}" && rm "${TMP}/kustomize.tar.gz"
  else
    echo "Using kustomize:  ${KUSTOMIZE}"
  fi

  export CONTROL_PLANE_PROVIDER_COMPONENTS="${TMP}/control-plane-talos/v0.4.0/control-plane-components.yaml"
  mkdir -p "$(dirname "${CONTROL_PLANE_PROVIDER_COMPONENTS}")"

  cp -rf config "${TMP}/config"

  cd "${TMP}/config/manager"
  # Point the controller image to the pull registry so the Talos cluster
  # can reach it directly (no mirror needed when PULL_REGISTRY is set).
  "${KUSTOMIZE}" edit set image "controller=${PULL_REGISTRY}/${NAME}:${TAG}"
  cd -
  "${KUSTOMIZE}" build "${TMP}/config/default" > "${CONTROL_PLANE_PROVIDER_COMPONENTS}"
  cp "${TMP}/config/metadata/metadata.yaml" "${TMP}/control-plane-talos/v0.4.0/"
}

# ---------------------------------------------------------------------------
# cluster — create the Talos-on-Docker management cluster
# ---------------------------------------------------------------------------
function cluster {
  if [[ "${TALOSCTL_PATH}" == "${TMP}/talosctl" ]]; then
    echo "talosctl not found in PATH, downloading..."
    curl -sSfLo "${TALOSCTL_PATH}" \
      "https://github.com/siderolabs/talos/releases/download/${TALOS_VERSION}/talosctl-${PLATFORM}-amd64"
    chmod +x "${TALOSCTL_PATH}"
  else
    echo "Using talosctl:   ${TALOSCTL_PATH}"
  fi

  # CAPD hardcodes the Docker network name "kind" for all workload cluster
  # containers (LB, control plane, workers). Create it if it doesn't exist.
  if ! docker network inspect kind &>/dev/null; then
    echo "Creating Docker network 'kind' required by CAPD..."
    docker network create kind
  fi

  TALOSCTL=("${TALOSCTL_PATH}" "--talosconfig=${TMP}/talosconfig")
  CREATED_CLUSTER="cacppt-test-$(echo $RANDOM | md5sum | head -c 10)"

  if [[ ! -f "${TMP}/kubeconfig" ]]; then
    local network_cidr node_ip gateway_ip
    network_cidr=$(find_free_cidr)
    node_ip=$(echo    "${network_cidr}" | sed 's|\.0/24|.2|')
    gateway_ip=$(echo "${network_cidr}" | sed 's|\.0/24|.1|')
    echo "Network CIDR: ${network_cidr}  node: ${node_ip}  gateway: ${gateway_ip}"

    local controlplane_patch='{"cluster": {"allowSchedulingOnControlPlanes": true}}'
    if [[ -n "${HTTP_PROXY:-}${http_proxy:-}" ]]; then
      local pull_host
      pull_host=$(echo "${PULL_REGISTRY}" | cut -d'/' -f1)
      local no_proxy_list="localhost,127.0.0.1,${gateway_ip},${network_cidr},${pull_host}"
      echo "Corporate proxy detected — injecting NO_PROXY=${no_proxy_list} into cluster config"
      controlplane_patch="{\"cluster\": {\"allowSchedulingOnControlPlanes\": true}, \"machine\": {\"env\": {\"no_proxy\": \"${no_proxy_list}\", \"NO_PROXY\": \"${no_proxy_list}\"}}}"
    fi

    echo "Creating management cluster ${CREATED_CLUSTER} (Docker/Talos)..."
    TAG="${TALOS_VERSION}" "${TALOSCTL_PATH}" cluster create docker \
        --name="${CREATED_CLUSTER}" \
        --talosconfig-destination="${TMP}/talosconfig" \
        --kubernetes-version="${K8S_VERSION}" \
        --config-patch-controlplanes "${controlplane_patch}" \
        --mtu=1450 \
        --memory-controlplanes="${MEMORY_CONTROLPLANES}" \
        --cpus-controlplanes="${CPUS_CONTROLPLANES}" \
        --workers=0 \
        --subnet="${network_cidr}" \
        --mount "type=bind,source=/var/run/docker.sock,destination=/var/run/docker.sock,bind-propagation=rslave" \
        ${REGISTRY_MIRROR_FLAGS[@]+"${REGISTRY_MIRROR_FLAGS[@]}"}

    # CAPD creates all workload cluster containers (LB, nodes) on the "kind"
    # Docker network. The management cluster node lives on its own subnet.
    # Connect the management node to "kind" so that CAPI controllers running
    # inside Talos can reach the workload cluster API server.
    echo "Connecting management node to Docker network 'kind'..."
    docker network connect kind "${CREATED_CLUSTER}-controlplane-1"

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

  # Point to the local CAPD+Talos cluster template (the upstream
  # siderolabs/cluster-api-templates repo has no docker/ directory).
  # The test reads CLUSTER_TEMPLATE; clusterctl accepts both file:// URIs and
  # plain absolute paths, so we use an absolute path here.
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  export CLUSTER_TEMPLATE="${script_dir}/templates/docker/standard/standard.yaml"

  ./_out/integration.test -test.v
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
push_controller_image
build_registry_mirrors
config
cluster
tests
