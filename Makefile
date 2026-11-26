# Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
# SPDX-License-Identifier: Apache-2.0

# elb-envoy-remote-infra-provider Makefile

# Build configuration
BINARY_NAME ?= elb-envoy-remote-infra-provider
MAIN_PATH ?= .
CRD_OUTPUT_DIR ?= ./pkg/config/crd
GIT_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GIT_COMMIT ?= $(shell git rev-parse HEAD 2>/dev/null || echo "unknown")
VERSION_PKG = github.com/aws/elb-envoy-remote-infra-provider/pkg/version
LDFLAGS = -X $(VERSION_PKG).GitVersion=$(GIT_VERSION) -X $(VERSION_PKG).GitCommit=$(GIT_COMMIT)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) $(MAIN_PATH)

# Cross-compiled binaries: bin/<os>/<arch>/$(BINARY_NAME).
BIN_DIR ?= bin
PLATFORMS ?= linux_amd64 linux_arm64

.PHONY: go.build.%
go.build.%: ## Build one platform, e.g. make go.build.linux_arm64
	$(eval OS := $(word 1,$(subst _, ,$*)))
	$(eval ARCH := $(word 2,$(subst _, ,$*)))
	CGO_ENABLED=0 GOOS=$(OS) GOARCH=$(ARCH) go build -trimpath \
		-ldflags "-s -w $(LDFLAGS)" -o $(BIN_DIR)/$(OS)/$(ARCH)/$(BINARY_NAME) $(MAIN_PATH)

.PHONY: build-multiarch
build-multiarch: $(addprefix go.build.,$(PLATFORMS))

# Images (build context: $(BIN_DIR)):
#   image            host arch, loaded locally
#   image.multiarch  all PLATFORMS, saved as an OCI archive
#   push-multiarch   all PLATFORMS, pushed as one multi-arch tag
DOCKER ?= docker
REGISTRY ?=
IMAGE_NAME ?= elb-envoy-remote-infra-provider
IMAGE ?= $(if $(REGISTRY),$(REGISTRY)/)$(IMAGE_NAME)
TAG ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
# Empty uses the Dockerfile's base.
BASE_IMAGE ?=
BUILDX_BUILDER ?= elb-envoy-remote-infra-provider-builder
# Extra buildx flags, e.g. --provenance=false.
BUILDX_FLAGS ?=
OCI_OUTPUT ?= _output/$(IMAGE_NAME)-oci.tar

HOST_ARCH := $(shell go env GOARCH)
comma := ,
empty :=
space := $(empty) $(empty)
BUILDX_PLATFORMS = $(subst _,/,$(subst $(space),$(comma),$(strip $(PLATFORMS))))
BUILD_ARGS = --build-arg BINARY_NAME=$(BINARY_NAME) $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE))

.PHONY: image
image: go.build.linux_$(HOST_ARCH)
	$(DOCKER) buildx build --platform linux/$(HOST_ARCH) $(BUILD_ARGS) $(BUILDX_FLAGS) \
		-f Dockerfile -t $(IMAGE):$(TAG) --load $(BIN_DIR)

# Multi-platform output needs a docker-container builder (selected with --builder).
.PHONY: image.multiarch.setup
image.multiarch.setup:
	$(DOCKER) buildx inspect $(BUILDX_BUILDER) >/dev/null 2>&1 || \
		$(DOCKER) buildx create --name $(BUILDX_BUILDER) --driver docker-container --platform $(BUILDX_PLATFORMS)

.PHONY: image.multiarch
image.multiarch: build-multiarch image.multiarch.setup
	mkdir -p $(dir $(OCI_OUTPUT))
	$(DOCKER) buildx build --builder $(BUILDX_BUILDER) --platform $(BUILDX_PLATFORMS) $(BUILD_ARGS) $(BUILDX_FLAGS) \
		-f Dockerfile -t $(IMAGE):$(TAG) --output type=oci,dest=$(OCI_OUTPUT) $(BIN_DIR)

.PHONY: push-multiarch
push-multiarch: build-multiarch image.multiarch.setup
	$(DOCKER) buildx build --builder $(BUILDX_BUILDER) --platform $(BUILDX_PLATFORMS) $(BUILD_ARGS) $(BUILDX_FLAGS) \
		-f Dockerfile -t $(IMAGE):$(TAG) --push $(BIN_DIR)

# Like push-multiarch, with Finch, which has no buildx. make docker-push uses it when DOCKER=finch.
.PHONY: push-finch
push-finch: build-multiarch
	$(DOCKER) build --platform $(BUILDX_PLATFORMS) $(BUILD_ARGS) -f Dockerfile -t $(IMAGE):$(TAG) $(BIN_DIR)
	$(DOCKER) push --all-platforms $(IMAGE):$(TAG)

