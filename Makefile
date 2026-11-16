# Seamark build automation. CGO is required (tree-sitter bindings).

# A bare `make` builds the binary. The test runner below is a prerequisite,
# not the default goal.
.DEFAULT_GOAL := build

BINARY  := bin/seamark
MODULE  := github.com/seamark-dev/seamark
# VERSION lands in shell recipes and file names, and git permits tag
# names (v1;id, v1/rc) that would be shell syntax or nested paths there.
# The raw describe output is stripped to a safe character set INSIDE this
# one shell pipeline — it never crosses the make→shell boundary unfiltered.
# The release workflow overrides VERSION with the (validated) pushed tag.
VERSION := $(shell (git describe --tags --always --dirty 2>/dev/null || echo dev) | LC_ALL=C tr -cd 'A-Za-z0-9._+-')
LDFLAGS := -ldflags "-X $(MODULE)/internal/cli.version=$(VERSION)"

# Test builds use Go's own build cache by default, so ordinary `go test`
# result caching works the same way it does for a hand-run command. A
# restricted runner that cannot write that cache sets TEST_GOCACHE
# explicitly; an empty resolved path is an error, never a silent switch to
# another directory.
NORMALIZED_TMPDIR := $(patsubst %/,%,$(strip $(TMPDIR)))
TEST_CACHE_ROOT   := $(if $(NORMALIZED_TMPDIR),$(NORMALIZED_TMPDIR),/tmp)
TEST_GOCACHE      ?= $(shell go env GOCACHE)
ifeq ($(strip $(TEST_GOCACHE)),)
$(error TEST_GOCACHE is empty: go env GOCACHE returned no path; set TEST_GOCACHE explicitly)
endif

# Focused iteration: TEST_PKGS selects packages and TEST_RUN is one -run
# regex. TEST_RUN is passed as a single quoted argument, so write
# alternation without spaces: TEST_RUN='TestA|TestB'. A literal $ is
# written $$ because Make expands the recipe before the shell sees it.
# TEST_FLAGS is a trusted list of ordinary go test flags (for example
# TEST_FLAGS='-count=1 -p=2 -parallel=2' on a constrained host), not a
# second package selector.
TEST_PKGS    ?= ./...
TEST_RUN     ?=
TEST_FLAGS   ?=
TEST_TIMEOUT ?= 10m

# Compact output is the default: the compiled testsummary runner captures
# complete per-invocation logs and prints a bounded summary. RAW=1 runs
# plain `go test` with its ordinary output.
RAW ?=
TEST_RUNNER     := bin/testsummary
TEST_RUNNER_DEP := $(if $(RAW),,$(TEST_RUNNER))
TEST_QUIET      := $(if $(RAW),,@)
TEST_LOG_DIR    ?= $(TEST_CACHE_ROOT)/seamark-test-logs-$(shell id -u)

TEST_ENV     := CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)"
TEST_CMD     := $(if $(RAW),go test,$(TEST_RUNNER) -log-dir "$(TEST_LOG_DIR)" -slow 3 --)
TEST_GOFLAGS := -timeout $(TEST_TIMEOUT) $(TEST_FLAGS)
TEST_SELECT  := $(TEST_PKGS) $(if $(strip $(TEST_RUN)),-run '$(TEST_RUN)')

$(TEST_RUNNER): $(shell find cmd/testsummary -name '*.go' 2>/dev/null) go.mod
	$(TEST_QUIET)CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go build -o $(TEST_RUNNER) ./cmd/testsummary

# Release packaging: one archive per platform, built natively (CGO rules
# out cross-compiling from a single host — the release workflow runs this
# target on one runner per OS/arch).
DIST    := dist
GOOS    := $(shell go env GOOS)
GOARCH  := $(shell go env GOARCH)
ARCHIVE := seamark_$(VERSION)_$(GOOS)_$(GOARCH).tar.gz

.PHONY: build test test-race verify install lint fmt tidy index report clean release-archive smoke skills-validate agents-test lesson-check lesson-check-race report-bench tui-test tui-race tui-capture tui-capture-check tui-measure agents-native-check agents-oracle-record agents-native-smoke native-replay native-record lessons-bench lessons-bench-prepare lessons-bench-preflight lessons-bench-report skills-bench skills-bench-preflight skills-bench-report skills-activation

