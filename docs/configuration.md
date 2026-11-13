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
`--json` for machines. `gate`, `check`, and `index` refuse a `-C` that
names no directory with `workspace <path>: not a directory` or `no such
file or directory`, and the gate blocks it under `--enforce`.

In a copied or moved checkout whose index still records the original
root, `seamark status` reports on that root, as the console does. Its
first line names that root and the `seamark index` run that records this
workspace, and `--json` adds `recorded_root`. `seamark doctor` reads the
`-C` workspace.

`seamark index` refuses a `--db` that records another repository, also
with `--force`, because that repository would lose its pins and
maintenance. A recorded path that does not exist now counts too, because
it can be a detached disk; only a file inside this workspace replaces
such a root by itself. A run that would skip still skips. Run the
command in that repository, or add `--move-root` to move the recorded
root to this workspace. The refusal names the flag only when the move
keeps one repository. A relative `--db` starts at the physical working
directory, as the system opens it. An index in a directory that refuses
writes cannot be read, because SQLite must create its WAL/SHM sidecars
there, so the refusal names that directory to make writable.

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
| `distill.write` | off (default) or on | at the next pin-write preview: apply, retarget, prune, new pins, pin edits, check definitions, and pin removals in Lessons › Pins; and for `lessons --apply`, `--prune`, `--retarget`, and `--set-mode`. Saving this setting writes no pin. A trigger extraction does not read it. |
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
| `threshold` | default, or a whole number from 1 to 2147483647 | 2 | the stored number; a stored 0 or a negative number reads as the default | at the next edit/exploration hook, preparation, `change_set`, `check`, `why`, `seamark lessons --file`, and Stats lesson counts; mined lessons only, never pins |
| `pin_budget` | as `threshold` | 3 | the stored number, at most 8; a stored 0 or a negative number reads as the default | in each edit-hook or exploration injection, at most 8 lessons total; pins come first, so 8 leaves no room for mined lessons |
| `change_budget` | as `threshold` | 6 | the stored number; a stored 0 or a negative number reads as the default | in one preparation, `change_set` answer or `check` advisory, shared by all its paths; not the hooks or `why`; preparation's check listing has no budget |
| `hook_delivery` | default, `always`, or `once-per-context` | `always` | the stored mode | repetition in the edit hook only; preparation, `change_set`, `check`, `why`, and CLI views always show their selection; exploration has separate repeat state |
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

### Executable lesson configuration

These keys belong to `.seamark/lessons.yaml`, not `config.yaml`. This
example defines a predicate; it is not a measured prevention result:

```yaml
lesson_checks_version: 1
checks:
  no-debug:
    kind: no-added-pattern/1
    pattern: 'DEBUG_ONLY'
  freeze-generated:
    kind: forbid-path-change/1
pin:
  - rule: keep-debug-markers-out
    region: src
    note: Do not add debug-only markers to source files.
    mode: check
    check: no-debug
```

| Key | Values and meaning |
| --- | --- |
| `lesson_checks_version` | `1` when checks or executable pin modes are used; a newer version is unsupported, not silently evaluated. |
| `checks.<id>` | A definition with `kind` and, only for the pattern checker, `pattern`. IDs use 1–64 lowercase letters, digits, dots, underscores or hyphens, starting with a letter or digit. Unknown definition fields are refused. |
| `kind` | `forbid-path-change/1` or `no-added-pattern/1`; the version is part of the predicate. |
| `pattern` | Nonempty Go regular expression, at most 1024 bytes, required only by `no-added-pattern/1`. No separate flags key. |
| `pin[].mode` | Omitted or `advisory`, `check`, or `enforce`. Only check/enforce pins run during verification. |
| `pin[].check` | Existing check ID, required for check/enforce. An advisory pin can retain a check, but verification lists it without running it. |

