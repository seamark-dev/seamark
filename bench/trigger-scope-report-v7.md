# Lessons benchmark report

Result schema: v7; claim schema: v1; evidence window: 2026-08-18T15:55:26Z to 2026-08-18T16:31:42Z.

## Raw inputs

| File | Rows | SHA-256 |
|---|---:|---|
| bench/repair-scope-release-v7.jsonl | 10 | `4f8d4e7bc950be2fc1dbb38f5840871e75a09a4765c43f2f5d7a2a5eb5ae5059` |
| bench/trigger-scope-release-v7.jsonl | 10 | `f0b0e050d781bbeaad275d8aa38fc261ab935f403e09e5b31d8b4e01ca405342` |

## Immutable cohorts

Rows are pooled only when their full experiment fingerprint matches. Invariant rates are conditional on completing the visible task.

| Instance | Fingerprint | Model | Valid pairs | Hook-on invariant | Hook-off invariant | Effect | Task completion on/off | Mean context on/off | Cost on/off |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| python-ts-schema-sync-repair-v1 | `6204990a3dff…` | claude-haiku-4-5-20251001 | 5 | 3/5 (60%) | 3/5 (60%) | +0 pp | 5/5 (100%) / 5/5 (100%) | 270k / 336k | $0.40 / $0.44 |
| python-ts-schema-sync-v1 | `982e47905bf2…` | claude-haiku-4-5-20251001 | 5 | 5/5 (100%) | 0/5 (0%) | +100 pp | 5/5 (100%) / 5/5 (100%) | 300k / 269k | $0.42 / $0.39 |

### Hook delivery intensity

| Instance | Fingerprint | Delivery | Exposure | Matches | Injections | Repeated injections | Suppressed | Context bytes |
|---|---|---|---|---:|---:|---:|---:|---:|
| python-ts-schema-sync-repair-v1 | `6204990a3dffaf8b01de730018458abccb7baf348a5b44837e4e1d841a78ad5d` | once-per-context | optional | 0 | 0 | 0 | 0 | 0 |
| python-ts-schema-sync-v1 | `982e47905bf2ebf2d8b3a2604d057d5f2b8b20d3912ca292420a8a7420547900` | once-per-context | required | 10 | 5 | 0 | 5 | 2209 |

### Exact cohort identities

- `python-ts-schema-sync-repair-v1` / `6204990a3dffaf8b01de730018458abccb7baf348a5b44837e4e1d841a78ad5d`
  - Task `1766a2b8055bafe095f6b036c053734a45d01b404d5aaadf89f5240127dff01a`; fixture `396b9c3d38326758bd377d01f051aeec9c1356a4`; pin `sync-generated-api-client`.
  - Task prompt: Expose each workspace's billing currency in `GET /api/workspaces/{id}` responses. Return it as `billingCurrency`, sourced from `Workspace.billing_currency`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Hook delivery policy: `once-per-context`.
  - Hook exposure: `optional`; comparison family `python-ts-schema-sync-scoping-v1`; protocol `982e47905bf2ebf2d8b3a2604d057d5f2b8b20d3912ca292420a8a7420547900`.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.4.0-3-gb5c1bd8`; binary `0c306bc1548a4184809bb3966b34a9c1478b3e69637704324ac835ee8f2289ff`.
- `python-ts-schema-sync-v1` / `982e47905bf2ebf2d8b3a2604d057d5f2b8b20d3912ca292420a8a7420547900`
  - Task `1766a2b8055bafe095f6b036c053734a45d01b404d5aaadf89f5240127dff01a`; fixture `396b9c3d38326758bd377d01f051aeec9c1356a4`; pin `sync-generated-api-client`.
  - Task prompt: Expose each workspace's billing currency in `GET /api/workspaces/{id}` responses. Return it as `billingCurrency`, sourced from `Workspace.billing_currency`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Hook delivery policy: `once-per-context`.
  - Hook exposure: `required`; comparison family `python-ts-schema-sync-scoping-v1`; protocol `982e47905bf2ebf2d8b3a2604d057d5f2b8b20d3912ca292420a8a7420547900`.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.4.0-3-gb5c1bd8`; binary `0c306bc1548a4184809bb3966b34a9c1478b3e69637704324ac835ee8f2289ff`.

### Paired details

Paired directions for `python-ts-schema-sync-repair-v1`/`6204990a3dff…`: 1 favorable, 1 unfavorable, 3 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -46 to +46 pp.

Paired directions for `python-ts-schema-sync-v1`/`982e47905bf2…`: 5 favorable, 0 unfavorable, 0 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +39 to +100 pp.

## Frozen claim assessment

- `lessons-owner-invariant`: **insufficient evidence** — 1/3 independent instances have at least 5 valid pairs (qualifying instances: 1).
  - Frozen conditions: 3 instances × 5 valid pairs; mean effect ≥ +30 pp; worst instance ≥ +0 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
- `lessons-delivery-scoping`: **passes frozen threshold** — difference-in-differences and harmful-interference thresholds pass (qualifying instances: 2, difference-in-differences: +100.0 pp, harmful interference: 0.0%).
  - Frozen conditions: two protocol-matched variants × 5 valid pairs; treatment effect minus control effect ≥ +40 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.
  - Comparison: (`python-ts-schema-sync-v1` hook-on − hook-off) − (`python-ts-schema-sync-repair-v1` hook-on − hook-off).
  - Report-only observations: component effects — treatment +100.0 pp, control +0.0 pp; difference-in-differences +100.0 pp.

## Interpretation guardrail

A cohort can validate the harness or support its specific task without establishing a broader product claim. A passing assessment supports only the committed synthetic claim under the exact model, effort, clean-build, specified instance/variant, protocol, and valid-pair conditions; it does not establish external validity. An insufficient assessment must not be promoted to a product claim.
