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
NORMALIZED_TMPDIR := $(patsubst %/,%,$(strip $(TMPDIR)))
TEST_CACHE_ROOT   := $(if $(NORMALIZED_TMPDIR),$(NORMALIZED_TMPDIR),/tmp)
TEST_GOCACHE      ?= $(TEST_CACHE_ROOT)/seamark-go-build-$(shell id -u)

# Release packaging: one archive per platform, built natively (CGO rules
# out cross-compiling from a single host — the release workflow runs this
# target on one runner per OS/arch).
DIST    := dist
GOOS    := $(shell go env GOOS)
GOARCH  := $(shell go env GOARCH)
ARCHIVE := seamark_$(VERSION)_$(GOOS)_$(GOARCH).tar.gz

.PHONY: build test lint fmt tidy index report clean release-archive smoke skills-validate agents-test tui-test tui-race tui-capture tui-capture-check tui-measure agents-native-check agents-native-smoke lessons-bench lessons-bench-prepare lessons-bench-preflight lessons-bench-report skills-bench skills-bench-preflight skills-bench-report skills-activation

build: ## Build the seamark binary into ./bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY) ./cmd/seamark

install: ## Install seamark into ~/.local/bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(HOME)/.local/bin/seamark ./cmd/seamark

test: ## Run all tests
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test ./...

test-race: ## Run all tests with the race detector
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -race ./...

# Run integration and supporting packages in full, plus focused tests
# for setup, hooks, gates, state, and lesson inference in other packages.
# These tests run offline without credentials; native Codex checks are separate.
AGENTS_TEST_PKGS := ./internal/integration/... ./internal/agent/... ./internal/hooks/... ./internal/approve/... ./internal/skills/... ./internal/delivery/... ./internal/gate/... ./internal/doctor/... ./internal/status/...
AGENTS_TEST_RUN  := 'Test(RunInit|InitClient|InitDefault|InitGateMode|ReportSkills|LessonsHook|GateHook|HookBudgets|HookDelivery|RecordHookDelivery|ResetHookDelivery|ContextDelivery|Summarize|PrintFiringSummary|ApproveTools|Reminder|LessonsForFiles|MergeHooks|ResolveGateMode|LessonsDistill|LessonsExtractTriggers|ExtractTriggers|ResourcesAndPrompts)'

agents-test: ## Run the agent-integration contract and characterization tests
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test $(AGENTS_TEST_PKGS)
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test ./internal/cli ./internal/reviews ./internal/report ./internal/mcp -run $(AGENTS_TEST_RUN)

# Native checks run against the installed client CLI (CLIENT=codex or
# claude) and are separate from agents-test because they need the client
# on PATH. `check` is offline: no model, no credentials, a scratch client
# home; the Go tests it starts with are gated by SEAMARK_NATIVE_CLIENT and
# skip under plain `go test`. `smoke` runs one bounded distillation with
# the operator's own login, so it costs tokens and refuses to start
# without a verified login. A blocked check exits 3, never 0: missing
# evidence is not a pass. See docs/agent-integrations.md.
CLIENT ?= codex

agents-native-check: build ## Offline checks against the installed $(CLIENT) CLI: version, exec surface, patch oracle, sandbox, generated setup
	SEAMARK_NATIVE_CLIENT=$(CLIENT) CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -v -run '^TestNative' ./internal/integration
	scripts/agent-integration-smoke.sh check $(CLIENT) $(BINARY)

agents-native-smoke: build ## One bounded distillation through the installed $(CLIENT) CLI (costs tokens; needs your own login)
	scripts/agent-integration-smoke.sh smoke $(CLIENT) $(BINARY)

# The console tests run offline against fixtures, a scripted service
# fake, a fake gh, and a fake agent. They cover the shell, the five
# destinations, the terminal lifecycle through tui.Run, the services
# behind the console, and the CLI entry point. No test contacts GitHub
# or runs a paid model. The services are the workspace and its root
# rule (workspace, history), setup (integration), proposals, statistics
# (lessonstats), settings, evidence refresh (learning, index, reviews,
# store), distillation (learning, distill, agent), and lesson
# maintenance: YAML splicing (yamledit), pin file editing (pinfile),
# retarget and prune (maintain), outcome readings (outcome), trigger
# extraction (learning, distill), and the lesson selection sample. The
# sample selects through the evidence read and the folds in report. It
# names hook paths with the path rule of delivery. So a change in report
# or delivery must fail this target too. The CLI does not call learning,
# because learning holds console-only policy. It calls the engines under
# learning. The index tests take about 25 s.
# They stay, because index holds the refresh engine and its stop
# guarantees, and a regression there must fail this target.
TUI_TEST_PKGS := ./internal/tui/... ./internal/workspace/... ./internal/history/... ./internal/scaffold/... ./internal/proposals/... ./internal/lessonstats/... \
	./internal/settings/... ./internal/learning/... ./internal/distill/... ./internal/reviews/... \
	./internal/index/... ./internal/store/... ./internal/agent/... ./internal/integration/... \
	./internal/yamledit/... ./internal/pinfile/... ./internal/maintain/... ./internal/outcome/... \
	./internal/report/... ./internal/delivery/...

