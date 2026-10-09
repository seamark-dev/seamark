package index

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// goneFile is a committed file that the deletion tests remove from the
// working tree. Its function name is unique in the fixture, so a symbol
// search finds only this file.
const goneFile = "internal/gone/gone.go"

// gitIn runs one git command in root with a fixed identity and no user
// configuration, so the test does not depend on the machine setup.
func gitIn(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// deletedRepo commits the Go fixture and goneFile into a new repository.
// The caller removes goneFile, so the deletion stays unstaged.
func deletedRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeFixture(t, root)

	writeRepoFile(t, root, goneFile, "package gone\n\nfunc Vanished() {}\n")

	gitIn(t, root, "init", "-b", "main")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-m", "initial")

	return root
}

// writeRepoFile writes content to the repo-relative path rel in root.
func writeRepoFile(t *testing.T, root, rel, content string) {
	t.Helper()

	p := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
}

// logRecorder collects the Logf lines of one run.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return strings.Join(r.lines, "\n")
}

// assertGoneFromIndex checks that the database at dbPath holds no symbol
// and no parse-cache row of goneFile.
func assertGoneFromIndex(t *testing.T, dbPath string) {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	syms, err := st.FindSymbols("Vanished", 5)
	require.NoError(t, err)
	assert.Empty(t, syms, "a deleted path leaves no symbol")

	cache, err := st.LoadParseCache()
	require.NoError(t, err)
	assert.NotContains(t, cache, goneFile, "a deleted path leaves no parse-cache row")
	assert.Contains(t, cache, "main.go", "the files that exist stay cached")
}

func TestListFilesSeparatesUnstagedDeletions(t *testing.T) {
	root := deletedRepo(t)

	// An unstaged deletion, a staged deletion, and an unstaged deletion
	// under a skipped directory.
	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(goneFile))))
	gitIn(t, root, "rm", "-q", "README.md")
	require.NoError(t, os.Remove(filepath.Join(root, "vendor", "dep", "dep.go")))

	// A directory replaces a tracked file. git status shows the file as
	// deleted, so the index does too.
	replaced := "internal/util/util.go"
	p := filepath.Join(root, filepath.FromSlash(replaced))
	require.NoError(t, os.Remove(p))
	require.NoError(t, os.Mkdir(p, 0o755))

	// An intent-to-add copy of goneFile, as git add -N leaves a renamed
	// file. The deletion must stay a deletion, not half of a rename.
	moved := "internal/moved/moved.go"
	writeRepoFile(t, root, moved, "package gone\n\nfunc Vanished() {}\n")
	gitIn(t, root, "add", "-N", moved)

	files, deleted, err := listFiles(root)
	require.NoError(t, err)

	assert.Equal(t, []string{goneFile, replaced}, deleted, "only the unstaged deletions outside skipped directories count")
	assert.NotContains(t, files, goneFile, "a deleted path is not a file to parse")
	assert.NotContains(t, files, replaced, "a file that a directory replaces is not a file to parse")
	assert.NotContains(t, files, "README.md", "a staged deletion leaves the cached list")
	assert.Contains(t, files, moved, "an intent-to-add file is a file to parse")
	assert.Contains(t, files, "main.go")
}

func TestListFilesLeavesOutMissingSkipWorktreePaths(t *testing.T) {
	root := deletedRepo(t)

	// A sparse checkout sets the skip-worktree bit and removes the file.
	// A user can also set the bit on a file that stays, to keep local
	// edits out of git status.
	gitIn(t, root, "update-index", "--skip-worktree", goneFile, "main.go")
	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(goneFile))))

	files, deleted, err := listFiles(root)
	require.NoError(t, err)

	assert.Empty(t, deleted, "git status does not show a skip-worktree path as deleted")
	assert.NotContains(t, files, goneFile, "a skip-worktree path without a file is not a file to parse")
	assert.Contains(t, files, "main.go", "a skip-worktree file that exists stays a file to parse")
}

func TestListFilesWalkHasNoDeletions(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root)

	files, deleted, err := listFiles(root)
	require.NoError(t, err)

	assert.Nil(t, deleted, "a plain directory has no tracked paths")
	assert.Contains(t, files, "main.go")
}

