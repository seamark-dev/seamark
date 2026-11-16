# Native Codex guard recording: codex-cli 0.169.0

This directory holds a **native** recording, not synthetic fixtures.
codex-cli 0.169.0 (`codex --version` before and after the call, macOS)
ran the 16 prompts of the guard scenario on 2026-11-16 (UTC). The
replay tests `TestNativeRecordCodexGuardRecording` and
`TestNativeRecordCodexGuardExtension` read the recording offline
(`make native-replay CLIENT=codex`), and
`TestGuardEvidenceMatchesRecordings` checks the guard evidence registry
and the timeout outcome against it in every test run. No test runs a
client.

The timing recording in `../timing` comes from the same commit and the
same client version.

## How it was recorded

`make native-record CLIENT=codex NATIVE_RECORD_SCENARIO=guard` ran
`scripts/native-record.sh` at commit `d7a24f9` of the release branch,
with the Seamark binary of that commit (`v0.8.0-301-gd7a24f9`). The
sanitizer `scripts/native-record-sanitize.py` of a later commit of the
same branch, with the Codex ID pass and the time shift, made this copy
from the same raw recording (see "Sanitization").

The script made a scratch Git repository with an enforce pin on
`sentinel/` (rule `frozen-sentinel`, a path check) and one on `notes/`
(rule `no-gate5-marker`, the pattern check `gate5-marker`), and ran
each prompt in a new thread
with `codex exec --json` on the model `gpt-5.6-luna` with low
reasoning, in the `workspace-write` sandbox. Every turn context of the
copy names that model, the effort `low`, and the approval policy
`never`. The automatic update of the client was off.

The hook document of the scratch repository held:

- the hooks of `seamark init --client codex --lesson-checks enforce`,
  as the release lays them out: `gate --hook` on Bash,
  `lessons --hook` on `apply_patch`, `lessons --guard --hook` on
  `apply_patch` (alone in the last PreToolUse group), and
  `lessons --verify --hook` on PostToolUse `apply_patch`. It held no
  capture hook.
- test instrumentation: an observer entry on each event, which copies
  each payload, and the indirection entry
  `sh /opt/instrumentation/pre-write.sh` on `apply_patch`. The phase
  sets what the indirection entry does.

The hook timeouts were 10 s on the guard, the lesson hook, and the
indirection entry, and 15 s on `gate --hook` and on
`lessons --verify --hook`.

Codex runs a project hook only after the user reviews and trusts it,
and a changed entry needs a new review. The script never grants trust:
it set up the scratch repository and stopped, the operator accepted the
project trust prompt and trusted each hook entry in Codex's own review
(`/hooks`), and a second call of the script with `NATIVE_RECORD_REPO`
reused that repository for the paid runs. A phase without the owned
guard removes only that entry and keeps every other entry at its key
with its content, so one review covers every phase. Before the paid
runs, a check without a model call read the hook list of Codex and
found every entry of both hook forms trusted.

Each case runs in one phase:

| Phase | Setup |
| --- | --- |
| `enforce` | the hooks of `init --lesson-checks enforce` |
| `check-pins` | the same hooks; both pins have mode `check` |
| `broken-config` | the same hooks; one check kind does not exist |
| `wrapper` | no owned guard; the indirection entry runs the guard and discards its exit status |
| `slow` | no owned guard; the indirection entry waits 15 s, past the 10 s hook timeout, and then runs the guard |
| `slow-deny` | no owned guard; the indirection entry waits 7 s, inside the hook timeout, and then runs the guard |

`guard-capability.jsonl` also holds a `wrapper-inline` report, which no
run uses.

## Cases

A child case starts one worker through the agent tools of code mode
(`multi_agent_v1__spawn_agent`, `__wait_agent`, `__close_agent`). Each
of the three child cases started its worker.

