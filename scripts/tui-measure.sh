#!/usr/bin/env sh
# Runs the Phase 4 benchmarks (every benchmark named Phase4) and writes
# the raw go test output into one file. limits.md of the Phase 4
# validation reads its numbers from that file, so the file starts with
# the Go version and the machine. Only a run that passes replaces the
# file: a failed or interrupted run leaves the old file as it was.
#
# usage: tui-measure.sh <output-file> <package>...
#
# The environment sets the run:
#
#   MEASURE_BENCHTIME  the -benchtime value; empty keeps the Go default
#                      (1s). 1x is a smoke run: it proves that each
#                      benchmark runs, and its numbers mean nothing.
#   MEASURE_COUNT      the -count value; empty means 1.
#   MEASURE_BASE       a git revision. The script also runs the hook
#                      selection benchmark on the internal/report
#                      sources of that revision ("before"). Empty skips
#                      the before run, and the file says so.
#   MEASURE_BASE_DIR   a directory with the internal/report production
#                      sources of the base. It replaces MEASURE_BASE, for
#                      a tree without git history.
#
# Each part of the output starts with a "tree: current" or a
# "tree: base <name>" line. benchstat reads such a line as a
# configuration value, so `benchstat -col tree <file>` compares before
# and after. The exit status is 0 when every run passes, 1 when a run
# fails, and 2 for a usage error or an unreadable base. The script reads
# the base before the first run. So a bad base stops the script before
# any benchmark runs, and no run output is lost. A run that fails prints
# its output on stderr, so the cause stays visible. An interrupted run
# prints nothing. Works with a plain POSIX shell; needs go on PATH, and
# git for MEASURE_BASE.
set -eu

usage() {
    echo "usage: $0 <output-file> <package>..." >&2
    exit 2
}

[ $# -ge 2 ] && [ -n "$1" ] || usage

SEAMARK_REPO=$(cd "$(dirname "$0")/.." && pwd -P)

# Resolve the output before the script changes its directory, because a
# relative path names a file below the caller's working directory.
mkdir -p "$(dirname "$1")"
OUT="$(cd "$(dirname "$1")" && pwd -P)/$(basename "$1")"
shift

# -p 1 runs one test binary at a time. Two benchmark binaries that run
# together share the processor and change each other's numbers.
FLAGS="-p 1 -run ^$ -benchmem -count ${MEASURE_COUNT:-1}"
if [ -n "${MEASURE_BENCHTIME:-}" ]; then
    FLAGS="$FLAGS -benchtime $MEASURE_BENCHTIME"
fi

# The runs write into PART beside OUT, and finish renames PART to OUT
# only after every run passes. One directory makes the rename one step,
# so OUT is never a partial file. The PID keeps two runs apart.
PART="$OUT.$$.part"

TMP=$(mktemp -d)
trap 'rm -rf "$TMP" "$PART"' EXIT

# A POSIX shell need not run the EXIT trap when a signal stops it. These
# traps turn an interrupt into an exit, so the cleanup above still runs.
trap 'exit 130' INT
trap 'exit 143' TERM

STATUS=0

# finish ends a run that reached its end. A run that passes replaces OUT
# with the complete output. A failed run keeps OUT as it was and prints
# its output on stderr. The exits before finish never touch OUT.
finish() {
    if [ "$STATUS" -eq 0 ]; then
        mv -f "$PART" "$OUT"
    else
        cat "$PART" >&2
        echo "tui-measure: a run failed; $OUT stays as it was" >&2
    fi

    exit "$STATUS"
}

# The script reads the base before the first run, because a bad base
# found after the run would discard the run output. BASE_SRC is the
# directory of the base sources. An empty BASE_SRC skips the before run.
BASE_SRC=""
BASE_NAME=""

if [ -n "${MEASURE_BASE_DIR:-}" ]; then
    if [ ! -d "$MEASURE_BASE_DIR" ]; then
        echo "$0: the base directory $MEASURE_BASE_DIR does not exist." >&2
        exit 2
    fi

    BASE_SRC=$(cd "$MEASURE_BASE_DIR" && pwd -P)
    BASE_NAME=$(basename "$BASE_SRC")
elif [ -n "${MEASURE_BASE:-}" ]; then
    BASE_SRC="$TMP/base"
    BASE_NAME="$MEASURE_BASE"
    mkdir "$BASE_SRC"

    if ! names=$(cd "$SEAMARK_REPO" && git ls-tree --name-only "$MEASURE_BASE" internal/report/); then
        echo "$0: git cannot list internal/report at $MEASURE_BASE; set MEASURE_BASE_DIR or MEASURE_BASE=" >&2
        exit 2
    fi

    for name in $names; do
        case "$name" in
        *_test.go) ;;
        *.go) (cd "$SEAMARK_REPO" && git show "$MEASURE_BASE:$name") >"$BASE_SRC/$(basename "$name")" ;;
        esac
    done