# TUI_CLI_PARITY selects the CLI tests that pin the output of the
# commands behind the console: the console entry point, lessons (with
# the apply, dismiss, distillation, maintenance, extraction and stats
# golden tests), init, the evidence refresh of `seamark index
# --fixes-only` and `--reviews`, and the older retarget, extraction and
# ledger tests. These commands run on the engines of the console, so a
# change to an engine must keep both surfaces green. CLIWorkspace pins
# how the commands find and open the index through internal/workspace.
TUI_CLI_PARITY := Test(TUI|Lessons|RunInit|InitClient|IndexFixesOnly|IndexReviews|RetargetKeeps|ExtractSummary|SelectionParsing|Ledger(Shows|Names)|PlanAnnotations|CLIWorkspace)

tui-test: ## Run the workspace console tests (shell, screens, shared engines, CLI goldens)
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test $(TUI_TEST_PKGS)
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test ./internal/cli -run '$(TUI_CLI_PARITY)'

# The console runs engine work on goroutines (refresh, distillation,
# saves, the selection sample), so its packages also run under the race
# detector. Every package of TUI_TEST_PKGS passes under the race
# detector, so the target runs all of them.
tui-race: ## Run the workspace console packages with the race detector
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -race $(TUI_TEST_PKGS)

# Captures are plain-text renders of representative console states at
# several terminal sizes, for the visual review (they stay local). The
# default directory holds the Phase 4 set, which also renders every
# earlier scene; the MVP, Phase 2 and Phase 3 sets stay in
# rfc/validation/tui-mvp/captures, rfc/validation/tui-phase-2/captures
# and rfc/validation/tui-phase-3/captures.
TUI_CAPTURE_DIR ?= rfc/validation/tui-phase-4/captures

tui-capture: ## Render representative console states into $(TUI_CAPTURE_DIR) for visual review
	mkdir -p "$(TUI_CAPTURE_DIR)"
	SEAMARK_TUI_CAPTURE_DIR="$(abspath $(TUI_CAPTURE_DIR))" CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -run '^TestCapture' ./internal/tui

# The capture check proves that a console change keeps every frame. It
# renders the captures into a new temporary directory and compares them
# with BASE, after it masks dates, clock times and elapsed durations.
# It never writes into BASE. A gate needs a BASE rendered from the
# starting tree on the same day, for example with
# `make tui-capture TUI_CAPTURE_DIR=<scratch>`. BASE has no default.
# A plain `make tui-capture` overwrites the default TUI_CAPTURE_DIR, so a
# check against that directory can compare a render with itself. The
# script lists the first 10 names of each list; LIST=1 lists every name,
# for an expected-diffs record that must name each frame.
tui-capture-check: ## Render the captures into a temporary directory and compare them with BASE=<dir> (masks dates, times, durations; LIST=1 lists every name)
	@[ -n "$(BASE)" ] || { echo "usage: make tui-capture-check BASE=<dir> [LIST=1]; render BASE first with make tui-capture TUI_CAPTURE_DIR=<dir>" >&2; exit 2; }
	LIST="$(LIST)" CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" scripts/tui-capture-check.sh "$(BASE)"

