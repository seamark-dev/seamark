#!/usr/bin/env sh
# Native checks of one agent integration against the installed client
# CLI, in a disposable repository. Two modes:
#
#   check  offline: no model, no credentials. Records the client
#          version, sets the client up with the built binary, and
#          verifies the generated configuration, the generated hook
#          commands, the offline diagnostics, and that no credential
#          from the environment reaches a generated file. For Codex it
#          also asks the client to read the generated MCP registration
#          (`codex mcp list`) from a scratch CODEX_HOME that trusts the
#          disposable repository, so the user's own trust records stay
#          untouched.
#   smoke  inference: runs one bounded distillation through the
#          installed client with the operator's own login. Costs
#          tokens. Refuses to start without a verified login and never
#          starts a login flow.
#
# Every check prints ok, FAIL, or blocked (a precondition is missing,
# so the check produced no evidence). The summary never calls a blocked
# check passed: the exit status is 0 only when every check is ok, 1 when
# one failed, and 3 when one was blocked. Works with a plain POSIX
# shell; needs git on PATH.
set -eu

usage() {
    echo "usage: $0 check|smoke <client> <path-to-seamark-binary>" >&2
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
    check|smoke) ;;
    *) usage ;;
esac

case "$CLIENT" in
    claude|codex) ;;
    *) echo "$0: unknown client \"$CLIENT\" (known: claude, codex)" >&2; exit 2 ;;
esac

# The client executable, the files setup writes for it, and the client
# flag at the end of its hook markers. Claude Code hooks carry no
# --client flag, because a hook command without the flag reads a
# Claude Code event.
case "$CLIENT" in
    claude) CLI=claude; HOOKS_FILE=.claude/settings.json; REGISTRATION_FILE=.mcp.json;          HOOK_FLAG= ;;
    codex)  CLI=codex;  HOOKS_FILE=.codex/hooks.json;     REGISTRATION_FILE=.codex/config.toml; HOOK_FLAG=" --client codex" ;;
esac

SEAMARK_REPO=$(cd "$(dirname "$0")/.." && pwd)

OK=0
FAILED=0
BLOCKED=0
# summary sets SUMMARIZED. The exit trap reads it, so a run that already
# printed its summary does not print a second one.
SUMMARIZED=

ok()      { OK=$((OK + 1));           echo "  ok       $1"; }
fail()    { FAILED=$((FAILED + 1));   echo "  FAIL     $1" >&2; }
blocked() { BLOCKED=$((BLOCKED + 1)); echo "  blocked  $1"; }

# expect <label> <needle> <cmd...>: ok when the command exits zero AND
# prints the needle. Never `cmd | grep`: a pipeline's status is grep's.
# The output stays in $out, so the next check can read it without a
# second run.
expect() {
    label=$1
    needle=$2
    shift 2

    if out=$("$@" 2>&1); then
        if printf '%s\n' "$out" | grep -q -- "$needle"; then
            ok "$label"
        else
            fail "$label (output lacks \"$needle\")"
        fi
    else
        fail "$label (exit status $?: $(printf '%s\n' "$out" | tail -1))"
    fi
}

# expect_lacks <label> <needle> <cmd...>: ok when the command exits
# zero AND never prints the needle. A failed command can stop before
# the line that holds the needle, so its output proves nothing.
expect_lacks() {
    label=$1
    needle=$2
    shift 2

    if out=$("$@" 2>&1); then
        if printf '%s\n' "$out" | grep -q -- "$needle"; then
            fail "$label (\"$needle\" found)"
        else
            ok "$label"
        fi
    else
        fail "$label (exit status $?: $(printf '%s\n' "$out" | tail -1))"
    fi
}

# absent <label> <needle> <file...>: ok when no file holds the needle.
absent() {
    label=$1
    needle=$2
    shift 2

    if grep -q -- "$needle" "$@" 2>/dev/null; then
        fail "$label (\"$needle\" found)"
    else
        ok "$label"
    fi
}

# snapshot: prints a checksum line for every file in the repository
# outside .git, in path order. Two snapshots differ when a file is
# added, removed, or changed.
snapshot() {
    find . -path ./.git -prune -o -type f -exec cksum {} + | sort -k 3
}

# changed_since <snapshot-file>: prints on one line each path whose
# checksum line differs from the saved snapshot, and each file that is
# newer than the snapshot file. Setup writes a new file and renames it
# over the old one. So a rewrite with the same bytes or a new mode also
# gives the file a new modification time.
changed_since() {
    {
        snapshot | diff "$1" - | sed -n 's|^[<>] [0-9]* [0-9]* \./||p'
        find . -path ./.git -prune -o -type f -newer "$1" -print | sed 's|^\./||'
    } | sort -u | paste -s -d ' ' -
}

