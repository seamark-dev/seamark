<p align="center">
  <img src="assets/seamark-banner-2560.png" alt="Seamark — the safe channel and the hazard" width="720">
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="License"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/go-%E2%89%A51.25-00ADD8.svg" alt="Go"></a>
  <a href="#get-started"><img src="https://img.shields.io/badge/platform-macOS%20%7C%20Linux-lightgrey.svg" alt="Platform"></a>
</p>

**The graph your repo's history knows and your code's blast radius — served to your editor, your agents, and your CI.**

Seamark indexes which files _really_ change together, why the weird parts
are weird, and which code paths can reach a database write, a process
spawn, or your production infrastructure. It serves that as editor
diagnostics, answers `why` on the CLI, and enforces your rules as
machine checks on agent commands — not as paragraphs in a prompt.
**Mistakes get caught instead of explained.**

> A _seamark_ is a navigational marker that shows both the safe channel
> and the hazard.

- [Why seamark?](#why-seamark)
- [Get started](#get-started)
- [The mental model](#the-mental-model)
- [Journey 1: understand an unfamiliar repository](#journey-1-understand-an-unfamiliar-repository)
- [Journey 2: stop repeating review mistakes](#journey-2-stop-repeating-review-mistakes)
- [Journey 3: guard agent commands](#journey-3-guard-agent-commands)
- [Surfaces: editor, agents, humans](#surfaces-editor-agents-humans)
- [How much can the answers be trusted?](#how-much-can-the-answers-be-trusted)
- [Durable state](#durable-state-the-index-is-not-a-throwaway-cache)
- [Configuration](#configuration)
- [How it works](#how-it-works)
- [Honest limits](#honest-limits)
- [Status & roadmap](#status--roadmap)

## Why seamark?

Code-graph tools converged on one design: parse symbols, store a graph,
save tokens. Three problems stay unsolved by all of them:

| Problem                                                                                             | What seamark does about it                                                                                         |
| --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| The graph knows _what_ the code is, not _why_                                                       | Mines git history: empirical co-change (with lift), the commit trail per region, revert markers                    |
| Agents repeat mistakes; the fix is a prompt paragraph that costs tokens every turn and gets ignored | Rules are **checks**, evaluated at edit/run time; they cost nothing until violated                                 |
| Agents cause real damage and the guardrails are regex denylists                                     | A real shell parser + effect classification + policy over your declared environment, with an append-only audit log |

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

Tagged [releases](https://github.com/seamark-dev/seamark/releases) attach
smoke-tested archives for macOS and Linux (amd64/arm64) with SHA-256
checksums:

```bash
sha256sum -c --ignore-missing SHA256SUMS   # macOS: shasum -a 256 -c --ignore-missing SHA256SUMS
tar -xzf seamark_*.tar.gz
mkdir -p ~/.local/bin
install seamark_*/seamark ~/.local/bin/
```

A matching checksum verifies the archive against the published
`SHA256SUMS` — integrity, not publisher identity; artifact signing is
not yet available ([docs/STATUS.md](docs/STATUS.md)).

Or build from source — Go ≥ 1.25 and a C compiler (tree-sitter uses
CGO):

```bash
git clone https://github.com/seamark-dev/seamark && cd seamark
make install        # builds and installs to ~/.local/bin/seamark
```

Both routes install to `~/.local/bin` — make sure it is on your `PATH`.

Then, in any repository:

```bash
seamark init        # scaffold config + wire the Claude Code agent hooks
seamark index       # parse + mine history + propagate effects (~seconds)
seamark why <symbol-or-file>
```

`seamark init` is optional but does the one-time setup for you: it writes
starter `.seamark/policy.yaml`, `.seamark/lessons.yaml` and
`.seamark/config.yaml` (never overwriting existing files), adds the
`.gitignore` carve-outs, and merges
the gate and review-lessons hooks into `.claude/settings.json` — leaving
any hooks you already have intact, and safe to re-run. Pass `--print` to
preview every change first. A fresh install on the starter warn policy
**never blocks anything** (an existing `enforce` policy is kept, and
keeps enforcing); turning enforcement on is a separate, explicit opt-in
([Journey 3](#journey-3-guard-agent-commands)).

The graph and your proposal decisions live in one SQLite database under
`.seamark/`, beside the audit logs and generated reports — all
gitignored except the reviewed YAML overlays. The database is _not_ a
throwaway cache once you start deciding on proposals — see
[Durable state](#durable-state-the-index-is-not-a-throwaway-cache).

## The mental model

Five nouns cover the learning pipeline, used with exactly these meanings
everywhere — CLI help, docs, and report alike:

```text
review comment or fix commit         what a reviewer (or a fix) said, once
        ↓  mined by `seamark index --reviews`
finding      one raw observation, kept verbatim with its provenance
        ↓  clustered on recurrence (≥2)
lesson       a pattern recurring across findings in a region — review-
             or fix-derived alike
        ↓  distilled by your agent CLI (optional) — or written by hand
proposal     a candidate rule awaiting YOUR decision
        ↓  `lessons --apply`  (a dismissal sticks until its evidence changes)
pin          an accepted rule, surfaced to agents at edit time
```

And five more for the risk layer:

| Term         | Meaning                                                                                            |
| ------------ | -------------------------------------------------------------------------------------------------- |
| **effect**   | an observable capability — `db:write`, `proc:exec`, `net:egress`, `infra:mutate`                   |
| **sink**     | an API or command that _directly_ produces an effect; everything else reaches effects transitively |
| **decision** | one historical record: a commit, PR, or revert mined from git                                      |
| **region**   | a file-or-directory scope that lessons, pins, and policy attach to                                 |
| **coupling** | empirical co-change measured from history — "usually travels with", never "depends on"             |

## Journey 1: understand an unfamiliar repository

_Five minutes from clone to knowing where the bodies are buried._

```bash
seamark index       # ~seconds; parse + mine history + propagate effects
seamark orient      # the one-screen overview
```

`orient` shows scale, module layout, the most-called production API,
the change hubs (files whose edits rarely travel alone), and the recent
decision trail. Then interrogate anything that looks load-bearing:

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

## Journey 2: stop repeating review mistakes

_Ten minutes to make the last hundred code reviews teach your agents._

Agents repeat mistakes. The correction lives in review threads — said
once by CodeRabbit, once by Copilot, once by a tired human — and never
sticks past the session. Mine it instead:

```bash
seamark index --reviews    # review comments via your gh CLI + fix commits from local git
```

Recurring feedback surfaces region-scoped through the same `seamark why`
/ `seamark orient` an agent already calls — no extra tokens per turn,
nothing bolted onto CLAUDE.md. From a private monorepo:

```text
$ seamark why scripts/fetcher.py
...
reviewers keep flagging  (recurring across pull requests)
  ×2     scripts     [copilot]     this script hard-codes a postgres url including credentials
  ×2     scripts     [coderabbit]  RUF003
```

Lint codes are the shallow end. The one-off comments the recurrence
threshold hides are often the most valuable, and the ledger keeps them
for the distiller and for you:

```text
$ seamark lessons --list
  ×1   core/session_calendar.py      [coderabbit]  loader is stricter on the go side than here — drift risk
  ×1   ingestor/cmd/ingestor/run.go  [coderabbit]  do not invalidate the live archive before its replacement succeeds
  …
```

A Python↔Go session calendar quietly drifting apart, an archive
invalidated before its replacement exists — each said exactly once,
each a production incident wearing a review comment. `seamark lessons
--distill` is how they become permanent: it batches the raw findings
through **your own agent CLI** (a full disclosure prints first;
`--dry-run` prints it and sends nothing — the exact payload is
documented in [docs/data-flow.md](docs/data-flow.md)) into proposed
pins — every proposal cites its evidence, nothing lands without your
explicit `--apply`, and a dismissal sticks until its evidence changes.

That drift-risk one-off, distilled together with a "regenerate the
frontend schema after this change" comment from `api/schemas.py`, is now
this applied pin in the monorepo's `lessons.yaml` — two review
remarks that became the repo's contract-synchronization rule:

```yaml
- rule: keep-parallel-implementations-consistent
  region: api
  regions: [api, core]
  note: "When two runtimes or call sites share a contract, mirror validation
    strictness and regenerate derived artifacts: run make sync-api after
    schemas.py changes, and keep the Go/Python seed loaders equally
    fail-fast."
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p60)
```

The region set is computed from the cited evidence, never guessed: a
theme living in `api` AND `core` says exactly that instead of claiming
the whole repo, so the pin only spends injection budget where its
evidence points. (Measured on the two development corpora, evidence-
coverage regions cut repo-wide `*` pins from 35 of 65 to 3.)

More pins the two repos distilled and applied:

- **recompute-derived-fields-with-source** — _"When a write changes
  inputs to derived columns (net_pnl, dedup_hash, avg price), recompute
  and persist them in the same transaction as the source update; never
  leave derived state stale or split across commits."_
- **no-parquet-for-live-session** — _"Never read the historical 1m
  parquet archive for today's/live session data — it lacks live-session
  bars and mixes historical with live."_
- **unsubscribe-on-every-exit-path** (`v2/pkg/engine/resolve`) —
  _"Every subscription termination path must also unsubscribe, not just
  close the writer; closing alone leaves the sub registered in
  triggers, skewing counters and leaking resources."_
- **copy-shared-mutable-data** (`v2/pkg/engine`) — _"Clone
  caller-supplied maps/headers and copy byte slices from pooled or
  datasource-owned buffers before storing or mutating them; aliasing
  reusable backing arrays causes cross-request races and corruption."_

None of that is lint. Those are the house rules a senior reviewer
carries in their head — extracted from this repo's own history, scoped
to the region they came from, and injected exactly when an agent is
about to edit there. That injection is the edit hook `seamark init`
wires: one local index read per edit, silent for files with no lessons.
(Proven: a headless agent given a plain edit task with no mention of
seamark still named every flagged rule.)

Every distilled pin also carries a live **confidence tier** — strong /
fair / weak, computed on each read from what its citations still
support: distinct events, review+fix corroboration, recency, and
whether the cited files still exist. Weak pins lose injection-budget
slots to strong ones and are tagged when they do surface; nothing is
stored, nothing is model-scored.

The whole lifecycle is `seamark lessons`, one flag per decision:

| Flag              | What it does                                                                                                                                                  |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--file <path>`   | the lessons that would fire when editing one file — the hook's view, uncapped                                                                                 |
| `--list` / `--region <dir>` | the raw ledger, one-offs included, with copy-paste config syntax                                                                                    |
| `--distill`       | batch new findings through your agent CLI into proposed pins; already-distilled evidence is never paid for twice (`--region`, `--limit`, `--dry-run` budget it; a preflight always discloses first) |
| `--proposals`     | the decision ledger, free: pending/applied/dismissed, each with its confidence facts, its prompt-era note, its outcome verdict (applied pins), and the regions today's inference would assign |
| `--apply p3,p7`   | pin chosen proposals (ranges work: `p1..p9`); writes `lessons.yaml` only with `distill.write`, else prints the block to paste                                 |
| `--dismiss p2`    | record a no — the same evidence is never re-proposed                                                                                                          |
| `--prune p16,p45` | retire pins that restate another (the ledger names the clusters); the theme stays pinned by its survivor                                                      |
| `--retarget p3`   | update an applied pin to the regions its living evidence supports now — failures roll `lessons.yaml` back, and re-running always converges                    |
| `--stats`         | the firing log: which lessons actually reach agents (split by surface: hook / change_set / check), which never fire — the decay signal — and per-pin outcomes: did the mistake recur after the pin started firing (working / not landing / untested) |
| `--hook`          | the PreToolUse entry point `seamark init` wires; offline, silent when a file has no lessons                                                                   |

Mined text is scrubbed of secret-shaped values (connection strings,
tokens) before it is stored — a credential a reviewer quoted once must
not be re-broadcast into agent context on every edit — and review
evidence has a shelf life: two years by default, with the newest 200
comments always kept so slow repositories stay covered.

A committed `.seamark/lessons.yaml` tunes what surfaces — `mute` kills
noise, `pin` forces what must never be ignored (single `region` or a
`regions: [api, db]` set), `threshold` sets the recurrence bar,
`pin_budget` caps the hook's injection (default 3), `change_budget` the
`change_set` block (default 6) — and `seamark report` renders the whole
decision queue as one self-contained HTML page. The full pipeline —
mining heuristics, fix-commit classification, region inference,
confidence tiers, distillation economics, near-duplicate pruning, the
firing stats, and the outcome loop that answers whether pins actually
change behavior — is documented in [docs/lessons.md](docs/lessons.md).

## Journey 3: guard agent commands

_Run warn-mode for a week; then decide if anything should actually block._

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
  classified, so it is flagged as _dynamic_ and policy decides; this is
  exactly the trick that walks through regex denylists.
- **Wrappers unwrap** — `sudo -E env FOO=bar kubectl delete` classifies
  as kubectl.
- **Subcommand-aware** — `terraform plan` passes, `terraform apply` does
  not; an argument merely _named_ "apply" does not trigger.
- **Environment-aware** — `env.is_prod` derives from the variables you
  declare (`KUBECONFIG`, `DATABASE_URL`, …) and your prod markers.

Policy is CEL over `effect`, `command`, `env`, and `diff`, in
`.seamark/policy.yaml`:

```yaml
mode: warn # report only; flip to "enforce" when the rules have earned trust
deny:
  - id: no-prod-infra-mutation
    when: 'effect.contains("infra:mutate") && env.is_prod'
    message: infrastructure mutation against production requires a human
require_approval:
  - id: prod-db-write
    when: 'effect.contains("db:write") && env.is_prod'
    message: database write against a production environment
```

`seamark init` wires the gate into `.claude/settings.json` as a
PreToolUse hook on Bash (`seamark gate --hook` — the payload is read
natively, no jq). By default the hook follows `policy.yaml`'s mode
(warn), so **a first install never blocks a command**. Opting in with
`seamark init --gate-mode enforce` bakes `--enforce` into the hook:
deny/require_approval verdicts exit 2 and block, and the gate **fails
closed** — a malformed payload, a broken policy file, or an internal
error blocks the command instead of silently allowing it. Re-running
`init` without `--gate-mode` keeps whatever mode is installed, and every
run ends with a `gate` line stating the effective behavior.

The agent sees the denial reason and corrects course; you see every
decision in `.seamark/audit.jsonl` — an append-only trail of what your
agents attempted, when, and why it was allowed or blocked. The log is
secret-safe by default: entries store the normalized command names, a
SHA-256 of the input, the verdict, and the policy hash — never the raw
command line, which frequently carries tokens, passwords, and connection
strings. It is created `0600` and rotated by size and age (one previous
generation kept; entries expire after 30 days). To also persist the
input line, opt in via `policy.yaml`:

```yaml
audit:
  raw: true
```

Raw inputs are scrubbed of secret-shaped patterns best-effort — treat
such a log as sensitive.

### Blast radius of a diff

```bash
git diff | seamark check          # or: seamark check   (uses git diff HEAD)
```

Changed lines map to symbols; symbols carry transitively-propagated
effect tags; the union is what the change can _ultimately_ reach — even
when the edited function never touches a sink itself. Policy rules over
`diff.effects` gate merges the same way `gate` gates commands, and
`diff.unindexed_files` exposes coverage blind spots to your rules —
changed files the index cannot attribute never silently read as safe.
The text output also appends the recurring lessons governing the
touched files — clearly marked advisory, never part of the verdict,
printed even when the verdict blocks (a deny is exactly when they
matter). `--json` stays verdict-shaped; `--enforce` makes blocking
verdicts exit 2.

What Guard can and cannot defend against is stated plainly in
[docs/threat-model.md](docs/threat-model.md): it is a defense-in-depth
policy layer, not a sandbox.

## Surfaces: editor, agents, humans

**Editor** — `seamark lsp` is a secondary language server that runs
_alongside_ gopls, pyright, or tsserver, adding the layer they cannot
see: hover with effect reach and co-change partners, caller-count code
lenses, and the save-time omission diagnostic (_"usually changed
together, not in this change: src/api/schema.ts — 38/58 commits, lift
6.6"_). Conservative thresholds by design; a diagnostic that nags gets
disabled. Setup: [docs/editors.md](docs/editors.md) and the ready-made
configs in [editors/](editors/).

**Agents** — `seamark mcp` speaks the Model Context Protocol over
stdio. Five tools, not forty — tool definitions are replayed to the
model on every turn, so a sprawling "token-saving" server defeats
itself:

| Tool         | Answers                                                                                                                                       |
| ------------ | --------------------------------------------------------------------------------------------------------------------------------------------- |
| `orient`     | one-screen repo overview: modules, most-called API, change hubs, recent decisions                                                             |
| `why`        | everything about a symbol or file: callers with confidence, co-change, commits, the lessons that govern it                                    |
| `change_set` | pre-edit: what history says changes together with your planned files, plus the budgeted lessons governing them (new files included)           |
| `check`      | a diff's reachable effects and the policy verdict, with the touched files' lessons as a marked advisory                                       |
| `expand`     | progressive disclosure: turn any ref a report returned into its content — source lines, or `lessons:<dir>` for an area's raw review findings  |

Register it in Claude Code by dropping an `.mcp.json` at the repo root
(this repository ships one):

```json
{ "mcpServers": { "seamark": { "command": "seamark", "args": ["mcp"] } } }
```

Every tool call re-checks the workspace fingerprint first and re-indexes
if anything changed, so answers never come from a stale graph. The
server also exposes `seamark://orient` and `seamark://status` as
resources and an `onboard` prompt that walks an agent through the repo
cheapest-signal-first.

**Humans** — `seamark report` renders everything the learning layer has
concluded as one self-contained HTML page (decision queue,
near-duplicate pins, hotspot map, full lesson ledger) — `--open` opens
it, `-o -` streams it to stdout; see [docs/lessons.md](docs/lessons.md).

Freshness is handled per surface, priced by what a stale answer would
cost: `check` **self-repairs** before evaluating (a blast radius from
stale line spans would be a wrong safety answer), the LSP reindexes on
save, MCP re-checks on every call, `why` answers immediately with a
staleness note, and `seamark index` no-ops in well under a second when
nothing changed — running it "just in case" is free. A content-hashed
per-file parse cache keeps reindexing at ~1.3s on an 831-file monorepo
(full parse ~3.2s, byte-identical results; resolution and propagation
always run globally so edges stay exact).

## How much can the answers be trusted?

```bash
seamark status          # or --json; also served as MCP resource seamark://status
```

```text
workspace      current (schema v3)
parsed         703 of 832 seen files (98 skipped by config)
symbols        1221, 2931 edges — call resolution 71% qualified · 24% same-package · 5% unique-name (1796 calls)
effects        83 direct-sink symbols, 692 by propagation
history        3814 decisions; evidence median age 74d (oldest 1042d)
reviews        3 lessons from 120 review findings; last mined 9d ago
distillation   claude -p — external data processing when run (see `lessons --distill --dry-run`)
gate           hook installed; policy mode warn governs
```

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
effect-catalogue compilation, Claude Code hook wiring, the distillation
agent, `gh`, MCP registration, and that the policy-as-code overlays are
not accidentally gitignored — and prints an exact corrective action for
anything broken, changing nothing itself.

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
  # argv: ["my-llm", "--stdin"]   # …or a custom command line
```

History mining has two flags on `seamark index` itself: `--max-commits`
bounds the git window (default 5000) and `--max-files-per-commit`
excludes bulk refactors from co-change (default 30). Every command
takes `-C <dir>` to name the workspace and `--db <path>` to override
the index location; `status`, `doctor`, `gate`, and `check` speak
`--json` for machines.

**Effect catalogue** — effect knowledge is data, not code. The built-in
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
table in [docs/data-flow.md](docs/data-flow.md).

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

## Status & roadmap

The current production status per capability profile — Navigate (stable),
Learn (functional, integration-dependent), Guard (warn ready, enforcement
beta) — lives in [docs/STATUS.md](docs/STATUS.md), kept separate from the
design history in [docs/PLAN.md](docs/PLAN.md). Trust boundaries:
[docs/data-flow.md](docs/data-flow.md) and
[docs/threat-model.md](docs/threat-model.md).

Planned next (see [docs/PLAN.md](docs/PLAN.md)): zero-token check
promotion from recurring lessons, function-grain precision refinements,
public signal evaluation and multi-instance lessons evidence, history
watermark + incremental daemon for keystroke-adjacent freshness, and signed
artifacts + npm/Homebrew packaging.

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
