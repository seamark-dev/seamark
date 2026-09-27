# Agent integrations

Seamark connects to Claude Code and Codex in three ways:

- **Skills and MCP tools** help the agent explore, plan, and review a
  repository. MCP is the protocol the agent uses to call Seamark tools.
- **Hooks** run automatically before supported edits and shell commands.
  They provide lesson reminders and check commands against repository policy.
- **Optional inference** asks your installed agent CLI to draft lessons from
  findings or suggest where existing lessons apply.

Start with [setup](#set-up-codex), then read about
[inference and authentication](#choose-an-agent-for-inference).
[Adding an agent](#adding-an-agent) is for contributors implementing a new
integration.

## Set up Codex

Install Seamark and Codex, make both commands available on `PATH`, and
run these commands from your repository:

```bash
seamark init --client codex --skills --print   # preview the files to change
seamark init --client codex --skills           # install hooks, MCP setup, and skills
seamark index                                # build the repository index
seamark doctor
```

Open the repository in Codex. If you trust it, accept Codex's project
trust prompt. Then use `/hooks` in the Codex CLI to review and trust the
Seamark hooks. These are separate decisions: project trust enables the
project configuration; hook trust allows each reviewed hook to run.
Changed hooks need another review. Seamark never grants either kind of
trust. See the [Codex hooks documentation](https://developers.openai.com/codex/hooks#review-and-trust-hooks).

`--skills` is optional. To also add grants for Seamark's five MCP tools,
rerun setup with `--approve-tools`:

```bash
seamark init --client codex --skills --approve-tools
```

Tool grants do not grant project or hook trust. Setup preserves explicit
restrictions already in your Codex configuration and reports conflicts.
The command gate defaults to warning on a new install; see the
[README's policy instructions](../README.md#journey-3-guard-agent-commands) to configure enforcement.

For Claude Code, use `seamark init --client claude --skills`. To configure
both agents, use `seamark init --client claude --client codex --skills`.
Without `--client`, the older setup behavior remains available; see the
[README](../README.md). With `--client`, use bare `--skills`, not
`--skills=codex`.

### Check the setup

`seamark doctor` reports missing or conflicting configuration and suggests
corrective actions. `seamark status --json` includes details for each agent.
Both run without logging in or calling a model.

An installed configuration does not prove that Codex has loaded or trusted
it. If tools or reminders are missing, reopen Codex from the repository,
check project trust and `/hooks`, and check that hooks are enabled in Codex.
Also ensure `seamark` is on the `PATH` visible to Codex. A lesson reminder
appears only when Seamark has a relevant lesson for the edited files.

## What each agent supports

The table distinguishes installed features from behavior verified with a
real client binary. Version numbers identify recorded checks, not a
minimum supported version.

| Capability | Claude Code | Codex |
| --- | --- | --- |
| Skills (`--skills`) | `.claude/skills/` | `.agents/skills/`; verified on codex-cli 0.152.1 |
| MCP registration | `seamark` entry in `.mcp.json` | `[mcp_servers.seamark]` in `.codex/config.toml`; `codex mcp list` read it on 0.157.0; live tool calls remain unverified |
| Tool grants (`--approve-tools`) | five MCP tool rules and three skill rules in `.claude/settings.json` | `approval_mode = "approve"` for the five tools; native approval behavior remains unverified |
| Edit hook (lesson reminders) | `PreToolUse` on Edit, Write, MultiEdit | `PreToolUse` on `apply_patch`; verified on codex-cli 0.154.0 |
| Command gate | `PreToolUse` on Bash | `PreToolUse` on `Bash`; native blocking behavior remains unverified |
| Context reset | `PostCompact` hook | no installed reset hook; reminders repeat, so there is no suppression state to clear |
| Inference (`agent.cli`) | `claude -p` | `codex exec` with the preset below; scripted-provider checks and a real-model distillation smoke passed on 0.157.0 |
| Project trust | managed by Claude Code, outside Seamark | the user's decision in Codex; Seamark reports it as unverified and never grants it |
| Repeated reminders | optional once-per-context delivery | reminders always repeat within the hook budget |

"Verified" means a recorded run exercised that behavior on the named
client version. "Pending" means evidence is still missing. `status` and
`doctor` use the adapter's recorded evidence; running a check does not
update those declarations automatically. Neither command verifies your
current Codex session or trust decisions.

A capability an agent does not declare is reported as unsupported, not
broken. `init` prints what it skipped, and `doctor` does not fail on it.

The real-model smoke passed on 2026-09-27 with codex-cli 0.157.0 and
GPT-6 Luna at low reasoning: one group produced a proposal with Codex
provenance, and all 10 script checks passed. A temporary CLI wrapper added
only the model and reasoning overrides to the preset. The tested tree was
uncommitted; its diff was saved with the run record. Trigger extraction
was dry-run only, and trusted-hook isolation was not tested.

## Choose an agent for inference

Setting up an agent with `--client` does not select it for inference.
To use Codex for `seamark lessons --distill` and
`seamark lessons --extract-triggers`, edit `.seamark/config.yaml`:

```yaml
agent:
  cli: codex
```

Remove any existing `agent.argv` override when selecting this preset:
`argv` takes precedence over `cli`. Claude Code is the default when neither
is configured. Preview a distillation before making a paid call:

```bash
seamark lessons --distill --dry-run
seamark lessons --distill --limit 1
seamark lessons --proposals
```

Distillation drafts proposals for review; it does not apply them to
`lessons.yaml`. See the [lessons guide](lessons.md) for mining findings
and accepting proposals.

### Authentication and usage-based billing

The agent CLI handles authentication and billing. Seamark uses the CLI's
saved login or environment, never calls a model API directly, and never
starts a login flow. You can use the CLI's subscription login or an API key
for usage-based billing.

Keep keys in the CLI's environment or credential store, not in
`.seamark/config.yaml`, `agent.argv`, or hook commands. The setup checks
use a fake key to detect accidental copying into generated configuration.

**Codex.** With `OPENAI_API_KEY` already supplied by your environment,
authenticate the CLI and check its saved login:

```sh
printenv OPENAI_API_KEY | codex login --with-api-key
codex login status
```

For a single non-interactive run, `CODEX_API_KEY` overrides the saved login.
`codex login status` does not validate the override that a later
`codex exec` receives. Check whether that variable is set when choosing
which account pays. See [Codex authentication](https://developers.openai.com/codex/auth)
and [non-interactive authentication](https://developers.openai.com/codex/noninteractive#authenticate-in-automation).

**Claude Code.** `ANTHROPIC_API_KEY` selects API authentication for
`claude -p` and takes precedence over a subscription login. Check the active
method in Claude Code before running a paid job; see
[Claude Code authentication](https://code.claude.com/docs/en/authentication).
To select this preset:

```yaml
agent:
  cli: claude
```

### Presets and custom commands

Prefer `agent.cli: codex`. Its full command is:

```text
codex exec --ephemeral --sandbox read-only -C <root> --skip-git-repo-check --ignore-rules -c features.hooks=false -
```

Seamark supplies the workspace root and sends the prompt on stdin. Codex
returns its final reply on stdout. The preset requests a read-only
sandbox, skips inherited command-allow rules, and disables hooks for that
inference run. Authentication, model/provider settings, and project
instructions still come from Codex's configuration.

Before the preset existed, a custom command was the workaround. This
configuration still works:

```yaml
agent:
  argv: [codex, exec, --ephemeral, --sandbox, read-only, "-"]
```

Custom `argv` replaces the preset entirely. It inherits the caller's
working directory and does not add the preset's flags. In this example,
inherited command-allow rules can permit writes outside the read-only
sandbox, and hooks remain enabled. A custom command is not a guarantee
of isolation or a spending limit.

### Cost and data sent to the model

The provider bills API-key usage. A failed group is eligible for retry on
the next run, which can incur further usage charges. The dry run reports a
text-size estimate, not an exact token count or price.

Lesson lookup and hook delivery make no model call. Delivered reminders
become part of the receiving agent's context. Inference runs can also load
project instructions such as `AGENTS.md` or `CLAUDE.md`, so the Seamark
prompt is not necessarily the entire model input. See
[data-flow.md](data-flow.md) for what Seamark sends and stores.

## Removing an integration

Rollback leaves user content and policy intact. Remove only the
entries seamark wrote, with the agent's native controls or an exact
edit:

- **Claude Code:** delete the three seamark hook entries from
  `.claude/settings.json` (their commands contain `gate --hook`,
  `lessons --hook`, and `lessons --hook-reset`; the gate may also have
  `--enforce`), the `seamark` server
  from `.mcp.json`, and the `mcp__seamark__*` and `Skill(seamark-*)`
  allow rules. The installed skills under `.claude/skills/seamark-*`
  carry an ownership marker; delete the directories.
- **Codex:** delete the seamark entries from `.codex/hooks.json`
  (commands containing `gate --hook --client codex` and
  `lessons --hook --client codex`; the gate may also have `--enforce`).
  Remove the `[mcp_servers.seamark]` table and its tool subtables from
  `.codex/config.toml` if they were added by Seamark. Preserve other
  servers and user settings. Skills live under `.agents/skills/seamark-*`;
  remove only Seamark-owned directories, and keep them if another agent
  still uses this shared location.

An old binary that meets a newer hook-state file delivers every
reminder and leaves the file alone; delete
`.seamark/lessons-hook-state.json` after a downgrade to get suppression
back. Accepted lessons, proposals, and audit history are never
rewritten.

## Adding an agent

Adding an agent is one descriptor and one registration in
`internal/integration`. The shared engines (lesson selection, policy
evaluation, proposal validation, setup planning, diagnostics) do not
change, and neither do the existing adapters. The contract tests in
`internal/integration/contract_test.go` register a test-only third
client to keep that true; it is permanent coverage, never a shipped
client.

### The descriptor

`integration.Client` names the agent and declares its optional
capabilities. Every field but `ID` and `Name` is optional. Here is a minimal
adapter for an agent whose CLI accepts a prompt on stdin and returns its
final reply on stdout:

```go
// internal/integration/acme.go
func acmeClient() Client {
	return Client{
		ID:   "acme", // used by `--client acme` and `agent.cli: acme`
		Name: "Acme Agent",
		Invocation: func(root string) (agent.CommandSpec, error) {
			return agent.CommandSpec{
				Name: "acme",
				Argv: []string{"acme", "run", "-"},
				Dir:  root,
			}, nil
		},
	}
}
```

This example needs the `agent` package import and no setup or hook adapter.
Its CLI and flags are illustrative; choose the actual command for the new
agent. Register the descriptor in `Builtin()`:

```go
func Builtin() *Registry {
	r, err := NewRegistry(claudeClient(), codexClient(), acmeClient())
	// ...
}
```

Add `SkillDirs` if the client can read Seamark skills, and implement
`Setup`, `Edits`, `Commands`, or `Resets` only for features it supports.
For example, `SkillDirs: []string{".agents/skills"}` shares Codex's skill
directory; the setup coordinator writes it once. `SetupOps` declares
which installation operations the setup adapter supports. Use
`codexClient()` and `claudeClient()` as complete examples.

Registration order is presentation order everywhere: `init --help`,
selection, `doctor`, `status`. `NewRegistry` rejects an invalid ID
(lowercase letters, digits, hyphens), a duplicate, an empty name, a
skill directory outside the repository, a setup operation without a
setup adapter, and an advice mechanism name that would not survive in
the firing log.

### The contracts

| Contract | Owns | Must not own |
| --- | --- | --- |
| `SetupAdapter` (`Inspect`, `Plan`) | reading native documents, recording input guards, planning edits, and reporting corrective actions | applying writes, deduplicating skill directories, or revalidating the whole plan; the coordinator does that |
| `EditHooks` (`DecodeEdit`, `EncodeAdvice`, `AdviceMechanism`) | the complete set of paths one native edit affects; the native nonblocking reply | lesson selection, budgets, suppression state, the firing log |
| `CommandHooks` (`DecodeCommand`, `EncodeDecision`, `EncodeFailure`) | the shell text of one native command event; the native block form | policy evaluation, warn/enforce precedence, auditing |
| `ResetHooks` (`DecodeReset`) | the receiving context a native reset clears | deciding whether to clear it |
| `Invocation` | the one-shot command for a workspace root | PATH lookup (`agent.NewCommand`) or process start (`Invoker.Invoke`); neither starts a login |

The rules that hold every adapter to the same behavior:

- **Ownership is by marker, and setup edits only what it owns.** A
  hook command is seamark's when it is one executable word followed by
  the marker (`gate --hook --client codex`). A wrapper, a shell
  condition, or an environment prefix is someone else's definition:
  keep it as written, report it, and install
  no second handler when it certainly runs the hook. When the adapter
  cannot tell whether a command runs the hook, install the managed hook
  and name the command; a missing hook costs more than a repeated
  reminder. A single-quoted executable path generated by Seamark is
  still owned; quoting alone does not make it a wrapper.
- **Plan reads; the coordinator writes.** `Plan` returns guards for
  every input it read, at most one write or one keep per native
  document, and findings. The coordinator verifies the guards again
  before writing, refuses a path with a symbolic link component, writes
  through a temporary file, and reports what landed after a failure. A
  changed input between plan and apply stops the run as stale.
- **Advice never blocks.** `EncodeAdvice` returns exit code zero. A
  decode failure yields no advice. If suppression state is unavailable
  or locked, advice is delivered again; an audit failure does not undo
  delivery. None of these failures blocks the edit. Only the gate
  blocks, with a nonzero `ExitCode` and the reason
  on stderr; `EncodeFailure` renders a gate failure in that form and
  the caller decides whether the mode requires blocking.
- **A partial patch is no event.** `DecodeEdit` returns the complete
  path set or an error (`ErrMalformedEvent`, `ErrUnsupportedGrammar`),
  never a subset. `DecodeCommand` returns `ErrNotApplicable` for an
  event of another tool, so patch text never reaches the gate.
- **Trust is reported, never granted.** An adapter that cannot read
  the agent's trust record reports `TrustUnknown` with the native
  instruction as a finding. `TrustEstablished` on an unsupported
  capability fails validation.
- **Evidence names its scope.** `VerifiedEvidence(version, surface)`
  rejects a blank version or surface. An adapter declares its evidence
  as constants (see `codexEditsEvidence` in
  `internal/integration/codex_setup.go`) and moves a level from
  `pending` to `verified` only after a recorded native run.
- **Receiving context or nothing.** An edit event names the actual
  receiver of the advice (`EventMeta.Context`) or leaves it nil. Nil
  turns once-per-context suppression off for that agent; a parent
  session id is never a receiver key for a subagent.
- **Unsupported is not broken.** `Client.Require` returns
  `ErrUnsupported`; every consumer treats it as not applicable.

### Fixtures and evidence

Native payloads and generated configuration live under
`internal/integration/testdata/<client>/`, one directory per agent,
with a `README.md` that states their provenance. A fixture shaped from
a reference is labelled **synthetic** until a recorded native run
reproduces its shape; the fixture test requires the label. Raw captures
stay out of the repository until sanitized: paths and identifiers are
substituted consistently, and nothing that names an account is kept.

An oracle file (`apply_patch_oracle.json`) records what the installed
client binary did with each input offline, no model involved. The
decoder is tested against it, and the native check reruns it on the
installed version to catch parser drift.

### Tests and checks

| Command | Needs | What it proves |
| --- | --- | --- |
| `make agents-test` | Go and the repository's build prerequisites; no agent CLI or credentials | adapter contracts, setup, hooks, delivery, gates, diagnostics, and fake subprocesses; also covered by `make test` and CI |
| `make smoke` | the built binary | the generated Claude Code and Codex configuration in a fresh repository, the hook commands it wrote running as written, and no credential in a generated file; part of CI and of every release |
| `make agents-native-check CLIENT=codex` | the client CLI on PATH; no model, no credentials | the installed version beside the recorded evidence; the preset's flags on this version; the patch oracle on this version; the preset refusing a workspace write under an allow rule, returning the final message on stdout, and failing with the provider's reason; the generated configuration read by the client from a scratch home that trusts the disposable repository |
| `make agents-native-smoke CLIENT=codex` | the client CLI and operator authentication; costs tokens | one distillation group read through the client preset with no failed group and a proposed pin, the pin's client provenance in the ledger, the command disclosed by `--dry-run`, trigger extraction in dry-run only, and the workspace unchanged afterwards |

The Go checks use ordinary test results and label missing prerequisites
as `blocked` in their failure messages. The shell script prints `ok`,
`FAIL`, or `blocked`; it exits 1 for failures and 3 for blocked checks.
The Makefile stops on either a failed Go test or a nonzero script exit;
do not rely on `make` returning the script's exit code. A blocked check
provides no compatibility evidence.

The Codex offline checks use a scratch `CODEX_HOME`. They configure trust
only for the disposable test repository, leaving your project's trust
unchanged. Scripted responses come from a local test server, not a model.
`CLIENT=claude` runs the shell configuration checks; the Go native tests
are Codex-only.

The credential canary belongs to the offline check only; the smoke
leaves the operator's environment as it is. The smoke's distillation
check passes only when no group failed and a pin was proposed; a failed
agent call is reported as a failure with the client's reason. The smoke
requires `codex login status` to succeed, so an environment-only
`CODEX_API_KEY` setup is not sufficient for its preflight. Neither
target starts a login flow.

The smoke requires a clean Seamark working tree unless
`SEAMARK_ALLOW_DIRTY=1` is set. If you use that override, record the diff
alongside the commit. Its workspace check uses Git status, which excludes
ignored files; it does not prove that every workspace byte is unchanged.
Its hooks start untrusted, so the absence of hook audit records does not
prove that the preset disables trusted hooks. Live gate blocking, native
MCP approval behavior, and trusted-hook isolation need separate checks.

### Release checklist

1. `make agents-test`, then `make test`, `make test-race`, `make lint`,
   `make build`, `make smoke`.
2. `make agents-native-check CLIENT=codex` on a machine with the CLI:
   record the installed version and every `blocked` line.
3. `make agents-native-smoke CLIENT=codex` from a committed tree with
   your own authentication. Record the version, date, and result. Keep
   untested behaviors pending in `docs/STATUS.md` and the adapter's
   evidence constants; one successful check does not verify every feature.
4. Review the generated files of the fixture run for anything
   credential-shaped; the checks grep for the canary they set, not for
   every possible secret.
5. Update the per-agent table above, `docs/STATUS.md`, the README's
   setup section, and `CHANGELOG.md`. `TestReadmeCoversEveryCommand`
   and `TestAgentIntegrationsGuideNamesRealCommands` check the command
   names and flags in the documentation against the CLI.
