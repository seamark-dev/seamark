# Configuration and local state

Reference settings for Seamark. For first-time setup, start with
[Getting started](getting-started.md).

## Configuration

**What gets indexed** — the indexer skips anything `.gitignore` ignores
and files carrying the conventional `Code generated … DO NOT EDIT.`
header (generated code inflates the graph and pollutes most-called lists
without ever being hand-navigated). A committed `.seamark/config.yaml`
tunes both:

```yaml
index:
  generated: true # index generated files after all
  exclude: # extra paths to skip, on top of .gitignore
    - "*.pb.go" # basename glob, any directory
    - "internal/gen/" # directory prefix
    # - "**/*_test.go" # uncomment for a production-only graph
```

Skipped files are counted in the `seamark index` output, and a malformed
config — including an exclude glob that could never match — fails the
index loudly rather than being silently ignored. Config edits count as
workspace changes, so the next index picks them up automatically.

The indexer also leaves out a tracked file that is deleted in the working
tree while the deletion is not staged. `seamark index` and
`seamark status` count these paths apart from parse errors. A path
outside a sparse checkout is left out without a count, because
`git status` shows no change for it either.

The same `config.yaml` carries the other opt-ins, each defaulting to
the safe side:

```yaml
reviews:
  window_days: 730 # review-comment shelf life; 0 = unlimited.
  #                  The newest 200 comments always survive the window.
distill:
  write: false # --apply prints the block for you to paste, and
  #              --prune/--retarget print the change to make by hand;
  #              flip to true to let them edit lessons.yaml
agent:
  cli: claude # the agent CLI --distill pipes findings through (the default)
  # cli: codex # use Codex for lesson generation
  # argv: ["my-llm", "--stdin"]   # …or a custom command line
```

