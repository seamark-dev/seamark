# Lessons benchmark operations

The benchmark runs coding agents in fresh, generated repositories. Source code
is never pasted into the task prompt: the agent receives the frozen task and
reads the fixture through its normal filesystem tools.

## Safe run sequence

Build Seamark and run every no-agent preflight first:

```sh
make lessons-bench-preflight
make lessons-bench-preflight BENCH_FLAGS='-hook-delivery once-per-context'
```

The first paid step is a single hook-off calibration for one instance. It asks
whether a capable unassisted agent still misses the owner-specific invariant:

```sh
make lessons-bench BENCH_FLAGS='-instance python-cache-version-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery always -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-cache-calibration-v6.jsonl -transcripts /tmp/seamark-cache-calibration'
```

Proceed only if the row is valid, the visible task passes, and the invariant
fails (`task=true invariant=false`). Inspect the transcript and patch before
calibrating the next instance:

```sh
make lessons-bench BENCH_FLAGS='-instance go-export-registry-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery always -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-export-calibration-v6.jsonl -transcripts /tmp/seamark-export-calibration'
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
9. hook-on receives the exact lesson, selected delivery policy, edit hook, and
   (for once-per-context delivery) compaction-reset hook.

## Result validity

Schema-v5 and schema-v6 rows separately record task success and
owner-invariant success. Schema v6 additionally records matching hook
invocations, actual injections, repeated injections, fully suppressed matches,
and injected context bytes. Reports accept frozen v5 evidence but never mix
schema versions in one report.
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

`result.schema.json` remains the frozen result-schema-v5 contract;
`result-v6.schema.json` documents current output. The report command also
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
pooling incompatible runs.

The first clean release cohort uses Seamark `v0.2.0-3-g6d5d87d`, Claude Haiku
`claude-haiku-4-5-20251001`, medium effort, and five valid pairs per instance:

| Instance | Hook-on invariant | Hook-off invariant | Effect | Approx. 95% interval | Mean context on/off | Total cost on/off |
|---|---:|---:|---:|---:|---:|---:|
| `python-ts-schema-sync-v1` | 5/5 | 3/5 | +40 pp | -12 to +77 pp | 368k / 353k | $0.47 / $0.46 |
| `python-cache-version-v1` | 5/5 | 0/5 | +100 pp | +39 to +100 pp | 279k / 253k | $0.41 / $0.38 |
| `go-export-registry-v1` | 5/5 | 0/5 | +100 pp | +39 to +100 pp | 279k / 224k | $0.42 / $0.37 |

All 30 sessions completed the visible task, so harmful task interference was
0%. The mean cross-instance effect was +80 percentage points and the worst
instance effect was +40 points; the cohort therefore passes the frozen
synthetic threshold. The schema-sync interval still includes zero, the corpus
contains only three generated fixtures, and all runs use one model/runtime, so
this is reproducible controlled evidence rather than external validation. The
earlier dirty three-pair schema-sync pilot remains historical calibration.

### Observed treatment cost

Across the 15 release pairs, hook-on processed 4.64 million context tokens
versus 4.16 million for hook-off: about +32k per session, or +11.4%. Measured
cost was $1.30 versus $1.20, about +8.4%. The increase is material and should
remain visible beside effectiveness. The literal lesson reminder is too small
to explain most of the gap by itself: each rendering was only 435–456 bytes.
The hook currently repeated it two times per schema run, three times per cache
run, and three or four times per export run. Hook-on also averaged 22.3 agent
turns versus 21.2 and was associated with inspection or updates of the
owner-specific companion surface. Because provider usage sums the growing
context processed across turns, an extra tool/agent turn can account for far
more tokens than the reminder text.

### Delivery-policy calibration

Schema v6 supports `-hook-delivery always|once-per-context`. The latter keeps
repository-scoped session and canonical lesson-content digests in local state,
suppresses repeats under a cross-process lock, permits changed lessons, resets
after context compaction, expires inactive sessions after 24 hours, and fails
open when identity or state is unavailable. `always` remains the product and
benchmark default.

The first mechanism smoke test completed on 2026-08-09 with one valid
hook-on session: three matching edits produced one injection, two suppressions,
zero repeated injections, and 455 injected bytes; the visible task and owner
invariant both passed. A follow-up two-pair export-registry calibration produced
the same result in both treatment sessions: four matches, one injection, three
suppressions, zero repeats, and 455 bytes. Both hook-on sessions passed the
owner invariant, while both hook-off sessions completed the task but missed it.

For context, the earlier clean `always` release cohort and the new
`once-per-context` calibration observed:

| Delivery cohort | Valid pairs | Hook-on invariant | Hook-off invariant | Hook-on delivery per session | Mean context on/off | Mean cost on/off |
|---|---:|---:|---:|---|---:|---:|
| `always` (schema v5, clean release) | 5 | 5/5 | 0/5 | 3.6 firings; suppression not measured | 279k / 225k | $0.084 / $0.073 |
| `once-per-context` (schema v6, dirty calibration) | 2 | 2/2 | 0/2 | 4 matches; 1 injection; 3 suppressed; 0 repeated | 346k / 200k | $0.094 / $0.069 |

This establishes that once-per-context suppression works without weakening the
lesson on this fixture. It does **not** establish a token or cost reduction:
the tiny calibration's hook-on sessions processed more context despite sending
the reminder once. The cohorts differ in size, harness schema, build identity,
and run date, and agent-turn variance dominates a 455-byte reminder. The
remaining evidence gap is a clean, balanced comparison of both policies under
the same model/runtime, with enough repetitions to estimate turns, context,
and cost. Until that evidence exists, no delivery-efficiency claim is made.
Delivery policy is part of the fingerprint, so `always` and
`once-per-context` are reported as distinct cohorts rather than being pooled.

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

Schema-v5 and schema-v6 rows include SHA-256 digests of the
transcript, stderr stream, and final patch, so local artifacts can be matched
to published rows without publishing their contents. Strict reporting requires
each artifact path and digest together. The earlier dirty schema-sync pilot
predates those digests and remains historical calibration rather than release
evidence. A release report is reproducible only from its named raw inputs and
committed claim registry.
