# Workspace console (`seamark tui`) — experimental

`seamark tui` opens a full-screen console for one workspace. It shows what
Seamark knows about the repository and its agent integrations, and it runs
two workflows end to end: setting up or repairing an integration, and
deciding on lesson proposals. It is an experimental MVP. The plain commands
stay the stable interface, and every console action has a plain route.

```bash
seamark tui                 # the enclosing git repository of the cwd
seamark tui -C path/to/repo # another workspace
seamark tui --ascii         # ASCII glyphs for limited fonts
```

The console honours `-C/--workspace` and `--db` like every other command.
An invalid workspace fails before the screen changes. Without an
interactive terminal (a pipe, CI, `TERM=dumb`) the command prints the
equivalent plain commands and exits with status 1. `NO_COLOR` turns color
off; every state keeps a glyph and a text label, so nothing depends on
color. The console supports macOS and Linux, like the rest of Seamark. It
is designed for 120×40: below 100 columns each view shows one pane at a
time (`enter` opens the detail, `esc` goes back). It needs at least 60×20
cells; a smaller terminal shows a resize message and keeps only `q` and `?`
active, so nothing hidden can be confirmed.

A workspace without any Seamark state opens a short guide with three
steps: create the shared configuration files, build the index, and connect
the agents found on `PATH`. Each step shows its plan first; `v` skips the
guide for this session without writing anything.

## What the console never does

- Opening, navigating, and refreshing the view (`r`) never write. They do
  not create or rebuild the index, record lesson delivery, fetch reviews,
  start an agent, or grant trust. SQLite may create its `-wal` and `-shm`
  side files next to an existing database.
- It never runs a command taken from diagnostics. Corrective commands are
  selectable text.
- It never retries a mutation, and it runs one mutation at a time.
- It never changes `agent.cli`/`agent.argv`, the policy, or the gate mode.

## Destinations

Press `1`–`4` to switch; `tab` moves between panes; `?` shows the keys of
the current view. The footer clock is the time of the last refresh.

### 1 Overview

- **Cards**: INDEX (coverage, parse errors, age, freshness), NAVIGATE
  (symbols, edges, decisions), LEARN (pending proposals, lessons, review
  mining, distill agent), and GUARD (policy mode and rule counts, gate
  hooks). Missing data says why; it never shows as zero. A stale index adds
  a banner with `b rebuild now`.
- **Attention**: actionable problems, optional additions, unsupported
  capabilities, and unknown evidence stay in separate groups. No overall
  score. Below them: one line per client and a doctor summary. In a narrow
  terminal the cards fold into one line, and the **Workspace summary** item
  shows every fact in full.
- **Index build** (`b`): a local parse plus git history mining that writes
  the index database. It never fetches reviews or runs distillation. The
  progress shows phases; only the parse phase has a count. A running build
  cannot be cancelled safely, so `q` offers to exit after it finishes.
  `enter` or `esc` closes the result.

### 2 Integrations

Claude Code and Codex from the shared client registry. Configuration
state, project trust, and native compatibility evidence are separate
columns: "configuration updated" never means "working". The capability
table shows where each capability lives; the next corrective command of
each incomplete capability follows it.

Setup (`s` or `enter`) chooses additions (MCP registration, lifecycle
hooks, managed skills, tool grants). A capability that is already current
is shown as kept (`[=]`), not as an unchecked box: setup only adds. `i`
shows the full explanation of the selected addition. The review lists every
affected file with its line counts, what the plan WILL and WON'T change, and
a diff with line numbers and `+`/`−` markers. `y` applies exactly the
reviewed plan, `n` goes back; a file that changed since the review stops it
before the first write. The result lists applied, kept, failed, and not-attempted
operations. A fresh inspection then groups what is left: **Needs
attention** (capabilities that are still not current), **Not configured
(optional)** (additions you did not choose), and **Remaining native
steps** (trust and native evidence; for Codex, review and trust the hooks
with `/hooks`). While you choose or review, the global keys keep working
and the wizard keeps your choices. `u` refreshes the managed skills of the
selected client.

The closest CLI route is `seamark init --client <claude|codex>
[--skills] [--approve-tools]` (add `--print` to preview). The CLI form
always installs hooks and registers MCP.

### 3 Lessons

**Proposals** lists pending, applied, and dismissed proposals (`f` cycles
the state, `/` filters by region). The detail shows the full rule and note,
regions, provenance, the cited evidence (or "unavailable" when it aged
out), and the advisories of `seamark lessons --proposals`. `space` selects
pending rows; a filter change clears the selection.

- **Apply** (`a`) opens the pin block in a modal with the target file and
  the ledger change. With `distill.write` off the action only shows the
  YAML: copy the block under `pin:` in `.seamark/lessons.yaml` (`c` asks
  the terminal to copy it through OSC 52, if the terminal allows it), or
  enable the gate and refresh. The preview also shows
  `seamark lessons --apply <ids>`, which prints the same block. A long note
  folds onto indented lines that YAML reads as the same text. With the gate
  on, `y` writes the pins and then records the proposals as applied.
- **Open PR** (`o`) opens the pull request of the cited evidence in the
  browser. Only http and https links open.
- **Existing pins**: when a selected proposal already has its pin, the
  preview names it and writes nothing for it, with the gate on or off.
  `c` records such proposals as applied in the ledger only; the footer
  offers `c` only when a pin is known to exist.
- **Dismiss** (`d`) marks proposals dismissed. The decision is remembered
  for the same evidence; pins stay unchanged. There is no undo.
- A preview is not an authorization: when the ledger, the pin file, or the
  gate changed, the console asks for a new preview.
- **Partial apply**: the pin file and the ledger are not one transaction.
  When the pins were written but the ledger update failed, the result says
  so. Applying again would duplicate the pins, so the console refuses it
  and offers `c`, a ledger-only "record as applied" for proposals whose
  pins already exist.

**Stats** shows the lessons by source, the proposals by state, the top
regions, and records delivered per day (`f` changes the period), with the
most surfaced lessons. It keeps the delivery totals by surface (hook,
`change_set`, `check`), client attribution where it was recorded ("no
client recorded" otherwise), suppressed repeats, and eligible lessons never
recorded as delivered. Delivered means emitted through a recorded surface, not read,
followed, or effective. The counts cover the retained log; a refresh
reads a new snapshot.

### 4 Doctor

Every check of `seamark doctor` with its state, detail, and fix. The fix
is selectable text; the console never runs it.

## Stays on the CLI

Review mining and distillation (`seamark index --reviews`,
`seamark lessons --distill`), trigger extraction, prune and retarget,
policy and configuration editing, the inference agent, graph browsing,
the HTML report, and uninstalling integrations.

## Versions and limits

- Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0. They keep the
  Go 1.25 minimum; Bubble Tea v2 needs Go 1.26.
- Index builds cannot be cancelled; the console offers to exit after
  completion instead.
- `seamark lessons --apply` on the CLI does not check for pins that already
  exist. After a partial apply in the console, record the proposals with
  `c` in the console instead of applying them again on the CLI.
- One workspace per invocation. No mouse support is needed or provided;
  commands and YAML are plain selectable text. Clipboard copy is an
  optional OSC 52 request that some terminals ignore.
