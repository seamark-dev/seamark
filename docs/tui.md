# Workspace console (`seamark tui`) — experimental

`seamark tui` opens a full-screen console for one workspace. It shows what
Seamark knows about the repository and its agent integrations, and it runs
the everyday workflows end to end: setting up or repairing an integration,
collecting review and fix evidence, distilling proposals with the
inference agent, deciding on those proposals, maintaining the installed
pins, and editing the settings that these workflows read. Pin maintenance
covers a retarget or a prune of a proposal-linked pin and a trigger
extraction for selected proposals. It is experimental. The plain commands
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
time (`enter` opens the detail, `esc` goes back), and a narrow tab row
shortens the labels (`Over`, `Integ`, `Set`; the help keeps the full
names). It needs at least 60×20 cells; a smaller terminal shows a resize
message and keeps only `q` and `?` active, so nothing hidden can be
confirmed.

A workspace without any Seamark state opens a short guide with three
steps: create the shared configuration files, build the index, and connect
the agents found on `PATH`. Each step shows its plan first; `v` skips the
guide for this session without writing anything.

## What the console never does

- Opening, navigating, and refreshing the view (`r`) never write. They do
  not create or rebuild the index, record lesson delivery, fetch reviews,
  start an agent, save settings, grant trust, or reconcile the pin file
  with the proposal ledger. SQLite may create its `-wal` and `-shm` side
  files next to an existing database.
- It never starts network or model work, and never writes a durable
  change, without a preview. The start or the save is always `y` in that
  preview, never `enter`; `esc` before `y` changes nothing. The index
  build keeps its confirmation (`enter` or `y`), because it writes only
  local derived data. A retarget or a prune writes `.seamark/lessons.yaml`
  and the ledger only after `y` in its preview, and only while
  `distill.write` is on.
- It never chains steps. An evidence refresh does not start a
  distillation, a distillation never applies a pin, and a settings save
  runs nothing. A trigger extraction is a separate paid run: it never
  changes the scope of an applied pin, and it never starts a retarget,
  an apply, or a prune. Each step has its own preview.
- It never runs a command taken from diagnostics. Corrective commands are
  selectable text.
- It never retries work, and it runs one mutation at a time.
- It never changes the policy, the gate mode, or pins outside a reviewed
  apply, retarget, or prune. It never edits the rule or the note of a
  pin, and it never retargets or prunes a pin without exactly one linked
  proposal.
  Settings edits only the five fields listed below; it can remove a
  custom `agent.argv` after a preview, but never writes one.

## Destinations

Press `1`–`5` to switch; `tab` moves between panes; `?` shows the keys of
the current view. The footer clock is the time of the last refresh; a
narrow Pins footer can leave it out (see Lessons › Pins, Action keys).

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
  shows every fact in full, including the stored findings by source and
  the pin-write gate.
- **Learning links**: the attention list links the learning workflow from
  typed data only. Each link opens a view; none of them starts work.

  | Item | Group | `enter` opens |
  | --- | --- | --- |
  | No evidence collected yet (ready index, no stored finding) | Optional | Lessons › Evidence |
  | N findings, no proposals in the ledger (the ledger holds no proposal at all) | Optional | Lessons › Run, the scope step of a distillation |
  | N findings, K distilled groups, no proposals (an earlier distillation recorded groups, and the ledger is empty) | Optional | Lessons › Run, the scope step, to check for new groups |
  | Inference agent unresolved, or its settings do not load | Action | Settings › inference |
  | Pin-write, index, or review settings do not load | Action | Settings › the group of the failing section |
  | Pin writing is off while proposals wait | Optional, never Action | Settings › pin writing |
  | A distillation, an evidence refresh, or a trigger extraction runs | Optional | Lessons › Run |

  A source that has not loaded or has failed adds no suggestion, and the
  tab count waits for every source. The Overview does not suggest
  repeated paid work:

  - A ledger with any proposal means that a distillation ran before. Only
    a new plan can tell whether new groups exist, so no item appears.
  - An empty ledger does not prove that no distillation ran, because a
    group can end without a proposal. The database records every
    distilled group, so the item says when an earlier distillation
    recorded groups and saved no proposal. It then offers only a check
    for new groups: new evidence can form them, and only a plan finds
    them. The link opens the scope step, which only plans; the plan skips
    each recorded group and says when no new group exists.
  - After a distillation of this session, the item stays hidden; Lessons ›
    Run shows its result. A later evidence refresh brings it back, because
    new findings can form new groups.
  - While a run is active, the evidence and distillation items stay
    hidden. The snapshots reload when the run ends, and the run row links
    to Lessons › Run.

  When `.seamark/config.yaml` cannot be edited (read-only, a symlink,
  mixed line breaks), the item for several failing readers names that
  block beside their errors, and the pin-write note names the manual route
  in place of the Settings route.
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
always installs hooks and registers MCP. Setup and the inference agent are
independent: setting up Codex does not make it the distill agent, and
choosing an agent in Settings changes no integration file.

### 3 Lessons

Five sub-views: **Proposals** (`p`), **Pins** (`m`), **Stats** (`s`),
**Evidence** (`e`), and **Run** (`u`, shown once a run or a draft exists
in this session). The sub-view keys work while the visible sub-view does
not capture keys, and `?` lists all of them in every sub-view. A narrow
tab line shortens the labels (`Props`, `Evid`).

**Proposals** lists pending, applied, dismissed, and superseded proposals
(`f` cycles the state in that order, `/` filters by region). A long
region keeps its last path components, as in Pins. The detail
shows the full rule and note, regions, provenance, the cited evidence (or
"unavailable" when it aged out), and the advisories of
`seamark lessons --proposals`. `space` selects pending rows; a filter
change clears the selection.

- **Apply** (`a`) opens the pin block in a modal with the target file and
  the ledger change. With `distill.write` off the action only shows the
  YAML: copy the block under `pin:` in `.seamark/lessons.yaml` (`c` asks
  the terminal to copy it through OSC 52, if the terminal allows it), or
  enable the gate in Settings and open a new preview. The preview also
  shows `seamark lessons --apply <ids>`, which prints the same block. A long
  note folds onto indented lines that YAML reads as the same text. With the
  gate on, `y` writes the pins and then records the proposals as applied.
  Two selected proposals with the same rule and regions get one entry,
  with the note of the lower ID. The preview names the other proposal
  under WON'T, and `y` records both as applied. Pins then shows the entry
  as `ambiguous`, because two applied proposals record it. Retarget,
  prune, and trigger extraction stay blocked for that entry and for both
  proposals, so edit that pin by hand. With the gate off, the YAML
  preview names the other proposal under the block.
  `seamark lessons --apply` uses the same decision flow, so the CLI
  writes the same pins.
