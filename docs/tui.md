# Workspace console (`seamark tui`) — experimental

`seamark tui` opens a full-screen console for one workspace. It shows what
Seamark knows about the repository and its agent integrations, and it runs
the everyday workflows end to end: setting up or repairing an integration,
collecting review and fix evidence, distilling proposals with the
inference agent, deciding on those proposals, and editing the settings that
these workflows read. It is experimental. The plain commands stay the
stable interface, and every console action has a plain route.

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
  start an agent, save settings, or grant trust. SQLite may create its
  `-wal` and `-shm` side files next to an existing database.
- It never starts network or model work, and never writes a durable
  change, without a preview. The start or the save is always `y` in that
  preview, never `enter`; `esc` before `y` changes nothing. The index
  build keeps its confirmation (`enter` or `y`), because it writes only
  local derived data.
- It never chains steps. An evidence refresh does not start a
  distillation, a distillation never applies a pin, and a settings save
  runs nothing. Each step has its own preview.
- It never runs a command taken from diagnostics. Corrective commands are
  selectable text.
- It never retries work, and it runs one mutation at a time.
- It never changes the policy, the gate mode, or pins outside a reviewed
  apply. Settings edits only the five fields listed below; it can remove
  a custom `agent.argv` after a preview, but never writes one.

## Destinations

Press `1`–`5` to switch; `tab` moves between panes; `?` shows the keys of
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
  | A distillation or evidence refresh runs | Optional | Lessons › Run |

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

Four sub-views: **Proposals** (`p`), **Stats** (`s`), **Evidence** (`e`),
and **Run** (`u`, shown once a run or a distillation draft exists in this
session). The sub-view keys work while the visible sub-view does not
capture keys.

**Proposals** lists pending, applied, and dismissed proposals (`f` cycles
the state, `/` filters by region). The detail shows the full rule and note,
regions, provenance, the cited evidence (or "unavailable" when it aged
out), and the advisories of `seamark lessons --proposals`. `space` selects
pending rows; a filter change clears the selection.

- **Apply** (`a`) opens the pin block in a modal with the target file and
  the ledger change. With `distill.write` off the action only shows the
  YAML: copy the block under `pin:` in `.seamark/lessons.yaml` (`c` asks
  the terminal to copy it through OSC 52, if the terminal allows it), or
  enable the gate in Settings and open a new preview. The preview also
  shows `seamark lessons --apply <ids>`, which prints the same block. A long
  note folds onto indented lines that YAML reads as the same text. With the
  gate on, `y` writes the pins and then records the proposals as applied.
- **Open PR** (`o`) opens the pull request of the cited evidence in the
  browser. Only http and https links open.
- **Existing pins**: when a selected proposal already has its pin, the
  preview names it and writes nothing for it, with the gate on or off.
  `c` records such proposals as applied in the ledger only; the footer
  offers `c` only when a pin is known to exist.
- **Dismiss** (`d`) marks proposals dismissed. The decision is remembered
  for the same evidence; pins stay unchanged. There is no undo.
- A preview is not an authorization: when the ledger, the pin file, or the
  gate changed, the console asks for a new preview. A settings save of the
  pin-write gate also drops an open apply preview.
- **Partial apply**: the pin file and the ledger are not one transaction.
  When the pins were written but the ledger update failed, the result says
  so. Applying again would duplicate the pins, so the console refuses it
  and offers `c`, a ledger-only "record as applied" for proposals whose
  pins already exist.
- **Generated proposals**: after a distillation, the result opens
  Proposals filtered to the IDs that the run saved. The filter is a
  visible chip, not a selection; `f`, `/`, or `esc` clears it.

