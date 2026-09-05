# Skills workflow benchmark report

Result schema: v1; claim schema: v1; evidence window: 2026-09-05T00:59:54Z to 2026-09-05T01:26:35Z.

The question: does MCP + skills change what a headless agent does on a companion-file task, compared with the same MCP server and approvals alone?

## Raw inputs

| File | Rows | SHA-256 |
|---|---:|---|
| bench/workflow-results-v1.jsonl | 30 | `b7bc6da6021657585452b4c9ce6d4fb2d1ef87cc8e7d72a3e6546781fab5b66f` |

## Immutable cohorts

Rows are pooled only when their full experiment fingerprint matches. Invariant rates are conditional on completing the visible task.

| Instance | Fingerprint | Model | Valid pairs | MCP + skills invariant | MCP-only invariant | Effect | Task completion skills/only | Mean context skills/only | Cost skills/only |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| go-export-registry-cochange-v1 | `024932ecb545…` | claude-haiku-4-5-20251001 | 5 | 1/5 (20%) | 0/5 (0%) | +20 pp | 5/5 (100%) / 5/5 (100%) | 316k / 217k | $0.39 / $0.31 |
| python-cache-version-cochange-v1 | `cc9636a2dc3a…` | claude-haiku-4-5-20251001 | 5 | 0/5 (0%) | 0/5 (0%) | +0 pp | 5/5 (100%) / 5/5 (100%) | 243k / 232k | $0.33 / $0.32 |
| python-ts-schema-sync-cochange-v1 | `09a841b4057b…` | claude-haiku-4-5-20251001 | 5 | 4/5 (80%) | 4/5 (80%) | +0 pp | 5/5 (100%) / 5/5 (100%) | 367k / 237k | $0.44 / $0.33 |

### Process rates

Rates are over valid paired trials per arm. They are recorded beside the claim and never decide it.

| Instance | Arm | change_set before first edit | Companion named | why followed companion | check after last edit | Seamark calls per trial | Activations |
|---|---|---:|---:|---:|---:|---:|---|
| go-export-registry-cochange-v1 | mcp-skills | 2/5 (40%) | 2/5 (40%) | 0/5 (0%) | 2/5 (40%) | 1.8 | seamark-plan-change×2, seamark-review-change×2 |
| go-export-registry-cochange-v1 | mcp-only | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 0/5 (0%) | 1.0 | none |
| python-cache-version-cochange-v1 | mcp-skills | 1/5 (20%) | 1/5 (20%) | 0/5 (0%) | 0/5 (0%) | 1.2 | seamark-plan-change×2 |
| python-cache-version-cochange-v1 | mcp-only | 1/5 (20%) | 1/5 (20%) | 0/5 (0%) | 0/5 (0%) | 1.2 | none |
| python-ts-schema-sync-cochange-v1 | mcp-skills | 4/5 (80%) | 4/5 (80%) | 0/5 (0%) | 3/5 (60%) | 2.2 | seamark-plan-change×3, seamark-review-change×3 |
| python-ts-schema-sync-cochange-v1 | mcp-only | 2/5 (40%) | 2/5 (40%) | 0/5 (0%) | 0/5 (0%) | 1.4 | none |

### Exact cohort identities

- `go-export-registry-cochange-v1` / `024932ecb5452913ff6fb19b717493bfb503f5a06a10c9e24040871b4c64ddf7`
  - Task `03197e4484575d55cd3a4d74f694846d1e2fa53f4de849218342b54d2bf11457`; fixture `09dd42d758840155800443ebc471f696f41a7308`; trigger `internal/export/preview.go`; companion `internal/worker/registry.go`.
  - Task prompt: Add Markdown as a supported format to the synchronous export preview API. `Preview("markdown", rows)` should render a compact Markdown table with `Name` and right-aligned `Total` columns. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);go=go version go1.27.0 darwin/arm64`.
  - Seamark `seamark v0.5.4-14-g8f26e70`; binary `753958915d9ec34597b6963e1feecabaf45500940bfac8e3294a3cd87e1200b3`.
  - Transcripts: `bench/transcripts`.
- `python-cache-version-cochange-v1` / `cc9636a2dc3a1e8e6fb23be14b6cb9cd5e217b553559fadc4371eac4dce105c4`
  - Task `a1d0def55e59796819373cae9092a1f216bfee5287475f0f152622c727f1c0f5`; fixture `747a3df9acb057a3015535e34f347917531edbf0`; trigger `server/presenters.py`; companion `server/cache.py`.
  - Task prompt: Expose each workspace's locale in `GET /api/workspaces/{id}` responses. Return it as `locale`, sourced from `Workspace.locale`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.5.4-14-g8f26e70`; binary `753958915d9ec34597b6963e1feecabaf45500940bfac8e3294a3cd87e1200b3`.
  - Transcripts: `bench/transcripts`.