build: ## Build the seamark binary into ./bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY) ./cmd/seamark

install: ## Install seamark into ~/.local/bin
	CGO_ENABLED=1 go build $(LDFLAGS) -o $(HOME)/.local/bin/seamark ./cmd/seamark

test: $(TEST_RUNNER_DEP) ## Run tests (TEST_PKGS, TEST_RUN, TEST_FLAGS; RAW=1 for plain go test)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) $(TEST_SELECT)

test-race: $(TEST_RUNNER_DEP) ## Run tests with the race detector (same selectors as test)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) -race $(TEST_GOFLAGS) $(TEST_SELECT)

# The one full local gate. Each suite runs once, in order, even under
# make -j: the recursive invocations are recipe lines, not unordered
# siblings. A narrowing selector is rejected before any test starts.
verify: ## One full local gate: ordinary tests, race tests, lint, smoke
	@case '$(strip $(TEST_PKGS))' in './...') ;; *) echo "verify: TEST_PKGS must stay ./... (got '$(strip $(TEST_PKGS))'); use make test for focused runs" >&2; exit 2;; esac
	@[ -z '$(strip $(TEST_RUN))' ] || { echo "verify: TEST_RUN must be empty; use make test for focused runs" >&2; exit 2; }
	@[ -z '$(strip $(TEST_FLAGS))' ] || { echo "verify: TEST_FLAGS must be empty; use make test for focused flags" >&2; exit 2; }
	@$(MAKE) --no-print-directory test TEST_PKGS=./... TEST_RUN= TEST_FLAGS=
	@$(MAKE) --no-print-directory test-race TEST_PKGS=./... TEST_RUN= TEST_FLAGS=
	@$(MAKE) --no-print-directory lint
	@$(MAKE) --no-print-directory smoke

# Run integration and supporting packages in full, plus focused tests
# for setup, hooks, gates, state, and lesson inference in other packages.
# These tests run offline without credentials; native Codex checks are separate.
AGENTS_TEST_PKGS := ./internal/integration/... ./internal/agent/... ./internal/hooks/... ./internal/approve/... ./internal/skills/... ./internal/delivery/... ./internal/gate/... ./internal/doctor/... ./internal/status/...
AGENTS_TEST_RUN  := 'Test(RunInit|InitClient|InitDefault|InitGateMode|ReportSkills|LessonsHook|GateHook|HookBudgets|HookDelivery|RecordHookDelivery|ResetHookDelivery|ContextDelivery|Summarize|PrintFiringSummary|ApproveTools|Reminder|LessonsForFiles|MergeHooks|ResolveGateMode|LessonsDistill|LessonsExtractTriggers|ExtractTriggers|ResourcesAndPrompts)'

agents-test: $(TEST_RUNNER_DEP) ## Run the agent-integration contract and characterization tests
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) $(AGENTS_TEST_PKGS)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) ./internal/cli ./internal/reviews ./internal/report ./internal/mcp -run $(AGENTS_TEST_RUN)

# Lesson-check tests: the check configuration, the working-tree
# snapshot, both /1 checkers, and the verification surfaces. The new
# packages run whole: lessoncheck, and privatedir, which holds the
# private-directory and log helpers that lessoncheck shares with the
# policy gate and the MCP usage log.
# The CLI and the MCP server run only the tests whose names match
# LESSON_CHECK_RUN, because -run applies to every package of one line.
# Preparation (lessons --prepare, MCP change_set) and the exploration
# hook (lessons --explore --hook) run here too. Neither calls a model.
# LessonsHookNeverInvokes proves that the edit hook and the reset hook
# write their audit records offline. The full name stays here, because
# the prefix LessonsHook also selects the other TestLessonsHook tests.
# The pre-edit hooks (lessons --guard --hook and lessons --baseline
# --hook) run here too, through LessonsGuard and LessonsBaseline.
# lessonreport, the read-only engine of the delivery report (delivery/1),
# runs whole here too. It reads the reminder log and the lesson-check
# stream, so a change to either reader must fail this target.
# mcpusage runs whole, and MCPUsage and LessonsStats select the MCP tool
# counter and the stats section that reads it, because both write or read
# a local log beside the audit logs.
# Nothing here calls a model or the network.
# A new lesson-check surface adds its test-name prefix and package here.
LESSON_CHECK_PKGS := ./internal/lessoncheck/... ./internal/privatedir/... ./internal/lessonreport/... ./internal/mcpusage/...
LESSON_CHECK_RUN  := 'Test(LessonCheck|LessonsVerify|LessonsSetMode|CheckLessons|LessonsPrepare|LessonsExploreHook|ChangeSetPreparation|LessonsHookNeverInvokes|LessonsGuard|LessonsBaseline|LessonsDelivery|MCPUsage|LessonsStats)'

