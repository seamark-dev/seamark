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
  Settings edits only the five fields listed in
  [Configuration](configuration.md#console-settings); it can remove a
  custom `agent.argv` after a preview, but never writes one.

## Destinations

Press `1`–`5` to switch; `tab` moves between panes; `?` shows every key of
the current view, and a key that does not work now reads `n/a` with its
reason. The footer clock is the time of the last refresh.

### 1 Overview

The state of the workspace at a glance.

- **Cards**: INDEX (coverage, parse errors, age, freshness, and tracked
  files deleted but not staged), NAVIGATE (symbols, edges, decisions),
  LEARN (pending proposals, lessons, review mining, distill agent), and
  GUARD (policy mode and rule counts, gate hooks). Missing data says why;
  it never shows as zero. A stale index adds a banner with `b rebuild
  now`. A narrow terminal folds the cards into the **Workspace summary**
  item, which shows every fact in full.
- **Attention**: actionable problems, optional additions, unsupported
  capabilities, and unknown evidence stay in separate groups, with one
  line per client and a doctor summary. No overall score.
- **Learning links**: an item for a blocked learning step (no evidence
  yet, findings without proposals, an agent or a settings reader that
  fails, pin writing off, a run in progress) opens Lessons or Settings
  with `enter`. A link only opens a view, and the Overview never suggests
  paid work that ran before or runs now.
- **Index build** (`b`): a local parse plus git history mining that writes
  the index database; `enter` or `y` confirms. It never fetches reviews or
  runs a distillation, and it cannot be cancelled, so `q` offers to exit
  after it finishes.

Plain route: `seamark status`, `seamark doctor`, `seamark index`. Exact
rules: the header of `internal/tui/overview_learn.go`.

### 2 Integrations

Claude Code and Codex from the shared client registry. Configuration
state, project trust, and native compatibility evidence are separate
columns: "configuration updated" never means "working". The capability
table shows where each capability lives, and the next corrective command
of each incomplete capability follows it.

Setup (`s` or `enter`) chooses additions (MCP registration, lifecycle
hooks, managed skills, tool grants). A capability that is already current
reads as kept (`[=]`): setup only adds. `i` explains the selected
addition. The review lists every affected file with its line counts, what
the plan WILL and WON'T change, and a numbered diff. `y` applies exactly
the reviewed plan, `n` goes back; a file that changed since the review
stops it before the first write. A fresh inspection then groups what is
left: **Needs attention**, **Not configured (optional)**, and **Remaining
native steps** (trust and native evidence; for Codex, review and trust the
hooks with `/hooks`). `u` refreshes the managed skills of the selected
client.

Plain route: `seamark init --client <claude|codex> [--skills]
[--approve-tools]` (add `--print` to preview); the CLI form always
installs hooks and registers MCP. Setup and the inference agent are
independent: setting up Codex does not make it the distill agent. Exact
rules: `internal/integration`.

### 3 Lessons

Five sub-views: **Proposals** (`p`), **Pins** (`m`), **Stats** (`s`),
**Evidence** (`e`), and **Run** (`u`, shown once a run or a draft exists
in this session). The sub-view keys work while the visible sub-view does
not capture keys. A narrow tab line shortens the labels (`Props`,
`Evid`).

**Proposals** lists pending, applied, dismissed, and superseded proposals
(`f` cycles the state, `/` filters by region). The detail shows the rule,
the note, the regions, the provenance, the cited evidence, and the
advisories of `seamark lessons --proposals`. `space` marks pending rows;
`x` clears the marks.

- `a` **apply** previews the pin block, the target file, and the ledger
  change. With `distill.write` off the preview only shows the YAML to
  paste under `pin:` (`c` copies it through OSC 52 where the terminal
  allows it). With the gate on, `y` writes the pins at the head of the
  `pin:` list and records the proposals as applied.
- `d` **dismiss** marks proposals dismissed for the same evidence; pins
  stay. There is no undo.
- `c` **record as applied** updates the ledger only, for a proposal
  whose pin is already in the file (a hand-written pin, or a partial
  apply). It works only when the console knows that pin.
- `o` opens the pull request of the cited evidence (http and https
  only); `t` opens a trigger extraction review (see Run).
- Apply refuses two marked proposals with the same rule and regions, a
  proposal whose rule and regions an applied proposal already has, and a
  proposal whose stored region fields disagree. When the console sees
  the refusal before a preview, `a` or `c` reads `n/a`, and one line
  names the step: dismiss with `d`, or edit by hand. The preview checks
  again in every case.
- A preview is not an authorization: a change of the ledger, the pin
  file, or the gate since the preview writes nothing and asks for a new
  preview. When the pins were written but the ledger update failed, the
  result says so and offers `c` instead of a second apply.
- Applied and superseded rows are read-only. The detail of an applied row
  follows the pin file: it names the linked pin in Pins, or says "not
  present".

Plain route: `seamark lessons --proposals`, `--apply <ids>`, `--dismiss
<ids>`; they run the same decision flow and give the same refusals.

**Pins** lists the entries of `.seamark/lessons.yaml` in file order,
because the order decides delivery ties. The pin file is the authority for
the installed guidance and the ledger for the decisions. Pins relates them
by rule and region set and never repairs one from the other. Each row
shows the rule, the regions (`repo-wide` for none), and a state label:

| State | Meaning | Route |
| --- | --- | --- |
| `pN linked` | One entry and one applied proposal have this rule and regions. | `w`, `-`, `t` |
| `pN edited` | Linked, but the live note differs from the recorded note. | As linked |
| `no proposal` | No applied or pending proposal has this rule and regions. | Edit by hand |
| `pending pN` | Only pending proposals match. | Record one as applied (`c` in Proposals) |
| `duplicate pin` | Several entries have this rule and regions. | Blocked; edit by hand |
| `ambiguous` | Several applied proposals record this pin. | Blocked; edit by hand |
| `ambiguous row` | A matching proposal's stored region fields disagree. | Blocked; edit by hand |
| `link unknown` | The pin file or the ledger could not be read. | Read-only; the state line names the error |

Attention words follow the relationship, the most urgent first: `blocked`,
`unknown key`, and `edited` (for example `p30 blocked`).

- `f` cycles the lists **installed**, **not present** (applied proposals
  whose pin no entry carries), and **superseded**. `/` filters by region,
  `z` searches the rule and the note.
- `space` marks installed pins and `x` clears the marks; a filter change
  clears them and says so. `w`, `-`, and `t` act on the marked pins, or on
  the cursor pin without a mark, never on every pin.
- The detail shows the live note, the regions, the source line, unknown
  keys, the linked proposals, and derived **READINGS** (evidence,
  citations, scope, triggers, outcome, exposure). Readings are advice:
  they choose no action. **SIMILAR** names linked pins with similar
  wording, and `v` compares the cursor pin with them side by side.
  **MAINTENANCE** names the keys that this pin offers and why the others
  are off.
- `w` **retarget** sets the regions of linked pins to the scope that the
  shared region engine computes from today's living evidence. `-` **prune**
  removes linked pins and moves their proposals from applied to
  superseded; it is a deliberate retirement, not a dismiss. `t` opens a
  trigger extraction review for the linked proposals.
- The preview lists every selected pin as `ready`, `no-op`, or `blocked`,
  the regions that change or the note that leaves, WILL and WON'T lines,
  the file diff, and the ledger change. `y` shows only when every pin is ready and
  `distill.write` is on; `o` opens Settings › Pin writing when it is off.
- `y` plans again from the current files, rows, gate, and evidence, and
  any difference writes nothing. The commit replaces the pin file
  atomically, then updates the ledger. The two writes are not one
  transaction: a partial result names each effect, and a ledger-only
  completion (`c`) is offered for this session only.
- A file that the editor cannot change safely (for example a symbolic
  link, a read-only file, CRLF line breaks, or an unsupported layout), a
  copied checkout whose index records another root, and a missing index
  keep the pins listed and turn the edits off; the state line says why.

Plain route: `seamark lessons --retarget <ids>` and `--prune <ids>` use
the same engine without a preview: they change the ready pins and print a
reason line for each other proposal.

**Stats** shows the lessons by source, the proposals by state, the top
regions, and the records delivered per day (`f` changes the period), with
the most surfaced lessons, the totals by surface (hook, `change_set`,
`check`) and by client, suppressed repeats, and eligible lessons never
delivered. Delivered means emitted through a recorded surface, not read or
followed. Plain route: `seamark lessons --stats`, from the same reader.

**Evidence** shows the two sources: local fix commits and GitHub reviews.
Each source shows its stored findings, its freshness (`refreshed <age>`,
`never refreshed`, or `last refresh unknown`; the console never guesses a
time), and what a refresh needs. It offers two refreshes: **Refresh local
fixes** mines the fix commits reachable from `HEAD` without a network
call, and **Refresh reviews + local fixes** also fetches the review
comments through `gh`. The preview names the workspace, the database, the
repository, the review window, the network use, and the stored data it
replaces. `y` starts the run; a change of `HEAD`, the remote,
`config.yaml`, or the database before `y` asks for a new preview. Each
source ends with its own outcome, and a failed or stopped fetch never
replaces the stored reviews. A stop lets a running local mine finish and
save, and it cancels the fetch. A failed fetch names its next step, such as
`gh auth login` or waiting for the rate limit. Plain route: `seamark index
--fixes-only` and `seamark index --reviews`.

**Run** holds the runs of this session (evidence refreshes,
distillations, and trigger extractions) and their results. `[` and `]`
step through the last five runs; an earlier run is read-only. A run holds
the single mutation slot, the notice row keeps it visible on every
destination, and `x` asks to stop it (`y` confirms); saved work stays
saved. In an agent run no new group or batch starts, and the running call
is cancelled with its whole process group. A provider can still bill a
cancelled call.

- **Distillation** starts from "Prepare distillation" in Evidence or an
  Overview link. The scope step takes a region (`/`) and a group limit
  (default 10, or all eligible groups); `enter` builds a read-only plan.
  The review names the agent and its command, the groups with their
  evidence, the approximate prompt size, the skipped groups, the stale
  pending proposals that the run removes, and the data sent. `y` starts
  after a fresh check of the plan; `n` goes back to the scope. A blocked
  plan offers `o` to the place that fixes it. When nothing new can be
  distilled but stale pending proposals exist, `c` offers their removal
  with its own question. The result reports each group; `enter` reviews
  the generated proposals, and `n` prepares the remaining work.
- **Trigger extraction** asks the agent where the mistake of each
  selected proposal occurs, so that its pin can reach the right paths.
  `t` in Pins or Proposals opens its review; the selection is the scope.
  The review names the eligible and the selected proposals, the batches,
  the prompt size, the agent command, what each proposal sends, and the
  reason for each proposal that is not asked. `y` starts after a fresh
  check. Answers are saved per proposal: a pending proposal gets new
  regions, and an applied proposal gets trigger paths as advice only. The
  pin file never changes here; Pins then shows the new scope advice. `n`
  prepares a review of the questions that are still open.

Plain route: `seamark lessons --distill` and `seamark lessons
--extract-triggers` run the same engines without a preview.

Exact rules: `internal/proposals` (decisions), `internal/pinfile` (pin file
layouts and edits), `internal/maintain` (readings, retarget, prune,
recovery), `internal/learning` and `internal/distill` (evidence,
distillation, trigger extraction), and the headers of
`internal/tui/proposals.go` and `internal/tui/pins.go` (the console keys).

### 4 Doctor

Every check of `seamark doctor` with its state, detail, and fix. The fix
is selectable text; the console never runs it. A check with a known
in-app workflow shows a NEXT line, and `enter` opens that workflow (in a
narrow terminal the first `enter` opens the detail): `agent` opens
Settings › inference, `gh` opens Lessons › Evidence, `index` opens the
build confirmation, `hooks`, `mcp`, `approvals` and `skills` open
Integrations, and a client check opens that client. Plain route:
`seamark doctor`. Exact rules: `internal/doctor`.

### 5 Settings

The focused settings of `.seamark/config.yaml`, with the stored value, the
default, and the effective value of each field: `agent.cli`, `agent.argv`
(read-only), `distill.write`, `reviews.window_days`, `index.generated`,
and `index.exclude`. [Configuration](configuration.md#console-settings)
lists their values and when each takes effect.

- The fields form four groups: inference, pin writing, evidence, and
  indexing. Each group keeps its own draft, and a draft never saves by
  itself. `d` returns a field to its default, which removes the key; `x`
  discards the draft of the group under the cursor.
- `s` shows the file diff of the group under the cursor; `y` saves
  exactly that diff, one group per save. A save keeps comments, unknown
  keys, and other sections, and replaces the file atomically. A file that
  the editor cannot change safely blocks the save and names the line to
  fix by hand.
- Leaving Settings or quitting with a draft asks first. A save drops the
  previews that it invalidates, and every changed setting needs its own
  later run.

Plain route: edit `.seamark/config.yaml`. Exact rules: `internal/settings`.

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
region set of your own choice), mutes, delivery budgets and mode, policy,
enforcement and effect-catalogue editing, authoring a custom
`agent.argv`, repository exploration (`orient`, `why`) and diff checks,
the HTML report, removing or revoking integrations, and more than one
workspace per session. A pin that the console does not change, for
example a pin without exactly one linked proposal, is edited in
`.seamark/lessons.yaml` by hand; `seamark lessons --retarget` and
`--prune` leave it as it is too. Settings covers five fields; every
other key of `.seamark/config.yaml` is edited by hand.

## Versions and limits

- Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0. They keep the
  Go 1.25 minimum; Bubble Tea v2 needs Go 1.26.
- Index builds cannot be cancelled; the console offers to exit after
  completion instead.
- The review fetch through `gh` has no overall time limit and no output
  size limit yet. A stop cancels it; the child process then gets 5 seconds
  to close its pipes. One distillation agent call has a limit of 4
  minutes. One trigger extraction call, a batch of up to 10 proposals, has
  a limit of 5 minutes.
- The agent runs in its own process group, in a distillation and in a
  trigger extraction. A stop, a quit, or a closed terminal (SIGHUP) stops
  the whole group, so a wrapper script or a launcher cannot leave a model
  call running. A SIGKILL to seamark itself cannot clean up.
- The console plans strictly: a malformed `.seamark/config.yaml` or
  `.seamark/lessons.yaml` blocks the evidence refresh, the distillation,
  or the trigger extraction that reads it. `seamark index --reviews` and
  `seamark lessons --distill` warn and continue instead.
- The pin file and the ledger are two stores, and no transaction spans
  both. A partial result is reported, and its recovery lives only in this
  session: there is no journal and no general undo.
- `seamark lessons --extract-triggers` asks every eligible pending and
  applied proposal. The console asks only the selected, eligible
  proposals.
- The index freshness covers the whole `.seamark/config.yaml`. After any
  settings save, the index reads as stale until the next build.
- A copied checkout keeps the index of the original repository until it
  is indexed again, and the console then works on the files of that
  root, as the CLI does. Only Pins detects the case and turns retarget and
  prune off; `seamark lessons --retarget` and `--prune` still edit the
  pin file of the original repository. Run `seamark index` in the copy
  before you use it.
- One workspace per invocation. No mouse support is needed or provided;
  commands and YAML are plain selectable text. Clipboard copy is an
  optional OSC 52 request that some terminals ignore.
