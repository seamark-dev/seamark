package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// caseVariant returns another case of the last element of dir, or skips
// on a case-sensitive volume, which treats it as another name.
func caseVariant(t *testing.T, dir string) string {
	t.Helper()

	variant := filepath.Join(filepath.Dir(dir), strings.ToUpper(filepath.Base(dir)))

	if _, err := os.Stat(variant); err != nil {
		t.Skip("the volume is case-sensitive")
	}

	return variant
}

// TestClassifyDatabaseAcceptsACaseVariantOfTheRoot covers a case-insensitive
// volume. A path keeps the case that the user typed, so only file identity
// shows that the file lies inside the root.
func TestClassifyDatabaseAcceptsACaseVariantOfTheRoot(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	root := filepath.Join(parent, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(root, DefaultDir), 0o755))
	require.NoError(t, os.WriteFile(DefaultPath(root), []byte("x"), 0o644))

	variant := caseVariant(t, root)

	assert.Equal(t, DatabaseDefault, ClassifyDatabase(root, DefaultPath(variant)))
	assert.Equal(t, DatabaseDefault, ClassifyDatabase(variant, DefaultPath(root)))
	assert.True(t, SameDirectory(root, variant))
}

func TestSameDirectoryComparesIdentity(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))

	assert.True(t, SameDirectory(dir, dir+string(filepath.Separator)))
	assert.True(t, SameDirectory(link, dir), "a link spelling names the same directory")
	assert.False(t, SameDirectory(dir, t.TempDir()))

	gone := filepath.Join(dir, "gone")
	assert.False(t, SameDirectory(dir, gone))
	assert.True(t, SameDirectory(gone, gone+string(filepath.Separator)), "a clean path needs no file")
}

// TestLiesInsideComparesIdentity covers a directory inside a root, the
// spelling through a link, and the two paths that are not below it.
func TestLiesInsideComparesIdentity(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "pkg")
	link := filepath.Join(t.TempDir(), "link")

	require.NoError(t, os.MkdirAll(inside, 0o755))
	require.NoError(t, os.Symlink(root, link))

	assert.True(t, LiesInside(inside, root))
	assert.True(t, LiesInside(inside, link), "a link spelling of the root")
	assert.True(t, LiesInside(filepath.Join(link, "pkg"), root), "a link spelling of the path")
	assert.False(t, LiesInside(root, root), "a directory is not below itself")
	assert.False(t, LiesInside(t.TempDir(), root))
	assert.False(t, LiesInside(filepath.Join(root, "missing"), root), "a missing path has no identity")
}

// checkoutDir returns a new resolved directory that holds mark, a
// directory such as .git or .seamark. An empty mark leaves it plain.
func checkoutDir(t *testing.T, mark string) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	if mark != "" {
		require.NoError(t, os.Mkdir(filepath.Join(dir, mark), 0o755))
	}

	return dir
}

// TestAbsolutePathNamesTheFileThatTheOpenOpens covers a working directory
// behind a link. $PWD holds the link, so filepath.Abs names a file beside
// the link. The system starts at the physical directory.
func TestAbsolutePathNamesTheFileThatTheOpenOpens(t *testing.T) {
	base := checkoutDir(t, "")
	here := filepath.Join(base, "data", "here")
	link := filepath.Join(base, "links", "here")
	shared := filepath.Join(base, "data", "shared.db")

	require.NoError(t, os.MkdirAll(here, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(here, link))
	require.NoError(t, os.WriteFile(shared, nil, 0o644))

	t.Chdir(link)

	wd, err := os.Getwd()
	require.NoError(t, err)
	require.Equal(t, link, wd, "the test needs a logical working directory")

	assert.Equal(t, shared, AbsolutePath("../shared.db"), "a file that exists")
	assert.Equal(t, filepath.Join(base, "data", "new.db"), AbsolutePath("../new.db"), "a file that does not exist")
	assert.Equal(t, filepath.Join(here, "x.db"), AbsolutePath("x.db"), "a plain name")
	assert.Equal(t, shared, AbsolutePath(filepath.Join(link, "..")+"/../data/shared.db"), "a path that Clean would shorten")
	assert.Equal(t, shared, AbsolutePath(link+"/../shared.db"), "an absolute path through a link")
	assert.Equal(t, filepath.Join(base, "data", "new.db"), AbsolutePath(link+"/../new.db"),
		"a new file through a link lands where the system puts it")
	assert.Equal(t, shared, AbsolutePath(shared), "an absolute path stays")
	assert.Empty(t, AbsolutePath(""), "no --db stays no --db")
}
