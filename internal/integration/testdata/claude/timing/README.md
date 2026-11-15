# Native Claude Code timing recording: Claude Code 2.1.319

This directory holds a **native** recording, not synthetic fixtures.
Claude Code 2.1.319 (`claude --version`, macOS) ran the nine prompts of
the timing scenario on 2026-11-07 (UTC). The replay test
`TestNativeRecordClaudeTimingRecording` reads the recording offline
(`make native-replay CLIENT=claude`), and
`TestTimingEvidenceMatchesRecordings` checks the timing evidence
registry against it in every test run. No test runs a client.

The guard recording in `../guard` comes from the same commit and the
same client version.

## How it was recorded

`make native-record CLIENT=claude NATIVE_RECORD_SCENARIO=timing` ran
`scripts/native-record.sh` and the sanitizer
`scripts/native-record-sanitize.py` at commit `b799ccb` of the release
branch, with the Seamark binary of that commit. The binary reports
`v0.8.0-143-g0cebfa8`, the commit's ID before the branch dates were
rewritten; the tree is the same.
The script made a new scratch Git repository and ran each prompt with
`claude -p` on the model `haiku` (`claude-haiku-5-5` in every result).
It turned off the server-side advisor tool, and the automatic update
of the client was off. Each run had a cost limit of 0.40 USD.

The hook document of the scratch repository held:

- the hooks of `seamark init`: `gate --hook` on Bash,
  `lessons --hook` on Edit, Write, and MultiEdit, and
  `lessons --hook-reset` on PostCompact. It held no capture hook.
- test instrumentation: an observer entry that copies each payload, and
  a hand-wired `lessons --explore --hook` entry on Read, on PostToolUse
  and PostToolUseFailure.

The hook timeouts were 10 s, and 15 s on `gate --hook`. The recording
switch for evaluation records was on, so each hook record of
`lessons-audit.jsonl` has `record_evaluations: true`.

## Cases

Runs 2 to 4 resume the session of run 1. Run 9 starts a subagent in the
background, as its prompt asks.

| Run | Case | Shows | Replay subtest |
| --- | --- | --- | --- |
| 1 | `prior-result` | a Read and its exploration reply, in the chain of the next request | a prior result, then a new model step |
| 2 | `new-model-step` | in a new turn, the reply of run 1 is in the chain of the Edit, and the text before the Edit holds the code word | a prior result, then a new model step |
| 3 | `compact-reset` | `/compact` in print mode: PreCompact, PostCompact, and one reset record that raises both context generations | a compaction resets the root context |
| 4 | `after-compact` | after the compaction, a Read and an Edit of `timing/after.txt`, a file that runs 1 to 3 never name; both records are `injected` in the new generation | advice returns after the compaction |
| 5 | `read-then-edit` | a Read, then an Edit in the same turn | a read, then an edit in the same turn |
| 6 | `same-call` | a Write with no Read; the edit reply is on its own call | a reminder on its own call |
| 7 | `batch-and-program` | two Write calls in one message, and a Bash program that runs `lessons --prepare` and then writes | a batch of writes and a program that prepares and writes |
| 8 | `failed-read` | a Read of an absent file reaches PostToolUseFailure and the exploration hook | a failed read |
| 9 | `subagent-read` | a background subagent Read in the child lane | a subagent read |

Every case matches its expected outcome, and every replay subtest
passes, also "the audit records name turns and lanes". Each edit reply
hangs off its tool call in the transcript, so only its blob in
`hook-context.jsonl` shows that it reached a request. The exploration
replies of runs 1, 4, 5, 8, and 9 are also in the chain of the next
assistant record.

The case after the compaction is optional. It shows the reason for the
reset: advice that the context lost in the compaction comes back. The
file of that case is new to run 4, so the compaction cannot restore it,
and the model must read it before the Edit.

## Sanitization

The sanitizer replaced each local value with the placeholder that the
`placeholders` field of `manifest.json` names: the scratch repository,
the home directory, the user name, the Seamark binary, the
instrumentation directory, the Claude Code task directory, the account
and organization identifiers, and the encoded bytes of an attachment.
It replaced system prompts, request copies, and generated context with
the marker `<sanitized>`. A scratch directory name in model text
becomes `native-scratch`. No such name reached model text in this
recording, so the word occurs only as the manifest key.

An ID pass replaced each ID that Claude Code or its API generated: the
session, prompt, agent, tool use, transcript record, message, request,
and task-notification run IDs. Each real ID has one synthetic value
with the shape and the length of the real ID, the same in every file
and file name. So the links between the payloads, the transcripts, and
the subagent files stay. `synthetic_ids` in `manifest.json` counts the
values of each kind (332 in all). The map from the real IDs to the
synthetic IDs stays outside the repository, because it holds the real
IDs.

Prompts, tool input, and tool output stay as recorded, apart from the
placeholders and the synthetic IDs. The compaction summary in
`hooks/017-PostCompact.json`, the summarizer reply in
`hooks/015-SubagentStop.json`, and the summary record of the main
transcript are model text about runs 1 and 2. They name the fixture
file `timing/prior.txt` and the code word `cobalt-heron`, and no
personal data.

Privacy check before the copy landed: a scan of every file found no
home path, user name, scratch directory name, e-mail address,
credential text (`sk-ant`, `api_key`, `bearer`), or real ID. The
thinking blocks of the transcripts hold no text.

## Files

| File | Holds |
| --- | --- |
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the 68 hook payloads by run and event, the transcripts, and the extra files |
| `hooks/` | the 68 hook payloads in the order of the observer |
| `projects/` | the 8 transcript files of the 6 sessions, with one subagent transcript and its meta file |
| `timing-cases.jsonl` | the case name of each run |
| `lessons-audit.jsonl` | the 13 audit records: 11 hook records, 1 reset record, and 1 preparation record |
| `hook-context.jsonl` | the 10 hook replies, each as a `hook_success` record, a hook record, and the blob of the request that holds it |
| `advisor.jsonl` | the advisor state of each run (`off` in all 9) |

Earlier recordings of this scenario on Claude Code 2.1.314 stay outside
the repository. The client updated itself to 2.1.319 before this
recording, so this copy is the evidence.
