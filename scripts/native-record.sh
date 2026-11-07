#!/usr/bin/env sh
# Native recordings of one client CLI with the lesson hooks. One mode:
#
#   record  paid: runs the prompts of one scenario with the installed
#           client, the operator's own login, and the cheapest model. It
#           prints one cost line per paid run. It refuses a binary or a
#           tree with uncommitted changes unless SEAMARK_ALLOW_DIRTY=1. It
#           writes the raw recording and a sanitized copy into an output
#           directory for review. It never writes into testdata. It
#           turns off the advisor tool of Claude Code for each Claude
#           run, and records the advisor state of each run. A run with
#           the advisor on or unknown prints FAIL (see claude_print). A
#           Codex run at a usage or account limit prints FAIL and stops
#           the call before the next run (see codex_verdict).
#
# The script has no offline mode. The Go tests replay a reviewed
# recording offline (make native-replay).
#
# Record settings (environment):
#   NATIVE_RECORD_OUT     output directory (default: a new temporary one)
#   NATIVE_RECORD_REPO    scratch repository (default: <out>/repo); the
#                         script reuses an existing one to keep Codex hook
#                         trust
#   NATIVE_RECORD_RUNS    0 skips the paid runs and only collects
#   NATIVE_RECORD_CLAUDE_MODEL   default haiku
#   NATIVE_RECORD_CODEX_MODEL    default gpt-5.6-luna (low reasoning)
#   NATIVE_RECORD_SCENARIO       timing (the default) runs the timing
#                                prompts in a scratch repository with
#                                lesson hooks (see "The timing scenario"
#                                below); guard runs the guard sentinel
#                                prompts in a scratch repository with
#                                enforce pins (see "The guard scenario"
#                                below).
#
# Every check prints ok, FAIL, or blocked. The exit status is 0 when
# every check prints ok, 1 when one prints FAIL, and 3 when one prints
# blocked. A blocked check is not a pass, because a missing precondition
# gives no evidence. The script works with a plain POSIX shell. It needs
# git and python3.
set -eu

usage() {
    echo "usage: $0 record <claude|codex> <path-to-seamark-binary>" >&2
    exit 2
}

[ $# -eq 3 ] || usage
MODE=$1
CLIENT=$2
[ -x "$3" ] || usage

# The shared helpers sit beside this script.
. "$(dirname "$0")/smoke-lib.sh"
resolve_binary "$3"

case "$MODE" in
    record) ;;
    *) usage ;;
esac

case "$CLIENT" in
    claude) CLI=claude ;;
    codex)  CLI=codex ;;
    *) echo "$0: unknown client \"$CLIENT\" (known: claude, codex)" >&2; exit 2 ;;
esac

# The scenario selects the prompts of record. The script refuses an
# unknown name before any setup, so a typing error never starts a paid
# run of the wrong scenario.
SCENARIO=${NATIVE_RECORD_SCENARIO:-timing}

case "$SCENARIO" in
    timing|guard) ;;
    *) echo "$0: unknown scenario \"$SCENARIO\" (known: timing, guard)" >&2; exit 2 ;;
esac

SEAMARK_REPO=$(cd "$(dirname "$0")/.." && pwd -P)
SCRIPTS=$SEAMARK_REPO/scripts
DATA=$SEAMARK_REPO/internal/integration/testdata/$CLIENT/$SCENARIO

OK=0
FAILED=0
BLOCKED=0
SUMMARIZED=

ok()      { OK=$((OK + 1));           echo "  ok       $1"; }
fail()    { FAILED=$((FAILED + 1));   echo "  FAIL     $1" >&2; }
blocked() { BLOCKED=$((BLOCKED + 1)); echo "  blocked  $1"; }
note()    { echo "  note     $1"; }

summary() {
    SUMMARIZED=1
    echo
    echo "native-record $MODE ($CLIENT): $OK ok, $FAILED failed, $BLOCKED blocked"

    if [ "$FAILED" -gt 0 ]; then
        echo "result: FAIL"
        exit 1
    fi

    if [ "$BLOCKED" -gt 0 ]; then
        echo "result: incomplete (a blocked check is not a pass)"
        exit 3
    fi

    echo "result: ok"
}

# new_repo <dir>: creates a scratch Git repository with one commit and a
# subdirectory, the layout of the recording.
new_repo() {
    mkdir -p "$1/sub"
    git -C "$1" init -q -b main
    git -C "$1" config user.name native
    git -C "$1" config user.email native@example.invalid
    printf 'scratch repository for a native recording\n' > "$1/README.md"
    printf 'keep\n' > "$1/sub/.keep"
    git -C "$1" add README.md sub/.keep
    git -C "$1" commit -q -m fixture
}

echo "native-record $MODE — client $CLIENT"
echo "  seamark   $("$BIN" version)"
echo "  date      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# Under set -e, a failed command outside the checks stops the script.
# The exit trap then reports a blocked check and prints the summary.
finish() {
    status=$?

    if [ -z "$SUMMARIZED" ]; then
        blocked "the run stopped: a command outside the checks failed (exit status $status)"
        summary
    fi
}
trap finish EXIT

record_mode() {
    if ! command -v "$CLI" >/dev/null 2>&1; then
        blocked "$CLI is not on PATH; install it to record"
        summary
    fi

    if ! command -v python3 >/dev/null 2>&1; then
        blocked "python3 is not on PATH; the instrumentation and the sanitizer need it"
        summary
    fi

    echo "  $CLI    $("$CLI" --version 2>&1 | head -1)"

    # A paid run from an uncommitted tree records evidence against code
    # that no commit names. So the script refuses such a run unless the
    # operator sets SEAMARK_ALLOW_DIRTY.
    if [ -z "${SEAMARK_ALLOW_DIRTY:-}" ]; then
        case "$("$BIN" version)" in
            *-dirty*)
                blocked "the seamark binary was built from a dirty tree; commit and rebuild, or set SEAMARK_ALLOW_DIRTY=1"
                summary ;;
        esac

        if [ -n "$(git -C "$SEAMARK_REPO" status --porcelain 2>/dev/null)" ]; then
            blocked "the seamark working tree is dirty; commit first, or set SEAMARK_ALLOW_DIRTY=1"
            summary
        fi
    fi

    # A recording replaces only the reviewed evidence of its scenario. The
    # note on the trust step names the scenario, so a rerun keeps it.
    # The scenario repository registers the MCP server by the bare command
    # seamark (see timing_codex). So the trust step puts the directory of
    # the round binary first on PATH, as the paid runs do. The note keeps
    # $PATH literal, so the operator's shell expands it.
    SCENARIO_ARG=" NATIVE_RECORD_SCENARIO=$SCENARIO"
    CODEX_TRUST="PATH=$(dirname "$BIN"):\$PATH codex"

    OUT=${NATIVE_RECORD_OUT:-$(mktemp -d)}
    mkdir -p "$OUT"
    OUT=$(cd "$OUT" && pwd -P)
    REPO=${NATIVE_RECORD_REPO:-$OUT/repo}
    INSTR=$REPO/.native-instrumentation
    OBS=$INSTR/observed
    echo "  output    $OUT"
    echo "  repo      $REPO"
    echo "  scenario  $SCENARIO"
    echo

    echo "scratch repository"

    # The setup of a scenario writes its marker file last. So a marker
    # names a repository that an earlier call set up completely. The
    # marker is a file of this script, so the check reads no Seamark
    # state.
    reuse=
    for marker in "$INSTR"/scenario-*; do
        [ ! -f "$marker" ] || reuse=1
    done

    mkdir -p "$REPO"

    # The physical path goes into the hook commands, so every later path
    # derives from it.
    REPO=$(cd "$REPO" && pwd -P)
    INSTR=$REPO/.native-instrumentation
    OBS=$INSTR/observed

    if [ -n "$reuse" ]; then
        note "reusing $REPO and its hook entries, so that client trust stays valid"

        # Each scenario installs its own hooks, so a repository keeps the
        # scenario of its setup.
        set_up=
        [ ! -f "$INSTR/scenario-timing" ] || set_up=timing
        [ ! -f "$INSTR/scenario-guard" ] || set_up=guard

        if [ "$set_up" != "$SCENARIO" ]; then
            blocked "$REPO was set up for the ${set_up:-another} scenario, not $SCENARIO; use another NATIVE_RECORD_REPO"
            summary
        fi
    else
        case "$SCENARIO" in
            timing) timing_setup ;;
            guard)  guard_setup ;;
        esac
    fi

    # Codex runs project hooks only after a trust review. A new timing or
    # guard repository has no trusted hook yet, so a paid run there
    # records no hook payload. The script stops before the paid runs
    # instead.
    if [ "$CLIENT" = codex ] && [ -z "$reuse" ]; then
        note "Codex runs project hooks only after you trust them; setup never grants trust:"
        note "  1. cd $REPO && $CODEX_TRUST   (accept the project trust prompt)"
        note "  2. review the hooks (\"Hooks need review\", or /hooks), trust every entry, then quit"
        note "  3. NATIVE_RECORD_REPO=$REPO make native-record CLIENT=codex$SCENARIO_ARG"
        blocked "no paid run: the new Codex hooks need your trust review first"
        summary
    fi

    : > "$OUT/runs.txt"
    rm -f "$OUT/advisor.jsonl"
    SESSION=

    # The hook processes start as children of the client, so they get
    # this switch from the environment of the paid run. With it, the
    # lesson hooks also write the evaluation and reset records that the
    # timing replay reads.
    SEAMARK_RECORD_EVALUATIONS=1
    export SEAMARK_RECORD_EVALUATIONS

    if [ "${NATIVE_RECORD_RUNS:-1}" = 0 ]; then
        note "NATIVE_RECORD_RUNS=0: no paid run; collecting what the repository already holds"
    else
        echo "paid runs ($CLIENT; costs tokens)"
        case "$SCENARIO:$CLIENT" in
            timing:claude) timing_begin; record_claude_timing ;;
            timing:codex)  timing_begin; record_codex_timing ;;
            guard:claude)  guard_begin; record_claude_guard ;;
            guard:codex)   guard_begin; record_codex_guard ;;
        esac
    fi

    echo "sanitized copy"
    # The transcripts are the files that the recorded payloads name, and
    # the session file of the paid run. The script leaves out a path that
    # names no file, for example the compaction summarizer's.
    python3 -I - "$CLIENT" "$("$CLI" --version 2>&1 | head -1)" "$REPO" "$BIN" "$INSTR" "$OBS" "$OUT" "$SESSION" <<'PY'
import glob, json, os, sys

client, version, repo, seamark, instr, observed, out, session = sys.argv[1:9]
home = os.path.expanduser("~")
version = version.split()[0] if client == "claude" else version.split()[-1]
paths = set()

for name in glob.glob(os.path.join(observed, "*.json")):
    if name.endswith(".meta.json"):
        continue
    with open(name) as f:
        payload = json.load(f)
    for key in ("transcript_path", "agent_transcript_path"):
        if payload.get(key):
            paths.add(payload[key])

if client == "claude":
    base = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.join(home, ".claude")
    slug = "".join(c if c.isascii() and c.isalnum() else "-" for c in repo)
    if session:
        paths.add(os.path.join(base, "projects", slug, session + ".jsonl"))
    for p in list(paths):
        sub = os.path.join(p[: -len(".jsonl")], "subagents")
        paths.update(glob.glob(os.path.join(sub, "agent-*.jsonl")))
        paths.update(glob.glob(os.path.join(sub, "agent-*.meta.json")))
