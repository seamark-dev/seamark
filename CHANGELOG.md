# Changelog

Notable changes, newest first. Every tagged release attaches
smoke-tested archives for macOS and Linux (amd64/arm64) and a
`SHA256SUMS` file; verify a download with
`sha256sum -c --ignore-missing SHA256SUMS` (on macOS:
`shasum -a 256 -c --ignore-missing SHA256SUMS`).

## Unreleased

- **Lessons deliver where the mistake is made.** Distilled pins used to
  scope to where reviewers commented — the repair site. For
  cross-boundary mistakes that is the wrong place: a "regenerate the
  client" lesson scoped to the generated TypeScript file can never
  fire for the author editing the backend model. Distillation now asks
  the agent for trigger paths and verifies every answer in three steps
  — it must parse, it must exist in the working tree, and co-change
  history must confirm it against the cited evidence — and only then
  widens the pin's regions to deliver at the trigger. A new advisory
  ("delivery may miss the trigger") flags mis-scoped pins on the
  `--proposals` ledger, the HTML report, and the distill plan, with a
  tail block so long lists cannot bury it. One shared recompute now
  backs every "regions now" reader, so `--retarget` applies a widening
  and can never strip one.
- **`lessons --extract-triggers` upgrades existing corpora.** Small
  batched agent calls ask the one trigger question per
  already-distilled proposal: a preflight always discloses cost,
  `--dry-run` stops there, live per-batch progress shows during the
  calls, and every answer — including "no trigger" — is stamped so
  re-running is free. Pending proposals widen in place; applied pins
  surface their drift in the ledger for the explicit `--retarget`, so
  an installed pin's delivery never changes without you. Hand-pruned
  pins are skipped: there is no delivery to widen. Field runs on the
  two development corpora: 17 of 53 proposals gained validated
  triggers on a two-language repository (the schema-sync pins now
  deliver at the backend model that triggers them), while a
  single-ecosystem repository correctly produced only same-site
  triggers and no false cross-boundary widenings.
- **Sharper failures.** Agent CLI complaints that arrive on stdout —
  usage limits, expired OAuth sessions — now surface in the error
  instead of a bare exit status. Contradictory flag combinations
  (`--dry-run` with a decision flag, `--proposals` with `--apply`,
  spending modes together) are refused before dispatch instead of one
  flag being silently ignored.

Upgrade note: the database upgrades to schema v5 in place on first
open (trigger paths, plus the answered-question stamp that keeps
re-runs free); an older binary then refuses the upgraded database —
the versioning contract. State bundles carry both fields; older
binaries still import the rest of a newer bundle.

## v0.3.0 — 2026-08-10

The outcome line: the system now measures whether its own lessons work —
passively from data every repo already has, and actively with a
controlled, reproducible benchmark — and tunes how lessons are
delivered based on what those measurements showed.

- **The passive outcome loop.** Every applied pin now carries a verdict
  computed from data seamark already holds — the firing log, the
  finding table, and mined history: `working` (flagged N× before
  exposure, none since, across enough region commits), `not landing`
  (the pin fires and the mistake recurs — the ledger names these and
  suggests escalation), or `untested` (with the missing evidence named:
  never fired, quiet region, evidence not mined since exposure,
  citations aged out). Rendered as the same falsifiable sentence in
  `lessons --stats`, the `--proposals` ledger, and the HTML report.
  Deterministic, recomputed per ask, no new stored state. Exposure
  starts at a pin's first firing — a pin no agent ever saw cannot have
  changed behavior — and mining freshness is now stamped so absence of
  recurrence is never claimed from an unmined corpus.
- **A controlled lessons benchmark, with its first passing claim.**
  `make lessons-bench` runs paired headless agent sessions in generated
  fixture repositories built around owner-specific invariants that
  public checks do not cover (a generated TS client, a cache version, an
  async worker registry). Model and effort are pinned, sessions are
  sandboxed and budget-capped, every row carries artifact digests, and
  a no-spend preflight proves the judges discriminate before anything
  is purchased. The first clean cohort (3 instances × 5 valid pairs,
  Haiku, medium effort) passed the pre-frozen claim: hook-on preserved
  the owner invariant 15/15 versus 3/15 hook-off, +80 pp mean lift, no
  visible-task regressions ([bench/lessons-report-v5.md](bench/lessons-report-v5.md)).
  Reproducible controlled evidence under exact conditions — not
  external validation.
