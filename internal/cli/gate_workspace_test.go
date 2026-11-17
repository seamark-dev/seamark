package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// TestGateRefusesAMissingWorkspace covers a mistyped -C. The gate must
// not read the default policy there, and its audit must not create the
// mistyped directory.
func TestGateRefusesAMissingWorkspace(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "typo", "sub")

	out, _, err := runErr(t, "-C", missing, "gate", "--enforce", "--command", "rm -rf /tmp/x")
	require.ErrorIs(t, err, gate.ErrBlocked)
	assert.EqualError(t, err, "blocked by policy: workspace "+missing+": no such file or directory")
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Empty(t, out, "a refused gate prints no verdict")
	assert.NoDirExists(t, filepath.Join(base, "typo"), "the refusal creates no directory")

	// Without --enforce the same failure is a plain error, and it still
	// writes nothing.
	_, _, err = runErr(t, "-C", missing, "gate", "--command", "rm -rf /tmp/x")
	require.Error(t, err)
	assert.NotErrorIs(t, err, gate.ErrBlocked)
	assert.NoDirExists(t, filepath.Join(base, "typo"))
}

// TestGateRefusesAFileWorkspace covers a -C that names a regular file.
// The audit used to fail on it with a mkdir warning after an allow.
func TestGateRefusesAFileWorkspace(t *testing.T) {
	root := writeFixture(t)
	file := filepath.Join(root, "go.mod")

	before, err := os.ReadFile(file)
	require.NoError(t, err)

	out, stderr, err := runErr(t, "-C", file, "gate", "--enforce", "--command", "rm -rf /tmp/x")
	require.ErrorIs(t, err, gate.ErrBlocked)
	assert.ErrorIs(t, err, workspace.ErrNotDirectory)
	assert.Empty(t, out)
	assert.NotContains(t, stderr, "audit log", "the gate stops before the audit")

	after, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the file keeps its bytes")
	assert.NoDirExists(t, filepath.Join(root, ".seamark"))
}

// TestGateHookRefusesAMissingWorkspace covers hook mode. Each client gets
// its own failure reply under --enforce, and a plain error without it.
// The path holds a control byte, because the reply reaches the agent.
func TestGateHookRefusesAMissingWorkspace(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "typo"+string(rune(0x1b))+"[2J")
	want := "blocked by policy: workspace " + render.Sanitize(missing) + ": no such file or directory"
	payload := `{"tool_name":"Bash","tool_input":{"command":"rm -rf /tmp/x"}}`

	for name, client := range map[string][]string{"claude": nil, "codex": {"--client", "codex"}} {
		args := append([]string{"-C", missing, "gate", "--enforce", "--hook"}, client...)

		out, _, err := runIn(t, payload, args...)
		require.ErrorIs(t, err, gate.ErrBlocked, name)
		assert.EqualError(t, err, want, name)
		assert.Empty(t, out, name)

		// The block comes from the client failure reply, which carries
		// only the text that the agent reads, not the Go error chain.
		assert.NotErrorIs(t, err, fs.ErrNotExist, name)

		args = append([]string{"-C", missing, "gate", "--hook"}, client...)

		_, _, err = runIn(t, payload, args...)
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, gate.ErrBlocked, name)
	}

	assert.NoDirExists(t, missing, "no hook run creates the directory")
}

// TestGateRefusesAPolicyItCannotRead covers a policy file with mode 000.
// The gate used to load the default rules and allow under --enforce.
func TestGateRefusesAPolicyItCannotRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file with mode 0000")
	}

	root := writeFixture(t)
	policyPath := filepath.Join(root, ".seamark", "policy.yaml")

	require.NoError(t, os.MkdirAll(filepath.Dir(policyPath), 0o755))
	require.NoError(t, os.WriteFile(policyPath, []byte(starterPolicyFor(gateModeEnforce)), 0o644))
	require.NoError(t, os.Chmod(policyPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(policyPath, 0o644) })

	out, _, err := runErr(t, "-C", root, "gate", "--enforce", "--command", "ls")
	require.ErrorIs(t, err, gate.ErrBlocked)
	assert.EqualError(t, err, "blocked by policy: gate: "+policyPath+": permission denied")
	assert.Empty(t, out)
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "audit.jsonl"), "a refused gate writes no audit entry")

	// Only the flag enforces before the policy loads.
	_, _, err = runErr(t, "-C", root, "gate", "--command", "ls")
	require.Error(t, err)
	assert.NotErrorIs(t, err, gate.ErrBlocked)
	assert.NoFileExists(t, filepath.Join(root, ".seamark", "audit.jsonl"))
}
