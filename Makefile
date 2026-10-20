# Makefile for pg-dump-dashboard.
#
# Frontend assets are compiled into internal/web/dist and embedded into the Go
# binary with go:embed, so `make build` (and every release build) runs the
# frontend build first.

SHELL := /bin/bash
BINARY := pg-dump-dashboard
MAIN := ./cmd/pg-dump-dashboard
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Version=$(VERSION) \
	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Commit=$(COMMIT) \
	-X github.com/fmotalleb/pg-dump-dashboard/internal/version.Date=$(BUILD_DATE)

# Integration test database (see test/integration/integration_test.go).
PGDD_TEST_PG_HOST ?= 127.0.0.1
PGDD_TEST_PG_PORT ?= 5432
PGDD_TEST_PG_USER ?= postgres
PGDD_TEST_PG_PASSWORD ?=
PGDD_TEST_PG_DATABASE ?= postgres

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'

.PHONY: deps
deps: ## Install frontend dependencies (npm ci)
	cd web && npm ci

.PHONY: frontend
frontend: ## Compile Tailwind CSS and copy assets into internal/web/dist
	cd web && npm run build

.PHONY: build
build: frontend ## Build the binary with the embedded frontend
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(MAIN)

.PHONY: run
run: build ## Run the dashboard (override flags with ARGS=...)
	./bin/$(BINARY) $(ARGS)

.PHONY: test
test: ## Run unit tests
	go test ./... -race -count=1

.PHONY: test-integration
test-integration: ## Run PostgreSQL integration tests against PGDD_TEST_PG_* settings
	PGDD_TEST_PG_HOST=$(PGDD_TEST_PG_HOST) \
	PGDD_TEST_PG_PORT=$(PGDD_TEST_PG_PORT) \
	PGDD_TEST_PG_USER=$(PGDD_TEST_PG_USER) \
	PGDD_TEST_PG_PASSWORD=$(PGDD_TEST_PG_PASSWORD) \
	PGDD_TEST_PG_DATABASE=$(PGDD_TEST_PG_DATABASE) \
	go test ./test/integration/... -count=1 -v

.PHONY: cover
cover: ## Produce a coverage report
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -n 1

.PHONY: fmt
fmt: ## Format Go sources
	gofmt -w .

.PHONY: lint
lint: ## Run golangci-lint with the repository configuration
	golangci-lint run ./...

.PHONY: lint-fix
lint-fix: ## Run golangci-lint and apply automatic fixes
	golangci-lint run --fix ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: check
check: fmt vet lint test ## Format, vet, lint and test

.PHONY: release-check
release-check: ## Validate the GoReleaser configuration
	goreleaser check

.PHONY: release-snapshot
release-snapshot: ## Build release artifacts locally without publishing
	goreleaser release --snapshot --clean

.PHONY: clean
clean: ## Remove build artifacts (keeps node_modules)
	rm -rf bin dist snapshot coverage.out
	rm -rf internal/web/dist
	mkdir -p internal/web/dist && touch internal/web/dist/.gitkeep
