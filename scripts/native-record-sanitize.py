#!/usr/bin/env python3
"""Sanitize one native recording for the committed evidence.

Usage: native-record-sanitize.py [--id-map <file>] <record.json> <out-dir>

record.json is written by scripts/native-record.sh record. It names the
client, the scratch repository, the home directory, the seamark binary,
the instrumentation directory, the observer output, the transcript files
of the recorded sessions, and the extra files of the scenario. The
script writes the sanitized hook payloads, a manifest with sizes, the
sanitized transcripts, and the extra files into <out-dir>, in the layout
of internal/integration/testdata/<client>/<scenario>/. The replay tests
read the transcripts, because each tool call of a payload must name a
call of the session transcript.

Sanitization replaces machine paths with fixed placeholders, replaces
account and organization identifiers, and replaces system prompts,
request copies, and generated context with markers, because they hold
account data and no test reads them. It replaces the user name where
it stands alone as a word, and the encoded bytes of every image or
document. It keeps the prompts, tool calls, tool output, and every field
that a decoder reads. A text that names the repository path in a form
that no path pair replaces, for example without the /private prefix or
in another case, keeps that form, and the check refuses the copy. A
scratch directory name of the record's deny list becomes one fixed word
where the text names it outside a full path, for example in a
compaction summary. It refuses to finish when a deny-listed value is
still present in any output file, for example the home path or the user
name in a form that no rule replaces.

For a Claude Code recording, an ID pass also replaces each ID that
Claude Code or its API generates: the session, prompt, agent, tool use,
transcript record, message, request, task-notification run, and
background task IDs. These IDs are keys of real sessions and API
requests of the operator's account, so they must not ship. Each real ID
gets one synthetic value with the shape and the length of the real ID.
That value is the same in every file and file name, because the replay
joins payloads, transcripts, and subagent files by these IDs. The
numbers follow the first use in a fixed input order and never the real
values, so a second run gives the same bytes. After the write, a
residual check refuses the copy when an ID of a known shape remains.
--id-map writes the map from real to synthetic IDs into a file outside
<out-dir>, so a reviewer can check that the pass changed only IDs.

A Codex recording gets an ID pass of its own, for the same reason: the
thread, turn, window, item, exec, call, response, reasoning, message,
tool list, and process IDs. A Codex session ID and agent ID are thread
IDs, so they share the thread kind. One map serves every file and file
name, because the replay joins a hook tool_use_id to a rollout item, a
session_id to the session_meta of a rollout, an agent_id to a worker
thread, and a transcript_path to a rollout file. A Codex thread ID is a
UUIDv7 and holds its creation time, so no synthetic value has that form.

A Codex recording also moves every recorded time by one constant offset
in whole seconds: ISO strings, epoch numbers, the time and the date
directory of each rollout file name, and the current_date tag. The
offset moves the first recorded time to 21:00:00 UTC on its own UTC
date, so the copy shows no real time of day. The window counts in UTC,
so the script names no local time zone. Every duration stays exact. A
rollout file name holds the local wall time of the thread start, so
the pass writes that name in UTC. The offset is written nowhere, and
the manifest says only that the times are shifted. The
additional_tools record lists the operator's tools, so its tool list
becomes a marker. A Codex residual check refuses the copy when a real
ID, a home path, a local time offset, or an unshifted time remains.
"""

import calendar
import datetime
import decimal
import json
import os
import re
import shutil
import sys
import time

# The placeholders of the committed evidence. The path placeholders are
# absolute, so the decoders accept the sanitized hints as paths.
WORKSPACE = "/workspace/native"
HOME = "/home/native"
SEAMARK = "/opt/seamark/bin/seamark"
INSTRUMENTATION = "/opt/instrumentation"
TASKS = "/tmp/claude-tasks"
ACCOUNT = "native-account"
MARKER = "<sanitized>"

# The user name placeholder is the last element of the home placeholder.
# Thus a recorded `ls -l` owner and the home path name the same user.
USER = os.path.basename(HOME)

# The placeholder of encoded attachment bytes. It is valid base64 of the
# text "sanitized", so a reader that decodes it gets no error. The
# decoders keep only a reference to an attachment, so no value of the
# bytes is evidence.
ATTACHMENT_BYTES = "c2FuaXRpemVk"

# The placeholder of a scratch directory name in model text. A compaction
# summary or a reply can name a shortened scratch path, such as
# ".../claude-timing/repo", that no path pair matches. The name holds
# machine values, such as the user ID in "claude-501", so it must not ship.
SCRATCH = "native-scratch"

# The placeholders by their manifest keys. The manifest lists them for the
# reviewer, and the scratch rule keeps each of them whole.
PLACEHOLDERS = {
    "workspace": WORKSPACE, "home": HOME, "user": USER, "seamark": SEAMARK,
    "instrumentation": INSTRUMENTATION, "claude_tasks": TASKS, "account": ACCOUNT,
    "attachment_bytes": ATTACHMENT_BYTES, "scratch": SCRATCH,
}

# Addresses that may stay: commit trailers in the client's own prompts
# and the scratch repository's Git identity.
ALLOWED_EMAILS = {"noreply@anthropic.com", "native@example.invalid"}
EMAIL = re.compile(r"[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}")

# Claude Code writes its task output files under a per-user directory
# of the system temporary directory.
CLAUDE_TASK_ROOT = re.compile(r"(?:/private)?/tmp/claude-\d+")

# Codex names the operator's time zone in its environment context and
# in "timezone" fields.
TIMEZONE_TAG = re.compile(r"<timezone>[^<]*</timezone>")
TIMEZONE = "Etc/UTC"

# Claude transcript records without a uuid that copy the model request.
# They hold the system prompt and account context.
CLAUDE_REQUEST_RECORDS = {"api-request", "api-request-blob", "api-request-shape"}

# The encoded bytes of an attachment in a data URL (RFC 2397). The
# prefix with the media type stays, because the decoders read it.
DATA_URL_BYTES = re.compile(r"(data:[A-Za-z0-9!#$&^_.+/-]*(?:;[A-Za-z0-9!#$&^_.+=-]*)*;base64,)[A-Za-z0-9+/=]+")

# The kinds of the Claude Code ID pass. Each kind numbers its IDs from 1.
SESSION = "session"
PROMPT = "prompt"
AGENT = "agent"
TOOL_USE = "tool_use"
RECORD = "record"
MESSAGE = "message"
REQUEST = "request"
RUN = "run"
TASK = "task"
KINDS = (SESSION, PROMPT, AGENT, TOOL_USE, RECORD, MESSAGE, REQUEST, RUN, TASK)

# The fixed start of the synthetic value of each kind. The number fills
# the rest with leading zeros, so the synthetic value keeps the length of
# the real ID. An agent or task ID keeps its own first letter instead,
# because that letter names the task type.
SYNTHETIC_PREFIX = {
    SESSION: "10000000-0000-4000-8000-",
    PROMPT: "20000000-0000-4000-8000-",
    TOOL_USE: "toolu_01NativeToolUse",
    RECORD: "30000000-0000-4000-8000-",
    MESSAGE: "msg_01NativeMessage",
    REQUEST: "req_01NativeRequest",
    RUN: "native-run-",
}

# The ID shapes of Claude Code. An agent ID is "a" and 16 or 7 hex
# digits. A background task ID is one lower-case letter for the task type
# and 8 base-36 characters. The two shapes differ in length, so the shape
# alone gives the kind of a task-id value. The recordings of Claude Code
# 2.1.319 hold the UUID and the 16-digit agent shape. The 7-digit agent
# shape and the task shape come from Claude Code 2.1.314, because no
# 2.1.319 recording holds them.
UUID_TEXT = r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}"
UUID_SHAPE = re.compile(r"^%s$" % UUID_TEXT)
AGENT_SHAPE = re.compile(r"^a(?:[0-9a-f]{16}|[0-9a-f]{7})$")
TASK_SHAPE = re.compile(r"^[a-z][0-9a-z]{8}$")

