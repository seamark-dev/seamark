#!/usr/bin/env python3
"""Sanitize one native recording for the committed evidence.

Usage: capture-native-sanitize.py <record.json> <out-dir>

record.json is written by scripts/capture-native.sh record. It names the
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
that a decoder reads. It refuses to finish when a deny-listed value is
still present in any output file.
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


def claude_slug(path):
    """Return the project directory name that Claude Code derives."""
    return "".join(c if c.isascii() and c.isalnum() else "-" for c in path)


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

    def text(self, s):
        for old, new in self.pairs:
            s = s.replace(old, new)

        if self.user_word:
            s = self.user_word.sub(USER, s)

        s = CLAUDE_TASK_ROOT.sub(TASKS, s)
        s = TIMEZONE_TAG.sub("<timezone>%s</timezone>" % TIMEZONE, s)
        s = DATA_URL_BYTES.sub(r"\g<1>" + ATTACHMENT_BYTES, s)

        return s

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
        """Sanitize one member of a JSON object.

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

        return self.value(v)

    def check(self, out_dir):
        """Fail when a deny-listed value or an e-mail address remains.

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
                        problems.append("%s: holds a deny-listed value" % path)

                for m in EMAIL.findall(data):
                    if m not in ALLOWED_EMAILS:
                        problems.append("%s: holds an e-mail address" % path)

        return problems


def dump_line(v):
    return json.dumps(v, ensure_ascii=False, separators=(",", ":")) + "\n"


def claude_record(s, r):
    """Sanitize one Claude transcript record."""
    t = r.get("type")

    if t in CLAUDE_REQUEST_RECORDS:
        keep = {k: r[k] for k in ("type", "sessionId", "agentId", "version", "timestamp") if k in r}
        keep["sanitized"] = True
        return s.value(keep)

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

    return s.value(r)


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


def hooks(record, s, out_dir):
    """Write the observed hook payloads in time order and the manifest.

    The manifest holds no native hook time. The lesson hooks run only on
    some of the observed events and tools, so a duration in the debug log
    cannot be paired with one observed payload by its event.
    """
    observed = record["observed"]
    metas = []

    for name in os.listdir(observed):
        if name.endswith(".meta.json"):
            with open(os.path.join(observed, name), encoding="utf-8") as f:
                meta = json.load(f)
            meta["file"] = name[: -len(".meta.json")] + ".json"
            metas.append(meta)

    metas.sort(key=lambda m: m["start_ns"])
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


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: capture-native-sanitize.py <record.json> <out-dir>")

    with open(sys.argv[1], encoding="utf-8") as f:
        record = json.load(f)

    out_dir = sys.argv[2]
    if os.path.exists(out_dir):
        shutil.rmtree(out_dir)
    os.makedirs(out_dir)

    s = Sanitizer(record)
    client = record["client"]
    manifest = {"client": client, "client_version": record["client_version"], "placeholders": {
        "workspace": WORKSPACE, "home": HOME, "user": USER, "seamark": SEAMARK,
        "instrumentation": INSTRUMENTATION, "claude_tasks": TASKS, "account": ACCOUNT,
        "attachment_bytes": ATTACHMENT_BYTES,
    }}

    if record.get("observed") and os.listdir(record["observed"]):
        manifest["hooks"] = hooks(record, s, out_dir)
    else:
        manifest["hooks"] = []

    fn = claude_record if client == "claude" else codex_record
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

    manifest["transcripts"] = files

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

    problems = s.check(out_dir)
    if problems:
        sys.exit("sanitization incomplete:\n  " + "\n  ".join(sorted(set(problems))))

    print("sanitized %d hook payloads and %d transcript files into %s" % (len(manifest["hooks"]), len(files), out_dir))


if __name__ == "__main__":
    main()
