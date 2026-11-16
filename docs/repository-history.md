# Repository history and code analysis

Use Seamark to understand past decisions, find files that often change
together, and explore the possible side effects of a change. These queries
give an AI coding agent evidence it cannot get from reading source alone.

## Explore a repository

```bash
seamark index       # ~seconds; parse + mine history + propagate effects
seamark orient      # the one-screen overview
```

`orient` shows scale, an index health header (index time and age,
freshness, file coverage, and call-edge origin shares), module layout,
the most-called production API, the change hubs (files whose edits
rarely travel alone), and the recent decision trail. Then interrogate
anything that looks load-bearing:

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
  [same-package]  internal/gate.gitPush                        internal/gate/gate.go:388
  [unique-name]   internal/effects.Catalog.MatchCommand        internal/effects/effects.go:140
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
know how much to trust an edge. Edges of the `[qualified]`,
`[same-package]`, and `[same-class]` tiers lead each list, and
`[unique-name]` guesses follow, so the 15-line cut drops guesses first.
A function or method with no caller edge prints
`callers (0)  — none resolved` and a note: a call by a method name that
several methods share, or through a function value, gets no edge, and
in Go neither does a common method name (`Close`, `Err`, …) from a file
that neither shares the method's directory nor imports its package, a
call whose operand
starts at a package outside the repository, or an unexported method of
another directory. When
every caller edge is a guess, `why` lists them under
`callers (N)  — N resolved by name match only`. The
`usually changed with` lines carry **function grain**: `· mostly …`
names the functions git's hunk headers show actually moved in the shared
commits — a factual report, not a statistical claim. Hunk headers
describe each old commit, so `mostly` leaves out a name that a parsed
partner no longer defines. `seamark check` lists the partners that the
diff leaves out; when it lists none, one `companions` line says why:
every partner is in the diff, the index holds no partner, or the read
failed. On a repo with real history, this section is where the
surprises live.

In a `usually changed with` line of `why`, and in a `usually changes
with` line of `change_set`, `6/58  commits` means that 6 commits
changed both files, out of the 58 commits that co-change counts in the
mined window. That count is the same on every line, and it is not the
commit count of either file. The window is the latest 5000 commits by
default (`--max-commits`). Lockfiles, generated files, and vendored and
build output do not count as changed files. A commit with no other
changed file, a commit with more than 30 other changed files
(`--max-files-per-commit`), and a merge do not count.

### Languages

| Language                      |  Symbols & calls   | Effect sinks | Notes                                                      |
| ----------------------------- | :----------------: | :----------: | ---------------------------------------------------------- |
| Go                            |         ✓          |      ✓       | multi-module monorepos supported                           |
| TypeScript / TSX / JavaScript |         ✓          |      ✓       | ES-module resolution, cross-file named imports             |
| Python                        |         ✓          |      ✓       | relative imports, `__init__` packages, self/cls resolution |
| SQL, HCL                      | history layer only |   planned    | co-change needs no parser                                  |

The history layer (co-change, decisions) is language-agnostic — it works
on every file git tracks.

History mining, the `mostly` names, fix mining, and the index freshness
check run git with fixed options: no color, no external diff, no
textconv, no signature lines, no rename following, a fixed rename
detection, the diff of the root commit, the `a/` and `b/` path
prefixes, raw non-ASCII paths, paths from the top of the work tree, the
short submodule form, the listing of untracked files, and basic regular
expressions for the revert search. A setting such as `color.ui=always`,
`diff.external`, `diff.renames`, `log.showRoot=false`,
`log.showSignature`, `grep.patternType`, or
`status.showUntrackedFiles=no` therefore cannot change what seamark
reads.

The rename detection differs by reader:

- History mining and the `mostly` names read history without rename
  detection. A rename is a deletion of the old path and an addition of
  the new path, so a renamed file starts new history at its new path.
- Fix mining reads the file list of a fix without rename detection, so
  a finding lists a renamed file under its old and its new path. It
  reads the patch of a fix, and the patch identity that drops a
  duplicate fix, with git's default rename detection.
- The index freshness check reads `git diff HEAD` with git's default
  rename detection.

The index freshness check, the `check` fallback, the console
(`seamark tui`), and the LSP server run git so that it does not rewrite
`.git/index`. A change to file metadata
only, such as `touch`, then leaves the git index as it was, so a
concurrent git command does not meet `.git/index.lock`.

An index run records the workspace state that it observes before it
reads any file, and it writes that record in the same transaction as
the graph. A file that changes during the run therefore makes the next
run, `check`, or MCP call rebuild, and the index never reads as current
for content that it did not parse.

## How much can the answers be trusted?

```bash
seamark status          # or --json; also served as MCP resource seamark://status
```

Example status output:

