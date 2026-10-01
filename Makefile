SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

BRIDGECTL_VERSION ?= v1.4.1
DEV_IMAGE ?= bosun:dev
# Stamped into the binary and image. Releases override this with the tag; local
# builds report the nearest tag plus commit, or "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: help deps setup proto test coverage lint build kind-up kind-down review clean

help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

## ---------------------------------------------------------------- tooling

deps: ## Install the tools needed for development
	@set -euo pipefail; \
	missing=(); \
	for t in docker kind kubectl helm shellcheck; do \
	  command -v $$t >/dev/null || missing+=("$$t"); \
	done; \
	if [ $${#missing[@]} -gt 0 ]; then \
	  if command -v brew >/dev/null; then \
	    echo "Installing: $${missing[*]}"; \
	    brew install "$${missing[@]}"; \
	  else \
	    echo "Missing tools and no Homebrew to install them: $${missing[*]}" >&2; \
	    echo "Install them with your package manager, then re-run 'make deps'." >&2; \
	    exit 1; \
	  fi; \
	fi
	@echo "All development tools present."

setup: deps ## Check development tools and show credential setup instructions
	@echo
	@echo "Tools ready. Set provider credentials manually (see docs/local-development.md)."
	@echo "Export credentials in your shell before running make kind-up."
	@echo
	@echo "Then: make test, make kind-up, make review REPO=/path/to/repo"

proto: ## Regenerate Go gRPC bindings (requires protoc and plugins in GOPATH/bin)
	@PATH="$$(go env GOPATH)/bin:$$PATH" protoc --go_out=. --go_opt=module=github.com/everydaydevopsio/bosun --go_opt=Mproto/bridge/v1/bridge.proto=github.com/everydaydevopsio/bosun/internal/bridgev1 --go-grpc_out=. --go-grpc_opt=module=github.com/everydaydevopsio/bosun --go-grpc_opt=Mproto/bridge/v1/bridge.proto=github.com/everydaydevopsio/bosun/internal/bridgev1 proto/bridge/v1/bridge.proto

## ---------------------------------------------------------------- checks

test: ## Run the Go test suite
	go test ./...

coverage: ## Run the Go test suite with a coverage profile
	go test ./... -covermode=atomic -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

lint: ## Lint shell scripts and the Helm chart
	gofmt -d $$(find cmd internal -name '*.go') | (! grep .)
	shellcheck -S warning scripts/*.sh
	helm lint charts/bosun
	helm template bosun charts/bosun --set ingress.enabled=true >/dev/null
	helm template bosun charts/bosun --set development.enabled=true >/dev/null

## ---------------------------------------------------------------- run

build: ## Build the Bosun image
	docker build --build-arg BRIDGECTL_VERSION=$(BRIDGECTL_VERSION) --build-arg VERSION=$(VERSION) -t $(DEV_IMAGE) .

kind-up: ## Create the Kind cluster and install Bosun
	./scripts/kind-up.sh

kind-down: ## Delete the Kind cluster
	./scripts/kind-down.sh

REPO ?= $(PWD)
review: ## Review a repository locally (REPO=/path/to/repo)
	./scripts/review-local.sh "$(REPO)" $(ARGS)

clean: ## Remove generated files
	go clean -testcache

.PHONY: cli install review-status
cli: ## Build the native Bosun CLI
	go build -ldflags "-X github.com/everydaydevopsio/bosun/internal/version.Version=$(VERSION)" -o bin/bosun ./cmd/bosun

install: ## Install Bosun into GOBIN or GOPATH/bin
	go install -ldflags "-X github.com/everydaydevopsio/bosun/internal/version.Version=$(VERSION)" ./cmd/bosun

.PHONY: release-check release-snapshot
release-check: ## Validate the GoReleaser config
	goreleaser check --config .goreleaser.yaml

release-snapshot: ## Build release archives locally without publishing or signing
	goreleaser release --snapshot --clean --config .goreleaser.yaml

review-status: ## Inspect a review (JOB=name, ARGS=--follow)
	go run ./cmd/bosun review-status "$(JOB)" $(ARGS)
