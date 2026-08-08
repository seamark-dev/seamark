# Lessons benchmark operations

The benchmark runs coding agents in fresh, generated repositories. Source code
is never pasted into the task prompt: the agent receives the frozen task and
reads the fixture through its normal filesystem tools.

## Safe run sequence

Build Seamark and run every no-agent preflight first:

```sh
make lessons-bench-preflight
```

The first paid step is a single hook-off calibration for one instance. It asks
whether a capable unassisted agent still misses the owner-specific invariant:

```sh
make lessons-bench BENCH_FLAGS='-instance python-cache-version-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-cache-calibration-v5.jsonl -transcripts /tmp/seamark-cache-calibration'
```

Proceed only if the row is valid, the visible task passes, and the invariant
fails (`task=true invariant=false`). Inspect the transcript and patch before
calibrating the next instance:

```sh
make lessons-bench BENCH_FLAGS='-instance go-export-registry-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-export-calibration-v5.jsonl -transcripts /tmp/seamark-export-calibration'
```

Do not increase the trial count or run hook-on until both tasks have a useful
baseline ceiling. Calibration artifacts belong in `/tmp` and are not release
evidence. The default is intentionally one trial per arm, not ten.

The default Claude adapter:

- requires an exact model ID and pinned effort;
- uses only project settings and disables user plugins, MCP servers, skills,
  browser integration, memory, and session persistence;
- exposes only Read, Edit, Write, and Bash;
- enables Claude Code's native OS sandbox with hard failure when unavailable,
  repository-only writes, no unsandboxed escape hatch, and no approved network
  domains for Bash subprocesses;
- moves Go, Python, and Node caches into the trial directory and disables Go
  dependency downloads;
- caps provider spend per session and preserves the complete stream transcript.

Do not use `--bare` or `--safe-mode` in a custom adapter: both disable the
project hook the treatment is meant to measure.

## Included instances

- `python-ts-schema-sync-v1` asks for a Python API response field. The visible
  backend implementation and public tests pass without touching the generated
  TypeScript client; the owner invariant requires the deterministic
  `make sync-api` output to be committed. Its synthetic git history includes a
  prior backend-only schema change and follow-up client refresh, making the
  lesson plausible repository knowledge rather than generic coding advice.
  This instance requires `python3` and `make`; preflight verifies both before
  any agent session is purchased.

- `python-cache-version-v1` asks for another Python API response field. Public
  checks cover the response, while the owner invariant requires bumping a
  response-cache version learned from a prior analogous change in git history.
- `go-export-registry-v1` asks for a Go Markdown preview renderer. Public checks
  cover synchronous previews, while the owner invariant requires registering
  the new format in a separate asynchronous worker registry. This supplies an
  independent language and failure mechanism rather than another schema-sync
  variant.

Each fixture is generated locally from frozen source and git history. No
private repository is read, copied, or required at run time. `-instance all`
is deliberately restricted to preflight and dry-run modes; paid instances must
be selected explicitly.

## Preflight contract

Preflight spends no model tokens. It rejects a run unless:

1. two generated repositories have the same commit hash;
2. the untouched repository passes its public checks but not the task judge;
3. a task-complete naive solution passes public checks and the task judge but
   fails the owner-invariant judge;
4. the external canonical solution passes task and invariant judges;
5. the canonical solution passes all public checks;
6. every repository check has a bounded per-command timeout;
7. the base fixture contains no Seamark or Claude treatment artifacts;
8. hook-off receives common sandbox settings but no hook or lesson; and
9. hook-on receives the exact lesson and hook configuration.

## Result validity

Schema-v5 rows separately record task success and owner-invariant success.
Only rows with both `valid=true` and `pair_valid=true` enter arm tallies.
Provider errors, rate limits, missing structured output, unexpected plugins or
MCP servers, model mismatches, and non-firing treatment hooks are excluded.
For a hooked arm, a firing counts only when the selected lesson identity,
edit-hook surface, edit tool, and configured file region all match. Treatment
or provider failures stop the batch before another paid session is started and
make the command exit unsuccessfully. Agent timeouts and agent-reported budget
or turn-limit errors remain measured outcomes rather than infrastructure
outages.

Token fields are not combined:

- `input_tokens` is fresh input;
- `cache_read_input_tokens` is cached context read again;
- `cache_creation_input_tokens` is context written to cache;
- `context_tokens` is their sum across the session.

Consequently, a large `context_tokens` value does not mean the fixture was sent
as one prompt. It normally represents repeated context traversal over many
agent turns.

Cost estimates only use valid prior rows with the exact same fingerprint. The
fingerprint binds the task, checks, agent command, model, effort, timeout,
runtime identity, exact Seamark binary digest, treatment and placebo content,
generated fixture HEAD, naive and gold patches, judge version, and embedded
execution, validation, and selected-instance sources. Adding a report or an
unrelated fixture does not invalidate an otherwise unchanged experiment.

## Frozen claim and reporting

`claims.yaml` freezes the initial lessons claim before the corpus is expanded:
at least three independent instances, at least five valid paired trials per
instance, a mean lift of at least 30 percentage points in invariant
success among task-complete runs, no negative per-instance effect, and no more
than 5% harmful task interference. Claim-level evidence must use the pinned
Haiku model at medium effort and a clean committed Seamark build.
A successful single fixture remains a fixture-specific result, not proof of the
cross-instance claim.

`result.schema.json` documents result schema v5. The report command also
strictly validates every row and refuses malformed data, invalid token totals,
duplicate input files, duplicate trial arms, and conflicting identities that
reuse a fingerprint:

```sh
make lessons-bench-report \
  BENCH_RESULTS='bench/schema-sync-release-v5.jsonl bench/cache-version-release-v5.jsonl bench/export-registry-release-v5.jsonl' \
  BENCH_REPORT_FLAGS='-out bench/lessons-report-v5.md'
```

The report records the SHA-256 of every raw input, keeps fingerprints in
separate immutable cohorts, reports paired direction and harmful interference,
includes an evidence window, exact experimental identities, and an approximate
95% score interval, and evaluates the committed threshold without silently
pooling incompatible runs. The committed three-pair
schema-sync pilot (`3/3` hook-on versus `0/3`
hook-off) validates that fixture and the treatment path only; it used a dirty
development binary and is below the frozen evidence floor.

## Artifact policy

Commit the claim registry, result schema, frozen fixture sources, selected
valid paired release JSONL, and the generated Markdown report. Do not commit
calibration runs, interrupted or rate-limited attempts, or exploratory tuning
outputs. Generate release evidence only after committing the harness so every
row names a clean, recoverable Seamark version. Full transcripts remain local
and ignored because they are large and
may contain generated repository content; artifact files are created
exclusively with `0600` permissions and a reused run ID is refused before an
agent session is purchased. Retain them securely for audit while the
corresponding release evidence is under review.

Schema-v5 rows created by the current harness include SHA-256 digests of the
transcript, stderr stream, and final patch, so local artifacts can be matched
to published rows without publishing their contents. Strict reporting requires
each artifact path and digest together. The earlier dirty schema-sync pilot
predates those digests and remains historical calibration rather than release
evidence. A release report is reproducible only from its named raw inputs and
committed claim registry.
