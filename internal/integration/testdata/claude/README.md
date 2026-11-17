# Claude Code hook fixtures

Provenance: shaped from the Claude Code hooks reference
(https://code.claude.com/docs/en/hooks) and from the payload fields
Seamark's existing Claude hook already reads (`session_id`, `tool_name`,
`tool_input.file_path`, `tool_use_id`). Local CLI at capture time:
Claude Code 2.1.270. Values are **synthetic**: paths, session ids, and
transcript paths are placeholders. No fixture is a raw capture from a
live session. Four directories are the exceptions, and the README of
each names its provenance: `native/` holds sanitized native payloads of
Claude Code 2.1.300, `timing/` and `guard/` hold the sanitized native
timing and guard recordings of Claude Code 2.1.319, and `post-edit/`
holds the sanitized native post-edit recording of Claude Code 2.1.325.
The replay tests read the last three.

Files:

- `pre_tool_use_edit.json`, `pre_tool_use_write.json`,
  `pre_tool_use_multiedit.json`: the three edit tools the lessons hook
  matches (`Edit|Write|MultiEdit`). All carry `tool_input.file_path`.
- `pre_tool_use_edit_subagent.json`: the Edit event as it fires inside a
  subagent. The hooks reference (fetched 2026-09-20) documents two extra
  common fields there: `agent_id` ("Present only when the hook fires
  inside a subagent call") and `agent_type`. The fixture keeps the parent
  `session_id`, the case in which a session-keyed receiver hides advice
  from the subagent. The reference does not say which `session_id` a
  subagent reports. The native run of Claude Code 2.1.300 shows it:
  `native/hooks/009-PostToolUse.json`, a tool call inside a subagent,
  carries the parent `session_id` beside `agent_id` and `agent_type`.
- `pre_tool_use_edit_replace_all.json`, `pre_tool_use_multiedit_create.json`,
  `pre_tool_use_write_new_file.json`: **synthetic** inputs of the
  lesson-check guard decoder (`claude_guard.go`), shaped from the edit
  fixtures above and the tool input fields that the decoder reads
  (`replace_all`, `edits[]`); local CLI at writing time:
  Claude Code 2.1.310. They are an `Edit` with
  `replace_all: true`; a `MultiEdit` on a file that does not exist yet,
  whose first edit has an empty `old_string`; a `Write` of a new file
  inside a subagent (`agent_id`). No native run produced them. The
  native guard recording of Claude Code 2.1.319 (`guard/`, case
  `n1-absent-edit`) shows that one `Edit` with an empty
  `old_string` creates an absent file with `new_string`, so the guard
  projects such an edit as an add. No native run made a `MultiEdit`
  call, so a first `MultiEdit` step with an empty `old_string` follows
  the `Edit` result by analogy. Claude Code offers no MultiEdit tool:
  the model's replies in the 2.1.319 guard recording say so, and the
  model-free check of `make agents-native-check CLIENT=claude` shows it
  on 2.1.325.
- `pre_tool_use_bash.json`: the shell event the gate hook matches.
- `post_compact.json`: the lifecycle event the reset hook matches.
- `post_compact_subagent.json`: a PostCompact with `agent_id`. The
  reference does not document a compaction event for a subagent. The
  fixture exists so the reset decoder is tested against it: such an event
  must never reset the parent context.
- `settings.hooks.json`: what `seamark init` writes into
  `.claude/settings.json` for a warn-mode install (binary path is a
  placeholder). Timeouts are seconds.
