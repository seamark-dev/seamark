package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// The fast path of Run compares a workspace fingerprint, and the
// fingerprint holds no path. These tests hold the rule for the root. The
// default database of a checkout must record that checkout. A --db
// database keeps its root unless the caller allows the move.

// tempDir returns a new directory by its resolved path, because Run
// records the resolved root.
func tempDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

// indexedRepo returns a clean git repository with a built default index.
func indexedRepo(t *testing.T) string {
	t.Helper()

	root := filepath.Join(tempDir(t), "origin")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".seamark/\n"), 0o644))

	gitIn(t, root, "init", "-b", "main")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-m", "initial")

	sum, err := Run(Options{Root: root})
	require.NoError(t, err)
	require.False(t, sum.Skipped)

	return root
}

// copyOf returns a full copy of root, with its index and its git data.
func copyOf(t *testing.T, root string) string {
	t.Helper()

	dst := filepath.Join(tempDir(t), "copy")
	require.NoError(t, os.CopyFS(dst, os.DirFS(root)))

	return dst
}

// recordedRoot reads the root that the database at path records.
func recordedRoot(t *testing.T, path string) string {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	root, err := st.GetMeta(store.MetaRepoRoot)
	require.NoError(t, err)

	return root
}

// setRecordedRoot writes the root that the database at path records.
func setRecordedRoot(t *testing.T, path, root string) {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	defer func() { require.NoError(t, st.Close()) }()

	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))
}

func TestRunRebuildsADefaultDatabaseOfAnotherRoot(t *testing.T) {
	t.Run("a copied checkout", func(t *testing.T) {
		origin := indexedRepo(t)
		dst := copyOf(t, origin)

		require.Equal(t, WorkspaceState(origin), WorkspaceState(dst), "the fingerprint cannot tell the two apart")
		require.Equal(t, origin, recordedRoot(t, store.DefaultPath(dst)))

		built, err := Run(Options{Root: dst})
		require.NoError(t, err)
		assert.False(t, built.Skipped, "the copy must record itself")
		assert.Equal(t, dst, recordedRoot(t, store.DefaultPath(dst)))

		again, err := Run(Options{Root: dst})
		require.NoError(t, err)
		assert.True(t, again.Skipped, "the rebuild ends the difference")

		assert.Equal(t, origin, recordedRoot(t, store.DefaultPath(origin)), "the origin keeps its own database")
	})

	t.Run("a moved checkout", func(t *testing.T) {
		origin := indexedRepo(t)
		moved := filepath.Join(tempDir(t), "moved")
		require.NoError(t, os.Rename(origin, moved))

		built, err := Run(Options{Root: moved})
		require.NoError(t, err)
		assert.False(t, built.Skipped)
		assert.Equal(t, moved, recordedRoot(t, store.DefaultPath(moved)))
	})

	t.Run("a link spelling of the default database", func(t *testing.T) {
		dst := copyOf(t, indexedRepo(t))

		link := filepath.Join(tempDir(t), "link")
		require.NoError(t, os.Symlink(dst, link))

		built, err := Run(Options{Root: dst, DBPath: store.DefaultPath(link)})
		require.NoError(t, err)
		assert.False(t, built.Skipped)
		assert.Equal(t, dst, recordedRoot(t, store.DefaultPath(dst)))
	})
}

func TestRunKeepsTheFastPathOfItsOwnRoot(t *testing.T) {
	t.Run("a rerun in one root", func(t *testing.T) {
		root := indexedRepo(t)

		sum, err := Run(Options{Root: root})
		require.NoError(t, err)
		assert.True(t, sum.Skipped)
		assert.Equal(t, root, sum.Root)
	})

	t.Run("a link spelling of the root", func(t *testing.T) {
		root := indexedRepo(t)

		link := filepath.Join(tempDir(t), "link")
		require.NoError(t, os.Symlink(root, link))
		setRecordedRoot(t, store.DefaultPath(root), link)

		sum, err := Run(Options{Root: root})
		require.NoError(t, err)
		assert.True(t, sum.Skipped, "one directory is no other root")
		assert.Equal(t, link, recordedRoot(t, store.DefaultPath(root)), "the hooks hash this spelling")
	})

	t.Run("no recorded root", func(t *testing.T) {
		root := indexedRepo(t)
		setRecordedRoot(t, store.DefaultPath(root), "")

		sum, err := Run(Options{Root: root})
		require.NoError(t, err)
		assert.True(t, sum.Skipped, "an empty value names no other root")
	})
}

