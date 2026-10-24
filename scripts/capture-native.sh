#!/usr/bin/env sh
# Native recordings of one client CLI with the lesson hooks. One mode:
#
#   record  paid: runs the prompts of one scenario with the installed
#           client, the operator's own login, and the cheapest model. It
#           prints one cost line per paid run. It refuses a binary or a
#           tree with uncommitted changes unless SEAMARK_ALLOW_DIRTY=1. It
#           writes the raw recording and a sanitized copy into an output
#           directory for review. It never writes into testdata.
#
# The script has no offline mode. The Go tests replay a reviewed
# recording offline (make capture-native-check).
#
# Record settings (environment):
#   CAPTURE_NATIVE_OUT    output directory (default: a new temporary one)
#   CAPTURE_NATIVE_REPO   scratch repository (default: <out>/repo); the
#                         script reuses an existing one to keep Codex hook
#                         trust
#   CAPTURE_NATIVE_RUNS   0 skips the paid runs and only collects
#   CAPTURE_NATIVE_CLAUDE_MODEL  default haiku
#   CAPTURE_NATIVE_CODEX_MODEL   default gpt-5.6-luna (low reasoning)
#   CAPTURE_NATIVE_SCENARIO      timing (the default and the only scenario)
#                                runs the gate-3 timing prompts in a
#                                scratch repository with lesson hooks (see
#                                "The gate-3 timing scenario" below).
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
SCENARIO=${CAPTURE_NATIVE_SCENARIO:-timing}

case "$SCENARIO" in
    timing) ;;
    *) echo "$0: unknown scenario \"$SCENARIO\" (known: timing)" >&2; exit 2 ;;
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
    echo "capture-native $MODE ($CLIENT): $OK ok, $FAILED failed, $BLOCKED blocked"

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

echo "capture-native $MODE — client $CLIENT"
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
    SCENARIO_ARG=" CAPTURE_NATIVE_SCENARIO=$SCENARIO"
    CODEX_TRUST="PATH=$(dirname "$BIN"):\$PATH codex"

    OUT=${CAPTURE_NATIVE_OUT:-$(mktemp -d)}
    mkdir -p "$OUT"
    OUT=$(cd "$OUT" && pwd -P)
    REPO=${CAPTURE_NATIVE_REPO:-$OUT/repo}
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

        if [ "$set_up" != "$SCENARIO" ]; then
            blocked "$REPO was set up for the ${set_up:-another} scenario, not $SCENARIO; use another CAPTURE_NATIVE_REPO"
            summary
        fi
    else
        timing_setup
    fi

    # Codex runs project hooks only after a trust review. A new timing
    # repository has no trusted hook yet, so a paid run there records no
    # hook payload. The script stops before the paid runs instead.
    if [ "$CLIENT" = codex ] && [ -z "$reuse" ]; then
        note "Codex runs project hooks only after you trust them; setup never grants trust:"
        note "  1. cd $REPO && $CODEX_TRUST   (accept the project trust prompt)"
        note "  2. review the hooks (\"Hooks need review\", or /hooks), trust every entry, then quit"
        note "  3. CAPTURE_NATIVE_REPO=$REPO make capture-native-record CLIENT=codex$SCENARIO_ARG"
        blocked "no paid run: the new Codex hooks need your trust review first"
        summary
    fi

    : > "$OUT/runs.txt"
    SESSION=

    # The hook processes start as children of the client, so they get
    # this switch from the environment of the paid run. With it, the
    # lesson hooks also write the evaluation and reset records that the
    # timing replay reads.
    SEAMARK_RECORD_EVALUATIONS=1
    export SEAMARK_RECORD_EVALUATIONS

    if [ "${CAPTURE_NATIVE_RUNS:-1}" = 0 ]; then
        note "CAPTURE_NATIVE_RUNS=0: no paid run; collecting what the repository already holds"
    else
        echo "paid runs ($CLIENT; costs tokens)"
        case "$CLIENT" in
            claude) timing_begin; record_claude_timing ;;
            codex)  timing_begin; record_codex_timing ;;
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
    "transcripts": sorted(p for p in paths if os.path.isfile(p)),
    "extras": {"exec-events.jsonl": os.path.join(out, "exec-events.jsonl")}
    if os.path.isfile(os.path.join(out, "exec-events.jsonl")) else {},
}
with open(os.path.join(out, "record.json"), "w") as f:
    json.dump(record, f, indent=1)
