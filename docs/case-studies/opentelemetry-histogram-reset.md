# Teaching an agent an OpenTelemetry invariant

Status: complete. The first constrained distillation remains **partial**. The
fully captured default-agent prompt-v5 proposal **passed** and was applied
unchanged. The first replay completed the task and preserved the
repository-specific rule.

OpenTelemetry-Go once had a subtle state-reuse bug: a histogram datapoint could
retain `Sum`, `Min`, or `Max` from an earlier collection after those fields had
been disabled. The visible fix was small. The repository-specific rule was
broader: explicit and exponential histogram implementations needed equivalent
reset behavior.

This case study asks whether Seamark can turn the repository's accumulated fix
history into that reusable lesson, deliver it when a coding agent edits the
trigger file, and leave an auditable record.

The experiment follows the reusable [case-study evidence
protocol](protocol.md). That document defines isolation, retry, artifact, and
claim rules. This page tells the OpenTelemetry story and shows the commands and
outputs that users see.

## The mistake

[Issue #8399][issue-8399] reported that reused histogram datapoints could leak
stale optional values. [PR #8403][pr-8403] fixed the explicit-bucket delta path
and the corresponding delta and cumulative exponential-histogram paths.

Another nearby fix, [PR #8428][pr-8428], had already introduced destination
buffer reuse in a cumulative histogram path and explicitly cleared an optional
field. Once #8403 landed, the history contained two independent fix events
about safe histogram destination reuse.

That timing matters. Seamark is not expected to predict the first unseen bug.
Its job is to make knowledge from repeated mistakes available during the next
change.

## What we are testing

The case has two stages:

1. **Learn:** index the post-fix history and ask Seamark to propose a lesson
   from the two recurring fix findings.
2. **Replay:** put the accepted lesson into the exact pre-fix tree and give a
   coding agent the specific historical task.

The proposal is successful if it:

- says that reused destinations must clear optional fields not assigned by a
  later collection;
- connects explicit and exponential histogram behavior;
- cites both independent events; and
- is delivered when `sdk/metric/internal/aggregate/histogram.go` is edited and
  does not apply to unrelated code.

We preserve the raw proposal before deciding whether to apply it. If a user
must materially rewrite it, we report the result as partial and disclose the
edit.

This product walkthrough uses the built-in agent preset from `seamark init`.
It does not pin the distillation model to the benchmark model. The benchmark
provides the controlled comparison. The walkthrough shows what a normally
configured user sees.

## Two snapshots, one clone

We use one clone and two detached Git worktrees:

| Directory   | Commit                                     | Role                                            |
| ----------- | ------------------------------------------ | ----------------------------------------------- |
| `learning/` | `0a1d12fb662e80de7f6f17128efc3960c6dc121b` | #8403 has landed; both fix events are available |
| `replay/`   | `0eb89a5210e64df2f38611b95d1ae0afd6b88fd7` | Exact pre-fix tree used by the benchmark        |

The worktrees share Git objects but have independent Seamark indexes, lesson
files, hook state, and audit logs.

Run these commands from the directory where you want to create the scratch
experiment:

```bash
mkdir seamark-otel-case-study
cd seamark-otel-case-study

git clone https://github.com/open-telemetry/opentelemetry-go.git source

git -C source worktree add --detach ../learning \
  0a1d12fb662e80de7f6f17128efc3960c6dc121b

git -C source worktree add --detach ../replay \
  0eb89a5210e64df2f38611b95d1ae0afd6b88fd7

mkdir captures
```

Record the tools and immutable inputs:

```bash
{
  git --version
  seamark version
  claude --version

  git -C learning show -s --format='%H %cI %s' HEAD
  git -C replay show -s --format='%H %cI %s' HEAD
} 2>&1 | tee captures/00-environment.txt
```

Output:

```text
git version 2.50.1 (Apple Git-155)
seamark v0.4.0-13-g5ffbd13
2.1.220 (Claude Code)
0a1d12fb662e80de7f6f17128efc3960c6dc121b 2026-06-10T09:57:18-04:00 fix: clear stale histogram fields on datapoint reuse (#8403)
0eb89a5210e64df2f38611b95d1ae0afd6b88fd7 2026-06-10T09:37:37-04:00 Fix memory leak in exemplar reservoir by storing SpanContext instead of Context (#8389)
```

## Step 1: index what the maintainers learned

Initialize only the post-fix learning tree. Then mine the fix commits that are
reachable from its pinned `HEAD`. This mode uses only local data and is
deterministic. It does not query GitHub, so later review comments cannot enter
the historical experiment.

The learning worktree must have a fresh `.seamark/index.db`. Fix-only mining
keeps any review rows that are already in an index. It does not convert a mixed
production index into a fix-only index.

```bash
cd learning

seamark init 2>&1 | tee ../captures/01-learning-init.txt

seamark index --fixes-only 2>&1 \
  | tee ../captures/02-learning-index.txt

seamark doctor 2>&1 \
  | tee ../captures/03-learning-doctor.txt
```

Output:

```text
indexed .../seamark-otel-case-study/learning in 4.881s
  files    1475 seen, 915 parsed; 271 skipped (generated/excluded)
  symbols  9167
  edges    21167
  history  4710 decisions, 11836 co-change pairs
fixes: 863 commits scanned, 223 findings kept
  fixes    223 findings mined from fix commits

all checks passed
```

For the primary walkthrough, do not uncomment or replace the generated
`agent` section. The built-in Claude preset is the default:

```yaml
# agent:
#   cli: claude                  # the built-in preset (default)
#   argv: ["my-llm", "--print"]  # or any CLI reading a prompt on stdin
```

For this version of Seamark, the preflight resolves that preset to
`claude -p`. Claude Code can choose its default model and load its normal
project or user context. For this reason, we record the CLI version, the
effective command, and the reviewed repository instructions and settings. We
do not describe this walkthrough as a findings-only or model-pinned inference
experiment.

## Step 2: inspect before sending anything

The following `01`–`07` captures preserve the original constrained v4
diagnostic. Its preflight shows the custom command used for that run. The later
default-path publication run repeats the complete checkpoint with separate
capture names.

First, record the bounded lesson ledger. Fix findings feed distillation
directly, so this ledger can be empty. The next preflight defines exactly what
the model will receive:

```bash
seamark lessons --list \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/04-learning-ledger.txt
```

Then run a dry preflight. This step does not call the agent:

```bash
seamark lessons --distill --dry-run --limit 1 \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/05-distill-preflight.txt
```

Output excerpt from `captures/05-distill-preflight.txt` (the agent argv is
wrapped and its empty `--tools` value quoted for readability):

```text
no review lessons under sdk/metric/internal/aggregate — widen the region, or run `seamark index --reviews`

distill preflight — what leaves this machine
  agent     claude -p --model claude-haiku-4-5-20251001 --effort medium
            --max-budget-usd 0.25 --safe-mode --tools ""
            --disable-slash-commands --strict-mcp-config
            --mcp-config '{"mcpServers":{}}' --no-chrome
            --no-session-persistence
  payload   1 group(s), 2 finding(s), ~1.2k tokens
    sdk/metric/internal/aggregate   2 finding(s)  ~1.2k tokens
      - fix:conventional PR #8403  sdk/metric/internal/aggregate/exponential_histogram.go  (finding 728759585664303326)
      - fix:issue-link PR #8428  sdk/metric/internal/aggregate/histogram.go  (finding 5121900077751038754)

(dry run — nothing was sent; 1 group(s) remain unread; drop --dry-run to distill)
```

The preflight shows the agent command, evidence categories, group and finding
counts, source, pull request, path, and approximate prompt size. It does not
invoke Claude or print the finding bodies.

**Experiment checkpoint:** inspect the index, ledger, and preflight captures
before you continue. Confirm that the planned call contains the intended
two-finding group and no GitHub-review evidence. If it does not, stop and find
the cause before you pay for a different experiment.

## Step 3: distill one recurring pattern

After you accept the preflight, send exactly one evidence group:

```bash
seamark lessons --distill --limit 1 \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/06-distill-run.txt

seamark lessons --proposals 2>&1 \
  | tee ../captures/07-proposal.txt
```

Raw proposal:

```text
  p1    clear-reused-histogram-fields      sdk/metric/internal        2 findings cited [custom/v4]
        Explicitly clear all fields (Sum, Min, Max) when reusing histogram datapoints across cycles—don't assume conditionals will prevent stale values.
```

Classification: **partial**.

The proposal found a real reusable invariant and cited both independent fix
events. It did not pass the predefined case criteria because it:

- flattened the important relationship between the explicit and exponential
  histogram implementations into generic "histogram datapoints";
- did not mention that delta and cumulative reset paths must stay equivalent;
- returned no trigger path; and
- consequently retained the broad evidence-derived
  `sdk/metric/internal` region instead of the intended
  `sdk/metric/internal/aggregate/histogram.go` trigger.

We did not edit or apply this proposal. The raw output remains in
`captures/06-distill-run.txt` and `captures/07-proposal.txt`.

### What the partial result taught us

The partial result identified three general product problems:

- a fix finding stored its multi-file footprint, but the distillation prompt
  disclosed only its primary path;
- the fixed 1,500-character prefix of #8403 ended during the first
  exponential-delta hunk, before the companion cumulative and explicit
  production hunks; and
- prompt v4 described `trigger_paths` as optional and suggested omitting a
  same-evidence trigger—the exact answer this case needed.

Prompt v5 addresses those problems. It includes the complete relevant path
footprint. It shares a fixed evidence budget across the group and selects
distinct production hunks before repetitive test content. It preserves
repository-specific relationships and requires an explicit `trigger_paths`
answer. Verified cited triggers become exact delivery scopes. Evidence
coverage remains the fallback when no trigger can be verified.

We keep the v4 result and its original classification. We first reran prompt
v5 with the same constrained custom command. It recovered exact triggers for
both histogram implementations, but it again reduced the relationship to a
generic field-reset note. This result is a useful controlled diagnostic. It is
not the primary user walkthrough:

```text
  p1    histogram-slot-reuse-stale-fields  sdk/metric/internal/aggregate/histogram.go, sdk/metric/internal/aggregate/exponential_histogram.go 2 findings cited [custom/v5]
        When reusing histogram datapoint slots across collect cycles, explicitly clear Sum and Min/Max fields to prevent stale values from prior iterations from leaking into output.
```

Classification: **partial**. Trigger extraction passed; the note still failed
to say that explicit and exponential, delta and cumulative paths must remain
synchronized.

Next, a clean exploratory run kept the generated agent configuration
unchanged. The normal `claude -p` preset produced:

```text
  p1    reused-datapoint-field-reset       sdk/metric/internal/aggregate/histogram.go, sdk/metric/internal/aggregate/exponential_histogram.go 2 findings cited [claude/v5]
        When writing into reused metricdata datapoint slots, unconditionally set every optional field (Sum, Min, Max) on the else branch; stale values leak across cycles. Keep delta and cumulative, histogram and exponential_histogram, in sync.
```

That proposal passes the predefined content and scope criteria without a manual
rewrite. It also shows why the walkthrough should not inherit the benchmark's
small-model constraint. However, this result does **not** show that the model
alone caused the improvement. The default command can also change the tools,
project memory, and operator configuration. The controlled run and the default
walkthrough answer different questions.

The exploratory run did not retain the complete preflight and raw output. We
therefore use it only as a diagnostic. For the publication run, create a fresh
worktree, use the generated default agent configuration, and assign a new run
identity:

```bash
cd ..

git -C source worktree add --detach ../learning-default-v5 \
  0a1d12fb662e80de7f6f17128efc3960c6dc121b

cd learning-default-v5

seamark init 2>&1 | tee ../captures/06f-default-v5-learning-init.txt

seamark index --fixes-only 2>&1 \
  | tee ../captures/06g-default-v5-learning-index.txt

seamark doctor 2>&1 \
  | tee ../captures/06h-default-v5-learning-doctor.txt

seamark lessons --list \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/06i-default-v5-learning-ledger.txt

seamark lessons --distill --dry-run --limit 1 \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/06j-default-v5-distill-preflight.txt
```

Review the repository's `CLAUDE.md`, `AGENTS.md`, and `.claude/settings.json`.
The agent can read this project context. Review the preflight again. It must
resolve the agent to `claude -p` and show the same two events, the relevant
multi-file footprint, and the adaptive per-finding cap. Then run the first and
only publication attempt:

```bash
seamark lessons --distill --limit 1 \
  --region sdk/metric/internal/aggregate 2>&1 \
  | tee ../captures/06k-default-v5-distill-run.txt

seamark lessons --proposals 2>&1 \
  | tee ../captures/07c-default-v5-proposal.txt
```

The first captured publication proposal was:

```text
  p1    reused-datapoint-stale-fields      sdk/metric/internal/aggregate/histogram.go, sdk/metric/internal/aggregate/exponential_histogram.go 2 findings cited [claude/v5]
        When a collect path reuses metricdata datapoint slots across cycles, explicitly zero every conditionally-set field (Sum, Min, Max) in the else branch; apply the same fix to both delta and cumulative in histogram.go and exponential_histogram.go.
        trigger sdk/metric/internal/aggregate/histogram.go — directly cited by the evidence
        trigger sdk/metric/internal/aggregate/exponential_histogram.go — directly cited by the evidence
```

Classification: **pass**. The proposal met the unchanged content and scope
rubric without a manual edit. The raw run and proposal ledger are retained in
`captures/06k-default-v5-distill-run.txt` and
`captures/07c-default-v5-proposal.txt`.

## Step 4: make the decision

If the proposal passes review, allow Seamark to write the accepted proposal to
the lesson file. Set this option in `.seamark/config.yaml`:

```yaml
distill:
  write: true
```

We also select once-per-context hook delivery in `.seamark/lessons.yaml`:

```yaml
hook_delivery: once-per-context
```

The maintainer accepted `p1` exactly as generated:

```bash
export OTEL_PROPOSAL_ID=p1

seamark lessons --apply "$OTEL_PROPOSAL_ID" 2>&1 \
  | tee ../captures/08-default-v5-apply.txt

seamark lessons --proposals 2>&1 \
  | tee ../captures/09-default-v5-proposal-after-decision.txt

cp .seamark/lessons.yaml ../captures/accepted-lessons.yaml
```

Accepted lesson:

```yaml
hook_delivery: once-per-context

pin:
  # distilled by claude/v5 from 2 findings (seamark lessons --distill, p1)
  - rule: reused-datapoint-stale-fields
    region: sdk/metric/internal/aggregate/histogram.go
    regions:
      [
        sdk/metric/internal/aggregate/histogram.go,
        sdk/metric/internal/aggregate/exponential_histogram.go,
      ]
    note: When a collect path reuses metricdata datapoint slots across cycles, explicitly zero every conditionally-set field (Sum, Min, Max) in the else branch; apply the same fix to both delta and cumulative in histogram.go and exponential_histogram.go.
```

## Step 5: replay the historical task

The replay worktree starts with no Seamark state. Initialize it and build a new
index from the pre-fix tree. Copy only the reviewed lesson file. Do not copy
the learning database or proposal state:

```bash
cd ..

seamark -C replay init 2>&1 \
  | tee captures/10-replay-init.txt

shasum -a 256 replay/.claude/settings.json \
  | tee captures/10a-replay-project-settings.sha256

jq '
  if (.env? | type) == "object" then
    .env |= with_entries(.value = "<redacted>")
  else . end
  | walk(
      if type == "object" then
        with_entries(
          if (.key | test("token|secret|password|credential|authorization|api[_-]?key"; "i"))
          then .value = "<redacted>"
          else . end
        )
      else . end
    )
  | walk(if type == "string" then gsub("/Users/[^/]+"; "<home>") else . end)
' replay/.claude/settings.json \
  | tee captures/10b-replay-project-settings.sanitized.json

cp captures/accepted-lessons.yaml replay/.seamark/lessons.yaml

seamark -C replay index 2>&1 \
  | tee captures/10c-replay-index.txt

seamark -C replay lessons \
  --file sdk/metric/internal/aggregate/histogram.go 2>&1 \
  | tee captures/11c-trigger-preview.txt

test ! -e replay/.seamark/lessons-audit.jsonl
test ! -e replay/.seamark/lessons-hook-state.json
```

During preparation, the first preview failed because an earlier runbook draft
omitted the independent replay index. We kept that pre-agent failure in
`captures/11-trigger-preview.txt`. No coding session had started. The corrected
runbook above builds the replay index locally. Its successful preview is in
`captures/11c-trigger-preview.txt`.

**Settings checkpoint:** inspect the sanitized capture before you start
Claude. It must contain only the expected Seamark gate, lesson-delivery, and
context-reset hooks. Stop if it contains another hook, environment override,
permission, plugin, or unexplained setting. The raw-file digest identifies the
exact file that was reviewed without publishing credentials. The replay
command below selects only the `project` setting source. If the host applies an
organization-managed Claude policy, record it with the environment.

Trigger preview:

```text
seamark — review lessons for sdk/metric/internal/aggregate/histogram.go
- [pin] reused-datapoint-stale-fields — When a collect path reuses metricdata
  datapoint slots across cycles, explicitly zero every conditionally-set field
  (Sum, Min, Max) in the else branch; apply the same fix to both delta and
  cumulative in histogram.go and exponential_histogram.go.
```

Enter the pre-fix tree and start an ordinary coding session:

```bash
cd replay

claude \
  --model claude-haiku-4-5-20251001 \
  --effort medium \
  --permission-mode acceptEdits \
  --setting-sources project
```

We give it the same specific task as the benchmark:

```text
Fix the metric SDK so a reused destination datapoint from a delta
explicit-bucket histogram does not retain `Sum`, `Min`, or `Max` values from
an earlier aggregation when the later delta histogram has sum or min/max
recording disabled. The same destination aggregation may be reused across
collectors with different recording settings. Keep the change focused and add
or update tests as appropriate. Before finishing, verify the result with
`go -C sdk/metric test ./internal/aggregate`.
```

The agent's final message was retained in the reviewed [evidence
bundle](artifacts/opentelemetry-histogram-reset/README.md). It reported:

```text
Work Complete ✓

The metric SDK has been successfully fixed to prevent delta and exponential
histograms from retaining stale Sum, Min, and Max values when reusing
destination datapoints across collectors with different recording settings.
```

This is the agent's completion report, not verification evidence. The focused
patch, package-test output, independent behavior checks, and hook audit below
provide separate evidence. The bundle retains the complete captured message.

## Step 6: verify the code and the reminder

After the agent finishes, verify code behavior and hook delivery separately:

```bash
git status --short 2>&1 | tee ../captures/12-replay-status.txt

git diff -- \
  sdk/metric/internal/aggregate/histogram.go \
  sdk/metric/internal/aggregate/histogram_test.go \
  sdk/metric/internal/aggregate/exponential_histogram.go \
  sdk/metric/internal/aggregate/exponential_histogram_test.go \
  2>&1 | tee ../captures/13-replay.diff

go -C sdk/metric test ./internal/aggregate 2>&1 \
  | tee ../captures/14-replay-test.txt

seamark lessons --stats 2>&1 \
  | tee ../captures/15-lesson-stats.txt

jq . .seamark/lessons-audit.jsonl 2>&1 \
  | tee ../captures/16-lesson-audit.txt

jq . .seamark/lessons-hook-state.json 2>&1 \
  | tee ../captures/16a-lesson-hook-state.txt
```

Verification output:

```text
ok   go.opentelemetry.io/otel/sdk/metric/internal/aggregate

independent replay checks:
  PASS  explicit histogram destination reuse
  PASS  exponential histogram destination reuse / delta
  PASS  exponential histogram destination reuse / cumulative

hook delivery — instrumented: 1 injected (0 repeated), 2 suppressed;
context: 588 bytes
```

We first tried to call the benchmark's internal judge wrapper directly. That
check was invalid because the wrapper requires an offline module cache from
the benchmark preparer. The manual replay does not create that cache. We kept
the failed diagnostic in `captures/14a-replay-frozen-judge.txt`. We then ran
the same explicit and exponential behavior checks through a non-mutating Go
overlay in the replay's normal module environment. All three paths passed in
`captures/14c-replay-independent-task-invariant.txt`.

The audit establishes that the reminder reached the agent. A single replay
cannot establish that the reminder caused the companion change, which is why
we finish with paired evidence.

## Reviewed evidence bundle

The compact [public artifact set](artifacts/opentelemetry-histogram-reset/README.md)
contains the preflight, raw proposal, accepted lesson, replay command and task,
agent result, patch, verification output, lesson statistics, raw audit rows,
and sanitized project settings. `SHA256SUMS` lists a digest for each reviewed
file. Provider transcripts, absolute operator paths, and unrelated local
diagnostics are not published.

## What the controlled benchmark found

The accepted benchmark used the same pre-fix commit, task, model, effort, and
once-per-context policy in isolated agent sessions. Its treatment lesson was
written before this case study. It independently expresses the same
repository-specific rule as the successful distilled proposal:

| Scope               |                     Hook on | Hook off |             Difference |
| ------------------- | --------------------------: | -------: | ---------------------: |
| Trigger file        | 5/5 preserved the invariant |      0/5 | +100 percentage points |
| Repair-file control |                         1/5 |      0/5 |  +20 percentage points |

The matched difference-in-differences was **+80 percentage points**, above the
precommitted +40-point threshold. All 20 sessions completed the visible task,
and the trigger treatment recorded one lesson injection per session.

The [full benchmark report](../../bench/otel-report-v7.md) contains cohort
identities, raw input digests, uncertainty, cost, and limitations. The
[benchmark runbook](../../bench/README.md#pinned-public-repository-calibration)
documents its independent sandboxed procedure.

## Conclusion

This case completed the full learning-to-delivery path:

- OpenTelemetry accumulated two relevant fix events.
- Prompt v4 found the general invariant but missed the repository-specific
  relationship and trigger.
- Under the constrained custom command, prompt v5 recovered precise
  triggers but still flattened the synchronization rule.
- Both runs with Seamark's generated default agent preset captured the full
  invariant and precise scope without a manual rewrite.
- We cannot attribute the difference between the custom and default runs only
  to a model. The commands also expose different model-selection and context
  behavior.
- The maintainer, not the model, decided what became policy and applied the
  captured proposal unchanged.
- In the first replay, the agent fixed the visible explicit path and the
  companion exponential delta and cumulative paths; package tests and the
  independent behavior checks passed.
- The audit recorded one 588-byte injection on the first trigger edit and two
  correctly suppressed repeat matches in the same context.
- The paired, model-pinned benchmark supplies the causal comparison for this
  task.

The walkthrough shows that Seamark can recover, review, deliver, and audit this
repository-specific rule through its normal user workflow. One replay does not
show that the reminder caused the companion change. We also cannot attribute
the difference between the custom and default distillation runs to a model
alone. The paired benchmark, not the walkthrough, supports the causal delivery
claim.

[issue-8399]: https://github.com/open-telemetry/opentelemetry-go/issues/8399
[pr-8428]: https://github.com/open-telemetry/opentelemetry-go/pull/8428
[pr-8403]: https://github.com/open-telemetry/opentelemetry-go/pull/8403