- **Open PR** (`o`) opens the pull request of the cited evidence in the
  browser. Only http and https links open.
- **Existing pins**: when a selected proposal already has its pin, the
  preview names it and writes nothing for it, with the gate on or off.
  `seamark lessons --apply` also adds no second entry: with the gate on
  it records such proposals as applied (ledger only), and with the gate
  off its block leaves them out and names them.
  `c` records such proposals as applied in the ledger only. `c` works,
  and the detail names the pin, only when a pin is known to exist: an
  apply preview or a partial apply found it, or Pins reads the entry as
  the pending match of the proposal. The last case shows before any
  preview. With marks, `c` works only when every marked proposal has a
  known pin, because the record refuses the whole selection when one pin
  is missing. Otherwise `c` opens no preview, and one line gives the
  reason, for example `No record applied: no pin in
  .seamark/lessons.yaml carries p2 yet. Press r after you edit the
  file.` With marks the line counts them (`1 of 2 marked proposals has
  no pin ...`). The console knows a pin only from its last read, so a
  pin that you add by hand shows after `r`, and a pin that you remove by
  hand turns `c` off after `r`, also when a preview found it before.
  When Pins could not read the pin file or the ledger, the line does
  not claim a missing pin: `No record applied: the pin file or the
  ledger could not be read; m (Pins) names the error. Press r after
  you fix it.` A proposal whose stored region fields disagree has no
  proven pin, because its apply writes a pin with other regions than
  the ones that Pins links. `c` stays off for it, its press says so
  (`No record applied: the stored region fields of p2 disagree, so no
  pin is proven to be its own.`), and the record refuses it.
- **Dismiss** (`d`) marks proposals dismissed. The decision is remembered
  for the same evidence; pins stay unchanged. There is no undo.
- A preview is not an authorization: when the ledger, the pin file, or the
  gate changed, the console asks for a new preview. A settings save of the
  pin-write gate also drops an open apply preview.
- **Partial apply**: the pin file and the ledger are not one transaction.
  When the pins were written but the ledger update failed, the result says
  so. Applying again would duplicate the pins, so the console refuses it
  and offers `c`, a ledger-only "record as applied" for proposals whose
  pins already exist. On the CLI with `distill.write` on, `seamark
  lessons --apply` with the IDs that the ledger did not record does the
  same record.
- **Generated proposals**: after a distillation, the result opens
  Proposals filtered to the IDs that the run saved. The filter is a
  visible chip, not a selection; `f`, `/`, or `esc` clears it.
- **Applied** rows are read-only. An applied proposal is a ledger state,
  not proof that its pin exists, so the detail follows the pin file: it
  names Lessons › Pins (`m`) when one entry links the proposal, says
  "not present" when no entry carries its pin, and lists every match
  when the link is ambiguous.
- **Superseded** rows are history: a confirmed prune removed their pins.
  They are read-only and cannot be restored here.
- **Trigger extraction** (`t`) opens the extraction review for the
  selected pending rows, or for the cursor row of the pending or the
  applied list. The footer shows `t` only when a target has an open
  trigger question; otherwise the detail says why. See **Trigger
  extraction** below.
- **Footer and help**: the footer lists only the keys that work for the
  cursor row, the selection, and the filters. `?` lists every Proposals
  key: `space`, `x`, `f`, `/`, `a`, `d`, `c`, `o`, `t`, and the pane
  key. It lists `esc all proposals` only while a run filter is on;
  without one the row is absent. The keys that do not work now follow
  the line `n/a: not available now`, and each of their rows reads `n/a`
  after the key (`c n/a  record applied`). On an applied row, for
  example, `space`, `a`, `d`, `x`, and `c` read `n/a`. A press of such a
  key opens nothing: on a read-only row, `a`, `d`, and `c` say that the
  row is read-only, and on a pending row without a known pin, `c` gives
  the reason (see **Existing pins**).

**Pins** lists the entries of `.seamark/lessons.yaml` in file order,
because the order decides delivery ties. The pin file is the authority
for the installed guidance, and the proposal ledger is the authority for
the recorded decisions. Pins relates the two by rule and region set, and
it never repairs one from the other. It reads the rule and the stored
region fields of a proposal as `seamark lessons` reads them, so both
name the same pin. It reads the file without the
index: without a readable ledger the pins stay listed, and every
relationship reads `link unknown`.

Each row shows the position in the file, the rule, the regions
(`repo-wide` for none, `N regions` for several; a long path keeps its
last components, for example `…/store`), and a state label. The label
has a glyph and words, so it reads without color:

| State | Meaning | Route |
| --- | --- | --- |
| `pN linked` | One entry and one applied proposal pN have this rule and these regions. | `w`, `-`, and `t`, as their rules below allow. |
| `pN edited` | Linked, but the live note differs from the note that pN recorded. | As linked. Maintenance keeps the live note; an extraction asks about the recorded note. |
| `no proposal` | No applied or pending proposal has this rule and these regions. That does not prove that a person wrote the pin. | Read-only; edit the file by hand. |
| `pending pN` | Only pending proposals match, and their stored region fields agree. | Read-only. Record pN as applied first: Proposals, select it, `c`. |
| `duplicate pin` | More than one entry of the file has this rule and these regions. | Blocked; remove the duplicates by hand. |
| `ambiguous` | More than one applied proposal records this pin. | Blocked; edit the pin by hand. |
| `ambiguous row` | The stored region fields of a matching applied or pending proposal disagree, so its apply writes a pin with other regions. | Blocked; edit the pin by hand. |
| `link unknown` | The pin file or the ledger could not be read. | Read-only; the state line names the error. |

