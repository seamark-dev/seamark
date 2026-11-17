# Native Codex post-edit recording: codex-cli 0.169.0

This directory holds a **native** recording, not synthetic fixtures.
codex-cli 0.169.0 (`codex --version` before and after the call, macOS)
ran the 4 prompts of the post-edit scenario on 2026-11-17 (UTC). The
replay test `TestNativeRecordCodexPostEditRecording` reads the
recording offline (`make native-replay CLIENT=codex`), and
`TestPostEditEvidenceMatchesRecordings` checks the post-edit evidence
registry and the post-edit timeout outcome against it in every test
run. No test runs a client.

The Claude Code post-edit recording in `../../claude/post-edit` comes
from the same commit. The timing and guard recordings in `../timing`
and `../guard` come from the same client version.

## How it was recorded

`NATIVE_RECORD_SCENARIO=post-edit make native-record CLIENT=codex` ran
`scripts/native-record.sh` and the sanitizer
`scripts/native-record-sanitize.py` at commit `facb617` of the release
branch, with the Seamark binary of that commit (`v0.8.0-353-gfacb617`).
The sanitizer at commit `334e43a` of the same branch made this copy
from the same raw recording. It also masks the commit ID in each
`session_meta` and the agent role list of the spawn tool (see
"Sanitization"). The copy differs from the output of the sanitizer of
`facb617` only in those 6 values.

The script ran each prompt in a new thread with `codex exec --json` on
the model `gpt-5.6-luna` with low reasoning, in the `workspace-write`
sandbox. Every turn context of the copy names that model, the effort
`low`, and the approval policy `never`. The automatic update of the
client was off.

The fixture files and the two pins are those of the guard scenario:
a path check on `sentinel/` (rule `frozen-sentinel`) and a pattern
check on `notes/`. Both pins have mode `check`, so no case can deny a
write.

The hook document of the scratch repository held:

- the hooks of `seamark init --client codex --lesson-checks check`:
  `gate --hook` on Bash, `lessons --hook` and `lessons --baseline
  --hook` on `apply_patch`, and `lessons --verify --hook` on
  PostToolUse `apply_patch`, alone in the last PostToolUse group. It
  held no capture hook.
- test instrumentation: an observer entry on each event, which copies
  each payload, and the post-edit indirection entry
  `sh /opt/instrumentation/post-edit.sh` on PostToolUse `apply_patch`.
  The phase sets what the indirection entry does.

The hook timeouts were 10 s on the lesson hook and the baseline
handler, and 15 s on `gate --hook`, on `lessons --verify --hook`, and
on the indirection entry.

Codex runs a project hook only after the user reviews and trusts it,
and a changed entry needs a new review. The script never grants trust:
it set up the scratch repository and stopped, the operator accepted the
project trust prompt and trusted each of the 16 hook entries in Codex's
own review (`/hooks`), and a second call of the script with
`NATIVE_RECORD_REPO` reused that repository for the paid runs. The slow
phases remove only the owned verify entry. It is alone in the last
PostToolUse group, so every other entry keeps its key and its content,
and one review covers every phase. Before the paid runs, a check
without a model call read the hook list of Codex and found every entry
of both hook forms trusted. The paid call checked the trust keys of
both forms again before its first turn.

Each case runs in one phase:

| Phase | Setup |
| --- | --- |
| `check` | the hooks of `init --lesson-checks check`; the indirection entry exits at once |
| `slow-post` | no owned verify entry; the indirection entry waits 12 s, inside the 15 s hook timeout, and then runs the verify command |
| `post-timeout` | no owned verify entry; the indirection entry waits 20 s, past the 15 s hook timeout, and then runs the verify command |

The indirection entry logs one line before its wait and one line with
the exit status of the verify command after it (`post_edit` in
`guard-cases.jsonl`).

## Cases

The child case starts one worker through the agent tools of code mode
(`multi_agent_v1__spawn_agent`, `__wait_agent` with `timeout_ms`
120000, `__close_agent`). The review accepted the reading of each case,
two of them with a note.

| Run | Case | Phase | Lane | Shows | Replay | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `execution-failure` | `check` | root | the patch passes PreToolUse and fails while Codex writes it; no PostToolUse follows | skip | accepted with a note |
| 2 | `slow-post-add` | `slow-post` | root | the hook runs to the end, the call output waits 12.15 s after the `FileChange`, and the reply enters the thread before the next model step | pass | accepted |
| 3 | `slow-post-subagent-add` | `slow-post` | child | the same in the worker (12.16 s); the parent waits 23.1 s in `wait_agent` | pass | accepted with a note |
| 4 | `post-timeout-add` | `post-timeout` | root | the call output comes 15.01 s after the `FileChange`, with no post-edit record; the reply is lost, and the file stays written | pass (stopped) | accepted |

### Known skip: the failed patch (run 1)