- **Once-per-context lesson delivery.** `.seamark/lessons.yaml` now accepts
  `hook_delivery: once-per-context` to inject each matching lesson once in the
  current agent context instead of repeating it after every edit. The default
  remains `always`. Local state contains only repository-scoped session and
  lesson digests, expires after 24 hours, resets after Claude Code compaction,
  and fails open so missing identity, lock contention, or corrupt state never
  hides guidance. Hook audit records carry match and context-generation
  digests; `lessons --stats` distinguishes injections, repeats, suppressions,
  and injected bytes. Per-lesson stats and passive outcome sentences now show
  match-inclusive counts alongside actual deliveries when repeats were
  suppressed, without moving the first-delivery exposure clock.
- **Auditable delivery-cost benchmarks.** Benchmark result schema v6 records
  the selected delivery policy and its hook intensity, keeps policy cohorts in
  separate fingerprints, and labels them in Markdown reports. The first
  two-pair export-registry calibration reduced four matching hook events to one
  injection and three suppressions per treatment session, with zero repeated
  injections and no loss of owner-invariant success. The small dirty-build
  sample does not yet support a token-cost claim.
- **More reproducible benchmark sessions.** The managed Claude adapter now
  strips inherited provider-routing, helper-model, thinking-budget, and prompt-
  caching overrides while preserving existing OAuth and API-key discovery. It
  also rejects unknown hook-delivery evidence during the run, keeps the file-
  only arm byte-stable across hook policies, and aligns the public v6 JSON
  Schema with the semantic validator where standard JSON Schema can express
  the constraint.

Upgrade note: run `seamark init` once after upgrading to install the
`PostCompact` lifecycle hook. Switching `hook_delivery` modes afterward does
not require another init run.

## v0.2.0 — 2026-08-05

The evidence-quality line: pins that say where they apply and how much
their evidence still supports them, and surfaces that inject that
memory at the moment of change.

- **Evidence confidence, everywhere pins compete.** Every distilled pin
  now carries a deterministic tier — strong / fair / weak — computed on
  read from what its citations still support: distinct events, source
  diversity (review+fix), recency, and whether the cited files still
  exist. Weak pins lose injection-budget slots to strong ones and carry
  a "weak evidence" tag in the hook; `why` prints each pin's tier with
  the facts behind it. Nothing is stored, nothing is model-scored.
- **The proposals ledger re-judges old decisions.** `lessons
  --proposals` now shows each pin's evidence health under TODAY'S rules
  — recomputed events, liveness, prompt era — and the regions current
  inference would assign. The new `lessons --retarget p3,p7` applies
  that tightening to lessons.yaml and the ledger together (write-gated
  like `--apply`): the upgrade path for pins distilled before region
  sets. The HTML report's decision cards carry the same health — tier
  badge, facts, era, and the drift line with its retarget command.
- **change_set and check carry the memory.** `change_set` answers now
  end with the budgeted lessons governing the files about to change
  (`change_budget`, default 6), and `check` appends an advisory block
  for the diff's files — clearly marked, never part of the verdict.
  Both record to the firing log with a surface tag, so `--stats`
  reflects all ambient exposure.
- **Review evidence gets a shelf life.** Review mining keeps two years
  of comments (fix mining always kept one) — with the newest 200 always
  surviving, so slow repositories keep a working corpus untuned.
  `reviews: {window_days: N}` adjusts; `0` means unlimited.
- **Region sets replace the repo-wide `*` collapse.** A proposal's
  region is now a small set (≤3 directories, depth ≤3) covering ≥80%
  of its cited *events*: test and doc paths don't vote (test-only
  evidence keeps its test region), root files can't drag a theme to
  `*`, and a theme living in `api` AND `db` says so instead of saying
  "everywhere". Measured on the corpora that motivated it: repo-wide
  proposals drop from 35/65 to 3/65 and 3/27 to 0/27. Applied pins
  carry both `region:` (first entry — what older seamark reads, still
  narrower than the old `*`) and `regions: [a, b]`; the schema
  migrates to v3 (`proposal.regions`, `finding.paths`) automatically.
  Deliberately NOT migrated: existing proposals and their applied pins
  keep the regions they were decided under — rewriting them would
  silently change pin identities behind lessons.yaml's back. New
  distillations get sets immediately; existing pins tighten through
  the upcoming revalidation audit, which shows the recomputed regions
  next to the stored ones with the command to apply them.
- **Fix findings point at the code, not the churn.** A fix's primary
  file is its most-changed non-test, non-doc file (tests routinely
  out-churn the fix they cover), and the finding stores the commit's
  full code footprint for region inference.
