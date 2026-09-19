# Codex hook fixtures

Provenance: shaped from the Codex hooks reference
(https://developers.openai.com/codex/hooks/, which redirects to
https://learn.chatgpt.com/docs/hooks; consulted 2026-09-13) and from the
`apply_patch` grammar embedded in the installed CLI (`codex-cli 0.154.0`,
Homebrew cask; the grammar text is recorded in
`rfc/validation/seamark-agent-integrations-compatibility.md`). Values are
**synthetic**: session, turn, and call ids are placeholders, and no file
here is a raw capture from a live Codex session. A fixture is promoted to
"confirmed" only when a recorded native run reproduces its shape.

Documented facts the fixtures encode:

- `hook_event_name`, `session_id`, `cwd`, `transcript_path`, `model`, and
  `permission_mode` are common to every event; turn-scoped events add
  `turn_id`.
- File edits arrive as `tool_name: "apply_patch"` with the patch text in
  `tool_input.command`. The matcher accepts `apply_patch`, `Edit`, or
  `Write`; the payload still says `apply_patch`.
- Shell commands arrive as `tool_name: "Bash"` with `tool_input.command`.
- Subagent hooks report the **parent** session id; `PreToolUse` carries no
  subagent identity. Suppression therefore has no reliable receiving
  context on this surface.
- `PostCompact` and `SessionStart` with `source: "compact"` both run after
  compaction of a root session.
- Hook `timeout` is seconds (default 600). `additionalContextLimit`
  bounds model-visible context (default about 2,500 tokens).

Files:

- `pre_tool_use_apply_patch_add.json`: one new file.
- `pre_tool_use_apply_patch_update.json`: one in-place update.
- `pre_tool_use_apply_patch_delete.json`: one deletion.
- `pre_tool_use_apply_patch_move.json`: update with `*** Move to:`.
- `pre_tool_use_apply_patch_multi.json`: add, update, move, and delete
  in one patch.
- `pre_tool_use_apply_patch_repeated.json`: the same path in two hunks.
- `pre_tool_use_apply_patch_nested_cwd.json`: relative paths with the
  event `cwd` in a subdirectory of the workspace.
- `pre_tool_use_apply_patch_outside.json`: a path that escapes the
  workspace and an absolute path; the decoder must report them, never
  silently drop or accept them.
- `pre_tool_use_apply_patch_malformed.json`: a truncated envelope; the
  decoder must return an explicit invalid result, not a partial set.
- `pre_tool_use_apply_patch_end_of_file.json`: a hunk with the
  `*** End of File` marker.
- `pre_tool_use_bash.json`: the shell event the gate matches.
- `post_compact.json`, `session_start_compact.json`: reset lifecycle.
- `subagent_start.json`: shows the separate `agent_id`; not consumed by
  Seamark today.
- `hooks.seamark.json`: the `.codex/hooks.json` entries the Codex setup
  adapter is expected to generate (proposed shape; binary path is a
  placeholder; confirmed only by a native check).