```text
workspace      current (schema v8)
parsed         703 of 832 seen files (98 skipped by config)
symbols        1221, 2931 edges — call resolution 71.3% qualified · 23.9% same-package · 4.8% unique-name (1796 calls)
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

When git tracks files that the working tree no longer has, and the
deletions are not staged, a `deleted` line after `parsed` says, for
example, `47 tracked files in the working tree (deletions not staged)`.
These are the paths that `git status` shows as deleted. They are not in
the seen count and are not parse errors, because no file exists to
parse. `--json` carries the count as
`coverage.files_deleted`, and `seamark index` adds
`(47 deleted in the working tree)` after the seen count.

When the index records another root than the workspace, as in a copied
or moved checkout, a `root` line comes first. It names that root, says
that the report describes it, and names the `seamark index` run that
records this workspace. `--json` carries the root as `recorded_root`.
The MCP resource `seamark://status` prints the same line for the same
workspace and database. When the index is stale and a plain
`seamark index` in this workspace would be refused, because the
database belongs to another repository, the `workspace` line names the
route of that state instead, for example
``STALE — run `seamark index` in <root>``. The stale note of `seamark why`,
`seamark orient`, and `seamark report` does the same. A database of
this workspace keeps ``STALE — run `seamark index` ``.

The MCP server never rebuilds an index that records another repository.
Every read answer (`orient`, `why`, `expand`, `change_set` with its
`change_set_report` key, and both resources) then opens with one note
that names that root:

- When the workspace fingerprint matches the index, as in a sibling
  worktree at the same commit, the note is
  `note: this index records another repository (<root>); it matches this workspace now, and this answer describes <root>.`
  `check` keeps its verdict and opens with the same note.
- When the fingerprints differ, the refresh is refused, and the note is
  `note: seamark did not refresh this index, because it records another repository (<root>) and a rebuild here would move its root; this answer describes <root> and can differ from this workspace.`
  The answer can hold symbols and effects that this workspace does not
  have. `check` refuses, because a verdict needs a current graph.

A recorded root that no longer exists reads `records a directory that
no longer exists (<root>)` in both notes. The `orient` title names the
recorded root, as `seamark orient` does. A server on a database of its
own workspace prints none of these notes.

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
through the other surfaces — `orient` opens with the index health and
warns when files failed to parse, and `seamark check` attaches a note
when a changed file has changes outside every indexed symbol, so absence of evidence never silently reads as evidence of
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
  game. Low-confidence edges are labeled `[unique-name]`. A production
  caller never resolves into a test file in that tier, and a Go test
  caller resolves into a test file only in its own directory. In Go,
  that tier also skips a call whose operand starts at a package outside
  the repository (`strings.NewReplacer(…).Replace`), an unexported
  method of another directory, and a common standard-library method
  name (`Err`, `Replace`, `Close`, …) when the calling file neither
  shares the method's directory nor imports its package. An index built
  before these Go rules keeps its old edges until an index run rebuilds
  it; on an unchanged workspace, `seamark index --force` rebuilds it at
  once.
- A method call whose name several methods share, a call that these Go
  rules skip, and a call through a function value get no edge. `why`
  then can print `callers (0)  — none resolved` for a method that has
  real callers, so read the call sites before you treat it as unused.
  Two of these Go rules also drop real calls. The common-name rule drops
  one from a file that neither shares the method's directory nor imports
  its package: an interface call, or a call on a field, a promoted
  method, a local variable, or a call result
  (`pair.Data.WriteString(…)`, `mk.Writer().Replace(…)`). The
  outside-package rule drops a chain that returns a repository value: a
  generic helper (`cmp.Or(p, q).Render()`, `slices.Max(…)`,
  `sync.OnceValue(…)()`), a conversion to an outside interface type
  (`io.Writer(w).Write(p)`), or an interface value that the outside
  package holds or returns (`flag.Lookup(…).Value.Set(…)`,
  `errors.Unwrap(err).Error()`). Go compiles no call that the
  unexported-method rule drops. Effects never cross a dropped edge, so
  the callee's effects do not reach the caller. `why` names these rules
  only under `callers (0)`, so a symbol that keeps other callers shows
  no sign of a dropped one.
- A local variable shadowing an import alias can produce a wrong edge
  (declared as such via its origin label). Scope tracking is future work.
- Python's DB-API has no syntactic read/write split, so `cursor.execute`
  tags conservatively as `db:write`.
- Co-change needs history: on a young repo the empirical layer is thin
  until commits accumulate.
- From the next index run, a deleted or renamed-away file is never
  named as a partner or a hub, and its commits still count in lift. It
  keeps its live partners. `why` and `change_set` list them when you
  ask for the old path. `check` and the MCP `check` tool list them for
  a diff that deletes the file or renames it away, also as part of a
  directory rename, in a piped diff and in the `git diff HEAD`
  fallback, because check reads the old path of a rename record as a
  changed file. A directory rename moves the partners inside that
  directory too, so the old paths keep only the partners outside it,
  and a live file whose partners all moved shows none. A file that
  comes back is a partner again at the next index run. A renamed file
  starts new history at its new path (see the rename detection of
  history mining above), so it has no partners until commits
  accumulate there.
- Seamark is a navigator, not an oracle. It tells you where to look and
  what usually travels together; "usually changes with" is empirical
  evidence, never a guarantee that _your_ edit is safe or complete. For
  a pinpoint lookup of a known symbol, a plain file read is cheaper —
  seamark earns its round-trip on orientation, risk, and history
  questions.

The LSP surface is [experimental](editors.md); AI coding agent workflows
through MCP, skills, and lesson hooks are the current focus.
