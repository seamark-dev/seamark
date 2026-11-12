# AI coding agent integrations

This guide targets **v0.7.0 or newer**. See
[installation](getting-started.md) to install or upgrade Seamark.

Seamark connects to Claude Code and Codex in three ways:

- **Skills and MCP tools** help the agent explore, plan, and review a
  repository. MCP is the protocol the agent uses to call Seamark tools.
- **Hooks** run automatically before supported edits and shell commands.
  They provide lesson reminders and check commands against repository policy.
  The agent reads a lesson reminder in its next model request, with the
  result of the edit call. So the reminder can guide the next step, not
  the edit that triggered it.
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
[experimental policy guide](policies.md) to configure enforcement.

For Claude Code, use `seamark init --client claude --skills`. To configure
both agents, use `seamark init --client claude --client codex --skills`.
Without `--client`, the older setup behavior remains available: plain
`seamark init` configures Claude Code hooks; the valued `--skills=claude`,
`--skills=codex`, and `--skills=all` forms select skill installation targets
and retain the legacy setup behavior. Use explicit `--client` for new setups.
With `--client`, use bare `--skills`, not `--skills=codex`.

### Check the setup

`seamark doctor` reports missing or conflicting configuration and suggests
corrective actions. `seamark status --json` includes details for each agent.
Both run without logging in or calling a model.

An installed configuration does not prove that Codex has loaded or trusted
it. If tools or reminders are missing, reopen Codex from the repository,
check project trust and `/hooks`, and check that hooks are enabled in Codex.
Also ensure `seamark` is on the `PATH` visible to Codex. A lesson reminder
appears only when Seamark has a relevant lesson for the edited files.

For Claude Code, check that no project settings file sets
`disableAllHooks: true`.
`seamark init`, `seamark doctor`, and `seamark status` read only
`.claude/settings.json` and `.claude/settings.local.json`. They do not read
your user settings (`~/.claude/settings.json`), settings passed with
`claude --settings`, or the Claude Code policy file
(`managed-settings.json`), so a hook or a `disableAllHooks: true` there is
not in their reports.

Doctor warns when a seamark hook command names a binary that does not
exist, or that is not an executable file. The warning names the cause:
the path is missing, is not a regular file, or has no execute bit. The
client cannot start such a hook, so it blocks nothing and delivers
nothing. A committed hook file with an absolute path from another
machine is one example. For the file that setup manages, the fix is a
setup run, which rewrites the command to the running binary; Codex then
asks for a new review in `/hooks`.

Setup keeps the matcher of an existing hook, because a narrow matcher can
be your choice. When the matcher of a seamark hook in the file that setup
manages never fires for the hook's tools, init and doctor warn and name
the matcher to set in that file. A setup run does not change it. When
another handler of setup in that file already runs the hook for each of
its tools, init and doctor name the removal of the idle handler instead,
because a matcher edit would make the hook run twice. A Codex
gate handler in the Claude Code form is the exception when `seamark init
--client codex` removes it, because another gate handler stays: doctor
names that setup run instead.

Codex runs every matching handler, so two handlers of the seamark gate
or lessons hook in `.codex/hooks.json` that run for one tool run that
hook twice. init and doctor warn and ask you to keep one of them. Setup
removes neither, because it does not choose which keys you keep.

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

A running MCP server keeps the seamark binary it started with, because
the agent starts it once per session. After you replace or upgrade the
binary, each tool reply opens with a note that names the old version
until you restart the agent or reconnect its MCP server.

The real-model smoke passed on 2026-09-27 with codex-cli 0.157.0 and
GPT-6 Luna at low reasoning: one group produced a proposal with Codex
provenance, and all 10 script checks passed. A temporary CLI wrapper added
only the model and reasoning overrides to the preset. The tested tree was
uncommitted; its diff was saved with the run record. Trigger extraction
was dry-run only, and trusted-hook isolation was not tested.

## Lesson checks and exploration (Unreleased)

