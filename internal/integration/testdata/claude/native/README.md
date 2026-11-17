# Native Claude Code hook payloads: Claude Code 2.1.300

This directory holds **native** hook payloads, not synthetic fixtures.
Claude Code 2.1.300 (`claude --version`, macOS) sent each payload to a
command hook on 2026-10-15. The decoder tests read them offline, so the
decoders keep a native shape as evidence. No test runs a client.

## How they were recorded

A hook observer in a scratch Git repository copied each payload from
stdin, for 12 events. The observer was test instrumentation: its own hook
entry in `.claude/settings.json`, beside the hooks of a Seamark
development build. A sanitizer then replaced the local values that the
first table of [Sanitization](#sanitization) names. Before the release,
a second pass replaced the IDs that Claude Code generated and two local
values that the sanitizer left (the second table there).

| Payloads | Surface | Model |
| --- | --- | --- |
| `001` to `020` | two print-mode runs, `claude -p --output-format json` with `--setting-sources project,local --strict-mcp-config`, so the operator's own settings, hooks, plugins, and MCP servers stay out | `haiku` (Haiku 5.5) |
| `021` to `027` | one interactive session in a terminal, permission mode `auto` | the operator's default model (`claude-opus-5-5` in `021`) |

Print-mode run 1 had one prompt on stdin, `--allowedTools Bash Agent
Task`, and a custom `probe` subagent on `haiku` through `--agents`:

```text
Do exactly these steps in order, one tool call per step, and do not add steps.
1. Run the Bash command: true
2. Run the Bash command: false
3. Run the Bash command: cd sub
4. Run the Bash command: pwd
5. Use the Agent tool with subagent_type "probe" and ask it to run the Bash command: pwd
Then reply with the single word: done
```

Print-mode run 2 was `/compact` on stdin with `--resume <session>` of
run 1.

In the interactive session, the operator typed one prompt and ended the
session with `/exit`. The leading space is part of the typed text.
`sample.png` is an 8 × 8 red PNG of 75 bytes in the scratch repository.

```text
 Read the file sample.png and tell me its size
```

## Sanitization

The sanitizer of the recording replaced these local values:

| Recorded value | Placeholder |
| --- | --- |
| the scratch repository | `/workspace/native` (project directory `-workspace-native`) |
| the home directory | `/home/native` |
| the Claude Code task directory (`/private/tmp/claude-<uid>/…`) | `/tmp/claude-tasks/-workspace-native` |
| the user name as a word of its own, for example the owner column of `ls -l` | `native` |
| the encoded bytes of an image | `c2FuaXRpemVk`, the base64 of `sanitized` |

A second pass before the release replaced these values:

| Recorded value | Placeholder |
| --- | --- |
| the 2 session IDs | `10000000-0000-4000-8000-000000000001` and `…0002` |
| the 5 prompt IDs | `20000000-0000-4000-8000-000000000001` to `…0005` |
| the 3 agent IDs | `a0000000000000001` to `a0000000000000003` |
| the 8 tool use IDs | `toolu_01NativeToolUse000000001` to `…0008` |
| the scratch repository name in the shortened path of the compaction summary (`017`, `019`), which the sanitizer did not match | `native`, so the path reads `.../native/sub` like the sanitized `pwd` output |
| the local time of day in the `ls -l` output (`024`) | `00:00` |

Each synthetic ID has the shape of the ID that it replaces. One recorded
ID gives the same synthetic ID in every file, also inside a path, a
background task, and the tags of the `012` prompt. So the links between
the payloads stay: a subagent payload names the session and the prompt
of its parent. The numbers follow the first use in file order. Prompts,
tool input, and tool output stay as recorded, apart from the
placeholders of the two tables. The interactive payloads were numbered
`008` to `014` in the sanitizer output. Here they follow the print-mode
payloads. The new numbers changed no byte of a payload.

Privacy review, 2026-11-06: each file was read before the release. No
home path, user name, e-mail address, account or organization ID,
credential, or ID that Claude Code generated for the recording remains.
Two payloads (`017`, `019`) hold the compaction summary that the model
wrote about run 1. It names the shortened scratch path `.../native/sub`
and no personal data.

## Files

| File | Shows |
| --- | --- |
| `hooks/001-SessionStart.json` | `source: startup` |
| `hooks/002-UserPromptSubmit.json` | the scripted prompt and its `prompt_id` |
| `hooks/003-PostToolUse.json` | Bash `true`: success, `tool_response` with `interrupted: false` |
| `hooks/004-PostToolUseFailure.json` | Bash `false`: `error: "Exit code 1"`, `is_interrupt: false` |
| `hooks/005-PostToolUse.json` | Bash `cd sub`; from here on the hook process starts in `sub` |
| `hooks/006-PostToolUse.json` | Bash `pwd` |
| `hooks/007-SubagentStart.json` | `agent_id`, `agent_type: probe` |
| `hooks/008-PostToolUse.json` | the Agent call: `tool_response` with `status: async_launched` and `agentId` |
| `hooks/009-PostToolUse.json` | Bash `pwd` inside the subagent: `agent_id`, `agent_type`, and the parent `session_id`, `prompt_id`, and `transcript_path` |
| `hooks/010-Stop.json` | `last_assistant_message: done`; `background_tasks` names the running child |
| `hooks/011-SubagentStop.json` | the child's stop: `agent_transcript_path` and its last message |
| `hooks/012-UserPromptSubmit.json` | the child's result as a `<task-notification>` prompt: the hook cannot tell it from a typed prompt |
| `hooks/013-Stop.json` | the answer to the notification |
| `hooks/014-SessionEnd.json` | `reason: other` |
| `hooks/015-SessionStart.json` | run 2: `source: resume` |
| `hooks/016-PreCompact.json` | `trigger: manual`, `custom_instructions: null` |
| `hooks/017-SubagentStop.json` | the compaction summarizer: `agent_type: ""` and no SubagentStart |
| `hooks/018-SessionStart.json` | `source: compact` |
| `hooks/019-PostCompact.json` | `trigger: manual` and `compact_summary` |
| `hooks/020-SessionEnd.json` | `reason: other`, with the `prompt_id` of the compaction |
| `hooks/021-SessionStart.json` | the interactive session: `source: startup` and the `model` |
| `hooks/022-UserPromptSubmit.json` | the typed prompt with its leading space |
| `hooks/023-PostToolUse.json` | `Read` on `sample.png`: `tool_response` is `{type: image, file: {base64, type: image/png, originalSize: 75, dimensions}}`. It is the only native `Read` payload. |
| `hooks/024-PostToolUse.json` | Bash `ls -l … && file … && sips …`: success; the owner column holds the user placeholder, and the time column the time placeholder |
| `hooks/025-Stop.json` | the answer, with `effort` and empty `background_tasks` |
| `hooks/026-SubagentStop.json` | an agent with `agent_type: ""`, no SubagentStart, and no `last_assistant_message` |
| `hooks/027-SessionEnd.json` | after `/exit`: `reason: prompt_input_exit`, with the `prompt_id` of the command |

## Not shown here

An interrupted tool, a permission denial, `StopFailure`, a session that
moves into a worktree, automatic compaction, a `PreToolUse` payload, a
document result (`Read` on a PDF), and a subagent of an interactive
session.
