# Seamark build automation. CGO is required (tree-sitter bindings).

BINARY  := bin/seamark
MODULE  := github.com/seamark-dev/seamark
# VERSION lands in shell recipes and file names, and git permits tag
# names (v1;id, v1/rc) that would be shell syntax or nested paths there.
# The raw describe output is stripped to a safe character set INSIDE this
# one shell pipeline — it never crosses the make→shell boundary unfiltered.
# The release workflow overrides VERSION with the (validated) pushed tag.
VERSION := $(shell (git describe --tags --always --dirty 2>/dev/null || echo dev) | LC_ALL=C tr -cd 'A-Za-z0-9._+-')
LDFLAGS := -ldflags "-X $(MODULE)/internal/cli.version=$(VERSION)"

# Test builds use an explicit writable cache. This avoids failures when a
# restricted macOS runner cannot access ~/Library/Caches/go-build.
TEST_CACHE_ROOT := $(if $(strip $(TMPDIR)),$(patsubst %/,%,$(TMPDIR)),/tmp)
TEST_GOCACHE    ?= $(TEST_CACHE_ROOT)/seamark-go-build-$(shell id -u)

# Release packaging: one archive per platform, built natively (CGO rules
# out cross-compiling from a single host — the release workflow runs this
# target on one runner per OS/arch).
DIST    := dist
GOOS    := $(shell go env GOOS)
GOARCH  := $(shell go env GOARCH)
ARCHIVE := seamark_$(VERSION)_$(GOOS)_$(GOARCH).tar.gz

.PHONY: build test lint fmt tidy index report clean release-archive smoke lessons-bench lessons-bench-prepare lessons-bench-preflight lessons-bench-report

build: ## Build the seamark binary into ./bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY) ./cmd/seamark

install: ## Install seamark into ~/.local/bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(HOME)/.local/bin/seamark ./cmd/seamark

test: ## Run all tests
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test ./...

test-race: ## Run all tests with the race detector
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -race ./...

lint: ## Static analysis (config in .golangci.yml)
	golangci-lint run ./...

fmt: ## Format all Go sources
	gofmt -w cmd internal

tidy: ## Sync go.mod/go.sum (-e: the tree-sitter grammar module's test files
	# reference a package that does not exist; plain tidy errors on it)
	go mod tidy -e

index: build ## Self-index this repository
	$(BINARY) index

report: index ## Self-index, then write .seamark/report.html
	$(BINARY) report

release-archive: build ## Package a versioned archive for this platform into ./dist
	@# Invariant check only — VERSION is sanitized at assignment (or
	@# overridden by the workflow after tag validation), so expanding it
	@# here is safe; an empty result must still refuse to package.
	@case '$(VERSION)' in (*[!A-Za-z0-9._+-]*|'') \
		echo 'release-archive: VERSION "$(VERSION)" is unsafe for file names and shell recipes' >&2; exit 1;; esac
	rm -rf "$(DIST)/seamark_$(VERSION)"
	mkdir -p "$(DIST)/seamark_$(VERSION)"
	cp $(BINARY) LICENSE README.md CHANGELOG.md "$(DIST)/seamark_$(VERSION)/"
	tar -czf "$(DIST)/$(ARCHIVE)" -C $(DIST) "seamark_$(VERSION)"
	rm -rf "$(DIST)/seamark_$(VERSION)"
	@echo "$(DIST)/$(ARCHIVE)"

smoke: build ## End-to-end smoke test of the built binary in a fresh fixture repo
	scripts/release-smoke.sh $(BINARY)

lessons-bench: build ## Controlled headless agent experiment (costs tokens; BENCH_FLAGS=-dry-run first)
	go run ./cmd/lessons-bench $(BENCH_FLAGS)

BENCH_INSTANCE ?= opentelemetry-go-histogram-reset-v1

lessons-bench-prepare: ## Fetch and verify one public benchmark source for offline trials
	go run ./cmd/lessons-bench-prepare -instance "$(BENCH_INSTANCE)"

lessons-bench-preflight: build ## Validate every benchmark fixture without buying agent sessions
	go run ./cmd/lessons-bench -instance all -preflight-only -agent "$$(command -v true)" $(BENCH_FLAGS)

lessons-bench-report: ## Render selected JSONL evidence (BENCH_RESULTS="bench/file.jsonl ..."; stdout by default)
	go run ./cmd/lessons-bench-report -claims bench/claims.yaml $(BENCH_REPORT_FLAGS) $(BENCH_RESULTS)

clean: ## Remove build artifacts and the local index
	rm -rf bin dist .seamark