Checks use literal repository-relative file or directory scopes, not
wildcards. An empty scope or `*` means repository-wide. The checker
matches paths within scope; advisory delivery can also match a parent
scope. See [predicate semantics](lessons.md#executable-lessons-advisory-check-and-enforce)
before turning on an executable mode.

Advisory readers retain guidance when a semantic check definition is
invalid. Executable evaluation instead reports configuration errors and
runs no checker. An armed guard requests denial on invalid configuration.
Neither a check definition nor `mode: enforce` installs a handler; the
separate setup flags and trust state determine the active paths.
`seamark lessons --set-mode` changes only the selected pin's mode/check
through validation and `distill.write`. The TUI also edits definitions
through a reviewed diff.

### Verification state and report inputs

Verification reads staged and unstaged changes as they exist in the
working tree against `HEAD`, or the empty tree before the first commit.
Default selection excludes ignored files, content inside submodules,
and work outside the snapshot. Skip-worktree/assume-unchanged index
entries have their worktree changes excluded and named when present.
Explicit `--file` selection includes ignored paths but never follows a
symbolic link as file content.

Default selection also excludes exactly these paths **only when they
are untracked**:

- `.seamark/lesson-check-audit.jsonl`
- `.seamark/lesson-check-audit.jsonl.1`
- `.seamark/lesson-check-audit.jsonl.lock`

Tracked copies and explicit `--file` selections keep them. Other
`.seamark` files are not a blanket exemption. The `not covered` line of
a default selection names these three as `seamark's own lesson-check
audit files`.

Evaluation is bounded: 5000 snapshot paths, 50000 census directories,
4 MiB per file, 64 MiB snapshot content and Git output, and 20000 diff
lines. Explicit verification has a 30-second timeout. The guard has a
5-second evaluation budget; post-edit evaluation has 10 seconds. These
are handler limits, not the candidate client hook timeouts. Exceeding an
evaluation bound gives an unknown result, not a partial pass. Output
caps (10 evidence entries, 160 characters per excerpt, 80 per rule) trim
display only. See the result's named limit and rerun command.

Pre-edit handlers save baselines in `.seamark/checks/pending/`, with
owner-only directories/files and a Git exclusion. These files can contain
before-content; keep them private. Baseline content is bounded at 16 MiB;
oversized content stays qualified. Post-edit handling consumes the
baseline; the next handler sweeps expired entries after one hour and
stale temporary files after ten minutes.

Verification and hook decisions append metadata to
`.seamark/lesson-check-audit.jsonl` (`lesson-check-audit/1`). The writer
uses a separate lock, rotates at 5 MiB or an oldest record of 30 days,
and retains one rotated generation. These records keep outcomes,
identities as digests and evidence locations, not matching source-line
excerpts. They are not the reminder firing log.

The **delivery report** reads the reminder log
`.seamark/lessons-audit.jsonl`, both retained check-audit generations,
current `lessons.yaml` and integration inspections. Optional proposal
links come from a read-only index. Each audit source has a 256 MiB prefix
bound; the two check-audit generations share one bound. Sizes are fixed
at open, incomplete final lines are ignored, and malformed/newer-schema
counts remain visible. Pin versions stay separate.

The delivery report writes no durable state: no audit append, writer
lock, configuration or review change. SQLite can still create empty
WAL/SHM sidecars when opening an existing database read-only. The older
`--proposals`, `--stats` and HTML report paths retain their existing
writable index/Git metadata behavior.

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
verdicts and any regions that a person chose, and the distillation
memory that keeps paid agent calls from ever being repeated. Rebuilds (including `--force`) preserve those
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

### Going back to an older seamark

The first open by a new binary moves the database to schema 8 in place,
and the older binary then refuses it. That first open can come from a
hook, so make the backup before you replace the binary. Each worktree
has its own database, so each one needs its own backup.

A bundle exported by the new binary has state version 4, so it is no way
back: an older binary refuses it. The way back is a backup made before
that first open, in one of two forms:

- a copy of `.seamark/index.db` with any `-wal` and `-shm` file, made
  while no seamark process runs. Put all of them back together, so that
  no side file of the schema 8 database stays beside the copy.
- `seamark state export`, run by the older binary. Move the schema 8
  files aside first, then run `seamark state import` with the older
  binary. Then rebuild with `seamark index`.

Keep the schema 8 files: they hold your decisions. Decisions after the
backup do not return. A pin whose regions changed after the backup then
reads as no proposal, so put `.seamark/lessons.yaml` back to the same
point. Use git if your repository tracks that file, else keep a copy with
the backup. A pin that you added after the backup leaves the file with
that step, so copy its text out first.

A newer binary still reads a bundle of an older version. A row of such a
bundle records no scope choice.
