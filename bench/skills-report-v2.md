# Skills workflow benchmark report

Result schema: v1; claim schema: v1; evidence window: 2026-09-05T12:56:40Z to 2026-09-05T13:30:24Z.

The question: does MCP + skills change what a headless agent does on a companion-file task, compared with the same MCP server and approvals alone?

## Raw inputs

| File | Rows | SHA-256 |
|---|---:|---|
| bench/workflow-results-v2.jsonl | 30 | `997977634f5822b6bce7d2ce0d877f3cc243e013219744fbe3cecc2059807872` |

## Immutable cohorts

Rows are pooled only when their full experiment fingerprint matches. Invariant rates are conditional on completing the visible task.

| Instance | Fingerprint | Model | Valid pairs | MCP + skills invariant | MCP-only invariant | Effect | Task completion skills/only | Mean context skills/only | Cost skills/only |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| go-export-registry-cochange-v1 | `c0e44c616723…` | claude-haiku-4-5-20251001 | 5 | 4/5 (80%) | 0/5 (0%) | +80 pp | 5/5 (100%) / 5/5 (100%) | 491k / 286k | $0.58 / $0.37 |
| python-cache-version-cochange-v1 | `b31d94c2ce05…` | claude-haiku-4-5-20251001 | 5 | 4/5 (80%) | 0/5 (0%) | +80 pp | 5/5 (100%) / 5/5 (100%) | 440k / 227k | $0.52 / $0.32 |
| python-ts-schema-sync-cochange-v1 | `806496a9573e…` | claude-haiku-4-5-20251001 | 5 | 4/5 (80%) | 1/5 (20%) | +60 pp | 5/5 (100%) / 5/5 (100%) | 471k / 209k | $0.55 / $0.31 |

### Process rates

Rates are over valid paired trials per arm. They are recorded beside the claim and never decide it.

| Instance | Arm | change_set before first edit | Companion named | Named by check | Companion opened | why followed companion | check after last edit | Seamark calls per trial | Activations |
|---|---|---:|---:|---:|---:|---:|---:|---:|---|
| go-export-registry-cochange-v1 | mcp-skills | 5/5 (100%) | 4/5 (80%) | 1/5 (20%) | 4/5 (80%) | 0/5 (0%) | 5/5 (100%) | 2.8 | seamark-plan-change×5, seamark-review-change×5 |
| go-export-registry-cochange-v1 | mcp-only | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 1.0 | none |
| python-cache-version-cochange-v1 | mcp-skills | 5/5 (100%) | 5/5 (100%) | 1/5 (20%) | 5/5 (100%) | 0/5 (0%) | 4/5 (80%) | 2.0 | seamark-plan-change×5, seamark-review-change×4 |
| python-cache-version-cochange-v1 | mcp-only | 5/5 (100%) | 5/5 (100%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 2.0 | none |
| python-ts-schema-sync-cochange-v1 | mcp-skills | 5/5 (100%) | 5/5 (100%) | 1/5 (20%) | 5/5 (100%) | 0/5 (0%) | 5/5 (100%) | 2.2 | seamark-plan-change×5, seamark-review-change×5 |
| python-ts-schema-sync-cochange-v1 | mcp-only | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 1.0 | none |

### Exact cohort identities

- `go-export-registry-cochange-v1` / `c0e44c616723d1a8c9cc3e20654cab64e7b1d0b5c8972f7f8991d3e4320f9032`
  - Task `03197e4484575d55cd3a4d74f694846d1e2fa53f4de849218342b54d2bf11457`; fixture `0d71dfd3d563569363aebcc4b9550409b488459a`; trigger `internal/export/preview.go`; companion `internal/worker/registry.go`.
  - Task prompt: Add Markdown as a supported format to the synchronous export preview API. `Preview("markdown", rows)` should render a compact Markdown table with `Name` and right-aligned `Total` columns. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);go=go version go1.27.0 darwin/arm64`.
  - Seamark `seamark v0.5.4-18-g802295a`; binary `98b1a2ba5f18fef5d5ad4668a64070a30ee143b9081a65face43289925524e98`.
  - Transcripts: `bench/transcripts`.
- `python-cache-version-cochange-v1` / `b31d94c2ce054d651e825fa6aecd92e419572779d153dfb4be6e151ca8427763`
  - Task `a1d0def55e59796819373cae9092a1f216bfee5287475f0f152622c727f1c0f5`; fixture `f000958339e4f2ee4e918158d3c557d471fc4814`; trigger `server/presenters.py`; companion `server/cache.py`.
  - Task prompt: Expose each workspace's locale in `GET /api/workspaces/{id}` responses. Return it as `locale`, sourced from `Workspace.locale`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.5.4-18-g802295a`; binary `98b1a2ba5f18fef5d5ad4668a64070a30ee143b9081a65face43289925524e98`.
  - Transcripts: `bench/transcripts`.