- **Merge-commit workflows get PR attribution.** Branch commits inherit
  their pull request from merge topology (`Merge pull request #N` +
  rev-list), so a review comment and the `fix: PR review` commit
  answering it finally count as one event in repos that don't squash.
  Explicit `(#N)` / `fixes #N` references still win. A merge from a
  `fix/`-named branch whose commits carry no fix-shaped message becomes
  one `fix:branch` finding (the merge's diff) — the tier only fires
  where there was no signal at all, and the source label shows in every
  evidence header.
- **Captured themes surface once.** A mined lesson stops surfacing in
  `why` and the edit hook when an applied pin cites every finding in
  its cluster AND that pin is currently present in lessons.yaml — the
  file stays the source of truth, so a hand-pruned pin resurfaces its
  lesson, a partially-cited cluster keeps surfacing (one comment can
  flag two mistakes), and a recurrence arriving after the pin was
  applied re-opens the lesson. The ledger (`lessons --list`,
  `--region`) still shows every raw lesson.
- **Secret redaction in mined text.** Review-comment bodies and
  fix-commit patches are scrubbed of secret-shaped values (connection
  strings, tokens, password assignments) at mining time, with the same
  patterns the gate's raw audit log uses — a credential a reviewer
  quoted once is not re-broadcast into agent context on every edit.
  Already-stored findings keep their text until the next
  `seamark index --reviews` re-mines them.
- **Compact hook injection.** The edit-hook reminder drops the
  terminal-table padding, per-line regions, and reviewer names for
  `- [pin]` / `- [×N]` lines: the reader is a model, and the tokens now
  go to the guidance. Deliberate views keep the full table.
- **Proposal dedup by evidence.** A distilled pattern citing exactly the
  same findings as one already proposed, applied, dismissed, or pinned
  is dropped as a re-derivation, whatever its wording (measured: two
  applied pins with identical citations and unrelated names). The
  `lessons --proposals` audit flags such pairs for pruning. Bare
  linter-code pins (RUF001 vs RUF003) never merge on wording; the edit
  hook collapses restated pins before spending its injection budget.
- **Stable distillation batches.** Oversized candidate groups are cut by
  finding-id hash instead of position, so one new finding re-opens one
  batch instead of re-billing the whole component. One-time cost on
  upgrade: existing oversized-group signatures change once, so the next
  `lessons --distill` re-reads those groups (small groups keep their
  signatures; applied and dismissed decisions are unaffected, and
  re-read groups cannot re-propose already-captured themes).

Upgrade notes: databases upgrade to schema v3 in place on first open
(an older binary then refuses them — the versioning contract). Existing
pins keep the regions they were decided under: `lessons --proposals`
shows what today's inference would assign, `lessons --retarget` applies
it. The next `lessons --distill` re-reads once-oversized groups whose
batch signatures changed — the preflight prices that before anything is
sent — and already-stored review text picks up secret redaction on the
next `seamark index --reviews`.

## v0.1.0 — 2026-08-03

The first tagged release: the production-readiness line — everything
between the first prototype and here.

- **Trust baseline.** A default `seamark init` can never block a
  command; gate enforcement is an explicit opt-in (`--gate-mode
  enforce`), and re-running init preserves the installed mode. The audit
  log stores normalized command names and input hashes instead of raw
  command lines (0600, rotated by size and age, flock-serialized,
  symlink-proof); opt-in raw logging redacts secret-shaped values.
- **Durable state.** The store schema is versioned with ordered,
  transactional migrations; an older binary refuses a newer database.
  Rebuilds preserve proposal decisions and distillation memory, and
  `seamark state export|import` makes them portable across clones.
- **Disclosure.** `lessons --distill` prints what would leave the
  machine before any agent call; `--dry-run` stops there. Data flows and
  the threat model are documented (docs/data-flow.md,
  docs/threat-model.md).
- **Health.** `seamark status` reports semantic health (coverage, call
  confidence, history age, integrations) as text, JSON, and an MCP
  resource; `seamark doctor` diagnoses the installation read-only with
  exact corrective actions. `check` exposes coverage blind spots to
  policy (`diff.unindexed_files`) and every surface distinguishes "no
  evidence" from "not indexed".
- **Distribution.** This release pipeline: per-platform native builds,
  end-to-end smoke tests on every artifact, SHA-256 checksums, draft
  releases for human review.

Upgrade notes: a pre-existing hook installed as `gate --enforce --hook`
is preserved by re-running `seamark init` (mode is kept unless
`--gate-mode` says otherwise). Databases from earlier builds upgrade in
place on first open; coverage metadata backfills on the next
`seamark index`.
