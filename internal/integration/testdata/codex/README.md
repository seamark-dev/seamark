# Codex hook fixtures

Provenance: shaped from the Codex hooks reference
(https://developers.openai.com/codex/hooks/, which redirects to
https://learn.chatgpt.com/docs/hooks; consulted 2026-09-13) and from the
`apply_patch` grammar embedded in the installed CLI (`codex-cli 0.154.0`,
Homebrew cask; the grammar text is recorded in
`rfc/validation/seamark-agent-integrations-compatibility.md`). Values are
**synthetic**: session, turn, and call ids are placeholders, and no file
here is a raw capture from a live Codex session. A fixture is promoted to
"confirmed" only when a recorded native run reproduces its shape. A native
run of `codex-cli 0.154.0` on 2026-09-20/21 (recorded in
`rfc/validation/seamark-agent-integrations-codex-2026-09-21/`) reproduced
the field sets of `PreToolUse` for `apply_patch` and `Bash`, of
`PostCompact`, of `SessionStart` with `source: "compact"`, of
`SubagentStart`, and of the `apply_patch` event inside a subagent. The
values here stay synthetic. In that run the model wrote every patch path
as an absolute path inside the workspace.

Documented facts the fixtures encode:

- `hook_event_name`, `session_id`, `cwd`, `transcript_path`, `model`, and
  `permission_mode` are common to every event; turn-scoped events add
  `turn_id`.
- File edits arrive as `tool_name: "apply_patch"` with the patch text in
  `tool_input.command`. The matcher accepts `apply_patch`, `Edit`, or
  `Write`; the payload still says `apply_patch`.
- Shell commands arrive as `tool_name: "Bash"` with `tool_input.command`.
- A hook that fires inside a subagent reports the **parent** session id
  and adds `agent_id` and `agent_type`; a `PreToolUse` inside a subagent
  carries both, with its own `turn_id` and `transcript_path`. The native
  run of 2026-09-21 observed this and refuted the earlier reference-derived
  claim that `PreToolUse` carries no subagent identity. The adapter reads
  no receiving context from it yet: a reset inside a subagent is
  unverified, so suppression stays off by policy, not because the event
  names no receiver.
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
- `pre_tool_use_apply_patch_subagent.json`: the edit event as it fires
  inside a subagent: the parent `session_id`, its own `agent_id` and
  `agent_type`, its own `turn_id`, and an absolute patch path. Shaped from
  the native capture. The decoder reads the same paths as for a parent
  edit and no receiver; a test pins that the identity is present and
  left unread.
- `pre_tool_use_bash.json`: the shell event the gate matches.
- `post_compact.json`, `session_start_compact.json`: reset lifecycle.
- `subagent_start.json`: shows the separate `agent_id`; not consumed by
  Seamark today. Natively, the same `agent_id` appears in the subagent's
  edit events and in `SubagentStop`.
- `apply_patch_oracle.json`: **not synthetic.** Forty patch texts and what
  `codex-cli 0.154.0` did with each one when it applied the patch offline
  (the binary run under the name `apply_patch`; no model, no credentials;
  a scratch directory; 2026-09-20): accepted or rejected, and the paths of
  its `A/M/D <path>` report. The patch decoder is tested against it: for
  every patch Codex accepts, the decoder names every file Codex touched by
  the same path. The decoder follows the parser source of that version
  (`codex-rs/apply-patch`, tag `rust-v0.154.0`), which accepts more than
  the grammar shown to the model: it trims header lines, accepts indented
  headers outside an update hunk, and accepts a shell heredoc wrapper. The
  file records parser behavior only. It is not a hook payload, and it says
  nothing about what `tool_input.command` carries in a live `PreToolUse`.
- `hooks.seamark.json`: the `.codex/hooks.json` entries the Codex setup
  adapter generates (binary path is a placeholder; confirmed only by a
  native check). A test compares the generated lesson entries with this
  file, entry for entry. The gate entry is generated by the command gate
  slice and not yet by the adapter. The adapter writes no `description`
  key: the file can hold the user's own hooks too. The file holds no
  `PostCompact` entry: the edit decoder reads no receiver yet, so a reset
  has nothing to clear, and setup installs no hook without an effect. The
  reset decoder exists and is tested against `post_compact.json`.