PY

    timing_extras

    if python3 -I "$SCRIPTS/capture-native-sanitize.py" "$OUT/record.json" "$OUT/sanitized"; then
        ok "sanitized copy in $OUT/sanitized; compare it with $DATA before you replace the evidence"
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
            note "  4. CAPTURE_NATIVE_REPO=$REPO make capture-native-record CLIENT=codex$SCENARIO_ARG"
        fi
    else
        ok "the hooks recorded $(find "$OBS" -name '*.meta.json' | wc -l | tr -d ' ') payloads"
    fi
}

# The gate-3 timing scenario (CAPTURE_NATIVE_SCENARIO=timing). It records
# when lesson advice reaches the model: a prior result before a later
# model step, a reminder on its own call, edits that one response
# constructs together, a failed read, and a subagent. Each paid run is
# one case. timing-cases.jsonl names the case of each run, so the replay
# tests need no session identifier. The reviewer copies the reviewed
# sanitized copy into internal/integration/testdata/<client>/timing.
# There the tests TestCaptureNativeClaudeTimingRecording and
# TestCaptureNativeCodexTimingRecording decode it.

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
# The synthetic lesson of the gate-3 timing recording
# (scripts/capture-native.sh, CAPTURE_NATIVE_SCENARIO=timing).
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
    cp "$SCRIPTS/capture-native-observer.py" "$INSTR/observer.py"
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

    if (cd "$REPO" && claude -p "$@" < "$OUT/prompt-$RUN.txt" > "$OUT/run-$RUN.json" 2> "$OUT/run-$RUN-stderr.txt"); then
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

# record_claude_timing runs the seven Claude Code timing cases. Each case
# starts a new session, except new-model-step: it resumes the session of
# prior-result, so its edit comes one turn after the read. The reader
# subagent has only the Read tool.
record_claude_timing() {
    model=${CAPTURE_NATIVE_CLAUDE_MODEL:-haiku}
    agents='{"reader":{"description":"Reads the one file it is given and reports its first line.","prompt":"Use the Read tool exactly once on the file that you are given, and reply with its first line only.","model":"'"$model"'","tools":["Read"]}}'
    steps='Do exactly these steps in order, and do not add steps.'

    timing_claude prior-result new "$steps
1. Use the Read tool to read the file timing/prior.txt.
Then reply with the single word: done"

    timing_claude new-model-step resume "$steps
1. Before any tool call, write one line: the code word of the seamark review lesson that you got for timing/prior.txt, or the word none.
2. Use the Edit tool to replace the line alpha with the line beta in timing/prior.txt.
Then reply with the single word: done"

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

    timing_claude subagent-read new "$steps
1. Use the Agent tool with subagent_type \"reader\" and ask it to read the file timing/child.txt.
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
    if (cd "$REPO" && PATH="$(dirname "$BIN"):$PATH" codex "$@" < "$OUT/prompt-$RUN.txt" > "$OUT/run-$RUN-events.jsonl" 2> "$OUT/run-$RUN-stderr.txt"); then
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
        fail "run $RUN ($tcase) failed: $(tail -1 "$OUT/run-$RUN-stderr.txt")"
    fi
}

# record_codex_timing runs the five Codex timing cases. Each case starts
# a new thread, except patch-after-preparation: it resumes the thread of
# prior-preparation, so its patch comes one turn after the preparation.
record_codex_timing() {
    model=${CAPTURE_NATIVE_CODEX_MODEL:-gpt-5.6-luna}
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

    timing_codex subagent-patch new "$steps
1. Use spawn_agent to start one worker agent, and ask it to use apply_patch to add the new file timing/child-patch.txt with the single line: child.
2. Wait for the agent with wait_agent, then close it.
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

case "$MODE" in
    record) record_mode ;;
esac

summary