| Run | Case | Phase | Lane | Shows | Replay |
| --- | --- | --- | --- | --- | --- |
| 1 | `add-violation` | `enforce` | root | an Add in the sentinel scope is denied before it writes | pass |
| 2 | `update-violation` | `enforce` | root | an Update in the sentinel scope (absolute path) is denied | pass |
| 3 | `delete-violation` | `enforce` | root | a Delete in the sentinel scope is denied | pass |
| 4 | `move-violation` | `enforce` | root | the client refuses the pure-move patch in its own patch check, before any hook | skip |
| 5 | `multi-file-violation` | `enforce` | root | one violating file of a two-file patch denies the whole patch | pass |
| 6 | `subagent-violation` | `enforce` | child | a worker Add in the sentinel scope is denied | pass |
| 7 | `update-unknown` | `enforce` | root | an Update in the pattern scope is denied as `unknown` (`unprojectable-edit`) | pass |
| 8 | `permitted-write` | `enforce` | root | a permitted Add completes, and its post-edit check passes | pass |
| 9 | `update-permitted` | `enforce` | root | a live Update outside every pin: one hunk on `free/oracle.txt` | pass |
| 10 | `config-error` | `broken-config` | root | an invalid check set denies a patch outside every pin | pass |
| 11 | `check-only` | `check-pins` | root | check pins never block; the post-edit reply reports the violation | pass |
| 12 | `report-only-wrapper` | `wrapper` | root | the guard denies, and the patch runs | pass |
| 13 | `slow-guard` | `slow` | root | the client stops the pre-write hook at 10 s and lets the patch run | pass |
| 14 | `subagent-check-only` | `check-pins` | child | the post-edit reply enters the worker thread | pass |
| 15 | `slow-deny-add` | `slow-deny` | root | the client waits for the slow guard and honors its deny of an Add | pass |
| 16 | `slow-deny-subagent-add` | `slow-deny` | child | the same for a worker Add | pass |

Runs 14 to 16 are the cases of `TestNativeRecordCodexGuardExtension`.
So this one copy holds a blocked deny and an in-budget slow deny of
`apply_patch` in both contexts: root (runs 1 and 15) and child (runs 6
and 16).

A deny reaches the model as the output of its `exec` call: `Script
error:` and `Command blocked by PreToolUse hook: seamark: blocked by
policy: …`. No PostToolUse follows a deny. The next event is Stop, or
SubagentStop in a worker. Every Add that ran wrote a final newline, so
the post-edit records of runs 8, 11, and 14 read `projection-match`.

### Known skip: the pure move (run 4)

The prompt asks for one Update File section with a Move to line and no
hunk. The model wrote exactly that patch. codex-cli 0.169.0 refused it
in its own patch check (`apply_patch verification failed: invalid hunk
at line 2, Update file hunk for path 'sentinel/move.txt' is empty`),
before any hook. So no PreToolUse payload, no audit record, and no
identity row exist for the run, and the fixture is unchanged. The model
replied `blocked` after the refusal. The replay skips the case with a
note that names the client refusal. The pure-move path keeps no deny
proof on this version, and no registry entry needs it: the root
`apply_patch` entry rests on runs 1 and 15.

### The slow guard (run 13)

The indirection entry waits 15 s before it runs the guard, and the hook
timeout is 10 s. PostToolUse `apply_patch` came 10.06 s after
PreToolUse, and the patch added `sentinel/slow.txt`. Every other
allowed patch of both copies shows about 0.1 s between the two events.
The indirection entry logged no line, so its `pre_write` is empty, and
the audit holds no guard record of the call. So Codex ended the hook
process at the timeout, before the guard ran, and then ran the patch.
This is the documented fail-open behavior at the 10 s timeout, not a
defect. Codex wrote nothing to stderr and no event about the timeout,
so only the limit text tells the user. The post-edit record of the call
reads `incomplete` with `no-baseline`.

### In-budget slow deny (runs 15 and 16)

The indirection entry waits 7 s and then runs the guard. Codex waited
about 7.2 s, read exit 2, did not run the patch, and showed the deny,
in the root and the worker. Each run holds exactly one covered
PreToolUse attempt and no PostToolUse of `apply_patch`, and the fixture
is unchanged.

### Replies

The one-word reply of each case is a corroboration only.

- Each post-edit record is a hook developer record of the rollout
  (`hooks.additional_context`). It sits after the `FileChange` item and
  before the call output and the next model step, in the root and in
  the worker.
- The check-only prompts ask for the check ID that the post-edit
  message names (`frozen-sentinel`). Run 11 replied the pin ref
  `04272dab8b19`, and the worker of run 14 the snapshot digest
  `5dd80e082998`. In each thread, only the post-edit record carried
  that value before the reply.
- The worker of run 14 listed its tools and called
  `mcp__seamark__change_set` before the patch, which its prompt did not
  ask for. Codex refused the call at its own approval check, before the
  MCP server (`MCP tool call requires approval, but approval policy is
  never`), so no pin text reached the worker before the patch.

### What the recorded reports show

The capability reports and the audit records come from the binary of
the recording, which had no reviewed Codex evidence yet. So every guard
record reads `native_blocking: unverified`, and in `enforce` both
pre-write paths read `active-unverified (content-unprojectable,
native-deny-unverified, native-timeout-unverified, trust-unknown)`. The
replay checks that no report claims enforcement without evidence. The
evidence registries of the current tree read this copy: the evidence of
both paths reads verified, with the limit "the client lets the write
run after its 10 s hook timeout". `trust-unknown` stays, because
Seamark cannot read the Codex trust record, so no Codex path reads
`active`.