# A shorter value is no generated ID. A substring replacement of a short
# value could also change other text, so the pass never maps one.
MIN_ID_LENGTH = 8

# The places where an ID stands inside a longer text: the tags of a
# task-notification prompt, the output file of a background task, the
# file of a subagent transcript, and the session directory of a
# transcript or a task output. A task-id or an output file can name an
# agent or another background task, so its shape gives the kind.
BACKGROUND = "background"
TEXT_IDS = [
    (re.compile(r"<task-id>([^<]*)</task-id>"), BACKGROUND),
    (re.compile(r"<tool-use-id>([^<]*)</tool-use-id>"), TOOL_USE),
    (re.compile(r"/tasks/([0-9a-z]+)\.output"), BACKGROUND),
    (re.compile(r"/subagents/(?:[^\s\"'<>]*/)?agent-([0-9a-z]+)\.(?:jsonl|meta\.json)"), AGENT),
    (re.compile(r"/projects/[^/\s\"'<>]+/(%s)(?:\.jsonl|/)" % UUID_TEXT), SESSION),
    (re.compile(r"/(%s)/tasks/" % UUID_TEXT), SESSION),
]

# The ID shapes that the residual check finds in text. A UUID matches in
# any case. An agent ID of 16 hex digits counts only between characters
# that are not letters or digits, so a part of a hex digest never counts.
# A token counts from 20 characters.
RESIDUAL_TEXT = [
    ("UUID", re.compile(UUID_TEXT)),
    ("agent", re.compile(r"(?<![A-Za-z0-9])(a[0-9a-f]{16})(?![A-Za-z0-9])")),
    ("agent", re.compile(r"agent-(a[0-9a-f]{7})(?![A-Za-z0-9])")),
    ("tool use", re.compile(r"toolu_[A-Za-z0-9]{14,}")),
    ("message", re.compile(r"msg_[A-Za-z0-9]{16,}")),
    ("request", re.compile(r"req_[A-Za-z0-9]{16,}")),
]

# The parts of an output file name that the refusal message hides. The
# message must not print an ID, also when the ID is a file or directory
# name.
HIDDEN_NAME_PARTS = [
    re.compile(UUID_TEXT),
    re.compile(r"a[0-9a-f]{16}"),
    re.compile(r"(?<=agent-)a[0-9a-f]{7}"),
    re.compile(r"(?:toolu|msg|req)_[A-Za-z0-9]{14,}"),
]

# The directory name of the subagent transcripts. It has the task shape,
# so the refusal message keeps it by name.
SUBAGENTS = "subagents"

# The kinds of the Codex ID pass. Each kind numbers its IDs from 1. A
# Codex session ID and agent ID are thread IDs: a hook names the root
# thread as its session and a worker thread as its agent.
THREAD = "thread"
TURN = "turn"
ITEM = "item"
WINDOW = "window"
EXEC = "exec"
CALL = "call"
CTC = "ctc"
CTCO = "ctco"
RESP = "resp"
RS = "rs"
MSG = "msg"
AT = "at"
PROCESS = "process"
CODEX_KINDS = (THREAD, TURN, ITEM, WINDOW, EXEC, CALL, CTC, CTCO, RESP, RS, MSG, AT, PROCESS)

# The fixed start of each synthetic Codex UUID. The number fills the rest.
# The layout is the one of a UUIDv4, so no synthetic value holds a time.
CODEX_UUID_PREFIX = {
    THREAD: "10000000-0000-4000-8000-",
    TURN: "20000000-0000-4000-8000-",
    ITEM: "30000000-0000-4000-8000-",
    WINDOW: "40000000-0000-4000-8000-",
    EXEC: "50000000-0000-4000-8000-",
    AT: "60000000-0000-4000-8000-",
    CTCO: "70000000-0000-4000-8000-",
    MSG: "80000000-0000-4000-8000-",
}

# The member names that name a Codex ID of one kind. The names come from
# the hook payloads, rollouts, and exec events of codex-cli 0.169.0.
CODEX_KEY_KINDS = {
    "session_id": THREAD, "agent_id": THREAD, "thread_id": THREAD, "parent_thread_id": THREAD,
    "sender_thread_id": THREAD, "receiver_thread_ids": THREAD, "target": THREAD, "targets": THREAD,
    "turn_id": TURN, "root_turn_id": TURN, "parent_turn_id": TURN,
    "window_id": WINDOW,
}

# A process ID is a short number, so a text replacement could change
# other numbers. The pass replaces it only as the value of this member.
PROCESS_KEY = "process_id"

# A rollout file name holds the start time of its thread and the thread
# ID. Codex writes the local wall time there, in a directory of the
# local date. ROLLOUT_PATH reads the dated name of a recorded rollout.
ROLLOUT_NAME = re.compile(r"rollout-\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2}-(%s)\.jsonl" % UUID_TEXT)
ROLLOUT_PATH = re.compile(
    r"\d{4}/\d{2}/\d{2}/rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-(%s)\.jsonl$" % UUID_TEXT)

# The path of a rollout in a copy. Its name must hold the UTC start of
# its thread, in a directory of the same date.
ROLLOUT_COPY = re.compile(
    r"(?:^|/)sessions/(\d{4})/(\d{2})/(\d{2})/rollout-(\d{4}-\d{2}-\d{2}T\d{2}-\d{2}-\d{2})-[^/]*\.jsonl$")

# A Codex UUID behind a prefix gets the kind of the prefix: the exec item
# of a tool call, the tool list, the output of a custom tool call, and a
# message.
CODEX_PREFIXED_UUID = re.compile(r"(?<![0-9A-Za-z_])(exec-|at_|ctco_|msg_)(%s)" % UUID_TEXT)
CODEX_UUID_PREFIX_KINDS = {"exec-": EXEC, "at_": AT, "ctco_": CTCO, "msg_": MSG}

# The OpenAI API object IDs with a prefix and a body of letters and
# digits: a call ID, a custom tool call, a response, a reasoning item,
# and a message. A message ID can also be a prefix and a UUID, which the
# UUID rule covers.
CODEX_TOKEN = re.compile(r"(?<![0-9A-Za-z_])(call|ctc|resp|rs|msg)_[A-Za-z0-9]{20,}(?![A-Za-z0-9])")
CODEX_TOKEN_KINDS = {"call": CALL, "ctc": CTC, "resp": RESP, "rs": RS, "msg": MSG}

# The residual check also refuses a function call ID (fc_). No recording
# holds one yet, so the pass has no kind for it.
CODEX_RESIDUAL_TOKEN = re.compile(r"(?<![0-9A-Za-z_])(call|ctc|resp|rs|msg|fc)_[A-Za-z0-9]{16,}(?![A-Za-z0-9])")

# Any UUID inside a text. The second walk of the Codex pass maps each one
# that the first walk left as an item ID.
UUID_ANY = re.compile(UUID_TEXT)

# A macOS home path. The deny list holds the operator's home, and this
# rule also refuses the home of another user.
HOME_ROOT = "/Users/"

# The time forms of a Codex recording. An ISO time can have a zone. A
# rollout file name writes the time with hyphens.
ISO_TIME = re.compile(r"(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-]\d{2}:\d{2})?")
NAME_TIME = re.compile(r"(\d{4})-(\d{2})-(\d{2})T(\d{2})-(\d{2})-(\d{2})")
CURRENT_DATE = re.compile(r"<current_date>(\d{4}-\d{2}-\d{2})</current_date>")
EPOCH_TEXT = re.compile(r"(?<![\d.])(\d{10}(?:\.\d+)?|\d{13}|\d{16}|\d{19})(?![\d.])")

# The window of the shifted times: 21:00 to 08:00 UTC. Every shifted
# time falls in it, so a copy shows no real time of day. The window
# counts in UTC, because a named local zone would tell where the
# operator lives.
NIGHT_START = 21
NIGHT_END = 8

