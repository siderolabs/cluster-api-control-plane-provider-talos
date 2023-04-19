REGISTRY ?= ghcr.io
USERNAME ?= siderolabs
SHA ?= $(shell git describe --match=none --always --abbrev=8 --dirty)
TAG ?= $(shell git describe --tag --always --dirty)
BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)
REGISTRY_AND_USERNAME := $(REGISTRY)/$(USERNAME)
NAME := cluster-api-control-plane-talos-controller
WITH_RACE ?= false
CGO_ENABLED = 0
TESTPKGS ?= ./controllers/...
CONTROLLER_GEN_VERSION ?= v0.11.3
CONVERSION_GEN_VERSION ?= v0.26.0

ifneq (, $(filter $(WITH_RACE), t true TRUE y yes 1))
GO_BUILDFLAGS += -race
CGO_ENABLED = 1
GO_LDFLAGS += -linkmode=external -extldflags '-static'
endif

GO_LDFLAGS += -s -w

ARTIFACTS := _out

TOOLS ?= ghcr.io/siderolabs/tools:v1.4.0-1-g955aabc
PKGS ?= v1.4.1-5-ga333a84

BUILD := docker buildx build
PLATFORM ?= linux/amd64
PROGRESS ?= auto
PUSH ?= false
COMMON_ARGS := --file=Dockerfile
COMMON_ARGS += --progress=$(PROGRESS)
COMMON_ARGS += --platform=$(PLATFORM)
COMMON_ARGS += --build-arg=REGISTRY_AND_USERNAME=$(REGISTRY_AND_USERNAME)
COMMON_ARGS += --build-arg=NAME=$(NAME)
COMMON_ARGS += --build-arg=TAG=$(TAG)
COMMON_ARGS += --build-arg=PKGS=$(PKGS)
COMMON_ARGS += --build-arg=TOOLS=$(TOOLS)
COMMON_ARGS += --build-arg=GO_BUILDFLAGS="$(GO_BUILDFLAGS)"
COMMON_ARGS += --build-arg=GO_LDFLAGS="$(GO_LDFLAGS)"
COMMON_ARGS += --build-arg=CGO_ENABLED="$(CGO_ENABLED)"
COMMON_ARGS += --build-arg=TESTPKGS="$(TESTPKGS)"
COMMON_ARGS += --build-arg=CONTROLLER_GEN_VERSION=$(CONTROLLER_GEN_VERSION)
COMMON_ARGS += --build-arg=CONVERSION_GEN_VERSION=$(CONVERSION_GEN_VERSION)

all: manifests container

.PHONY: help
help: ## This help menu.
	@grep -E '^[a-zA-Z%_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'

target-%: ## Builds the specified target defined in the Dockerfile. The build result will remain only in the build cache.
	@$(BUILD) \
		--target=$* \
		$(COMMON_ARGS) \
		$(TARGET_ARGS) .

local-%: ## Builds the specified target defined in the Dockerfile using the local output type. The build result will be output to the specified local destination.
	@$(MAKE) target-$* TARGET_ARGS="--output=type=local,dest=$(DEST) $(TARGET_ARGS)"

docker-%: ## Builds the specified target defined in the Dockerfile using the docker output type. The build result will be loaded into docker.
	@$(MAKE) target-$* TARGET_ARGS="--tag $(REGISTRY_AND_USERNAME)/$(NAME):$(TAG) $(TARGET_ARGS)"

define RELEASEYAML
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
namespace: $(NAMESPACE)
commonLabels:
  app: cluster-api-control-plane-provider-talos
bases:
  - crd
  - rbac
  - manager
endef

export RELEASEYAML
.PHONY: init
init: ## Initialize the project.
	@mkdir tmp \
	&& cd tmp \
	&& kubebuilder init --repo $(REGISTRY_AND_USERNAME)/$(NAME) --domain $(DOMAIN) \
	&& rm -rf Dockerfile Makefile .gitignore bin hack \
	&& mv ./* ../ \
	&& cd .. \
	&& rm -rf tmp \
	&& echo "$$RELEASEYAML" > ./config/kustomization.yaml

.PHONY: generate
generate: ## Generate source code.
	@$(MAKE) local-$@ DEST=./ PLATFORM=linux/amd64

.PHONY: container
container: generate ## Build the container image.
	@$(MAKE) docker-$@ TARGET_ARGS="--push=$(PUSH)"

.PHONY: manifests
manifests: ## Generate manifests (e.g. CRD, RBAC, etc.).
	@$(MAKE) local-$@ DEST=./ PLATFORM=linux/amd64

.PHONY: release-notes
release-notes: ## Create the release notes.
	@mkdir -p $(ARTIFACTS)
	ARTIFACTS=$(ARTIFACTS) ./hack/release.sh $@ $(ARTIFACTS)/RELEASE_NOTES.md $(TAG)

.PHONY: release
release: manifests container release-notes ## Create the release YAML. The build result will be ouput to the specified local destination.
	@$(MAKE) local-$@ DEST=./$(ARTIFACTS) PLATFORM=linux/amd64

.PHONY: deploy
deploy: manifests ## Deploy to a cluster. This is for testing purposes only.
	kubectl apply -k config/default

.PHONY: destroy
destroy: ## Remove from a cluster. This is for testing purposes only.
	kubectl delete -k config/default

.PHONY: install
install: manifests ## Install CRDs into a cluster.
	kubectl apply -k config/crd

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from a cluster.
	kubectl delete -k config/crd

.PHONY: run
run: install ## Run the controller locally. This is for testing purposes only.
	@$(MAKE) docker-container TARGET_ARGS="--load"
	@docker run --rm -it --net host -v $(PWD):/src -v $(KUBECONFIG):/root/.kube/config -e KUBECONFIG=/root/.kube/config $(REGISTRY_AND_USERNAME)/$(NAME):$(TAG)

.PHONY: clean
clean:
	@rm -rf $(ARTIFACTS)

integration-test-build:
	@$(MAKE) local-integration-test DEST=./_out/ PLATFORM=linux/amd64

.PHONY: integration-test
integration-test: integration-test-build
	@REGISTRY_AND_USERNAME=$(REGISTRY_AND_USERNAME) TAG=$(TAG) NAME=$(NAME) bash hack/test/e2e-aws.sh

.PHONY: unit-tests
unit-tests:  ## Performs unit tests
	@$(MAKE) local-$@ DEST=$(ARTIFACTS)

check-dirty: ## Verifies that source tree is not dirty
	@if test -n "`git status --porcelain`"; then echo "Source tree is dirty"; git status; exit 1 ; fi