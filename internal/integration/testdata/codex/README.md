# Codex hook fixtures

Provenance: shaped from the Codex hooks reference
(https://developers.openai.com/codex/hooks/, which redirects to
https://learn.chatgpt.com/docs/hooks; consulted 2026-09-13) and from the
`apply_patch` grammar embedded in the installed CLI (`codex-cli 0.154.0`,
Homebrew cask). Values are **synthetic**: session, turn, and call ids are
placeholders, and no file here is a raw recording from a live Codex session.
A fixture is promoted to "confirmed" only when a recorded native run
reproduces its shape. A native run of `codex-cli 0.154.0` on
2026-09-20/21 reproduced the field sets of `PreToolUse` for `apply_patch`
and `Bash`, of `PostCompact`, of `SessionStart` with `source: "compact"`,
of `SubagentStart`, and of the `apply_patch` event inside a subagent. The
values here stay synthetic. In that run the model wrote every patch path
as an absolute path inside the workspace.

The native recordings in `timing/` and `guard/` are **not synthetic**
(codex-cli 0.169.0, 2026-11-16; the README in each directory gives the
provenance). They show the same field sets of `PreToolUse` and
`PostToolUse` for `apply_patch` and `Bash`, of `SubagentStart`, and of
the `apply_patch` event inside a worker. Natively, `tool_use_id` reads
`exec-<UUID>`: the ID of the item that the call made, not the `call_id`
of the code-mode `exec` call that ran the tool. The fixtures keep the
placeholder form `call_synthetic_NNNN`. In those recordings the model
wrote most patch paths relative to the workspace. Only two Update
patches, of a timing file and of a sentinel file, used an absolute
path, so the decoders must read both forms. A worker also gets a
`UserPromptSubmit` with its `agent_id`, and its `SubagentStart` comes
before the `PreToolUse` of the parent's wait call.

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
  the receiving context from `session_id` and `agent_id`: the root lane
  without `agent_id`, the child lane with it. No reset of a Codex context
  is verified, so no context is resettable and suppression stays off by
  policy, not because the event names no receiver. The edit decoder also
  reads `turn_id` as the turn.
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
  edit and names the child lane, which is not resettable; a test pins
  the lane, the turn, and that suppression stays off.
- `pre_tool_use_bash.json`: the shell event the gate matches. The command
  codec reads `tool_input.command` from a `Bash` event only; an event of
  another tool, `apply_patch` included, is not applicable, so a patch is
  never parsed as a shell command. The Codex hooks reference (consulted
  2026-09-26) documents exit 2 with stderr as a block, `permissionDecision:
  "ask"` as parsed but unsupported, and plain text on stdout as ignored;
  the codec uses the exit protocol. No native run has observed the
  generated gate hook block a command yet.
- `post_compact.json`, `session_start_compact.json`: reset lifecycle.
- `post_tool_use.json`: **synthetic**. An `apply_patch` result whose
  `tool_response` is the exec header text with exit code 0.
- `post_tool_use_nonzero_exit.json`: **synthetic**. A `Bash` result whose
  `tool_response` is an object with `exit_code: 1`. It pins a decoder
  rule; the 0.157.1 source sends no such object for a shell command.
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
  The `content` part of the same file is also **not synthetic**: schema
  `apply-patch-content/1`, recorded from `codex-cli 0.169.0` on
  2026-11-16 by `make agents-oracle-record ORACLE_VERSION=0.169.0`
  (`native_codex_content_oracle_test.go`). It holds 95 cases. The first
  recording of cases 01 to 29, from `codex-cli 0.164.0` on 2026-11-03,
  gave the same exit status, stdout, stderr, and after files for every
  case. Each case holds the before files, the patch text, and what the installed applier did with them,
  offline in a new scratch directory with a scratch `CODEX_HOME`: the
  exit status, stdout, stderr (the scratch directory written as
  `<scratch>`), and every file after the run, also after a refusal. The
  shapes are one and many hunks, `*** End of File`, a before file
  without a final newline, CRLF files and patches, repeated context with
  and without an `@@` anchor, `*** Move to` with a hunk, extra
  whitespace in headers, a context that does not match, a refusal after
  a good file, fuzzy context (trailing spaces, an en dash), Add with and
  without a final newline, and Delete. Cases 30 to 95 pin each rule of
  the Update projection: context lines that keep the file bytes, the
  line end of new lines, the final newline, pure insertions, the order
  of the match passes, anchors with spaces or Unicode quotes,
  `*** End of File`, the retry
  without a trailing empty line, the chunk order and its refusals,
  grammar refusals, a byte order mark, lone CR files, one file named
  twice, a move onto an existing CRLF file, line separators inside a
  line, and every character of the normalization table with one
  unmapped neighbor per class. What the cases prove: the bytes
  that this applier version writes for these inputs. For example, it
  writes a final newline, it keeps CRLF line ends, it changes the first
  match of repeated context, and a refusal in a later file keeps the
  earlier files written. What they do not prove: what
  `tool_input.command` carries in a live `PreToolUse`, how the Codex
  tool call applies a patch inside a session, or any other version. The
  guard recording in `guard/` holds one live Update from a real Codex
  session (case `update-permitted`): the one-hunk shape of case
  `01_one_hunk`, on codex-cli 0.169.0, the version of the oracle.
  `make agents-native-check`
  replays every content case and skips with the reason when the
  installed version differs from the recorded one. A different version
  never rewrites the file by itself: the recorder runs only when
  `ORACLE_VERSION` names the installed version, and it keeps the bytes
  of the path cases. Every file in a case is UTF-8 text, so a JSON
  string holds its exact bytes.
- `apply_patch_path_control_oracle.json`: **not synthetic.** Schema
  `apply-patch-path-controls/1`, 28 cases recorded from `codex-cli
  0.169.0` on 2026-11-16 by `make agents-oracle-record
  ORACLE_VERSION=0.169.0` (`native_codex_path_control_oracle_test.go`), in the
  form of the content cases: the before files, the patch text, the exit
  status, stdout, stderr, and every file after the run, offline in a new
  scratch directory with a scratch `CODEX_HOME`. Each case puts a control
  or space character into a header path, or into added content. What the
  cases prove for this applier version: it removes a tab and a carriage
  return from an Add, Update, Delete, or Move to path when it writes, at
  any position inside the path, while its report still names the path
  with the character; it keeps a vertical tab, a form feed, U+0001, an
  escape, DEL, NEL, a no-break space, U+2028, and a zero-width space in
  the written path; a tab or a carriage return at the end of a header
  line goes in the line trim; a line feed ends the header line; and added
  content keeps a tab and a carriage return. The guard decoder is tested
  against it offline: it refuses a header path with a tab or a carriage
  return, and for every other case it names every file that the applier
  changed. `make agents-native-check` replays the cases byte for byte
  only on the recorded version, and on any version checks the same rule
  over the cases and a generated sweep of control and space characters.
  The cases say nothing about what `tool_input.command` carries in a
  live `PreToolUse`.
- `hooks.seamark.json`: the `.codex/hooks.json` entries the Codex setup
  adapter generates in warn mode (binary path is a placeholder; the lesson
  entry was confirmed by the native run of 2026-09-21, the gate entry by
  no native run yet). A test compares the generated gate and lesson
  entries with this file, entry for entry. The adapter writes no
  `description` key, because the file can hold the user's own hooks too.
  The fixture keeps a top-level `description`, as a user's file can, and
  the test does not compare it. The file holds no
  `PostCompact` entry: the edit decoder names no resettable receiver, so
  a reset has nothing to clear, and setup installs no hook without an
  effect. The reset decoder exists and is tested against
  `post_compact.json`.
