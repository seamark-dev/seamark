package store

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// databaseRoot returns a resolved directory with a default database file.
// The file needs no content, because the classification reads no data.
func databaseRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(root, DefaultDir), 0o755))
	require.NoError(t, os.WriteFile(DefaultPath(root), []byte("x"), 0o644))

	return root
}

// TestClassifyDatabaseComparesFiles covers each spelling that names the
// default database with another string.
func TestClassifyDatabaseComparesFiles(t *testing.T) {
	root := databaseRoot(t)

	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(root, link))

	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, DefaultPath(root)))
	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, DefaultPath(link)), "a link to the root")
	assert.Equal(t, DatabaseDefault, ClassifyDatabase(link, DefaultPath(root)), "a linked root")

	// workspace.Resolve makes a console --db absolute, and the working
	// directory then supplies the link.
	t.Chdir(link)

	relative := filepath.Join(DefaultDir, DefaultDBName)

	absolute, err := filepath.Abs(relative)
	require.NoError(t, err)
	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, absolute), "an absolute form of a relative path")

	assert.Equal(t, DatabaseLinked, ClassifyDatabase(root, relative),
		"a relative path shows no parent, and `seamark index --db` keeps it as given")
}

// TestClassifyDatabaseKeepsTheFilesOfOtherRoots covers the files that
// are not the default database of root.
func TestClassifyDatabaseKeepsTheFilesOfOtherRoots(t *testing.T) {
	root, other := databaseRoot(t), databaseRoot(t)

	assert.Equal(t, DatabaseOther, ClassifyDatabase(root, DefaultPath(other)))

	t.Run("the default path links outside the root", func(t *testing.T) {
		require.NoError(t, os.Remove(DefaultPath(root)))
		require.NoError(t, os.Symlink(DefaultPath(other), DefaultPath(root)))

		assert.Equal(t, DatabaseLinked, ClassifyDatabase(root, DefaultPath(root)))
		assert.Equal(t, DatabaseLinked, ClassifyDatabase(root, DefaultPath(other)),
			"the default path names the same file")
		assert.Equal(t, DatabaseDefault, ClassifyDatabase(other, DefaultPath(other)),
			"the file stays the default database of its own root")
	})

	t.Run("a hard link outside the root", func(t *testing.T) {
		own := databaseRoot(t)
		twin := filepath.Join(t.TempDir(), "twin.db")
		require.NoError(t, os.Link(DefaultPath(own), twin))

		assert.Equal(t, DatabaseLinked, ClassifyDatabase(own, twin))
	})
}

// TestClassifyDatabaseWithoutAFileReadsThePaths covers a database that
// a first build creates. A missing file has no identity.
func TestClassifyDatabaseWithoutAFileReadsThePaths(t *testing.T) {
	root := t.TempDir()

	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, DefaultPath(root)))
	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, DefaultPath(root)+string(filepath.Separator)),
		"the clean path decides")
	assert.Equal(t, DatabaseOther, ClassifyDatabase(root, filepath.Join(t.TempDir(), "new.db")))

	filled := databaseRoot(t)
	assert.Equal(t, DatabaseOther, ClassifyDatabase(filled, filepath.Join(t.TempDir(), "new.db")),
		"a missing --db file is not the existing default database")
}