// TestRunUnderAnotherDatabaseKeepsItsRules holds the --db rules. Another
// recorded root is normal there. A rebuild would move the root away from
// the repository that owns the database, so only MoveRoot does it.
func TestRunUnderAnotherDatabaseKeepsItsRules(t *testing.T) {
	// sibling returns a worktree of root at the same commit.
	sibling := func(t *testing.T, root string) string {
		t.Helper()

		dir := filepath.Join(tempDir(t), "sibling")
		gitIn(t, root, "worktree", "add", dir, "-b", "side")

		return dir
	}

	t.Run("one fingerprint skips and keeps the root", func(t *testing.T) {
		root := indexedRepo(t)
		side := sibling(t, root)
		require.Equal(t, WorkspaceState(root), WorkspaceState(side))

		sum, err := Run(Options{Root: side, DBPath: store.DefaultPath(root)})
		require.NoError(t, err)
		assert.True(t, sum.Skipped)
		assert.Equal(t, root, sum.Root)
		assert.Equal(t, root, recordedRoot(t, store.DefaultPath(root)))
	})

	t.Run("another fingerprint refuses and keeps the root", func(t *testing.T) {
		root := indexedRepo(t)
		side := sibling(t, root)

		require.NoError(t, os.WriteFile(filepath.Join(side, "side.go"), []byte("package main\n"), 0o644))
		gitIn(t, side, "add", "-A")
		gitIn(t, side, "commit", "-m", "side")

		_, err := Run(Options{Root: side, DBPath: store.DefaultPath(root)})
		require.ErrorIs(t, err, ErrRootMove)
		assert.Equal(t, root, recordedRoot(t, store.DefaultPath(root)))
	})

	t.Run("force does not pass the guard", func(t *testing.T) {
		root := indexedRepo(t)
		side := sibling(t, root)

		_, err := Run(Options{Root: side, DBPath: store.DefaultPath(root), Force: true})
		require.ErrorIs(t, err, ErrRootMove)
		assert.Equal(t, root, recordedRoot(t, store.DefaultPath(root)))
	})

	t.Run("MoveRoot rebuilds and records the workspace", func(t *testing.T) {
		root := indexedRepo(t)
		side := sibling(t, root)

		sum, err := Run(Options{Root: side, DBPath: store.DefaultPath(root), MoveRoot: true})
		require.NoError(t, err)
		assert.False(t, sum.Skipped, "one fingerprint must not keep the old root")
		assert.Equal(t, side, recordedRoot(t, store.DefaultPath(root)))
	})
}

// TestRunKeepsALinkedDatabaseOfAnotherWorktree covers a default path
// that links to the database of another worktree. That database records
// the other worktree, so a rebuild here moves its root on every run.
func TestRunKeepsALinkedDatabaseOfAnotherWorktree(t *testing.T) {
	origin := indexedRepo(t)
	side := copyOf(t, origin)

	require.NoError(t, os.Remove(store.DefaultPath(side)))
	require.NoError(t, os.Symlink(store.DefaultPath(origin), store.DefaultPath(side)))

	sum, err := Run(Options{Root: side})
	require.NoError(t, err)

	assert.True(t, sum.Skipped, "the fingerprint matches, so the fast path holds")
	assert.Equal(t, origin, recordedRoot(t, store.DefaultPath(origin)))
}

// TestRunHealsARenameThatChangesOnlyTheCase covers a case-insensitive
// volume. The directory stays one directory, but the path readers compare
// the recorded text, so an old spelling loses every absolute path lookup.
func TestRunHealsARenameThatChangesOnlyTheCase(t *testing.T) {
	root := indexedRepo(t)
	parent := filepath.Dir(root)
	renamed := filepath.Join(parent, strings.ToUpper(filepath.Base(root)))

	if _, err := os.Stat(renamed); err != nil {
		t.Skip("the volume is case-sensitive")
	}

	moved := filepath.Join(parent, "moved")
	require.NoError(t, os.Rename(root, moved))
	require.NoError(t, os.Rename(moved, renamed))

	sum, err := Run(Options{Root: renamed})
	require.NoError(t, err)

	assert.False(t, sum.Skipped, "the recorded spelling is wrong, so the fast path does not hold")
	assert.Equal(t, renamed, recordedRoot(t, store.DefaultPath(renamed)))
}
