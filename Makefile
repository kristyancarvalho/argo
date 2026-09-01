GO ?= go
GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint
BIN_DIR ?= bin
RUN_ARGS ?=

.PHONY: all build check ci format format-check lint run test test-e2e test-integration test-race test-unit vet

all: check

format:
	$(GOFMT) -w $$(find . -name '*.go' -type f)

format-check:
	test -z "$$($(GOFMT) -l .)"

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run

test-unit:
	$(GO) test ./tests/unit/...

test-integration:
	$(GO) test ./tests/integration/...

test-e2e:
	$(GO) test ./tests/e2e/...

test: test-unit test-integration test-e2e

test-race:
	$(GO) test -race ./...

build:
	mkdir -p $(BIN_DIR)
	$(GO) build -o $(BIN_DIR)/argo ./cmd/argo
	$(GO) build -o $(BIN_DIR)/argod ./cmd/argod
	$(GO) build -o $(BIN_DIR)/argo-qosd ./cmd/argo-qosd

run: build
	$(BIN_DIR)/argod $(RUN_ARGS)

check: format-check vet lint test test-race build

ci: check