else:
    base = os.environ.get("CODEX_HOME") or os.path.join(home, ".codex")
    if session:
        paths.update(glob.glob(os.path.join(base, "sessions", "*", "*", "*", "rollout-*-%s.jsonl" % session)))

runs = []
with open(os.path.join(out, "runs.txt")) as f:
    runs = [int(line) for line in f if line.strip()]

record = {
    "client": client, "client_version": version, "repo": repo, "home": home, "seamark": seamark,
    "instrumentation": instr, "observed": observed, "runs": runs,
    # guard_extras reads the exit status of each hook from these logs.
    "debug_logs": sorted(glob.glob(os.path.join(out, "run-*-debug.log"))),
    "transcripts": sorted(p for p in paths if os.path.isfile(p)),
    "extras": {"exec-events.jsonl": os.path.join(out, "exec-events.jsonl")}
    if os.path.isfile(os.path.join(out, "exec-events.jsonl")) else {},
}

# The advisor state of each Claude run goes into the sanitized copy, so
# the reviewer sees it beside the transcripts.
if os.path.isfile(os.path.join(out, "advisor.jsonl")):
    record["extras"]["advisor.jsonl"] = os.path.join(out, "advisor.jsonl")

# A compaction summary can name a shortened path of the scratch
# repository, which the path pairs of the sanitizer do not match. So the
# distinctive names of the scratch directories go on the deny list. The
# sanitizer replaces each of them with one fixed word, and its deny check
# proves that none is left. A distinctive name has 8 or more characters
# and a digit or a hyphen. Thus a common word such as projects never
# changes, and a name of the home path stays out too.
if client == "claude":
    own = set(home.split(os.sep))
    names = (set(os.path.dirname(repo).split(os.sep)) | set(out.split(os.sep))) - own
    record["deny"] = sorted(n for n in names if len(n) >= 8 and any(c.isdigit() or c == "-" for c in n))

with open(os.path.join(out, "record.json"), "w") as f:
    json.dump(record, f, indent=1)
PY

    case "$SCENARIO" in
        timing) timing_extras ;;
        guard)  guard_extras ;;
    esac

    # The ID pass of a Claude Code recording writes its map from real to
    # synthetic IDs beside the raw recording. The map holds the real IDs,
    # so it stays outside the sanitized copy. The reviewer reads it to
    # check that the pass changed only IDs.
    id_map=
    [ "$CLIENT" != claude ] || id_map=$OUT/id-map.json

    if python3 -I "$SCRIPTS/native-record-sanitize.py" ${id_map:+--id-map "$id_map"} "$OUT/record.json" "$OUT/sanitized"; then
        ok "sanitized copy in $OUT/sanitized; compare it with $DATA before you replace the evidence"
        [ -z "$id_map" ] || note "the ID map is $id_map; it holds the real IDs, so it never goes into testdata"
    else
        fail "the sanitizer refused the recording; nothing in $OUT/sanitized is safe to commit"
    fi

    if [ -z "$(find "$OBS" -name '*.json' 2>/dev/null | head -1)" ]; then
        blocked "no $CLIENT hook ran in $REPO, so no hook payload exists"
        if [ "$CLIENT" = codex ]; then
            note "Codex runs project hooks only after you trust them; setup never grants trust:"
            note "  1. cd $REPO && $CODEX_TRUST   (accept the project trust prompt)"
            note "  2. review the hooks (\"Hooks need review\", or /hooks) and trust every entry"
            note "  3. type one short prompt, for example: run the shell command true; then quit"
            note "  4. NATIVE_RECORD_REPO=$REPO make native-record CLIENT=codex$SCENARIO_ARG"
        fi
    else
        ok "the hooks recorded $(find "$OBS" -name '*.meta.json' | wc -l | tr -d ' ') payloads"
    fi
}

# claude_print <run> <claude arguments...> runs one print-mode session
# in the scratch repository. The prompt comes from prompt-<run>.txt. The
# result goes to run-<run>.json, and stderr goes to run-<run>-stderr.txt.
# It returns the exit status of claude. Each paid Claude run goes through
# this function, so each run gets the same advisor switch.
#
# Claude Code 2.1.314 has a server-side advisor tool that calls a stronger
# model. So a run can call a model that --model does not name. The client
# turns the tool on through a server flag or through
# CLAUDE_CODE_ENABLE_EXPERIMENTAL_ADVISOR_TOOL, and only for some API
# providers. This script cannot see or set the server flag. The client
# checks CLAUDE_CODE_DISABLE_ADVISOR_TOOL before both, so
# CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1 keeps the tool off. The change log of
# the installed client names this switch.
#
# The function removes the result, stderr, and debug files of the run
# before claude starts. Claude Code appends to the --debug-file path, and
# a file of an earlier call of this script can use the same run number.
# Without the removal, claude_advisor can read the advisor line of that
# earlier run. The function then records the advisor state of the run,
# also after a failed run.
claude_print() {
    print_run=$1
    shift
    print_status=0
    rm -f "$OUT/run-$print_run.json" "$OUT/run-$print_run-stderr.txt" "$OUT/run-$print_run-debug.log"
    (cd "$REPO" && CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1 claude -p "$@" < "$OUT/prompt-$print_run.txt" \
        > "$OUT/run-$print_run.json" 2> "$OUT/run-$print_run-stderr.txt") || print_status=$?
    claude_advisor "$print_run"
    return "$print_status"
}

# claude_advisor <run> prints the advisor state of one Claude run, and
# appends it to advisor.jsonl. Claude Code writes the debug line
# "[AdvisorTool] Server-side tool enabled with <model> as the advisor
# model" only when the tool is on. It writes no line when the tool is
# off. So a debug log without that line reads off. A missing or empty
# debug log reads unknown, because it shows nothing.
#
# A state of on or unknown prints FAIL, so the summary counts it. No
# recording run may use the advisor, and unknown gives no proof that the
# tool was off. The FAIL line comes in addition to the advisor record and
# the exit status of the run, so the run keeps its own cost and result.
claude_advisor() {
    advisor_log=$OUT/run-$1-debug.log
    advisor_model=

    if [ ! -s "$advisor_log" ]; then
        advisor_state=unknown
    elif grep -q -F '[AdvisorTool] Server-side tool enabled with' "$advisor_log"; then
        advisor_state=on
        advisor_model=$(sed -n 's/.*Server-side tool enabled with \([^" \\]*\) as the advisor model.*/\1/p' "$advisor_log" | head -1)
    else
        advisor_state=off
    fi

    printf '{"run": %d, "advisor": "%s", "advisor_model": "%s", "switch": "CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1"}\n' \
        "$1" "$advisor_state" "$advisor_model" >> "$OUT/advisor.jsonl"

    case "$advisor_state" in
        on)
            echo "  advisor  run $1: on, with the advisor model ${advisor_model:-(not named)}"
            fail "run $1 used the advisor tool; no recording run may use it" ;;
        unknown)
            echo "  advisor  run $1: unknown"
            fail "run $1 has no advisor state, because its debug log is missing or empty" ;;
        *)
            echo "  advisor  run $1: $advisor_state" ;;
    esac
}

# codex_failure <events> reads the `codex exec --json` events of one turn.
# Codex reports a failed turn in the event stream, not on stderr: a usage
# limit gave an error event and a turn.failed event, and stderr was empty.
# The function prints the message of the failure on one line. Its exit
# status is the verdict:
#   0  no turn.failed event and no limit message; the line holds the
#      message of the last error event, or nothing
#   1  a turn.failed event; the line holds its message, or else the
#      message of the last error event
#   2  a turn.failed or error message reports a usage or account limit;
#      the line holds that message
# Only the words "usage limit" and "account limit" count as a limit. A
# plain rate limit can pass after a retry, so it does not stop the call.
# The line drops control characters, because the message goes to the
# terminal.
codex_failure() {
    python3 -I - "$1" <<'PY'
import json, re, sys

LIMIT = re.compile(r"\b(usage|account)[ _-]limit", re.IGNORECASE)


def message(event):
    # A turn.failed event nests the message under error. An error event
    # holds it at the top level.
    error = event.get("error")
    if isinstance(error, dict) and isinstance(error.get("message"), str):
        return error["message"]
    if isinstance(event.get("message"), str):
        return event["message"]
    return ""


def one_line(text):
    text = "".join(c if c.isprintable() else " " for c in text)
    text = " ".join(text.split())
    return text if len(text) <= 400 else text[:397] + "..."


failed, failed_text, error_text, limit_text = False, "", "", ""

try:
    with open(sys.argv[1], encoding="utf-8", errors="replace") as f:
        lines = f.readlines()
except OSError:
    lines = []

for raw in lines:
    try:
        event = json.loads(raw)
    except ValueError:
        continue
    if not isinstance(event, dict) or event.get("type") not in ("turn.failed", "error"):
        continue

    text = one_line(message(event))
    if event["type"] == "turn.failed":
        failed = True
        failed_text = text or failed_text
    else:
        error_text = text or error_text
    if not limit_text and LIMIT.search(text):
        limit_text = text

if limit_text:
    print(limit_text)
    sys.exit(2)

if failed:
    print(failed_text or error_text)
    sys.exit(1)

print(error_text)
PY
}

# codex_verdict <label> <exit status> <events> <stderr> judges one Codex
# turn. It returns 0 for a turn that passed. For a failed turn it prints
# FAIL with the event message, or else the last stderr line, and returns
# 1. A turn that exits 0 and still sends turn.failed is a failed turn.
# At a usage or account limit it prints FAIL and stops the record call,
# because every later turn fails the same way and the stop rule must not
# depend on an operator who watches the output.
codex_verdict() {
    verdict_status=0
    verdict_reason=$(codex_failure "$3") || verdict_status=$?

    if [ "$verdict_status" = 2 ]; then
        fail "$1 stopped at a Codex usage or account limit: $verdict_reason"
        limit_stop
    fi

    if [ "$2" = 0 ] && [ "$verdict_status" = 0 ]; then
        return 0
    fi

    [ -n "$verdict_reason" ] || verdict_reason=$(tail -1 "$4" 2> /dev/null || true)
    [ -n "$verdict_reason" ] || verdict_reason="codex exited with status $2 and printed no message"
    fail "$1 failed: $verdict_reason"
    return 1
}

# limit_stop ends the record call after a Codex limit, before the next
# run. The guard scenario first puts back the enforce setup, so the
# reused repository keeps the hook document that the operator trusted.
# The summary exits with status 1, because the limit line counts as FAIL.
limit_stop() {
    [ "$SCENARIO" != guard ] || guard_restore
    note "no later run starts; run the record call again after the limit resets"
    summary
}

# The timing scenario (NATIVE_RECORD_SCENARIO=timing). It records
# when lesson advice reaches the model: a prior result before a later
# model step, a reminder on its own call, edits that one response
# constructs together, a failed read, and a subagent. Each paid run is
# one case. timing-cases.jsonl names the case of each run, so the replay
# tests need no session identifier. The reviewer copies the reviewed
# sanitized copy into internal/integration/testdata/<client>/timing.
# There the tests TestNativeRecordClaudeTimingRecording and
# TestNativeRecordCodexTimingRecording decode it.