lesson-check: $(TEST_RUNNER_DEP) ## Run the lesson-check, guard, preparation, and delivery-report tests (offline, no model)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) $(LESSON_CHECK_PKGS)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) ./internal/cli ./internal/mcp -run $(LESSON_CHECK_RUN)

lesson-check-race: $(TEST_RUNNER_DEP) ## Run the lesson-check tests with the race detector
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) -race $(TEST_GOFLAGS) $(LESSON_CHECK_PKGS)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) -race $(TEST_GOFLAGS) ./internal/cli ./internal/mcp -run $(LESSON_CHECK_RUN)

# The fixture is synthetic and deterministic, so this target reads no real
# log and no file under .seamark. The numbers depend on the machine.
report-bench: ## Measure the delivery report build over synthetic audit logs (offline, no model)
	CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -run '^$$' -bench '^BenchmarkBuild' -benchmem ./internal/lessonreport

# Native checks run against the installed client CLI (CLIENT=codex or
# claude) and are separate from agents-test because they need the client
# on PATH. `check` is offline: no model, no credentials, a scratch client
# home; the Go tests it starts with are gated by SEAMARK_NATIVE_CLIENT and
# skip under plain `go test`. `smoke` runs one bounded distillation with
# the operator's own login, so it costs tokens and refuses to start
# without a verified login. A blocked check exits 3, never 0: missing
# evidence is not a pass. For Codex, `check` also runs 500 seeded
# Update patches through the installed apply_patch applier and the guard
# projection, and fails on any difference. See docs/agent-integrations.md.
CLIENT ?= codex

agents-native-check: build ## Offline checks against the installed $(CLIENT) CLI: version, exec surface, patch oracle, Update projection, sandbox, generated setup, offered tools, hook trust
	SEAMARK_NATIVE_CLIENT=$(CLIENT) CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -v -run '^TestNative' ./internal/integration
	scripts/agent-integration-smoke.sh check $(CLIENT) $(BINARY)

# The apply_patch oracles: the installed codex binary, run offline under
# the name apply_patch in scratch directories, applies each case, and the
# recorders write what it did. The content cases go into
# internal/integration/testdata/codex/apply_patch_oracle.json, and the
# cases with control characters in header paths go into
# apply_patch_path_control_oracle.json beside it. No model, no login.
# ORACLE_VERSION must name the installed version (for example 0.164.0),
# so a different installed version never rewrites a file by itself. The
# path cases keep their bytes. agents-native-check replays both.
agents-oracle-record: ## Record the apply_patch content and path-control oracles from the installed codex (offline; ORACLE_VERSION=<codex version>)
	@test -n "$(ORACLE_VERSION)" || { echo "ORACLE_VERSION must name the installed codex version, for example ORACLE_VERSION=0.164.0" >&2; exit 2; }
	SEAMARK_NATIVE_CLIENT=codex SEAMARK_ORACLE_RECORD="$(ORACLE_VERSION)" CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -v -run '^TestRecordCodex(Content|PathControl)Oracle$$' ./internal/integration

agents-native-smoke: build ## One bounded distillation through the installed $(CLIENT) CLI (costs tokens; needs your own login)
	scripts/agent-integration-smoke.sh smoke $(CLIENT) $(BINARY)

