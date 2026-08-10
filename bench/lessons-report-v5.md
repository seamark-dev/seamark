# Lessons benchmark report

Result schema: v5; claim schema: v1; evidence window: 2026-08-08T23:47:40Z to 2026-08-09T00:40:22Z.

## Raw inputs

| File | Rows | SHA-256 |
|---|---:|---|
| bench/cache-version-release-v5.jsonl | 10 | `2656c0e6e10dfef5e8ffbe3541dd0c46f87b0e4a6fcea24878edebc6c80a7e7e` |
| bench/export-registry-release-v5.jsonl | 10 | `92c2df7d3c87d37482a2b2e975e19c6b3faccb00412808c1ba535628123ee73a` |
| bench/schema-sync-release-v5.jsonl | 10 | `51f4a81b415251f85110caf793a64ab5e1c1fcba7e30db43670de8088fa5f73b` |

## Immutable cohorts

Rows are pooled only when their full experiment fingerprint matches. Invariant rates are conditional on completing the visible task.

| Instance | Fingerprint | Model | Valid pairs | Hook-on invariant | Hook-off invariant | Effect | Task completion on/off | Mean context on/off | Cost on/off |
|---|---|---|---:|---:|---:|---:|---:|---:|---:|
| go-export-registry-v1 | `6859bcf37d4b…` | claude-haiku-4-5-20251001 | 5 | 5/5 (100%) | 0/5 (0%) | +100 pp | 5/5 (100%) / 5/5 (100%) | 279k / 224k | $0.42 / $0.37 |
| python-cache-version-v1 | `3ee116843ad6…` | claude-haiku-4-5-20251001 | 5 | 5/5 (100%) | 0/5 (0%) | +100 pp | 5/5 (100%) / 5/5 (100%) | 279k / 253k | $0.41 / $0.38 |
| python-ts-schema-sync-v1 | `d438efd8b090…` | claude-haiku-4-5-20251001 | 5 | 5/5 (100%) | 3/5 (60%) | +40 pp | 5/5 (100%) / 5/5 (100%) | 368k / 353k | $0.47 / $0.46 |

### Exact cohort identities

- `go-export-registry-v1` / `6859bcf37d4b737ae09bd147511a71ec03df2fc7a8395db33f10090a336ee2bd`
  - Task `03197e4484575d55cd3a4d74f694846d1e2fa53f4de849218342b54d2bf11457`; fixture `9a2e87e8faa7fe4c1070ba941bd36b7b846c11f7`; pin `register-async-export-format`.
  - Task prompt: Add Markdown as a supported format to the synchronous export preview API. `Preview("markdown", rows)` should render a compact Markdown table with `Name` and right-aligned `Total` columns. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);go=go version go1.26.5 darwin/arm64`.
  - Seamark `seamark v0.2.0-3-g6d5d87d`; binary `f4cac633340909e8221b6787b9d42532774b1a725919f31b442071ba49c8bcc0`.
- `python-cache-version-v1` / `3ee116843ad6e06e3d3bdb5a87f6c59091aafaff95a7bfcee5fe08145737adc8`
  - Task `a1d0def55e59796819373cae9092a1f216bfee5287475f0f152622c727f1c0f5`; fixture `1a522125c951586b2de5fea7615f73f5b0127f62`; pin `bump-cached-response-version`.
  - Task prompt: Expose each workspace's locale in `GET /api/workspaces/{id}` responses. Return it as `locale`, sourced from `Workspace.locale`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.2.0-3-g6d5d87d`; binary `f4cac633340909e8221b6787b9d42532774b1a725919f31b442071ba49c8bcc0`.
- `python-ts-schema-sync-v1` / `d438efd8b0900376c8569a77345192a1259ff4c403ea0ba4ad8fc8a9f9f5408b`
  - Task `1766a2b8055bafe095f6b036c053734a45d01b404d5aaadf89f5240127dff01a`; fixture `396b9c3d38326758bd377d01f051aeec9c1356a4`; pin `sync-generated-api-client`.
  - Task prompt: Expose each workspace's billing currency in `GET /api/workspaces/{id}` responses. Return it as `billingCurrency`, sourced from `Workspace.billing_currency`. Keep the change minimal and add or update tests as appropriate.
  - Model requested `claude-haiku-4-5-20251001`, observed `claude-haiku-4-5-20251001`; effort `medium`; maximum $0.25/session.
  - Agent `2.1.220 (Claude Code)`; runtime `claude-native-sandbox-v2;darwin/arm64;agent=2.1.220 (Claude Code);python3=Python 3.13.3;make=GNU Make 3.81`.
  - Seamark `seamark v0.2.0-3-g6d5d87d`; binary `f4cac633340909e8221b6787b9d42532774b1a725919f31b442071ba49c8bcc0`.

### Paired details

Paired directions for `go-export-registry-v1`/`6859bcf37d4b…`: 5 favorable, 0 unfavorable, 0 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +39 to +100 pp.

Paired directions for `python-cache-version-v1`/`3ee116843ad6…`: 5 favorable, 0 unfavorable, 0 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: +39 to +100 pp.

Paired directions for `python-ts-schema-sync-v1`/`d438efd8b090…`: 2 favorable, 0 unfavorable, 3 tied; 0 harmful task regressions.
Approximate 95% Wilson score interval for the conditional effect: -12 to +77 pp.

## Frozen claim assessment

- `lessons-owner-invariant`: **passes frozen threshold** — mean effect, per-instance effect, and harmful-interference thresholds pass (qualifying instances: 3, mean effect: +80.0 pp, worst instance: +40.0 pp, harmful interference: 0.0%).
  - Frozen conditions: 3 instances × 5 valid pairs; mean effect ≥ +30 pp; worst instance ≥ +0 pp; harmful task interference ≤ 5.0%; model `claude-haiku-4-5-20251001`; effort `medium`; clean Seamark required.

## Interpretation guardrail

A cohort can validate the harness or support its specific task without establishing the cross-instance product claim. A passing assessment supports only the committed synthetic claim under the exact model, effort, clean-build, independent-instance, and valid-pair conditions; it does not establish external validity. An insufficient assessment must not be promoted to a product claim.