func TestIndexCountsUnstagedDeletionApartFromParseErrors(t *testing.T) {
	root := deletedRepo(t)

	// The first build indexes the file, so its symbols and its
	// parse-cache row exist before the deletion.
	first, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.Zero(t, first.FilesDeleted)

	before, err := store.Open(first.DBPath)
	require.NoError(t, err)

	syms, err := before.FindSymbols("Vanished", 5)
	require.NoError(t, err)
	require.NotEmpty(t, syms, "the first build stores the symbols of the file")

	cache, err := before.LoadParseCache()
	require.NoError(t, err)
	require.Contains(t, cache, goneFile, "the first build caches the file")
	require.NoError(t, before.Close())

	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(goneFile))))

	var logs logRecorder

	second, err := Run(Options{Root: root, Logf: logs.logf})
	require.NoError(t, err)

	assert.False(t, second.Skipped, "the deletion changes the workspace fingerprint")
	assert.Zero(t, second.ParseErrors, "a deleted path is not a parse error")
	assert.Equal(t, 1, second.FilesDeleted)
	assert.Equal(t, first.FilesSeen-1, second.FilesSeen, "a deleted path is not a seen file")
	assert.NotContains(t, logs.text(), "warn: read", "a deleted path gets no read warning")

	assertGoneFromIndex(t, second.DBPath)

	// The index summary stores the count for seamark status.
	st, err := store.Open(second.DBPath)
	require.NoError(t, err)

	raw, err := st.GetMeta(store.MetaIndexSummary)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	var coverage map[string]int
	require.NoError(t, json.Unmarshal([]byte(raw), &coverage))
	assert.Equal(t, 1, coverage["files_deleted"])
	assert.Zero(t, coverage["parse_errors"])
	assert.Equal(t, second.FilesSeen, coverage["files_seen"])
}

func TestIndexFirstBuildSkipsUnstagedDeletion(t *testing.T) {
	root := deletedRepo(t)
	require.NoError(t, os.Remove(filepath.Join(root, filepath.FromSlash(goneFile))))

	var logs logRecorder

	sum, err := Run(Options{Root: root, DBPath: filepath.Join(t.TempDir(), "fresh.db"), Logf: logs.logf})
	require.NoError(t, err)

	assert.Zero(t, sum.ParseErrors)
	assert.Equal(t, 1, sum.FilesDeleted)
	assert.NotContains(t, logs.text(), "warn: read")

	assertGoneFromIndex(t, sum.DBPath)
}

func TestIndexUnreadableFileStaysAParseError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file without read permission")
	}

	root := deletedRepo(t)

	// The file exists but cannot be read. That is not a deletion, so it
	// stays a visible parse error.
	p := filepath.Join(root, "main.go")
	require.NoError(t, os.Chmod(p, 0o000))
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	var logs logRecorder

	sum, err := Run(Options{Root: root, Logf: logs.logf})
	require.NoError(t, err)

	assert.Equal(t, 1, sum.ParseErrors, "a permission error is a parse error")
	assert.Zero(t, sum.FilesDeleted, "an unreadable file is not a deleted path")
	assert.Contains(t, logs.text(), "warn: read main.go")
}

func TestIndexUnsearchableDirectoryStaysAParseError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a directory without search permission")
	}

	root := deletedRepo(t)

	// Without search permission on its directory, lstat of goneFile
	// fails with a permission error. The file is not deleted, so it
	// stays a visible parse error.
	dir := filepath.Dir(filepath.Join(root, filepath.FromSlash(goneFile)))
	require.NoError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	var logs logRecorder

	sum, err := Run(Options{Root: root, Logf: logs.logf})
	require.NoError(t, err)

	assert.Equal(t, 1, sum.ParseErrors, "a permission error on the directory is a parse error")
	assert.Zero(t, sum.FilesDeleted, "a file that git cannot lstat is not a deleted path")
	assert.Contains(t, logs.text(), "warn: read "+goneFile)
}

func TestIndexLeavesOutPathsOutsideASparseCheckout(t *testing.T) {
	root := deletedRepo(t)

	// Cone mode keeps the top-level files and internal/util. It removes
	// goneFile from the working tree and sets its skip-worktree bit.
	gitIn(t, root, "sparse-checkout", "init", "--cone")
	gitIn(t, root, "sparse-checkout", "set", "internal/util")
	require.NoFileExists(t, filepath.Join(root, filepath.FromSlash(goneFile)))

	var logs logRecorder

	sum, err := Run(Options{Root: root, Logf: logs.logf})
	require.NoError(t, err)

	assert.Zero(t, sum.ParseErrors, "a path outside the sparse checkout is not a parse error")
	assert.Zero(t, sum.FilesDeleted, "git status does not show a path outside the sparse checkout as deleted")
	assert.NotContains(t, logs.text(), "warn: read")

	assertGoneFromIndex(t, sum.DBPath)
}
