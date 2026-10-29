#!/bin/bash

set -eou pipefail

TMP="/tmp/cacppt-e2e"
mkdir -p "${TMP}"

AWS_ACCESS_KEY_ID=${AWS_ACCESS_KEY_ID:-}
AWS_SECRET_ACCESS_KEY=${AWS_SECRET_ACCESS_KEY:-}

if [[ -z ${AWS_ACCESS_KEY_ID} || -z ${AWS_SECRET_ACCESS_KEY} ]]; then
  if [ -f ~/.aws/credentials ]; then
    AWS_B64ENCODED_CREDENTIALS=${AWS_B64ENCODED_CREDENTIALS:-$(cat ~/.aws/credentials | base64 -w0)}
  else
    echo "either AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY or AWS_B64ENCODED_CREDENTIALS must be defined to run this test"

    exit 1
  fi
fi

TAG="${TAG:-$(git describe --tag --always --dirty)}"
REGION="us-east-1"
BUCKET="talos-ci-e2e"
PLATFORM=$(uname -s | tr "[:upper:]" "[:lower:]")
TALOS_VERSION="${TALOS_DEFAULT:-v1.14.0}" # NOTE: this is Talos version for the test environment, not Talos version for CAPI templates (see capi-utils)
K8S_VERSION="${K8S_VERSION:-v1.35.8}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.36.4}"
KUBECONFIG=
AMI=${AWS_AMI:-$(curl -sL https://github.com/talos-systems/talos/releases/download/${TALOS_VERSION}/cloud-images.json | \
    jq -r --arg REGION "${REGION}" '.[] | select(.region == $REGION) | select (.arch == "amd64") | .id')}
export PROVIDER=aws:v2.9.3
# pin the core and bootstrap providers, so that the test runs against a known CAPI release under the v1beta2 contract;
# the bootstrap provider version follows the CABPT version in go.mod
export CORE_PROVIDER="${CORE_PROVIDER:-cluster-api:v1.14.3}"
CABPT_VERSION="${CABPT_VERSION:-$(awk '$1 == "github.com/siderolabs/cluster-api-bootstrap-provider-talos" { print $2 }' go.mod)}"
if [[ -z ${CABPT_VERSION} ]]; then
  echo "failed to determine the CABPT version from go.mod"

  exit 1
