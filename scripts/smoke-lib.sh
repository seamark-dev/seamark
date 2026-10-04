# Shared helpers of the smoke scripts: scripts/release-smoke.sh and
# scripts/agent-integration-smoke.sh source this file, so both check
# the hook commands by one rule. Works with a plain POSIX shell.

# resolve_binary <path>: sets BIN to the physical path of the seamark
# binary at path, or exits with status 2.
#
# Setup writes the running binary into each hook command by its
# physical path, with every link resolved. The hook checks compare
# against that exact path. `cd -P` resolves a linked directory. The
# function refuses a linked binary, because setup writes the path that
# the link points to. Setup also recognizes its own hooks only by the
# binary name seamark, so the function refuses another name.
resolve_binary() {
    if [ -L "$1" ] || [ "$(basename "$1")" != seamark ]; then
        echo "$0: $1 must be the seamark binary itself, named seamark and not a link" >&2
        exit 2
    fi

    BIN=$(cd -P "$(dirname "$1")" && pwd -P)/$(basename "$1")

    # For a binary in a Homebrew Cellar, setup writes the stable opt
    # link, not the binary path. The function refuses that binary,
    # because every hook check would then report a false failure.
    case "$BIN" in
        */Cellar/seamark/*/bin/seamark)
            echo "$0: $1 is in a Homebrew Cellar, and setup writes the opt link for it; pass a built binary" >&2
            exit 2
            ;;
    esac
}

# hook_command <file> <marker>: prints the hook command that runs BIN
# with the marker, when the file holds it as a JSON string. Setup writes
# the path bare, or single-quoted when a shell would split it, so both
# forms count. A command that runs another binary never counts, even
# when it ends with the marker.
hook_command() {
    # Inside the quotes, setup writes a quote character as '\''.
    quoted="'$(printf '%s' "$BIN" | sed "s/'/'\\\\''/g")'"

    for cmd in "$BIN $2" "$quoted $2"; do
        # JSON escapes a backslash and a double quote inside a string.
        # The Claude Code settings writer also escapes &, <, and > as
        # \u0026, \u003c, and \u003e, so both encodings count.
        json=$(printf '%s' "$cmd" | sed 's/[\\"]/\\&/g')
        html=$(printf '%s' "$json" | sed 's/&/\\u0026/g; s/</\\u003c/g; s/>/\\u003e/g')

        for encoded in "$json" "$html"; do
            if grep -qF -- "\"$encoded\"" "$1" 2>/dev/null; then
                printf '%s\n' "$cmd"

                return 0
            fi
        done
    done

    return 1
}
