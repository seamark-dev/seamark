# Repository history and code analysis

Use Seamark to understand past decisions, find files that often change
together, and explore the possible side effects of a change. These queries
give an AI coding agent evidence it cannot get from reading source alone.

## Explore a repository

```bash
seamark index       # ~seconds; parse + mine history + propagate effects
seamark orient      # the one-screen overview
```

`orient` shows scale, module layout, the most-called production API,
the change hubs (files whose edits rarely travel alone), and the recent
decision trail. Then interrogate anything that looks load-bearing:

Example output (counts and paths depend on the indexed checkout):

```text
$ seamark why gate.EvalCommand
internal/gate.EvalCommand  (function)
  defined  internal/gate/gate.go:136
  sig      func EvalCommand(p *Policy, catalog *effects.Catalog, root, commandLine string) (*Decision, error)
  effects  proc:exec [depth 1]

callers (4)
  [qualified]     internal/cli.newGateCmd                      internal/cli/gate.go:21
  (+3 in tests)

calls (8)  — 3 resolved by name match only
  [unique-name]   internal/effects.Catalog.MatchCommand        internal/effects/effects.go:140
  [same-package]  internal/gate.gitPush                        internal/gate/gate.go:388
  ...

usually changed with  (empirical, lift > 1 means beyond chance)
   6/58  commits  lift 4.1   internal/effects/effects.go  · mostly MatchCommand, Load
recent decisions
  2026-07-26  ...  Implement Python parser
```

Read it top to bottom: this function _can ultimately spawn a process_
(one hop away), here is its production surface (test callers collapse to
a count instead of burying it), and here is the commit trail. Every call
edge declares how it was derived (`[qualified]`, `[same-package]`,
`[same-class]`, or the low-confidence `[unique-name]`), so you always
know how much to trust an edge. The `usually changed with` lines carry
**function grain**: `· mostly …` names the functions git's hunk headers
show actually moved in the shared commits — a factual report, not a
statistical claim. On a repo with real history, this section is where
the surprises live.

### Languages

| Language                      |  Symbols & calls   | Effect sinks | Notes                                                      |
| ----------------------------- | :----------------: | :----------: | ---------------------------------------------------------- |
| Go                            |         ✓          |      ✓       | multi-module monorepos supported                           |
| TypeScript / TSX / JavaScript |         ✓          |      ✓       | ES-module resolution, cross-file named imports             |
| Python                        |         ✓          |      ✓       | relative imports, `__init__` packages, self/cls resolution |
| SQL, HCL                      | history layer only |   planned    | co-change needs no parser                                  |

The history layer (co-change, decisions) is language-agnostic — it works
on every file git tracks.

## How much can the answers be trusted?

```bash
seamark status          # or --json; also served as MCP resource seamark://status
```

Example status output:

```text
workspace      current (schema v3)
parsed         703 of 832 seen files (98 skipped by config)
symbols        1221, 2931 edges — call resolution 71% qualified · 24% same-package · 5% unique-name (1796 calls)
effects        83 direct-sink symbols, 692 by propagation
history        3814 decisions; evidence median age 74d (oldest 1042d)
reviews        3 lessons from 120 review findings; last mined 9d ago
distillation   claude -p (invoker claude) — external data processing when run (see `lessons --distill --dry-run`)
gate           hook installed (claude); policy mode warn governs
skills         claude 3/3 current · codex not installed
approvals      claude 8/8 rules · codex not registered
clients        claude  gate (warn) + lessons hooks installed; registered in .mcp.json as "seamark"
               claude  skills, mcp-registration, tool-grants, edits, commands, resets: not natively verified
               codex   no hooks installed; MCP registration not registered
```

The `clients` block is the per-agent view: which hooks and which MCP
registration each agent has, the native evidence behind them (a
recorded check on a named version and surface, a pending check, or
none), and every limitation the adapter reports, such as Codex hook
trust that seamark cannot read, or reminders that repeat because the
adapter identifies no receiving context. `--json` carries the same
facts as a `clients` array with typed, named states. The `gate` line
covers every agent with a gate hook and names the installed hook mode
apart from the policy mode, because a warn hook still follows an
enforcing policy file. A wrapped gate hook that certainly runs and
whose exit status the wrapper discards (`… || true`) reads
`report-only`: no policy mode makes it block.

Every safety-sensitive answer needs this context: **"no effects found"
from a half-parsed index is not "no effects."** The same honesty runs
through the other surfaces — `orient` warns when files failed to parse,
and `seamark check` attaches a note when changed files have no indexed
symbols, so absence of evidence never silently reads as evidence of
safety.

Installation health is the other half:

```bash
seamark doctor          # read-only, offline; exit 1 when a check fails
```

`doctor` verifies everything seamark needs to run — git, the index
database (schema version and SQLite integrity), policy and
effect-catalogue compilation, the hook wiring of every agent, the
distillation agent, `gh`, MCP registration, the agent skills, the tool
approvals, and that the policy-as-code overlays are not accidentally
gitignored — and prints an exact corrective action for anything broken,
changing nothing itself. `init`, `doctor`, and `status` read one
inspection per agent, so they never disagree about what is set up; a
line named after an agent carries its native evidence and limitations,
and its corrective command comes from the same adapter that installs
the artifact.

## How it works

```text
  sources                     seamark                      surfaces
┌──────────┐      ┌───────────────────────────┐      ┌──────────────┐
│ source   │─parse─▶ symbols / calls / imports │────▶ │ LSP  (stdio) │
│ tree     │      │                           │      ├──────────────┤
├──────────┤      │  co-change (lift) +       │────▶ │ CLI / hooks  │
│ git log  │─mine──▶ decisions per region      │      ├──────────────┤
├──────────┤      │                           │────▶ │ MCP  (stdio) │
│ .seamark/ │─load──▶ effect propagation to     │      └──────────────┘
│ yaml     │      │  fixpoint + CEL policy    │
└──────────┘      └────────── SQLite ─────────┘
```

One binary, one SQLite file, no daemon required. Parsing is tree-sitter;
call edges are resolved syntactically and **labeled with their
derivation** so consumers can filter by confidence. Effects seed from the
catalogue at call sites (including calls into external dependencies) and
propagate backwards along call edges to fixpoint, with depth. What
touches the network, what reaches a model, and what persists is one
table in [Data flow](data-flow.md).

## Honest limits

- Call resolution is syntactic, not type-checked. gopls will always be
  better at _find references_ within one language — that is not the
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
  evidence, never a guarantee that _your_ edit is safe or complete. For
  a pinpoint lookup of a known symbol, a plain file read is cheaper —
  seamark earns its round-trip on orientation, risk, and history
  questions.

The LSP surface is [experimental](editors.md); AI coding agent workflows
through MCP, skills, and lesson hooks are the current focus.