summary() {
    SUMMARIZED=1
    echo
    echo "agent-integration $MODE ($CLIENT): $OK ok, $FAILED failed, $BLOCKED blocked"

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

# The record header: what ran against what. A reader of a pasted run
# needs the versions before the checks.
echo "agent-integration $MODE — client $CLIENT"
echo "  seamark   $("$BIN" version)"

if command -v "$CLI" >/dev/null 2>&1; then
    echo "  $CLI    $("$CLI" --version 2>&1 | head -1)"
else
    blocked "$CLI is not on PATH; install it to run the native checks"
    summary
fi

echo "  date      $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# Under set -e, a failed command outside the checks stops the script,
# for example a fixture step. The exit trap then reports a blocked
# check, because the later checks produce no evidence. It also prints
# the summary, so every run ends with a result.
TMP=
finish() {
    status=$?
    [ -z "$TMP" ] || rm -rf "$TMP"

    if [ -z "$SUMMARIZED" ]; then
        blocked "the run stopped: a command outside the checks failed (exit status $status)"
        summary
    fi
}
trap finish EXIT

TMP=$(mktemp -d)

# Codex keeps login, rules, and trust under CODEX_HOME. A scratch home
# keeps every run away from the user's records; the smoke mode needs
# the real login and leaves CODEX_HOME alone.
if [ "$MODE" = check ] && [ "$CLIENT" = codex ]; then
    CODEX_HOME=$TMP/codex-home
    export CODEX_HOME
    mkdir -p "$CODEX_HOME"
fi

# A disposable repository with one call edge, like the release smoke.
# The physical path matters: Codex keys project trust by the resolved
# directory, and a temporary directory on macOS sits behind a link.
mkdir -p "$TMP/repo"
cd "$TMP/repo"
REPO=$(pwd -P)
git init -q -b main .
git config user.name smoke
git config user.email smoke@example.invalid
cat > go.mod <<'GOMOD'
module example.com/smoke
GOMOD
cat > main.go <<'GO'
package main

func main() { helper() }

// helper does the work.
func helper() {}
GO
git add -A
git commit -q -m "fixture"

check_mode() {
    # Canaries: credential-shaped values in the environment must never
    # reach a generated file or the setup output. Offline only: the
    # smoke mode needs the operator's real credentials in place.
    CANARY=sk-native-canary-0f9e8d7c
    export OPENAI_API_KEY=$CANARY CODEX_API_KEY=$CANARY ANTHROPIC_API_KEY=$CANARY

    echo "generated configuration"
    expect "init --client $CLIENT --skills --approve-tools" "$HOOKS_FILE" \
        "$BIN" init --client "$CLIENT" --skills --approve-tools
    [ -f "$HOOKS_FILE" ] && ok "$HOOKS_FILE written" || fail "$HOOKS_FILE missing"
    [ -f "$REGISTRATION_FILE" ] && ok "$REGISTRATION_FILE written" || fail "$REGISTRATION_FILE missing"

    # Idempotence: a second init changes no file and narrates no write.
    # The narrators start a write line with "wrote", "updated", or
    # "approved", and note a removed hook flag with "removed". The
    # snapshot and the file times also catch a write that no line names.
    snapshot > "$TMP/before-second-init"
    expect "second init keeps every file" "kept" "$BIN" init --client "$CLIENT" --skills --approve-tools
    write_line=$(printf '%s\n' "$out" | grep -E '^ +(note +)?(wrote|updated|approved|removed) ' | head -1 | sed 's/^ *//')
    changed=$(changed_since "$TMP/before-second-init")

    if [ -n "$write_line" ]; then
        fail "second init narrates a write: $write_line"
    else
        ok "second init narrates no write"
    fi

    if [ -n "$changed" ]; then
        fail "second init changed files: $changed"
    else
        ok "second init changes no file"
    fi

    absent "no credential in the generated files" "$CANARY" "$HOOKS_FILE" "$REGISTRATION_FILE" .seamark/config.yaml
    # One check per command. Under set -e, a failed command inside one
    # shared substitution stops the script before it prints a result.
    expect_lacks "no credential in the init --print output" "$CANARY" \
        "$BIN" init --client "$CLIENT" --skills --approve-tools --print

    echo "generated hook commands"
    # Each command must be the exact command setup writes for BIN: its
    # absolute path, then the marker. Each must run as written on a
    # native-shaped payload and never block a harmless command.
    if GATE_CMD=$(hook_command "$HOOKS_FILE" "gate --hook$HOOK_FLAG"); then
        payload=$(printf '{"session_id":"native-check","cwd":"%s","hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"call_native_1","tool_input":{"command":"ls -la"}}' "$REPO")
        if printf '%s' "$payload" | sh -c "$GATE_CMD" >/dev/null 2>&1; then
            ok "gate hook runs as written and lets a harmless command through"
        else
            fail "gate hook command failed: $GATE_CMD"
        fi
    else
        fail "no gate hook command for $BIN in $HOOKS_FILE"
    fi

    if LESSONS_CMD=$(hook_command "$HOOKS_FILE" "lessons --hook$HOOK_FLAG"); then
        case "$CLIENT" in
            codex)  edit='{"tool_name":"apply_patch","tool_input":{"command":"*** Begin Patch\n*** Add File: docs/new.md\n+hello\n*** End Patch\n"}}' ;;
            claude) edit='{"tool_name":"Write","tool_input":{"file_path":"docs/new.md","content":"hello"}}' ;;
        esac
        payload=$(printf '{"session_id":"native-check","cwd":"%s","hook_event_name":"PreToolUse","tool_use_id":"call_native_2",%s' "$REPO" "${edit#\{}")
        if printf '%s' "$payload" | sh -c "$LESSONS_CMD" >/dev/null 2>&1; then
            ok "lessons hook runs as written and never blocks an edit"
        else
            fail "lessons hook command failed: $LESSONS_CMD"
        fi
    else
        fail "no lessons hook command for $BIN in $HOOKS_FILE"
    fi

    echo "offline diagnostics"
    expect "index" "symbols" "$BIN" index
    "$BIN" doctor >/dev/null 2>&1 && ok "doctor passes" || fail "doctor fails on a fresh setup"
    expect "doctor names the $CLIENT hooks" "$CLIENT gate (warn) + lessons hooks installed" "$BIN" doctor
    expect "status names the $CLIENT registration" "$CLIENT" "$BIN" status
    expect "status --json carries the clients array" '"clients"' "$BIN" status --json
    # Status exits non-zero without an index, so its leak check runs
    # after `index`. The doctor leak check runs here too, next to the
    # other doctor checks.
    expect_lacks "no credential in the doctor output" "$CANARY" "$BIN" doctor
    expect_lacks "no credential in the status --json output" "$CANARY" "$BIN" status --json

    if [ "$CLIENT" = codex ]; then
        echo "native registration"
        # Codex reads a project's .codex/ only when the project is trusted.
        # The scratch home trusts the disposable repository and nothing
        # else; `codex mcp list` then parses the generated table offline.
        printf '[projects."%s"]\ntrust_level = "trusted"\n' "$REPO" > "$CODEX_HOME/config.toml"
        if out=$(codex mcp list --json 2>&1); then
            if printf '%s' "$out" | grep -q '"name": *"seamark"'; then
                ok "codex mcp list reads the generated registration in a trusted project"
            else
                fail "codex mcp list does not show the seamark registration: $(printf '%s' "$out" | head -3)"
            fi
        else
            blocked "codex mcp list failed: $(printf '%s' "$out" | tail -1)"
        fi
        rm -f "$CODEX_HOME/config.toml"
        if out=$(codex mcp list --json 2>&1) && [ "$(printf '%s' "$out" | tr -d '[:space:]')" = "[]" ]; then
            ok "an untrusted project exposes no registration (trust stays the user's decision)"
        else
            blocked "could not confirm that an untrusted project hides the registration"
        fi
        echo "  note     hook trust is not checked here: Codex records it only after the user reviews the hooks with /hooks"
    fi
}

