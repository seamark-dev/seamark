# Native Claude Code post-edit recording: Claude Code 2.1.325

This directory holds a **native** recording, not synthetic fixtures.
Claude Code 2.1.325 (`claude --version` before and after the call,
macOS) ran the 7 prompts of the post-edit scenario on 2026-11-17 (UTC).
The replay test `TestNativeRecordClaudePostEditRecording` reads the
recording offline (`make native-replay CLIENT=claude`), and
`TestPostEditEvidenceMatchesRecordings` checks the post-edit evidence
registry and the post-edit timeout outcome against it in every test
run. No test runs a client.

The Codex post-edit recording in `../../codex/post-edit` comes from the
same commit. The timing and guard recordings in `../timing` and
`../guard` come from Claude Code 2.1.319.

## How it was recorded

`NATIVE_RECORD_SCENARIO=post-edit make native-record CLIENT=claude` ran
`scripts/native-record.sh` at commit `facb617` of the release branch,
with the Seamark binary of that commit (`v0.8.0-353-gfacb617`). The
sanitizer `scripts/native-record-sanitize.py` of a later commit of the
same branch, with the time shift for Claude Code recordings, made this
copy from the same raw recording (see "Sanitization").

The script made a new scratch Git repository in a first call without a
paid run, and the paid call reused it. It ran each prompt with
`claude -p` on the model `haiku` (`claude-haiku-5-5` in every result),
with `--setting-sources project,local` and `--strict-mcp-config`. It
turned off the server-side advisor tool (`advisor.jsonl`: `off` in all
7 runs), and the automatic update of the client was off. Each run had
a cost limit of 0.40 USD.

The fixture files and the two pins are those of the guard scenario:
a path check on `sentinel/` (rule `frozen-sentinel`) and a pattern
check on `notes/`. Both pins have mode `check`, so no case can deny a
write.

The hook document of the scratch repository held:

- the hooks of `seamark init --client claude --lesson-checks check`:
  `gate --hook` on Bash, `lessons --hook` and `lessons --baseline
  --hook` on Edit, Write, and MultiEdit, `lessons --hook-reset` on
  PostCompact, and `lessons --verify --hook` on PostToolUse and
  PostToolUseFailure. It held no capture hook.
- test instrumentation: an observer entry on each event, which copies
  each payload, and the post-edit indirection entry
  `sh /opt/instrumentation/post-edit.sh` on PostToolUse for Edit,
  Write, and MultiEdit. The phase sets what the indirection entry does.

The hook timeouts were 10 s, and 15 s on `gate --hook`, on the two
`lessons --verify --hook` entries, and on the indirection entry.

Each case runs in one phase:

| Phase | Setup |
| --- | --- |
| `check` | the hooks of `init --lesson-checks check`; the indirection entry exits at once |
| `slow-post` | no owned verify entry on PostToolUse; the indirection entry waits 12 s, inside the 15 s hook timeout, and then runs the verify command |
| `post-timeout` | no owned verify entry on PostToolUse; the indirection entry waits 20 s, past the 15 s hook timeout, and then runs the verify command |

The indirection entry logs one line before its wait and one line with
the exit status of the verify command after it (`post_edit` in
`guard-cases.jsonl`). The PostToolUseFailure verify entry stays in
every phase. No case fails, so it never fires.

## Cases

Each child case starts a subagent with `run_in_background: false`. In
each of the three child cases the observed launch shape is the
foreground shape that the prompt sets, so the parent waits for the
child inside its Agent call. The review accepted the reading of each
case.