**Stats** shows the lessons by source, the proposals by state, the top
regions, and records delivered per day (`f` changes the period), with the
most surfaced lessons. It keeps the delivery totals by surface (hook,
`change_set`, `check`), client attribution where it was recorded ("no
client recorded" otherwise), suppressed repeats, and eligible lessons never
recorded as delivered. Delivered means emitted through a recorded surface, not read,
followed, or effective. The counts cover the retained log; a refresh
reads a new snapshot.

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
  proposal IDs, and the estimated usage. The console stays usable
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
  through the last five runs. An earlier run is read-only: the movement
  keys work, and `enter` reviews its proposals.

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
| `agent.cli` | a registered preset with a one-shot command (`claude`, `codex`) | at the next prepared distillation. Saving runs no model, authenticates nothing, and changes no integration file. |
| `agent.argv` | read-only; a custom command overrides the preset | choosing a preset while it is set shows its removal in the diff. The console never writes a custom command. |
| `distill.write` | off (default) or on | at the next apply preview. It also allows the CLI pin writes (`lessons --apply`, `--prune`, `--retarget`). Saving applies nothing. |
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
  a symbolic link, a read-only file, or a file that changed since the
  diff blocks the save and names the line to fix by hand. A missing file
  is created with the selected keys only.
- While another mutation runs, drafting still works, but `s` waits: the
  diff opens only after the run ends, and the list names the running
  operation. A diff that was open before the run started stays open, and
  its `y` waits for the end of the run.
- A save drops the previews that it invalidates, and every changed
  setting needs its own later run: a build, a refresh, or a new
  distillation preview.

## Keys

| Context | Keys |
| --- | --- |
| Global | `1`–`5` destinations, `r` refresh the view, `?` help, `q` quit |
| Lessons sub-views | `p` Proposals, `s` Stats, `e` Evidence, `u` Run |
| Evidence | `↑↓` sources and actions, `enter` open the preview of the selected action, `tab` detail |
| Evidence preview | `y` start, `esc` cancel |
| Evidence refresh | `x` stop (confirm with `y`); after the end `enter` prepares a distillation, `n` previews the same refresh again |
| Distillation scope | `↑↓` field, `space` choose the limit option, `/` edit the region, `enter` review the plan, `o` open Settings › Inference (invalid agent configuration), `i` about (narrow terminals), `esc` close |
| Distillation review | `↑↓`/`pgup pgdn` groups, `tab` pane, `y` start, `n` back to the scope, `esc` cancel; a blocked plan: `o` open the place that fixes the blocker (Settings › Inference or the Overview build), `enter` plan again; a cleanup-only plan: `c` clean up (confirm with `y`, keep with `esc`) |
| Run, active | `↑↓` rows, `x` stop (confirm with `y`, keep running with `esc`), `tab` pane |
| Run, result | `↑↓` rows, `tab` pane, `enter` review the generated proposals, `n` prepare the remaining work, `esc` close |
| Run, earlier records | `[` earlier run, `]` later run; read-only: `↑↓` rows, `tab` pane, `enter` review its proposals |
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
build). `q` does nothing there. A quit with an unsaved Settings draft
asks first, see Settings.

## Stays on the CLI

Trigger extraction (`seamark lessons --extract-triggers`), prune and
retarget, manual pin authoring and editing, mutes, delivery budgets and
mode, policy, enforcement and effect-catalogue editing, authoring a custom
`agent.argv`, repository exploration (`orient`, `why`) and diff checks,
the HTML report, removing or revoking integrations, and more than one
workspace per session. Settings covers five fields; every other key of
`.seamark/config.yaml` is edited by hand.

## Versions and limits

- Bubble Tea v1.3.10, Bubbles v1.0.0, Lip Gloss v1.1.0. They keep the
  Go 1.25 minimum; Bubble Tea v2 needs Go 1.26.
- Index builds cannot be cancelled; the console offers to exit after
  completion instead.
- The review fetch through `gh` has no overall time limit and no output
  size limit yet. A stop cancels it; the child process then gets 5 seconds
  to close its pipes. `gh` is a single process, so this stops the whole
  fetch. One agent call has a limit of 4 minutes.
- The agent runs in its own process group. A stop, a quit, or a closed
  terminal (SIGHUP) stops the whole group, so a wrapper script or a
  launcher cannot leave a model call running. A SIGKILL to seamark
  itself cannot clean up.
- The console plans strictly: a malformed `.seamark/config.yaml` or
  `.seamark/lessons.yaml` blocks the evidence refresh or the distillation
  that reads it. The CLI keeps its warning and its default values.
- The index freshness covers the whole `.seamark/config.yaml`. After any
  settings save, also of the agent or the review window, the index reads
  as stale until the next build.
- `seamark lessons --apply` on the CLI does not check for pins that already
  exist. After a partial apply in the console, record the proposals with
  `c` in the console instead of applying them again on the CLI.
- One workspace per invocation. No mouse support is needed or provided;
  commands and YAML are plain selectable text. Clipboard copy is an
  optional OSC 52 request that some terminals ignore.
