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
| Trigger paths (extraction at distill time, `--extract-triggers` backfill, scope advisory in the ledger/report/plan) | working; every named path is verified against the tree and co-change history before delivery widens; answered proposals are never re-paid |
| Passive outcome loop (per-pin `working` / `not landing` / `untested` verdicts in `--stats`, the ledger, and the HTML report) | working; deterministic, recomputed on read, honesty-gated on activity and mining freshness |
| Once-per-context hook delivery (`hook_delivery` in `lessons.yaml`) | working; opt-in, digest-only local state, fails open; needs one `seamark init` re-run for the `PostCompact` hook |
| Lessons benchmark (`make lessons-bench`, paired headless sessions, frozen claim registry) | working; operator-run, spends provider tokens; protocol in [bench/README.md](../bench/README.md) |

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

Released: [v0.4.0](https://github.com/seamark-dev/seamark/releases)
(2026-08-15) ships native archives for macOS and Linux (amd64/arm64),
each smoke-tested end to end before publishing, with SHA-256 checksums
(`SHA256SUMS` on every release). Source builds need Go ≥ 1.25 and a C
compiler. Windows is untested and unsupported. Artifact signing, SBOMs,
and package-manager installs (Homebrew, npm) are the next distribution
milestone.

## Verification

- CI: full test suite + lint on every change; regression tests pin the
  trust baseline (non-blocking default init, audit redaction, durable
  state surviving rebuilds, docs-command drift).
- The first clean synthetic lessons release cohort meets the frozen controlled
  threshold across three independent fixtures and five paired trials each:
  hook-on preserved the owner invariant in 15/15 task-complete runs versus
  3/15 for hook-off, with no visible-task regressions. Mean cross-instance lift
  was +80 percentage points and the worst instance was +40 points. Hook-on used
  about 11.4% more processed context and 8.4% more measured cost. This is
  reproducible evidence for the exact Haiku/medium/runtime conditions, not
  external validation; the schema-sync instance alone remains uncertain. Raw
  rows and the generated report live under `bench/`; the calibration, artifact,
  and interpretation protocol is in [bench/README.md](../bench/README.md).
- The clean trigger-scope cohort also meets its frozen controlled threshold:
  trigger-scoped delivery preserved the schema-sync invariant in 5/5 hooked
  sessions versus 0/5 unhooked, while the repair-scoped control was 3/5 in
  both arms and received zero hook exposure. The protocol-matched
  difference-in-differences effect was +100 percentage points, with all 20
  visible tasks complete and no harmful interference. This demonstrates the
  behavioral value of delivering at a known trigger; extraction accuracy and
  external validity remain separate open evidence requirements. Raw evidence
  and the report live under `bench/`.
- External pilots: none yet — that is the bar between "works here" and
  "production-ready", and claims stay scoped until it is met.