| Run | Case | Phase | Tool, lane | Shows | Replay | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `post-edit-edit` | `check` | Edit, root | the owned verify entry runs (96 ms), and its reply reaches the next model request | pass | accepted |
| 2 | `post-edit-subagent-edit` | `check` | Edit, child | the same in the child request (97 ms) | pass | accepted |
| 3 | `slow-post-write` | `slow-post` | Write, root | the hook runs 12.21 s to the end, the next model request waits for it, and the reply reaches that request | pass | accepted |
| 4 | `slow-post-edit` | `slow-post` | Edit, root | the same (12.15 s) | pass | accepted |
| 5 | `slow-post-subagent-write` | `slow-post` | Write, child | the same in the child request (12.21 s); the parent waits in its Agent call | pass | accepted |
| 6 | `slow-post-subagent-edit` | `slow-post` | Edit, child | the same (12.22 s) | pass | accepted |
| 7 | `post-timeout-write` | `post-timeout` | Write, root | the client stops the hook at 15 s, before the verify command; the reply is lost, and the file stays written | pass (stopped) | accepted |

Each Edit case read the file first and then made one Edit. Each Write
case made one Write. No case made another tool call, a retry, or a
MultiEdit call.

### Replies

Each of the six replies is a `hook_additional_context` attachment of
PostToolUse. It is in the `parentUuid` chain of the next assistant
record, with exactly one `api-request` record between, and the kept
request blob of the same session holds the reply header
`seamark post-edit lesson checks after this <tool> call: violation`.
`guard-context.jsonl` holds the six triples: `hook_success`,
`hook_additional_context`, and the request blob.

The one-word reply of each case is a corroboration only. All six
answered `frozen-sentinel`, the check ID that the prompt asks for. The
advisory reminder of the same call also holds that word, so the word
alone does not prove reach. The chain and the blob prove it.

### Slow hooks (runs 3 to 6)

The PostToolUse hooks of one call run in parallel. The `tool_result`
record and the post-edit attachments carry the time at which the
slowest hook ended, and the next `api-request` follows. So Claude Code
2.1.325 holds the next model step for the whole slow hook, about 12.2 s.
The debug log gives 12211, 12150, 12206, and 12216 ms for the
indirection entry (`guard-hooks.jsonl`). In each child case the parent
Agent call returned after the last model step of the child.

### The stop at 15 s (run 7)

The debug log names the stop: the indirection entry `timed out after
15000ms (15035ms)`, and the hook was cancelled (the `note` in
`guard-hooks.jsonl`). The indirection entry logged only
`post-timeout start`, so it never ran the verify command, and the audit
holds no post-edit record of the call. The file was written. The
`hook_cancelled` attachment has no rendered text, and the next request
blob holds the advisory context only: no notice of the stop and no
post-edit reply. The model answered `none`. So the client drops the
post-edit findings after its 15 s hook timeout, and the edit stays.

The baseline of that call stays in `.seamark/checks/pending/` of the
scratch repository, because no post-edit check read it. A later hook
call or `seamark lessons --verify` discards a pending baseline that is
older than `pending-max-age` (1 h).

### Not in this recording

- No case fails, so no PostToolUseFailure payload exists. The failure
  reply keeps the proof of the Claude Code 2.1.319 guard recording
  (`../guard`, run 23), and no recording has a slow or a stopped hook
  on PostToolUseFailure.
- No case stops a hook at 15 s in a child context or for an Edit.
- No MultiEdit case: Claude Code 2.1.325 offers no MultiEdit tool
  (`TestNativeClaudeOffersNoMultiEditTool`,
  `make agents-native-check CLIENT=claude`).

### What the recorded reports show

The capability reports come from the binary of the recording, which
had no post-edit evidence. In the `check` phase every post-edit path
reads `active-unverified` with the evidence `unverified`. The slow
phases add `partial-install`, because the PostToolUseFailure verify
entry stays without its PostToolUse pair. The Write and Edit pre-write
paths read `active` with the Claude Code 2.1.319 evidence and its 10 s
limit, and the MultiEdit pre-write paths read `active-unverified`. The
replay checks that no report claims enforcement without evidence.

The evidence registry of the current tree reads this copy. The
post-edit evidence of Write and Edit in the root and the child context
reads verified on Claude Code 2.1.325, with the limit "the client drops
the post-edit findings after its 15 s hook timeout; the edit stays".
Each of these paths rests on its in-budget slow case. The stop at 15 s
of run 7 gives the limit. The MultiEdit post-edit paths stay pending.
The claim covers PostToolUse only.

