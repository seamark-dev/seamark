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

The ID pass skips a Codex recording. Its thread and call IDs need a
decision of their own, and a residual check would refuse each Codex copy
because of its UUID thread IDs.
"""

import json
import os
import re
import shutil
import sys

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
    """Maps each real Claude Code ID of one recording to its synthetic value."""

    def __init__(self):
        self.kinds = {}
        self.synthetic = {}
        self.number = {}
        self.counts = {kind: 0 for kind in KINDS}
        self.pattern = None
        self.values = set()

    def add(self, kind, real):
        """Give a real ID the next number of its kind, unless it has one."""
        if not isinstance(real, str) or len(real) < MIN_ID_LENGTH or real in self.kinds:
            return

        self.counts[kind] += 1
        self.kinds[real] = kind
        self.number[real] = self.counts[kind]
        self.synthetic[real] = synthetic(kind, self.counts[kind], real)

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

    def compile(self):
        """Compile one alternation of the real IDs, longest first.

        One pattern is fast enough for about a thousand IDs, where one
        replacement per ID and string is not. The longest-first order
        keeps a long ID whole when a shorter mapped ID is part of it.
        """
        keys = sorted(self.synthetic, key=len, reverse=True)
        self.pattern = re.compile("|".join(re.escape(k) for k in keys)) if keys else None
        self.values = set(self.synthetic.values())

    def replace(self, s):
        """Replace each mapped real ID inside a text with its synthetic value."""
        if self.pattern is None:
            return s

        return self.pattern.sub(lambda m: self.synthetic[m.group(0)], s)

    def entries(self):
        """Return the map in kind order, then in number order."""
        rows = sorted(self.synthetic, key=lambda r: (KINDS.index(self.kinds[r]), self.number[r]))

        return [{"kind": self.kinds[r], "real": r, "synthetic": self.synthetic[r]} for r in rows]


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

        # The ID map of the Claude Code ID pass. It stays None for Codex.
        self.ids = None

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

        return v

    def member(self, parent, key, v):
        """Sanitize one member of a JSON object (see replaced_member)."""
        placeholder = replaced_member(parent, key, v)
        if placeholder is not None:
            return placeholder

        return self.value(v)

    def label(self, out_dir, path):
        """Return the name of an output file for a refusal message.

        A Claude Code copy names the file relative to out_dir, with each
        ID part hidden. A Codex copy keeps the full path.
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
    """Return an output file name with each ID part replaced by <id>."""
    parts = []

    for part in rel.split("/"):
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


def codex_record(s, r):
    """Sanitize one Codex rollout line."""
    r = json.loads(json.dumps(r))
    p = r.get("payload")

    if not isinstance(p, dict):
        return s.value(r)

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

    return s.value(r)


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

    return [claude_shape(v) for v in values] if form == "transcript" else values


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
    if claude and id_map:
        inside = os.path.realpath(id_map) + os.sep
        if inside.startswith(os.path.realpath(out_dir) + os.sep):
            sys.exit("the ID map must lie outside %s, because it holds the real IDs" % out_dir)

    if claude:
        s.ids = build_ids(record)

    if os.path.exists(out_dir):
        shutil.rmtree(out_dir)
    os.makedirs(out_dir)

    manifest = {"client": client, "client_version": record["client_version"], "placeholders": dict(PLACEHOLDERS)}

    # The count of each kind lets the testdata README state the replaced
    # IDs without their values.
    if claude:
        manifest["synthetic_ids"] = dict(s.ids.counts)

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

    # The script sorts the transcripts by their real paths. A Claude Code
    # copy sorts the synthetic paths again, so the order shows no real ID.
    manifest["transcripts"] = sorted(files) if claude else files

    # Extra JSONL files keep only the path and identifier replacements,
    # for example the event stream of `codex exec --json`.
    extras = []
    for name, src in sorted(record.get("extras", {}).items()):
        sanitize_jsonl(src, os.path.join(out_dir, name), lambda s, r: s.value(r), s)
        extras.append(name)

    manifest["extras"] = extras

    with open(os.path.join(out_dir, "manifest.json"), "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)
        f.write("\n")

    if claude and id_map:
        with open(id_map, "w", encoding="utf-8") as f:
            json.dump({"ids": s.ids.entries()}, f, ensure_ascii=False, indent=1)
            f.write("\n")

    problems = s.check(out_dir)
    if claude:
        problems += residual(out_dir, s.ids, lambda path: s.label(out_dir, path))

    if problems:
        sys.exit("sanitization incomplete:\n  " + "\n  ".join(sorted(set(problems))))

    print("sanitized %d hook payloads and %d transcript files into %s" % (len(manifest["hooks"]), len(files), out_dir))


if __name__ == "__main__":
    main()