# Native timing, guard, and post-edit recordings for CLIENT=claude or
# codex. native-replay is offline: no model, no credentials, no client
# CLI. It runs the Go tests that replay the reviewed recordings in
# internal/integration/testdata/$(CLIENT)/timing, .../guard, and
# .../post-edit. Without a reviewed recording they skip as pending, and
# a skip gives no evidence. native-record runs the prompts of one
# scenario with the installed client and your own login, so it costs
# tokens. It prints one cost line per run. It refuses a binary or a tree
# with uncommitted changes unless SEAMARK_ALLOW_DIRTY=1. It writes a
# sanitized copy for review and never writes into testdata. A blocked
# check exits 3, never 0.
# NATIVE_RECORD_SCENARIO=timing (with native-record) runs the prompts of
# the timing scenario; it is the default.
# NATIVE_RECORD_SCENARIO=guard (with native-record) runs the sentinel
# prompts of the guard scenario in a scratch repository with enforce pins.
# NATIVE_RECORD_SCENARIO=post-edit (with native-record) runs the slow
# post-edit prompts in a scratch repository with check pins.
# NATIVE_RECORD_RUNS=0 (with native-record) sets up the scratch
# repository and runs no paid turn; a new Codex repository stops at the
# trust step.
# NATIVE_RECORD_RUN selects the tests whose names start with
# TestNativeRecord and the client name, such as the replays. A replay
# test without that prefix does not run here.
NATIVE_RECORD_RUN = '^TestNativeRecord$(if $(filter claude,$(CLIENT)),Claude,$(if $(filter codex,$(CLIENT)),Codex,$(error CLIENT must be claude or codex)))'

native-replay: build ## Offline: replay the reviewed $(CLIENT) timing, guard, and post-edit recordings (no model)
	SEAMARK_NATIVE_CLIENT=$(CLIENT) CGO_ENABLED=1 GOCACHE="$(TEST_GOCACHE)" go test -count=1 -v -run $(NATIVE_RECORD_RUN) ./internal/integration

native-record: build ## Record native timing, guard, or post-edit traces through the installed $(CLIENT) CLI (costs tokens; needs your own login)
	scripts/native-record.sh record $(CLIENT) $(BINARY)

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
# lessonreport is the delivery-report engine behind the Pins view. An
# engine regression behind a console view must fail this target too.
TUI_TEST_PKGS := ./internal/tui/... ./internal/workspace/... ./internal/history/... ./internal/scaffold/... ./internal/proposals/... ./internal/lessonstats/... \
	./internal/settings/... ./internal/learning/... ./internal/distill/... ./internal/reviews/... \
	./internal/index/... ./internal/store/... ./internal/agent/... ./internal/integration/... \
	./internal/yamledit/... ./internal/pinfile/... ./internal/maintain/... ./internal/outcome/... \
	./internal/report/... ./internal/delivery/... ./internal/lessonreport/...

# TUI_CLI_PARITY selects the CLI tests that pin the output of the
# commands behind the console: the console entry point, lessons (with
# the apply, dismiss, distillation, maintenance, extraction and stats
# golden tests), init, the evidence refresh of `seamark index
# --fixes-only` and `--reviews`, and the older retarget, extraction and
# ledger tests. These commands run on the engines of the console, so a
# change to an engine must keep both surfaces green. CLIWorkspace pins
# how the commands find and open the index through internal/workspace.
TUI_CLI_PARITY := Test(TUI|Lessons|RunInit|InitClient|IndexFixesOnly|IndexReviews|RetargetKeeps|ExtractSummary|SelectionParsing|Ledger(Shows|Names)|PlanAnnotations|CLIWorkspace)

tui-test: $(TEST_RUNNER_DEP) ## Run the workspace console tests (shell, screens, shared engines, CLI goldens)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) $(TUI_TEST_PKGS)
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) $(TEST_GOFLAGS) ./internal/cli -run '$(TUI_CLI_PARITY)'

# The console runs engine work on goroutines (refresh, distillation,
# saves, the selection sample), so its packages also run under the race
# detector. Every package of TUI_TEST_PKGS passes under the race
# detector, so the target runs all of them.
tui-race: $(TEST_RUNNER_DEP) ## Run the workspace console packages with the race detector
	$(TEST_QUIET)$(TEST_ENV) $(TEST_CMD) -race $(TEST_GOFLAGS) $(TUI_TEST_PKGS)

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
