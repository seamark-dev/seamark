# Native Codex timing recording: codex-cli 0.169.0

This directory holds a **native** recording, not synthetic fixtures.
codex-cli 0.169.0 (`codex --version` before and after the call, macOS)
ran the five prompts of the timing scenario on 2026-11-16 (UTC). The
replay test `TestNativeRecordCodexTimingRecording` reads the recording
offline (`make native-replay CLIENT=codex`), and
`TestTimingEvidenceMatchesRecordings` checks the timing evidence
registry against it in every test run. No test runs a client.

The guard recording in `../guard` comes from the same commit and the
same client version.

## How it was recorded

`make native-record CLIENT=codex NATIVE_RECORD_SCENARIO=timing` ran
`scripts/native-record.sh` at commit `d7a24f9` of the release branch,
with the Seamark binary of that commit (`v0.8.0-301-gd7a24f9`). The
sanitizer `scripts/native-record-sanitize.py` at commit `abc794b` of the
same branch, with the Codex ID pass, the time shift, and the commit ID,
agent role, and model override masks, made this copy from the same raw
recording (see "Sanitization").

The script ran each prompt with `codex exec --json` on the model
`gpt-5.6-luna` with low reasoning, in the `workspace-write` sandbox.
Every turn context of the copy names that model, the effort `low`, and
the approval policy `never`. The automatic update of the client was off.

The scratch repository held one pin, `timing-sentinel`, on `timing/`.
Its note holds the code word `cobalt-heron`. The hook document of the
repository held the hooks of `seamark init --client codex`, as the
release lays them out: `gate --hook` on Bash (timeout 15 s) and
`lessons --hook` on `apply_patch` (timeout 10 s). It held no capture
hook. Test instrumentation added an observer entry on each event,
which copies each payload. The recording switch for evaluation records
was on, so each hook record of `lessons-audit.jsonl` has
`record_evaluations: true`.

Codex runs a project hook only after the user reviews and trusts it.
The script never grants trust: it set up the scratch repository and
stopped, the operator accepted the project trust prompt and trusted
each hook entry in Codex's own review (`/hooks`), and a second call of
the script with `NATIVE_RECORD_REPO` reused that repository for the
paid runs. Before the paid runs, a check without a model call read
the hook list of Codex and found every entry of the hook document
trusted.

## Cases

Run 2 resumes the thread of run 1 (`codex exec resume`). Every other
run starts a new thread.

| Run | Case | Shows | Replay subtest |
| --- | --- | --- | --- |
| 1 | `prior-preparation` | a Bash call of `lessons --prepare timing/prior.txt`; its command output holds the pin and the code word; one `prepare` audit record without a receiver | a prior preparation, then a patch in a later turn |
| 2 | `patch-after-preparation` | in the next turn of the same thread, the text before the patch is the code word; the patch reminder follows the patch call | a prior preparation, then a patch in a later turn |
| 3 | `same-call` | a patch with no read first; the reminder enters the thread after the call that it names | a reminder on its own call |
| 4 | `multi-file-patch` | one patch of two files gives one payload and one audit record; the reminder names `2 files` | one patch for two files |
| 5 | `subagent-patch` | a worker patch keeps the parent `session_id`, adds the `agent_id` of SubagentStart and its own `turn_id`, and gives a `child` audit record | a subagent patch |

Runs 1 to 4 match their expected outcome. Run 5 shows the expected
worker patch, but the worker also made a failed `change_set` call
before the patch; the note on run 5 below says why the case stays
accepted. Every replay subtest passes, also "the audit records name
turns and lanes".

In each of the four patch calls, the reminder is a hook developer
record of the rollout. It sits after the `exec` call and before the
`FileChange` item and the call output, so the next model step follows
it. The turn of run 2 rests on the command output of run 1, because
the gate hook gives no reply in warn mode. Codex names the turn in each
payload (`turn_id`).

The timing evidence registry reads this copy for the root context
only: run 3 for the call and the lane, and runs 1 and 2 for the turn.
No case shows a model step, so that item stays pending.

### Note on run 5

The worker listed its tools and called `mcp__seamark__change_set`
before the patch, and read the new file after it. The prompt of the
worker did not ask for these steps. Codex refused the `change_set` call
at its own approval check, before the MCP server: the item reads
`failed` with `MCP tool call requires approval, but approval policy is
never`, and no PostToolUse and no `change_set` audit record follow. So
no pin text reached the worker before the patch. The case verifies no
item: the payloads of a Codex worker give no call or lane identity of
the child (see `payloadIdentities`).

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
own Codex configuration. The model override list of the same tool reads
`<sanitized>` in that output too, because it names the models and the
reasoning efforts of the operator's account; the model of the run,
`gpt-5.6-luna`, stays. Only `hook-context.jsonl` keeps the text of
the four hook developer records of the patch reminders, as the script
copied them from the rollouts.
Each one joins its rollout record by its message ID and its time.

An ID pass replaced each ID that Codex or its API generated: the
thread, turn, window, item, exec, call, response, reasoning, message,
tool list, and process IDs. A Codex session ID and agent ID are thread
IDs, so they share the thread kind. Each real ID has one synthetic
value, the same in every file and file name. So the joins of the replay
stay: a hook `tool_use_id` to a rollout item, a `session_id` to the
`session_meta` of a rollout, an `agent_id` to the worker thread, and a
`transcript_path` to a rollout file. `synthetic_ids` in `manifest.json`
counts the values of each kind (156 in all). The map from the real IDs
to the synthetic IDs stays outside the repository, because it holds the
real IDs.

Every recorded time moved by one constant offset in whole seconds, so
the copy names no real time of day. The first recorded time of the copy
reads 21:00:00 UTC. Every duration and the order of every record stay
exact. A rollout file name holds the start time of its thread in UTC,
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
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the time shift flag, the 42 hook payloads by run and event, the rollouts, and the extra files |
| `hooks/` | the 42 hook payloads in the order of the observer |
| `sessions/` | the 5 rollout files: 4 parent threads and 1 worker |
| `timing-cases.jsonl` | the case name of each run |
| `lessons-audit.jsonl` | the 5 audit records: 1 preparation record and 4 hook records (3 root, 1 child) |
| `hook-context.jsonl` | the 4 hook developer records of the patch reminders, one per `apply_patch` call |
| `exec-events.jsonl` | the `codex exec --json` events of each run |

Earlier recordings of this scenario on codex-cli 0.164.0 were never
committed, and their scratch directories are gone. Homebrew updated the
client to 0.169.0 before this recording, so this copy is the evidence.

## Record again

A new recording needs the operator's own login and costs tokens.

1. `NATIVE_RECORD_SCENARIO=timing make native-record CLIENT=codex` from
   a committed tree sets up a new scratch repository and stops before
   any paid run.
2. Open the repository in Codex, accept the project trust prompt, and
   trust each hook entry in `/hooks`. Seamark never grants trust.
3. `NATIVE_RECORD_REPO=<repository> NATIVE_RECORD_SCENARIO=timing make
   native-record CLIENT=codex` reuses the repository, runs the paid
   prompts, and writes the raw recording and a sanitized copy into its
   output directory. It never writes into testdata.
4. Review the copy case by case. To check the ID pass, run the
   sanitizer again with `--id-map <file>` outside the repository.
5. Replace this directory with the reviewed copy, update this README,
   and run `make native-replay CLIENT=codex`. A new client version
   needs new registry entries (`codexEvidenceVersion`).
