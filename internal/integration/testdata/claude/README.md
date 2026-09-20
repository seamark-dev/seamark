# Claude Code hook fixtures

Provenance: shaped from the Claude Code hooks reference
(https://code.claude.com/docs/en/hooks) and from the payload fields
Seamark's existing Claude hook already reads (`session_id`, `tool_name`,
`tool_input.file_path`, `tool_use_id`). Local CLI at capture time:
Claude Code 2.1.270. Values are **synthetic**: paths, session ids, and
transcript paths are placeholders. No fixture is a raw capture from a
live session.

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
  subagent reports; a native parent/subagent run is pending.
- `pre_tool_use_bash.json`: the shell event the gate hook matches.
- `post_compact.json`: the lifecycle event the reset hook matches.
- `post_compact_subagent.json`: a PostCompact with `agent_id`. The
  reference does not document a compaction event for a subagent. The
  fixture exists so the reset decoder is tested against it: such an event
  must never reset the parent context.
- `settings.hooks.json`: what `seamark init` writes into
  `.claude/settings.json` for a warn-mode install (binary path is a
  placeholder). Timeouts are seconds.
