# Policies and command checks (experimental)

Policies are experimental and awaiting refinement. Seamark can classify
commands, report possible side effects, and evaluate repository rules.
Enforcement is not a complete approval system or a sandbox. See the
[current limitations](STATUS.md#guard--experimental).

## Check a command

Start in warn mode and review the results before considering enforcement.

`seamark gate` classifies a shell command's effects and evaluates your
policy **before the command runs** — built for agent pre-execution hooks
and CI:

```bash
$ KUBECONFIG=~/.kube/prod.yaml seamark gate --command "terraform apply -auto-approve"
verdict  deny (mode: warn)
effects  [infra:mutate]
  [deny] no-prod-infra-mutation: production infrastructure mutation requires maintainer action
```

What makes it more than a denylist:

- **A real shell parser** (`mvdan.cc/sh`) — every command in pipelines,
  `&&`-chains, and `$(substitutions)` is classified; unparseable input
  is reported as an error; under enforcement it blocks.
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
    message: production infrastructure mutation requires maintainer action
require_approval:
  - id: prod-db-write
    when: 'effect.contains("db:write") && env.is_prod'
    message: database write against a production environment
```

Every rule sees the same four inputs. `gate` fills `effect`, `command`,
and `env`, and gives `diff` its empty values. `check` fills `diff` and
`env`, and gives `effect` and `command` their empty values. Every key is
always present, because CEL stops a rule with an error when it reads a
missing key.

| Input | Value |
| --- | --- |
| `effect` | the effect tags of the command, as a sorted list of strings |
| `command.name` | the base name of the first command whose name is known |
| `command.argv` | the arguments of that command, its name included |
| `command.raw` | the command line as the gate received it |
| `command.is_push`, `command.is_force_push` | whether the commands include a `git push`, and a forced one |
| `command.target_branch` | the branch of that push, when the gate can name it |
| `command.dynamic` | true when a command name comes from a variable, or when an interpreter payload cannot be read |
| `env.detected` | a map of the variables that `environment.detect` names and the process has set |
| `env.is_prod`, `env.is_dev` | `is_prod` is true when a detected value, in lower case, contains one of `environment.prod_markers`; `is_dev` is its opposite |
| `diff.files` | the changed paths of the diff, as a sorted list |
| `diff.effects` | the effect tags that the changed code reaches through its own calls, as a sorted list; the code that reads a changed value is not in it (see below) |
| `diff.unindexed_files` | a number, not a list: the count of changed files with a change outside any indexed symbol (see below) |
| `diff.unknown_reach_symbols` | a number, not a list: the count of changed value and type symbols with no effects of their own, whose readers seamark does not track (see below) |

A rule over a count compares a number, for example
`when: 'diff.unindexed_files > 0 || diff.unknown_reach_symbols > 0'`.

`seamark init` wires the gate into `.claude/settings.json` as a
PreToolUse hook on Bash (`seamark gate --hook` — the payload is read
natively, no jq). `seamark init --client codex` wires the same gate into
`.codex/hooks.json` as a PreToolUse hook on Codex's `Bash` tool
(`seamark gate --hook --client codex`). By default the hook follows
`policy.yaml`'s mode. A fresh configuration uses warn mode and does not block;
an existing enforcing policy remains in force. Use
`seamark init --client claude --gate-mode enforce` or
`seamark init --client codex --gate-mode enforce` to opt in for a selected
AI coding agent. This bakes
`--enforce` into the hook: deny/require_approval verdicts exit 2 and
block, and the gate **fails closed** on its own failures — a malformed
payload, a broken policy file, a policy file that cannot be read, a `-C`
that names no directory, or an internal error exits 2 and blocks the
command instead of silently allowing it. This needs a gate process
that starts and returns. The client stops a gate hook that runs past its
timeout (setup writes 15 seconds), and it cannot start a seamark binary
that was moved or deleted. Then the gate returns no exit status 2 and
cannot block the command, and the client's own rule decides. Claude Code
2.1.319 lets the tool run after a hook timeout (recorded for the
pre-write lesson hook). No Codex recording shows what Codex does in
either case. Re-running `init` without `--gate-mode` keeps whatever mode
each selected client has installed, and every run ends with a `gate`
line stating the effective behavior; a selected client whose hook still
runs without the flag is named under that line. When a client's settings
turn its hooks off (Claude Code `disableAllHooks: true`, Codex
`[features] hooks = false`), the `gate` line of init and status leaves
that client's gate hook out. Init says that the hook does not run: on
the gate line when no other gate hook runs, else in a note under it.
Init also says what the managed gate hook there does once the hooks
run, for example that it carries `--enforce`. When status can read
the hook file that setup manages for that client, the status gate line
names no such client, and it says that no gate hook runs only when no
gate hook runs. The client's warning in the status clients list then
says that its hooks never run. When status cannot read that file, it
names the client as unreadable on the gate line or under it. A
`.seamark/policy.yaml` that fails to load changes this: while a warn or
enforce gate hook runs, the status gate line names the broken policy
and leaves the unreadable file out. A run without `--client`
does not check `.claude/settings.local.json` for gate hooks. Its `gate`
line says so and names `seamark doctor`, which checks that file. A verdict blocks only by
exit status 2, so a gate hook that you wrap yourself must pass that
status on. A wrapper that discards it (`… || true`, `… ; echo done`,
`… | cat`) reads as `report-only`: that hook reports its verdicts, and
none of them blocks, whatever `--enforce` or `policy.yaml` says. A gate
in the background (`… &`) discards its exit status too. The shell does
not wait for it, so it counts as a hook that may run, and setup
installs the managed gate hook beside it. A hook that the client itself
runs in the background (`"async": true`, and for Claude Code also
`"asyncRewake": true`) reads as `report-only` too; setup removes that
key from its own gate hook. A hook that only may run, for
example one that another program gets as its arguments, never makes
the `gate` line say `enforce`: the line names it as a definition that
may run a gate, and nothing is known to block. Beside a gate hook that
runs in warn mode, such a definition never makes the line say that
nothing blocks either: the line says that the gate hooks that certainly
run block nothing, and a note names the definition. An explicit `--gate-mode`
also installs the managed gate hook beside a wrapped gate that cannot
deliver the mode you asked for, such as a `report-only` one, and warns
that the hook then runs twice.

Setup also wires the edit-time lessons hook. Claude Code gets a silent
`PostCompact` reset for `hook_delivery: once-per-context`; Codex reminders
repeat and have no reset hook. See [agent capabilities](agent-integrations.md#what-each-agent-supports)
for the recorded verification limits, including native Codex command blocking.

The AI coding agent receives the denial reason; you see every
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

A failed append never changes a verdict. `gate` and `check` print
`seamark: audit log: <error>` on stderr. The MCP `check` tool ends its
text with `note: the audit record of this verdict was not written
(<error>)`, because the agent never reads the server log.

### Blast radius of a diff

```bash
git diff | seamark check          # or: seamark check   (uses git diff HEAD)
```

A stdin pipe that sends no byte within 1 s counts as idle: `check`
prints `seamark: stdin idle after 1s; checking git diff HEAD` on stderr
and uses `git diff HEAD`, so a harness that keeps stdin open cannot
make `check` wait forever. Under enforcement (`--enforce` or
`mode: enforce`) an idle pipe blocks with exit 2 instead, because the
diff of `git diff HEAD` can allow what the piped diff blocks. Write a
slow diff to a file first
(`gh pr diff 42 > pr.diff; seamark check --enforce < pr.diff`), or run
`seamark check </dev/null` to check `git diff HEAD` on purpose.

The fallback runs `git diff HEAD` with fixed options: no color, no
external diff, no textconv, the `a/` and `b/` path prefixes, raw
non-ASCII paths, paths from the top of the work tree, the short
submodule form, and git's default rename detection (renames, not
copies). A git setting such as `color.ui=always`, `diff.external`, or
`diff.renames` therefore cannot change the diff that `check` reads.

Outside a git work tree, or before the first commit, `git diff HEAD`
cannot run, so `check` needs a diff on stdin (MCP `check`: the `diff`
argument). Without one, `check` says which of the two applies, names
that route, and exits 1. With a terminal or `/dev/null` on stdin, it
says so before the index refresh, so the refusal writes no `.seamark`
folder.

A diff with no changed file never reads as a clean allow. Empty output
of `git diff HEAD` allows with the note `no changed files in git diff
HEAD; untracked files are not part of it`, in every mode, because a
clean tree is a normal state. Input that has bytes but names no changed
file (no `diff --git`, `---`, `+++` or rename header with a path) is
not a diff that `check` can read. In warn mode the verdict carries a
note that names the source (`stdin`, the MCP `diff` argument) and says
that it covers no change. Under enforcement it blocks with exit 2 and
writes no audit entry; the MCP `check` tool returns an error result.

Changed lines map to symbols; symbols carry transitively-propagated
effect tags; the union is what the changed code reaches through its own
calls — even when the edited function never touches a sink itself. The
union leaves out the code that reads a changed value: the index records
calls, not reads, so a change to a constant that a function passes to
`exec.CommandContext` reaches no effect, while the same value written
inline in that function reaches `proc:exec`. The value note below makes
such a change read as unknown, not clean. A package-level
value is a symbol too and carries the calls of its initializer, and a
class carries the calls of its body outside its methods, so an edit
inside a table of handlers or a class field reaches what it calls. A
private JS/TS method (`#m`) is a symbol. Each access to a private
member, called or not, reaches only the member of the class that
declares the name, or the class for a private field. The class reaches
its private methods and accessors, so an edit anywhere in the class
still reports their effects. A method that is no symbol,
such as a JS/TS method with a computed name (`[Symbol.iterator]()`) or
a Python `def` inside an `if` of the class body, is code of its class,
so an edit inside it reaches what the class reaches. A changed line
counts for every symbol whose span covers it. A class span covers its
methods, so an edit inside a method reaches what that method reaches
and also what the class body calls, such as the calls of its fields.
This is a known over-report. Seamark keeps it, because a line is the
finest position of a diff: a rule that counts a line only for the
method drops the class code on that line, such as a decorator on the
line of its method, and then misses an effect. Only added and removed
lines count: the context lines of a hunk do not.
Removed lines count as the line directly above them, and for a pure
deletion also as the line below them, because either line can belong to the symbol that
lost them. When the first removed line with text is indented, the
nearest line with text above them in the hunk counts too: a Python
function ends at its last statement, so a blank line between it and the
removed lines lies outside it. A cut before the first line, such as the
deletion of a whole file, counts no line, so the reach of that file
stays unknown. A rename record puts its old path in `diff.files` with
no changed line and counts it in `diff.unindexed_files`, so the reach
of that path stays unknown too, as for a deleted file. A rule over `diff.files` can
therefore match a rename away from a path, and a rule over
`diff.unindexed_files` can match a rename that edits only symbols of
the new path. A change group is the added and removed lines of a hunk
with no context line between them, and it counts for a symbol when one
of the lines it counts as lies in that symbol. The unknown-reach note
and `diff.unindexed_files` count a file when one of its change groups
counts for no symbol, also when another group of the same file touches
a symbol. A rule over `diff.unindexed_files` can therefore match, and
under enforcement block, more diffs, for example an import or
doc-comment edit next to a function edit. A deletion with no context
line between it and an edit inside a symbol is one group with that
edit, so it counts for that symbol: the index holds only the symbol
spans of the new side.

The value note names the value and type symbols of a change whose
readers seamark does not track:
`1 changed value or type has no effects of its own (internal/git.timeout) — seamark does not track readers of a value, so its effect reach is unknown, not clean`.
A value or type symbol is a Go `const`, `var` or `type`, a JS/TS
module-level `const`, `let` or `var` that holds no function, an
`interface`, `type`, `enum` or class, and a Python module-level
assignment or class. A class field and a class attribute are code of
their class, so an edit there names the class. A symbol is named when
the change adds a line in it or removes lines inside it, outside any
function or method, and the symbol carries no effects. Removed lines lie
inside a symbol when the lines on both sides of them lie in it. A Python
class has no closing line, so a removed last attribute lies inside the
class too: it is indented deeper than the class line, and the line with
text above it lies in the class. The line above a replaced or removed
line counts for `diff.effects` and for the unknown-reach note, but it
names no symbol, because that line did not change: an edit of one const
of a block does not name the const above it. A removed top-level value,
for example a const of a Go const block, leaves no symbol on the new
side to name, so its removal gets no value note. A symbol with effects
is not named, because `diff.effects` shows them. A class is not named
when the same change group counts for one of its methods, for example
an added method with an added blank line before it; that group reports
the effects of the method. The note names at most ten symbols in sorted
order and counts the rest, and
`diff.unknown_reach_symbols` counts them all. The note and the count
never change `diff.effects` or the verdict of a rule that does not read
the count.

Policy
rules over `diff.effects` gate merges the same way `gate` gates commands, and
`diff.unindexed_files` and `diff.unknown_reach_symbols` expose coverage
blind spots to your rules — changed files the index cannot attribute and
changed values whose readers it does not track never silently read as
safe.
`diff.files` and `diff.effects` reach the rules in sorted order, so a
rule that indexes them gives the same verdict on every run. The
`effect` list of `gate` is sorted in the same way.
The text output also appends the recurring lessons governing the
touched files — clearly marked advisory, never part of the verdict,
printed even when the verdict blocks (a deny is exactly when they
matter). `--json` stays verdict-shaped; `--enforce` makes blocking
verdicts exit 2.

### Executable lesson checks are a separate result (Unreleased)

`seamark check --lessons` adds working-tree verification of executable
pins after the existing policy check. MCP `check` opts in with
`lesson_checks: true`. The policy uses its supplied diff; lesson checks
read the current working tree against `HEAD`, including nonignored
untracked changes. These inputs can differ: a supplied diff that names a
file outside the verified snapshot gets a note under the combined line
when an executable check exists.
Plain `check` keeps its policy verdict and advisory guidance unchanged;
when an executable pin covers a changed file, its text ends with a
`lesson checks: not requested` line that names the route.

The text form opens with the combined line, for example `combined: pass
(policy deny, not blocking in warn mode; lessons passed)`. The combined
JSON uses `check-lessons/1`, with policy, lessons and combined results;
`combined.policy` says only whether the policy blocks, and
`combined.policy_verdict` holds the verdict. A blocking policy or a
lesson violation gives exit 2; without either, incomplete lesson evidence
gives exit 1; otherwise exit 0.
The advisory reminder is not a predicate result and never changes the
policy verdict. See [executable lessons](lessons.md#executable-lessons-advisory-check-and-enforce)
for the two supported predicates, omissions and standalone verification.

A pin's `enforce` mode requests the separate pre-write guard. It is not
policy enforcement and does not prove native denial. The ordinary edit
hook still fails open. Its reminder arrives after the agent constructed
the call; use preparation before construction rather than claiming the
hook can reshape the pending arguments. A reviewed Claude Code 2.1.319
recording verifies native guard denial of `Write` and `Edit`, with the
limit that the client lets the write run after its 10 s hook timeout.
Native denial of `MultiEdit` and of Codex `apply_patch`, and the reply
evidence, remain pending; v0.9.0 cannot ship without
[reviewed native evidence and the required fixes](agent-integrations.md#release-checklist).

What Guard can and cannot defend against is stated plainly in
[Threat model](threat-model.md): it is a defense-in-depth
policy layer, not a sandbox.
