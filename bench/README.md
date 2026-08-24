# Lessons benchmark operations

The benchmark runs coding agents in fresh worktrees created from either local
synthetic fixtures or an exact pinned public-repository commit. Source code is
never pasted into the task prompt: the agent receives the frozen task and
reads the repository through its normal filesystem tools.

## Safe run sequence

Prepare the public OpenTelemetry-Go source once. This explicit step is the only
part of the public-repository workflow that uses the network: it fetches the
exact commit, vendors the nested Go module, verifies the frozen dependency-tree
digest, and stores the result in the user cache. The repair-scoped variant uses
the same cache.

```sh
make lessons-bench-prepare BENCH_INSTANCE=opentelemetry-go-histogram-reset-v1
```

Then build Seamark and run every no-agent preflight:

```sh
make lessons-bench-preflight
make lessons-bench-preflight BENCH_FLAGS='-hook-delivery once-per-context'
```

The first paid step is a single hook-off calibration for one instance. It asks
whether a capable unassisted agent still misses the owner-specific invariant:

```sh
make lessons-bench BENCH_FLAGS='-instance python-cache-version-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery always -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-cache-calibration-v7.jsonl -transcripts /tmp/seamark-cache-calibration'
```

Proceed only if the row is valid, the visible task passes, and the invariant
fails (`task=true invariant=false`). Inspect the transcript and patch before
calibrating the next instance:

```sh
make lessons-bench BENCH_FLAGS='-instance go-export-registry-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery always -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-export-calibration-v7.jsonl -transcripts /tmp/seamark-export-calibration'
```

Do not increase the trial count or run hook-on until both tasks have a useful
baseline ceiling. Calibration artifacts belong in `/tmp` and are not release
evidence. The default is intentionally one trial per arm, not ten.

The default Claude adapter:

- requires an exact model ID and pinned effort;
- selects only the project setting source and disables user plugins, MCP
  servers, skills, browser integration, memory, and session persistence;
- exposes only Read, Edit, Write, and Bash;
- enables Claude Code's native OS sandbox with hard failure when unavailable,
  repository-only writes, no unsandboxed escape hatch, and no approved network
  domains for Bash subprocesses;
- moves Go, Python, and Node caches into the trial directory and disables Go
  dependency downloads;
- removes inherited provider-routing, helper-model, thinking-budget, and prompt-
  caching overrides while preserving existing OAuth and API-key authentication;
- tells agents to leave trial changes uncommitted and unstaged for evaluation;
- caps provider spend per session and preserves the complete stream transcript.

The Claude process retains the operator's `HOME` and `CLAUDE_CONFIG_DIR` so
existing keychain, subscription, and API-key authentication keeps working. Its
shell and hook subprocesses run through a wrapper with disposable `HOME` and XDG
configuration paths, keeping language tools such as Go away from operator state.
`--setting-sources project` keeps ordinary user instructions out of the session,
but organization-managed policy and global account state remain host
prerequisites. Use a dedicated runner when publishing evidence intended to
reproduce across operators.

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

- `python-ts-schema-sync-repair-v1` is the matched trigger-extraction control.
  It reuses the schema-sync task, fixture, judges, patches, checks, and lesson
  text, but scopes the lesson to the generated TypeScript repair directory
  instead of the backend trigger directory. Its hooked arm declares exposure
  optional: a correctly wired hook that sees no matching editor operation is
  valid experimental data, not an infrastructure failure. Reports compare the
  two variants only when their shared protocol fingerprints match.

- `python-cache-version-v1` asks for another Python API response field. Public
  checks cover the response, while the owner invariant requires bumping a
  response-cache version learned from a prior analogous change in git history.
- `go-export-registry-v1` asks for a Go Markdown preview renderer. Public checks
  cover synchronous previews, while the owner invariant requires registering
  the new format in a separate asynchronous worker registry. This supplies an
  independent language and failure mechanism rather than another schema-sync
  variant.

