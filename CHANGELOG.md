# Changelog

Notable changes, newest first. Every tagged release attaches
smoke-tested archives for macOS and Linux (amd64/arm64) and a
`SHA256SUMS` file; verify a download with
`sha256sum -c --ignore-missing SHA256SUMS` (on macOS:
`shasum -a 256 -c --ignore-missing SHA256SUMS`).

## Unreleased

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
