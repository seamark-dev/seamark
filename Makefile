# Seamark build automation. CGO is required (tree-sitter bindings).

BINARY  := bin/seamark
MODULE  := github.com/seamark-dev/seamark
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X $(MODULE)/internal/cli.version=$(VERSION)"

.PHONY: build test lint fmt tidy index clean

build: ## Build the seamark binary into ./bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY) ./cmd/seamark

test: ## Run all tests
	CGO_ENABLED=1 go test ./...

test-race: ## Run all tests with the race detector
	CGO_ENABLED=1 go test -race ./...

lint: ## Static analysis (config in .golangci.yml)
	golangci-lint run ./...

fmt: ## Format all Go sources
	gofmt -w cmd internal

tidy: ## Sync go.mod/go.sum (-e: the tree-sitter grammar module's test files
	# reference a package that does not exist; plain tidy errors on it)
	go mod tidy -e

index: build ## Self-index this repository
	$(BINARY) index

clean: ## Remove build artifacts and the local index
	rm -rf bin .seamark
