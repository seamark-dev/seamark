# Threat model

What seamark's guard layer (command gate, diff check, lesson guard,
audit) defends against, what it only degrades, and what it explicitly
cannot do. The
one-line version: **Guard is a defense-in-depth policy layer, not a
sandbox.** It raises the cost of the mistakes agents actually make; it
does not confine a determined adversary.

## Actors, strongest defended to undefended

**1. An honest agent making mistakes.** The primary case and the design
target. The gate classifies commands with a real shell parser (wrappers
unwrapped, interpreter payloads re-parsed, variable indirection surfaced
as `dynamic`), the check evaluates a diff's transitive effect reach, and
lessons inject the mistakes reviewers already flagged. Warn mode
surfaces all of it without blocking; enforce mode makes deny and
require_approval verdicts binding. The lesson guard checks a proposed
edit against the pins in `enforce` mode before the tool writes, and asks
the client to deny a violation. This layer is effective here because
an honest agent reads the verdict and corrects course.

**2. A prompt-injected agent.** Partially defended. The hooks still run
and still classify — a hijacked agent told to `terraform apply` in prod
hits the same deny rule. Repository text that flows into seamark's own
surfaces is treated as data (rendered output is sanitized; distillation
prompts frame review comments as quoted material, not instructions).
But an injected agent is an adversary with shell access, which leads
directly to actor 3.

**3. An agent that edits seamark's own configuration.** Not defended,
and this is the honest boundary: `.seamark/policy.yaml`,
`.seamark/lessons.yaml`, the effect catalogue, the hook wiring
(`.claude/settings.json`, `.claude/settings.local.json`,
`.codex/hooks.json`, and the inline hooks and the `[features]` table of
`.codex/config.toml`), and the audit logs live in files the agent can
typically write. An adversary with write access can weaken policy,
remove hooks or turn them off, or delete audit history. In
`lessons.yaml` it can lower the mode of a pin from `enforce` to `check`
or `advisory`, or change its check, and the guard then lets the edit
through. Mitigations that exist today: policy files are committed and reviewable
(a weakened policy shows up in diff review), decisions carry the policy
hash that produced them, a lesson-check record names the digest of the
`lessons.yaml` that it read, Codex asks for a new review of a changed
hook, and `seamark init` reports hook and mode changes loudly. Real
containment (policy pinned outside the workspace, approval tokens minted
out of band) is future Guard work and is a precondition for calling
enforcement production-grade.

**4. Malicious code already in the repository.** Partially defended.
Seamark parses source as data with tree-sitter (no execution), refuses
symlinked audit paths, sanitizes repository text before rendering it,
and bounds interpreter-payload recursion in the gate. Two channels
remain open by design and must be understood:

- **`.seamark/config.yaml` chooses the distillation executable.** The
  committed `agent.argv` is what `lessons --distill` runs, with your
  privileges. In a repository you did not author, that is
  repository-selected code execution one command away: read
  `.seamark/*.yaml` before running distillation there, and check the
  preflight's `agent` line — it names the exact command (sanitized and
  secret-scrubbed) before anything is sent.
- `seamark index` runs `git` in the workspace, and distillation feeds
  reviewer-authored text to a model; a repository crafted against those
  channels is only partially contained.

**5. A compromised external CLI.** Not defended. `git`, `gh`, and the
configured agent CLI run with your credentials at your privilege;
seamark trusts them by construction. Choosing and updating them is host
hygiene, outside seamark's boundary.

**6. A host-level attacker.** Out of scope. Anyone with your filesystem
permissions can delete the audit log, replace the binary, or edit the
database. Local hash chaining could detect some tampering but cannot
prevent deletion by an actor with the same permissions — which is why
the audit log is honest about being append-only *local* evidence, not a
tamper-proof record.

**The lesson guard has two more limits.** It covers only the edit tools
of the client (Claude Code `Edit`, `Write`, and `MultiEdit`; Codex
`apply_patch`). A shell command can write any file, and no lesson guard
sees it. The guard also needs a process that answers in time. Setup
gives the guard a 10 s hook timeout, and the guard has a 5 s evaluation
budget of its own. Recordings of Claude Code 2.1.319 and codex-cli
0.169.0 show that each client lets the write run after the 10 s
timeout. These recordings also verify the native deny of Claude Code
`Write` and `Edit` and of Codex `apply_patch` only; the deny of
`MultiEdit` is not verified.

## Non-goals

- Guard does not sandbox processes, filter syscalls, or contain
  execution. Pair it with real isolation (containers, VM, devcontainer)
  when running untrusted agents.
- Seamark never auto-applies model-generated rules; distilled proposals
  require an explicit maintainer `--apply`.
- Warn mode never blocks anything, by contract; a default init installs
  warn mode only ([data-flow.md](data-flow.md) lists every capability's
  effects).