# The Phase 4 measurement runs every benchmark named Phase4, each in the
# package that owns its subject:
#   tui       Lessons › Pins and the lessons Settings groups
#   ui        the note field of the pin form
#   services  the selection sample on the real engine
#   maintain  pin authoring, and the pin read behind each Pins load
#   settings  the lessons plan
#   report    the hook selection
# The fixtures come from internal/pinfile/pinfiletest. The script writes
# the raw output into MEASURE_OUT, after the Go version and the machine.
# It replaces MEASURE_OUT only after every run passes, so a failed or
# interrupted run keeps the old file. A failed run prints its output on
# stderr. The script checks the base before the first run, so a bad base
# stops it before any benchmark runs.
# limits.md takes its numbers from that file. MEASURE_BASE is the commit
# before Phase 4. The hook selection also runs on the report sources of
# that commit, so the file holds before and after (benchstat -col tree).
# MEASURE_BASE_DIR names a directory of those sources, for a tree
# without git. MEASURE_BENCHTIME=1x is a smoke run, and its numbers mean
# nothing. A default run takes about three minutes (Apple M1 Pro).
MEASURE_OUT       ?= rfc/validation/tui-phase-4/measure.txt
MEASURE_BENCHTIME ?=
MEASURE_COUNT     ?= 1
MEASURE_BASE      ?= 5b02c73
MEASURE_BASE_DIR  ?=
MEASURE_PKGS      := ./internal/tui ./internal/tui/ui ./internal/tui/services ./internal/maintain ./internal/settings ./internal/report

tui-measure: ## Run the Phase 4 benchmarks into $(MEASURE_OUT) (MEASURE_BENCHTIME=1x: smoke run only)
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" MEASURE_BENCHTIME="$(MEASURE_BENCHTIME)" MEASURE_COUNT="$(MEASURE_COUNT)" \
		MEASURE_BASE="$(MEASURE_BASE)" MEASURE_BASE_DIR="$(MEASURE_BASE_DIR)" \
		scripts/tui-measure.sh "$(MEASURE_OUT)" $(MEASURE_PKGS)

lint: ## Static analysis (config in .golangci.yml)
	golangci-lint run ./...

fmt: ## Format all Go sources
	gofmt -w cmd internal embed.go

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

# Local use only: the Go tests under internal/skills are the CI check for
# the skills tree; this runs the client's own validator on top of them.
skills-validate: ## Validate skills/ with Claude Code's validator (needs the claude CLI)
	@command -v claude >/dev/null 2>&1 || { \
		echo 'skills-validate: the claude CLI is not on PATH; install Claude Code (https://code.claude.com) to run it' >&2; \
		exit 1; }
	claude plugin validate --strict skills/

lessons-bench: build ## Controlled headless agent experiment (costs tokens; BENCH_FLAGS=-dry-run first)
	go run ./cmd/lessons-bench $(BENCH_FLAGS)

BENCH_INSTANCE ?= opentelemetry-go-histogram-reset-v1

lessons-bench-prepare: ## Fetch and verify one public benchmark source for offline trials
	go run ./cmd/lessons-bench-prepare -instance "$(BENCH_INSTANCE)"

lessons-bench-preflight: build ## Validate every benchmark fixture without buying agent sessions
	go run ./cmd/lessons-bench -instance all -preflight-only -agent "$$(command -v true)" $(BENCH_FLAGS)

lessons-bench-report: ## Render selected JSONL evidence (BENCH_RESULTS="bench/file.jsonl ..."; stdout by default)
	go run ./cmd/lessons-bench-report -claims bench/claims.yaml $(BENCH_REPORT_FLAGS) $(BENCH_RESULTS)

# The skills workflow experiment shares the lessons fixtures and plumbing but
# never a row file, a claim registry, or a fingerprint with the lessons
# benchmark; see bench/README.md, "Skills workflow benchmark".
skills-bench: build ## Paired MCP-only vs MCP + skills experiment (costs tokens; BENCH_FLAGS=-dry-run first)
	go run ./cmd/skills-bench $(BENCH_FLAGS)

skills-bench-preflight: build ## Validate every workflow fixture, its co-change pair, MCP, and arm wiring without buying agent sessions
	go run ./cmd/skills-bench -instance all -preflight-only -agent "$$(command -v true)" $(BENCH_FLAGS)

skills-bench-report: ## Render workflow JSONL evidence (BENCH_RESULTS="bench/file.jsonl ..."; BENCH_REPORT_FLAGS='-activation bench/activation-results-v1.jsonl')
	go run ./cmd/skills-bench-report -claims bench/workflow-claims.yaml $(BENCH_REPORT_FLAGS) $(BENCH_RESULTS)

skills-activation: build ## Replay the activation prompt set, one skills-arm session per prompt (costs tokens)
	go run ./cmd/skills-bench -activation bench/activation/prompts.yaml $(BENCH_FLAGS)

clean: ## Remove build artifacts and the local index
	rm -rf bin dist .seamark