The prompt asks for one Add of `sentinel/locked/new.txt`. The script
makes `sentinel/locked` a read-only directory (mode 0555) for this case
only, so the patch passes the pre-write hooks and fails while Codex
writes it. The PreToolUse payload names the absolute path
`/workspace/native/sentinel/locked/new.txt`, and the baseline handler
wrote its record. Then the `FileChange` item reads `failed` with
`Failed to write file`, and the call output reads `Script error`. The
client also logged `Exit code: 1` on stderr, outside the copy. No
PostToolUse payload follows. The next event is Stop. The fixture is
unchanged, and the file does not exist.

So no post-edit check ran for the failed patch, and its baseline waits
in `.seamark/checks/pending/` of the scratch repository. A later hook
call or `seamark lessons --verify` discards a pending baseline that is
older than `pending-max-age` (1 h). The replay checks that the decoded
path is exactly the case path and that the file was not created. Then
it skips with the note `codex 0.169.0 sent no PostToolUse after the
failed patch; a hook after a failed patch stays unsupported on that
version`.

The copy shows one failure shape: a patch that passes PreToolUse and
fails during its execution. A patch that Codex refuses in its own patch
check reaches no hook at all, as the pure move of `../guard` (run 4)
shows. No recording shows an Update whose hunk does not match the file,
a write that the sandbox refuses, or a patch of several files that
fails at a later file. So a Codex hook after a failed patch is
unsupported on codex-cli 0.169.0 for the two recorded shapes. The copy
does not show that every failure shape gets no hook.

### Slow hooks (runs 2 and 3)

PostToolUse came about 0.1 s after PreToolUse, and the call output
waited until the slow hook ended: 12.15 s after the `FileChange` in run
2, and 12.16 s in the worker of run 3. Each post-edit reply is a hook
developer record of the rollout (`hooks.additional_context`, with hook
provenance). It sits after the `FileChange` item and before the call
output and the next model step. The audit holds one post-edit record
per call, 12 s after its baseline. The worker payloads carry the
`agent_id` of the SubagentStart payload.

The one-word reply of each case is a corroboration only. Both answered
`04272dab8b19`, the pin ref of the post-edit message, not the check ID
that the prompt asks for. The advisory reminders of the threads hold
`frozen-sentinel` but not `04272dab8b19`, and the post-edit records
hold both. So in each thread only the post-edit record carried the word
that the model gave.

### The refused `change_set` call of the worker (run 3)

The parent listed its tools, started the worker, waited in `wait_agent`
for 23.1 s, and closed the worker. The worker message forbids a retry
and another way. The worker still listed its tools and called
`mcp__seamark__change_set` before the patch, which its message did not
ask for. Codex refused the call at its own approval check, before the
MCP server (`MCP tool call requires approval, but approval policy is
never`). The call has a PreToolUse payload and no PostToolUse payload.
So no pin text reached the worker before the patch. The patch came
after the refusal, and the post-edit facts of the case come after the
patch. The worker of the guard recording did the same (`../guard`, run
14).

### The stop at 15 s (run 4)

The call output came 15.01 s after the `FileChange`, with no developer
record of the post-edit hook, and the model went on 1.7 s later. The
indirection entry logged only `post-timeout start`, and the audit holds
no post-edit record of the call. The file was written. The call output,
the client stderr (empty), and the event stream say nothing about the
timeout. The model answered `none`. So Codex stopped waiting at 15 s,
dropped the reply, and kept the patch: the client drops the post-edit
findings after its 15 s hook timeout, and the edit stays. Codex writes
no debug log, so the copy cannot show whether Codex ended the hook
process at 15 s or at the end of the session about 2 s later. The
baseline of the call waits for the sweep, as in run 1.

Three clocks agree that the waits happened: the rollout times (call
output 12.15, 12.16, and 15.01 s after the `FileChange`), the audit
times (baseline to post-edit record 12 s, twice), and the hook offsets
in `manifest.json` (PostToolUse to the next Stop or SubagentStop 18.3,
15.9, and 17.0 s).

### Not in this recording

- No case stops a hook at 15 s in the worker.
- No failure shape other than the one above, and no hook after it.

### What the recorded reports show

The capability reports come from the binary of the recording, which
had no post-edit evidence. In the `check` phase the pre-write paths
read `active-unverified (trust-unknown)` with the verified codex-cli
0.169.0 evidence and its 10 s limit, and the post-edit paths read
`active-unverified (trust-unknown)` with the evidence `unverified`. The
slow phases read the post-edit paths `inactive (not-installed)`,
because the owned verify entry is the only Codex post-edit entry. The
replay checks that no report claims enforcement without evidence.

The evidence registry of the current tree reads this copy. The
post-edit evidence of `apply_patch` in the root and the child context
reads verified on codex-cli 0.169.0, with the limit "the client drops
the post-edit findings after its 15 s hook timeout; the edit stays".
Each path rests on its in-budget slow case, and the stop at 15 s of run
4 gives the limit. `trust-unknown` stays, because Seamark cannot read
the Codex trust record, so no Codex post-edit path reads `active`.