# timing_setup creates the scratch repository of the timing scenario.
# Three committed fixture files lie under timing/, and one advisory pin
# with a check covers that region. The pin note holds a code word, so a
# reviewer can find the advice in the transcripts. `seamark init`
# installs the lesson hooks of the round binary there. The paid runs set
# the recording switch, because only then do the hooks write the turn and
# reset evidence that a timing reading needs. The exploration hook is
# wired by hand, because `seamark init` does not install it yet.
timing_setup() {
    new_repo "$REPO"
    mkdir -p "$REPO/timing"

    for f in prior intra child; do
        printf 'alpha\n' > "$REPO/timing/$f.txt"
    done

    git -C "$REPO" add timing
    git -C "$REPO" commit -q -m 'timing fixture'

    mkdir -p "$REPO/.seamark"
    cat > "$REPO/.seamark/lessons.yaml" <<'YAML'
# The synthetic lesson of the timing scenario
# (scripts/native-record.sh, NATIVE_RECORD_SCENARIO=timing).
hook_delivery: once-per-context
lesson_checks_version: 1
checks:
  timing-marker:
    kind: no-added-pattern/1
    pattern: 'TIMING-MARKER'
pin:
  - rule: timing-sentinel
    region: timing/
    note: 'Gate-3 timing lesson. The code word is cobalt-heron. Keep each file under timing/ to one line.'
    check: timing-marker
YAML
    ok "fixture files and one pin with a check under timing/"

    "$BIN" -C "$REPO" init --client "$CLIENT" > "$OUT/init.txt" 2>&1
    ok "init --client $CLIENT installed the lesson hooks"

    mkdir -p "$OBS"
    cp "$SCRIPTS/native-record-observer.py" "$INSTR/observer.py"
    # The observer also runs on PreToolUse, because the edit hook reads
    # that event. The entries go into the documents that init wrote,
    # beside the lesson hook entries.
    python3 -I - "$CLIENT" "python3 -I $INSTR/observer.py $OBS $CLIENT" "$BIN lessons --explore --hook" "$REPO" <<'PY'
import json, os, sys

client, observer, explore, repo = sys.argv[1:5]
tools = {"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied"}

if client == "claude":
    events = ["UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied", "Stop",
              "StopFailure", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact",
              "SessionStart", "SessionEnd"]
    path = os.path.join(repo, ".claude", "settings.json")
else:
    events = ["SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SubagentStart",
              "SubagentStop", "PreCompact", "PostCompact", "Interrupt", "SessionEnd"]
    path = os.path.join(repo, ".codex", "hooks.json")

doc = {"hooks": {}}
if os.path.exists(path):
    with open(path) as f:
        doc = json.load(f)

hooks = doc.setdefault("hooks", {})

for e in events:
    timeout = 3 if client == "codex" and e in ("Interrupt", "SessionEnd") else 10
    entry = {"hooks": [{"type": "command", "command": observer, "timeout": timeout}]}
    if e in tools:
        entry["matcher"] = "*"
    hooks.setdefault(e, []).append(entry)

# The exploration hook on both Read events. The failed-read entry is test
# instrumentation: the recording shows whether its reply reaches the model.
if client == "claude":
    for e in ("PostToolUse", "PostToolUseFailure"):
        entry = {"matcher": "Read", "hooks": [{"type": "command", "command": explore, "timeout": 10}]}
        hooks.setdefault(e, []).append(entry)

os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "w") as f:
    json.dump(doc, f, indent=2)
PY
    ok "instrumentation entries added beside the lesson hook entries"

    : > "$INSTR/scenario-timing"
}

# timing_begin keeps one timing recording to the runs of one call of the
# script. It moves the hook payloads of an earlier call aside, notes the
# size of the audit log, and starts an empty case list.
timing_begin() {
    if [ -n "$(find "$OBS" -name '*.json' 2>/dev/null | head -1)" ]; then
        mv "$OBS" "$INSTR/observed-before-$(date +%s)"
        mkdir -p "$OBS"
        note "moved the hook payloads of an earlier call aside"
    fi

    AUDIT_START=0
    [ ! -f "$REPO/.seamark/lessons-audit.jsonl" ] || AUDIT_START=$(wc -c < "$REPO/.seamark/lessons-audit.jsonl" | tr -d ' ')
    : > "$OUT/timing-cases.jsonl"
}

# timing_start <case> starts one paid run of a timing case. The start
# time assigns each later hook payload to this run, and the case list
# names the case of the run.
timing_start() {
    RUN=$(( $(wc -l < "$OUT/runs.txt") + 1 ))
    perl -MTime::HiRes=time -e 'printf "%d\n", time * 1e9' >> "$OUT/runs.txt" 2>/dev/null || echo "$(date +%s)000000000" >> "$OUT/runs.txt"
    printf '{"run": %d, "case": "%s"}\n' "$RUN" "$1" >> "$OUT/timing-cases.jsonl"
}