v0.9.0 is the planned first release with lesson-check hook handlers. It
has not shipped. The features below have fixture coverage. Reviewed
native recordings of Claude Code 2.1.319 cover part of them, as the
tables below name; the rest still needs the native evidence of the
[release checklist](#release-checklist). Installing an `enforce`
configuration is not a claim of verified enforcement.

Choose each client's lesson hooks explicitly:

```bash
seamark init --client claude --lesson-checks check
seamark init --client claude --lesson-checks enforce --lesson-exploration on
seamark init --client codex --lesson-checks check
seamark init --client codex --lesson-checks enforce
```

Omitting either option preserves that installed choice. `check` installs
a pre-edit baseline and post-edit verification pair; `enforce` replaces
the baseline handler with the separate pre-write guard and keeps the
post-edit verification. Neither turns the advisory lesson hook into a
guard. Pin modes and check definitions remain in `lessons.yaml`; see
[executable lessons](lessons.md#executable-lessons-advisory-check-and-enforce).

### Tool, stage, and context coverage

Each root and child context has its own reported path. This table names
implemented handlers, not verified native prevention:

| Client / tool | Pre-write | Post-edit | Contexts and current evidence |
| --- | --- | --- | --- |
| Claude Code `Edit`, `Write`, `MultiEdit` | Baseline in check mode; complete-or-unknown projection and guard in enforce mode | `PostToolUse` and `PostToolUseFailure` verification | Root and child paths. `Write` and `Edit`: native deny and in-budget slow deny verified on Claude Code 2.1.319, with the client's 10 s fail-open limit. `MultiEdit`: pending, because the client offered no MultiEdit tool in the recording. Post-edit reply: seen in the recording, still unverified in the report |
| Claude Code `Read` | None | Exploration guidance after the read, when enabled | Root and child; reply seen in the next model request on Claude Code 2.1.319, still unverified in the report; main-context repeats reset at compaction, child reminders repeat |
| Claude Code `NotebookEdit`, `Bash` | No lesson-check hook | No lesson-check hook | Uncovered writers in both contexts; a shell command can write files |
| Codex `apply_patch` | Baseline or guard; Add/Delete and move metadata supported; Update before/after content projection not yet supported | `PostToolUse` verification, also for failed tools | Root and child paths declared; native deny, timeout, reply and child attribution remain pending or unsupported as detailed below |
| Codex structured `Read` | Unsupported | No exploration hook | Setup prints the no-Read notice and installs nothing for exploration; use preparation |
| Codex `shell` | No lesson-check hook | No lesson-check hook | Uncovered writer in both contexts |

For Codex Update hunks, a content check currently gets
`unknown/unprojectable-edit`; an armed guard requests denial instead of
guessing the after-content. Path-only checks can still inspect complete
path metadata. Verified before/after projection is **required before
release**, not an accepted omission from v0.9.0. Post-edit findings and
explicit working-tree verification do not prove pre-write projection.

Codex post-edit findings go back as `additionalContext` in the
`PostToolUse` hook reply on stdout, with exit 0. The advisory edit hook
uses the same form on `PreToolUse`. A native recording of codex-cli
0.164.0 showed that findings on stderr never reached the model, so that
channel is no longer used. No reviewed recording shows yet that the new
reply reaches the model, so the Codex post-edit path stays
`active-unverified` in root and child contexts.

Codex bounds model-visible hook context with `additionalContextLimit`,
about 2,500 tokens by default. So the Codex reply holds at most 3,072
bytes of findings. A longer text, such as the findings of a patch on many
files, keeps its first whole lines. Its last line counts the lines left
out and names `seamark lessons --verify`. That command checks tracked
changes and untracked files that git does not ignore, so it can skip an
ignored file that the call wrote. The Claude Code reply keeps the whole
text.

The installed pre-edit timeout is **10 seconds**, and the post-edit
timeout is **15 seconds**. The handlers budget 5 seconds for the guard
and 10 seconds for post-edit evaluation inside those limits. The hook
timeouts remain candidates until native validation covers each supported
tool and context. A slow-tool result cannot verify another path. Failed
validation reopens the defaults before release. Claude Code 2.1.319
validates the pre-edit timeout for `Write` and `Edit` in the root and
child contexts: it waits for a pre-edit hook that ends after about 7
seconds and honors its deny, and it stops a hook at 10 seconds and lets
the write run. `MultiEdit`, the post-edit timeout, and every Codex path
remain candidates.

### Installed, active, and verified are different

`status`, `doctor`, preparation, mode-change impact and TUI read the same
capability report. It separates the requested pin mode, installed mode,
path state, binary identity and native evidence. An installed path
reads `active` only with verified native evidence and no reason; it
can also read `active-unverified`, `inactive`, or `unsupported`. A
requested mode alone does not establish a working guard. A pin reads
`enforced` only when every pre-write path of the client is `active`. An
`enforced-unverified` badge is explicitly unverified, not the verified
`enforced` state.

A verified path can carry a limit. A client can stop a slow pre-edit
hook at its timeout and let the write run (fail-open). Claude Code
2.1.319 does this at 10 seconds. Such a path reads `active`, and a pin
`enforced`, only when a reviewed native recording of that client
version, tool, and context shows a guard that is slow but ends inside
the timeout, and the client waits for it and honors its deny. The claim
then holds only inside the limit, so every surface that shows it names
the limit too, for example `claude enforced (limit: the client lets the
write run after its 10 s hook timeout)`. Status, doctor, setup,
preparation, mode-change impact, and the TUI print it. The JSON report
carries it in `verification.limit` of the path and in `limits` of the
pin badge. A client that waits for a hook past its timeout, or blocks
the write, gives no limit. No path reads `active` before its recording
is reviewed and committed as test data.

The reviewed Claude Code 2.1.319 recordings give this proof for `Write`
and `Edit` in the root and child contexts. Status prints these paths as
`pre-write Edit, Write active (limit: the client lets the write run after
its 10 s hook timeout)`. The baseline paths of check mode read the same,
because the recording shows that the client waits for a slow pre-edit
hook. `MultiEdit` has no recorded call, so its paths stay
`active-unverified`. In enforce mode its guard paths also name
`native-deny-unverified` and `native-timeout-unverified`, and a Claude
Code enforce pin reads `enforced-unverified`. In check mode its baseline
path reads `active-unverified` with no reason. No Codex path is verified
yet.

Setup keeps warning-and-install behavior for unknown local capability or
trust. It refuses known-bad enforce states, such as disabled hooks (Claude
Code `disableAllHooks: true` in `.claude/settings.json` or
`.claude/settings.local.json`, Codex `[features] hooks = false`) or a
report-only wrapper, rather than silently substituting check mode. A
client without a guard cannot install enforce mode. Codex trust remains
the user's decision: review new or changed hooks with `/hooks`. Seamark
never grants trust, and cannot read that approval back as proof.

A gate or pre-write hook that runs in the background (`async`, and for
Claude Code also `asyncRewake`) cannot block. Setup removes that key
from its own gate, lesson guard, and lesson baseline entries and says
so. Codex then asks you to review that entry again in `/hooks`. A
definition that setup does not manage and that runs in the background
is report-only: setup warns, and enforce refuses a background pre-write
hook. A Claude Code `if` field on a tool event makes coverage uncertain:
setup reports the hook as one that may run, and an owned lesson-check
path reads `tool-uncovered`. On another event, such as `PostCompact`,
that hook never runs. Setup reports an `if` field on its own entries and
leaves it. `onFailure` does not change the gate mode, because a deny
exits with status 2, and status 2 blocks whatever `onFailure` says.

Claude and Codex setup both keep one handler per lesson check. When a
definition that setup does not manage certainly runs a lesson-check hook
that the run installs or keeps, setup refuses before any write, including
a relevant flag-less rerun. When setup cannot tell whether a definition
runs the hook, it warns and installs the managed handler. Claude refuses
only when the definition shares a tool with the managed handler, because
setup keeps a matcher that the user narrowed. A second exploration
handler gets a warning and no refusal, because that hook runs no check.
`--lesson-checks off` is never refused.

Claude Code runs a handler that both settings files define identically
only once: its handler key is the type, `shell`, `command`, `args`, and
`if`. So Claude setup accepts a copy in `.claude/settings.local.json`
that equals the managed handler in every field except `statusMessage`,
and reports it as information. A background key (`async` or
`asyncRewake`) that is not `true`, such as `false`, counts as absent in
this comparison, because either copy then runs in the foreground. A copy with another `if`, `shell`, or `args` is a second
handler: Claude Code runs both, so setup refuses it. A copy with
another `timeout`, `async`, `asyncRewake`, or `onFailure` is refused
too: Claude Code runs one of the two, and setup cannot tell which
fields apply. For the gate, lessons, context reset, and exploration
hooks, such a copy gets a warning that says so, not "runs twice".
Codex runs every copy, so Codex setup refuses each. A plain `init` reads
`.claude/settings.local.json` for this rule only when it installs or keeps
lesson-check hooks, and an unreadable file there gets a warning. Doctor
retains all-client coverage: a Claude-only setup can still warn about
Codex.

For an unreadable hook document, path states report
`inactive (document-unreadable)`. The doctor, status and TUI summaries
read `unreadable (<reason>)`, and the reason names the file. An unreadable
supplemental source keeps the installed mode and adds
`(a hook source is unreadable)`. Beside no installed handler, no path
reports that source, so the summary stays `off`. The JSON report has no
unknown mode, so it keeps `installed: off` and the path reason
`document-unreadable`. Do not read `off` as proof that an unreadable
document contains no handler.

### Delivery attribution

Attribution is separate from hook installation and from the native
recordings above:

| Item | Claude root | Claude child | Codex root | Codex child |
| --- | --- | --- | --- | --- |
| Call match identity | Verified | Verified | Unverified | Unsupported |
| Receiving-lane identity | Verified | Verified | Unverified | Unsupported |
| Context reset | Unverified | Unsupported | Unsupported | Unsupported |
| Turn boundary | Verified | Unverified | Unverified | Unverified |
| Model boundary | Verified | Unverified | Unverified | Unverified |

The reviewed timing recording of Claude Code 2.1.319 verifies the
Claude items marked Verified, for that client version only. Its one
child case is a subagent read, which shows no turn or model step of the
child, so those child items stay unverified. Context reset copies the
evidence of the reset hook, which the report keeps unverified. No reviewed
Codex timing recording exists. Own-call advice arrives in the same call
as the edit that it names, so it is no evidence of guidance before
construction. A command/MCP preparation names no receiver. The delivery
report reads every observation window as incomplete. A guard's requested
deny is not proof of prevention: `prevented` counts only a deny with a
written reply, no later execution record, and a verified native deny
path, which today means Claude Code `Write` or `Edit`. See
[lesson delivery](lessons.md#lesson-delivery-lessons---delivery).

### Disable and downgrade safely

Use the binary that supports the handlers before installing a binary
older than v0.9.0. Remove checks and exploration for **each configured
client**, then inspect the result:

```bash
seamark init --client claude --lesson-checks off --lesson-exploration off
seamark init --client codex --lesson-checks off --lesson-exploration off
seamark status
seamark doctor
```

These commands remove owned lesson-check/exploration entries, not the
advisory reminders, accepted pins or policy. Inspect user-added duplicate
handlers separately. A downgrade does not make a schema 8 database
readable by an older binary, and hook removal does not reverse that
upgrade. Preserve a compatible backup; see
[Going back to an older seamark](configuration.md#going-back-to-an-older-seamark).
For removal of the rest of an integration, see below.

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

Rollback leaves user content and policy intact. First
[disable lesson checks and exploration](#disable-and-downgrade-safely)
for each configured client, if installed. Remove only the remaining
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

## Legacy paths and their retirement

Two forms of several surfaces live side by side: the one that predates
the registry and the one the registry introduced. Each legacy form is
kept on purpose, tested, and listed here with the condition for its
removal, so the doubling stays temporary. Removing a path is a separate
change that deletes the tests that pin it; nothing is removed in
silence.

| Legacy path | Replacement | Pinned by | Retire when |
| --- | --- | --- | --- |
| `seamark init` without `--client`: detection by `.codex/` and `.agents/`, the valued `--skills=claude\|codex\|all` forms (`integration.LegacySetups`, `legacyNotes` in `internal/cli/init_approve.go`) | `seamark init --client <name>` with a bare `--skills` | `TestRunInitLegacySelectionMatrix`, `TestRunInitGateModePrecedence`, `TestLegacySetupsKeepTheGranularIntent` | `--client` has been the only documented setup form for one release, and the plain `seamark init` of the release smoke has moved to `--client claude`; the plain form then becomes an alias of `--client claude` for one more release before the detection code goes |
| `agent.ResolveCommand`, `agent.Resolve`, `agent.New`, and the preset table in `internal/agent` | `Registry.ResolveInvocation` and `agent.NewCommand` | `TestResolveNeedsNoBinary`, `TestResolveInvocationKeepsTheLegacyPrecedence` | the benchmark runners (`internal/bench`, the `cmd/*-bench*` entry points), their only consumers, resolve their agent through the registry; that migration is specified separately and is not part of the integration work |
| Hook commands without `--client` (`gate --hook`, `lessons --hook`, `lessons --hook-reset`) read a Claude Code event | the same commands with `--client claude` | the marker tests in `internal/hooks`, `TestLessonsHook*`, `TestGateHook*` | not planned: every `.claude/settings.json` that an earlier seamark wrote carries the bare form, and a hook command must keep its meaning; a re-run of `init` may rewrite the managed hooks to the explicit form first, and only a release after that rewrite can drop the default |
| The legacy JSON fields of `status --json` (`gate_hook_mode`, `gate_hook_error`, `skills`, `approvals`, `distill_agent`) beside `clients` and `distill_client` | the `clients` array and `distill_client` | the status JSON round-trip tests, the MCP `seamark://status` resource test | the `clients` array has shipped in one release, so an MCP client reading the resource had a release to move; the fields then stay one more release with a deprecation note in `CHANGELOG.md` |
| The version 1 hook-state file and `reviews.LegacyClientID` | the version 2 state keyed by client and receiving context | `TestHookDeliveryStateFailsOpenOnAUnknownVersion` and the delivery state tests | the release that writes version 2 has been out longer than the audit retention (30 days); a version 1 file is then older than any state worth keeping, and the reader can treat it as absent |

Until a row is retired, both forms get the same tests, and a change to
the shared engine must keep both green.

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
| `SetupAdapter` (`Inspect`, `ManagedGateMode`, `Plan`) | reading native documents, recording input guards, planning edits, reading the mode of the gate hook it manages, and reporting corrective actions | applying writes, deduplicating skill directories, or revalidating the whole plan; the coordinator does that |
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
  no second handler when it certainly runs the hook. For a lesson-check
  hook, setup refuses the run instead (`refuseSecondLessonHandler`).
  When the adapter
  cannot tell whether a command runs the hook, install the managed hook
  and name the command; a missing hook costs more than a repeated
  reminder. Such a definition is one that *may* run a gate: the
  inspection reports its mode apart (`possible_gate_mode`), the gate
  line of `init` and `status` names it as possible, and it never makes
  the line say `enforce` or `warn`. A run with an explicit `--gate-mode`
  installs the managed gate hook beside a definition that certainly
  runs the gate but cannot deliver the mode asked for (a `report-only`
  one, or a warn one under a request for enforce). Codex runs both, and
  setup warns that the hook runs twice. Claude Code runs both when the
  two have different handler keys, and setup warns the same. A Claude
  Code copy with the handler key of the managed gate hook is
  `report-only` only through a background key. Claude Code runs one of
  the two copies, so setup warns that it runs only one, and reads the
  managed gate as `report-only` too. A single-quoted executable path generated by Seamark is
  still owned; quoting alone does not make it a wrapper. A wrapped gate
  that certainly runs and whose exit status never reaches the client
  (`… || true`, `… ; echo done`, `… | cat`) is `report-only`: it runs
  and reports, no verdict of it blocks, and a finding says what to
  change. A gate in the background (`… &`) may run, so the adapter
  installs the managed hook beside it, and the finding about its exit
  status still applies. A gate definition that the client runs in the
  background (its execution rule, read beside the matcher rule) is
  `report-only` too, and the finding names the key. The managed mode
  stays the mode of the owned marker, because the next setup run keeps
  that mode and removes the key. The reader parses a hook command
  with `mvdan.cc/sh`, the parser the gate uses, in the bash dialect,
  which accepts every POSIX form, so a form that bash and dash read in
  different ways (`&>`, `((` at the start of a command) is seen and
  refused rather than misread; it bounds the words and the nesting of
  a command before the parser reads it, because a command line from a
  repository file is untrusted input.
- **Plan reads; the coordinator writes.** `Plan` returns guards for
  every input it read, at most one write or one keep per native
  document, and findings. The coordinator verifies the guards again
  before writing, refuses a path with a symbolic link component, writes
  through a temporary file, and reports what landed after a failure. A
  changed input between plan and apply stops the run as stale.
- **Inspect reads each document by the link rule of `Plan`.** `Plan`
  refuses a symbolic link at a document that the adapter writes
  (`.claude/settings.json`, `.codex/hooks.json`, and
  `.codex/config.toml` when the run registers the server or grants
  tools). It follows a link at a document that it only reads, or
  writes only when the registration is missing (`.mcp.json`,
  `.claude/settings.local.json`, and the inline hooks of
  `.codex/config.toml` in a run that registers nothing). The
  coordinator refuses every write through a link. Every inspection
  reader, the approval records included, applies the same rule and
  refuses a path that is not a regular file. For a linked document
  that stops setup, the inspection action names the fix that clears
  it, never a re-run alone: replace the link, or make the change in the
  file it points to. An absent hook entry names the command that
  installs the hook in that workspace, so doctor never suggests a run
  that stops at a link.
- **One rule for the managed gate mode.** `ManagedGateMode` reads the
  managed hook document alone and returns the mode of the gate hook
  setup manages, or "" for an adapter without one. It must equal
  `Inspect(root).ManagedGateMode` and the installed mode that `Plan`
  keeps when no mode is requested, so read all three through one helper
  (see `codexInstalledGateMode`). init calls it before planning, so it
  must not read any other document; a test records the documents it
  reads. `Plan` rewrites every owned gate command, also one under a
  matcher that never fires, and the narration reports each `--enforce`
  that the rewrite removes. Codex also runs a gate handler in the
  Claude Code form (`gate --hook` or `gate --enforce --hook`, without
  `--client codex`), and that handler blocks. So the Codex readers count
  it as a managed gate in its own mode, and doctor names it. The Codex
  `Plan` keeps at most one such handler: none when a Codex-form handler
  runs for Bash, else the first one that runs, rewritten in place to the
  managed command with its matcher and other keys. It removes the
  others, and an entry left without handlers. `Plan` keeps every
  Codex-form handler, so it reads the merged document and warns when two
  of them still run for one tool, as `Inspect` does. The merged document
  also picks the route for a Codex-form handler that never fires, because
  the merge rewrites a handler whose binary is missing, and that handler
  then runs. Without `--gate-mode`,
  enforce in either form wins, and each removal or rewrite names the
  `/hooks` review that the new trust hash needs.
- **A finding names its document once.** `Finding.Path` is the document
  that holds what the finding describes, and every consumer prints
  `<Path>: <Reason>`. The reason never starts with a path. It names a
  second document only when the finding is about two. The reason of a
  read error drops the path in front of the error (see `readReason`).
  The setup and matrix tests reject a reason that starts with a path or
  that names its own path again.
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
| `make agents-native-check CLIENT=codex` | the client CLI on PATH; no model, no credentials | the installed version beside the recorded evidence; the preset's flags on this version; the patch oracle on this version; the exit status, output, and after bytes of every apply_patch content case, when the installed version is the recorded one (else it skips with the reason); the preset refusing a workspace write under an allow rule, returning the final message on stdout, and failing with the provider's reason; the generated configuration read by the client from a scratch home that trusts the disposable repository |
| `make agents-oracle-record ORACLE_VERSION=<version>` | the Codex CLI on PATH at that version; no model, no credentials | a new recording of the apply_patch content cases into `internal/integration/testdata/codex/apply_patch_oracle.json`, from the installed applier in scratch directories; it refuses a version that is not installed, and it keeps the bytes of the path cases |
| `make agents-native-smoke CLIENT=codex` | the client CLI and operator authentication; costs tokens | one distillation group read through the client preset with no failed group and a proposed pin, the pin's client provenance in the ledger, the command disclosed by `--dry-run`, trigger extraction in dry-run only, and the workspace unchanged afterwards |
| `make native-replay CLIENT=<client>` | Go and the repository's build prerequisites; no client CLI, no model, no credentials | the replay of the reviewed timing and guard recordings in `internal/integration/testdata/<client>/timing` and `.../guard`, and the offline tests of the recording script for that client; a replay without a reviewed recording skips as pending, and a skip gives no evidence |
| `make native-record CLIENT=<client>` | the client CLI and operator authentication; costs tokens | the prompts of one scenario (`NATIVE_RECORD_SCENARIO=timing`, the default, or `guard`) in a scratch repository, with one cost line per run; it writes a sanitized copy for review and never writes into testdata; it refuses a binary or a tree with uncommitted changes unless `SEAMARK_ALLOW_DIRTY=1` |

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

**v0.9.0 is blocked until the prerequisites below are complete and
reviewed.** Fixture success and confirmed version numbering are not
native compatibility evidence. Do not publish the release while any
required path or follow-up remains unresolved.

Before release:

- Collect and review native timing and guard recordings for each
  supported client, tool, stage and root/child context. Record versions,
  receiving identity, reply visibility, denied execution and slow-hook
  behavior. Preserve pending or unsupported states for unproven paths;
  never generalize a successful path to the rest. Claude Code: the
  timing and guard recordings of 2.1.319 are reviewed and committed in
  `internal/integration/testdata/claude/timing` and `.../guard`;
  `MultiEdit` has no recorded call. Codex: pending.
- Validate the candidate 10-second pre-edit and 15-second post-edit
  timeouts per tool/context, including slow cases. Change defaults if
  validation fails. Verify post-edit reply visibility separately from
  pre-write denial, and verify Codex trust without granting it in setup.
  Claude Code 2.1.319 validates the 10-second pre-edit timeout for
  `Write` and `Edit` in both contexts, with a fail-open limit at 10
  seconds.
- Add a verified Codex Update before/after oracle, then implement and
  test content projection. Current conservative unknown/deny behavior
  is not sufficient for the planned release.
- **Done:** Refuse conflicting duplicate lesson-check handlers consistently
  across clients. Retain Codex strict refusal and add equivalent Claude
  handling.
- **Done:** Fix the misleading status/TUI `off` summary for unreadable hook
  configuration; keep the already qualified doctor behavior.
- **Done:** Name the internal audit/lock exclusions in verification's
  `not covered` output, as one fixed text. Default selection semantics
  and explicit-path inclusion stay unchanged.
- Investigate and fix the known local-script timeout flake in the
  benchmark tests without weakening validated behavior. Handle any
  benchmark fingerprint change explicitly; successful reruns alone do
  not close this prerequisite.

Native recording commands require the operator's login, can cost tokens,
and run in scratch workspaces. Cover the per-tool/context cases above:

```bash
NATIVE_RECORD_SCENARIO=timing make native-record CLIENT=claude
NATIVE_RECORD_SCENARIO=timing make native-record CLIENT=codex
NATIVE_RECORD_SCENARIO=guard make native-record CLIENT=claude
NATIVE_RECORD_SCENARIO=guard make native-record CLIENT=codex
```

A sanitized recording needs review before evidence declarations change.
The reviewer copies a reviewed recording into
`internal/integration/testdata/<client>/timing` or `.../guard`, where
`make native-replay CLIENT=<client>` replays it offline, with no model.
Do not substitute an in-process handler fixture for a native result. Keep
raw conversations and native identifiers out of public reports.

Then run the existing release gates:

1. `make verify`: one full gate that runs the ordinary tests, the race
   detector, lint and the binary smoke exactly once, in that order. It
   refuses a narrowed package, regex or flag selection.
2. `make agents-native-check CLIENT=codex` on a machine with the CLI:
   record the installed version and every `blocked` line.
3. `make agents-native-smoke CLIENT=codex` from a committed tree with
   your own authentication. Record the version, date, and result. Keep
   untested behaviors pending in `docs/STATUS.md` and the adapter's
   evidence constants; one successful check does not verify every feature.
4. Review the generated files of the fixture run for anything
   credential-shaped; the checks grep for the canary they set, not for
   every possible secret.
5. `make native-replay CLIENT=claude` and `make native-replay CLIENT=codex`
   replay the reviewed timing and guard recordings. A replay without a
   reviewed recording skips as pending and gives no evidence. A reviewed
   recording moves an aspect from pending to verified only for the client
   version, tool and context that it shows.
6. Update the per-agent table above, `docs/STATUS.md`, the README's
   setup section, and `CHANGELOG.md`. `TestUserDocsCoverEveryCommand`
   and `TestOnboardingGuidesNameRealCommands` check the command
   names and flags in the documentation against the CLI.