NS = 10 ** 9
DAY_NS = 86400 * NS

# The powers of ten of the epoch units: seconds, milliseconds,
# microseconds, and nanoseconds.
EPOCH_UNITS = (0, 3, 6, 9)

# The range of a plausible epoch time, from 2001 to 2100, in seconds.
EPOCH_RANGE = (10 ** 9, 4102444800)


def claude_slug(path):
    """Return the project directory name that Claude Code derives."""
    return "".join(c if c.isascii() and c.isalnum() else "-" for c in path)


def repo_forms(repo):
    """Return the case-folded forms of the repository path that refuse a copy.

    The script records the physical path. On macOS it starts with
    /private, because /tmp and /var are links into /private. A client or a
    model can name the path without that prefix or in another case. The
    path pairs replace only the recorded spelling. The form without the
    prefix is part of the form with it, so one search finds both. The
    project directory name that Claude Code derives from the path is a
    path pair too, so its form without the prefix also counts.
    """
    bare = repo[len("/private"):] if repo.startswith("/private/") else repo

    return [bare.casefold(), claude_slug(bare).casefold()]


def scratch_pattern(record):
    """Return the pattern of the scratch names to replace, or None.

    The scratch names are the "deny" entries of the record. The match
    ignores case, as the deny check does. The longer names come first,
    so a name never loses only a part. A name of the home path, such as
    the user name, stays out and keeps its refusal. So does a name inside
    a placeholder, because the replacement would change that placeholder.
    """
    own = {p.casefold() for p in record["home"].split(os.sep) if p}
    fixed = [p.casefold() for p in list(PLACEHOLDERS.values()) + [claude_slug(WORKSPACE)]]
    names = [n for n in record.get("deny", []) if n and n.casefold() not in own and
             not any(n.casefold() in p for p in fixed)]

    if not names:
        return None

    names.sort(key=len, reverse=True)

    return re.compile("|".join(re.escape(n) for n in names), re.IGNORECASE)


def replaced_member(parent, key, v):
    """Return the placeholder of one JSON member, or None to keep it.

    Attachment bytes become a placeholder. A pasted image or a file
    that a tool reads can be personal, and the decoders store only a
    reference to it. The "base64" member of a Claude Code tool output
    holds the bytes. So does the "data" member of a Claude API source
    block (type "base64") and of an MCP image or document block.
    """
    if key == "timezone":
        return TIMEZONE

    carrier = key == "data" and parent.get("type") in ("base64", "image", "document")
    if isinstance(v, str) and (key == "base64" or carrier):
        return ATTACHMENT_BYTES

    return None


def strings(v, key=None, obj=None, owner=None):
    """Yield every string of a decoded JSON value in document order.

    Each item is (text, key, obj, owner, is_key): the member name of the
    text, the object that holds it, and the member name of that object.
    An object key comes before its value. A list passes its own member
    name to its items, so an item of background_tasks has that owner. A
    member that the sanitizer replaces with a placeholder yields nothing.
    """
    if isinstance(v, str):
        yield v, key, obj, owner, False
    elif isinstance(v, list):
        for x in v:
            yield from strings(x, key, obj, owner)
    elif isinstance(v, dict):
        for k, x in v.items():
            yield k, None, v, key, True

            if replaced_member(v, k, x) is None:
                yield from strings(x, k, v, key)


def synthetic(kind, n, real):
    """Return the synthetic value number n of a kind for one real ID."""
    prefix = real[0] if kind in (AGENT, TASK) else SYNTHETIC_PREFIX[kind]

    return prefix + str(n).zfill(len(real) - len(prefix))


def codex_synthetic(kind, n, real):
    """Return the synthetic value number n of a Codex kind for one real ID.

    A UUID keeps the UUID form with the fixed start of its kind. A token
    keeps its prefix, and a process ID starts with 9. The number fills
    the rest with leading zeros, so the value keeps the length of the
    real ID.
    """
    if kind == PROCESS:
        prefix = "9"
    elif UUID_SHAPE.match(real):
        prefix = CODEX_UUID_PREFIX[kind]
    else:
        prefix = real[: real.index("_") + 1]

    return prefix + str(n).zfill(len(real) - len(prefix))


def background_kind(value):
    """Return the kind of a background task value by its shape, or None."""
    if AGENT_SHAPE.match(value):
        return AGENT

    if TASK_SHAPE.match(value):
        return TASK

    return None


def key_kind(text, key, obj, owner):
    """Return the kind of an ID that its member names, or None.

    The member names come from the hook payloads, transcripts, and
    subagent meta files of Claude Code 2.1.314. They cover the recordings
    of Claude Code 2.1.319 too: the residual check found no ID there that
    the pass left.
    """
    if key in ("session_id", "sessionId"):
        return SESSION

    if key in ("prompt_id", "promptId"):
        return PROMPT

    if key in ("agent_id", "agentId"):
        return AGENT

    if key in ("tool_use_id", "toolUseId"):
        return TOOL_USE

    # toolUseID names a tool call on attachment records and a transcript
    # record on system records. The second form is a UUID, which the
    # record walk maps.
    if key == "toolUseID" and text.startswith("toolu_"):
        return TOOL_USE

    if key == "id" and obj.get("type") == "tool_use":
        return TOOL_USE

    if key == "id" and owner == "message" and text.startswith("msg_"):
        return MESSAGE

    if key == "requestId" and text.startswith("req_"):
        return REQUEST

    if key == "runId" and owner == "origin":
        return RUN

    if key == "id" and owner == "background_tasks":
        return AGENT if obj.get("type") == "subagent" else background_kind(text) or TASK

    if key == "backgroundTaskId":
        return background_kind(text) or TASK

    return None