smoke_mode() {
    echo "authentication"
    case "$CLIENT" in
        codex)
            if codex login status >/dev/null 2>&1; then
                ok "codex login status reports a login (no login flow started)"
            else
                blocked "codex login status reports no login; authenticate outside this script (see docs/agent-integrations.md)"
                summary
            fi
            ;;
        claude)
            echo "  note     claude has no offline login probe; the run fails if the CLI is not authenticated"
            ;;
    esac

    # A paid run from an uncommitted tree records evidence against
    # code that no commit names. Refuse unless the operator says so.
    if [ -z "${SEAMARK_ALLOW_DIRTY:-}" ] && [ -n "$(git -C "$SEAMARK_REPO" status --porcelain 2>/dev/null)" ]; then
        blocked "the seamark working tree is dirty; commit first, or set SEAMARK_ALLOW_DIRTY=1"
        summary
    fi

    echo "fixture"
    # Three fix commits in one area make one distillation group.
    mkdir -p pkg/store
    cat > pkg/store/cache.go <<'GO'
package store

// Cache keeps entries for reuse.
type Cache struct {
	entries map[string]int
	hits    int
}

// Reset clears the cache.
func (c *Cache) Reset() {
	c.entries = map[string]int{}
}
GO
    git add -A
    git commit -q -m "feat: add cache"
    cat > pkg/store/cache.go <<'GO'
