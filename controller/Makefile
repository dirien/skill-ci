CONTROLLER_GEN_VERSION ?= v0.23.0
CONTROLLER_GEN ?= $(shell go env GOPATH)/bin/controller-gen
IMG ?= ghcr.io/dirien/skill-ci-controller:dev

.PHONY: all
all: generate manifests fmt lint test build

$(CONTROLLER_GEN):
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate DeepCopy methods.
	$(CONTROLLER_GEN) object paths=./api/...

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate the SkillEval CRD into ../config/crd.
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=../config/crd

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: lint
lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test ./...

.PHONY: build
build:
	go build -o bin/manager ./cmd/manager

# A development image for linux/amd64. Releases are built by GoReleaser from Dockerfile.goreleaser.
.PHONY: image
image:
	docker build --platform linux/amd64 -t $(IMG) .