`agent.cli` selects the AI coding agent CLI used for `lessons --distill`
and `lessons --extract-triggers`. It is separate from the clients that
`seamark init --client` sets up. `agent.argv` overrides the preset when set.
See [inference setup and authentication](agent-integrations.md#choose-an-agent-for-inference)
for the commands, model access, data sent, and compatibility evidence.

To let an explicit proposal decision update `lessons.yaml`, change the existing
`distill` section to:

```yaml
distill:
  write: true
```

This does not apply proposals automatically. You still select their IDs with
`seamark lessons --apply`. With `write: false`, the command only prints the
YAML to paste and leaves the proposal pending.

History mining has two flags on `seamark index` itself: `--max-commits`
bounds the git window (default 5000) and `--max-files-per-commit`
excludes bulk refactors from co-change (default 30). Every command
takes `-C <dir>` to name the workspace and `--db <path>` to override
the index location; `status`, `doctor`, `gate`, and `check` speak
`--json` for machines.

### Console settings

The Settings destination of `seamark tui` edits five fields of
`config.yaml` and shows `agent.argv` read-only. It also edits the lesson
settings of `.seamark/lessons.yaml` (see below). It reads each file with
the same readers as the CLI. A save writes one file only; each field
takes effect at the step that reads it next.

| Field | Values | Takes effect |
| --- | --- | --- |
| `agent.cli` | a registered preset with a one-shot command (`claude`, `codex`) | at the next prepared distillation or trigger extraction. Saving runs no model, authenticates nothing, and changes no integration file. |
| `agent.argv` | read-only in the console; a custom command overrides the preset | when the console saves a preset while it is set, the diff shows its removal. The console never writes a custom command. |
| `distill.write` | off (default) or on | at the next pin-write preview: apply, retarget, prune, and new pins, pin edits, and pin removals in Lessons › Pins, in the console; and for `lessons --apply`, `--prune`, and `--retarget`. Saving writes no pin. A trigger extraction does not read it. |
| `reviews.window_days` | default, a positive number of days, or unlimited (writes `0`) | at the next evidence refresh. The stored findings stay until then. |
| `index.generated` | skip (default) or include generated files | at the next index build. |
| `index.exclude` | an ordered list of globs | at the next index build. Nothing is deleted or scanned at the save. |

`d` in the console returns a field to its default, which removes the key
line with its comment. A save keeps every other comment. So the console
refuses to remove an exclude pattern with a comment after its text; edit
that pattern by hand. Every other key of `config.yaml` is edited by hand.

#### Lesson settings

Two more groups, **Lesson delivery** and **Lesson mutes**, edit
`.seamark/lessons.yaml`. Each field shows three values: the stored text
as the file holds it, the default, and the effective value that the
lesson readers use. A stored value that the reader reads differently
gets a note, for example `stored 0 → default 3` or `stored 100 → at
most 8 pins per edit (the hook total cap)`.

| Field | Values | Default | Effective value | Takes effect |
| --- | --- | --- | --- | --- |
| `threshold` | default, or a whole number from 1 to 2147483647 | 2 | the stored number; a stored 0 or a negative number reads as the default | at the next edit hook, `change_set`, `check`, `why`, `seamark lessons --file`, and the Stats lesson counts; mined lessons only, never pins |
| `pin_budget` | as `threshold` | 3 | the stored number, at most 8; a stored 0 or a negative number reads as the default | in the edit hook only, at most 8 lessons in total per edit; pins come first, so a budget of 8 leaves no room for mined lessons |
| `change_budget` | as `threshold` | 6 | the stored number; a stored 0 or a negative number reads as the default | in one `change_set` answer or one `check` advisory, shared by all its files; not the hook or `why` |
| `hook_delivery` | default, `always`, or `once-per-context` | `always` | the stored mode | repetition in the edit hook only; `change_set`, `check`, `why`, and the CLI views always show their selection |
| `mute` | one entry per save: add, change, or remove an entry with a rule, a region, or both | none | the entries that the reader decodes | as `threshold` |

- A save of a lesson setting writes `.seamark/lessons.yaml` only. It
  needs no index, and `distill.write` does not gate it, because it
  writes no pin. It applies when a consumer next reads the file, so a
  hook that runs now does not change.
- A delivery save does not install or change hooks, grant trust, reset
  or delete delivery state, re-mine, or call a model. The hook state
  file stays, so a switch back can reuse still-valid context state.
- Default removes the key. The mute list has no reset: one save changes
  one entry.
- A mute matches a rule in any case, and a region and every region
  under it. A region with a trailing "/" (`api/`) matches only the
  regions under it, not `api` itself, because a lesson region has no
  trailing "/". A mute never hides a pin. The editor shows an entry that
  matches nothing, a duplicate, and an entry that overlaps another one.
- A missing `lessons.yaml` is created with only the saved key. A save
  keeps comments, unknown keys, and the pin list byte for byte. A reset
  removes the key line with its comment. The console refuses to remove
  a mute entry with a comment after its text. Remove that entry by
  hand.
- Some files block both lesson groups: a file with CRLF line breaks, a
  file with more than one hard link, and a file that the lessons reader
  rejects, for example for an unknown `hook_delivery` mode. The
  config.yaml groups stay usable.

## Effect catalogue

Effect knowledge is data, not code. The built-in
catalogue covers ~40 sinks across Go, Python, and JS/TS plus common CLI
tools; your workspace extends it additively in `.seamark/effects.yaml`:

```yaml
sinks:
  - language: python
    import: my_company_infra
    names: [apply, provision]
    tag: infra:mutate
commands:
  - name: my-deploy-tool
    subcommands: [rollout]
    tag: infra:mutate
```

Custom tags propagate up the call graph and participate in policy exactly
like the built-ins.

## Durable state: the index is not a throwaway cache

`.seamark/index.db` carries two kinds of state with different lifecycles.
The derived graph — symbols, edges, co-change, decisions, effects — is
rebuilt from the workspace on every reindex. But the same file also holds
**durable decisions**: proposal history with your applied/dismissed
verdicts, and the distillation memory that keeps paid agent calls from
ever being repeated. Rebuilds (including `--force`) preserve those
tables; **deleting the file destroys them**. The schema is versioned with
ordered migrations — an older seamark refuses a newer database instead of
guessing at it.

To back up or move the durable part:

```bash
seamark state export --out decisions.json   # proposals + distillation memory
seamark state import decisions.json         # merge into this clone (works pre-index)
```

Import never overwrites a local decision: it adds missing rows, and a
still-pending proposal may adopt an imported verdict — a decision beats
no decision.