Attention words follow the relationship, the most urgent first:
`blocked` (this entry cannot be edited safely), `unknown key` (the entry
has a key that no reader uses), and `edited`. Examples: `p30 blocked`,
`no proposal, unknown key`. A label that does not fit its column keeps
the first attention word and counts the others (`p9 blocked +2`); the
detail shows every word.

- **Lists**: `f` cycles **installed** (the default), **not present**, and
  **superseded**. Not present lists the applied proposals whose rule and
  regions no entry carries. That is a mismatch: Seamark does not
  supersede such a proposal or write its pin back. A proposal whose
  stored region fields disagree reads `ambiguous row` there, because the
  pin that its apply wrote can be in the file with other regions, so
  Seamark cannot prove that its pin is gone. Superseded lists the
  proposals whose pins a confirmed prune removed. Both history lists are
  read-only. The filter line shows the count of each list; a count that
  cannot be known shows `?`, never 0.
- **Filters**: `/` filters by a repository-relative region; a
  repository-wide pin matches every region. `z` searches the rule and the
  note, without regard to case: the live text of a pin, the recorded text
  of a history row.
- **Selection**: `space` marks an installed pin while the pin file and the
  ledger read; `x` clears the marks. A filter change clears the marks and
  says so, so a hidden pin never changes by accident. A refresh keeps a
  marked pin that only moved in the file. A marked pin that changed, left
  the file, or changed its link leaves the selection with a note. The
  cursor follows the same pin, and a note names a cursor pin that is gone.
- **Targets**: `w`, `-`, and `t` act on the marked pins in file order.
  Without a mark they act on the cursor pin, and the preview says "the
  cursor row". They never act on every pin. `v` acts on the cursor pin.
- **Action keys**: the footer and the help list `w`, `-`, `t`, and
  `v`, so each key can be found before a pin needs it. A key that the
  targets cannot use is muted and follows the word `n/a`. The word reads
  without color and in ASCII. Its press opens nothing, plans nothing,
  and writes nothing: one line gives the reason first and the pin last,
  for example `No retarget: its regions are already current
  ("wrap-sql-errors").` A narrow notice row cuts the end of the line,
  so it cuts the pin name before the reason.
- **Footer**: the available keys come first and the `n/a` group follows,
  for example `n/a: w retarget, v compare`. When the footer cannot hold
  every hint, the group shows only its keys (`n/a: w t v`), so `tab`,
  `f`, and `/` still fit at 120 columns. In a list without a pin to
  select (a history list, no pin file, an unreadable file), `tab`, `f`,
  `/`, and `z` come first, because `f` is the route back; the group
  follows them. At 120 columns and wider the footer always lists the
  four keys. A narrower footer drops hints from the end. It lists the
  four keys when the hints before them fit, and it then leaves out the
  clock if those cells keep the group in view. Otherwise `?` lists them.
- **Help**: `?` lists every Pins key at every size. The keys that do not
  work now follow the line `n/a: not available now`, and each of their
  rows also reads `n/a` after the key (`w n/a  retarget`), so a scrolled
  help still marks them.
- **Detail** (`tab` beside the list; `enter` and `esc` below 100 columns):
  the live note from the file, the regions, the source line, unknown keys
  ("no reader uses it"), and a seamark provenance comment above the entry.
  **PROPOSALS** states the link in one sentence and lists every matching
  applied, pending, or superseded proposal; an edited pin adds the
  recorded note, and a duplicate pin names each other entry with its
  line. **SIMILAR** and **MAINTENANCE** follow the readings.

**READINGS** are derived advice. They choose no action and select
nothing:

| Reading | Shows | Limit |
| --- | --- | --- |
| Evidence | the confidence tier and its facts | the same confidence as `seamark lessons --proposals` |
| Citations | how many cited findings are living; each living finding with its PR or source, reviewer, date, and path (`+N` counts its other paths, and a finding ID tells two equal rows apart); each cited finding that is no longer in the index | a finding that aged out, was reverted, or was removed is cited but not living; the finding text is not shown |
| Scope | `current`; `cannot recompute` when no cited finding is living; or `would change`, with the added (`+`) and removed (`−`) regions and a broader, repository-wide, or narrower note | the shared region engine computes it; it needs an indexed database, and missing evidence never widens a pin |
| Triggers | `open` or `answered` with the reason, the stored trigger paths, and each confirmed trigger that sets no region (its path is not in the working tree, or the region set is full) | only validated paths are stored; the reading names a hand edit only when `w` is not offered and the scope is not current |
| Outcome | the recurrence verdict with its window and reason | a derived recurrence check: not proof of cause, and not a reason to prune |
| Exposure | how often the pin was delivered and the first delivery, or "never delivered under its current rule, note and regions" | delivery history follows the rule, the note, and the regions, so a retarget starts a new history |

Evidence, Citations, Scope, Triggers, and Outcome need one linked
proposal; other pins show only Exposure. A reading whose input is not
available reads unknown, with the reason.

- **SIMILAR** lists other present, linked pins that the lessons
  clustering groups by wording. `v` opens a read-only comparison of the
  cursor pin with them: both rules, notes, and region sets side by side,
  and the paths that only one of them reaches (`tab` shows the next
  similar pin, `esc` closes). Without a similar pin, `v` is `n/a`, and
  its press says why. Similar wording does not mean the same
  coverage, and a similar pin is no reason to prune. A refresh that
  changes a compared pin closes the comparison with a note.
- **MAINTENANCE** names the keys that this pin offers, the reason for
  each key that it does not offer, and what the write gate does. A
  blocked pin names its route: edit the file by hand, record the pending
  proposal as applied first, or build the index.

Empty and error states:

- No pin file: no pin is installed. Pins come from applied proposals, so
  `p` opens Proposals and `e` opens Evidence.
- An empty pin list: apply a proposal in Proposals to add one.
- A pin file that cannot be read or does not parse: the view lists no pin
  and shows none as removed, because an error is not an empty list. The
  message names the line when it is known. Fix the file and press `r`.
  When the entries still decode, they stay listed, read-only, and the
  state line names the error.
- A file that the console cannot edit safely (a symbolic link, a
  read-only file, a file that this user cannot write, or a layout that
  the editor does not support) stays listed. The state line names the
  reason, and retarget and prune are off. A trigger extraction still
  works, because it never writes the file.