# timing_claude <case> <new|resume> <prompt> runs one print-mode session
# of a timing case. A resumed run continues the session of the run before
# it. Every run gets the same tools and the read-only subagent.
timing_claude() {
    tcase=$1
    session=$2

    if [ "$session" = resume ] && [ -z "$SESSION" ]; then
        fail "$tcase: no session to resume, because the run before it failed"
        return
    fi

    timing_start "$tcase"
    printf '%s' "$3" > "$OUT/prompt-$RUN.txt"

    set -- --model "$model" --output-format json --setting-sources project,local --strict-mcp-config \
        --max-budget-usd 0.40 --debug hooks --debug-file "$OUT/run-$RUN-debug.log" \
        --allowedTools Read Edit Write Bash Agent Task --agents "$agents"
    [ "$session" = new ] || set -- "$@" --resume "$SESSION"

    if claude_print "$RUN" "$@"; then
        line=$(python3 -I -c '
import json, sys
d = json.load(open(sys.argv[1]))
print(d.get("session_id", ""), d.get("total_cost_usd", 0), d.get("is_error"))' "$OUT/run-$RUN.json")
        SESSION=${line%% *}
        echo "  cost     run $RUN ($tcase): claude -p --model $model: total_cost_usd $(printf '%s' "$line" | cut -d' ' -f2) (a resumed session reports the session total)"

        if [ "$(printf '%s' "$line" | cut -d' ' -f3)" = True ]; then
            fail "run $RUN ($tcase) reported an error; see $OUT/run-$RUN.json"
        else
            ok "run $RUN ($tcase) finished (session $SESSION)"
        fi
    else
        SESSION=
        fail "run $RUN ($tcase) failed: $(tail -1 "$OUT/run-$RUN-stderr.txt")"
    fi
}

# record_claude_timing runs the eight Claude Code timing cases. Each case
# starts a new session, except new-model-step and compact-reset.
# new-model-step resumes the session of prior-result, so its edit comes
# one turn after the read. compact-reset resumes the session of
# new-model-step and compacts it. That context then holds an exploration
# injection and an edit injection, so the reset record shows that both
# kinds of delivery state start a new generation. The reader subagent has
# only the Read tool.
#
# The /compact instruction keeps the summary short and free of paths,
# because the summary goes into hook payloads and transcripts. Claude Code
# builds the PreCompact payload from the typed text, and the observer
# prints nothing on PreCompact, so the summary instruction stays equal to
# that text.
record_claude_timing() {
    # Claude Code turns /compact off when DISABLE_COMPACT is set. The
    # compaction case would then give no reset record, and the runs before
    # it would cost money for nothing. So the scenario stops before run 1.
    if [ -n "${DISABLE_COMPACT:-}" ]; then
        blocked "DISABLE_COMPACT is set, so /compact cannot run; unset it to record the Claude timing scenario"
        summary
    fi

    model=${NATIVE_RECORD_CLAUDE_MODEL:-haiku}
    agents='{"reader":{"description":"Reads the one file it is given and reports its first line.","prompt":"Use the Read tool exactly once on the file that you are given, and reply with its first line only.","model":"'"$model"'","tools":["Read"]}}'
    steps='Do exactly these steps in order, and do not add steps.'

    timing_claude prior-result new "$steps
1. Use the Read tool to read the file timing/prior.txt.
Then reply with the single word: done"

    timing_claude new-model-step resume "$steps
1. Before any tool call, write one line: the code word of the seamark review lesson that you got for timing/prior.txt, or the word none.
2. Use the Edit tool to replace the line alpha with the line beta in timing/prior.txt.
Then reply with the single word: done"

    timing_claude compact-reset resume "/compact Keep the summary to one short sentence and name no file path."

    timing_claude read-then-edit new "$steps
1. Use the Read tool to read the file timing/intra.txt.
2. Use the Edit tool to replace the line alpha with the line beta in timing/intra.txt.
Then reply with the single word: done"

    timing_claude same-call new "$steps
1. Use the Write tool to create the new file timing/same.txt with the single line: gamma
Then reply with the single word: done"

    timing_claude batch-and-program new "$steps
1. In one single response, call the Write tool twice in parallel: create the new file timing/batch-a.txt with the single line: one, and create the new file timing/batch-b.txt with the single line: two.
2. Run this one Bash command exactly as written: $BIN lessons --prepare timing/program.txt && echo delta > timing/program.txt
Then reply with the single word: done"

    timing_claude failed-read new "$steps
1. Use the Read tool to read the file timing/missing.txt. The file does not exist, so the read fails. Do not create the file.
2. Write one line: the code word of the seamark review lesson that you got for timing/missing.txt, or the word none.
Then reply with the single word: done"

    # The prompt sets the background shape of the Agent call, for the
    # reason in the comment of record_claude_guard.
    timing_claude subagent-read new "$steps
1. Use the Agent tool with subagent_type \"reader\" and run_in_background set to true, and ask it to read the file timing/child.txt.
Then reply with the single word: done"
}

# timing_codex <case> <new|resume> <prompt> runs one `codex exec` turn of
# a timing case. The workspace-write sandbox lets apply_patch and the
# preparation command write in the scratch repository. A resumed run
# continues the thread of the run before it. It never passes
# --dangerously-bypass-hook-trust, because trust stays the operator's
# decision.
timing_codex() {
    tcase=$1
    session=$2

    if [ "$session" = resume ] && [ -z "$SESSION" ]; then
        fail "$tcase: no thread to resume, because the run before it failed"
        return
    fi

    timing_start "$tcase"
    printf '%s' "$3" > "$OUT/prompt-$RUN.txt"

    # `exec resume` has no --cd and no --sandbox flag. So the run starts
    # in the repository and sets the sandbox through the configuration.
    if [ "$session" = new ]; then
        set -- exec -m "$model" -c model_reasoning_effort=low --json -s workspace-write -C "$REPO" -
    else
        set -- exec resume -m "$model" -c model_reasoning_effort=low -c sandbox_mode=workspace-write --json "$SESSION" -
    fi

    # `seamark init` registers the MCP server by the bare command seamark,
    # because config.toml is shared between machines. Codex finds that
    # command on PATH. So the run puts the directory of the round binary
    # first on PATH. The MCP server then comes from the commit under test,
    # not from an older installed binary with other instructions.
    exec_status=0
    (cd "$REPO" && PATH="$(dirname "$BIN"):$PATH" codex "$@" < "$OUT/prompt-$RUN.txt" > "$OUT/run-$RUN-events.jsonl" 2> "$OUT/run-$RUN-stderr.txt") || exec_status=$?

    if codex_verdict "run $RUN ($tcase)" "$exec_status" "$OUT/run-$RUN-events.jsonl" "$OUT/run-$RUN-stderr.txt"; then
        cat "$OUT/run-$RUN-events.jsonl" >> "$OUT/exec-events.jsonl"
        line=$(python3 -I -c '
import json, sys
thread, usage = "", {}
for raw in open(sys.argv[1]):
    try:
        e = json.loads(raw)
    except ValueError:
        continue
    if e.get("type") == "thread.started":
        thread = e.get("thread_id", "")
    if e.get("type") == "turn.completed":
        usage = e.get("usage", {})
print(thread, " ".join("%s=%s" % kv for kv in sorted(usage.items())))' "$OUT/run-$RUN-events.jsonl")
        [ -z "${line%% *}" ] || SESSION=${line%% *}
        echo "  cost     run $RUN ($tcase): codex exec -m $model -c model_reasoning_effort=low: ${line#* }"
        ok "run $RUN ($tcase) finished (thread $SESSION)"
    else
        SESSION=
    fi
}

# record_codex_timing runs the five Codex timing cases. Each case starts
# a new thread, except patch-after-preparation: it resumes the thread of
# prior-preparation, so its patch comes one turn after the preparation.
record_codex_timing() {
    model=${NATIVE_RECORD_CODEX_MODEL:-gpt-5.6-luna}
    steps='Do exactly these steps in order, and do not add steps.'

    timing_codex prior-preparation new "$steps
1. Run the shell command: $BIN lessons --prepare timing/prior.txt
Then reply with the single word: done"

    timing_codex patch-after-preparation resume "$steps
1. Before any tool call, write one line: the code word of the seamark review lesson in the preparation output of the previous turn, or the word none.
2. Use apply_patch to replace the line alpha with the line beta in timing/prior.txt.
Then reply with the single word: done"

    timing_codex same-call new "$steps
1. Use apply_patch to add the new file timing/same.txt with the single line: gamma
Then reply with the single word: done"

    timing_codex multi-file-patch new "$steps
1. Use one single apply_patch call that adds two new files: timing/multi-a.txt with the single line: one, and timing/multi-b.txt with the single line: two.
Then reply with the single word: done"

    # The prompt names the agent tools as Codex lists them in code mode, and
    # the worker agent_type. The comment at the guard case subagent-violation
    # gives the reason.
    timing_codex subagent-patch new "$steps
1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to use apply_patch to add the new file timing/child-patch.txt with the single line: child.
2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.
Then reply with the single word: done"
}

# timing_extras adds the evidence of the timing scenario to record.json:
# the case list, the audit records of this call, and every transcript
# record that holds the hook reminder text. The sanitizer replaces the
# text of attachments and developer messages with a marker, so only this
# copy keeps the text that the model got. Assistant and reasoning
# records stay out, because the sanitizer removes thinking text.
timing_extras() {
    [ -f "$OUT/timing-cases.jsonl" ] || : > "$OUT/timing-cases.jsonl"

    python3 -I - "$OUT/record.json" "$REPO/.seamark/lessons-audit.jsonl" "${AUDIT_START:-0}" "$OUT" <<'PY'
import glob, json, os, sys

record_path, audit, start, out = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
header = "seamark — review lessons for"

with open(record_path, encoding="utf-8") as f:
    record = json.load(f)


def model_output(r):
    """Return True for a record that the model wrote."""
    msg = r.get("message")
    if isinstance(msg, dict) and msg.get("role") == "assistant":
        return True

    p = r.get("payload")
    if not isinstance(p, dict):
        return False

    item = p.get("item") if isinstance(p.get("item"), dict) else {}
    return p.get("role") == "assistant" or p.get("type") in ("reasoning", "agent_reasoning") or \
        item.get("type") == "Reasoning"


# The record step adds the rollout of the last Codex thread and the
# rollouts that payloads name. A timing call runs several threads, and a
# subagent payload can name no rollout. So every thread and agent ID of
# the payloads adds its own rollout.
if record["client"] == "codex":
    base = os.environ.get("CODEX_HOME") or os.path.join(record["home"], ".codex")
    ids = set()
    for name in glob.glob(os.path.join(record["observed"], "*.json")):
        if name.endswith(".meta.json"):
            continue
        with open(name, encoding="utf-8") as f:
            payload = json.load(f)
        ids.update(payload.get(k) for k in ("session_id", "agent_id") if payload.get(k))
    found = set(record.get("transcripts", []))
    for i in ids:
        found.update(glob.glob(os.path.join(base, "sessions", "*", "*", "*", "rollout-*-%s.jsonl" % i)))
    record["transcripts"] = sorted(found)

context = os.path.join(out, "hook-context.jsonl")
with open(context, "w", encoding="utf-8") as w:
    for path in record.get("transcripts", []):
        if not path.endswith(".jsonl"):
            continue
        with open(path, encoding="utf-8") as f:
            for line in f:
                if not line.strip():
                    continue
                r = json.loads(line)
                if header in json.dumps(r, ensure_ascii=False) and not model_output(r):
                    w.write(json.dumps(r, ensure_ascii=False) + "\n")

extras = record.setdefault("extras", {})
extras["hook-context.jsonl"] = context
extras["timing-cases.jsonl"] = os.path.join(out, "timing-cases.jsonl")

# The audit log can hold the records of an earlier call. Only the bytes
# after the size that timing_begin noted belong to this recording.
if os.path.isfile(audit):
    tail = os.path.join(out, "lessons-audit.jsonl")
    with open(audit, "rb") as f, open(tail, "wb") as w:
        f.seek(start)
        w.write(f.read())
    extras["lessons-audit.jsonl"] = tail

with open(record_path, "w", encoding="utf-8") as f:
    json.dump(record, f, indent=1)
PY
}

# The guard scenario (NATIVE_RECORD_SCENARIO=guard). It records
# whether the client stops a write that the
# lesson-check guard denies, and what it does with the replies of the
# post-edit hook. The scratch repository holds three fixture
# directories: sentinel/ under an enforce forbid-path-change/1 pin,
# notes/ under an enforce no-added-pattern/1 pin, and free/ without a
# pin. Each paid run is one case. guard-cases.jsonl names the case of
# each run and the state of the three directories before and after the
# run, so the replay tests need no session identifier. The reviewer
# copies the reviewed sanitized copy into
# internal/integration/testdata/<client>/guard. There the tests
# TestNativeRecordClaudeGuardRecording and
# TestNativeRecordCodexGuardRecording decode it, and
# TestGuardEvidenceMatchesRecordings checks the evidence against it.
#
# Each case runs in one phase. The phase sets the lessons.yaml variant,
# the form of the hook document, and the mode of the indirection entry:
#
#   enforce          the setup of init --lesson-checks enforce
#   check-pins       the same hooks; both pins have mode check
#   broken-config    the same hooks; one check kind does not exist
#   check-installed  init --lesson-checks check (Claude Code only)
#   post-only        no pre-write hook; the post-edit hooks stay
#   wrapper          no owned guard; the indirection entry runs the guard
#                    and discards its exit status (report-only)
#   slow             no owned guard; the indirection entry waits past the
#                    hook timeout and then runs the guard
#   slow-deny        no owned guard; the indirection entry waits inside the
#                    hook timeout and then runs the guard
#
# The cases after slow-guard came later than the first recordings. They
# run after the earlier cases, so each earlier case keeps its run number
# and its prompt, and an earlier recording still replays.
#
# The indirection entry is the command sh <instrumentation>/pre-write.sh.
# Its text holds no guard marker, so neither setup nor inspection reads
# it as a wrapper of the guard. Setup adds it once, and the phases select
# its behavior through the file pre-write-mode. A phase without the owned
# guard changes the hook document only by the removal of that entry.
#
# Codex keys the trust of a project hook by its position (event, group,
# handler) and compares a hash of its content at that key. A moved entry
# is "modified", and Codex does not run it without a new review. So the
# Codex setup puts the owned guard in the last PreToolUse group. Its
# removal then moves no other entry, and the one review of the enforce
# form covers every form that a Codex case runs with (see guard_keys).

# GUARD_SLOW_SECONDS is the wait of the slow mode. It is above the
# timeout of the guard hook, 10 seconds, so the client stops the slow
# entry before it runs the guard.
GUARD_SLOW_SECONDS=15

# GUARD_IN_BUDGET_SECONDS is the wait of the slow-deny mode. It is below
# the 10-second timeout of the guard hook, so a client that waits for its
# hook runs the guard to the end. A deny then shows that the client waits
# for a slow guard and honors its exit status.
GUARD_IN_BUDGET_SECONDS=7

# guard_vars sets the client parts of the guard scenario: the hook
# document, the marker of the owned guard, and the matcher of the guard
# spec. The values follow the guard hook specs that init writes.
guard_vars() {
    if [ "$CLIENT" = claude ]; then
        GUARD_DOC=$REPO/.claude/settings.json
        GUARD_MARKER="lessons --guard --hook"
        GUARD_MATCHER="Edit|Write|MultiEdit"
    else
        GUARD_DOC=$REPO/.codex/hooks.json
        GUARD_MARKER="lessons --guard --hook --client codex"
        GUARD_MATCHER=apply_patch
    fi
}

# guard_setup creates the scratch repository of the guard scenario. It
# commits the fixture files and writes the lessons.yaml variants.
# `seamark init --lesson-checks enforce` installs the guard and the
# post-edit hooks of the round binary there. Setup then adds the
# observer entries and the indirection entry, and keeps a copy of the
# hook document for the phases. For Codex, it moves the owned guard to
# the last PreToolUse group before it keeps the copy, because the operator
# reviews the trust of that copy.
guard_setup() {
    guard_vars
    new_repo "$REPO"
    mkdir -p "$REPO/sentinel" "$REPO/notes" "$REPO/free" "$REPO/.seamark" "$OBS"

    for f in edit update delete move; do
        printf 'alpha\n' > "$REPO/sentinel/$f.txt"
    done

    for f in unknown failure update; do
        printf 'alpha\n' > "$REPO/notes/$f.txt"
    done

    printf 'alpha\ngamma\n' > "$REPO/notes/multi.txt"
    printf 'one\ntwo\nthree\n' > "$REPO/free/oracle.txt"
    git -C "$REPO" add sentinel notes free
    git -C "$REPO" commit -q -m 'guard fixture'

    guard_lessons
    cp "$INSTR/lessons-enforce.yaml" "$REPO/.seamark/lessons.yaml"
    ok "fixture files under sentinel/, notes/, and free/; one enforce pin on sentinel/ and one on notes/"

    # Setup refuses enforce on a known-bad state. The refusal names the
    # reason, so the script shows it and stops.
    if ! "$BIN" -C "$REPO" init --client "$CLIENT" --lesson-checks enforce > "$OUT/init.txt" 2>&1; then
        fail "init --lesson-checks enforce refused: $(tail -1 "$OUT/init.txt")"
        summary
    fi

    ok "init --client $CLIENT --lesson-checks enforce installed the guard and the post-edit hooks"

    # `seamark status` reads the index, so the capability reports of the
    # phases need one.
    "$BIN" -C "$REPO" index > "$OUT/index.txt" 2>&1
    ok "index built for the capability reports of seamark status"

    cp "$SCRIPTS/native-record-observer.py" "$INSTR/observer.py"
    guard_prewrite_script
    guard_instrument
    [ "$CLIENT" != codex ] || guard_last
    ok "instrumentation entries added beside the lesson hook entries"

    cp "$GUARD_DOC" "$INSTR/hooks-enforce.json"
    : > "$INSTR/scenario-guard"

    # A failure here means that the lesson hook entries have a layout that
    # guard_last does not expect. One trust review would then not cover
    # every phase, so setup stops before the operator reviews anything.
    if [ "$CLIENT" = codex ]; then
        if reason=$(guard_keys); then
            ok "every Codex hook form keeps the trust keys and the content of the enforce form"
        else
            fail "a Codex hook form moves or changes a trusted entry: $reason"
            summary
        fi
    fi
}

# guard_instrument adds the observer entries and the indirection entry
# after the lesson hook entries of the hook document.
guard_instrument() {
    # The observer runs on PreToolUse too, because the guard reads that
    # event. The indirection entry gets the matcher and the timeout of
    # the guard spec, so the slow mode meets the timeout of the guard.
    python3 -I - "$CLIENT" "python3 -I $INSTR/observer.py $OBS $CLIENT" "sh $INSTR/pre-write.sh" "$GUARD_MATCHER" "$GUARD_DOC" <<'PY'
import json, os, sys

client, observer, prewrite, matcher, path = sys.argv[1:6]
tools = {"PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied"}

if client == "claude":
    events = ["UserPromptSubmit", "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied", "Stop",
              "StopFailure", "SubagentStart", "SubagentStop", "PreCompact", "PostCompact",
              "SessionStart", "SessionEnd"]
else:
    events = ["SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "Stop", "SubagentStart",
              "SubagentStop", "PreCompact", "PostCompact", "Interrupt", "SessionEnd"]

doc = {"hooks": {}}
if os.path.exists(path):
    with open(path) as f:
        doc = json.load(f)

hooks = doc.setdefault("hooks", {})

for e in events:
    timeout = 3 if client == "codex" and e in ("Interrupt", "SessionEnd") else 10
    entry = {"hooks": [{"type": "command", "command": observer, "timeout": timeout}]}
    if e in tools:
        entry["matcher"] = "*"
    hooks.setdefault(e, []).append(entry)

hooks.setdefault("PreToolUse", []).append(
    {"matcher": matcher, "hooks": [{"type": "command", "command": prewrite, "timeout": 10}]})

os.makedirs(os.path.dirname(path), exist_ok=True)
with open(path, "w") as f:
    json.dump(doc, f, indent=2)
PY
}

# guard_last moves the owned guard entry of the Codex hook document into
# a new PreToolUse group after all other groups. The group keeps the
# matcher and the other fields of the group that init wrote. Only the
# position changes, so the guard runs with the same command, matcher,
# and timeout. The no-guard form then removes the last group, and no
# other entry changes its trust key.
guard_last() {
    python3 -I - "$GUARD_DOC" "$GUARD_MARKER" <<'PY'
import json, sys

path, marker = sys.argv[1:3]

with open(path, encoding="utf-8") as f:
    doc = json.load(f)

kept, moved = [], []
for entry in doc["hooks"]["PreToolUse"]:
    hooks = entry.get("hooks", [])
    owned = [h for h in hooks if h.get("command", "").endswith(" " + marker)]
    others = [h for h in hooks if not h.get("command", "").endswith(" " + marker)]
    if owned:
        moved.append(dict(entry, hooks=owned))
        if others:
            kept.append(dict(entry, hooks=others))
    else:
        kept.append(entry)

doc["hooks"]["PreToolUse"] = kept + moved
with open(path, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2)
PY
}

# guard_keys checks the trust keys of the Codex hook forms against the
# enforce form that setup kept, and that the operator reviews. The enforce
# form must hold one owned guard entry, alone in the last PreToolUse
# group. The no-guard form must hold every other entry at the same key
# (event, group, handler) with the same content, because Codex hashes
# the content of each entry and compares it per key. It prints the
# problems on one line, and returns 1 when a problem exists.
#
# The enforce, check-pins, and broken-config phases run with the enforce
# form itself. The wrapper, slow, and slow-deny phases run with the
# no-guard form. The inline-wrapper form exists only while seamark status
# reads it, and no Codex turn runs with it.
guard_keys() {
    guard_doc no-guard "$INSTR/hooks-no-guard.json"
    python3 -I - "$INSTR/hooks-enforce.json" "$INSTR/hooks-no-guard.json" "$GUARD_MARKER" <<'PY'
import json, sys

enforce_path, form_path, marker = sys.argv[1:4]


def entries(path):
    """Return the entries of a hook document by trust key, and its PreToolUse group count."""
    with open(path, encoding="utf-8") as f:
        doc = json.load(f)
    out = {}
    for event, groups in doc.get("hooks", {}).items():
        for g, entry in enumerate(groups):
            fields = {k: v for k, v in entry.items() if k != "hooks"}
            for h, hook in enumerate(entry.get("hooks", [])):
                out["%s:%d:%d" % (event, g, h)] = (fields, hook)
    return out, len(doc.get("hooks", {}).get("PreToolUse", []))


enforce, groups = entries(enforce_path)
form, _ = entries(form_path)
problems = []

owned = [k for k, (_, hook) in enforce.items() if hook.get("command", "").endswith(" " + marker)]
last = "PreToolUse:%d:0" % (groups - 1)
if len(owned) != 1:
    problems.append("the enforce form holds %d owned guard entries, not one" % len(owned))
elif owned[0] != last or "PreToolUse:%d:1" % (groups - 1) in enforce:
    problems.append("the owned guard entry is at %s, not alone at %s" % (owned[0], last))

for key in sorted(form):
    if key not in enforce:
        problems.append("%s is not in the enforce form" % key)
    elif form[key] != enforce[key]:
        problems.append("%s holds other content than in the enforce form" % key)

for key in sorted(set(enforce) - set(form) - set(owned)):
    problems.append("%s of the enforce form is missing" % key)

print("; ".join(problems))
sys.exit(1 if problems else 0)
PY
}

# guard_lessons writes the three lessons.yaml variants into the
# instrumentation directory. The check variant keeps the pins and sets
# their mode to check. The broken variant names a check kind that no
# release has, a tier-2 configuration error.
guard_lessons() {
    cat > "$INSTR/lessons-enforce.yaml" <<'YAML'
# The synthetic lessons of the guard scenario
# (scripts/native-record.sh, NATIVE_RECORD_SCENARIO=guard).
lesson_checks_version: 1
checks:
  frozen-sentinel:
    kind: forbid-path-change/1
  gate5-marker:
    kind: no-added-pattern/1
    pattern: 'GATE5-MARKER'
pin:
  - rule: frozen-sentinel
    region: sentinel/
    note: 'Gate-5 sentinel lesson. The files under sentinel/ stay as committed.'
    mode: enforce
    check: frozen-sentinel
  - rule: no-gate5-marker
    region: notes/
    note: 'Gate-5 pattern lesson. No file under notes/ gets a line with the gate-5 marker.'
    mode: enforce
    check: gate5-marker
YAML
    sed 's/^    mode: enforce$/    mode: check/' "$INSTR/lessons-enforce.yaml" > "$INSTR/lessons-check.yaml"
    sed 's|kind: forbid-path-change/1|kind: forbid-path-change/9|' "$INSTR/lessons-enforce.yaml" > "$INSTR/lessons-broken.yaml"
}

# guard_prewrite_script writes the indirection entry. The file
# pre-write-mode selects what it does: off exits 0, report-only runs the
# guard and discards its exit status, slow waits GUARD_SLOW_SECONDS and
# then runs the guard, and slow-deny waits GUARD_IN_BUDGET_SECONDS and
# then runs the guard. Each guard run appends its mode and exit status to
# pre-write.log. A run that the client stops at the timeout appends
# nothing.
guard_prewrite_script() {
    cat > "$INSTR/pre-write.sh" <<EOF
#!/bin/sh
# Test instrumentation of the guard scenario (scripts/native-record.sh).
mode=\$(cat '$INSTR/pre-write-mode' 2>/dev/null || echo off)
case "\$mode" in
    report-only)
        '$BIN' $GUARD_MARKER
        echo "report-only \$?" >> '$INSTR/pre-write.log'
        exit 0 ;;
    slow)
        sleep $GUARD_SLOW_SECONDS
        '$BIN' $GUARD_MARKER
        status=\$?
        echo "slow \$status" >> '$INSTR/pre-write.log'
        exit \$status ;;
    slow-deny)
        sleep $GUARD_IN_BUDGET_SECONDS
        '$BIN' $GUARD_MARKER
        status=\$?
        echo "slow-deny \$status" >> '$INSTR/pre-write.log'
        exit \$status ;;