- `opentelemetry-go-histogram-reset-v1` checks out OpenTelemetry-Go at commit
  `0eb89a5210e64df2f38611b95d1ae0afd6b88fd7`, derived from
  [issue #8399](https://github.com/open-telemetry/opentelemetry-go/issues/8399)
  and [PR #8403](https://github.com/open-telemetry/opentelemetry-go/pull/8403).
  The visible task fixes stale fields when an explicit-bucket histogram reuses
  a data point. The owner invariant requires the parallel fix in both delta and
  cumulative exponential-histogram collection. Its trigger-scoped lesson
  matches the explicit implementation the task leads an agent to edit. The
  task names the module-local offline test command so repository-layout
  discovery does not become an unrelated source of task failure.

- `opentelemetry-go-histogram-reset-repair-v1` is the protocol-matched public
  control. It changes only the lesson region, moving it to the exponential
  repair file. Zero hook exposure is valid for this control. Both public
  variants use a prepared, content-verified vendor tree and run trials with Go
  downloads disabled; the paid agent never receives network access.

Synthetic fixtures are generated locally from frozen source and git history;
the public fixture is cloned from its verified local cache. No private
repository is read, copied, or required at run time. Prepare the public cache
before using `-instance all`. That selector is deliberately restricted to
preflight and dry-run modes; paid instances must be selected explicitly.

### Trigger-scope calibration

Run this comparison only from a reviewed, committed tree: the frozen claim
rejects dirty Seamark builds. Start with one paired trial for each variant,
using identical agent, budget, timeout, arms, and delivery policy:

```sh
make lessons-bench BENCH_FLAGS='-instance python-ts-schema-sync-v1 -arm both -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery once-per-context -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-trigger-scope-calibration-v7.jsonl -transcripts /tmp/seamark-trigger-scope-calibration-v7'

make lessons-bench BENCH_FLAGS='-instance python-ts-schema-sync-repair-v1 -arm both -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery once-per-context -max-budget-usd 0.25 -timeout 10m -out /tmp/seamark-repair-scope-calibration-v7.jsonl -transcripts /tmp/seamark-repair-scope-calibration-v7'
```

Compare the value printed as `fingerprint` by the trigger run with the value
printed as `protocol` by the repair run; they must be equal. The trigger run
does not print a separate protocol line because its protocol and full
fingerprint are identical. Its hook-on row must prove at least one injection.
The repair-scoped hook-on row may validly report zero matches and zero
injections; that absence is the control behavior, not an infrastructure
failure. Generate the diagnostic report from both files:

```sh
make lessons-bench-report \
  BENCH_RESULTS='/tmp/seamark-trigger-scope-calibration-v7.jsonl /tmp/seamark-repair-scope-calibration-v7.jsonl' \
  BENCH_REPORT_FLAGS='-out /tmp/seamark-trigger-scope-calibration-v7.md'
```

One pair validates wiring and direction only. Inspect both patches and
transcripts before authorizing the frozen five-pair cohort.

### Pinned public-repository calibration

The OpenTelemetry pair follows the same trigger-versus-repair protocol on real,
historical source. Run it only from a reviewed, committed tree. Start with a
single hook-off trigger trial so an unexpectedly easy or impossible visible
task does not consume a full cohort. The $0.90 session ceiling leaves room for
realistic test development on this larger codebase; it is a cap, not a spending
target, and must remain identical across both arms and scope variants:

```sh
make lessons-bench BENCH_FLAGS='-instance opentelemetry-go-histogram-reset-v1 -arm hook-off -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery once-per-context -max-budget-usd 0.90 -timeout 10m -out /tmp/seamark-otel-trigger-calibration-v7.jsonl -transcripts /tmp/seamark-otel-trigger-calibration-v7'
```

The useful baseline is `task=true invariant=false`. Inspect its patch and
transcript before running one paired trial for each variant:

```sh
make lessons-bench BENCH_FLAGS='-instance opentelemetry-go-histogram-reset-v1 -arm both -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery once-per-context -max-budget-usd 0.90 -timeout 10m -out /tmp/seamark-otel-trigger-pilot-v7.jsonl -transcripts /tmp/seamark-otel-trigger-pilot-v7'

make lessons-bench BENCH_FLAGS='-instance opentelemetry-go-histogram-reset-repair-v1 -arm both -trials 1 -model claude-haiku-4-5-20251001 -effort medium -hook-delivery once-per-context -max-budget-usd 0.90 -timeout 10m -out /tmp/seamark-otel-repair-pilot-v7.jsonl -transcripts /tmp/seamark-otel-repair-pilot-v7'
```

The trigger run's printed `fingerprint` must equal the repair run's printed
`protocol`, the trigger hook-on row must show an injection, and zero exposure is
valid for the repair hook-on row. These pilot artifacts remain in `/tmp`; do
not increase to the frozen five-pair cohort until the task, patches, transcript,
and diagnostic report have been reviewed:

```sh
make lessons-bench-report \
  BENCH_RESULTS='/tmp/seamark-otel-trigger-pilot-v7.jsonl /tmp/seamark-otel-repair-pilot-v7.jsonl' \
  BENCH_REPORT_FLAGS='-out /tmp/seamark-otel-pilot-v7.md'
```

## Preflight contract

Preflight spends no model tokens. It rejects a run unless:

1. two fresh fixture worktrees have the same commit hash;
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

Schema-v5 through schema-v7 rows separately record task success and
owner-invariant success. Schema v6 added matching hook
invocations, actual injections, repeated injections, fully suppressed matches,
and injected context bytes. Schema v7 adds the per-row hook-exposure
expectation and a protocol fingerprint shared by intentional experiment
variants. Reports accept frozen v5/v6 evidence but never mix schema versions
in one report.
Only rows with both `valid=true` and `pair_valid=true` enter arm tallies.
Provider errors, rate limits, missing structured output, unexpected plugins or
MCP servers, model mismatches, and non-firing required-exposure hooks are
excluded. A scope-control instance may explicitly permit zero exposure; its
preflight still proves that the hook and exact lesson were installed.
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

The trigger-extraction claim is a separate matched factorial experiment. It
compares the hook-on minus hook-off effect for the trigger-scoped instance with
the same effect for the repair-scoped control. Both cohorts must have the same
protocol fingerprint, which binds the shared task, fixture, judges, checks,
agent configuration, runtime, Seamark binary, budget, timeout, and delivery
policy. The frozen contrast is:

```text
(trigger-scoped hook-on - hook-off) - (repair-scoped hook-on - hook-off)
```

This benchmark measures the behavioral value of the trigger regions produced
by extraction. Deterministic extraction validation remains responsible for
proving that the production pipeline derives and stores those regions from
evidence rather than from the benchmark's hand-authored fixture.

`lessons-public-delivery-scoping` freezes the same +40-point matched contrast
for the pinned OpenTelemetry-Go trigger and repair variants, with five valid
pairs per variant. It is intentionally a one-repository claim: passing it would
show that the delivery mechanism transfers to this exact historical public
task under the pinned model/runtime, not that results generalize across public
repositories. The claim was committed before any paid OpenTelemetry run.

### Trigger-scope release cohort

The first clean trigger-scope cohort ran on 2026-08-18 with Seamark
`v0.4.0-3-gb5c1bd8`, Claude Haiku 4.5 at medium effort, once-per-context
delivery, and five valid pairs per variant:

| Variant               | Hook-on invariant | Hook-off invariant | Within-variant effect | Approx. 95% interval | Mean context on/off | Total cost on/off |
| --------------------- | ----------------: | -----------------: | --------------------: | -------------------: | ------------------: | ----------------: |
| Trigger-scoped        |               5/5 |                0/5 |               +100 pp |       +39 to +100 pp |         300k / 269k |     $0.42 / $0.39 |
| Repair-scoped control |               3/5 |                3/5 |                 +0 pp |        -46 to +46 pp |         270k / 336k |     $0.40 / $0.44 |

All 20 sessions completed the visible task, with no harmful task regression.
The matched difference-in-differences effect was +100 percentage points,
passing the frozen +40-point threshold. The trigger treatment recorded ten
matching edits, five injections, five suppressions, zero repeated injections,
and 2,209 injected bytes. The repair-scoped hook recorded zero matches and
zero injections, as expected: the agent never edited the generated-client
region where the old pin would have waited.

The control's spontaneous invariant success varied by trial, but its aggregate
rate was exactly 3/5 in both arms. Subtracting that baseline is why the claim
uses a protocol-matched factorial comparison rather than comparing raw
hook-on rates. Context usage also moved in opposite directions across the two
variants, so this cohort supports delivery effectiveness, not a token-saving
claim. Raw rows and the generated assessment are in
[`trigger-scope-release-v7.jsonl`](trigger-scope-release-v7.jsonl),
[`repair-scope-release-v7.jsonl`](repair-scope-release-v7.jsonl), and
[`trigger-scope-report-v7.md`](trigger-scope-report-v7.md).

This is controlled synthetic evidence for one cross-boundary invariant under
one model/runtime. It does not establish extraction precision on unseen
repositories or external validity.

### OpenTelemetry public-repository release cohort

The accepted clean public cohort ran on 2026-08-20 with Seamark
`v0.4.0-11-g67707a2`, Claude Haiku 4.5 at medium effort,
once-per-context delivery, and five valid pairs per scope variant. Both
variants used OpenTelemetry-Go commit
`0eb89a5210e64df2f38611b95d1ae0afd6b88fd7` with the same task, judges,
offline vendor tree, budget, timeout, and protocol fingerprint:

| Variant | Hook-on invariant | Hook-off invariant | Within-variant effect | Approx. 95% interval | Mean context on/off | Total cost on/off |
|---|---:|---:|---:|---:|---:|---:|
| Trigger-scoped | 5/5 | 0/5 | +100 pp | +39 to +100 pp | 1,360k / 1,340k | $1.61 / $1.49 |
| Repair-scoped control | 1/5 | 0/5 | +20 pp | -26 to +62 pp | 1,365k / 1,050k | $1.54 / $1.24 |

The matched difference-in-differences effect was +80 percentage points,
passing the frozen +40-point threshold. All 20 sessions completed the visible
task, so harmful task interference was 0%. The trigger treatment recorded five
matches and five injections—one per session, 3,060 bytes total. The
repair-scoped hook recorded two matches, one injection, one suppression, and
624 injected bytes: in one treatment run the agent independently reached the
repair file, so the late reminder could help finish the companion work. The
control's +20-point effect is therefore retained and subtracted rather than
discarded.

Trigger hook-on processed only 1.5% more context than hook-off in this cohort;
measured cost was about 8.0% higher, or $0.024 per treatment session. The
repair control had a much larger context imbalance even though four of its
five hook-on sessions received no injection. With five pairs, these noisy
usage figures support no general token- or cost-efficiency claim.

Calibration materially improved the protocol before this accepted run. One
earlier treatment agent implemented the invariant but added uncompilable tests
after failing to locate OpenTelemetry's nested Go module; the task now gives
both arms the exact offline verification command. A later attempt contained a
status-less provider interruption (`API Error: Connection closed
mid-response`) that the first classifier treated as an agent outcome. That
cohort was discarded, the classifier was fixed to exclude provider API errors
without conflating budget or turn exhaustion, and the complete experiment was
rerun from a new clean fingerprint. Neither calibration is pooled into the
release evidence.

The accepted raw rows and generated assessment are
[`otel-trigger-release-v7.jsonl`](otel-trigger-release-v7.jsonl),
[`otel-repair-release-v7.jsonl`](otel-repair-release-v7.jsonl), and
[`otel-report-v7.md`](otel-report-v7.md). This result establishes the
behavioral value of correct trigger placement for this pinned historical task;
the fixture uses a hand-authored lesson, so automatic extraction from the
upstream review and fix history remains a separate validation step. The
[OpenTelemetry case study](../docs/case-studies/opentelemetry-histogram-reset.md)
defines that experiment; the generic
[case-study protocol](../docs/case-studies/protocol.md) prevents its user-style
replay from being pooled into this controlled cohort.

Regenerate the committed assessment directly from its named raw inputs:

```sh
make lessons-bench-report \
  BENCH_RESULTS='bench/otel-trigger-release-v7.jsonl bench/otel-repair-release-v7.jsonl' \
  BENCH_REPORT_FLAGS='-out /tmp/seamark-otel-report-v7.md'

cmp /tmp/seamark-otel-report-v7.md bench/otel-report-v7.md
```

`result.schema.json` and `result-v6.schema.json` remain the frozen v5/v6
contracts. `result-v7.schema.json` documents current output and enforces the
implications JSON Schema can express. Arithmetic relations such as delivery
and token sums remain semantic constraints; the report command is the
normative validator for those and also refuses malformed data, duplicate input
files, duplicate trial arms, and conflicting identities that reuse a
fingerprint:

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

| Instance                   | Hook-on invariant | Hook-off invariant |  Effect | Approx. 95% interval | Mean context on/off | Total cost on/off |
| -------------------------- | ----------------: | -----------------: | ------: | -------------------: | ------------------: | ----------------: |
| `python-ts-schema-sync-v1` |               5/5 |                3/5 |  +40 pp |        -12 to +77 pp |         368k / 353k |     $0.47 / $0.46 |
| `python-cache-version-v1`  |               5/5 |                0/5 | +100 pp |       +39 to +100 pp |         279k / 253k |     $0.41 / $0.38 |
| `go-export-registry-v1`    |               5/5 |                0/5 | +100 pp |       +39 to +100 pp |         279k / 224k |     $0.42 / $0.37 |

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

Schemas v6 and v7 support `-hook-delivery always|once-per-context`. The latter
keeps repository-scoped session and canonical lesson-content digests in local
state, suppresses repeats under a cross-process lock, permits changed lessons,
resets after context compaction, expires inactive sessions after 24 hours, and
fails open when identity or state is unavailable. `always` remains the product
and benchmark default.

The first mechanism smoke test completed on 2026-08-09 with one valid
hook-on session: three matching edits produced one injection, two suppressions,
zero repeated injections, and 455 injected bytes; the visible task and owner
invariant both passed. A follow-up two-pair export-registry calibration produced
the same result in both treatment sessions: four matches, one injection, three
suppressions, zero repeats, and 455 bytes. Both hook-on sessions passed the
owner invariant, while both hook-off sessions completed the task but missed it.

For context, the earlier clean `always` release cohort and the new
`once-per-context` calibration observed:

| Delivery cohort                                   | Valid pairs | Hook-on invariant | Hook-off invariant | Hook-on delivery per session                     | Mean context on/off | Mean cost on/off |
| ------------------------------------------------- | ----------: | ----------------: | -----------------: | ------------------------------------------------ | ------------------: | ---------------: |
| `always` (schema v5, clean release)               |           5 |               5/5 |                0/5 | 3.6 firings; suppression not measured            |         279k / 225k |  $0.084 / $0.073 |
| `once-per-context` (schema v6, dirty calibration) |           2 |               2/2 |                0/2 | 4 matches; 1 injection; 3 suppressed; 0 repeated |         346k / 200k |  $0.094 / $0.069 |

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

Schema-v5 through schema-v7 rows include SHA-256 digests of the
transcript, stderr stream, and final patch, so local artifacts can be matched
to published rows without publishing their contents. Strict reporting requires
each artifact path and digest together. The earlier dirty schema-sync pilot
predates those digests and remains historical calibration rather than release
evidence. A release report is reproducible only from its named raw inputs and
committed claim registry.