package store

// Cache keeps entries for reuse.
type Cache struct {
	entries map[string]int
	hits    int
}

// Reset clears the cache.
func (c *Cache) Reset() {
	c.entries = map[string]int{}
	c.hits = 0
}
GO
    git commit -qam "fix: reset the hit counter with the entries"
    cat >> pkg/store/cache.go <<'GO'

// Pool reuses caches.
type Pool struct {
	free []*Cache
	used int
}

// Reset returns every cache.
func (p *Pool) Reset() {
	p.free = nil
}
GO
    git commit -qam "feat: add pool"
    sed 's/^\tp.free = nil$/\tp.free = nil\n\tp.used = 0/' pkg/store/cache.go > pkg/store/cache.go.new
    mv pkg/store/cache.go.new pkg/store/cache.go
    git commit -qam "fix: reset the used counter when the pool is reset"
    cat >> pkg/store/cache.go <<'GO'

// Stats counts.
type Stats struct {
	total int
	last  int
}

// Reset zeroes the stats.
func (s *Stats) Reset() {
	s.total = 0
}
GO
    git commit -qam "feat: add stats"
    sed 's/^\ts.total = 0$/\ts.total = 0\n\ts.last = 0/' pkg/store/cache.go > pkg/store/cache.go.new
    mv pkg/store/cache.go.new pkg/store/cache.go
    git commit -qam "fix: reset every stats field, not only the total"

    # Every later step needs the setup. A failed init, or any failed
    # check before it, stops the run here, so that it spends no tokens.
    expect "init --client $CLIENT" "" "$BIN" init --client "$CLIENT"
    [ "$FAILED" -eq 0 ] || summary
    printf 'agent:\n  cli: %s\n' "$CLIENT" >> .seamark/config.yaml
    expect "index --fixes-only mines the fix commits" "3 findings" "$BIN" index --fixes-only
    git add -A
    git commit -q -m "seamark setup"
    [ -z "$(git status --porcelain)" ] && ok "fixture tree is clean before inference" || fail "fixture tree is dirty before inference"

    echo "inference ($CLIENT; costs tokens)"
    expect "distill --dry-run discloses the $CLIENT command" "agent     $CLI" "$BIN" lessons --distill --dry-run
    # A failed agent call still prints a summary that names proposals, so
    # the needle is the outcome: no failed group, and a proposed pin.
    if out=$("$BIN" lessons --distill --limit 1 2>&1); then
        case "$out" in
            *"failed (retried next run)"*)
                fail "distill --limit 1: the agent call failed: $(printf '%s\n' "$out" | grep -m1 'failed:' | sed 's/^ *//')" ;;
            *"proposed pins"*)
                ok "distill --limit 1 read one group and proposed a pin" ;;
            *)
                fail "distill --limit 1 read a group but proposed nothing: $(printf '%s\n' "$out" | tail -1)" ;;
        esac
    else
        fail "distill --limit 1 (exit status $?: $(printf '%s\n' "$out" | tail -1))"
    fi
    expect "the proposal carries $CLIENT provenance" "\[$CLIENT/" "$BIN" lessons --proposals
    expect "extract-triggers --dry-run runs" "" "$BIN" lessons --extract-triggers --dry-run

    echo "after the run"
    [ -z "$(git status --porcelain)" ] && ok "workspace byte-identical (tracked and untracked files)" || fail "the run changed the workspace: $(git status --porcelain | head -3)"
    [ ! -f .seamark/lessons-audit.jsonl ] && ok "no lesson firing was recorded inside the run" || fail "a lesson hook fired inside the inference run"
    [ ! -f .seamark/audit.jsonl ] && ok "no gate decision was recorded inside the run" || fail "a gate hook fired inside the inference run"
    echo "  note     the fixture's hooks are untrusted in a fresh directory, so this proves nothing about trusted hooks"
    echo "  note     record this run (client version, date, result) in the compatibility record before citing it"
}

case "$MODE" in
    check) check_mode ;;
    smoke) smoke_mode ;;
esac

summary