class IDMap:
    """Maps each real ID of one recording to its synthetic value.

    kinds gives the kind order of the counts and the map file, and make
    gives the synthetic value of a kind. A Claude Code recording and a
    Codex recording use their own kinds and values.
    """

    def __init__(self, kinds=KINDS, make=synthetic):
        self.order = kinds
        self.make = make
        self.kinds = {}
        self.synthetic = {}
        self.processes = {}
        self.renames = {}
        self.replacements = {}
        self.number = {}
        self.counts = {kind: 0 for kind in kinds}
        self.pattern = None
        self.values = set()

    def add(self, kind, real):
        """Give a real ID the next number of its kind, unless it has one.

        A process ID is short, so it skips the length rule and stays out
        of the text pattern. The sanitizer replaces it only as the value
        of its own member.
        """
        if not isinstance(real, str) or real in self.kinds:
            return

        if kind != PROCESS and len(real) < MIN_ID_LENGTH:
            return

        self.counts[kind] += 1
        self.kinds[real] = kind
        self.number[real] = self.counts[kind]
        value = self.make(kind, self.counts[kind], real)

        if kind == PROCESS:
            self.processes[real] = value
        else:
            self.synthetic[real] = value

    def read_names(self, text, key, obj, owner, is_key):
        """Map the IDs that a member name or a text pattern identifies."""
        if is_key:
            return

        kind = key_kind(text, key, obj, owner)
        if kind:
            self.add(kind, text)

        # A background value of another shape stays unmapped, so the
        # residual check refuses it.
        for pattern, kind in TEXT_IDS:
            for m in pattern.finditer(text):
                value = m.group(1)
                found = background_kind(value) if kind == BACKGROUND else kind
                if found:
                    self.add(found, value)

    def read_records(self, text, key, obj, owner, is_key):
        """Map every other UUID as a record ID, under any key and at any depth.

        Thus the compaction keys (logicalParentUuid, compactMetadata,
        droppedPromptUuids) and the submitUuid of a refused slash command
        need no list. Session and prompt IDs keep their own kind,
        because the first walk maps them before this walk.
        """
        if UUID_SHAPE.match(text):
            self.add(RECORD, text)

    def read_codex_names(self, text, key, obj, owner, is_key):
        """Map the Codex IDs that a member name, a rollout name, or a prefix identifies.

        The keys of agents_states name the thread of each agent. A
        member value of a known name gives the kind of that name. Any
        text can hold a rollout file name, a prefixed UUID, or a token.
        """
        if is_key:
            if owner == "agents_states" and UUID_SHAPE.match(text):
                self.add(THREAD, text)
        elif key in CODEX_KEY_KINDS and UUID_SHAPE.match(text):
            self.add(CODEX_KEY_KINDS[key], text)

        for m in ROLLOUT_NAME.finditer(text):
            self.add(THREAD, m.group(1))

        for m in CODEX_PREFIXED_UUID.finditer(text):
            self.add(CODEX_UUID_PREFIX_KINDS[m.group(1)], m.group(2))

        for m in CODEX_TOKEN.finditer(text):
            self.add(CODEX_TOKEN_KINDS[m.group(1)], m.group(0))

    def read_codex_items(self, text, key, obj, owner, is_key):
        """Map every other UUID inside a text as an item ID.

        So an item of a completed event, a retained message revision, and
        a later UUID key need no list.
        """
        for m in UUID_ANY.finditer(text):
            self.add(ITEM, m.group(0))

    def read_processes(self, v):
        """Map the value of each process_id member at any depth."""
        if isinstance(v, list):
            for x in v:
                self.read_processes(x)
        elif isinstance(v, dict):
            for k, x in v.items():
                if k == PROCESS_KEY and isinstance(x, (str, int)) and not isinstance(x, bool):
                    self.add(PROCESS, str(x))
                else:
                    self.read_processes(x)

    def compile(self):
        """Compile one alternation of the real IDs and file names, longest first.

        One pattern is fast enough for about a thousand IDs, where one
        replacement per ID and string is not. The longest-first order
        keeps a long ID whole when a shorter mapped ID is part of it. It
        also renames a dated rollout path before its bare file name.
        """
        self.replacements = {**self.synthetic, **self.renames}
        keys = sorted(self.replacements, key=len, reverse=True)
        self.pattern = re.compile("|".join(re.escape(k) for k in keys)) if keys else None
        self.values = set(self.synthetic.values()) | set(self.processes.values())

    def replace(self, s):
        """Replace each mapped real ID and file name inside a text."""
        if self.pattern is None:
            return s

        return self.pattern.sub(lambda m: self.replacements[m.group(0)], s)

    def entries(self):
        """Return the ID map in kind order, then in number order.

        The renamed file names stay out, because they hold the real
        times and the map shows only IDs.
        """
        value = {**self.synthetic, **self.processes}
        rows = sorted(value, key=lambda r: (self.order.index(self.kinds[r]), self.number[r]))

        return [{"kind": self.kinds[r], "real": r, "synthetic": value[r]} for r in rows]


def is_number(v):
    """Return True for a JSON number. A bool is no number here."""
    return isinstance(v, (int, float)) and not isinstance(v, bool)


def scaled(v, power):
    """Return a number times 10**power as an exact integer.

    A float goes through its shortest text, so the result holds the
    digits of the recording and no binary rounding.
    """
    if isinstance(v, int):
        return v * 10 ** power

    number = v if isinstance(v, decimal.Decimal) else decimal.Decimal(repr(v))

    return int(number.scaleb(power))


def zone_seconds(zone):
    """Return the offset of an ISO zone (Z or +hh:mm) in seconds."""
    if zone in (None, "Z"):
        return 0

    sign = -1 if zone[0] == "-" else 1

    return sign * (int(zone[1:3]) * 3600 + int(zone[4:6]) * 60)


def iso_seconds(m):
    """Return the whole UTC seconds of an ISO_TIME match."""
    parts = tuple(int(m.group(i)) for i in range(1, 7))

    return calendar.timegm(parts) - zone_seconds(m.group(8))


def iso_ns(m):
    """Return the UTC time of an ISO_TIME match in nanoseconds."""
    fraction = (m.group(7) or ".")[1:]

    return iso_seconds(m) * NS + int((fraction + "0" * 9)[:9])


def name_seconds(m):
    """Return the seconds of a NAME_TIME match, read as UTC."""
    return calendar.timegm(tuple(int(m.group(i)) for i in range(1, 7)))


def epoch_ns(v):
    """Return a number as epoch nanoseconds when its size fits an epoch unit, or None.

    The ranges of the units do not overlap, so the size alone gives the
    unit. The residual check uses this to find a time of any key.
    """
    for unit in EPOCH_UNITS:
        if EPOCH_RANGE[0] * 10 ** unit <= v <= EPOCH_RANGE[1] * 10 ** unit:
            return scaled(v, 9 - unit)

    return None