- `python-ts-schema-sync-cochange-v1` / `806496a9573e148eb393f8f24d550b61c07af27cff559d302ca7af5caff04e0d`
  - Task `1766a2b8055bafe095f6b036c053734a45d01b404d5aaadf89f5240127dff01a`; fixture `ab7d97437cded1c83fcbfb873aee1d806f28356f`; trigger `server/schema.py`; companion `web/src/api/generated.ts`.
  - Task prompt: Expose each workspace's billing currency in `GET /api/workspaces/{id}` responses. Return it as `billingCurrency`, sourced from `Workspace.billing_currency`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.5.4-18-g802295a`; binary `1ea8e019a09d0365e22b055bb0185453f9a17d63577e56c540db2f8348df7153`.
  - Transcripts: `bench/transcripts`.

### Paired details

Paired directions for `go-export-registry-cochange-v1`/`c0e44c616723…`: 4 favorable, 0 unfavorable, 1 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +19 to +96 pp.

Paired directions for `python-cache-version-cochange-v1`/`b31d94c2ce05…`: 4 favorable, 0 unfavorable, 1 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +19 to +96 pp.

Paired directions for `python-ts-schema-sync-cochange-v1`/`806496a9573e…`: 3 favorable, 0 unfavorable, 2 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -0 to +83 pp.

## Frozen claim assessment

- `skills-companion-workflow`: **passes frozen threshold** — mean effect, per-instance effect, and harmful-interference thresholds pass (qualifying instances: 3, mean effect: +73.3 pp, worst instance: +60.0 pp, harmful interference: 0.0%).
  - Frozen conditions: 3 instances × 5 valid pairs; mean effect ≥ +30 pp; worst instance ≥ +0 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
  - Comparison: `mcp-skills_vs_mcp-only` within each instance.
  - Recorded process metrics (not gating): change_set_before_first_edit_rate, companion_named_rate, companion_named_by_check_rate, companion_opened_rate, why_followed_companion_rate, check_after_last_edit_rate.

## Activation evaluation

One fresh skills-arm session per prompt. Recall is measured on the should-activate prompts of each skill; false activation on the prompts no skill should answer.

| File | Rows | SHA-256 |
|---|---:|---|
| bench/activation-results-v2.jsonl | 19 | `f5a9f71b9774267563159ed7279a5c875a960192fc01c5b4091925741289ce81` |

Identity: fingerprint `189e610483c4fcfe04af99611936d011c7cec498b3a9036e0aa23f79e64643be`; prompt set `ef64726cc244dcead50bb42fbbf57c41260acec5e15ec760223d2ce3363e65e9`; model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; turn cap 8 turns. Valid sessions: 19; invalid: 0.

- recall seamark-plan-change — 5/5 (100%)
- recall seamark-review-change — 5/5 (100%)
- recall seamark-understand-repo — 5/5 (100%)
- false activation — 0/4 (0%)

Frozen criteria: **passes frozen criteria**.

| Prompt | Expected | Activated | Hit | Valid | Turns | Cost |
|---|---|---|---|---|---:|---:|
| comment-pagination | none | none | true | yes | 3 | $0.02 |
| plan-billing-field | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.04 |
| plan-fix-missing-region | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.03 |
| plan-rename-region | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.03 |
| plan-task-minimal | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.03 |
| plan-task-owner | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.04 |
| rename-local-variable | none | none | true | yes | 3 | $0.02 |
| review-before-commit | seamark-review-change | seamark-review-change | true | yes | 8 | $0.03 |
| review-before-pr | seamark-review-change | seamark-review-change | true | yes | 5 | $0.03 |
| review-double-check | seamark-review-change | seamark-review-change | true | yes | 8 | $0.03 |
| review-task-handover | seamark-review-change | seamark-review-change | true | yes | 5 | $0.02 |
| review-what-breaks | seamark-review-change | seamark-review-change | true | yes | 7 | $0.03 |
| symbol-lookup | none | none | true | yes | 2 | $0.01 |
| typo-readme | none | none | true | yes | 3 | $0.01 |
| understand-cochange-hubs | seamark-understand-repo | seamark-understand-repo | true | yes | 7 | $0.03 |
| understand-load-bearing | seamark-understand-repo | seamark-understand-repo | true | yes | 7 | $0.03 |
| understand-response-pipeline | seamark-understand-repo | seamark-understand-repo | true | yes | 16 | $0.05 |
| understand-sync-mechanism | seamark-understand-repo | seamark-understand-repo | true | yes | 9 | $0.04 |
| understand-web-client | seamark-understand-repo | seamark-understand-repo | true | yes | 4 | $0.02 |

## Interpretation guardrail

A cohort can validate the harness or support its specific task without establishing a broader product claim. A passing assessment supports only the committed claim under the exact model, effort, clean-build, instance, and valid-pair conditions; it does not establish external validity. An insufficient assessment must not be promoted to a product claim. The synthetic fixtures were built to carry the companion pair in their history, so an effect here says the skills use evidence that exists, not that every repository has it.