esac
exit 0
EOF
    printf 'off\n' > "$INSTR/pre-write-mode"
}

# guard_doc <form> [<file>] writes one form of the hook document from
# the copy that setup kept: enforce (the copy itself), no-guard (without
# the owned guard entry), or inline-wrapper (the owned guard command with
# "|| true" after it, a report-only wrapper that inspection detects). It
# writes the hook document of the client, or <file> when it is given, so
# guard_keys can read a form that no client reads.
guard_doc() {
    python3 -I - "$INSTR/hooks-enforce.json" "${2:-$GUARD_DOC}" "$GUARD_MARKER" "$1" <<'PY'
import json, shutil, sys

saved, path, marker, form = sys.argv[1:5]

if form == "enforce":
    shutil.copyfile(saved, path)
    sys.exit(0)

with open(saved, encoding="utf-8") as f:
    doc = json.load(f)

entries = []
for entry in doc.get("hooks", {}).get("PreToolUse", []):
    hooks = []
    for h in entry.get("hooks", []):
        command = h.get("command", "")
        if command.endswith(" " + marker):
            if form == "no-guard":
                continue
            h = dict(h, command=command + " || true")
        hooks.append(h)
    if hooks:
        entries.append(dict(entry, hooks=hooks))

doc["hooks"]["PreToolUse"] = entries
with open(path, "w", encoding="utf-8") as f:
    json.dump(doc, f, indent=2)
PY
}

# guard_capability <phase> appends the lesson-check report of
# `seamark status --json` to guard-capability.jsonl. The report shows
# the capability state of the phase before its runs.
guard_capability() {
    "$BIN" -C "$REPO" status --json > "$OUT/status-$1.json" 2> /dev/null || true
    python3 -I - "$OUT/status-$1.json" "$1" "$OUT/guard-capability.jsonl" <<'PY'
import json, sys

src, phase, dst = sys.argv[1:4]
try:
    with open(src, encoding="utf-8") as f:
        report = json.load(f).get("lesson_checks")
except ValueError:
    report = None

with open(dst, "a", encoding="utf-8") as w:
    w.write(json.dumps({"phase": phase, "lesson_checks": report}) + "\n")
PY
}

