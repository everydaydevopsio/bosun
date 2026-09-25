SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

AWS_SECRET ?= /ai-desktops/markcallen/agents
BRIDGECTL_VERSION ?= v1.3.0
DEV_IMAGE ?= bosun:dev

.PHONY: help deps setup env proto test lint build kind-up kind-down review clean

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
	@command -v node >/dev/null || { echo "node is required for env-secrets; install Node 20+" >&2; exit 1; }
	@command -v env-secrets >/dev/null || { echo "Installing env-secrets"; npm install -g env-secrets; }
	@command -v aws >/dev/null || { echo "aws CLI is required for env-secrets" >&2; exit 1; }
	@echo "All development tools present."

setup: deps ## Prepare the local shell environment
	@set -euo pipefail; \
	echo "Writing agent credentials to .env from $(AWS_SECRET)"; \
	env-secrets aws -s "$(AWS_SECRET)" -o .env >/dev/null; \
	chmod 600 .env
	@echo
	@echo "Ready. Load the environment into your shell with:"
	@echo
	@echo "    source ./scripts/activate.sh"
	@echo
	@echo "Then: make test, make kind-up, make review REPO=/path/to/repo"

env: ## Refresh .env from AWS Secrets Manager
	@env-secrets aws -s "$(AWS_SECRET)" -o .env >/dev/null && chmod 600 .env && echo "Refreshed .env"

proto: ## Regenerate Go gRPC bindings (requires protoc and plugins in GOPATH/bin)
	@PATH="$$(go env GOPATH)/bin:$$PATH" protoc --go_out=. --go_opt=module=github.com/everydaydevopsio/bosun --go_opt=Mproto/bridge/v1/bridge.proto=github.com/everydaydevopsio/bosun/internal/bridgev1 --go-grpc_out=. --go-grpc_opt=module=github.com/everydaydevopsio/bosun --go-grpc_opt=Mproto/bridge/v1/bridge.proto=github.com/everydaydevopsio/bosun/internal/bridgev1 proto/bridge/v1/bridge.proto

## ---------------------------------------------------------------- checks

test: ## Run the Go test suite
	go test ./...

lint: ## Lint shell scripts and the Helm chart
	gofmt -d $$(find cmd internal -name '*.go') | (! grep .)
	shellcheck -S warning scripts/*.sh
	helm lint charts/bosun
	helm template bosun charts/bosun --set ingress.enabled=true >/dev/null
	helm template bosun charts/bosun --set development.enabled=true >/dev/null

## ---------------------------------------------------------------- run

build: ## Build the Bosun image
	docker build --build-arg BRIDGECTL_VERSION=$(BRIDGECTL_VERSION) -t $(DEV_IMAGE) .

kind-up: ## Create the Kind cluster and install Bosun
	./scripts/kind-up.sh

kind-down: ## Delete the Kind cluster
	./scripts/kind-down.sh

REPO ?= $(PWD)
review: ## Review a repository locally (REPO=/path/to/repo)
	./scripts/review-local.sh $(REPO)

clean: ## Remove generated files
	go clean -testcache
