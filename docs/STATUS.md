# Production status

The concise, current state of seamark — what each capability profile can
be trusted with today. Design history and engineering narrative live in
[PLAN.md](PLAN.md); this page only says what is true now.

Seamark's surface splits into three capability profiles with different
maturity. They share one binary and one index; they do not share one
trust level.

## Navigate — stable

Local indexing, history mining, orientation, and the read surfaces.

| Capability | State |
|---|---|
| Indexer (Go, TypeScript/TSX/JS, Python) | working; parse cache, self-repairing freshness |
| History layer (co-change, decisions, fix density) | working; needs git history to be useful |
| `why`, `orient`, `change_set` | working (CLI + MCP; `change_set` is MCP-only) |
| LSP server (hover, lenses, omission diagnostics) | working; editor setup is manual ([editors.md](editors.md)) |
| HTML report | working |
| MCP server | working; five tools + `orient`/`status` resources + `onboard` prompt |
| Schema versioning, durable-state export/import | working |
| Health: `seamark status`, `seamark doctor` | working |

Known limits are documented in the README's *Honest limits*: syntactic
resolution with labeled confidence, no scope tracking, conservative
Python DB tagging.

## Learn — functional, integration-dependent

Review mining, fix mining, lessons, distillation, pins.

| Capability | State |
|---|---|
| Fix-commit mining (local git) | working, offline |
| Review-comment mining | working; requires an authenticated `gh` CLI and a GitHub remote on github.com |
| Lessons + edit hook + tuning (`lessons.yaml`) | working |
| Evidence confidence + ledger revalidation (`--proposals`, `--retarget`) | working; tiers recomputed on read, never stored |
| Distillation (plan/apply, dedup memory, preflight disclosure, `--dry-run`) | working; requires your own agent CLI; sends finding text to it ([data-flow.md](data-flow.md)) |

## Guard — warn mode ready; enforcement is beta

Command gate, diff check, audit, hooks.

| Capability | State |
|---|---|
| Command classification (shell parser, wrappers, interpreter payloads, dynamic detection) | working |
| Diff blast radius with coverage uncertainty (`unindexed_files`) | working |
| Warn mode (report, never block) | ready — the recommended deployment |
| Secret-safe audit log (hashed by default, 0600, rotation, flock) | working |
| Enforce mode (exit 2, fail closed) | works, **beta**: an agent that can edit `policy.yaml` or `.claude/settings.json` can weaken it ([threat-model.md](threat-model.md)) |
| Real approvals (`require_approval` with out-of-band human tokens) | **not built** — today a require_approval verdict simply blocks under enforce |
| Policy integrity (pinned policy outside agent reach) | **not built** |

Guard is a defense-in-depth policy layer, not a sandbox. Run untrusted
agents inside real isolation regardless.

## Distribution

Released: [v0.2.0](https://github.com/seamark-dev/seamark/releases)
(2026-08-05) ships native archives for macOS and Linux (amd64/arm64),
each smoke-tested end to end before publishing, with SHA-256 checksums
(`SHA256SUMS` on every release). Source builds need Go ≥ 1.25 and a C
compiler. Windows is untested and unsupported. Artifact signing, SBOMs,
and package-manager installs (Homebrew, npm) are the next distribution
milestone.

## Verification

- CI: full test suite + lint on every change; regression tests pin the
  trust baseline (non-blocking default init, audit redaction, durable
  state surviving rebuilds, docs-command drift).
- The first committed synthetic lessons pilot records a promising result: on
  the schema-sync task, hook-on preserved the owner invariant in 3/3 completed
  trials versus 0/3 for hook-off. It used a dirty development binary and falls
  below the frozen pair count, so it validates the harness and task but is not
  release evidence. The frozen registry requires three independent instances
  with five valid pairs each under the pinned model, effort, and clean-build
  conditions. Raw rows live under `bench/`, and `make lessons-bench-report`
  keeps incompatible fingerprints separate; the calibration and artifact
  protocol is in [bench/README.md](../bench/README.md).
- External pilots: none yet — that is the bar between "works here" and
  "production-ready", and claims stay scoped until it is met.