- A copied checkout whose index still records the original repository:
  a retarget or a prune would write the pin file of that repository and
  the ledger of this workspace. So the state line names the recorded
  root, retarget and prune are off, and MAINTENANCE names both pin
  files. Build the index of the copy from the Overview (`1`, then `b`)
  or run `seamark index` there, then press `r`. With `--db`, another
  recorded root is expected, and this check does not apply.
- No readable ledger (for example no index): the pins stay listed with
  `link unknown`. Build the index from the Overview (`1`, then `b`) or run
  `seamark index`.
- A not-present or superseded list that cannot be read says which read
  failed and gives its route. Seamark never reads an error as a missing
  pin.

**Retarget** (`w`) sets the regions of linked pins to the scope that the
shared region engine computes from today's living evidence and validated
triggers. **Prune** (`-`) removes linked pins and moves their proposals
from applied to superseded. A prune is a deliberate retirement. It
differs from a dismiss, which decides a pending proposal, and from the
cleanup of stale pending proposals after a distillation.

- `w` is available only when a target pin is linked, its entry and the
  file can be edited, and its computed scope differs from its live
  regions. That
  needs an indexed database and at least one living cited finding. When
  another pin or another applied proposal already has the rule with the
  computed regions, `w` is `n/a`, and the reason names that pin (with
  its line) or that proposal.
- `-` is available only when a target pin is linked and its entry and
  the file can be edited.
- In a copied checkout whose index records another repository, `w` and
  `-` are `n/a` (see the error states above).
- `t` is available only when a target pin is linked and its trigger
  question is open with living evidence. An unknown reading leaves `t`
  available, and the review decides.
- A key that is `n/a` opens nothing; it shows the reason in one line.

The preview:

- **Selected** lists each pin with a badge (`ready`, `no-op`, or
  `blocked`) and its reason. `y` shows only when every selected pin is
  ready. Otherwise the preview says "revise the selection" and offers no
  `y`: the console never processes a different subset. Unmark a pin with
  `space` in Pins, then preview again.
- **Retarget**: per pin, the removed (`−`) and added (`+`) regions, a
  warning for a broader or a repository-wide scope (a note for a narrower
  one), and why the evidence supports the new scope. When the note was
  edited after distillation, the live note stands beside the recorded
  note. The preview says that delivery history restarts under the new
  pin identity. LEDGER names each change, for example
  `p4 internal/store › internal/store/sql`; the proposals stay applied.
- **Prune**: per pin, the full live note, the regions, unknown keys (they
  leave with the entry), and a retained similar pin when one stays
  linked. A retained pin is advisory. Without one the preview says "No
  retained equivalent is established; this is a deliberate retirement."
  LEDGER names each change, for example `p16 applied › superseded`.
- WILL and WON'T lines follow, then the file diff with line numbers and
  `+`/`−` markers. The diff hides text that looks like a secret; the file
  gets the exact bytes.
- A summary line above the actions names the scope and what `y` writes.
  It stays visible at every size.
- A retarget that reads a stale index still plans, but the preview says
  so, and the summary line adds `index stale`. The computed regions then
  miss the newest history. To use it, build the index from the Overview
  (`1`, then `b`) and preview again.
- While another operation runs, the summary line says that `y` waits,
  and `y` does nothing until that operation ends.

The write gate and the commit:

- With `distill.write` off, the preview shows the full change, labels it
  "preview only", and offers no `y`. `o` opens Settings › Pin writing.
  Any settings save closes an open preview, so press `w` or `-` again
  after the save. A gate that cannot be read blocks the preview and
  offers no `o`.
- `y` plans again from the current file, ledger rows, write gate, and
  evidence. Any difference refuses the commit before a write: the result
  "Not written" says "Something changed since the preview — preview
  again. Nothing was written." `enter` then builds a new preview. When
  the pin has left the file since the request, the new preview names it
  by its rule and says "no longer in the file".
- The commit replaces the pin file atomically (a temporary file and a
  rename; the file mode stays), then updates the ledger rows in one short
  transaction that checks each row again. The two writes are not one
  transaction. The commit cannot stop safely: every key waits for the
  result, and `ctrl+c` offers to exit after it ends.

What a change keeps:

- A retarget changes only the `region` and `regions` lines of each
  reviewed entry, in place. It can add or remove a `regions` key, and it
  writes `region: "*"` for a repository-wide scope. The rule spelling,
  the live note, comments, unknown keys, blank lines, the entry position,
  other pins, mutes, and settings stay. The proposal stays applied, only
  its stored regions change, and its recorded note stays as history.
- A prune removes the lines of the entry and the seamark provenance
  comments directly above it that name its proposal. Other comments and
  other pins stay, also pins with the same rule in other regions.
  Findings, signatures, and the delivery log stay. No proposal becomes
  dismissed, and there is no undo.
- Before any write the candidate file must load, keep every other key and
  pin, and change only the reviewed lines. Otherwise the pin is blocked:
  "cannot prove the edit preserves the file".
- A new rule and region set that another entry, another selected pin, or
  another applied proposal already has is a conflict. The console never
  merges or overwrites pins.

The pin file layouts:

| | Layout |
| --- | --- |
| Supported | items of a block list under `pin:` at any consistent indent; keys in any order; comments and blank lines between and inside items; `regions` as a one-line flow list (`[a, b]`) or a block list with one value per line; quoted or plain values; keys that no reader uses (a retarget keeps them, a prune removes them with the entry) |
| Blocks the whole file | a symbolic link on the path; a file without the owner write bit; a file that this user cannot write (for example, an access control list denies it); more than one YAML document; a top level that is not a block mapping; a `pin:` value that is not a block list; aliases, anchors, merge keys, or tags in the pin section; duplicate keys; an empty `-` item; a tab in the indentation; CRLF or CR line breaks; text that is not UTF-8; a file that the lessons reader rejects |
| Blocks one entry | a flow-style entry (`{ … }`); a `rule` or `region` value that spans several lines or is not text; a flow `regions` list over several lines; a block `regions` item that is not one value on its own line |
| Blocks a retarget only | a key removal or move that would drop or separate a comment; a region without a safe one-line YAML form |

A blocked file or entry stays listed, and the detail names the reason
with its line. Edit such a file by hand.

The result names each effect on its own row, and its title never claims
a change that did not happen:

| Row | Says |
| --- | --- |
| Pin file | written, not changed, restored, or "changed or not checked" when the write reported a failure |
| File error, Ledger error | the error of that step |
| Ledger | the new ledger values, or that this operation did not change the rows |
| Restore | done, refused (another writer changed the file), failed, or not attempted |
| Rows now | the planned ledger rows as read again after a failure |
| Recovery | available (`c`), or "none can be proven: reconcile by hand" |

Every result ends with "Nothing is retried automatically."

- **Partial failure**: when the file write succeeds and the ledger update
  fails, Seamark reads the planned rows again. When every row still holds
  its earlier values, it writes the earlier pin file back, but only while
  the file still holds exactly the bytes that it wrote; when another
  writer changed the file, it restores nothing. When every row already
  holds its new values, another writer completed the ledger, and nothing
  more is needed. Any other state, or rows that cannot be read, restores
  nothing and needs reconciliation. The same rules apply when the file
  write reports a failure but the new bytes are in the file. When the
  file cannot be read after a failed write, its state is unknown, nothing
  is restored, and no recovery is offered. The result says "restored"
  only when the restore write succeeded.
- **Session recovery**: when the file holds this session's change and the
  ledger does not, the result offers `c`, a ledger-only completion with
  its own preview and `y`. The preview proves the result again from a
  fresh read: the pin file reads; after a retarget, the reviewed entry is
  in the file with the reviewed content and its earlier regions are gone;
  after a prune, the pruned pin is absent; every planned row still holds
  its earlier values; and `distill.write` is on. `y` records the ledger
  only; the pin file stays as it is. A completion that fails keeps the
  offer. `enter` never drops it; `esc` on the result drops it, and it
  cannot come back.
- **After a restart**: the proof lives in memory only. There is no
  journal, so a quit, a restart, or `esc` on the result loses it. Pins
  then shows the mismatch: after a retarget the pin reads `no proposal`
  and its proposal is under not present; after a prune the proposal is
  under not present. A bare absence never supersedes a proposal, and
  nothing repairs the mismatch automatically. To reconcile by hand, check
  `git diff .seamark/lessons.yaml`. Restore the entry with git and preview
  again, or keep the file and accept the mismatch. Do not repeat the
  operation while the file keeps the change; the changed pin no longer
  links its proposal, so the console does not offer it again.

