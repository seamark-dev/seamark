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

// TestRootOwnsDatabaseFollowsTheDefaultPathOfTheRoot covers the rule of
// the write guard: a recorded root proves nothing about a file unless its
// default path names that file or it has no default database.
func TestRootOwnsDatabaseFollowsTheDefaultPathOfTheRoot(t *testing.T) {
	root := databaseRoot(t)
	other := filepath.Join(t.TempDir(), "other.db")
	require.NoError(t, os.WriteFile(other, []byte("x"), 0o644))

	assert.True(t, RootOwnsDatabase(root, DefaultPath(root)), "the default database")
	assert.False(t, RootOwnsDatabase(root, other), "another file beside a default database")

	t.Run("the default path links to the file", func(t *testing.T) {
		require.NoError(t, os.Remove(DefaultPath(root)))
		require.NoError(t, os.Symlink(other, DefaultPath(root)))

		assert.True(t, RootOwnsDatabase(root, other))
	})

	t.Run("no default database", func(t *testing.T) {
		assert.True(t, RootOwnsDatabase(t.TempDir(), other), "a custom database of a repository")
	})

	t.Run("a root that no longer exists owns nothing", func(t *testing.T) {
		assert.False(t, RootOwnsDatabase(filepath.Join(t.TempDir(), "gone"), other))
	})
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

func TestLiesInOtherCheckoutReadsTheStateDirectory(t *testing.T) {
	root, other, recorded := databaseRoot(t), databaseRoot(t), databaseRoot(t)

	got, ok := OtherOwner(root, recorded, DefaultPath(other))
	assert.True(t, ok, "the default database of a copy")
	assert.Equal(t, other, got)

	renamed := filepath.Join(other, DefaultDir, "keep.db")
	require.NoError(t, os.WriteFile(renamed, []byte("x"), 0o644))

	_, ok = OtherOwner(root, recorded, renamed)
	assert.True(t, ok, "a renamed database of a copy")

	own := filepath.Join(root, DefaultDir, "custom.db")
	require.NoError(t, os.WriteFile(own, []byte("x"), 0o644))

	_, ok = OtherOwner(root, recorded, own)
	assert.False(t, ok, "a file of this root")

	plain := filepath.Join(t.TempDir(), "custom.db")
	require.NoError(t, os.WriteFile(plain, []byte("x"), 0o644))

	_, ok = OtherOwner(root, recorded, plain)
	assert.False(t, ok, "a file outside any checkout")

	_, ok = OtherOwner(root, recorded, filepath.Join(t.TempDir(), "missing.db"))
	assert.False(t, ok)
}

// statRootHidden returns a path that a stat cannot reach, because its
// parent gives no permission. The test skips as root, because root
// ignores the file mode.
func statRootHidden(t *testing.T) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root reads a directory with mode 0000")
	}

	parent := t.TempDir()
	root := filepath.Join(parent, "root")

	require.NoError(t, os.Mkdir(root, 0o755))
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	return root
}

// TestStatRootReadsEachState covers each answer. ENOTDIR means that
// nothing exists at the path. Another failure leaves the root unknown.
func TestStatRootReadsEachState(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")

	require.NoError(t, os.WriteFile(file, nil, 0o644))

	assert.Equal(t, RootFound, StatRoot(dir))
	assert.Equal(t, RootGone, StatRoot(filepath.Join(dir, "missing")))
	assert.Equal(t, RootGone, StatRoot(filepath.Join(file, "below")))
	assert.Equal(t, RootHidden, StatRoot(statRootHidden(t)))
}

// TestRootOwnsDatabaseReadsARootBelowAFileAsGone keeps the two rules
// together: a root that the build rule calls gone owns nothing.
func TestRootOwnsDatabaseReadsARootBelowAFileAsGone(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")

	require.NoError(t, os.WriteFile(file, nil, 0o644))

	assert.False(t, RootOwnsDatabase(filepath.Join(file, "old"), filepath.Join(t.TempDir(), "custom.db")))
}