## Sanitization

The sanitizer replaced each local value with the placeholder that the
`placeholders` field of `manifest.json` names: the scratch repository,
the home directory, the user name, the Seamark binary, the
instrumentation directory, the Claude Code task directory, the account
and organization identifiers, and the encoded bytes of an attachment.
It replaced system prompts, request copies, and generated context with
the marker `<sanitized>`. Only `guard-context.jsonl` keeps the text of
the post-edit replies and the request blobs, as the script copied them
from the transcripts.

An ID pass replaced each ID that Claude Code or its API generated: the
session, prompt, agent, tool use, transcript record, message, and
request IDs. Each real ID has one synthetic value with the shape and
the length of the real ID, the same in every file and file name. So the
links between the payloads, the transcripts, and the subagent files
stay. `synthetic_ids` in `manifest.json` counts the values of each kind
(351 in all). The map from the real IDs to the synthetic IDs stays
outside the repository, because it holds the real IDs.
`guard-identities.jsonl` joins each payload to its audit record by
digests. It holds no raw session, agent, or tool-use ID.

Every recorded time moved by one constant offset in whole seconds, so
the copy names no real time of day: the ISO times of the transcripts,
the context records, and the audit records, the time in the debug log
line of `guard-hooks.jsonl`, and the `startTime` epoch numbers. The
first recorded time of the copy reads 21:00:00 UTC. Every duration and
the order of every record stay exact, so the durations above are the
recorded ones. `times_shifted` in `manifest.json` marks the shift.

Prompts, tool input, and tool output stay as recorded, apart from the
placeholders and the synthetic IDs.

Privacy check before the copy landed: a scan of every file found no
home path, user name, scratch directory name, e-mail address,
credential text, local time offset, time zone name, real ID (none of
the 358 IDs collected from the raw inputs), or time between 08:00 and
21:00 UTC.

## Files

| File | Holds |
| --- | --- |
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the time shift flag, the 62 hook payloads by run and event, the transcripts, and the extra files |
| `hooks/` | the 62 hook payloads in the order of the observer |
| `projects/` | the 13 transcript files of the 7 sessions, with 3 subagent transcripts and their meta files |
| `guard-cases.jsonl` | per run: the case, the phase, the fixture state before and after, the audit range, the lines of the post-edit indirection entry, and the one-word reply |
| `lesson-check-audit.jsonl` | the 13 lesson-check audit records: a baseline and a post-edit record for runs 1 to 6, and a baseline for run 7 |
| `guard-identities.jsonl` | the 7 identity rows that join a payload to its audit records |
| `guard-context.jsonl` | the 18 records of the six post-edit replies |
| `guard-hooks.jsonl` | the status and the duration of each baseline, verify, and indirection command, from the client debug log, and the note of the stopped hook |
| `guard-capability.jsonl` | the 3 lesson-check capability reports of `seamark status --json`, one each time a phase starts |
| `advisor.jsonl` | the advisor state of each run (`off` in all 7) |

Claude Code writes the hooks of a subagent into the debug log of the
parent session, so `guard-hooks.jsonl` holds the results of a child
call under the run of its parent.

## Record again

A new recording needs the operator's own login and costs tokens.

1. `NATIVE_RECORD_SCENARIO=post-edit make native-record CLIENT=claude`
   from a committed tree sets up a new scratch repository, runs the
   paid prompts, and writes the raw recording and a sanitized copy into
   its output directory. It never writes into testdata. With
   `NATIVE_RECORD_RUNS=0` it sets up the repository without a paid run,
   and `NATIVE_RECORD_REPO=<repository>` reuses it later.
2. Review the copy case by case: the chain and the blob of each reply,
   and the debug log of each slow hook. To check the ID pass, run the
   sanitizer again with `--id-map <file>` outside the repository.
3. Replace this directory with the reviewed copy, update this README,
   and run `make native-replay CLIENT=claude`. A new client version
   needs new registry entries (`claudePostEditEvidenceVersion`) and a
   new post-edit timeout outcome.