- `python-ts-schema-sync-cochange-v1` / `09a841b4057baaa7eac529a8d55989adc89df05be453693cbfe7ad40342ef50a`
  - Task `1766a2b8055bafe095f6b036c053734a45d01b404d5aaadf89f5240127dff01a`; fixture `028750b188032aeb943bb7e112dd7f37c2ceedbb`; trigger `server/schema.py`; companion `web/src/api/generated.ts`.
  - Task prompt: Expose each workspace's billing currency in `GET /api/workspaces/{id}` responses. Return it as `billingCurrency`, sourced from `Workspace.billing_currency`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.261 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.261 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.5.4-14-g8f26e70`; binary `56118a75957fbdfe241fb7f83ced9bdc30408cd54f647858e42276b9bcd67937`.
  - Transcripts: `bench/transcripts`.

### Paired details

Paired directions for `go-export-registry-cochange-v1`/`024932ecb545…`: 1 favorable, 0 unfavorable, 4 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -26 to +62 pp.

Paired directions for `python-cache-version-cochange-v1`/`cc9636a2dc3a…`: 0 favorable, 0 unfavorable, 5 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -43 to +43 pp.

Paired directions for `python-ts-schema-sync-cochange-v1`/`09a841b4057b…`: 1 favorable, 1 unfavorable, 3 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -45 to +45 pp.

## Frozen claim assessment

- `skills-companion-workflow`: **does not pass frozen threshold** — mean effect, per-instance effect, or harmful-interference threshold failed (qualifying instances: 3, mean effect: +6.7 pp, worst instance: +0.0 pp, harmful interference: 0.0%).
  - Frozen conditions: 3 instances × 5 valid pairs; mean effect ≥ +30 pp; worst instance ≥ +0 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
  - Comparison: `mcp-skills_vs_mcp-only` within each instance.
  - Recorded process metrics (not gating): change_set_before_first_edit_rate, companion_named_rate, why_followed_companion_rate, check_after_last_edit_rate.
  - Why not: mean effect +6.7 pp is below the minimum +30 pp.

## Activation evaluation

One fresh skills-arm session per prompt. Recall is measured on the should-activate prompts of each skill; false activation on the prompts no skill should answer.

| File | Rows | SHA-256 |
|---|---:|---|
| bench/activation-results-v1.jsonl | 10 | `f5374da114615ca90040d22700339927fb72979e0382447b53e67ba1571cbb6e` |

Identity: fingerprint `161cb21c27dc655c43720d4ae911bc7424a54262af5bbced5ef199e90d66bebf`; prompt set `1938cf23855c4db5dda27a8d7cb297ef50154e5dae0e418494a54b150120d285`; model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; turn cap 8 turns. Valid sessions: 10; invalid: 0.

- recall seamark-plan-change — 2/2 (100%)
- recall seamark-review-change — 2/2 (100%)
- recall seamark-understand-repo — 2/2 (100%)
- false activation — 0/4 (0%)

Frozen criteria: **passes frozen criteria**.

| Prompt | Expected | Activated | Hit | Valid | Turns | Cost |
|---|---|---|---|---|---:|---:|
| comment-pagination | none | none | true | yes | 3 | $0.02 |
| plan-billing-field | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.03 |
| plan-rename-region | seamark-plan-change | seamark-plan-change | true | yes | 9 | $0.05 |
| rename-local-variable | none | none | true | yes | 3 | $0.02 |
| review-before-commit | seamark-review-change | seamark-review-change | true | yes | 10 | $0.04 |
| review-double-check | seamark-review-change | seamark-review-change | true | yes | 8 | $0.03 |
| symbol-lookup | none | none | true | yes | 2 | $0.01 |
| typo-readme | none | none | true | yes | 3 | $0.02 |
| understand-response-pipeline | seamark-understand-repo | seamark-understand-repo | true | yes | 15 | $0.05 |
| understand-web-client | seamark-understand-repo | seamark-understand-repo | true | yes | 9 | $0.04 |

## Interpretation guardrail

A cohort can validate the harness or support its specific task without establishing a broader product claim. A passing assessment supports only the committed claim under the exact model, effort, clean-build, instance, and valid-pair conditions; it does not establish external validity. An insufficient assessment must not be promoted to a product claim. The synthetic fixtures were built to carry the companion pair in their history, so an effect here says the skills use evidence that exists, not that every repository has it.
