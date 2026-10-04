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

The same `config.yaml` carries the other opt-ins, each defaulting to
the safe side:

```yaml
reviews:
  window_days: 730 # review-comment shelf life; 0 = unlimited.
  #                  The newest 200 comments always survive the window.
distill:
  write: false # --apply/--prune/--retarget print the block for you to
  #              paste; flip to true to let them edit lessons.yaml
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