# guard_restore puts back the enforce setup: the enforce lessons.yaml,
# the hook document that setup kept, and the indirection entry off.
guard_restore() {
    cp "$INSTR/lessons-enforce.yaml" "$REPO/.seamark/lessons.yaml"
    guard_doc enforce
    printf 'off\n' > "$INSTR/pre-write-mode"
}

# guard_phase <phase> sets up one phase (see the list above) and appends
# its capability report. A case in the current phase changes nothing.
guard_phase() {
    [ "$1" != "${GUARD_PHASE:-}" ] || return 0
    GUARD_PHASE=$1
    guard_restore

    case "$1" in
        check-pins)
            cp "$INSTR/lessons-check.yaml" "$REPO/.seamark/lessons.yaml" ;;
        broken-config)
            cp "$INSTR/lessons-broken.yaml" "$REPO/.seamark/lessons.yaml" ;;
        check-installed)
            "$BIN" -C "$REPO" init --client "$CLIENT" --lesson-checks check > "$OUT/init-check.txt" 2>&1 ||
                fail "init --lesson-checks check failed: $(tail -1 "$OUT/init-check.txt")" ;;
        post-only)
            guard_doc no-guard ;;
        wrapper)
            # Inspection reads an inline wrapper as report-only. No run
            # uses that form, so its report is the only evidence of it.
            guard_doc inline-wrapper
            guard_capability wrapper-inline
            guard_doc no-guard
            printf 'report-only\n' > "$INSTR/pre-write-mode" ;;
        slow)
            guard_doc no-guard
            printf 'slow\n' > "$INSTR/pre-write-mode" ;;
        slow-deny)
            guard_doc no-guard
            printf 'slow-deny\n' > "$INSTR/pre-write-mode" ;;
    esac

    guard_capability "$1"
}

# guard_reset puts the fixture directories back to the commit, so each
# case starts from the same files. It first makes the read-only directory
# of guard_fixture writable again, because git clean cannot remove a file
# inside a directory without write permission.
guard_reset() {
    [ ! -d "$REPO/sentinel/locked" ] || chmod 0755 "$REPO/sentinel/locked"
    git -C "$REPO" checkout -q -- sentinel notes free
    git -C "$REPO" clean -fdq -- sentinel notes free
}

# guard_fixture <case> adds the fixture files of one case after the reset.
# The execution-failure case gets the empty directory sentinel/locked with
# mode 0555. Claude Code checks a new Write target only for its absence
# before the hooks. So the Write runs PreToolUse, then fails when it opens
# its temporary file in that directory, and that failure fires
# PostToolUseFailure. An empty directory changes no file hash and no git
# status line, so the state of the case stays comparable.
guard_fixture() {
    case "$1" in
        execution-failure)
            mkdir -p "$REPO/sentinel/locked"
            chmod 0555 "$REPO/sentinel/locked" ;;
    esac
}

# guard_state <file> writes the state of the fixture directories: the
# SHA-256 of every file, the text of every file under free/, and the git
# status lines. A denied write leaves all three unchanged.
guard_state() {
    python3 -I - "$REPO" "$1" <<'PY'
import hashlib, json, os, subprocess, sys

repo, dst = sys.argv[1:3]
dirs = ["sentinel", "notes", "free"]
files, text = {}, {}

for d in dirs:
    for base, _, names in os.walk(os.path.join(repo, d)):
        for name in names:
            path = os.path.join(base, name)
            rel = os.path.relpath(path, repo).replace(os.sep, "/")
            with open(path, "rb") as f:
                data = f.read()
            files[rel] = hashlib.sha256(data).hexdigest()
            if rel.startswith("free/"):
                text[rel] = data.decode("utf-8", "replace")

status = subprocess.run(["git", "-C", repo, "status", "--porcelain", "--untracked-files=all", "--"] + dirs,
                        capture_output=True, text=True, check=True).stdout.splitlines()

with open(dst, "w", encoding="utf-8") as f:
    json.dump({"files": files, "text": text, "status": status}, f)
PY
}

# guard_lines <file> prints the number of lines of a file, or 0 when the
# file is absent.
guard_lines() {
    if [ -f "$1" ]; then
        wc -l < "$1" | tr -d ' '
    else
        echo 0
    fi
}

# guard_begin keeps one guard recording to the runs of one call of the
# script. It moves the hook payloads of an earlier call aside, notes the
# length of the lesson-check audit stream, and starts empty case and
# capability lists.
#
# For Codex it first checks the trust keys of the hook forms. A
# repository that an older version of this script set up keeps the owned
# guard before the observer and the indirection entry. There the no-guard
# form moves both entries, Codex does not run them, and the wrapper,
# slow, and slow-deny cases record nothing. So no paid turn starts there.
guard_begin() {
    guard_vars

    if [ "$CLIENT" = codex ]; then
        if reason=$(guard_keys); then
            ok "every Codex hook form keeps the trust keys and the content of the enforce form"
        else
            blocked "a Codex hook form moves or changes a trusted entry: $reason"
            note "set up a new repository with this script (another NATIVE_RECORD_REPO), and review its trust"
            summary
        fi
    fi

    if [ -n "$(find "$OBS" -name '*.json' 2>/dev/null | head -1)" ]; then
        mv "$OBS" "$INSTR/observed-before-$(date +%s)"
        mkdir -p "$OBS"
        note "moved the hook payloads of an earlier call aside"
    fi

    AUDIT_LINES0=$(guard_lines "$REPO/.seamark/lesson-check-audit.jsonl")
    : > "$OUT/guard-cases.jsonl"
    : > "$OUT/guard-capability.jsonl"
    GUARD_PHASE=
}

# guard_run <phase> <case> <prompt> runs one case. It sets up the phase,
# resets the fixture, adds the fixture files of the case, notes the state,
# runs one paid client session, notes the state again, and appends the
# case line to guard-cases.jsonl. The start time of the run assigns each
# later hook payload to it.
guard_run() {
    guard_phase "$1"
    guard_reset
    guard_fixture "$2"
    guard_state "$OUT/state-before.json"
    audit_from=$(guard_lines "$REPO/.seamark/lesson-check-audit.jsonl")
    log_from=$(guard_lines "$INSTR/pre-write.log")

    gcase=$2
    RUN=$(( $(wc -l < "$OUT/runs.txt") + 1 ))
    perl -MTime::HiRes=time -e 'printf "%d\n", time * 1e9' >> "$OUT/runs.txt" 2>/dev/null || echo "$(date +%s)000000000" >> "$OUT/runs.txt"
    printf '%s' "$3" > "$OUT/prompt-$RUN.txt"
    : > "$OUT/reply-$RUN.txt"

    case "$CLIENT" in
        claude) guard_claude ;;
        codex)  guard_codex ;;
    esac

    guard_state "$OUT/state-after.json"
    python3 -I - "$OUT" "$RUN" "$gcase" "$GUARD_PHASE" "${AUDIT_LINES0:-0}" "$audit_from" \
        "$(guard_lines "$REPO/.seamark/lesson-check-audit.jsonl")" "$INSTR/pre-write.log" "$log_from" <<'PY'
import json, os, re, sys

out, run, case, phase = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
base, audit_from, audit_to = int(sys.argv[5]), int(sys.argv[6]), int(sys.argv[7])
log, log_from = sys.argv[8], int(sys.argv[9])


def load(name):
    with open(os.path.join(out, name), encoding="utf-8") as f:
        return json.load(f)


lines = []
if os.path.isfile(log):
    with open(log, encoding="utf-8") as f:
        lines = [line.strip() for line in f][log_from:]

# The reply is a corroboration only. A single word stays, for example
# done, blocked, no-multiedit, or a check id. Other text does not.
with open(os.path.join(out, "reply-%d.txt" % run), encoding="utf-8") as f:
    reply = f.read().strip().strip(".").lower()

record = {
    "run": run, "case": case, "phase": phase,
    "before": load("state-before.json"), "after": load("state-after.json"),
    "audit": [audit_from - base, audit_to - base],
    "pre_write": lines,
    "reply": reply if re.fullmatch(r"[a-z0-9_-]{1,40}", reply) else "",
}

with open(os.path.join(out, "guard-cases.jsonl"), "a", encoding="utf-8") as w:
    w.write(json.dumps(record) + "\n")
PY
}

