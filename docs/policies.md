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
block, and the gate **fails closed** — a malformed payload, a broken
policy file, or an internal error blocks the command instead of silently
allowing it. Re-running `init` without `--gate-mode` keeps whatever mode
each selected client has installed, and every run ends with a `gate`
line stating the effective behavior; a selected client whose hook still
runs without the flag is named under that line. A verdict blocks only by
exit status 2, so a gate hook that you wrap yourself must pass that
status on. A wrapper that discards it (`… || true`, `… ; echo done`,
`… | cat`) reads as `report-only`: that hook reports its verdicts, and
none of them blocks, whatever `--enforce` or `policy.yaml` says. A gate
in the background (`… &`) discards its exit status too. The shell does
not wait for it, so it counts as a hook that may run, and setup
installs the managed gate hook beside it. A hook that only may run, for
example one that another program gets as its arguments, never makes
the `gate` line say `enforce`: the line names it as a definition that
may run a gate, and nothing is known to block. An explicit `--gate-mode`
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
[Threat model](threat-model.md): it is a defense-in-depth
policy layer, not a sandbox.