**Stats** shows the lessons by source (review-mined lessons and pins; the
pin count includes the pins applied from proposals, so it does not mean
hand-written), the proposals by state, the top
regions, and records delivered per day (`f` changes the period), with the
most surfaced lessons. It keeps the delivery totals by surface (hook,
`change_set`, `check`), client attribution where it was recorded ("no
client recorded" otherwise), suppressed repeats, and eligible lessons never
recorded as delivered. Delivered means emitted through a recorded surface, not read,
followed, or effective. The counts cover the retained log; a refresh
reads a new snapshot. `seamark lessons --stats` prints from the same
reader, so both surfaces count the same records by the same rules.

**Evidence** shows the two evidence sources: local fix commits and GitHub
reviews. For each source it shows the stored findings, its freshness, and
what a refresh needs. `gh` on `PATH` is not proof of a login; only the
fetch tests access. The resolved GitHub repository comes from the `origin`
remote. The view needs a usable index; without one it links to the build
on the Overview and offers no refresh.

The freshness comes from the time of the last successful refresh that the
database records. The console never guesses a time:

- `refreshed <age>` (for example `refreshed 3h ago`): the database
  records the time. The detail pane adds the date.
- `never refreshed`: no time is recorded and the source has no stored
  finding. A new database starts here.
- `last refresh unknown`: findings exist without a recorded time (an
  older Seamark stored them before it recorded refresh times), or the
  stored evidence cannot be read.

The view offers two refreshes:

- **Refresh local fixes** mines the fix commits reachable from `HEAD`. It
  makes no network call and keeps the stored review findings.
- **Refresh reviews + local fixes** first mines the local fixes, then
  fetches the review comments through `gh`. The two sources succeed or
  fail on their own: a local success stays when GitHub fails.
- The preview names the workspace, the database, the repository, the
  review window (the newest comments always stay, so the window is not a
  strict cutoff), the network use, and the stored data it replaces. `y`
  starts; `esc` cancels. A change of `HEAD`, the remote, `config.yaml`, or
  the database before `y` asks for a new preview.
- Without a commit on `HEAD`, the local fix source reads unavailable and
  keeps the stored fix findings. When no fetch can run either (the local
  refresh, or no GitHub origin), the refresh changes no evidence, and the
  preview names no later effect.
- Each source ends as refreshed, successful-empty (an authoritative empty
  answer that replaces the stored findings), unavailable, failed, stopped,
  or not attempted. A failed, skipped, or stopped fetch never replaces the
  stored reviews. A source counts as refreshed only after its data is
  saved.
- A failed fetch names its cause, and the result shows the next step:
  `gh login` for no login, a bad or expired token, or a token without
  access (`gh auth login`); `rate limit` for HTTP 429 or a 403 rate limit
  (wait for the reset; `gh api rate_limit` shows it); `fetch error` for
  any other failure. The reason line keeps the text of `gh`.
- A stop during the local-fix mine lets that mine finish and save, then
  stops before any GitHub work. A stop during the fetch cancels `gh` and
  keeps the stored reviews.

**Distillation** runs in the **Run** sub-view. "Prepare distillation"
(from Evidence, or the Overview link) opens its scope step; it plans
nothing and sends nothing.

- **Scope**: `/` edits the region, which is normalized to a
  repository-relative path (a path outside the workspace is rejected).
  The group limit defaults to 10, the CLI default. "All eligible groups"
  is an explicit choice (the CLI `--limit 0`); a negative limit is
  rejected. `enter` builds the plan. When the agent configuration is
  invalid, the scope says why and `o` opens Settings › Inference.
- **Review**: the plan names the resolved agent and command, the groups in
  engine order with their evidence and its source references, the
  approximate prompt size (estimated; later prompts can grow with labels
  that earlier groups return, and no dollar figure exists), the groups
  that recorded signatures skip, and the stale pending proposals that the
  run removes. It also says what data goes to the agent. Planning runs no
  agent and writes nothing. `y` starts, `n` goes back to the scope, `esc`
  cancels.
- **Agent command**: the scope and the review hide a value that looks
  like a secret. A value that also holds shell syntax (`$(`, a backquote,
  `;`, `|`, `&`, `<`, `>`, or a line break) stays visible, because it is
  code that runs after `y`. The trigger extraction review shows the
  command the same way.
- **Blocked plan**: when the plan cannot start, the review says why and
  offers no `y`; `enter` plans again. When a place in the console fixes
  the blocker, `o` opens it: Settings › Inference for an invalid
  `.seamark/config.yaml` or an agent command that is not on `PATH`, or
  the Overview build for an index that is not ready. `o` only navigates;
  the fix stays a separate action.
- **Cleanup only**: when no group is eligible but stale pending proposals
  exist, the review shows "Nothing new to distill" and `c` offers to
  remove those proposals. A separate question follows: `y` removes them,
  `esc` keeps them. The cleanup calls no agent and leaves applied pins
  unchanged.
- **Start**: the console revalidates the plan first. A change to the
  findings, the decisions and signatures, the pins, the inference
  settings, or the workspace identity refuses the old plan and asks for a
  new one. It never adds groups, switches agents, or widens the scope.
- **Run**: the view shows the phases, each group's outcome, the saved
  proposal IDs, and the estimated usage. The usage counts a prompt as
  soon as its group is sent and says that the reply is pending. The
  console stays usable
  meanwhile, and the notice row keeps the run visible on every
  destination. `x` asks to stop (`y` confirms): no new group starts, the
  running agent call is cancelled, and each saved group stays saved. A
  provider can still bill a cancelled call.
- **Result**: completed, partial, failed, stopped, or nothing to do. Groups
  that failed are never reported as a success, and a zero-proposal reply
  differs from "no eligible work". `enter` reviews the generated
  proposals, and `n` prepares the remaining work (a new plan from the
  current state; saved groups are skipped, failed ones can be eligible
  again). No pin is ever applied by a run, and no run repeats by itself.
- Run records live for this session only. After a restart the stored
  findings, proposals, and signatures are the record. `[` and `]` step
  through the last five runs of every kind. An earlier run is read-only:
  the movement keys work, and `enter` reviews the proposals of an
  earlier distillation.

**Trigger extraction** asks the inference agent where the mistake of
each selected proposal occurs, so that its pin can reach the right
paths. It is a separate paid run in the **Run** sub-view. `t` opens its
review from Pins (the linked proposal of each target pin) or from
Proposals. The selection is the scope: the console never selects every
proposal, and `esc` closes the review and returns to where it opened.

- **Review**: the plan pane names the eligible and the selected count,
  the batches, the estimated prompt size (no cost figure exists), the
  agent command, and the directory where the agent runs. It says what
  each proposal sends: its ID, rule, recorded note, and cited evidence
  paths, and no finding text. The agent can also load the project
  instructions. It lists what the answers can change and what the run
  never does. The proposals pane lists the batches in send order, with
  the reason that each question is open ("never asked", or a negative
  answer to an older question). Then it lists every selected item that
  is not asked, with the reason. The detail of an item shows its regions,
  its cited paths, and what an answer changes. When the live note of a
  pin differs from the recorded note, both show, and the question uses
  the recorded note. Planning reads the index, the pins, and the agent
  configuration, checks only that the agent command is on `PATH`, runs
  no agent, and stamps nothing. `y` starts; a review without an eligible
  proposal offers no `y`.
- **Eligible**: a pending or applied proposal whose trigger question is
  open and that has at least one living cited finding. An applied
  proposal also needs exactly one linked pin. Not asked: a dismissed or
  superseded proposal; a question already answered with trigger paths,
  or answered negative under the current question; an applied proposal
  whose pin is not present or ambiguous; a pending proposal whose pin is
  already in the file (record it as applied first, Proposals › `c`); a
  proposal whose stored region fields disagree; and a pin without a
  linked proposal. No key asks an answered question again.
- **Batches**: pending proposals first, then applied ones, each newest
  first, 10 per batch, as `seamark lessons --extract-triggers` sends
  them.
- **Blocked review**: a malformed `.seamark/config.yaml` or
  `.seamark/lessons.yaml`, an agent setting that does not resolve, an
  index that is not ready, or an agent command that is not on `PATH`
  blocks the review and offers no `y`. `o`
  opens the place that fixes it: Settings › Inference, Lessons › Pins
  (which shows where the pin file fails), or the Overview build. A
  working directory that cannot be read needs a restart of Seamark from
  an existing directory. `enter` plans again.
- **Start**: `y` checks the review again: the workspace, the database,
  both files, the agent command, the working directory, every selected
  row with its living findings, and the pin links. A change refuses the
  start ("The preview is out of date"), and nothing is sent; `enter`
  plans again. A settings save also makes an open review out of date.
- **Run**: per-batch progress, the answers saved so far, and the
  estimated usage, which counts a prompt as soon as its batch is sent.
  The notice row keeps the run visible on every
  destination. Before each batch the run checks the files and the rows
  of that batch again, and a change stops the remaining batches ("an
  input changed"). `x` asks to stop (`y` confirms): no new batch starts,
  the running agent call is cancelled with its whole process group, and
  saved answers stay saved. A provider can still bill a cancelled call.
- **Answers**: each answer is saved per proposal when its batch returns.
  A proposal that changed during the call (applied, dismissed,
  retargeted, or answered elsewhere) is a conflict: its answer is not
  saved, and a new preview decides whether it can be asked. The result
  reads the ledger again and shows the current state of such a row. When
  the change closed its question (for example a dismiss, or an answer
  saved elsewhere), the row is listed as closed now, and `n` leaves it
  out. A pending proposal gets its
  validated trigger paths and new regions: a confirmed trigger replaces
  the regions with its own region, which can lie outside the current
  regions, and a negative or unconfirmed answer sets the coverage of its
  evidence. It stays pending, and Apply stays a separate decision. An
  applied proposal gets only its trigger paths, as advice: the pin file
  and the installed scope never change here. Pins then shows the new
  scope advice, and `w` previews a retarget when that scope differs.
- **Negative answers**: the agent lists only the proposals that it can
  place. A proposal missing from a valid reply is a completed negative
  answer ("omitted"). An empty list, or a reply with only invalid or
  missing paths, is a negative answer too. A negative answer is stamped
  with the current question, so it is not asked again. Only a failed
  call, a timeout, a reply that does not parse, or a reply without a
  `triggers` key leaves a question open. The console retries none of
  them by itself.
- **Result**: completed, partial, stopped, or failed; a run with an open
  question never reads as completed. The result counts the paths that the
  agent named, the valid ones, and the stored ones. It lists the pending
  proposals whose regions changed, and it groups every proposal: stored,
  negative (with the omitted ones), conflict, failed (by cause),
  cancelled, not attempted, or unknown. A question that another writer
  closed during the run also shows under closed now, with its reason.
  When several proposals conflict, the lead names each of them. `enter`
  shows the cursor proposal: an applied proposal with stored paths in
  Pins, an answered pending one in Proposals. It opens no preview. `n`
  prepares a new review of the questions that are still open, which
  starts only with its own `y`. The result ends with what the run did:
  it changed no installed pin scope and did not write
  `.seamark/lessons.yaml`. Nothing is retried, and nothing chains into a
  retarget or an apply.
- An extraction does not read `distill.write`, because it never writes a
  pin.

### 4 Doctor

Every check of `seamark doctor` with its state, detail, and fix. The fix
is selectable text; the console never runs it. A check with a known
in-app workflow shows a NEXT line, and `enter` opens that workflow (in a
narrow terminal the first `enter` opens the detail): `agent` opens
Settings › inference, `gh` opens Lessons › Evidence, `index` opens the
build confirmation, `hooks`, `mcp`, `approvals` and `skills` open
Integrations, and a client check opens that client. Other checks keep
`enter` for the detail.

### 5 Settings

The focused settings of `.seamark/config.yaml`, with the stored value,
the default, and the effective value of each field. The console reads
the file with the same readers as the CLI.

| Field | Values | Takes effect |
| --- | --- | --- |
| `agent.cli` | a registered preset with a one-shot command (`claude`, `codex`) | at the next prepared distillation or trigger extraction. Saving runs no model, authenticates nothing, and changes no integration file. |
| `agent.argv` | read-only; a custom command overrides the preset | choosing a preset while it is set shows its removal in the diff. The console never writes a custom command. |
| `distill.write` | off (default) or on | at the next apply, retarget, or prune preview. It also allows the CLI pin writes (`lessons --apply`, `--prune`, `--retarget`). Saving writes no pin; only a new preview offers the write. A trigger extraction does not read it. |
| `reviews.window_days` | default, a positive number of days, or unlimited (writes `0`) | at the next evidence refresh. The stored findings stay until then. |
| `index.generated` | skip (default) or include generated files | at the next index build. |
| `index.exclude` | an ordered list of globs (`a` add, `x` remove, `K`/`J` move) | at the next index build. Nothing is deleted or scanned now. |

- The fields form four groups: inference, pin writing, evidence, and
  indexing. Each group keeps its own draft, so several groups can hold
  drafts at the same time. A draft never saves by itself and never
  affects a run. `d` returns the field to its default, which removes the
  key (the diff shows the exact YAML effect). `x` discards the draft of
  the group under the cursor after a question.
- `s` shows the file diff of the group under the cursor; one save writes
  one group. `y` saves exactly that diff, and the drafts of other groups
  stay. `esc` keeps editing.
- Moving between groups asks nothing. Leaving Settings with any draft (a
  number key or a link) asks: keep the drafts, discard the drafts of
  every group, or stay. Kept drafts stay for this session. A quit (`q`,
  or `ctrl+c`) with any draft asks "Discard the unsaved settings draft
  and quit?": `enter` or `y` quits, `esc` stays. Without a draft the
  quit asks nothing. The question of a running operation comes first
  and also names the draft.
- A save changes only the lines of that group. It keeps comments, unknown
  keys, other sections, and the file mode, and it replaces the file
  atomically. Malformed or ambiguous YAML (duplicate keys, aliases),
  a symbolic link, a read-only file, a file that this user cannot write
  (for example, an access control list denies it), or a file that changed
  since the diff blocks the save and names the line to fix by hand. A
  missing file is created with the selected keys only.
- While another mutation runs, drafting still works, but `s` waits: the
  diff opens only after the run ends, and the list names the running
  operation. A diff that was open before the run started stays open, and
  its `y` waits for the end of the run.
- A save drops the previews that it invalidates, and every changed
  setting needs its own later run: a build, a refresh, or a new
  distillation, extraction, apply, retarget, or prune preview.

## Keys

| Context | Keys |
| --- | --- |
| Global | `1`–`5` destinations, `r` refresh the view, `?` help, `q` quit |
| Lessons sub-views | `p` Proposals, `m` Pins, `s` Stats, `e` Evidence, `u` Run |
| Proposals | `space` select pending rows, `x` clear the selection, `f` state (pending, applied, dismissed, superseded), `/` region, `a` apply, `d` dismiss, `c` record as applied, `o` open the PR, `t` trigger extraction; `?` lists every key, and `n/a` marks a key that the cursor row, the selection, or the filters cannot use; the footer lists only the keys that work |
| Pins | `↑↓` pins, `tab` detail, `space` mark, `x` clear the marks, `f` list (installed, not present, superseded), `/` region, `z` text search, `v` compare with a similar pin, `w` retarget preview, `-` prune preview, `t` trigger extraction; `?` always lists `w`, `-`, `t`, and `v`, and the footer does from 120 columns; `n/a` marks a key that the targets cannot use (its press names the reason) |
| Pins region or search field | `enter` apply, `esc` keep the previous value |
| Pins comparison | `tab` next similar pin, `↑↓` scroll, `esc`/`enter` close |
| Retarget or prune preview | `↑↓` scroll, `y` confirm (every pin ready, `distill.write` on, and no other operation running), `o` open Settings › Pin writing (gate off), `esc` cancel |
| Retarget or prune result | `enter` close, or preview again after a refusal; with a recovery offer: `c` preview the ledger-only completion, `esc` drop it |
| Ledger completion preview | `y` complete the ledger update, `o` open Settings › Pin writing (gate off), `esc` back to the result |
| Evidence | `↑↓` sources and actions, `enter` open the preview of the selected action, `tab` detail |
| Evidence preview | `y` start, `esc` cancel |
| Evidence refresh | `x` stop (confirm with `y`); after the end `enter` prepares a distillation, `n` previews the same refresh again |
| Distillation scope | `↑↓` field, `space` choose the limit option, `/` edit the region, `enter` review the plan, `o` open Settings › Inference (invalid agent configuration), `i` about (narrow terminals), `esc` close |
| Distillation review | `↑↓`/`pgup pgdn` groups, `tab` pane, `y` start, `n` back to the scope, `esc` cancel; a blocked plan: `o` open the place that fixes the blocker (Settings › Inference or the Overview build), `enter` plan again; a cleanup-only plan: `c` clean up (confirm with `y`, keep with `esc`) |
| Extraction review | `↑↓` proposals, `pgup pgdn` scroll, `tab` pane, `y` start, `o` open the place that fixes a blocker, `enter` plan again (a review that cannot start), `esc` close |
| Run, active | `↑↓` rows, `x` stop (confirm with `y`, keep running with `esc`), `tab` pane |
| Distillation result | `↑↓` rows, `tab` pane, `enter` review the generated proposals, `n` prepare the remaining work, `esc` close |
| Extraction result | `↑↓` rows, `tab` pane, `enter` show the cursor proposal in Pins or Proposals, `n` prepare a new review of the open questions |
| Run, earlier records | `[` earlier run, `]` later run; read-only: `↑↓` rows, `tab` pane, `enter` review the proposals of a distillation |
| Settings list | `↑↓` move, `enter` edit, `d` return to default, `s` review and save the group under the cursor, `x` discard the draft of that group, `tab` detail |
| Settings editor | `↑↓` choose, `space` select, `enter`/`esc` done; list editor `a` add, `x` remove, `K`/`J` move |
| Settings diff | `y` save, `esc` keep editing |
| Discard question (`x`) | `d` discard the draft of the group, `esc` keep it |
| Unsaved draft question (leaving Settings) | `k`/`enter` keep the drafts, `d` discard the drafts of every group, `esc` stay |
| Unsaved draft question (quit) | `enter`/`y` discard the drafts and quit, `esc` stay |

While a view captures keys (a text field, a preview, a question), the
footer shows `ctrl+c quit` instead of `q quit`. `q` during a run asks
whether to stop and exit: `enter` stops the run and exits once the work
has ended (at most 10 seconds), `esc` keeps working. During an index
build the question offers to exit after completion instead. While the
question is open, the footer shows `ctrl+c quit`: a second `ctrl+c`
answers yes, so it stops and exits (or exits after completion for a
build). `q` does nothing there. A retarget or prune commit cannot stop
safely either: its preview takes every key, and `ctrl+c` asks "Exit after
the running operation?"; `enter` exits once the commit has ended. A quit
with an unsaved Settings draft asks first, see Settings.

## Stays on the CLI

Manual pin authoring and editing (a new pin, the rule or the note, a
region set of your own choice), a change or a removal of a pin without
exactly one linked proposal, mutes, delivery budgets and mode, policy,
enforcement and effect-catalogue editing, authoring a custom
`agent.argv`, repository exploration (`orient`, `why`) and diff checks,
the HTML report, removing or revoking integrations, and more than one
workspace per session. A pin that the console does not change is edited
in `.seamark/lessons.yaml` by hand. Settings covers five fields; every
other key of `.seamark/config.yaml` is edited by hand.

## Versions and limits

- Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0. They keep the
  Go 1.25 minimum; Bubble Tea v2 needs Go 1.26.
- Index builds cannot be cancelled; the console offers to exit after
  completion instead.
- The review fetch through `gh` has no overall time limit and no output
  size limit yet. A stop cancels it; the child process then gets 5 seconds
  to close its pipes. `gh` is a single process, so this stops the whole
  fetch. One distillation agent call has a limit of 4 minutes. One
  trigger extraction call, a batch of up to 10 proposals, has a limit of
  5 minutes.
- The agent runs in its own process group, in a distillation and in a
  trigger extraction. A stop, a quit, or a closed terminal (SIGHUP) stops
  the whole group, so a wrapper script or a launcher cannot leave a model
  call running. The end of each call also ends the group, so a process
  that the agent leaves in its group does not outlive the call. A SIGKILL
  to seamark itself cannot clean up.
- The console plans strictly: a malformed `.seamark/config.yaml` or
  `.seamark/lessons.yaml` blocks the evidence refresh, the distillation,
  or the trigger extraction that reads it. A retarget or a prune also
  reads `distill.write` strictly: a gate that cannot be read blocks it.
  The CLI keeps its warning and its default values.
- The pin file and the ledger are two stores, and no transaction spans
  both. A retarget or a prune writes the file first and the ledger
  second. A partial result is reported, and its recovery lives only in
  this session: there is no journal and no general undo.
- The CLI forms of pin maintenance keep their earlier behavior.
  `seamark lessons --retarget` removes the entry and adds the stored
  proposal back at the top of `pin:`, so a hand-edited note, the rule
  spelling, and unknown keys of that entry do not survive.
  `seamark lessons --prune` removes every entry with the same rule and
  regions, duplicates included. `seamark lessons --extract-triggers`
  asks every eligible pending and applied proposal, also proposals whose
  cited findings aged out and applied proposals with duplicate pins. The
  console edits in place, blocks duplicates, and asks only the selected,
  eligible proposals.
- The index freshness covers the whole `.seamark/config.yaml`. After any
  settings save, also of the agent or the review window, the index reads
  as stale until the next build.
- A copied checkout keeps the index of the original repository until it
  is indexed again. That index records the original root. The console
  then works on the files of that root with the database of the copy, as
  the CLI does: the Proposals apply, the evidence refresh (also its git
  history), the distillation, and the trigger extraction read or write
  the files of the original repository. Only Pins detects the case: it
  turns retarget and prune off, and a retarget or prune commit refuses
  the case again before any write. Run `seamark index` in the copy before
  you use it.
- One workspace per invocation. No mouse support is needed or provided;
  commands and YAML are plain selectable text. Clipboard copy is an
  optional OSC 52 request that some terminals ignore.