// TestRootMovedByBuild pins the answer of the rule for each state. The
// console and the CLI read this one answer, so each state holds for both.
func TestRootMovedByBuild(t *testing.T) {
	// records returns a database that records root, in its own directory.
	records := func(t *testing.T, root string) string {
		t.Helper()

		path := filepath.Join(t.TempDir(), "custom.db")

		st, err := Open(path)
		require.NoError(t, err)
		require.NoError(t, st.SetMeta(MetaRepoRoot, root))
		require.NoError(t, st.Close())

		return path
	}

	cases := []struct {
		name string
		// setup returns the workspace, the database, and the root that the
		// build would move, or "".
		setup func(t *testing.T) (root, db, moved string)
	}{
		{"a database of another repository", func(t *testing.T) (string, string, string) {
			other := t.TempDir()

			return t.TempDir(), records(t, other), other
		}},
		{"a database that records this workspace", func(t *testing.T) (string, string, string) {
			root := t.TempDir()

			return root, records(t, root), ""
		}},
		{"a link to this workspace as the recorded root", func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			link := filepath.Join(t.TempDir(), "link")

			require.NoError(t, os.Symlink(root, link))

			return root, records(t, link), ""
		}},
		{"a recorded root that no longer exists", func(t *testing.T) (string, string, string) {
			gone := filepath.Join(t.TempDir(), "gone")

			return t.TempDir(), records(t, gone), gone
		}},
		{"a recorded root that no longer exists with a file inside root", func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			path := filepath.Join(root, "custom.db")

			require.NoError(t, os.Rename(records(t, filepath.Join(t.TempDir(), "gone")), path))

			return root, path, ""
		}},
		{"a recorded root below a file", func(t *testing.T) (string, string, string) {
			file := filepath.Join(t.TempDir(), "file")

			require.NoError(t, os.WriteFile(file, nil, 0o644))

			gone := filepath.Join(file, "old")

			return t.TempDir(), records(t, gone), gone
		}},
		{"a recorded root that a stat cannot reach", func(t *testing.T) (string, string, string) {
			hidden := statRootHidden(t)

			return t.TempDir(), records(t, hidden), hidden
		}},
		{"an empty recorded root", func(t *testing.T) (string, string, string) {
			return t.TempDir(), records(t, ""), ""
		}},
		{"a file that does not exist", func(t *testing.T) (string, string, string) {
			return t.TempDir(), filepath.Join(t.TempDir(), "new.db"), ""
		}},
		{"an empty file", func(t *testing.T) (string, string, string) {
			path := filepath.Join(t.TempDir(), "empty.db")

			require.NoError(t, os.WriteFile(path, nil, 0o644))

			return t.TempDir(), path, ""
		}},
		{"the default database of a copied checkout", func(t *testing.T) (string, string, string) {
			root := t.TempDir()
			path := DefaultPath(root)

			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.Rename(records(t, t.TempDir()), path))

			return root, path, ""
		}},
		{"a default path that links to another repository", func(t *testing.T) (string, string, string) {
			root, other := t.TempDir(), t.TempDir()
			path := DefaultPath(root)

			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.Symlink(records(t, other), path))

			return root, path, other
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, db, want := tc.setup(t)

			got, err := RootMovedByBuild(root, db)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// TestRootMovedByBuildReturnsTheErrorOfARead covers a file with content
// that gives no root. The caller refuses, so the rule must not read it as
// a file without a root.
func TestRootMovedByBuildReturnsTheErrorOfARead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")

	require.NoError(t, os.WriteFile(path, []byte("this file is no database, and it is long enough to read"), 0o644))

	_, err := RootMovedByBuild(t.TempDir(), path)
	require.Error(t, err)
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

// TestCheckoutOfNamesTheNearestMark covers each answer. A nearer mark
// wins, so a nested repository owns its files. A link does not move a
// file, and .git is a file in a linked worktree.
func TestCheckoutOfNamesTheNearestMark(t *testing.T) {
	outer := checkoutDir(t, DefaultDir)
	nested := filepath.Join(outer, "a", "nested")
	deep := filepath.Join(nested, "b")
	plain := checkoutDir(t, "")

	require.NoError(t, os.MkdirAll(deep, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, ".git"), []byte("gitdir: x\n"), 0o644))

	file := filepath.Join(deep, "custom.db")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	link := filepath.Join(outer, "link.db")
	require.NoError(t, os.Symlink(file, link))

	got, ok := CheckoutOf(file)
	assert.True(t, ok)
	assert.Equal(t, nested, got)

	got, ok = CheckoutOf(link)
	assert.True(t, ok)
	assert.Equal(t, nested, got, "a link does not move the file")

	got, ok = CheckoutOf(filepath.Join(outer, "a", "x.db"))
	assert.True(t, ok)
	assert.Equal(t, outer, got)

	got, ok = CheckoutOf(filepath.Join(outer, DefaultDir, "sub", "x.db"))
	assert.True(t, ok)
	assert.Equal(t, outer, got, "the state directory belongs to its checkout")

	_, ok = CheckoutOf(filepath.Join(plain, "custom.db"))
	assert.False(t, ok)
}

