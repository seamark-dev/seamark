# Changelog

Notable changes, newest first. Every tagged release attaches
smoke-tested archives for macOS and Linux (amd64/arm64) and a
`SHA256SUMS` file; verify a download with
`sha256sum -c --ignore-missing SHA256SUMS` (on macOS:
`shasum -a 256 -c --ignore-missing SHA256SUMS`).

## Unreleased

The production-readiness line — everything between the first prototype
and the first tagged release:

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
