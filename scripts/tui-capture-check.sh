#!/usr/bin/env sh
# Compares a fresh render of the console captures with a base set, so a
# refactor of internal/tui can prove that no frame changed. The script
# renders the captures into a new temporary directory and never writes
# into the base directory. Render the base from the starting tree on the
# same day: make tui-capture TUI_CAPTURE_DIR=<scratch>.
#
# Some capture text moves with the clock: the footer clock, the log
# times, the dates of "ago" columns, and elapsed durations. The script
# masks that text in both sets before it compares them. Each mask keeps
# the width of the text, so a change in the column layout still shows.
#
# The script prints the file counts, the names of the files that differ
# or exist in one set only, and the first unified diffs. It prints the
# first 10 names of each list; with LIST=1 in the environment it prints
# every name, so a record of expected differences can name each frame.
# The exit status is 0 when every file matches, 1 when a file differs or
# exists in one set only, and 2 for a usage error or a failed render.
# Works with a plain POSIX shell; needs go and perl on PATH.
set -eu

# LIST=1 prints every name of each list; any other value keeps the
# first 10.
LIST=${LIST:-}

# The number of diffs to print, and the line limit of each diff. A full
# frame diff of the large size has about 100 lines.
SHOW_DIFFS=3
DIFF_LINES=60

usage() {
    echo "usage: $0 <base-capture-dir>" >&2
    exit 2
}

[ $# -eq 1 ] && [ -n "$1" ] || usage

if [ ! -d "$1" ]; then
    echo "$0: the base directory $1 does not exist." >&2
    echo "$0: render one from the starting tree: make tui-capture TUI_CAPTURE_DIR=<dir>" >&2
    exit 2
fi

# Resolve the base before the script changes its directory, because a
# relative path names a directory below the caller's working directory.
BASE=$(cd "$1" && pwd -P)
SEAMARK_REPO=$(cd "$(dirname "$0")/.." && pwd -P)

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir "$TMP/render" "$TMP/base" "$TMP/new"

# The capture tests write one file per frame into the directory that the
# variable names. -count=1 forces a new render, because a cached test
# result writes no file.
if ! (cd "$SEAMARK_REPO" && SEAMARK_TUI_CAPTURE_DIR="$TMP/render" go test -count=1 -run '^TestCapture' ./internal/tui); then
    echo "$0: the capture tests failed, so the script compares nothing" >&2
    exit 2
fi

# mask_dir <from> <to> <list>: copies every frame (*.txt) of from into
# to, with the volatile text masked, and writes the file names into
# list. Perl does the masks, because the BSD sed of macOS silently
# ignores \b.
#
#   2026-10-04  -> YYYY-MM-DD   (a date)
#   20:40:32    -> HH:MM:SS     (a clock time with seconds)
#   18:40       -> HH:MM        (a clock time)
#   2m 14s, 0s  -> #m ##s, #s   (an elapsed duration in d, h, m, s)
#
# The list uses one byte order, because comm needs sorted input.
mask_dir() {
    : >"$3"

    # Only *.txt files are frames. A capture directory can also hold
    # notes, such as EXPECTED-DIFFS.md, which are not frames.
    for f in "$1"/*.txt; do
        [ -f "$f" ] || continue

        name=$(basename "$f")
        perl -pe '
            s/\b[0-9]{4}-[0-9]{2}-[0-9]{2}\b/YYYY-MM-DD/g;
            s/\b[0-9]{2}:[0-9]{2}:[0-9]{2}\b/HH:MM:SS/g;
            s/\b[0-9]{2}:[0-9]{2}\b/HH:MM/g;
            s{\b[0-9]+[dhms](?: [0-9]+[hms])*\b}{(my $d = $&) =~ tr/0-9/#/; $d}ge;
        ' "$f" >"$2/$name"
        printf '%s\n' "$name" >>"$3"
    done

    LC_ALL=C sort -o "$3" "$3"
}

mask_dir "$BASE" "$TMP/base" "$TMP/base.list"
mask_dir "$TMP/render" "$TMP/new" "$TMP/new.list"

LC_ALL=C comm -23 "$TMP/base.list" "$TMP/new.list" >"$TMP/only-base.list"
LC_ALL=C comm -13 "$TMP/base.list" "$TMP/new.list" >"$TMP/only-new.list"
LC_ALL=C comm -12 "$TMP/base.list" "$TMP/new.list" >"$TMP/both.list"

: >"$TMP/changed.list"
while IFS= read -r name; do
    cmp -s "$TMP/base/$name" "$TMP/new/$name" || printf '%s\n' "$name" >>"$TMP/changed.list"
done <"$TMP/both.list"

count() {
    wc -l <"$1" | tr -d ' '
}

CHANGED=$(count "$TMP/changed.list")
ONLY_BASE=$(count "$TMP/only-base.list")
ONLY_NEW=$(count "$TMP/only-new.list")

echo "base: $BASE"
echo "base $(count "$TMP/base.list"), new $(count "$TMP/new.list"), changed $CHANGED, only in base $ONLY_BASE, only in new $ONLY_NEW"

if [ "$CHANGED" -eq 0 ] && [ "$ONLY_BASE" -eq 0 ] && [ "$ONLY_NEW" -eq 0 ]; then
    echo "tui-capture-check: ok, every frame matches after the masks"
    exit 0
fi

# list_names <title> <file>: prints the first names of a list, so a large
# difference stays readable. With LIST=1 it prints every name.
list_names() {
    [ -s "$2" ] || return 0

    echo "$1:"

    if [ "$LIST" = 1 ]; then
        sed 's/^/  /' "$2"
        return 0
    fi

    head -n 10 "$2" | sed 's/^/  /'
    [ "$(count "$2")" -le 10 ] || echo "  ... (LIST=1 prints every name)"
}

list_names "only in base" "$TMP/only-base.list"
list_names "only in new" "$TMP/only-new.list"
list_names "changed" "$TMP/changed.list"

# The diffs run inside TMP, so their headers name base/<file> and
# new/<file> and not the temporary path.
head -n "$SHOW_DIFFS" "$TMP/changed.list" | while IFS= read -r name; do
    echo
    (cd "$TMP" && diff -u "base/$name" "new/$name" | head -n "$DIFF_LINES") || true
done

echo
echo "tui-capture-check: FAIL, the frames differ from the base" >&2
exit 1