fi
export BOOTSTRAP_PROVIDER="${BOOTSTRAP_PROVIDER:-talos:${CABPT_VERSION}}"
# the locally built provider is staged under a fake version tag, which must fall into the
# 0.6 release series of config/metadata/metadata.yaml (v1beta2 contract)
CACPPT_STAGED_VERSION="v0.6.0"
export CONTROL_PLANE_PROVIDER="talos:${CACPPT_STAGED_VERSION}"
# bounded wait for the workload Cluster deletion on failure, so that CAPA can deprovision AWS resources
CLUSTER_DELETE_TIMEOUT="${CLUSTER_DELETE_TIMEOUT:-15m}"
# bound the e2e suite, so that a hang still leaves time for the cleanup above within the CI job timeout
# (timeoutMinutes in .kres.yaml): the suite timeout plus CLUSTER_DELETE_TIMEOUT must fit in it after the build
E2E_TEST_TIMEOUT="${E2E_TEST_TIMEOUT:-60m}"

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
      ${KUBECTL} get pods -A -o wide || true
      ${KUBECTL} get namespaces -o wide || true
      ${KUBECTL} get events -A --sort-by=.lastTimestamp || true

      ${KUBECTL} logs -n capa-system deployment/capa-controller-manager manager || true
      ${KUBECTL} logs -n cacppt-system deployment/cacppt-controller-manager || true
      ${KUBECTL} logs -n capi-system deployment/capi-controller-manager || true
      ${KUBECTL} get cluster,taloscontrolplane,machine -A -o yaml || true

      # delete the workload clusters and give CAPA a bounded amount of time to deprovision
      # the AWS resources before the management cluster is destroyed below; the logs above
      # have already been captured, so a stuck teardown can't hide them.
      ${KUBECTL} delete cluster --all --all-namespaces --wait=false || true
      if [[ -n "$(${KUBECTL} get cluster --all-namespaces -o name 2>/dev/null)" ]] && \
        ! ${KUBECTL} wait --for=delete cluster --all --all-namespaces --timeout=${CLUSTER_DELETE_TIMEOUT}; then
        echo "workload cluster deletion did not finish within ${CLUSTER_DELETE_TIMEOUT}, AWS resources may be leaked"
        ${KUBECTL} get cluster,awscluster,awsmachine -A -o wide || true
        ${KUBECTL} logs -n capa-system deployment/capa-controller-manager manager --since=${CLUSTER_DELETE_TIMEOUT} || true
      fi
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
  export CONTROL_PLANE_PROVIDER_COMPONENTS=${TMP}/control-plane-talos/${CACPPT_STAGED_VERSION}/control-plane-components.yaml
  mkdir -p $(dirname ${CONTROL_PLANE_PROVIDER_COMPONENTS})

  cp -rf config ${TMP}/config

  cd ${TMP}/config/manager
  ${KUSTOMIZE} edit set image controller=${REGISTRY_AND_USERNAME}/${NAME}:${TAG}
  cd -
  ${KUSTOMIZE} build ${TMP}/config/default >${CONTROL_PLANE_PROVIDER_COMPONENTS}
  cp ${TMP}/config/metadata/metadata.yaml $(dirname ${CONTROL_PLANE_PROVIDER_COMPONENTS})/
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
        --config-patch-controlplanes hack/test/schedule-controlplanes.patch \
        --mtu=1450 \
        --memory-controlplanes=8GiB \
        --cpus-controlplanes=8 \
        --workers=0

    ${TALOSCTL} config nodes 10.5.0.2
    ${TALOSCTL} kubeconfig -f ${TMP}/kubeconfig
  fi

  export KUBECONFIG=${TMP}/kubeconfig
}

function aws_setup {
  if [[ -z ${AMI} ]]; then
    echo "no AMI image was defined"

    exit 1
  fi

  export NAMESPACE=default

  ## Cluster-wide vars
  export AWS_REGION=${AWS_REGION:-us-east-1}
  export AWS_SSH_KEY_NAME=${AWS_SSH_KEY_NAME:-capi-ci-e2e}
  export AWS_VPC_ID=${AWS_VPC_ID:-vpc-0a3b8fa38a76e6402}
  export AWS_SUBNET=${AWS_SUBNET:-subnet-00715427dc2c5d768}
  export AWS_SUBNET_AZ=${AWS_SUBNET_AZ:-us-east-1a}

  ## Control plane vars
  export AWS_CONTROL_PLANE_AMI_ID=${AMI}
  export AWS_CONTROL_PLANE_ADDL_SEC_GROUPS=${AWS_CONTROL_PLANE_ADDL_SEC_GROUPS:-'[{id: sg-0582ba1634acaf525}]'}

  CREDS=$(echo "[default]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}" | base64 -w0)

  ## Worker vars
  export AWS_NODE_AMI_ID=${AMI}
  export AWS_NODE_ADDL_SEC_GROUPS=${AWS_CONTROL_PLANE_ADDL_SEC_GROUPS:-'[{id: sg-0582ba1634acaf525}]'}
  export AWS_B64ENCODED_CREDENTIALS=${AWS_B64ENCODED_CREDENTIALS:-${CREDS}}
}

function tests {
  export WORKLOAD_TALOS_VERSION=${TALOS_VERSION}
  ./_out/integration.test -test.v -test.timeout="${E2E_TEST_TIMEOUT}"
}

build_registry_mirrors
config
cluster
aws_setup
tests
