package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// run executes the CLI with args against a fresh command tree, returning
// stdout.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd := New()
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	if errOut.Len() > 0 {
		t.Logf("stderr: %s", errOut.String())
	}
	return out.String(), err
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
