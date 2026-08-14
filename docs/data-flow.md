# Data flow

What each seamark capability reads, where its output goes, whether it
touches the network, and what it persists. This is the reference the
README's claims are held to; if behavior and this document disagree,
that is a bug.

Seamark itself runs no cloud service, sends no telemetry, and stores no
credentials. Three boundaries can carry repository-derived data further
than this machine, all through tools you configure: the `gh` CLI for
review mining, your agent CLI for distillation, and the agent client you
connect over MCP (or the editor over LSP) — everything seamark serves to
a connected client is content that client may forward to *its own* model
service. Each is authenticated and chosen by you; the first two are
optional.

## Capability matrix

| Capability | Data source | Destination | Network | Persisted |
|---|---|---|---|---|
| Index (`seamark index`) | source tree, local git history | `.seamark/index.db` | none | yes |
| Review mining (`index --reviews`) | GitHub PR review comments, via your `gh` | `index.db` (lessons, findings) | GitHub API through `gh` | yes |
| Fix mining (part of `index`) | local `git log` | `index.db` (findings) | none | yes |
| Distillation (`lessons --distill`) | mined findings | your agent CLI's stdin → its model service | via the agent CLI | proposals + signature marks in `index.db` |
| Trigger backfill (`lessons --extract-triggers`) | proposal notes + evidence paths and one excerpt each | your agent CLI's stdin → its model service | via the agent CLI | validated trigger paths + answered-question stamps in `index.db` |
| Pin apply (`lessons --apply`) | your decision | `.seamark/lessons.yaml` (committed file; only with `distill.write`) | none | yes |
| Gate hook (`gate --hook`) | the agent's proposed command | verdict on stdout/exit code; `.seamark/audit.jsonl` | none | audit entries |
| Edit hook (`lessons --hook`) | edited file path and provider session ID | lesson reminders on stdout | none | firing log plus optional local, digest-only once-per-context state |
| Diff check (`seamark check`) | a unified diff | verdict; audit entry; index self-repair | none | audit entries, refreshed index |
| MCP server (`seamark mcp`) | `index.db` | the connected agent client, per request (stdio) | none | index self-repair |
| LSP server (`seamark lsp`) | source tree, `index.db` | your editor (stdio) | none | refreshed index |
| Report (`seamark report`) | `index.db` | a local HTML file | none | yes |
| State (`seamark state export`/`import`) | durable tables in `index.db` | a local JSON file, or stdin/stdout | none | yes |
| Health (`seamark status`, `seamark doctor`) | `index.db`, local config, PATH | terminal | none | no |

Every command that runs `git` or `gh` does so as a subprocess of the
local binary you already trust; seamark adds no credentials of its own
and requires none (`gh` must be authenticated by you for `--reviews`).

## What distillation sends

`lessons --distill` is the one capability that hands repository-derived
text to a model. Before the first agent call it prints a preflight: the
exact agent command line, how many groups and findings would be sent,
and the approximate token cost. The payload per finding is:

- the reviewer's comment or fix-commit subject, verbatim (it may quote
  your code, because reviewers do), capped per finding;
- the finding's provenance: its id, source kind (review / fix / revert),
  PR number when known, and the reviewer's name;
- the repo-relative file path;
- the rule labels of patterns already captured for the area — pinned
  ones and previously proposed ones alike, dismissed included (so the
  model does not re-derive or relitigate them).

No source files, no diffs, no environment values. **No redaction is
applied to finding bodies** — they are sent as reviewers wrote them.

The executable itself comes from the committed
`.seamark/config.yaml` (`agent:` section). That file chooses what
`--distill` runs — treat changes to it like code, and check the
preflight's `agent` line in a repository you did not author
(see [threat-model.md](threat-model.md)).

```bash
seamark lessons --distill --dry-run   # the full disclosure, nothing sent
```

The dry run prints metadata only — never finding bodies — and works
even when the agent CLI is not installed.

`lessons --extract-triggers` sends a smaller slice through the same
agent CLI: per already-distilled proposal, its rule label, its note
(model-written text you reviewed at apply time), the repo-relative
paths of its cited evidence, and ONE evidence excerpt capped at 400
characters. Same preflight, same `--dry-run`, same `agent:` line from
`config.yaml`. The reply is never trusted: named paths must exist in
the working tree, and only co-change-confirmed ones widen delivery.

## Command declarations

| Command | Network | Sends data to another process/model | Writes repo-local state | Modifies committed files | Can block | Needs credentials |
|---|---|---|---|---|---|---|
| `init` | no | no | `.seamark/` scaffolds, `.claude/settings.json` | `.gitignore`, scaffolded YAML (meant to be committed) | no | no |
| `index` | only `--reviews`, via `gh` | no | `index.db` | no | no | `gh` auth for `--reviews` |
| `why` / `orient` | no | no | no | no | no | no |
| `lessons` | no | `--distill` and `--extract-triggers`: your agent CLI | proposals, trigger paths in `index.db`; firing log | `lessons.yaml`, only via `--apply`/`--prune`/`--retarget` with `distill.write` | no | the agent CLI's own |
| `report` | no | no | the HTML file | no | no | no |
| `gate` | no | no | `audit.jsonl` | no | exit 2 under enforce | no |
| `check` | no | no | `audit.jsonl`, index refresh | no | exit 2 under enforce | no |
| `mcp` | no | the connected agent client | index refresh, `audit.jsonl` (check tool) | no | no | no |
| `lsp` | no | your editor | index refresh | no | no | no |
| `state` | no | no | `index.db` on import | no | no | no |
| `status` | no | no | no | no | no | no |
| `doctor` | no | no | no | no | no | no |

See [threat-model.md](threat-model.md) for what these boundaries do and
do not defend against.
