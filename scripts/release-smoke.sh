#!/usr/bin/env sh
# End-to-end smoke test of a built seamark binary: a fresh fixture repo
# goes through init → index → why → gate → status → doctor. The release
# workflow runs this against every unpacked platform archive before it
# ships — an archive that cannot reach a useful `orient` does not get
# released. Works with a plain POSIX shell; needs git on PATH.
set -eu

if [ $# -ne 1 ] || [ ! -x "$1" ]; then
    echo "usage: $0 <path-to-seamark-binary>" >&2
    exit 2
fi

BIN=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
cd "$TMP"

# A minimal but real fixture: a git repo with one call edge.
git init -q -b main .
git config user.name smoke
git config user.email smoke@example.invalid
cat > go.mod <<'EOF'
module example.com/smoke
EOF
cat > main.go <<'EOF'
package main

func main() { helper() }

// helper does the work.
func helper() {}
EOF
git add -A
git commit -q -m "fixture"

fail() {
    echo "smoke: FAIL at $1" >&2
    exit 1
}

# expect <needle> <cmd...>: the command must BOTH exit zero and print
# the needle. Never `cmd | grep`: a pipeline's status is grep's, so a
# command that printed the needle and then crashed would still pass.
expect() {
    needle=$1
    shift

    out=$("$@") || fail "$* (exit status $?)"
    printf '%s\n' "$out" | grep -q -- "$needle" || fail "$* (output lacks \"$needle\")"
}

expect seamark          "$BIN" version
expect "gate    warn"   "$BIN" init
expect symbols          "$BIN" index
expect orientation      "$BIN" orient
expect helper           "$BIN" why helper
expect allow            "$BIN" gate --command "ls -la"
expect workspace        "$BIN" status
expect schema_version   "$BIN" status --json
"$BIN" doctor           || fail "doctor (a fresh fixture must pass)"

echo "smoke: ok ($("$BIN" version))"
