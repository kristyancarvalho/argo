GO ?= go
GOFMT ?= gofmt
GOLANGCI_LINT ?= golangci-lint

.PHONY: all build check ci format format-check lint test test-e2e test-integration test-race test-unit vet

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
	$(GO) build ./cmd/argo ./cmd/argod ./cmd/argo-qosd

check: format-check vet lint test test-race build

ci: check