.PHONY: docker-push
docker-push:
	@test -n "$(IMG)" || { echo "IMG is required, e.g. make docker-push IMG=<registry>/<repo>:<tag>"; exit 1; }
	@img="$(IMG)"; repo="$${img%:*}"; tag="$${img##*:}"; \
	case "$$tag" in */*|"$$img") repo="$$img"; tag="$(TAG)";; esac; \
	$(MAKE) $(if $(filter finch,$(notdir $(DOCKER))),push-finch,push-multiarch) IMAGE="$$repo" TAG="$$tag"

.PHONY: clean
clean:
	rm -rf "$(BIN_DIR)"
	rm -f "$(BINARY_NAME)" "$(OCI_OUTPUT)"

.PHONY: generate
generate:
	controller-gen object:headerFile="hack/boilerplate.go.txt" paths=./pkg/api/...

.PHONY: mocks
mocks:
	GOFLAGS='-mod=mod' go generate ./...

# Helm chart. `manifests` also copies the generated CRD into the chart's crds/ directory,
# which CI checks is identical to $(CRD_OUTPUT_DIR).
CHART_DIR ?= charts/elb-envoy-remote-infra-provider
CHART_CRD_DIR ?= $(CHART_DIR)/crds

.PHONY: manifests
manifests:
	controller-gen crd paths=./pkg/api/... output:crd:dir=$(CRD_OUTPUT_DIR)
	mkdir -p $(CHART_CRD_DIR)
	cp $(CRD_OUTPUT_DIR)/*.yaml $(CHART_CRD_DIR)/

# Generate the CRD API reference page for the docs site.
.PHONY: api-ref-docs
api-ref-docs:
	crd-ref-docs \
		--source-path=$(CURDIR)/pkg/api/ \
		--config=crd-ref-docs.yaml \
		--renderer=markdown \
		--output-path=$(CURDIR)/docs/guide/spec.md

# Docs site (Material for MkDocs), published per version with mike. The versions match
# .github/workflows/docs.yml.
# The commands are described in CONTRIBUTING.md.
MKDOCS ?= mkdocs
MIKE ?= mike
DOCS_PIP_PACKAGES ?= mkdocs==1.6.0 mkdocs-material==9.5.31 mike==2.1.2
DOCS_LATEST ?= true
# The docs version is the MAJOR.MINOR of version.txt (0.9.0-rc.1 gives 0.9). Set DOCS_VERSION to override it.
DOCS_VERSION ?= $(if $(wildcard version.txt),$(shell sed -E 's/^([0-9]+\.[0-9]+).*/\1/' version.txt | tr -d '[:space:]'))
# The docs workflow passes DOCS_VERSION as an empty environment variable, which ?= treats as set.
ifeq ($(strip $(DOCS_VERSION)),)
DOCS_VERSION := $(if $(wildcard version.txt),$(shell sed -E 's/^([0-9]+\.[0-9]+).*/\1/' version.txt | tr -d '[:space:]'))
endif

.PHONY: docs-deps
docs-deps:
	python3 -m pip install $(DOCS_PIP_PACKAGES)

.PHONY: docs-serve
docs-serve:
	$(MKDOCS) serve

.PHONY: docs-build
docs-build:
	$(MKDOCS) build --strict

.PHONY: docs-verify
docs-verify: api-ref-docs docs-build
	git diff --exit-code docs/guide/spec.md || { echo "Run 'make api-ref-docs' and commit docs/guide/spec.md."; exit 1; }

.PHONY: docs-publish
docs-publish:
ifeq ($(strip $(DOCS_VERSION)),)
	$(error DOCS_VERSION is required, as MAJOR.MINOR, e.g. make docs-publish DOCS_VERSION=1.0. On a release branch it comes from version.txt)
endif
ifeq ($(DOCS_LATEST),true)
	$(MIKE) deploy --push --update-aliases $(DOCS_VERSION) latest
	$(MIKE) set-default --push latest
else
	$(MIKE) deploy --push --update-aliases $(DOCS_VERSION)
endif

.PHONY: test
test:
	go test ./...

.PHONY: fmt
fmt:
	go fmt ./...

# -tags e2e also vets test/e2e, like CI.
.PHONY: vet
vet:
	go vet -tags e2e ./...

.PHONY: lint
lint:
	staticcheck ./...

# Lint the chart with the webhook off (default) and on. clusterName is required with the webhook.
.PHONY: helm-lint
helm-lint:
	helm lint $(CHART_DIR) --set clusterName=lint
	helm lint $(CHART_DIR) --set clusterName=lint --set webhook.enabled=true

# Run the checks CI runs. Regenerates files first, so `git status` afterwards should show
# no changes to generated files.
.PHONY: ci
ci: generate manifests vet lint test helm-lint docs-verify

# Cut a release: set the version (version.txt, the chart, and the docs links), lint the chart,
# and build the release artifacts in $(RELEASE_DIR). Commit the result as "cut vX.Y.Z release"
# on the release branch. When it merges, .github/workflows/tag-release.yml creates the tag.
#   make release-cut VERSION=1.0.0      (no leading v)
# It sets the chart's image.tag to v$(VERSION), so publish the controller image as
# $(IMAGE_REPO):v$(VERSION).
IMAGE_REPO ?= public.ecr.aws/aws-elb-proxy/elb-envoy-remote-infra-provider
RELEASE_DIR = _output/release/v$(VERSION)