## Sanitization

The sanitizer replaced each local value with the placeholder that the
`placeholders` field of `manifest.json` names: the scratch repository,
the home directory, the user name, the Seamark binary, the
instrumentation directory, and the account identifiers. It replaced the
base instructions, the text of every developer message, the encrypted
reasoning, the rate limits, and the world state of the rollouts with
the marker `<sanitized>`, and kept the section tags. The `tools` of
each `additional_tools` record also read `<sanitized>`, because that
record lists the tools of the operator's environment. The sanitizer
kept the tool outputs, so the rollouts still hold the 9 deny outputs as
`custom_tool_call_output` records. The 4 post-edit records are
developer messages, so only `guard-context.jsonl` keeps their text, as
the script copied them from the rollouts. That file also holds a copy
of the 9 deny outputs.

An ID pass replaced each ID that Codex or its API generated: the
thread, turn, window, item, exec, call, response, reasoning, message,
and tool list IDs. A Codex session ID and agent ID are thread IDs, so
they share the thread kind. Each real ID has one synthetic value, the
same in every file and file name. So the joins of the replay stay: a
hook `tool_use_id` to a rollout item, a `session_id` to the
`session_meta` of a rollout, an `agent_id` to the worker thread, and a
`transcript_path` to a rollout file. `synthetic_ids` in `manifest.json`
counts the values of each kind (464 in all). The map from the real IDs
to the synthetic IDs stays outside the repository, because it holds the
real IDs. `guard-identities.jsonl` joins each payload to its audit
record by digests. It holds no raw session, agent, or tool-use ID.

Every recorded time moved by one constant offset in whole seconds, so
the copy names no real time of day. The first recorded time of the copy
reads 21:00:00 UTC. Every duration and the order of every record stay
exact, so the timeout and slow-deny durations above are the recorded
ones. A rollout file name holds the start time of its thread in UTC,
the same time as its `session_meta`. `times_shifted` in `manifest.json`
marks the shift. The time zone tag reads `Etc/UTC`.

Prompts, tool input, and tool output stay as recorded, apart from the
placeholders and the synthetic IDs.

Privacy check before the copy landed: a scan of every file found no
home path, user name, scratch directory name, e-mail address,
credential text, local time offset, real ID, or time between 08:00 and
21:00 UTC.

## Files

| File | Holds |
| --- | --- |
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the time shift flag, the 113 hook payloads by run and event, the rollouts, and the extra files |
| `hooks/` | the 113 hook payloads in the order of the observer |
| `sessions/` | the 19 rollout files: 16 parent threads and 3 workers |
| `guard-cases.jsonl` | per run: the case, the phase, the fixture state before and after, the audit range, the lines of the indirection entry, and the one-word reply |
| `lesson-check-audit.jsonl` | the 20 lesson-check audit records |
| `guard-identities.jsonl` | the 15 identity rows that join a payload to its audit record, one per covered PreToolUse (none for run 4) |
| `guard-context.jsonl` | the 13 reply records of the rollouts: 9 deny outputs and 4 post-edit records |
| `guard-hooks.jsonl` | empty: Codex writes no debug log of its hook commands |
| `guard-capability.jsonl` | the 8 lesson-check capability reports of `seamark status --json`, one each time a phase starts |
| `exec-events.jsonl` | the `codex exec --json` events of each run |

Earlier recordings of this scenario on codex-cli 0.164.0 were never
committed, and their scratch directories are gone. Homebrew updated the
client to 0.169.0 before this recording, so this copy is the evidence.

## Record again

A new recording needs the operator's own login and costs tokens.

1. `NATIVE_RECORD_SCENARIO=guard make native-record CLIENT=codex` from
   a committed tree sets up a new scratch repository and stops before
   any paid run.
2. Open the repository in Codex, accept the project trust prompt, and
   trust each hook entry in `/hooks`. Seamark never grants trust.
3. `NATIVE_RECORD_REPO=<repository> NATIVE_RECORD_SCENARIO=guard make
   native-record CLIENT=codex` reuses the repository, runs the paid
   prompts, and writes the raw recording and a sanitized copy into its
   output directory. It never writes into testdata.
4. Review the copy case by case. To check the ID pass, run the
   sanitizer again with `--id-map <file>` outside the repository.
5. Replace this directory with the reviewed copy, update this README,
   and run `make native-replay CLIENT=codex`. A new client version
   needs new registry entries (`codexEvidenceVersion`) and a new
   timeout outcome.