fi

{
    echo "# seamark tui-measure, $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# $(go version)"
    echo "# $(uname -a)"

    # Only macOS has this key; elsewhere go test prints a cpu line.
    cpu=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || true)
    if [ -n "$cpu" ]; then
        echo "# cpu: $cpu"
    fi

    echo "# go test $FLAGS -bench Phase4 $*"
    echo
    echo "tree: current"
} >"$PART"

# The flags stay unquoted on purpose: each one is a separate argument.
# shellcheck disable=SC2086
if ! (cd "$SEAMARK_REPO" && go test $FLAGS -bench Phase4 "$@") >>"$PART" 2>&1; then
    STATUS=1
fi

if [ -z "$BASE_SRC" ]; then
    echo >>"$PART"
    echo "# before: skipped, because MEASURE_BASE and MEASURE_BASE_DIR are empty" >>"$PART"
    echo "tui-measure: the before run was skipped (MEASURE_BASE is empty)" >&2
    finish
fi

# The before run of the hook selection. BenchmarkPhase4LessonsForFilesBudget
# calls only the production wrapper, so it compiles against the report
# sources of the base. A go test -overlay swaps the production files of
# internal/report and leaves every other package as it is.
REPORT="$SEAMARK_REPO/internal/report"

# keep_test lists the test files of the before run: the benchmark and
# the files of its helpers (seedStore, goldenConfig, distinctPins).
# Other test files can use the typed selection, which the base does not
# have, so the overlay removes them.
keep_test() {
    case "$1" in
    selection_bench_test.go | report_test.go | selection_golden_test.go | edit_lessons_test.go) return 0 ;;
    esac

    return 1
}

# The overlay changes each current file of the package. A production
# file gets its base version, or nothing when the base has none. A test
# file outside keep_test gets nothing. A base file that the current
# package lacks is added under its own name.
{
    printf '{"Replace": {'
    sep=""

    for f in "$REPORT"/*.go; do
        name=$(basename "$f")
        to=""

        case "$name" in
        *_test.go) keep_test "$name" && continue ;;
        *) [ -f "$BASE_SRC/$name" ] && to="$BASE_SRC/$name" ;;
        esac

        printf '%s\n "%s": "%s"' "$sep" "$f" "$to"
        sep=","
    done

    for f in "$BASE_SRC"/*.go; do
        name=$(basename "$f")
        [ -f "$REPORT/$name" ] && continue

        printf '%s\n "%s": "%s"' "$sep" "$REPORT/$name" "$f"
        sep=","
    done

    printf '\n}}\n'
} >"$TMP/overlay.json"

{
    echo
    echo "tree: base $BASE_NAME"
} >>"$PART"

# shellcheck disable=SC2086
if ! (cd "$SEAMARK_REPO" && go test -overlay "$TMP/overlay.json" $FLAGS -bench Phase4LessonsForFilesBudget ./internal/report) >>"$PART" 2>&1; then
    STATUS=1
fi

finish
