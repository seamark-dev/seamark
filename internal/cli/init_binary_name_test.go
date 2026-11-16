package cli

// Tests of init through a binary whose file name is not seamark, such as
// a renamed build. The test binary is such a binary: its basename is
// cli.test, and `seamark init` writes its resolved path into every hook
// command. The commands run through the whole command tree on temporary
// roots only.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// renamedBinary returns the hook path of the running test binary, and
// fails when its basename is seamark, because then the tests would
// prove nothing about another file name.
func renamedBinary(t *testing.T) string {
	t.Helper()

	bin := hooks.BinaryPath()
	require.False(t, hooks.IsSeamarkBinary(bin), "the test binary stands for a binary with another name: %s", bin)

	return bin
}

// readCodexHooks parses .codex/hooks.json under root.
func readCodexHooks(t *testing.T, root string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".codex", "hooks.json"))
	require.NoError(t, err)

	var document map[string]any
	require.NoError(t, json.Unmarshal(data, &document))

	return document
}

// initAs runs `seamark init` with args on root and returns its output.
func initAs(t *testing.T, root string, args ...string) string {
	t.Helper()

	out, err := run(t, append([]string{"-C", root, "init"}, args...)...)
	require.NoError(t, err)

	return out
}

func TestInitThroughARenamedBinaryOwnsItsClaudeCodeHooks(t *testing.T) {
	bin := renamedBinary(t)
	root := writeFixture(t)
	gitify(t, root)

	gate := func(mode string) string { return hooks.ClaudeSpecs(mode)[0].Command(bin) }
	lessons := hooks.ClaudeSpecs(hooks.ModeWarn)[1].Command(bin)
	reset := hooks.ClaudeSpecs(hooks.ModeWarn)[2].Command(bin)

	// A re-run finds the hooks that the first run wrote and adds nothing.
	initAs(t, root)
	out := initAs(t, root)

	settings := readSettings(t, root)
	assert.Equal(t, []string{gate(hooks.ModeWarn), lessons}, commands(t, settings), "a re-run adds no second set")
	assert.Equal(t, []string{reset}, commandsForEvent(t, settings, "PostCompact"))
	assert.Contains(t, out, "kept    .claude/settings.json (seamark hooks already wired)", "the re-run changes nothing")

	// A mode change rewrites the gate hook in place, so the gate line
	// describes the only gate hook there is.
	out = initAs(t, root, "--gate-mode", "enforce")
	assert.Contains(t, out, "gate    enforce")
	assert.Equal(t, []string{gate(hooks.ModeEnforce), lessons}, commands(t, readSettings(t, root)))

	out = initAs(t, root, "--gate-mode", "warn")
	assert.Contains(t, out, "gate    warn")
	assert.Equal(t, []string{gate(hooks.ModeWarn), lessons}, commands(t, readSettings(t, root)),
		"no enforce hook stays beside the warn line")

	// doctor and status run as the same binary and count the hooks.
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err = run(t, "-C", root, "doctor")
	require.NoError(t, err)
	assert.Contains(t, out, "claude gate (warn) + lessons hooks installed")
	assert.NotContains(t, out, "claude no hooks installed")

	out, err = run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "gate           hook installed (claude)")
	assert.NotContains(t, out, "no gate hook installed")
}

func TestInitThroughARenamedBinaryOwnsItsCodexHooks(t *testing.T) {
	bin := renamedBinary(t)
	root := writeFixture(t)
	gitify(t, root)

	preToolUse := func() []string { return commands(t, readCodexHooks(t, root)) }
	gate := func(mode string) string { return hooks.CodexSpecs(mode)[0].Command(bin) }
	lessons := hooks.CodexSpecs(hooks.ModeWarn)[1].Command(bin)

	initAs(t, root, "--client", "codex")
	initAs(t, root, "--client", "codex")
	assert.Equal(t, []string{gate(hooks.ModeWarn), lessons}, preToolUse(), "a re-run adds no second set")

	out := initAs(t, root, "--client", "codex", "--gate-mode", "enforce")
	assert.Contains(t, out, "gate    enforce")
	assert.Equal(t, []string{gate(hooks.ModeEnforce), lessons}, preToolUse())

	out = initAs(t, root, "--client", "codex", "--gate-mode", "warn")
	assert.Contains(t, out, "gate    warn")
	assert.Equal(t, []string{gate(hooks.ModeWarn), lessons}, preToolUse(), "no enforce hook stays beside the warn line")

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err = run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "gate           hook installed (codex)")
	assert.NotContains(t, out, "no gate hook installed")

	out, _ = run(t, "-C", root, "doctor")
	assert.Contains(t, out, "codex gate (warn) + lessons hooks installed")
	assert.NotContains(t, out, "codex no hooks installed")
}

func TestInitThroughARenamedBinaryReadsAWrapperOfItsGate(t *testing.T) {
	// A wrapper is the user's definition: setup keeps it as written. It
	// must still read the wrapper around the running binary like one
	// around a binary named seamark, or the gate line says "nothing
	// blocks" while the wrapped enforce gate blocks.
	bin := renamedBinary(t)
	root := writeFixture(t)
	gitify(t, root)

	quoted := hooks.ShellQuote(bin)
	wrapper := "test -x " + quoted + " && " + quoted + " " + hooks.GateMarker(hooks.ModeEnforce)
	writeHookDocument(t, root, ".claude/settings.json", "Bash", wrapper)

	want := []string{wrapper, hooks.ClaudeSpecs(hooks.ModeWarn)[0].Command(bin), hooks.ClaudeSpecs(hooks.ModeWarn)[1].Command(bin)}

	for _, args := range [][]string{nil, {"--gate-mode", "warn"}} {
		out := initAs(t, root, args...)
		assert.Contains(t, out, "note    .claude/settings.json: also runs `"+wrapper+"` for Bash", args)
		assert.Contains(t, out, "gate    enforce — .claude/settings.json runs its own gate hook with --enforce", args)
		assert.NotContains(t, out, "nothing blocks", args)
		assert.Equal(t, want, commands(t, readSettings(t, root)), "setup keeps the wrapper as written: %v", args)
	}

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.Contains(t, out, "gate           enforce (claude hook carries --enforce")
	assert.NotContains(t, out, "no gate hook installed")
}
