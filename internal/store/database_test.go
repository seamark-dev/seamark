package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
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

// TestProbeRootReadsEverySchemaAndChangesNothing covers the databases
// that the version probe stops at. Their root still names their owner.
func TestProbeRootReadsEverySchemaAndChangesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	st, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(MetaRepoRoot, "/work/origin"))
	require.NoError(t, st.Close())

	for _, version := range []int{SupportedSchema(), SupportedSchema() - 1, SupportedSchema() + 1} {
		raw, err := sql.Open("sqlite", dsn(path, "_pragma=busy_timeout(5000)"))
		require.NoError(t, err)

		_, err = raw.Exec(`UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(version), MetaSchemaVersion)
		require.NoError(t, err)
		require.NoError(t, raw.Close())

		root, err := ProbeRoot(path)
		require.NoError(t, err)
		assert.Equal(t, "/work/origin", root)

		stamped, err := ProbeVersion(path)
		require.NoError(t, err)
		assert.Equal(t, version, stamped, "the probe migrates nothing")
	}
}

func TestProbeRootWithoutARoot(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.db")
	require.NoError(t, os.WriteFile(empty, nil, 0o644))

	root, err := ProbeRoot(empty)
	require.NoError(t, err, "a new empty file holds no root")
	assert.Empty(t, root)

	path := filepath.Join(t.TempDir(), "index.db")
	st, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	root, err = ProbeRoot(path)
	require.NoError(t, err, "no index run recorded a root")
	assert.Empty(t, root)
}

func TestProbeRootFailsOnAFileThatIsNoDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	require.NoError(t, os.WriteFile(path, []byte("this file is no database, and it is long enough to read"), 0o644))

	_, err := ProbeRoot(path)
	assert.Error(t, err)
}
