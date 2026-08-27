# Lessons benchmark report

Result schema: v7; claim schema: v1; evidence window: 2026-08-20T14:46:55Z to 2026-08-20T15:53:18Z.

## Raw inputs

| File | Rows | SHA-256 |
|---|---:|---|
| bench/otel-repair-release-v7.jsonl | 10 | `1d631fe309dc7444b9aeefed81040f5524a51d1598bac194b910990fc1450073` |
| bench/otel-trigger-release-v7.jsonl | 10 | `d3937158f9d172b64543fd6d4a60ea2ef963311f1563c42d35605e8f7b5aa119` |

## Immutable cohorts

Rows are pooled only when their full experiment fingerprint matches. Invariant rates are conditional on completing the visible task.

| Instance | Fingerprint | Model | Valid pairs | Hook-on invariant | Hook-off invariant | Effect | Task completion on/off | Mean context on/off | Cost on/off |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| opentelemetry-go-histogram-reset-repair-v1 | `8bdca1489407…` | claude-haiku-4-5-20251001 | 5 | 1/5 (20%) | 0/5 (0%) | +20 pp | 5/5 (100%) / 5/5 (100%) | 1365k / 1050k | $1.54 / $1.24 |
| opentelemetry-go-histogram-reset-v1 | `2c2bf8b58e0d…` | claude-haiku-4-5-20251001 | 5 | 5/5 (100%) | 0/5 (0%) | +100 pp | 5/5 (100%) / 5/5 (100%) | 1359k / 1340k | $1.61 / $1.49 |

### Hook delivery intensity

| Instance | Fingerprint | Delivery | Exposure | Matches | Injections | Repeated injections | Suppressed | Context bytes |
|---|---|---|---|---:|---:|---:|---:|---:|
| opentelemetry-go-histogram-reset-repair-v1 | `8bdca14894076144558de31348418fb1f496d6404b79bae019a66294209fa735` | once-per-context | optional | 2 | 1 | 0 | 1 | 624 |
| opentelemetry-go-histogram-reset-v1 | `2c2bf8b58e0d549eebd481f4429dd9209a67ba0a441cc71809615942fa28b736` | once-per-context | required | 5 | 5 | 0 | 0 | 3060 |

### Exact cohort identities

- `opentelemetry-go-histogram-reset-repair-v1` / `8bdca14894076144558de31348418fb1f496d6404b79bae019a66294209fa735`
  - Task `4ab4a1bb2be01c1c30ebea30cf2c512bfcf043cf89acdc9831e19a99ca67e0aa`; fixture `0eb89a5210e64df2f38611b95d1ae0afd6b88fd7`; pin `keep-histogram-reset-paths-in-sync`.
  - Task prompt: Fix the metric SDK so a reused destination datapoint from a delta explicit-bucket histogram does not retain `Sum`, `Min`, or `Max` values from an earlier aggregation when the later delta histogram has sum or min/max recording disabled. The same destination aggregation may be reused across collectors with different recording settings. Keep the change focused and add or update tests as appropriate. Before finishing, verify the result with `go -C sdk/metric test ./internal/aggregate`.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.90/session.
  - Hook delivery policy: `once-per-context`.
  - Hook exposure: `optional`; comparison family `opentelemetry-go-histogram-reset-scoping-v1`; protocol `2c2bf8b58e0d549eebd481f4429dd9209a67ba0a441cc71809615942fa28b736`.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);go=go version go1.26.5 darwin/arm64`.
  - Seamark `seamark v0.4.0-11-g67707a2`; binary `602dd49095ab7102e46ed22667265456534b2e69d3a0b4a51fbde201de45ba01`.
- `opentelemetry-go-histogram-reset-v1` / `2c2bf8b58e0d549eebd481f4429dd9209a67ba0a441cc71809615942fa28b736`
  - Task `4ab4a1bb2be01c1c30ebea30cf2c512bfcf043cf89acdc9831e19a99ca67e0aa`; fixture `0eb89a5210e64df2f38611b95d1ae0afd6b88fd7`; pin `keep-histogram-reset-paths-in-sync`.
  - Task prompt: Fix the metric SDK so a reused destination datapoint from a delta explicit-bucket histogram does not retain `Sum`, `Min`, or `Max` values from an earlier aggregation when the later delta histogram has sum or min/max recording disabled. The same destination aggregation may be reused across collectors with different recording settings. Keep the change focused and add or update tests as appropriate. Before finishing, verify the result with `go -C sdk/metric test ./internal/aggregate`.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.90/session.
  - Hook delivery policy: `once-per-context`.
  - Hook exposure: `required`; comparison family `opentelemetry-go-histogram-reset-scoping-v1`; protocol `2c2bf8b58e0d549eebd481f4429dd9209a67ba0a441cc71809615942fa28b736`.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);go=go version go1.26.5 darwin/arm64`.
  - Seamark `seamark v0.4.0-11-g67707a2`; binary `602dd49095ab7102e46ed22667265456534b2e69d3a0b4a51fbde201de45ba01`.

### Paired details

Paired directions for `opentelemetry-go-histogram-reset-repair-v1`/`8bdca1489407…`: 1 favorable, 0 unfavorable, 4 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -26 to +62 pp.

Paired directions for `opentelemetry-go-histogram-reset-v1`/`2c2bf8b58e0d…`: 5 favorable, 0 unfavorable, 0 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +39 to +100 pp.

## Frozen claim assessment

- `lessons-owner-invariant`: **insufficient evidence** — 0/3 independent instances have at least 5 valid pairs.
  - Frozen conditions: 3 instances × 5 valid pairs; mean effect ≥ +30 pp; worst instance ≥ +0 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
- `lessons-delivery-scoping`: **insufficient evidence** — 0/2 independent instances have at least 5 valid pairs.
  - Frozen conditions: two protocol-matched variants × 5 valid pairs; treatment effect minus control effect ≥ +40 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
  - Comparison: (`python-ts-schema-sync-v1` hook-on − hook-off) − (`python-ts-schema-sync-repair-v1` hook-on − hook-off).
- `lessons-public-delivery-scoping`: **passes frozen threshold** — difference-in-differences and harmful-interference thresholds pass (qualifying instances: 2, difference-in-differences: +80.0 pp, harmful interference: 0.0%).
  - Frozen conditions: two protocol-matched variants × 5 valid pairs; treatment effect minus control effect ≥ +40 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
  - Comparison: (`opentelemetry-go-histogram-reset-v1` hook-on − hook-off) − (`opentelemetry-go-histogram-reset-repair-v1` hook-on − hook-off).
  - Report-only observations: component effects — treatment +100.0 pp, control +20.0 pp; difference-in-differences +80.0 pp.

## Interpretation guardrail

A cohort can validate the harness or support its specific task without establishing a broader product claim. A passing assessment supports only the committed synthetic claim under the exact model, effort, clean-build, specified instance/variant, protocol, and valid-pair conditions; it does not establish external validity. An insufficient assessment must not be promoted to a product claim.
