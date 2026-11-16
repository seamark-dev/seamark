# Native Claude Code guard recording: Claude Code 2.1.319

This directory holds a **native** recording, not synthetic fixtures.
Claude Code 2.1.319 (`claude --version`, macOS) ran the 24 prompts of
the guard scenario on 2026-11-07 (UTC). The replay tests
`TestNativeRecordClaudeGuardRecording` and
`TestNativeRecordClaudeGuardExtension` read the recording offline
(`make native-replay CLIENT=claude`), and
`TestGuardEvidenceMatchesRecordings` checks the guard evidence registry
against it in every test run. No test runs a client.

The timing recording in `../timing` comes from the same commit and the
same client version.

## How it was recorded

`make native-record CLIENT=claude NATIVE_RECORD_SCENARIO=guard` ran
`scripts/native-record.sh` and the sanitizer
`scripts/native-record-sanitize.py` at commit `b799ccb` of the release
branch, with the Seamark binary of that commit. The binary reports
`v0.8.0-143-g0cebfa8`, an ID of the same tree on the development
branch.
The script made a new scratch Git repository with enforce pins on
`sentinel/` and `notes/`, and ran each prompt with `claude -p` on the
model `haiku` (`claude-haiku-5-5` in every result). It turned off the
server-side advisor tool, and the automatic update of the client was
off. Each run had a cost limit of 0.40 USD.

The hook document of the scratch repository held:

- the hooks of `seamark init --lesson-checks enforce`: `gate --hook` on
  Bash, `lessons --hook` on Edit, Write, and MultiEdit,
  `lessons --hook-reset` on PostCompact, `lessons --guard --hook` on
  PreToolUse, and `lessons --verify --hook` on PostToolUse and
  PostToolUseFailure. It held no capture hook.
- test instrumentation: an observer entry that copies each payload, and
  the indirection entry `sh /opt/instrumentation/pre-write.sh`. The
  phase sets what the indirection entry does.

The hook timeouts were 10 s, and 15 s on `gate --hook` and on the two
`lessons --verify --hook` entries.

Each case runs in one phase:

| Phase | Setup |
| --- | --- |
| `enforce` | the hooks of `init --lesson-checks enforce` |
| `check-pins` | the same hooks; both pins have mode `check` |
| `broken-config` | the same hooks; one check kind does not exist |
| `check-installed` | the hooks of `init --lesson-checks check` |
| `post-only` | no pre-write hook; the post-edit hooks stay |
| `wrapper` | no owned guard; the indirection entry runs the guard and discards its exit status |
| `slow` | no owned guard; the indirection entry waits 15 s, past the 10 s hook timeout, and then runs the guard |
| `slow-deny` | no owned guard; the indirection entry waits 7 s, inside the hook timeout, and then runs the guard |

`guard-capability.jsonl` also holds a `wrapper-inline` report, which no
run uses.

## Cases

A child case starts a subagent in the launch shape that its prompt
sets. In each of the nine child cases the observed shape equals the
requested shape.

| Run | Case | Phase | Lane, shape | Shows | Replay |
| --- | --- | --- | --- | --- | --- |
| 1 | `edit-violation` | `enforce` | root | an Edit in the sentinel scope is denied before it writes | pass |
| 2 | `write-violation` | `enforce` | root | a Write in the sentinel scope is denied before it writes | pass |
| 3 | `multiedit-violation` | `enforce` | root | no MultiEdit call; the reply is `absent` | skip |
| 4 | `subagent-violation` | `enforce` | child, foreground | a child Write is denied | pass |
| 5 | `unknown-edit` | `enforce` | root | the client refuses the Edit in its input validation, before any hook | skip |
| 6 | `n1-absent-edit` | `enforce` | root | one Edit with an empty `old_string` creates an absent file outside every pin | pass |
| 7 | `permitted-write` | `enforce` | root | a permitted Write completes, and its post-edit check passes | pass |
| 8 | `config-error` | `broken-config` | root | an invalid check set denies a Write outside every pin | pass |
| 9 | `check-only` | `check-pins` | root | check pins never block; the post-edit reply reports the violation | pass |
| 10 | `check-installed` | `check-installed` | root | the check mode stores a baseline and never blocks | pass |
| 11 | `failure-reach` | `post-only` | root | the client refuses the Edit in its input validation, before any hook | skip |
| 12 | `report-only-wrapper` | `wrapper` | root | the guard denies, and the Write runs | pass |
| 13 | `slow-guard` | `slow` | root | the client stops the pre-write hook at 10 s and lets the Write run | pass |
| 14 | `subagent-edit-violation` | `enforce` | child, foreground | a child Edit is denied | pass |
| 15 | `subagent-multiedit-violation` | `enforce` | child, foreground | no MultiEdit call; the reply is `absent` | skip |
| 16 | `subagent-check-only` | `check-pins` | child, background | the post-edit reply reaches the child request | pass |
| 17 | `slow-deny-write` | `slow-deny` | root | the client waits for the slow guard and honors its deny of a Write | pass |
| 18 | `slow-deny-edit` | `slow-deny` | root | the same for an Edit | pass |
| 19 | `slow-deny-multiedit` | `slow-deny` | root | no MultiEdit call; the reply is `absent` | skip |
| 20 | `slow-deny-subagent-write` | `slow-deny` | child, background | the same for a child Write | pass |
| 21 | `slow-deny-subagent-edit` | `slow-deny` | child, background | the same for a child Edit | pass |
| 22 | `slow-deny-subagent-multiedit` | `slow-deny` | child, background | no MultiEdit call | skip |
| 23 | `execution-failure` | `post-only` | root | a Write fails with `EACCES` after PreToolUse; PostToolUseFailure reaches the verify hook | pass |
| 24 | `slow-deny-subagent-write-foreground` | `slow-deny` | child, foreground | the client waits for the slow guard of a child Write; the parent waits for the child | pass |