def in_night(ns):
    """Return True when a time in nanoseconds falls between 21:00 and 08:00 UTC."""
    hour = datetime.datetime.fromtimestamp(ns // NS, datetime.timezone.utc).hour

    return hour >= NIGHT_START or hour < NIGHT_END


class TimeShift:
    """Moves every recorded time of a Codex recording by one offset.

    The first walk collects the ISO times with a zone and every number.
    settle then gives the offset in whole seconds, so every duration and
    every fraction stays exact. A number counts as a time when it lies
    within one day of the ISO times in some epoch unit. A recording
    without an ISO time keeps its times, and the residual check refuses
    each one outside the window.
    """

    def __init__(self):
        self.isos = []
        self.numbers = []
        self.span = None
        self.offset = None
        self.real = None
        self.bounds = None

    def read(self, v):
        """Collect the ISO times and the numbers of a decoded JSON value."""
        if isinstance(v, str):
            self.isos += [iso_ns(m) for m in ISO_TIME.finditer(v) if m.group(8)]
        elif isinstance(v, list):
            for x in v:
                self.read(x)
        elif isinstance(v, dict):
            for x in v.values():
                self.read(x)
        elif is_number(v):
            self.numbers.append(v)

    def unit(self, v):
        """Return the power of ten of the epoch unit of a recorded time, or None."""
        if self.span is None or not is_number(v):
            return None

        for unit in EPOCH_UNITS:
            if self.span[0] - DAY_NS <= scaled(v, 9 - unit) <= self.span[1] + DAY_NS:
                return unit

        return None

    def settle(self):
        """Give the offset that moves the first time to 21:00:00 UTC on its own UTC date.

        The offset uses the first time floored to the second, so it is a
        whole number of seconds. The sanitizer refuses a recording whose
        shifted times reach 08:00 UTC of the next day, because no single
        offset then keeps every time in the window.
        """
        if not self.isos:
            return

        self.span = (min(self.isos), max(self.isos))
        times = list(self.isos)
        for v in self.numbers:
            unit = self.unit(v)
            if unit is not None:
                times.append(scaled(v, 9 - unit))

        self.real = (min(times), max(times))

        first = self.real[0] // NS
        day = datetime.datetime.fromtimestamp(first, datetime.timezone.utc).date()
        start = calendar.timegm((day.year, day.month, day.day, NIGHT_START, 0, 0))

        # UTC has no daylight saving change, so the window always lasts
        # from 21:00 to 08:00 of the next day, 11 hours.
        end = start + (24 - NIGHT_START + NIGHT_END) * 3600

        self.offset = start - first
        self.bounds = (start * NS, self.real[1] + self.offset * NS)

        if self.bounds[1] >= end * NS:
            sys.exit("sanitization incomplete:\n  the recorded times span more than the window from 21:00 to 08:00 UTC")

    def number(self, v):
        """Return a number shifted when it is a recorded time."""
        unit = self.unit(v) if self.offset is not None else None
        if unit is None:
            return v

        step = self.offset * 10 ** unit
        if isinstance(v, int):
            return v + step

        return float(decimal.Decimal(repr(v)) + step)

    def text(self, s):
        """Shift each ISO time with a zone inside a text and write it in UTC."""
        if self.offset is None:
            return s

        return ISO_TIME.sub(self.shift_iso, s)

    def shift_iso(self, m):
        """Return one ISO_TIME match shifted, in UTC, with its fraction digits."""
        zone = m.group(8)
        if not zone:
            return m.group(0)

        stamp = time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(iso_seconds(m) + self.offset))

        return stamp + (m.group(7) or "") + ("+00:00" if zone == "+00:00" else "Z")

    def shifted(self, ns):
        """Return True when a time lies in the window and, after a shift, in the shifted span."""
        if not in_night(ns):
            return False

        return self.bounds is None or self.bounds[0] <= ns <= self.bounds[1]

    def near_real(self, ns):
        """Return True when a time lies within one day of the real times."""
        return self.real is not None and self.real[0] - DAY_NS <= ns <= self.real[1] + DAY_NS

    def dates(self):
        """Return the UTC dates of the shifted span."""
        if self.bounds is None:
            return set()

        first = datetime.datetime.fromtimestamp(self.bounds[0] // NS, datetime.timezone.utc).date()
        last = datetime.datetime.fromtimestamp(self.bounds[1] // NS, datetime.timezone.utc).date()

        return {(first + datetime.timedelta(days=i)).isoformat() for i in range((last - first).days + 1)}


class Sanitizer:
    """Replaces machine values in text with the placeholders."""

    def __init__(self, record):
        self.repo = record["repo"]
        self.pairs = [
            (self.repo, WORKSPACE),
            (claude_slug(self.repo), claude_slug(WORKSPACE)),
            (record["seamark"], SEAMARK),
            (record["instrumentation"], INSTRUMENTATION),
            (record["home"], HOME),
        ]
        # Longer values first, so a path never loses only its prefix.
        self.pairs.sort(key=lambda p: len(p[0]), reverse=True)
        self.user = os.path.basename(record["home"].rstrip("/"))

        # Tool output can name the user as a word of its own, for example
        # the owner column of `ls -l`. Only a word between white space or
        # the text ends counts. Any other form, for example "uid=501(name)",
        # stays, so the deny list below still refuses it.
        self.user_word = re.compile(r"(?<!\S)%s(?!\S)" % re.escape(self.user)) if self.user else None

        self.deny = [record["home"], self.repo, record["seamark"], self.user]
        self.deny += record.get("deny", [])
        self.repo_forms = repo_forms(self.repo)
        self.scratch = scratch_pattern(record)

        # The ID map of the ID pass, and the time shift of a Codex
        # recording. A Claude Code recording keeps its times.
        self.ids = None
        self.times = None

    def text(self, s):
        if self.ids:
            s = self.ids.replace(s)

        for old, new in self.pairs:
            s = s.replace(old, new)

        if self.user_word:
            s = self.user_word.sub(USER, s)

        # A form of the repository path that no pair replaced keeps its
        # text, so the check refuses the copy. The task-root rule and the
        # scratch rule would change a part of that form, for example
        # "/tmp/claude-501" or a scratch name. The changed machine path
        # would then pass the check.
        if not self.holds_repo(s):
            s = CLAUDE_TASK_ROOT.sub(TASKS, s)

            # The scratch rule comes after the path rules. Thus "claude-501"
            # in a task path becomes part of the task placeholder, and only a
            # name that no path rule replaced becomes the scratch placeholder.
            if self.scratch:
                s = self.scratch.sub(SCRATCH, s)

        s = TIMEZONE_TAG.sub("<timezone>%s</timezone>" % TIMEZONE, s)
        s = DATA_URL_BYTES.sub(r"\g<1>" + ATTACHMENT_BYTES, s)

        if self.times is not None:
            s = self.times.text(s)

        return s

    def holds_repo(self, s):
        """Return True when a text holds a form of the repository path."""
        folded = s.casefold()

        return any(form in folded for form in self.repo_forms)

    def value(self, v):
        """Sanitize every string inside a decoded JSON value."""
        if isinstance(v, str):
            return self.text(v)
        if isinstance(v, list):
            return [self.value(x) for x in v]
        if isinstance(v, dict):
            return {self.text(k): self.member(v, k, x) for k, x in v.items()}

        if self.times is not None and is_number(v):
            return self.times.number(v)

        return v

    def member(self, parent, key, v):
        """Sanitize one member of a JSON object (see replaced_member)."""
        placeholder = replaced_member(parent, key, v)
        if placeholder is not None:
            return placeholder

        # A process ID keeps its JSON type, a string or a number.
        if self.ids is not None and key == PROCESS_KEY and (isinstance(v, str) or is_number(v)) and \
                str(v) in self.ids.processes:
            mapped = self.ids.processes[str(v)]
            return mapped if isinstance(v, str) else int(mapped)

        return self.value(v)

    def label(self, out_dir, path):
        """Return the name of an output file for a refusal message.

        The name is relative to out_dir, with each ID part and each file
        name time hidden. A copy without an ID map keeps the full path.
        """
        if self.ids is None:
            return path

        return shown_name(os.path.relpath(path, out_dir).replace(os.sep, "/"))

    def check(self, out_dir):
        """Fail when a deny-listed value, a repository path form, or an e-mail address remains.

        The deny-list match ignores case, because a tool can print the
        user name or a path in another case. A false hit only refuses
        the copy, which is the safe direction.
        """
        problems = []
        deny = [d.casefold() for d in self.deny if d]

        for base, _, files in os.walk(out_dir):
            for name in files:
                path = os.path.join(base, name)
                with open(path, encoding="utf-8", errors="replace") as f:
                    data = f.read()

                folded = data.casefold()

                for d in deny:
                    if d in folded:
                        problems.append("%s: holds a deny-listed value" % self.label(out_dir, path))

                if self.holds_repo(data):
                    problems.append("%s: holds a form of the repository path that no path pair replaces"
                                    % self.label(out_dir, path))

                for m in EMAIL.findall(data):
                    if m not in ALLOWED_EMAILS:
                        problems.append("%s: holds an e-mail address" % self.label(out_dir, path))

        return problems


def shown_name(rel):
    """Return an output file name with each ID part replaced by <id>.

    The time of a rollout file name becomes <time>, so the message never
    prints a time that the shift missed.
    """
    parts = []

    for part in rel.split("/"):
        part = NAME_TIME.sub("<time>", part)

        for pattern in HIDDEN_NAME_PARTS:
            part = pattern.sub("<id>", part)

        stem, dot, ext = part.partition(".")
        bare = stem[len("agent-"):] if stem.startswith("agent-") else stem

        if bare != SUBAGENTS and TASK_SHAPE.match(bare):
            stem = stem[: len(stem) - len(bare)] + "<id>"

        parts.append(stem + dot + ext)

    return "/".join(parts)


def residual(out_dir, ids, label):
    """Fail when an output file or file name holds an unmapped ID.

    The text check finds each ID shape that has a pattern. A synthetic
    value of the map is no hit, because the synthetic session, prompt,
    and record values are UUIDs too. A background task ID has no text
    pattern, so the place check reads each place that can hold one: it
    must hold a synthetic agent or task value. The task-notification
    run ID has neither. Only the key map covers it, and the privacy
    review reads it. A false hit only refuses the copy, which is the
    safe direction.
    """
    background = {v for r, v in ids.synthetic.items() if ids.kinds[r] in (AGENT, TASK)}
    problems = []

    def text_hits(text):
        for kind, pattern in RESIDUAL_TEXT:
            for m in pattern.finditer(text):
                if m.group(m.lastindex or 0) not in ids.values:
                    yield kind

    for base, _, files in os.walk(out_dir):
        for name in files:
            path = os.path.join(base, name)
            rel = os.path.relpath(path, out_dir).replace(os.sep, "/")
            shown = label(path)

            with open(path, encoding="utf-8", errors="replace") as f:
                data = f.read()

            for kind in set(text_hits(rel)) | set(text_hits(data)):
                problems.append("%s: holds an unmapped %s ID" % (shown, kind))

            for value in json_values(path, data):
                for text, key, obj, owner, is_key in strings(value):
                    places = []
                    if not is_key and ((key == "id" and owner == "background_tasks") or key == "backgroundTaskId"):
                        places.append(text)

                    for pattern, kind in TEXT_IDS:
                        if kind == BACKGROUND:
                            places += [m.group(1) for m in pattern.finditer(text)]

                    if any(p not in background for p in places):
                        problems.append("%s: holds an unmapped task ID" % shown)

    return problems


def codex_text_hits(text, ids, times):
    """Yield what a text of a Codex copy holds that the copy must not hold.

    A real ID or file name of the map counts, and so does an ID shape
    that is not synthetic, because the pass maps every UUID and token
    that it knows. A home path, an ISO time with a local zone or without
    a zone, and a time outside the shifted window count too.
    """
    if ids.pattern is not None:
        for m in ids.pattern.finditer(text):
            real = m.group(0)
            yield "a real rollout file name" if real in ids.renames else "a real %s ID" % ids.kinds[real]

    for m in UUID_ANY.finditer(text):
        if m.group(0) not in ids.values:
            yield "an unmapped UUID"

    for m in CODEX_RESIDUAL_TOKEN.finditer(text):
        if m.group(0) not in ids.values:
            yield "an unmapped %s_ ID" % m.group(1)

    if HOME_ROOT in text:
        yield "a home path"

    for m in ISO_TIME.finditer(text):
        zone = m.group(8)
        if not zone:
            yield "a timestamp without a time zone"
        elif zone not in ("Z", "+00:00"):
            yield "a timestamp with a local offset"
        elif not times.shifted(iso_ns(m)):
            yield "an unshifted time"

    for m in NAME_TIME.finditer(text):
        if not times.shifted(name_seconds(m) * NS):
            yield "an unshifted file name time"

    if times.bounds is not None:
        dates = times.dates()
        for m in CURRENT_DATE.finditer(text):
            if m.group(1) not in dates:
                yield "an unshifted current_date"


def codex_value_hits(v, ids, times, key=None):
    """Yield what a decoded JSON value of a Codex copy must not hold.

    A number of epoch size must lie in the shifted window, under any
    member name. Epoch digits inside a string are never shifted, so
    digits near a real time count. A process_id must be synthetic.
    """
    if isinstance(v, dict):
        for k, x in v.items():
            yield from codex_value_hits(x, ids, times, k)
    elif isinstance(v, list):
        for x in v:
            yield from codex_value_hits(x, ids, times, key)
    elif isinstance(v, str):
        for m in EPOCH_TEXT.finditer(v):
            ns = epoch_ns(decimal.Decimal(m.group(1)))
            if ns is not None and times.near_real(ns):
                yield "epoch digits of a recorded time"

        if key == PROCESS_KEY and v not in ids.processes.values():
            yield "an unmapped process ID"
    elif is_number(v):
        ns = epoch_ns(v)
        if ns is not None and not times.shifted(ns):
            yield "an unshifted epoch time"

        if key == PROCESS_KEY and str(v) not in ids.processes.values():
            yield "an unmapped process ID"


def session_start(values):
    """Return the UTC start of the first session_meta record in nanoseconds, or None."""
    for r in values:
        if not isinstance(r, dict) or r.get("type") != "session_meta":
            continue

        stamp = (r.get("payload") or {}).get("timestamp")
        m = ISO_TIME.fullmatch(stamp) if isinstance(stamp, str) else None

        return iso_ns(m) if m and m.group(8) else None

    return None


def codex_residual(out_dir, ids, times, label):
    """Fail when a Codex copy holds a real ID, a home path, a local time, or an unshifted time.

    Each file name and each text is scanned, and each JSON value is
    read. A rollout file name must hold the UTC start of its thread in a
    directory of that date. The message names the file and the kind,
    never the value. A false hit only refuses the copy, which is the
    safe direction.
    """
    problems = []

    for base, _, files in os.walk(out_dir):
        for name in files:
            path = os.path.join(base, name)
            rel = os.path.relpath(path, out_dir).replace(os.sep, "/")
            shown = label(path)

            with open(path, encoding="utf-8", errors="replace") as f:
                data = f.read()

            values = json_values(path, data)
            hits = set(codex_text_hits(rel, ids, times)) | set(codex_text_hits(data, ids, times))

            for v in values:
                hits.update(codex_value_hits(v, ids, times))

            m = ROLLOUT_COPY.search(rel)
            if m:
                start = session_start(values)
                stamp = time.strftime("%Y-%m-%dT%H-%M-%S", time.gmtime(start // NS)) if start is not None else None

                if stamp != m.group(4) or "-".join(m.group(1, 2, 3)) != m.group(4)[:10]:
                    hits.add("a rollout file name that is not the UTC start of its thread")

            problems += ["%s: holds %s" % (shown, hit) for hit in hits]

    return problems


def json_values(path, data):
    """Return the decoded JSON values of an output file, one per JSONL line."""
    try:
        if path.endswith(".jsonl"):
            return [json.loads(line) for line in data.splitlines() if line.strip()]

        return [json.loads(data)]
    except ValueError:
        return []


def dump_line(v):
    return json.dumps(v, ensure_ascii=False, separators=(",", ":")) + "\n"


def claude_shape(r):
    """Return one Claude transcript record without the parts it drops.

    The text replacements come later. The ID pass reads this form, so a
    value that the sanitizer drops takes no number.
    """
    t = r.get("type")

    if t in CLAUDE_REQUEST_RECORDS:
        keep = {k: r[k] for k in ("type", "sessionId", "agentId", "version", "timestamp") if k in r}
        keep["sanitized"] = True
        return keep

    if t == "attachment" and isinstance(r.get("attachment"), dict):
        # Generated context: environment, account, skills, and prompt
        # snapshots. The decoder reads only the type of these records.
        r = dict(r)
        r["attachment"] = {"type": r["attachment"].get("type"), "sanitized": True}

        # "rendered" is the text that the model saw for the attachment.
        if "rendered" in r:
            r["rendered"] = MARKER

    msg = r.get("message")
    if isinstance(msg, dict) and isinstance(msg.get("content"), list):
        blocks = []
        for b in msg["content"]:
            if isinstance(b, dict) and b.get("type") in ("thinking", "redacted_thinking"):
                b = dict(b)
                if "thinking" in b:
                    b["thinking"] = ""
                for key in ("signature", "data"):
                    if key in b:
                        b[key] = MARKER
            blocks.append(b)
        r = dict(r)
        r["message"] = dict(msg, content=blocks)

    return r


def claude_record(s, r):
    """Sanitize one Claude transcript record."""
    return s.value(claude_shape(r))


def claude_extra(s, r):
    """Sanitize one line of an extra file of a Claude Code recording."""
    return s.value(r)


def codex_shape(r):
    """Return one Codex rollout line with its masked parts replaced.

    The text replacements come later. The ID pass reads this form, so a
    value that a mask replaces takes no number.
    """
    r = json.loads(json.dumps(r))
    p = r.get("payload")

    if not isinstance(p, dict):
        return r

    for key in ("creator_user_id", "creator_account_id"):
        if key in p:
            p[key] = ACCOUNT

    if isinstance(p.get("base_instructions"), dict):
        p["base_instructions"] = {"text": MARKER}

    if "encrypted_content" in p:
        p["encrypted_content"] = MARKER

    # The world state holds the operator's approved command prefixes of
    # other projects and the skill list. The rate limits hold the account
    # plan and its use. No decoder reads them.
    if r.get("type") == "world_state" and "state" in p:
        p["state"] = MARKER

    if "rate_limits" in p:
        p["rate_limits"] = MARKER

    # The developer message lists the operator's skills, plugins, and
    # apps. The leading tag stays, because it names the section.
    if p.get("type") == "message" and p.get("role") == "developer":
        for c in p.get("content") or []:
            if isinstance(c, dict) and isinstance(c.get("text"), str):
                tag = re.match(r"\s*(<[a-z_]+>)", c["text"])
                c["text"] = (tag.group(1) + " " if tag else "") + MARKER

    # The additional_tools record of codex-cli 0.169.0 lists the tools
    # that the session offers, also the MCP tools and plugins of the
    # operator's own setup. No decoder reads the list. The type, the ID,
    # and the role stay.
    if p.get("type") == "additional_tools" and "tools" in p:
        p["tools"] = MARKER

    return r


def map_strings(v, fn):
    """Return a decoded JSON value with fn applied to each string value."""
    if isinstance(v, str):
        return fn(v)

    if isinstance(v, list):
        return [map_strings(x, fn) for x in v]

    if isinstance(v, dict):
        return {k: map_strings(x, fn) for k, x in v.items()}

    return v


def codex_dates(s, r):
    """Write the UTC date of the shifted record time into each current_date tag.

    Codex writes the local date there. The record timestamp is already
    shifted and in UTC, so its date is the date of the tag. A record
    without a timestamp keeps its tags, and the residual check reads
    them.
    """
    stamp = r.get("timestamp") if isinstance(r, dict) else None
    if s.times is None or s.times.offset is None or not isinstance(stamp, str) or not ISO_TIME.match(stamp):
        return r

    tag = "<current_date>%s</current_date>" % stamp[:10]

    return map_strings(r, lambda t: CURRENT_DATE.sub(tag, t))


def codex_record(s, r):
    """Sanitize one Codex rollout line."""
    return codex_dates(s, s.value(codex_shape(r)))


def codex_extra(s, r):
    """Sanitize one line of an extra file of a Codex recording."""
    return codex_dates(s, s.value(r))


def sanitize_jsonl(src, dst, fn, s):
    os.makedirs(os.path.dirname(dst), exist_ok=True)
    with open(src, encoding="utf-8") as f, open(dst, "w", encoding="utf-8") as out:
        for line in f:
            if not line.strip():
                continue
            out.write(dump_line(fn(s, json.loads(line))))


def observed_metas(record):
    """Return the meta files of the observed hook payloads in time order.

    On macOS the observer clock counts whole microseconds, so two hooks
    that start together can have the same start_ns. The payload file name
    breaks such a tie, and the guard_identities step of
    scripts/native-record.sh uses the same order. Thus both give a payload
    the same file number, and the directory listing order never changes
    the copy.
    """
    observed = record.get("observed")
    if not observed or not os.path.isdir(observed):
        return []

    metas = []

    for name in os.listdir(observed):
        if name.endswith(".meta.json"):
            with open(os.path.join(observed, name), encoding="utf-8") as f:
                meta = json.load(f)
            meta["file"] = name[: -len(".meta.json")] + ".json"
            metas.append(meta)

    metas.sort(key=lambda m: (m["start_ns"], m["file"]))

    return metas


def hooks(record, s, out_dir):
    """Write the observed hook payloads in time order and the manifest.

    The manifest holds no native hook time. The lesson hooks run only on
    some of the observed events and tools, so a duration in the debug log
    cannot be paired with one observed payload by its event.
    """
    observed = record["observed"]
    metas = observed_metas(record)
    runs = record.get("runs", [])
    entries = []
    hook_dir = os.path.join(out_dir, "hooks")
    os.makedirs(hook_dir, exist_ok=True)

    for i, meta in enumerate(metas, 1):
        with open(os.path.join(observed, meta["file"]), "rb") as f:
            raw = f.read()

        payload = s.value(json.loads(raw))
        event = meta["event"]
        name = "%03d-%s.json" % (i, event)

        with open(os.path.join(hook_dir, name), "w", encoding="utf-8") as f:
            json.dump(payload, f, ensure_ascii=False, indent=2)
            f.write("\n")

        run = 0
        for n, start in enumerate(runs, 1):
            if meta["start_ns"] >= start:
                run = n

        entry = {
            "file": "hooks/" + name,
            "run": run,
            "event": event,
            "bytes": len(raw),
            "process_cwd": s.text(meta["process_cwd"]),
            "project_dir": s.text(meta["CLAUDE_PROJECT_DIR"]) if meta.get("CLAUDE_PROJECT_DIR") else None,
            "offset_ms": round((meta["start_ns"] - metas[0]["start_ns"]) / 1e6),
        }

        entries.append(entry)

    return entries


def read_values(form, path):
    """Return the decoded JSON values of one input file, as the write path sees them."""
    with open(path, encoding="utf-8") as f:
        if form == "json":
            return [json.load(f)]

        values = [json.loads(line) for line in f if line.strip()]

    if form == "transcript":
        return [claude_shape(v) for v in values]

    if form == "rollout":
        return [codex_shape(v) for v in values]

    return values


def first_timestamp(path):
    """Return the first record timestamp of a JSONL file, or None."""
    if not path.endswith(".jsonl"):
        return None

    with open(path, encoding="utf-8") as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            if isinstance(r, dict) and isinstance(r.get("timestamp"), str):
                return r["timestamp"]

    return None


def by_time(paths):
    """Return a sort key that orders by the first record timestamp.

    A file without a timestamp comes last. The path only breaks a tie.
    """
    ts = first_timestamp(paths[0])

    return (ts is None, ts or "", paths[0])


def transcript_units(paths, ids):
    """Yield the transcript files in the numbering order of the ID pass.

    Each item is (form, path, names): the read form of the file and the
    IDs that its file name gives, by kind. The sessions come in number
    order, and then the sessions that no hook payload numbered, by time.
    Each session gives its main transcript, then its subagent files in
    agent number order, then the agents without a number by time. One
    agent gives its .jsonl file before its .meta.json file. The order
    reads the map while the walk fills it, so a parent transcript line
    gives an agent its number before the subagent file comes. A file
    outside this layout comes last, in input order.
    """
    sessions, other = {}, []

    for p in paths:
        parts = p.split(os.sep)
        name = parts[-1]

        if SUBAGENTS in parts[:-1]:
            i = parts.index(SUBAGENTS)
            agent = name[len("agent-"):].split(".", 1)[0] if name.startswith("agent-") else ""

            if i > 0 and UUID_SHAPE.match(parts[i - 1]) and AGENT_SHAPE.match(agent) and \
                    name.endswith((".jsonl", ".meta.json")):
                entry = sessions.setdefault(os.sep.join(parts[:i]), {"main": None, "agents": {}})
                entry["agents"].setdefault(agent, []).append(p)
                continue
        elif name.endswith(".jsonl") and UUID_SHAPE.match(name[: -len(".jsonl")]):
            entry = sessions.setdefault(p[: -len(".jsonl")], {"main": None, "agents": {}})
            if entry["main"] is None:
                entry["main"] = p
                continue

        other.append(p)

    def files(entry):
        return [entry["main"]] if entry["main"] else sorted(f for fs in entry["agents"].values() for f in fs)

    numbered = sorted((k for k in sessions if ids.kinds.get(os.path.basename(k)) == SESSION),
                      key=lambda k: ids.number[os.path.basename(k)])
    rest = sorted((k for k in sessions if k not in numbered), key=lambda k: by_time(files(sessions[k])))

    for key in numbered + rest:
        entry = sessions[key]
        session = os.path.basename(key)

        if entry["main"]:
            yield "transcript", entry["main"], {SESSION: session}

        agents = entry["agents"]
        known = sorted((a for a in agents if ids.kinds.get(a) == AGENT), key=lambda a: ids.number[a])
        unknown = sorted((a for a in agents if a not in known), key=lambda a: by_time(sorted(agents[a])))

        for agent in known + unknown:
            for p in sorted(agents[agent], key=lambda f: (not f.endswith(".jsonl"), f)):
                yield ("transcript" if p.endswith(".jsonl") else "json"), p, {SESSION: session, AGENT: agent}

    for p in other:
        yield ("transcript" if p.endswith(".jsonl") else "json"), p, {}


def build_ids(record):
    """Build the ID map of a Claude Code recording before any output exists.

    The first walk maps the IDs that a member name, a text pattern, or a
    file name identifies, in the input order: the hook payloads in time
    order, the transcripts (see transcript_units), and the extras in
    name order. The second walk goes the same way and maps every other
    UUID as a record ID. So the record numbers follow the second walk.
    """
    ids = IDMap()
    units = []

    def walk_first(unit):
        form, path, names = unit
        units.append(unit)

        for kind, value in names.items():
            ids.add(kind, value)

        for v in read_values(form, path):
            for item in strings(v):
                ids.read_names(*item)

    for meta in observed_metas(record):
        walk_first(("json", os.path.join(record["observed"], meta["file"]), {}))

    for unit in transcript_units(record.get("transcripts", []), ids):
        walk_first(unit)

    for _, src in sorted(record.get("extras", {}).items()):
        walk_first(("extra", src, {}))

    for form, path, _ in units:
        for v in read_values(form, path):
            for item in strings(v):
                ids.read_records(*item)

    ids.compile()

    return ids


def build_codex(record):
    """Build the ID map and the time shift of a Codex recording before any output exists.

    The first walk maps the IDs that a member name, a rollout file name,
    or a prefix identifies, and it collects the times. It reads the hook
    payloads in time order, the rollouts in record order with the file
    name first, and the extras in name order. The second walk goes the
    same way and maps every other UUID as an item. So the numbers follow
    the first use and never the real values. Then the offset of the
    times and the new rollout file names follow.
    """
    ids = IDMap(CODEX_KINDS, codex_synthetic)
    times = TimeShift()

    units = [("json", os.path.join(record["observed"], meta["file"])) for meta in observed_metas(record)]
    units += [("rollout", p) for p in record.get("transcripts", [])]
    units += [("extra", src) for _, src in sorted(record.get("extras", {}).items())]

    for form, path in units:
        if form == "rollout":
            ids.read_codex_names(path, None, {}, None, False)

        for v in read_values(form, path):
            for item in strings(v):
                ids.read_codex_names(*item)

            ids.read_processes(v)
            times.read(v)

    for form, path in units:
        if form == "rollout":
            ids.read_codex_items(path, None, {}, None, False)

        for v in read_values(form, path):
            for item in strings(v):
                ids.read_codex_items(*item)

    times.settle()
    codex_renames(record, ids, times)
    ids.compile()

    return ids, times


def codex_renames(record, ids, times):
    """Give each rollout file a UTC name: the shifted thread start and the synthetic thread ID.

    Codex names a rollout file by the local wall time of the thread
    start, in a directory of the local date. The session_meta record of
    the file holds the same start in UTC. The new name holds that start,
    floored to the second and shifted, in a directory of its UTC date.
    The rename applies wherever the dated name or the bare file name
    stands in a text. The sanitizer refuses a rollout whose name time is
    not its session_meta start in some time zone, because the rule then
    reads the wrong time.
    """
    problems = []

    for src in record.get("transcripts", []):
        m = ROLLOUT_PATH.search(src.replace(os.sep, "/"))
        if not m:
            continue

        start = session_start(read_values("rollout", src))
        if start is None or times.offset is None:
            problems.append("a rollout file name time has no session_meta start time")
            continue

        local = name_seconds(NAME_TIME.fullmatch(m.group(1)))
        zone = local - start // NS
        if zone % 900 or abs(zone) > 14 * 3600:
            problems.append("a rollout file name time is not its session_meta start time in a time zone")
            continue

        stamp = time.gmtime(start // NS + times.offset)
        name = "rollout-%s-%s.jsonl" % (time.strftime("%Y-%m-%dT%H-%M-%S", stamp), ids.synthetic[m.group(2)])
        dated = m.group(0)

        ids.renames[dated] = time.strftime("%Y/%m/%d/", stamp) + name
        ids.renames[dated[len("YYYY/MM/DD/"):]] = name

    if problems:
        sys.exit("sanitization incomplete:\n  " + "\n  ".join(sorted(set(problems))))


def parse_args(argv):
    """Return the record path, the output directory, and the map path or None."""
    args, id_map = list(argv), None

    if args[:1] == ["--id-map"] and len(args) >= 2:
        id_map = args[1]
        args = args[2:]

    if len(args) != 2:
        sys.exit("usage: native-record-sanitize.py [--id-map <file>] <record.json> <out-dir>")

    return args[0], args[1], id_map


def main():
    record_path, out_dir, id_map = parse_args(sys.argv[1:])

    with open(record_path, encoding="utf-8") as f:
        record = json.load(f)

    s = Sanitizer(record)
    client = record["client"]
    claude = client == "claude"

    # The map holds the real IDs, so it must never enter the copy.
    if id_map:
        inside = os.path.realpath(id_map) + os.sep
        if inside.startswith(os.path.realpath(out_dir) + os.sep):
            sys.exit("the ID map must lie outside %s, because it holds the real IDs" % out_dir)

    if claude:
        s.ids = build_ids(record)
    else:
        s.ids, s.times = build_codex(record)

    if os.path.exists(out_dir):
        shutil.rmtree(out_dir)
    os.makedirs(out_dir)

    manifest = {"client": client, "client_version": record["client_version"], "placeholders": dict(PLACEHOLDERS)}

    # The count of each kind lets the testdata README state the replaced
    # IDs without their values. A Codex copy also says whether its times
    # are shifted, and never by how much.
    manifest["synthetic_ids"] = dict(s.ids.counts)

    if not claude:
        manifest["times_shifted"] = s.times.offset is not None

    if record.get("observed") and os.listdir(record["observed"]):
        manifest["hooks"] = hooks(record, s, out_dir)
    else:
        manifest["hooks"] = []

    fn = claude_record if claude else codex_record
    files = []

    for src in record.get("transcripts", []):
        rel = s.text(src)
        # The transcript path keeps its layout below the client's home.
        rel = rel[len(HOME) + 1:] if rel.startswith(HOME + "/") else os.path.basename(rel)
        rel = rel.replace(".claude/", "", 1).replace(".codex/", "", 1)
        dst = os.path.join(out_dir, rel)

        if src.endswith(".jsonl"):
            sanitize_jsonl(src, dst, fn, s)
        else:
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            with open(src, encoding="utf-8") as f:
                data = s.value(json.load(f))
            with open(dst, "w", encoding="utf-8") as f:
                json.dump(data, f, ensure_ascii=False)
                f.write("\n")

        files.append(rel)

    # The script sorts the transcripts by their real paths. The copy
    # sorts the synthetic paths again, so the order shows no real ID.
    manifest["transcripts"] = sorted(files)

    # Extra JSONL files keep only the path, identifier, and time
    # replacements, for example the event stream of `codex exec --json`.
    extra = claude_extra if claude else codex_extra
    extras = []
    for name, src in sorted(record.get("extras", {}).items()):
        sanitize_jsonl(src, os.path.join(out_dir, name), extra, s)
        extras.append(name)

    manifest["extras"] = extras

    with open(os.path.join(out_dir, "manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)
        f.write("\n")

    if id_map:
        with open(id_map, "w", encoding="utf-8") as f:
            json.dump({"ids": s.ids.entries()}, f, ensure_ascii=False, indent=1)
            f.write("\n")

    problems = s.check(out_dir)

    def label(path):
        return s.label(out_dir, path)

    if claude:
        problems += residual(out_dir, s.ids, label)
    else:
        problems += codex_residual(out_dir, s.ids, s.times, label)

    if problems:
        sys.exit("sanitization incomplete:\n  " + "\n  ".join(sorted(set(problems))))

    print("sanitized %d hook payloads and %d transcript files into %s" % (len(manifest["hooks"]), len(files), out_dir))


if __name__ == "__main__":
    main()
