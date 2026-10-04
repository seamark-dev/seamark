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

# The shared helpers sit beside this script. Source them before the
# script changes its directory.
. "$(dirname "$0")/smoke-lib.sh"
resolve_binary "$1"

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
# Preview only: the fixture stays free of installed skills so `doctor`
# below keeps reporting them as info and passing. The "would write"
# lines prove the embedded skills tree made it into this archive.
expect "would write"    "$BIN" init --skills --print
# Approvals stay preview-only for the same reason: both client files are
# then absent, and `doctor` keeps reporting them as not configured.
expect "would approve 8" "$BIN" init --approve-tools --print
expect "approved 5 tools" "$BIN" init --skills=codex --approve-tools --print
# Explicit client selection: registration without grants, the Codex
# lesson hook, and the honest report that trust stays with the user.
expect "registered seamark mcp)" "$BIN" init --client codex --print
expect "lessons --hook --client codex" "$BIN" init --client codex --print
expect "setup never grants trust" "$BIN" init --client codex --print
expect symbols          "$BIN" index
expect orientation      "$BIN" orient
expect helper           "$BIN" why helper
expect allow            "$BIN" gate --command "ls -la"
expect workspace        "$BIN" status
expect skills           "$BIN" status
expect schema_version   "$BIN" status --json
"$BIN" doctor           || fail "doctor (a fresh fixture must pass)"

# Generated configuration: the Codex setup is applied for real (hooks
# and registration, no skills, no grants), and the hook commands it
# wrote must run as written. A credential-shaped value in the
# environment must reach no generated file. `doctor` must still pass:
# a registration without grants is reported, never failed.
CANARY=sk-release-smoke-canary-3c1b7a
OPENAI_API_KEY=$CANARY CODEX_API_KEY=$CANARY ANTHROPIC_API_KEY=$CANARY \
    "$BIN" init --client codex >/dev/null || fail "init --client codex"
[ -f .codex/hooks.json ]  || fail "init --client codex (no .codex/hooks.json)"
[ -f .codex/config.toml ] || fail "init --client codex (no .codex/config.toml)"
grep -q -- "$CANARY" .codex/hooks.json .codex/config.toml .claude/settings.json .seamark/config.yaml 2>/dev/null \
    && fail "a credential from the environment reached a generated file"
GATE_CMD=$(hook_command .codex/hooks.json "gate --hook --client codex") \
    || fail "init --client codex (no gate hook command for $BIN in .codex/hooks.json)"
printf '{"session_id":"smoke","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"call_smoke_1","tool_input":{"command":"ls -la"}}' "$TMP" \
    | sh -c "$GATE_CMD" || fail "generated Codex gate hook ($GATE_CMD)"
LESSONS_CMD=$(hook_command .codex/hooks.json "lessons --hook --client codex") \
    || fail "init --client codex (no lessons hook command for $BIN in .codex/hooks.json)"
printf '{"session_id":"smoke","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"apply_patch","tool_use_id":"call_smoke_2","tool_input":{"command":"*** Begin Patch\\n*** Add File: docs/new.md\\n+hello\\n*** End Patch\\n"}}' "$TMP" \
    | sh -c "$LESSONS_CMD" || fail "generated Codex lessons hook ($LESSONS_CMD)"
expect "codex gate (warn) + lessons hooks installed" "$BIN" doctor
expect "gate (warn) + lessons hooks installed" "$BIN" status
expect '"client": "codex"'  "$BIN" status --json
"$BIN" doctor           || fail "doctor (a registration without grants is reported, not failed)"

echo "smoke: ok ($("$BIN" version))"