.PHONY: release-cut
release-cut:
ifndef VERSION
	$(error VERSION is required, e.g. make release-cut VERSION=1.0.0)
endif
	@grep -q -F "| v$(VERSION) |" docs/compatibility.md || { echo "docs/compatibility.md has no row for v$(VERSION). Add one to the version matrix, then run release-cut again." >&2; exit 1; }
	hack/set-version.sh $(VERSION)
	$(MAKE) helm-lint
	rm -rf $(RELEASE_DIR)
	mkdir -p $(RELEASE_DIR)
	helm package $(CHART_DIR) --destination $(RELEASE_DIR)
	cp $(CHART_CRD_DIR)/*.yaml k8s/iam-policy.json $(RELEASE_DIR)/
	sed "s|<tag>|v$(VERSION)|" k8s/envoy-gateway-values.yaml > $(RELEASE_DIR)/envoy-gateway-values.yaml
	@echo
	@echo "Release artifacts for v$(VERSION) are in $(RELEASE_DIR):"
	@ls $(RELEASE_DIR)
	@echo
	@echo "The chart pulls $(IMAGE_REPO):v$(VERSION). Next:"
	@echo "  1. Run 'make ci'. Update docs/compatibility.md and docs/deploy/installation.md if the supported versions changed."
	@echo "  2. git add version.txt charts docs && git commit -m 'cut v$(VERSION) release', then open a PR against main."
	@echo "  3. When it merges, the tag-release workflow creates the tag v$(VERSION)."
	@echo "  4. Create the branch release-$(word 1,$(subst ., ,$(VERSION))).$(word 2,$(subst ., ,$(VERSION))) from the tag v$(VERSION), check it out, and publish: LOGIN=1 hack/publish-image.sh, then LOGIN=1 hack/publish-chart.sh."
	@echo "  5. Attach $(RELEASE_DIR)/* to the GitHub release, and publish the docs."
# Show which version this checkout is at. version.txt is written by `make release-cut`.
.PHONY: version
version:
	@echo "version.txt:  $$(cat version.txt 2>/dev/null || echo 'none yet (written by make release-cut)')"
	@echo "chart:        $$(sed -n -E 's/^appVersion:[[:space:]]*"?([^"]*)"?[[:space:]]*$$/\1/p' $(CHART_DIR)/Chart.yaml)"
	@echo "build version: $(GIT_VERSION)"

# Install the code generation and lint tools at the versions CI uses (.github/workflows/ci.yml).
# The controller-gen version must match the annotation in the generated CRD.
CONTROLLER_GEN_VERSION ?= v0.19.0
CRD_REF_DOCS_VERSION ?= v0.2.0
STATICCHECK_VERSION ?= v0.8.2

.PHONY: tools
tools:
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)
	go install github.com/elastic/crd-ref-docs@$(CRD_REF_DOCS_VERSION)
	go install honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

# E2E test configuration.
#   E2E_ROLE_ARN           required — IAM role ARN for ELBProxyConfiguration.spec.roleArn
#   E2E_SUBNET_IDS         optional — comma-separated subnet IDs (else auto-discovery)
#   E2E_CONTROLLER_NAME    optional — controllerName of the GatewayClass each test creates
#   E2E_RUN_TEST           optional — run a single test by ShortName
E2E_FLAGS ?= -role-arn "$(E2E_ROLE_ARN)"
ifdef E2E_SUBNET_IDS
E2E_FLAGS += -subnet-ids "$(E2E_SUBNET_IDS)"
endif
ifdef E2E_CONTROLLER_NAME
E2E_FLAGS += -controller-name "$(E2E_CONTROLLER_NAME)"
endif
ifdef E2E_RUN_TEST
E2E_FLAGS += -run-test "$(E2E_RUN_TEST)"
endif


# Max e2e cases to run concurrently (go test -parallel).
E2E_PARALLEL ?= 4
E2E_FLAGS += -parallel $(E2E_PARALLEL)

# Overall go-test binary timeout
E2E_TIMEOUT ?= 240m

.PHONY: e2e
e2e:
ifndef E2E_ROLE_ARN
	$(error E2E_ROLE_ARN is required, e.g. make e2e E2E_ROLE_ARN=arn:aws:iam::123456789012:role/my-proxy-role)
endif
	# Target the single test package (./test/e2e, not ./test/e2e/...) so go test
	# streams the binary's stderr live. With a multi-package pattern, go test
	# buffers each package's output until it finishes, hiding live progress.
	go test -tags e2e -v -timeout $(E2E_TIMEOUT) ./test/e2e $(E2E_FLAGS)

.PHONY: e2e-build
e2e-build:
	# Build/vet all e2e packages (framework, tests, entry point).
	go test -tags e2e -c -o /dev/null ./test/e2e