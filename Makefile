.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
BINARY ?= bin/pal-media-server
GOLANGCI_LINT_VERSION ?= v2.14.0
LOCAL_GOLANGCI_LINT := bin/golangci-lint
COMPOSE := docker compose --env-file .env -f build/compose.yaml
GO_SOURCE_DIRS := app cmd pkg

.PHONY: help all check fmt fmt-check vet test lint build build-go build-web \
	web-install web-lint clean tidy update-dependencies compose-build compose-up compose-down

help: ## Show available targets
	@printf 'Usage: make [target]\n\nTargets:\n'
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: check build ## Run checks and build the application

check: fmt-check lint vet test web-lint build-web ## Run all backend and frontend checks

fmt: ## Format Go source files
	@files="$$(find $(GO_SOURCE_DIRS) -type f -name '*.go')"; \
	if [ -n "$$files" ]; then gofmt -w $$files; fi

fmt-check: ## Check Go source formatting
	@files="$$(find $(GO_SOURCE_DIRS) -type f -name '*.go' -exec gofmt -l {} +)"; \
	if [ -n "$$files" ]; then \
		printf 'The following files need gofmt:\n%s\n' "$$files"; \
		exit 1; \
	fi

vet: ## Run go vet
	$(GO) vet ./app/... ./cmd/...

test: ## Run Go tests
	$(GO) test ./app/... ./cmd/...

lint: $(LOCAL_GOLANGCI_LINT) ## Run the pinned golangci-lint version
	$(LOCAL_GOLANGCI_LINT) run ./app/... ./cmd/...

$(LOCAL_GOLANGCI_LINT):
	@mkdir -p $(dir $(LOCAL_GOLANGCI_LINT))
	GOBIN=$(CURDIR)/$(dir $(LOCAL_GOLANGCI_LINT)) $(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

build: build-go build-web ## Build the backend and frontend

build-go: ## Build the Go server
	@mkdir -p $(dir $(BINARY))
	$(GO) build -trimpath -o $(BINARY) ./cmd/server

web-install: ## Install locked frontend dependencies
	cd web && $(NPM) ci

web-lint: web-install ## Lint the frontend
	cd web && $(NPM) run lint

build-web: web-install ## Build the frontend
	cd web && $(NPM) run build

clean: ## Remove generated build artifacts
	$(GO) clean
	rm -rf bin web/dist

tidy: ## Normalize go.mod and go.sum
	$(GO) mod tidy

update-dependencies: ## Update direct and indirect Go dependencies
	$(GO) get -u ./...
	$(GO) mod tidy

compose-build: ## Build the local container images
	$(COMPOSE) build

compose-up: ## Start the local application stack
	$(COMPOSE) up -d --build

compose-down: ## Stop the local application stack without deleting data
	$(COMPOSE) down