# guard_claude runs the prompt of the current case in a new print-mode
# session. Bash is not an allowed tool, so a denied write has no shell
# path around the guard. The final reply goes to reply-<run>.txt.
guard_claude() {
    set -- --model "$model" --output-format json --setting-sources project,local --strict-mcp-config \
        --max-budget-usd 0.40 --debug hooks --debug-file "$OUT/run-$RUN-debug.log" \
        --allowedTools Read Edit Write MultiEdit Agent Task --agents "$agents"

    if claude_print "$RUN" "$@"; then
        line=$(python3 -I -c '
import json, sys
d = json.load(open(sys.argv[1]))
with open(sys.argv[2], "w") as f:
    f.write(str(d.get("result") or ""))
print(d.get("session_id", ""), d.get("total_cost_usd", 0), d.get("is_error"))' "$OUT/run-$RUN.json" "$OUT/reply-$RUN.txt")
        SESSION=${line%% *}
        echo "  cost     run $RUN ($gcase): claude -p --model $model: total_cost_usd $(printf '%s' "$line" | cut -d' ' -f2)"

        if [ "$(printf '%s' "$line" | cut -d' ' -f3)" = True ]; then
            fail "run $RUN ($gcase) reported an error; see $OUT/run-$RUN.json"
        else
            ok "run $RUN ($gcase) finished (session $SESSION)"
        fi
    else
        fail "run $RUN ($gcase) failed: $(tail -1 "$OUT/run-$RUN-stderr.txt")"
    fi
}

# guard_codex runs the prompt of the current case in a new `codex exec`
# thread, as timing_codex does. The final agent message goes to
# reply-<run>.txt. It never passes --dangerously-bypass-hook-trust,
# because trust stays the operator's decision.
guard_codex() {
    exec_status=0
    (cd "$REPO" && PATH="$(dirname "$BIN"):$PATH" codex exec -m "$model" -c model_reasoning_effort=low --json \
        -s workspace-write -C "$REPO" - < "$OUT/prompt-$RUN.txt" > "$OUT/run-$RUN-events.jsonl" 2> "$OUT/run-$RUN-stderr.txt") || exec_status=$?

    if codex_verdict "run $RUN ($gcase)" "$exec_status" "$OUT/run-$RUN-events.jsonl" "$OUT/run-$RUN-stderr.txt"; then
        cat "$OUT/run-$RUN-events.jsonl" >> "$OUT/exec-events.jsonl"
        line=$(python3 -I -c '
import json, sys
thread, usage, reply = "", {}, ""
for raw in open(sys.argv[1]):
    try:
        e = json.loads(raw)
    except ValueError:
        continue
    item = e.get("item") if isinstance(e.get("item"), dict) else {}
    if e.get("type") == "thread.started":
        thread = e.get("thread_id", "")
    if e.get("type") == "turn.completed":
        usage = e.get("usage", {})
    if e.get("type") == "item.completed" and item.get("type") == "agent_message":
        reply = item.get("text", "")
with open(sys.argv[2], "w") as f:
    f.write(reply)
print(thread, " ".join("%s=%s" % kv for kv in sorted(usage.items())))' "$OUT/run-$RUN-events.jsonl" "$OUT/reply-$RUN.txt")
        [ -z "${line%% *}" ] || SESSION=${line%% *}
        echo "  cost     run $RUN ($gcase): codex exec -m $model -c model_reasoning_effort=low: ${line#* }"
        ok "run $RUN ($gcase) finished (thread $SESSION)"
    fi
}

# The prompt parts of the guard cases. The model must not retry a
# blocked call or go around it, so the first attempt is the evidence.
GUARD_STEPS='Do exactly these steps in order, and do not add steps.'
GUARD_STOP='If a tool call is blocked or fails, do not retry it and do not try another tool or another way.
Then reply with the single word: done, or with the single word: blocked when a tool call was blocked or failed.'
GUARD_REPORT='Then reply with one line: the check id that a seamark post-edit message named after the tool call, or the single word none when no such message came.'

# The prompt parts of the child cases. The parent cannot see the tool
# results of the child, so it repeats the reply word of the child. That
# relay is a corroboration only. The parent can reword the reply, so the
# replay reads the reply of the child from its SubagentStop payload.
GUARD_RELAY='Do not use any tool yourself. Then reply with the single word that the subagent replied.'

# record_claude_guard runs the twenty-three Claude Code guard cases, each
# in a new session. The writer subagent has only the Write tool. The
# phases that change the hook document run last among the first thirteen
# cases. The ten later cases add child denies, a child post-edit reply,
# the in-budget slow deny of each tool and lane, and a write that fails
# during its execution.
#
# Each child prompt sets run_in_background of the Agent call. Claude Code
# 2.1.314 starts an agent in the background when the field is not set, and
# another build can change that default. A fixed shape per case gives the
# Write and the Edit child a deny in each shape: a foreground deny in
# subagent-violation and subagent-edit-violation, and a background deny
# after the in-budget wait in slow-deny-subagent-write and
# slow-deny-subagent-edit.
record_claude_guard() {
    model=${NATIVE_RECORD_CLAUDE_MODEL:-haiku}
    agents='{"writer":{"description":"Creates the one file it is given with the given content.","prompt":"Use the Write tool exactly once to create the file that you are given with the content that you are given. If the call is blocked or fails, do not retry it and do not try another way. Reply with the single word done or blocked.","model":"'"$model"'","tools":["Write"]}}'

    guard_run enforce edit-violation "$GUARD_STEPS
1. Use the Read tool to read the file sentinel/edit.txt.
2. Use the Edit tool once to replace the line alpha with the line beta in sentinel/edit.txt.
$GUARD_STOP"

    guard_run enforce write-violation "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/written.txt with the single line: written
$GUARD_STOP"

    guard_run enforce multiedit-violation "$GUARD_STEPS
1. Use the Read tool to read the file notes/multi.txt.
2. Use the MultiEdit tool once on notes/multi.txt with two edits in this order: replace the line alpha with the line beta, then replace the line gamma with the line GATE5-MARKER.
If you have no MultiEdit tool, use no other tool for step 2 and reply with the single word: no-multiedit.
$GUARD_STOP"

    guard_run enforce subagent-violation "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"writer\" and run_in_background set to false, and ask it to create the new file sentinel/child.txt with the single line: child
$GUARD_STOP"

    guard_run enforce unknown-edit "$GUARD_STEPS
1. Use the Read tool to read the file notes/unknown.txt.
2. Use the Edit tool once on notes/unknown.txt with the old_string gamma and the new_string delta. The text gamma does not occur in the file; make the call anyway.
$GUARD_STOP"

    guard_run enforce n1-absent-edit "$GUARD_STEPS
1. Use the Edit tool once on the file free/n1.txt with an empty old_string and the new_string: created
The file does not exist yet. Do not read it first, and do not use the Write tool.
$GUARD_STOP"

    guard_run enforce permitted-write "$GUARD_STEPS
1. Use the Write tool once to create the new file notes/permitted.txt with the single line: permitted
$GUARD_STOP"

    guard_run broken-config config-error "$GUARD_STEPS
1. Use the Write tool once to create the new file free/config.txt with the single line: config
$GUARD_STOP"

    guard_run check-pins check-only "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/checked.txt with the single line: checked
$GUARD_REPORT"

    guard_run check-installed check-installed "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/baseline.txt with the single line: baseline
$GUARD_REPORT"

    guard_run post-only failure-reach "$GUARD_STEPS
1. Use the Read tool to read the file notes/failure.txt.
2. Use the Edit tool once on notes/failure.txt with the old_string gamma and the new_string delta. The text gamma does not occur in the file, so the call fails; make it anyway and do not retry it.
$GUARD_REPORT"

    guard_run wrapper report-only-wrapper "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/wrapped.txt with the single line: wrapped
$GUARD_STOP"

    guard_run slow slow-guard "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/slow.txt with the single line: slow
$GUARD_STOP"

    # The later cases get two more subagents. The earlier cases keep the
    # agent list of their first recordings. The editor reads before it
    # edits, because Edit and MultiEdit need a read of the file first.
    agents='{"writer":{"description":"Creates the one file it is given with the given content.","prompt":"Use the Write tool exactly once to create the file that you are given with the content that you are given. If the call is blocked or fails, do not retry it and do not try another way. Reply with the single word done or blocked.","model":"'"$model"'","tools":["Write"]},'
    agents=$agents'"editor":{"description":"Reads the one file it is given, then makes the one edit it is given.","prompt":"Use the Read tool once on the file that you are given. Then make the edit that you are given with exactly the tool that you are given, once. If you do not have that tool, use no other tool and reply with the single word no-multiedit. If the call is blocked or fails, do not retry it and do not try another way. Reply with the single word done or blocked.","model":"'"$model"'","tools":["Read","Edit","MultiEdit"]},'
    agents=$agents'"reporter":{"description":"Creates the one file it is given, then reports the post-edit message.","prompt":"Use the Write tool exactly once to create the file that you are given with the content that you are given. Do not retry it. Then reply with one line: the check id that a seamark post-edit message named after the tool call, or the single word none when no such message came.","model":"'"$model"'","tools":["Write"]}}'

    guard_run enforce subagent-edit-violation "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"editor\" and run_in_background set to false, and ask it to read sentinel/edit.txt and then use the Edit tool once to replace the line alpha with the line beta in sentinel/edit.txt.
$GUARD_RELAY"

    guard_run enforce subagent-multiedit-violation "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"editor\" and run_in_background set to false, and ask it to read notes/multi.txt and then use the MultiEdit tool once on notes/multi.txt with two edits in this order: replace the line alpha with the line beta, then replace the line gamma with the line GATE5-MARKER.
$GUARD_RELAY"

    guard_run check-pins subagent-check-only "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"reporter\" and run_in_background set to true, and ask it to create the new file sentinel/child-checked.txt with the single line: checked
Do not use any tool yourself. Then reply with the one line that the subagent replied."

    guard_run slow-deny slow-deny-write "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/slow-deny.txt with the single line: slow
$GUARD_STOP"

    guard_run slow-deny slow-deny-edit "$GUARD_STEPS
1. Use the Read tool to read the file sentinel/edit.txt.
2. Use the Edit tool once to replace the line alpha with the line beta in sentinel/edit.txt.
$GUARD_STOP"

    guard_run slow-deny slow-deny-multiedit "$GUARD_STEPS
1. Use the Read tool to read the file notes/multi.txt.
2. Use the MultiEdit tool once on notes/multi.txt with two edits in this order: replace the line alpha with the line beta, then replace the line gamma with the line GATE5-MARKER.
If you have no MultiEdit tool, use no other tool for step 2 and reply with the single word: no-multiedit.
$GUARD_STOP"

    guard_run slow-deny slow-deny-subagent-write "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"writer\" and run_in_background set to true, and ask it to create the new file sentinel/slow-child.txt with the single line: child
$GUARD_RELAY"

    guard_run slow-deny slow-deny-subagent-edit "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"editor\" and run_in_background set to true, and ask it to read sentinel/edit.txt and then use the Edit tool once to replace the line alpha with the line beta in sentinel/edit.txt.
$GUARD_RELAY"

    guard_run slow-deny slow-deny-subagent-multiedit "$GUARD_STEPS
1. Use the Agent tool with subagent_type \"editor\" and run_in_background set to true, and ask it to read notes/multi.txt and then use the MultiEdit tool once on notes/multi.txt with two edits in this order: replace the line alpha with the line beta, then replace the line gamma with the line GATE5-MARKER.
$GUARD_RELAY"

    # The Write targets a new file in the read-only directory that
    # guard_fixture makes. An Edit with a wrong old_string cannot show this
    # path, because the client refuses it before any hook. The phase has no
    # baseline, so the post-edit hook reports the failed write as
    # incomplete and replies.
    guard_run post-only execution-failure "$GUARD_STEPS
1. Use the Write tool once to create the new file sentinel/locked/new.txt with the single line: locked
The directory sentinel/locked already exists. Do not read it and do not create it. If the call fails, do not retry it and do not try another way.
$GUARD_REPORT"

    guard_restore
}

# record_codex_guard runs the sixteen Codex guard cases, each in a new
# thread: the four apply_patch section kinds, the update content cases,
# and the phase cases. The phases that change the hook document run last
# among the first thirteen cases. The three later cases add a worker
# post-edit reply and the in-budget slow deny of the root and the worker.
record_codex_guard() {
    model=${NATIVE_RECORD_CODEX_MODEL:-gpt-5.6-luna}

    guard_run enforce add-violation "$GUARD_STEPS
1. Use apply_patch once to add the new file sentinel/added.txt with the single line: added
$GUARD_STOP"

    guard_run enforce update-violation "$GUARD_STEPS
1. Use apply_patch once to replace the line alpha with the line beta in sentinel/update.txt.
$GUARD_STOP"

    guard_run enforce delete-violation "$GUARD_STEPS
1. Use apply_patch once to delete the file sentinel/delete.txt.
$GUARD_STOP"

    guard_run enforce move-violation "$GUARD_STEPS
1. Use apply_patch once to move the file sentinel/move.txt to free/moved.txt without a change to its content: one Update File section with a Move to line and no hunk.
$GUARD_STOP"

    guard_run enforce multi-file-violation "$GUARD_STEPS
1. Use one single apply_patch call that adds two new files: notes/multi-ok.txt with the single line: ok, and sentinel/multi.txt with the single line: multi.
$GUARD_STOP"

    # The prompt names the agent tools as Codex 0.164.0 lists them in code
    # mode. With the short name spawn_agent, the model called a tool that
    # does not exist, and no worker started. The prompt also names the
    # agent_type and message fields, because without agent_type the tool
    # starts a fork of the whole history, not a worker.
    guard_run enforce subagent-violation "$GUARD_STEPS
1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to use apply_patch once to add the new file sentinel/child.txt with the single line: child, and not to retry a blocked call.
2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.
$GUARD_STOP"

    guard_run enforce update-unknown "$GUARD_STEPS
1. Use apply_patch once to replace the line alpha with the line beta in notes/update.txt.
$GUARD_STOP"

    guard_run enforce permitted-write "$GUARD_STEPS
1. Use apply_patch once to add the new file notes/permitted.txt with the single line: permitted
$GUARD_STOP"

    # The prompt gives the exact patch for the fixture one, two, three. A
    # description in words let the model write the line text "line two",
    # which the file does not hold, so the applier refused the patch. The
    # patch has the one-hunk shape of the content oracle, so a successful
    # run gives a live update case.
    guard_run enforce update-permitted "$GUARD_STEPS
1. Use apply_patch once with exactly this patch, line for line. The context lines one and three each start with one space.
*** Begin Patch
*** Update File: free/oracle.txt
@@
 one
-two
+TWO
 three
*** End Patch
$GUARD_STOP"

    guard_run broken-config config-error "$GUARD_STEPS
1. Use apply_patch once to add the new file free/config.txt with the single line: config
$GUARD_STOP"

    guard_run check-pins check-only "$GUARD_STEPS
1. Use apply_patch once to add the new file sentinel/checked.txt with the single line: checked
$GUARD_REPORT"

    guard_run wrapper report-only-wrapper "$GUARD_STEPS
1. Use apply_patch once to add the new file sentinel/wrapped.txt with the single line: wrapped
$GUARD_STOP"

    guard_run slow slow-guard "$GUARD_STEPS
1. Use apply_patch once to add the new file sentinel/slow.txt with the single line: slow
$GUARD_STOP"

    # The prompt names the agent tools and the worker agent_type for the
    # reason in the comment at the case subagent-violation.
    guard_run check-pins subagent-check-only "$GUARD_STEPS
1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to use apply_patch once to add the new file sentinel/child-checked.txt with the single line: checked, not to retry the call, and to reply with one line: the check id that a seamark post-edit message named after the call, or the single word none when no such message came.
2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.
Do not use apply_patch yourself. Then reply with the one line that the worker replied."

    guard_run slow-deny slow-deny-add "$GUARD_STEPS
1. Use apply_patch once to add the new file sentinel/slow-deny.txt with the single line: slow
$GUARD_STOP"

    # The prompt names the agent tools and the worker agent_type for the
    # reason in the comment at the case subagent-violation.
    guard_run slow-deny slow-deny-subagent-add "$GUARD_STEPS
1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to use apply_patch once to add the new file sentinel/slow-child.txt with the single line: child, and not to retry a blocked call.
2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.
$GUARD_STOP"

    guard_restore
}

# guard_extras adds the evidence of the guard scenario to record.json:
# the case list, the capability reports, the lesson-check audit records
# of this call, the transcript records that hold a guard deny or a
# post-edit reply, and the guard and post-edit hook results of the
# Claude Code debug logs. The sanitizer replaces the text of attachments
# and developer messages with a marker, so only this copy keeps the text
# that the model got. Assistant and reasoning records stay out, because
# the sanitizer removes thinking text.
guard_extras() {
    for f in guard-cases.jsonl guard-capability.jsonl; do
        [ -f "$OUT/$f" ] || : > "$OUT/$f"
    done

    python3 -I - "$OUT/record.json" "$REPO/.seamark/lesson-check-audit.jsonl" "${AUDIT_LINES0:-0}" "$OUT" <<'PY'
import glob, json, os, re, sys

record_path, audit, start, out = sys.argv[1], sys.argv[2], int(sys.argv[3]), sys.argv[4]
needles = ("blocked by policy", "seamark post-edit lesson checks after this")
markers = (("lessons --guard --hook", "guard"), ("lessons --baseline --hook", "baseline"),
           ("lessons --verify --hook", "verify"), ("pre-write.sh", "pre-write"))
finished = re.compile(r"Hooks: ([A-Za-z]+)(?::([^ ]*))? \[(.*?)\] finished with status (\d+) \((\d+)ms\)")
stopped = re.compile(r"time ?out|timed out|cancel|abort|kill", re.IGNORECASE)

with open(record_path, encoding="utf-8") as f:
    record = json.load(f)


def model_output(r):
    """Return True for a record that the model wrote."""
    msg = r.get("message")
    if isinstance(msg, dict) and msg.get("role") == "assistant":
        return True

    p = r.get("payload")
    if not isinstance(p, dict):
        return False

    item = p.get("item") if isinstance(p.get("item"), dict) else {}
    return p.get("role") == "assistant" or p.get("type") in ("reasoning", "agent_reasoning") or \
        item.get("type") == "Reasoning"


# Every Codex case runs a thread of its own, and a subagent payload can
# name no rollout. So every thread and agent ID of the payloads adds its
# own rollout, as in timing_extras.
if record["client"] == "codex":
    base = os.environ.get("CODEX_HOME") or os.path.join(record["home"], ".codex")
    ids = set()
    for name in glob.glob(os.path.join(record["observed"], "*.json")):
        if name.endswith(".meta.json"):
            continue
        with open(name, encoding="utf-8") as f:
            payload = json.load(f)
        ids.update(payload.get(k) for k in ("session_id", "agent_id") if payload.get(k))
    found = set(record.get("transcripts", []))
    for i in ids:
        found.update(glob.glob(os.path.join(base, "sessions", "*", "*", "*", "rollout-*-%s.jsonl" % i)))
    record["transcripts"] = sorted(found)

context = os.path.join(out, "guard-context.jsonl")
with open(context, "w", encoding="utf-8") as w:
    for path in record.get("transcripts", []):
        if not path.endswith(".jsonl"):
            continue
        with open(path, encoding="utf-8") as f:
            for line in f:
                if not line.strip():
                    continue
                r = json.loads(line)
                text = json.dumps(r, ensure_ascii=False)
                if any(n in text for n in needles) and not model_output(r):
                    w.write(text + "\n")

# The debug log of a Claude Code run names the exit status of each hook
# command. A line about a stopped hook has no status, so its text stays.
results = os.path.join(out, "guard-hooks.jsonl")
with open(results, "w", encoding="utf-8") as w:
    for path in record.get("debug_logs", []):
        run = int(re.search(r"run-(\d+)-debug\.log$", path).group(1))
        with open(path, encoding="utf-8", errors="replace") as f:
            for line in f:
                hook = next((name for marker, name in markers if marker in line), None)
                if hook is None:
                    continue
                m = finished.search(line)
                if m:
                    entry = {"run": run, "hook": hook, "event": m.group(1), "tool": m.group(2) or "",
                             "status": int(m.group(4)), "ms": int(m.group(5))}
                elif stopped.search(line):
                    entry = {"run": run, "hook": hook, "note": line.strip()[:300]}
                else:
                    continue
                w.write(json.dumps(entry) + "\n")

extras = record.setdefault("extras", {})
for name in ("guard-cases.jsonl", "guard-capability.jsonl", "guard-context.jsonl", "guard-hooks.jsonl"):
    extras[name] = os.path.join(out, name)

# The audit stream can hold the records of an earlier call. Only the
# lines after the count that guard_begin noted belong to this recording.
tail = os.path.join(out, "lesson-check-audit.jsonl")
with open(tail, "w", encoding="utf-8") as w:
    if os.path.isfile(audit):
        with open(audit, encoding="utf-8") as f:
            w.writelines(list(f)[start:])
extras["lesson-check-audit.jsonl"] = tail

with open(record_path, "w", encoding="utf-8") as f:
    json.dump(record, f, indent=1)
PY

    guard_identities
}

# guard_identities binds observed pre-write files to the audit digests.
# The fresh fixture's canonical git root and index root must agree. A
# mismatch refuses collection; no alternate identity or hash is tried.
guard_identities() {
    python3 -I - "$OUT/record.json" <<'PY'
import collections, glob, hashlib, json, os, sqlite3, subprocess, sys
from pathlib import Path

with open(sys.argv[1], encoding="utf-8") as f:
    record = json.load(f)
repo, observed = record["repo"], record["observed"]
git_root = subprocess.run(["git", "-C", repo, "rev-parse", "--show-toplevel"],
                          capture_output=True, text=True, check=True).stdout.strip()
db_path = Path(repo, ".seamark", "index.db")
with sqlite3.connect(db_path.as_uri() + "?mode=ro", uri=True) as db:
    row = db.execute("SELECT value FROM meta WHERE key = 'repo_root'").fetchone()
if repo != os.path.realpath(repo) or git_root != repo or row != (repo,):
    sys.exit("guard identity metadata requires the canonical fixture git/index root")

out = os.path.dirname(sys.argv[1])
with open(os.path.join(out, "guard-cases.jsonl"), encoding="utf-8") as f:
    cases = [json.loads(line) for line in f if line.strip()]
by_run = {c["run"]: c["case"] for c in cases}
if len(by_run) != len(cases) or len(set(by_run.values())) != len(cases):
    sys.exit("guard identity metadata requires unique case runs")

metas = []
for path in glob.glob(os.path.join(observed, "*.meta.json")):
    with open(path, encoding="utf-8") as f:
        meta = json.load(f)
    meta["file"] = os.path.basename(path)[:-len(".meta.json")] + ".json"
    metas.append(meta)

# On macOS the observer clock counts whole microseconds, so two hooks that
# start together can tie, for example PostToolUse and SubagentStart of a
# background Agent launch. The payload file name breaks a tie, as in
# observed_metas of the sanitizer, so both steps number the payloads alike.
# The file number of a PreToolUse payload is its identity, but inside a
# tie it follows the file name and not the real hook order. So a tie that
# holds a PreToolUse payload refuses the record. A tie of other events
# changes no PreToolUse number.
metas.sort(key=lambda m: (m["start_ns"], m["file"]))
starts = collections.Counter(m["start_ns"] for m in metas)
if any(m["event"] == "PreToolUse" and starts[m["start_ns"]] > 1 for m in metas):
    sys.exit("guard identity metadata requires a PreToolUse payload with an observer timestamp of its own")

def compose(parts):
    return "".join("%d:%s" % (len(p.encode("utf-8")), p) for p in parts)

def digest(text):
    return hashlib.sha256(text.encode("utf-8")).hexdigest()

client = record["client"]
tools = {"claude": {"Edit", "Write", "MultiEdit"}, "codex": {"apply_patch"}}[client]
destination = os.path.join(out, "guard-identities.jsonl")
with open(destination, "w", encoding="utf-8") as w:
    for i, meta in enumerate(metas, 1):
        if meta["event"] != "PreToolUse":
            continue
        run = 0
        for n, start in enumerate(record["runs"], 1):
            if meta["start_ns"] >= start:
                run = n
        if run not in by_run:
            continue
        with open(os.path.join(observed, meta["file"]), encoding="utf-8") as f:
            payload = json.load(f)
        session, call = payload.get("session_id"), payload.get("tool_use_id")
        agent, tool = payload.get("agent_id", ""), payload.get("tool_name")
        if tool not in tools or not isinstance(session, str) or not session or \
                not isinstance(call, str) or not call or not isinstance(agent, str):
            continue
        receiver = compose([session, agent] if agent else [session])
        entry = {"file": "hooks/%03d-PreToolUse.json" % i, "run": run, "case": by_run[run],
                 "client": client, "tool": tool, "lane": "child" if agent else "root",
                 "session_sha256": digest(repo + "\0" + session),
                 "match_sha256": digest(repo + "\0" + session + "\0" + call),
                 "context_sha256": digest(compose(["context", repo, client, receiver]))}
        w.write(json.dumps(entry) + "\n")
record.setdefault("extras", {})["guard-identities.jsonl"] = destination
with open(sys.argv[1], "w", encoding="utf-8") as f:
    json.dump(record, f, indent=1)
PY
}

case "$MODE" in
    record) record_mode ;;
esac

summary
