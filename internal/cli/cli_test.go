package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
)

// run executes the CLI with args against a fresh command tree, returning
// stdout. Use runErr when a test needs stderr too.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()

	stdout, _, err := runErr(t, args...)

	return stdout, err
}

func runErr(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()

	if errOut.Len() > 0 {
		t.Logf("stderr: %s", errOut.String())
	}

	return out.String(), errOut.String(), err
}

func writeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n",
		"a.go": `package main

func main() { helper() }

// helper does the work.
func helper() {}
`,
	}

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	return root
}

func TestVersionCommand(t *testing.T) {
	out, err := run(t, "version")
	require.NoError(t, err)
	assert.Contains(t, out, "seamark")
}

func TestIndexThenWhy(t *testing.T) {
	root := writeFixture(t)

	out, err := run(t, "-C", root, "index")
	require.NoError(t, err)
	assert.Contains(t, out, "symbols", "index summary should report symbol count")
	assert.FileExists(t, filepath.Join(root, ".seamark", "index.db"))

	// Symbol report: helper's caller is main, with the edge's derivation.
	out, err = run(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	for _, want := range []string{"helper", "(function)", "a.go:6", "callers (1)", "main", "[same-package]"} {
		assert.Contains(t, out, want)
	}

	// File report lists its symbols.
	out, err = run(t, "-C", root, "why", "a.go")
	require.NoError(t, err)
	assert.Contains(t, out, "defines (2)")
}

func gitify(t *testing.T, root string) {
	t.Helper()

	for _, args := range [][]string{
		{"init", "-b", "main"}, {"add", "-A"}, {"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}
}

func TestWhyWarnsWhenStale(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	_, stderr, err := runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "workspace changed", "fresh index warns about nothing")

	// Change the workspace: the same query now carries a staleness note.
	require.NoError(t, os.WriteFile(filepath.Join(root, "b.go"),
		[]byte("package main\n\nfunc extra() {}\n"), 0o644))

	_, stderr, err = runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.Contains(t, stderr, "workspace changed since the last index")
}

func TestCheckSelfRepairsStaleIndex(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	// No index exists at all: check builds one instead of failing.
	_, stderr, err := runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, stderr, "index refreshed")

	// Fresh index + unchanged workspace: no repair on the second run.
	_, stderr, err = runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.NotContains(t, stderr, "index refreshed",
		"an up-to-date index must not be rebuilt")

	// A workspace change repairs on the next check, then settles.
	require.NoError(t, os.WriteFile(filepath.Join(root, "c.go"),
		[]byte("package main\n\nfunc later() {}\n"), 0o644))

	_, stderr, err = runErr(t, "-C", root, "check")
	require.NoError(t, err)
	assert.Contains(t, stderr, "index refreshed")
}

func TestWhyWithoutIndexFails(t *testing.T) {
	root := t.TempDir()
	_, err := run(t, "-C", root, "why", "anything")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "seamark index", "error should point at the fix")
}

func TestWhyUnknownSymbolFails(t *testing.T) {
	root := writeFixture(t)
	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	_, err = run(t, "-C", root, "why", "definitely_not_there_xyz")
	assert.Error(t, err)
}

// runIn is runErr with a stdin payload, for hook-mode tests.
func runIn(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()

	return out.String(), errOut.String(), err
}

func TestGateHookMode(t *testing.T) {
	root := writeFixture(t)

	// A PreToolUse payload is parsed natively — no jq in the loop.
	payload := `{"tool_name":"Bash","tool_input":{"command":"git push --force origin main"}}`

	_, _, err := runIn(t, payload, "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "force-push to main must block")

	out, _, err := runIn(t, `{"tool_input":{"command":"ls -la"}}`,
		"-C", root, "gate", "--enforce", "--hook")
	require.NoError(t, err)
	assert.Contains(t, out, "allow")
}

func TestGateHookModeFailsClosed(t *testing.T) {
	root := writeFixture(t)

	// Under enforcement the gate's OWN failures must block: a malformed
	// payload or a missing command means it cannot vouch for anything.
	_, _, err := runIn(t, "{not json", "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "malformed payload must fail closed")

	_, _, err = runIn(t, `{"tool_input":{}}`, "-C", root, "gate", "--enforce", "--hook")
	assert.ErrorIs(t, err, gate.ErrBlocked, "empty command must fail closed")

	// Without enforcement the same failures surface as plain errors.
	_, _, err = runIn(t, "{not json", "-C", root, "gate", "--hook")
	require.Error(t, err)
	assert.NotErrorIs(t, err, gate.ErrBlocked)
}
