#!/usr/bin/env python3
"""Test instrumentation of the native recordings.

This is not a seamark hook. scripts/native-record.sh record installs it
beside the lesson hook entries, for the events of the scenario, so that
the recording keeps every native payload exactly as the client sent it.

Usage: native-record-observer.py <out-dir> <client>

It copies stdin into <out-dir>/NNN-<event>.json and writes the process
directory, CLAUDE_PROJECT_DIR, the payload size, and the start time into
<out-dir>/NNN-<event>.meta.json. It prints "{}" for codex, because Codex
reads Stop stdout as JSON, and nothing for claude, because Claude Code
adds hook stdout to the context.
"""

import json
import os
import sys
import time


def main():
    out_dir, client = sys.argv[1], sys.argv[2]
    start = time.time_ns()
    payload = sys.stdin.buffer.read()
    os.makedirs(out_dir, exist_ok=True)

    event = "unknown"
    try:
        event = json.loads(payload).get("hook_event_name", "unknown")
    except (ValueError, AttributeError):
        pass

    # Exclusive creation gives each payload its own number, also when the
    # client runs two hooks of one event at the same time.
    n = 1
    while True:
        path = os.path.join(out_dir, "%03d-%s.json" % (n, event))
        try:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            break
        except FileExistsError:
            n += 1

    with os.fdopen(fd, "wb") as f:
        f.write(payload)

    meta = {
        "event": event,
        "bytes": len(payload),
        "start_ns": start,
        "process_cwd": os.getcwd(),
        "CLAUDE_PROJECT_DIR": os.environ.get("CLAUDE_PROJECT_DIR"),
    }

    with open(path[: -len(".json")] + ".meta.json", "w", encoding="utf-8") as f:
        json.dump(meta, f, indent=1)

    if client == "codex":
        sys.stdout.write("{}")


if __name__ == "__main__":
    main()
