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
| Agent skills (`skills/`, embedded; `init --skills`) | working; opt-in; installs into `.claude/skills` and `.agents/skills`, reported by `status`/`doctor`; `init --approve-tools` writes the Claude Code allow rules and the Codex per-tool approvals the model-driven path needs, reported by `status`/`doctor`; the second paired cohort (2026-09-05, Haiku 4.5, 15 pairs) passed the frozen claim at +73 pp mean invariant lift (12/15 vs 1/15) at about twice the context per session; the skills stay opt-in so the spend is the user's decision ([bench/skills-report-v2.md](../bench/skills-report-v2.md)) |
| Skills workflow benchmark (`make skills-bench`, MCP-only vs MCP + skills; `make skills-activation`) | two cohorts ran 2026-09-05: the first found no effect and its transcripts drove the fixture, skill, `change_set`, and `check` revisions; the second passed the frozen claim; own rows, claims, fingerprint, and report, none shared with the lessons benchmark ([bench/README.md](../bench/README.md)); the 2026-09-08 review fixes moved the fingerprint without changing what was measured: the skills tree differs from the measured one only in the `lessons --region` grant, and `check`/`change_set` only in how fast the companion reasons are computed, so no cohort was re-run |

Known limits are documented in the README's *Honest limits*: syntactic
resolution with labeled confidence, no scope tracking, conservative
Python DB tagging.

## Learn — functional, integration-dependent

Review mining, fix mining, lessons, distillation, pins.

| Capability | State |
|---|---|
| Fix-commit mining (local git) | working, offline; `index --fixes-only` refreshes only fix evidence from local history |
| Review-comment mining | working; requires an authenticated `gh` CLI and a GitHub remote on github.com |
| Lessons + edit hook + tuning (`lessons.yaml`) | working |
| Evidence confidence + ledger revalidation (`--proposals`, `--retarget`) | working; tiers recomputed on read, never stored |
| Distillation (plan/apply, dedup memory, preflight disclosure, `--dry-run`) | working; requires your own agent CLI; sends finding text to it ([data-flow.md](data-flow.md)) |
| Trigger paths (extraction at distill time, `--extract-triggers` backfill, scope advisory in the ledger/report/plan) | working; every named path is verified against the tree and direct evidence or co-change history before it becomes a precise delivery scope; evidence coverage is the fallback; answered proposals are never re-paid |
| Passive outcome loop (per-pin `working` / `not landing` / `untested` verdicts in `--stats`, the ledger, and the HTML report) | working; deterministic, recomputed on read, honesty-gated on activity and mining freshness |
| Once-per-context hook delivery (`hook_delivery` in `lessons.yaml`) | working; opt-in, digest-only local state, fails open; needs one `seamark init` re-run for the `PostCompact` hook |
| Lessons benchmark (`make lessons-bench`, paired headless sessions, frozen claim registry) | working; accepted synthetic and pinned OpenTelemetry-Go cohorts; operator-run and spends provider tokens; protocol and evidence in [bench/README.md](../bench/README.md) |

## Guard — warn mode ready; enforcement is beta

Command gate, diff check, audit, hooks.

| Capability | State |
|---|---|
| Command classification (shell parser, wrappers, interpreter payloads, dynamic detection) | working |
| Diff blast radius with coverage uncertainty (`unindexed_files`) | working |
| Warn mode (report, never block) | ready — the recommended deployment |
| Secret-safe audit log (hashed by default, 0600, rotation, flock) | working |
| Enforce mode (exit 2, fail closed) | works, **beta**: an agent that can edit `policy.yaml` or `.claude/settings.json` can weaken it ([threat-model.md](threat-model.md)) |
| Real approvals (`require_approval` with out-of-band approval tokens) | **not built** — today a require_approval verdict simply blocks under enforce |
| Policy integrity (pinned policy outside agent reach) | **not built** |

Guard is a defense-in-depth policy layer, not a sandbox. Run untrusted
agents inside real isolation regardless.

## Distribution

Latest published release: [v0.5.4](https://github.com/seamark-dev/seamark/releases/tag/v0.5.4)
(2026-08-28). The next release, v0.6.0, is not yet published; it adds the
three opt-in agent skills and their benchmark. Every release
ships native archives for macOS and Linux (amd64/arm64),
each smoke-tested end to end before publishing, with SHA-256 checksums
(`SHA256SUMS` on every release). Source builds need Go ≥ 1.25 and a C
compiler. Windows is untested and unsupported.

Homebrew: `brew install seamark-dev/tap/seamark` installs pre-built
bottles for Apple Silicon macOS and x86_64 Linux from
[seamark-dev/homebrew-tap](https://github.com/seamark-dev/homebrew-tap);
other platforms build from source automatically. The tap README
documents the bottle release runbook. Artifact signing,
SBOMs, and an npm install are the next distribution milestone.

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
- The accepted OpenTelemetry-Go cohort meets its frozen public-repository
  threshold at an exact historical commit: trigger-scoped delivery preserved
  the parallel histogram invariant 5/5 versus 0/5 without the hook; the
  repair-scoped control was 1/5 versus 0/5. The protocol-matched
  difference-in-differences effect was +80 percentage points, all 20 visible
  tasks completed, and harmful interference was 0%. This is evidence for one
  pinned task, Haiku/medium configuration, and runtime—not broad external
  validity. Raw rows and the
  [generated assessment](../bench/otel-report-v7.md) live under `bench/`.
- The second skills workflow cohort meets its frozen threshold across the
  three co-change fixtures and five paired trials each: MCP + skills
  preserved the companion-file invariant in 12/15 task-complete sessions
  versus 1/15 for the MCP server and its approvals alone, +60, +80, and +80
  percentage points per instance and +73 on average, with all 30 tasks
  complete and no harmful interference. The activation set passed at 5/5
  recall per skill and 0/4 false activations. The skills arm processed
  about twice the context per session, which is why the skills stay opt-in.
  The first cohort, on histories that carried the pair at the mining floor
  and a `check` without a companion list, measured +6.7 points; both cohorts
  and the diagnosis are in [bench/README.md](../bench/README.md) and the
  [generated assessment](../bench/skills-report-v2.md).
- The separate [OpenTelemetry case study](case-studies/opentelemetry-histogram-reset.md)
  records the normal user workflow from a pinned commit through local-fix
  indexing, bounded distillation, proposal review, accepted-pin delivery, and
  independent verification. The replay recorded one 588-byte lesson injection
  at the trigger and two suppressed repeat matches; focused package tests and
  independent checks passed for explicit, exponential-delta, and
  exponential-cumulative histogram reuse. This is a reproducible worked
  example, not a causal or population-level extraction claim. Its sanitized,
  checksummed captures are stored with the case study.