// TestFileFitsRootReadsTheNearestMark covers the file that a workspace
// can take. A repository nested in root owns its file, also without a
// state directory, so a move to root would split the two.
func TestFileFitsRootReadsTheNearestMark(t *testing.T) {
	root := checkoutDir(t, ".git")
	nested := filepath.Join(root, "nested")

	require.NoError(t, os.MkdirAll(filepath.Join(nested, ".git"), 0o755))

	file := func(dir string) string {
		path := filepath.Join(dir, "custom.db")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		return path
	}

	assert.True(t, FileFitsRoot(root, file(root)))
	assert.True(t, FileFitsRoot(root, file(checkoutDir(t, ""))), "a file that shows no owner")
	assert.False(t, FileFitsRoot(root, file(nested)), "a repository nested in root")
	assert.False(t, FileFitsRoot(root, file(checkoutDir(t, DefaultDir))), "a third checkout")
	assert.False(t, FileFitsRoot(root, file(checkoutDir(t, ".git"))), "a git work tree without a state directory")
}

// TestOtherOwnerNeedsAStateDirectory covers the third checkout. A git work
// tree without a state directory, such as a home directory, owns no
// database. A file in the recorded root stays with that root.
func TestOtherOwnerNeedsAStateDirectory(t *testing.T) {
	root, recorded := checkoutDir(t, DefaultDir), checkoutDir(t, DefaultDir)
	copied := checkoutDir(t, DefaultDir)
	home := checkoutDir(t, ".git")
	submodule := filepath.Join(recorded, "sub")

	require.NoError(t, os.MkdirAll(filepath.Join(submodule, DefaultDir), 0o755))

	file := func(dir string) string {
		path := filepath.Join(dir, "custom.db")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		return path
	}

	got, ok := OtherOwner(root, recorded, file(copied))
	assert.True(t, ok)
	assert.Equal(t, copied, got)

	_, ok = OtherOwner(root, recorded, file(home))
	assert.False(t, ok, "a git work tree without a state directory")

	_, ok = OtherOwner(root, recorded, file(submodule))
	assert.False(t, ok, "a submodule of the recorded root")

	_, ok = OtherOwner(root, recorded, file(root))
	assert.False(t, ok, "a file of this workspace")

	_, ok = OtherOwner(root, recorded, file(recorded))
	assert.False(t, ok, "a file of the recorded root")

	_, ok = OtherOwner(root, recorded, file(checkoutDir(t, "")))
	assert.False(t, ok, "a file that shows no owner")
}