Each deny and each post-edit reply is in the chain of the next
assistant record of its transcript.

### Known skips

- MultiEdit, root and child (runs 3, 15, 19, 22): Claude Code 2.1.319
  offered no MultiEdit tool to the model, and the model made no
  MultiEdit call. The negative reply word is the plain word `absent`,
  which the model spells reliably. In run 22 only the last text of the
  child holds `absent`, so the recorded reply is empty. The MultiEdit
  path keeps no native evidence. Claude Code 2.1.325 has no MultiEdit
  tool either. `TestNativeClaudeOffersNoMultiEditTool`
  (`make agents-native-check CLIENT=claude`) runs that client against a
  scripted loopback Messages API, with no model. It offers no MultiEdit
  tool, inline or deferred, and answers a scripted MultiEdit call with
  `No such tool available: MultiEdit`.
- An Edit that fails (runs 5 and 11): the client refuses the call in
  its input validation, before any hook, so no PreToolUse or
  PostToolUseFailure payload exists. The Write failure path has its own
  case (run 23).

### Replies

The one-word reply of each case is a corroboration only. The proof of
reach is the chain and the blob. Three cases have no one-word reply:

- `execution-failure` (run 23): the parent reply was not one word. The
  hook chain and the blob in `guard-context.jsonl` show that the reply
  of the verify hook reached the model.
- `edit-violation` (run 1): the last text of the model holds `blocked`
  and the rule name `frozen-sentinel`, but it is not one word. The deny
  is in the chain of that text.
- `slow-deny-subagent-multiedit` (run 22): see the known skips.

### Optional case

The case `slow-deny-subagent-write-foreground` (run 24) is optional. It
gives the Write child an in-budget slow deny in the foreground launch
shape too, beside the background shape of run 20. The Edit child has
the in-budget slow deny in the background shape only (run 21).

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
values of each kind (1,063 in all). The map from the real IDs to the
synthetic IDs stays outside the repository, because it holds the real
IDs. `guard-identities.jsonl` joins each payload to its audit record by
digests. It holds no raw session, agent, or tool-use ID.

Prompts, tool input, and tool output stay as recorded, apart from the
placeholders and the synthetic IDs.

Privacy check before the copy landed: a scan of every file found no
home path, user name, scratch directory name, e-mail address,
credential text (`sk-ant`, `api_key`, `bearer`), or real ID. The
thinking blocks of the transcripts hold no text.

## Files

| File | Holds |
| --- | --- |
| `manifest.json` | the client version, the placeholders, the synthetic ID counts, the 182 hook payloads by run and event, the transcripts, and the extra files |
| `hooks/` | the 182 hook payloads in the order of the observer |
| `projects/` | the 40 transcript files of the 24 sessions, with 8 subagent transcripts and their meta files |
| `guard-cases.jsonl` | per run: the case, the phase, the fixture state before and after, the audit range, the lines of the indirection entry, and the one-word reply |
| `lesson-check-audit.jsonl` | the 24 lesson-check audit records |
| `guard-identities.jsonl` | the 18 identity rows that join a payload to its audit record |
| `guard-context.jsonl` | the deny and post-edit reply records of the transcripts |
| `guard-hooks.jsonl` | the status and the duration of each guard, baseline, post-edit, and indirection command, from the client debug log |
| `guard-capability.jsonl` | the 13 lesson-check capability reports of `seamark status --json`, one each time a phase starts |
| `advisor.jsonl` | the advisor state of each run (`off` in all 24) |

The slow-guard case shows what the client does past its hook timeout.
Claude Code 2.1.319 stopped the pre-write hook after 10 s ("timed out
after 10000ms") and let the Write run. The in-budget slow cases show
that the client waits for a slow guard inside the timeout and honors
its deny, in the root and the child context.

Earlier recordings of this scenario on Claude Code 2.1.314 stay outside
the repository. The client updated itself to 2.1.319 before this
recording, so this copy is the evidence.
