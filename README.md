# Seamark

**The graph your repo's history knows and your code's blast radius — served to your editor, your agents, and your CI.**

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-%E2%89%A51.25-00ADD8.svg)](go.mod)
[![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey.svg)](#get-started)

Seamark indexes which files *really* change together, why the weird parts
are weird, and which code paths can reach a database write, a process
spawn, or your production infrastructure. It serves that as editor
diagnostics, answers `why` on the CLI, and enforces your rules as
machine checks on agent commands — not as paragraphs in a prompt.
**Mistakes get caught instead of explained.**

> A *seamark* is a navigational marker that shows both the safe channel
> and the hazard.

- [Why seamark?](#why-seamark)
- [Get started](#get-started)
- [Ask why](#ask-why)
- [Editor integration](#editor-integration)
- [Guarding agents: the gate](#guarding-agents-the-gate)
- [Blast radius of a diff](#blast-radius-of-a-diff)
- [Keeping the index fresh](#keeping-the-index-fresh)
- [Extending the effect catalogue](#extending-the-effect-catalogue)
- [How it works](#how-it-works)
- [Honest limits](#honest-limits)
- [Status & roadmap](#status--roadmap)

## Why seamark?

Code-graph tools converged on one design: parse symbols, store a graph,
save tokens. Three problems stay unsolved by all of them:

| Problem | What seamark does about it |
|---|---|
| The graph knows *what* the code is, not *why* | Mines git history: empirical co-change (with lift), the commit trail per region, revert markers |
| Agents repeat mistakes; the fix is a prompt paragraph that costs tokens every turn and gets ignored | Rules are **checks**, evaluated at edit/run time; they cost nothing until violated |
| Agents cause real damage and the guardrails are regex denylists | A real shell parser + effect classification + policy over your declared environment, with an append-only audit log |

Measured on a real production monorepo (831 files, Python + TypeScript +
Go, 447 commits): full index in **~3s**; the strongest signal found was a
hand-synchronized Python↔TypeScript schema contract (38 shared commits,
lift 6.6) — invisible to every language server by construction, caught by
seamark as a save-time diagnostic. 598 symbols were found to transitively
reach a sink (DB write, process spawn, network egress).

Everything in the index is **falsifiable**: every edge traces to a parse,
a commit, or a policy file. No LLM-generated "insights" are ever stored.
100% local. No account, no API key, no telemetry.

## Get started

Requires Go ≥ 1.25 and a C compiler (tree-sitter uses CGO). Prebuilt
binaries and package-manager installs are planned; today it builds from
source:

```bash
git clone https://github.com/seamark-dev/seamark && cd seamark
make install        # builds and installs to ~/.local/bin/seamark
```

Then, in any repository:

```bash
seamark init        # scaffold config + wire the Claude Code agent hooks
seamark index       # parse + mine history + propagate effects (~seconds)
seamark why <symbol-or-file>
```

`seamark init` is optional but does the one-time setup for you: it writes
starter `.seamark/policy.yaml` and `.seamark/lessons.yaml` (never
overwriting existing files), adds the `.gitignore` carve-outs, and merges
the gate and review-lessons hooks into `.claude/settings.json` — leaving
any hooks you already have intact, and safe to re-run. Pass `--print` to
preview every change first.

The index is a single SQLite file under `.seamark/` — delete it to start
fresh, commit nothing.

## Ask why

`seamark why` answers the onboarding questions grep cannot — here run
on seamark's own gate:

```text
$ seamark why gate.EvalCommand
internal/gate.EvalCommand  (function)
  defined  internal/gate/gate.go:136
  sig      func EvalCommand(p *Policy, catalog *effects.Catalog, root, commandLine string) (*Decision, error)
  effects  proc:exec [depth 1]

callers (3)
  internal/cli.newGateCmd                  internal/cli/gate.go:20         [qualified]
  internal/gate.evalCmd                    internal/gate/gate_test.go:30   [same-package]
  ...

calls (8)  — 3 resolved by name match only
  internal/effects.Catalog.MatchCommand    internal/effects/effects.go:140 [unique-name]
  internal/gate.gitPush                    internal/gate/gate.go:388       [same-package]
  ...

usually changed with  (empirical, lift > 1 means beyond chance)
recent decisions
  2026-07-26  ...  Implement Python parser
```

Read it top to bottom: this function *can ultimately spawn a process*
(one hop away — it calls the push detector, which shells out to git to
resolve `HEAD`), here is who calls it including tests, and here is the
commit trail.
Every call edge declares how it was derived (`[qualified]`,
`[same-package]`, `[same-class]`, or the low-confidence `[unique-name]`),
so you always know how much to trust an edge. On a repo with real
history, the `usually changed with` section is where the surprises live.

### Languages

| Language | Symbols & calls | Effect sinks | Notes |
|---|:-:|:-:|---|
| Go | ✓ | ✓ | multi-module monorepos supported |
| TypeScript / TSX / JavaScript | ✓ | ✓ | ES-module resolution, cross-file named imports |
| Python | ✓ | ✓ | relative imports, `__init__` packages, self/cls resolution |
| SQL, HCL | history layer only | planned | co-change needs no parser |

The history layer (co-change, decisions) is language-agnostic — it works
on every file git tracks.

## Learning from review history

Agents repeat mistakes. The same linter code, the same "use timezone-aware
datetimes," the same non-ASCII character keeps getting flagged in review
— by CodeRabbit, by Copilot, by a human — and the correction never sticks
past the session. `seamark index --reviews` mines your pull-request review
comments and clusters the recurrences into **lessons**:

```text
$ seamark why scripts/rollover.py
...
reviewers keep flagging  (recurring across pull requests)
  E702                     ×20  scripts [coderabbit]
  RUF001                   ×6   scripts [coderabbit]
  reportArgumentType       ×2   api/services [coderabbit]
```

A cited linter code clusters by directory (a habit, not one line); an
un-coded comment clusters by file and issue title. Only patterns that
recur (≥2) surface, region-scoped, so an agent editing `scripts/` sees
what reviewers keep catching there *before* it makes the mistake again —
through the same `why`/`orient` an agent already calls, no extra tokens
per turn and nothing bolted onto CLAUDE.md.

It's reviewer-agnostic (bots and humans travel the same path) and
best-effort: needs the GitHub CLI (`gh`) authenticated and a github.com
remote; without them the layer is simply absent. Lessons refresh on the
review cadence — `--reviews` is opt-in, and a normal `seamark index` (and
every agent tool call) leaves them untouched rather than re-hitting the
network. A failed mine (offline, logged out) fails safe: it keeps the
lessons already stored rather than clearing them.

### Aware without being told

Having the lessons in `why`/`orient` only helps if an agent *asks*. To
make it automatic, wire the edit hook — a `PreToolUse` hook that injects
the relevant lessons the moment an agent is about to edit a file:

```json
{ "hooks": { "PreToolUse": [ {
  "matcher": "Edit|Write|MultiEdit",
  "hooks": [ { "type": "command", "command": "seamark lessons --hook" } ]
} ] } }
```

Now an agent editing anything under `scripts/` is told "reviewers keep
flagging E702, RUF001 here" whether or not it thought to check — a single
local index read, no network, silent for files with no lessons. (Proven:
a headless agent given a plain edit task with no mention of seamark still
named every flagged rule.)

### Tuning what surfaces

Like the gate's policy, a committed `.seamark/lessons.yaml` controls what
shows — applied at surface time, so edits take effect with no re-mining:

```yaml
threshold: 2                     # min recurrences to surface (default 2)
mute:
  - rule: F541                   # hush a noisy rule everywhere
  - region: alembic/versions     # …or every lesson under generated code
pin:                             # your "must not be ignored" list —
  - rule: RUF001                 # surfaced always for its region, even if
    region: scripts              # mining found it once or never
    note: "Keep scripts ASCII — smart quotes have bitten us"
```

`mute` kills noise; `pin` is the escape hatch for a rule you care about
more than the mined frequency implies. Both flow through `why`, `orient`,
and the edit hook via one path, so they never disagree.

You don't have to author this by hand. `seamark lessons --list` prints
every mined lesson — including the one-off noise that `why`/`orient` hide
— with the exact rule and region values to paste, and flags anything your
config already mutes:

```text
$ seamark lessons --list
review lessons (all mined, strongest first) — 436 total

  ×20   E702                 scripts        [coderabbit]
  ×3    F541                 scripts        [coderabbit]  (muted)
  ×1    RUF002               fetcher.py     [coderabbit]
  …
```

### Is it working? `lessons --stats`

The edit hook appends a line to `.seamark/lessons-audit.jsonl` each time
it reminds an agent — the impact/decay counterpart to the gate's audit
log. `seamark lessons --stats` turns that into which lessons actually
reach agents, and which *would* surface but never have (a lesson whose
region no edit touches is a pruning candidate):

```text
$ seamark lessons --stats
lesson firings — 128 edits reminded across 24 files

most surfaced
  ×41  E702                 scripts          last 2026-07-26
  ×18  RUF001               scripts          last 2026-07-26
  …
never fired — 7 lessons in regions no edit has touched (decay candidates)
  E741                 tests
  …
```

## Editor integration

`seamark lsp` is a secondary language server that runs *alongside* gopls,
pyright, or tsserver — it adds the layer they cannot see:

- **Hover** — signature, caller count with confidence, effect reach
  ("Reaches `db:write` (2 hops)"), co-change partners, recent commits.
- **Code lenses** — caller counts per function; a file-level coupling
  headline.
- **Diagnostics on save** — the co-change omission check: *"usually
  changed together, not in this change: src/api/schema.ts (38/58
  commits, lift 6.6)"*. Conservative thresholds by design; a diagnostic
  that nags gets disabled.

Setup for Neovim (0.9+) and VS Code: see [docs/editors.md](docs/editors.md)
and the ready-made configs in [editors/](editors/).

## Agent integration: the MCP server

`seamark mcp` speaks the Model Context Protocol over stdio. Five tools,
not forty — tool definitions are replayed to the model on every turn, so
a sprawling "token-saving" server defeats itself:

| Tool | Answers |
|---|---|
| `orient` | one-screen repo overview: modules, most-called API, change hubs, recent decisions |
| `why` | everything about a symbol or file: callers with confidence, co-change, commits |
| `change_set` | pre-edit: what history says changes together with your planned files |
| `check` | a diff's reachable effects and the policy verdict |
| `expand` | progressive disclosure: turn any ref a report returned into source lines |

Register it in Claude Code by dropping an `.mcp.json` at the repo root
(this repository ships one):

```json
{"mcpServers": {"seamark": {"command": "seamark", "args": ["mcp"]}}}
```

Every tool call re-checks the workspace fingerprint first and re-indexes
if anything changed, so answers never come from a stale graph. The
server also exposes the orientation as an MCP resource and an `onboard`
prompt that walks an agent through the repo cheapest-signal-first.

A typical agent exchange, on this repository's own history:
`change_set(["internal/gate/gate.go"])` answers with the files that
historically ship alongside gate changes and the effect tags the edit
can reach — the "what am I about to forget?" question, answered from
evidence before the first line is written.

## Guarding agents: the gate

`seamark gate` classifies a shell command's effects and evaluates your
policy **before the command runs** — built for agent pre-execution hooks
and CI:

```bash
$ KUBECONFIG=~/.kube/prod.yaml seamark gate --command "terraform apply -auto-approve"
verdict  deny (mode: warn)
effects  [infra:mutate]
  [deny] no-prod-infra-mutation: infrastructure mutation against a production environment requires a human
```

What makes it more than a denylist:

- **A real shell parser** (`mvdan.cc/sh`) — every command in pipelines,
  `&&`-chains, and `$(substitutions)` is classified; unparseable input
  fails closed.
- **Indirection is detected, not missed** — `$TOOL apply` cannot be
  classified, so it is flagged as *dynamic* and policy decides; this is
  exactly the trick that walks through regex denylists.
- **Wrappers unwrap** — `sudo -E env FOO=bar kubectl delete` classifies
  as kubectl.
- **Subcommand-aware** — `terraform plan` passes, `terraform apply` does
  not; an argument merely *named* "apply" does not trigger.
- **Environment-aware** — `env.is_prod` derives from the variables you
  declare (`KUBECONFIG`, `DATABASE_URL`, …) and your prod markers.

Policy is CEL over `effect`, `command`, `env`, and `diff`, in
`.seamark/policy.yaml`:

```yaml
mode: warn          # report only; flip to "enforce" when the rules have earned trust
deny:
  - id: no-prod-infra-mutation
    when: 'effect.contains("infra:mutate") && env.is_prod'
    message: infrastructure mutation against production requires a human
require_approval:
  - id: prod-db-write
    when: 'effect.contains("db:write") && env.is_prod'
    message: database write against a production environment
```

Ships in **warn mode**: run it for a week, read the audit log, see what
*would* have been blocked, then opt into `enforce` (exit code 2 blocks
the caller — hook- and CI-friendly).

### As a Claude Code hook

`.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [{
      "matcher": "Bash",
      "hooks": [{
        "type": "command",
        "command": "seamark gate --enforce --hook"
      }]
    }]
  }
}
```

`--hook` reads the PreToolUse JSON from stdin natively — no jq, and the
gate **fails closed** under `--enforce`: a malformed payload, a broken
policy file, or an internal error blocks the command instead of silently
allowing it.

The agent sees the denial reason and corrects course; you see every
decision in `.seamark/audit.jsonl` — an append-only trail of what your
agents attempted, when, and why it was allowed or blocked.

## Blast radius of a diff

```bash
git diff | seamark check          # or: seamark check   (uses git diff HEAD)
```

Changed lines map to symbols; symbols carry transitively-propagated
effect tags; the union is what the change can *ultimately* reach — even
when the edited function never touches a sink itself. Policy rules over
`diff.effects` gate merges the same way `gate` gates commands.

## Keeping the index fresh

Seamark fingerprints the workspace (HEAD + `git status`, hashed) every
time the index is built, so every surface knows whether its answers are
current — and reacts according to what a stale answer would cost:

| Surface | Freshness behavior |
|---|---|
| Editor (LSP) | Automatic: every save reindexes and republishes diagnostics; no-change saves are free |
| `seamark check` | **Self-repairs**: missing or stale index is rebuilt before evaluating — a blast radius from stale line spans would be a wrong safety answer |
| `seamark gate` | Never needs the index (catalogue + policy only); always current |
| `seamark why` | Answers immediately, with a stderr note when the workspace changed since the last index |
| `seamark index` | No-ops in well under a second when nothing changed (`--force` rebuilds) — running it "just in case" is free |

A full rebuild is fast enough to be the current strategy: ~3.5s for an
831-file, three-language monorepo; ~150ms for a small repo. The
fingerprint check itself is ~30ms.

### Planned: incremental indexing

Measured breakdown of that 3.5s: history mining 0.6s, parse + graph +
SQLite ~2.9s. The roadmap attacks it in order of value
(see [docs/PLAN.md](docs/PLAN.md)):

1. **Incremental inputs, exact outputs** — a content-hashed per-file
   parse cache (re-parse only changed files) and a git-log watermark
   (mine only commits since the last run). Resolution and effect
   propagation stay full-fidelity: they are the cheap part, and global
   exactness is what makes edges trustworthy. Expected result: a
   one-file change reindexes in well under a second.
2. **`seamarkd` daemon** — fsnotify watching with debounced incremental
   refresh and delta writes; the LSP becomes a thin client and freshness
   becomes keystroke-adjacent instead of save-adjacent.
3. **Incremental resolution** — dependency-tracked edge invalidation,
   deliberately deferred until a very large monorepo demands it: the
   lowest-confidence resolution tier is nonlocal (a new method named `X`
   anywhere can invalidate an edge elsewhere), and cheap exactness beats
   clever approximation until the numbers say otherwise.

## Extending the effect catalogue

Effect knowledge is data, not code. The built-in catalogue covers ~40
sinks across Go, Python, and JS/TS plus common CLI tools; your workspace
extends it additively in `.seamark/effects.yaml`:

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

## How it works

```text
  sources                     seamark                      surfaces
┌──────────┐      ┌───────────────────────────┐      ┌──────────────┐
│ source   │─parse─▶ symbols / calls / imports │────▶ │ LSP  (stdio) │
│ tree     │      │                           │      ├──────────────┤
├──────────┤      │  co-change (lift) +       │────▶ │ CLI / hooks  │
│ git log  │─mine──▶ decisions per region      │      ├──────────────┤
├──────────┤      │                           │────▶ │ MCP (planned)│
│ .seamark/ │─load──▶ effect propagation to     │      └──────────────┘
│ yaml     │      │  fixpoint + CEL policy    │
└──────────┘      └────────── SQLite ─────────┘
```

One binary, one SQLite file, no daemon required. Parsing is tree-sitter;
call edges are resolved syntactically and **labeled with their
derivation** so consumers can filter by confidence. Effects seed from the
catalogue at call sites (including calls into external dependencies) and
propagate backwards along call edges to fixpoint, with depth.

## Honest limits

- Call resolution is syntactic, not type-checked. gopls will always be
  better at *find references* within one language — that is not the
  game. Low-confidence edges are labeled `[unique-name]` and test
  doubles are excluded from that tier.
- A local variable shadowing an import alias can produce a wrong edge
  (declared as such via its origin label). Scope tracking is future work.
- Python's DB-API has no syntactic read/write split, so `cursor.execute`
  tags conservatively as `db:write`.
- Co-change needs history: on a young repo the empirical layer is thin
  until commits accumulate.
- Seamark is a navigator, not an oracle. It tells you where to look and
  what usually travels together; "usually changes with" is empirical
  evidence, never a guarantee that *your* edit is safe or complete. For
  a pinpoint lookup of a known symbol, a plain file read is cheaper —
  seamark earns its round-trip on orientation, risk, and history
  questions.

## Status & roadmap

Working today: indexer (Go/TS/JS/Python), history mining, effects +
propagation, LSP server, gate + check + audit, Claude Code hook, MCP
server (`orient`, `change_set`, `why`, `check`, `expand` + resource +
prompt), review-comment lessons (`index --reviews`). Planned next (see
[docs/PLAN.md](docs/PLAN.md)): zero-token check promotion from recurring
lessons, function-grain history enrichment, `seamarkd` daemon with
incremental indexing, prebuilt binaries + npm/Homebrew distribution.

## Development

```bash
make test     # full suite (testify)
make lint     # golangci-lint
make index    # self-index this repo
```

Contributions that need no Go at all: the effect catalogue and default
policy are plain YAML — adding sinks for your framework is a
ten-line PR.

## License

Apache-2.0. See [LICENSE](LICENSE).