// TestBelongsToCheckoutReadsTheInnerDirectory covers each place of a file
// relative to the workspace and the recorded root.
func TestBelongsToCheckoutReadsTheInnerDirectory(t *testing.T) {
	// inner returns a new directory below dir with the mark, if any.
	inner := func(t *testing.T, dir, mark string) string {
		t.Helper()

		path := filepath.Join(dir, "inner")
		require.NoError(t, os.MkdirAll(filepath.Join(path, mark), 0o755))

		return path
	}

	// file creates a file in dir and returns its path.
	file := func(t *testing.T, dir string) string {
		t.Helper()

		require.NoError(t, os.MkdirAll(dir, 0o755))

		path := filepath.Join(dir, "custom.db")
		require.NoError(t, os.WriteFile(path, nil, 0o644))

		return path
	}

	cases := []struct {
		name  string
		setup func(t *testing.T) (root, recorded, db string)
		want  bool
	}{
		{"a file in a checkout with git", func(t *testing.T) (string, string, string) {
			root := checkoutDir(t, ".git")

			return root, checkoutDir(t, ""), file(t, root)
		}, true},
		{"a file in a checkout outside git", func(t *testing.T) (string, string, string) {
			root := checkoutDir(t, DefaultDir)

			return root, checkoutDir(t, ""), file(t, root)
		}, true},
		{"a file in a plain directory", func(t *testing.T) (string, string, string) {
			root := checkoutDir(t, "")

			return root, checkoutDir(t, ""), file(t, root)
		}, false},
		{"a file outside the workspace", func(t *testing.T) (string, string, string) {
			return checkoutDir(t, ".git"), checkoutDir(t, ""), file(t, checkoutDir(t, ""))
		}, false},
		{"a file of a recorded root inside the workspace", func(t *testing.T) (string, string, string) {
			root := checkoutDir(t, ".git")
			recorded := inner(t, root, "")

			return root, recorded, file(t, recorded)
		}, false},
		{"a file of a repository inside the recorded root", func(t *testing.T) (string, string, string) {
			recorded := checkoutDir(t, ".git")
			root := inner(t, recorded, ".git")

			return root, recorded, file(t, root)
		}, true},
		{"a file beside the state directory of a copy in the recorded root", func(t *testing.T) (string, string, string) {
			recorded := checkoutDir(t, DefaultDir)
			root := inner(t, recorded, DefaultDir)

			return root, recorded, file(t, root)
		}, true},
		{"a file below the state directory of a copy in the recorded root", func(t *testing.T) (string, string, string) {
			recorded := checkoutDir(t, DefaultDir)
			root := inner(t, recorded, DefaultDir)

			return root, recorded, file(t, filepath.Join(root, DefaultDir, "sub"))
		}, true},
		{"a file of a plain directory inside the recorded root", func(t *testing.T) (string, string, string) {
			recorded := checkoutDir(t, DefaultDir)
			root := inner(t, recorded, "")

			return root, recorded, file(t, root)
		}, false},
		{"a file of a repository nested in the workspace", func(t *testing.T) (string, string, string) {
			root := checkoutDir(t, ".git")

			return root, checkoutDir(t, ".git"), file(t, inner(t, root, ".git"))
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, recorded, db := tc.setup(t)

			assert.Equal(t, tc.want, BelongsToCheckout(root, recorded, db))
		})
	}
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

// TestCheckoutOfSkipsTheHomeDirectory covers a mark in the home directory.
// An earlier run there leaves a state directory, and dotfiles can make it
// a git work tree. Neither says that the home directory owns a file below
// it. A checkout below the home directory still counts.
func TestCheckoutOfSkipsTheHomeDirectory(t *testing.T) {
	for _, mark := range []string{DefaultDir, ".git"} {
		t.Run(mark, func(t *testing.T) {
			home := checkoutDir(t, mark)
			repo := filepath.Join(home, "work", "repo")

			t.Setenv("HOME", home)

			require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))

			_, ok := CheckoutOf(filepath.Join(home, "dbs", "shared.db"))
			assert.False(t, ok, "the home directory owns no file")

			got, ok := CheckoutOf(filepath.Join(repo, "custom.db"))
			assert.True(t, ok)
			assert.Equal(t, repo, got)

			assert.True(t, FileFitsRoot(repo, filepath.Join(home, "dbs", "shared.db")), "a file without an owner fits")
		})
	}
}

// TestNearestDirectoryTestsTheFilesystemRoot covers a workspace at the
// filesystem root, as in a container. The test needs no mark at the real
// root, because the walk takes the test as a value. LiesInside tests that
// directory too, so the two rules agree.
func TestNearestDirectoryTestsTheFilesystemRoot(t *testing.T) {
	seen := []string{}

	got, ok := nearestDirectory("/a/b", func(dir string) bool {
		seen = append(seen, dir)

		return dir == "/"
	})

	assert.True(t, ok)
	assert.Equal(t, "/", got)
	assert.Equal(t, []string{"/a/b", "/a", "/"}, seen)

	_, ok = nearestDirectory("/a", func(string) bool { return false })
	assert.False(t, ok, "the walk ends at the filesystem root")
}

// TestRootMovesItselfReadsTheNearestCheckout covers a file of a repository
// nested in the workspace. It lies inside the workspace but moved with
// the nested repository, so a build in the workspace must not replace
// its root.
func TestRootMovesItselfReadsTheNearestCheckout(t *testing.T) {
	root := checkoutDir(t, ".git")
	nested := filepath.Join(root, "lib")
	gone := filepath.Join(root, "gone")

	require.NoError(t, os.MkdirAll(filepath.Join(nested, DefaultDir), 0o755))

	inside := filepath.Join(root, "custom.db")
	beside := filepath.Join(nested, "custom.db")
	state := filepath.Join(nested, DefaultDir, "custom.db")

	for _, path := range []string{inside, beside, state} {
		require.NoError(t, os.WriteFile(path, nil, 0o644))
	}

	assert.True(t, RootMovesItself(root, gone, inside))
	assert.False(t, RootMovesItself(root, gone, beside), "a file of the nested repository")
	assert.False(t, RootMovesItself(root, gone, state), "a file in the state directory of the nested repository")
}