## Sanitization

The sanitizer replaced each local value with the placeholder that the
`placeholders` field of `manifest.json` names: the scratch repository,
the home directory, the user name, the Seamark binary, the
instrumentation directory, and the account identifiers. It replaced the
base instructions, the text of every developer message, the encrypted
reasoning, the rate limits, and the world state of the rollouts with
the marker `<sanitized>`, and kept the section tags. The `tools` of
each `additional_tools` record also read `<sanitized>`, because that
record lists the tools of the operator's environment. The
`commit_hash` of each `session_meta` reads `<sanitized>` too: Codex
writes the HEAD commit of the scratch repository there, and a commit ID
hashes the commit time and the local time offset. The agent role list
of the spawn tool reads `<sanitized>` in the one tool output that
prints that tool, because the list names the roles of the operator's
own Codex configuration. The sanitizer kept the other tool outputs.
The 2 post-edit records are developer messages, so only
`guard-context.jsonl` keeps their text, as the script copied them from
the rollouts.

An ID pass replaced each ID that Codex or its API generated: the
thread, turn, window, item, exec, call, custom tool call, response,
reasoning, message, and tool list IDs. A Codex session ID and agent ID
are thread IDs, so they share the thread kind. Each real ID has one
synthetic value, the same in every file and file name. So the joins of
the replay stay: a hook `tool_use_id` to a rollout item, a `session_id`
to the `session_meta` of a rollout, an `agent_id` to the worker thread,
and a `transcript_path` to a rollout file. `synthetic_ids` in
`manifest.json` counts the values of each kind (138 in all). The map
from the real IDs to the synthetic IDs stays outside the repository,
because it holds the real IDs. `guard-identities.jsonl` joins each
payload to its audit record by digests. It holds no raw session, agent,
or tool-use ID.

Every recorded time moved by one constant offset in whole seconds, so
the copy names no real time of day. The first recorded time of the copy
reads 21:00:00 UTC. Every duration and the order of every record stay
exact, so the durations above are the recorded ones. A rollout file
name holds the start time of its thread in UTC, the same time as its
`session_meta`. `times_shifted` in `manifest.json` marks the shift. The
time zone tag reads `Etc/UTC`.

Prompts, tool input, and tool output stay as recorded, apart from the
placeholders and the synthetic IDs.

Privacy check before the copy landed: a scan of every file found no
home path, user name, scratch directory name, e-mail address,
credential text, local time offset, real ID, commit ID of the scratch
repository (in full or cut to 7 hex digits), or time between 08:00 and
21:00 UTC.

## Files

| File | Holds |
| --- | --- |
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the time shift flag, the 33 hook payloads by run and event, the rollouts, and the extra files |
| `hooks/` | the 33 hook payloads in the order of the observer |
| `sessions/` | the 5 rollout files: 4 parent threads and 1 worker |
| `guard-cases.jsonl` | per run: the case, the phase, the fixture state before and after, the audit range, the lines of the post-edit indirection entry, and the one-word reply |
| `lesson-check-audit.jsonl` | the 6 lesson-check audit records: a baseline for runs 1 and 4, and a baseline and a post-edit record for runs 2 and 3 |
| `guard-identities.jsonl` | the 4 identity rows that join a payload to its audit records |
| `guard-context.jsonl` | the 2 post-edit records of the rollouts |
| `guard-hooks.jsonl` | empty: Codex writes no debug log of its hook commands |
| `guard-capability.jsonl` | the 3 lesson-check capability reports of `seamark status --json`, one each time a phase starts |
| `exec-events.jsonl` | the `codex exec --json` events of each run |

## Record again

A new recording needs the operator's own login and costs tokens.

1. `NATIVE_RECORD_SCENARIO=post-edit make native-record CLIENT=codex`
   from a committed tree sets up a new scratch repository and stops
   before any paid run.
2. Open the repository in Codex, accept the project trust prompt, and
   trust each hook entry in `/hooks`, while the hook document is in the
   check form. Seamark never grants trust.
3. `NATIVE_RECORD_REPO=<repository> NATIVE_RECORD_SCENARIO=post-edit
   make native-record CLIENT=codex` reuses the repository, checks the
   trust keys of both hook forms, runs the paid prompts, and writes the
   raw recording and a sanitized copy into its output directory. It
   never writes into testdata.
4. Review the copy case by case. To check the ID pass, run the
   sanitizer again with `--id-map <file>` outside the repository.
5. Replace this directory with the reviewed copy, update this README,
   and run `make native-replay CLIENT=codex`. A new client version
   needs new registry entries (`codexEvidenceVersion`) and a new
   post-edit timeout outcome.
