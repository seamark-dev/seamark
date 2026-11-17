package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests of a -C that names no directory. gate, check and index must
// refuse it in one text before any read or write, and the file that -C
// names must keep its bytes.

// fileWorkspace returns a repository and a regular file in it, with the
// bytes of that file.
func fileWorkspace(t *testing.T) (root, file string, data []byte) {
	t.Helper()

	root, _ = gitWorkspace(t)
	file = filepath.Join(root, "go.mod")
	data = []byte("module example.com/fix\n")

	require.NoError(t, os.WriteFile(file, data, 0o644))

	return root, file, data
}

// assertUntouched asserts that a refused run kept the file and wrote no
// state folder in the repository.
func assertUntouched(t *testing.T, root, file string, data []byte) {
	t.Helper()

	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, data, got, "the file keeps its bytes")
	assert.NoDirExists(t, filepath.Join(root, ".seamark"), "a refused run creates no state folder")
}

func TestCheckAndIndexRefuseAFileWorkspace(t *testing.T) {
	root, file, data := fileWorkspace(t)
	want := "workspace " + file + ": not a directory"

	// check with /dev/null on stdin, with each flag that changes its path.
	for _, flags := range [][]string{nil, {"--enforce"}, {"--lessons"}} {
		stderr, err := runCheckOnDevNull(t, file, flags...)
		assertBrokenCheck(t, err)
		assert.EqualError(t, err, want, "check %v", flags)
		assert.Empty(t, stderr, "check %v", flags)
	}

	// check with a pipe on stdin.
	out, _, err := runIn(t, "", "-C", file, "check")
	assertBrokenCheck(t, err)
	assert.EqualError(t, err, want)
	assert.Empty(t, out)

	out, err = run(t, "-C", file, "index")
	require.Error(t, err)
	assert.ErrorIs(t, err, workspace.ErrNotDirectory)
	assert.EqualError(t, err, want)
	assert.Empty(t, out)

	assertUntouched(t, root, file, data)
}

func TestCheckAndIndexRefuseAMissingWorkspace(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	want := "workspace " + missing + ": no such file or directory"

	stderr, err := runCheckOnDevNull(t, missing)
	assertBrokenCheck(t, err)
	assert.EqualError(t, err, want)
	assert.Empty(t, stderr)

	_, err = run(t, "-C", missing, "index")
	require.Error(t, err)
	assert.EqualError(t, err, want)
	assert.NoDirExists(t, missing, "the refusal creates no directory")
}

func TestGateNamesAFileWorkspaceInTheTextOfCheck(t *testing.T) {
	root, file, data := fileWorkspace(t)

	_, _, err := runErr(t, "-C", file, "gate", "--enforce", "--command", "ls")
	require.ErrorIs(t, err, gate.ErrBlocked)
	assert.EqualError(t, err, "blocked by policy: workspace "+file+": not a directory")

	assertUntouched(t, root, file, data)
}

// TestIndexWithADatabaseBelowAFile covers --db below a regular file. No
// database can exist there, so the build runs and fails on the directory
// of the database. That is the text of the store, not a root refusal.
func TestIndexWithADatabaseBelowAFile(t *testing.T) {
	root, _ := gitWorkspace(t)
	file := filepath.Join(root, "main.go")
	data := []byte("package main\n")

	require.NoError(t, os.WriteFile(file, data, 0o644))

	db := filepath.Join(file, "x.db")
	want := "store: create index dir: mkdir " + file + ": not a directory"

	_, err := run(t, "-C", root, "--db", db, "index")
	assert.EqualError(t, err, want)

	// A piped diff, because the repository has no commit for git diff HEAD.
	_, _, err = runIn(t, "diff --git a/main.go b/main.go\n", "-C", root, "--db", db, "check")
	assertBrokenCheck(t, err)
	assert.EqualError(t, err, want)

	got, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, data, got, "the file keeps its bytes")
}
