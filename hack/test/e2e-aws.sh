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
TALOS_VERSION="${TALOS_DEFAULT:-v1.9.0}" # NOTE: this is Talos version for the test environment, not Talos version for CAPI templates (see capi-utils)
K8S_VERSION="${K8S_VERSION:-v1.30.4}"
export WORKLOAD_KUBERNETES_VERSION="${WORKLOAD_KUBERNETES_VERSION:-${K8S_VERSION}}"
export UPGRADE_K8S_VERSION="${UPGRADE_K8S_VERSION:-v1.31.0}"
KUBECONFIG=
AMI=${AWS_AMI:-$(curl -sL https://github.com/talos-systems/talos/releases/download/${TALOS_VERSION}/cloud-images.json | \
    jq -r --arg REGION "${REGION}" '.[] | select(.region == $REGION) | select (.arch == "amd64") | .id')}
export PROVIDER=aws:v1.5.2

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
      ${KUBECTL} logs -n capa-system deployment/capa-controller-manager manager || true
      ${KUBECTL} logs -n cacppt-system deployment/cacppt-controller-manager || true
    fi
  fi

  # delete deployed cluster
  if [[ ! -z ${CREATED_CLUSTER} ]] && [ "${TEARDOWN_CLUSTER}" = true ]; then
    echo "destroying deployed cluster"
    rm -rf ~/.talos/clusters/${CREATED_CLUSTER}
    ${TALOSCTL} cluster destroy --name=${CREATED_CLUSTER} || true
  fi

  if [ "${TEARDOWN_CLUSTER}" = true ]; then
    rm -rf ${TMP}
  fi
  trap - EXIT
}

trap 'cleanup $?' INT TERM EXIT

function build_registry_mirrors {
  if [[ "${CI:-false}" == "true" ]]; then
    REGISTRY_MIRROR_FLAGS=

    for registry in docker.io k8s.gcr.io quay.io gcr.io ghcr.io registry.dev.talos-systems.io; do
      local service="registry-${registry//./-}.ci.svc"
      local addr=`python3 -c "import socket; print(socket.gethostbyname('${service}'))"`

      REGISTRY_MIRROR_FLAGS="${REGISTRY_MIRROR_FLAGS} --registry-mirror ${registry}=http://${addr}:5000"
    done
  else
    # use the value from the environment, if present
    REGISTRY_MIRROR_FLAGS=${REGISTRY_MIRROR_FLAGS:-}
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
    TAG="${TALOS_VERSION}" ${TALOSCTL} cluster create \
      --name=${CREATED_CLUSTER} \
      --kubernetes-version=${K8S_VERSION} \
      ${REGISTRY_MIRROR_FLAGS} \
      --cidr 172.27.0.0/24 \
      --workers=0

    ${KUBECTL} taint node ${CREATED_CLUSTER}-controlplane-1 node-role.kubernetes.io/control-plane=:NoSchedule-

    ${TALOSCTL} config nodes 172.27.0.2
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
  export AWS_SSH_KEY_NAME=${AWS_SSH_KEY_NAME:-talos-e2e}
  export AWS_VPC_ID=${AWS_VPC_ID:-vpc-ff5c5687}
  export AWS_SUBNET=${AWS_SUBNET:-subnet-c4e9b3a0}
  export AWS_SUBNET_AZ=${AWS_SUBNET_AZ:-us-east-1a}

  ## Control plane vars
  export AWS_CONTROL_PLANE_AMI_ID=${AMI}
  export AWS_CONTROL_PLANE_ADDL_SEC_GROUPS=${AWS_CONTROL_PLANE_ADDL_SEC_GROUPS:-'[{id: sg-ebe8e59f}]'}

  CREDS=$(echo "[default]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}" | base64 -w0)

  ## Worker vars
  export AWS_NODE_AMI_ID=${AMI}
  export AWS_NODE_ADDL_SEC_GROUPS=${AWS_CONTROL_PLANE_ADDL_SEC_GROUPS:-'[{id: sg-ebe8e59f}]'}
  export AWS_B64ENCODED_CREDENTIALS=${AWS_B64ENCODED_CREDENTIALS:-${CREDS}}
}

function tests {
  export WORKLOAD_TALOS_VERSION=${TALOS_VERSION}
  ./_out/integration.test -test.v
}

build_registry_mirrors
config
cluster
aws_setup
tests
