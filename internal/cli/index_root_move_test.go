package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// These tests drive `seamark index` and the commands that reach an index
// run through the command tree, on real git repositories with a real
// index. A build records its workspace in the database. So a build must
// not take the recorded root of another existing repository, unless the
// user asks for that move.

// rootMoveThird builds a real repository whose commit differs from each
// other test repository. Its fingerprint differs too, so a run there
// cannot skip, and only the guard keeps the recorded root.
func rootMoveThird(t *testing.T) string {
	t.Helper()

	root := rootWriteDir(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, "third.go"), []byte("package third\n"), 0o644))
	gitify(t, root)

	return root
}

// rootMoveLead is the refusal of a build in here up to its route.
func rootMoveLead(recorded, here string) string {
	return "this database records another repository (" + recorded + "), so a rebuild in " + here +
		" would move its recorded root, and that repository would lose its pins and maintenance; nothing changed; "
}

// rootMoveRefused is the refusal of a build in here under a file of the
// recorded root. The move would split two repositories, so the only route
// is the recorded root.
func rootMoveRefused(recorded, here, db string) string {
	return rootMoveLead(recorded, here) + "run `seamark index --db " + db + "` in " + recorded
}

// rootMoveRefusedWithMove is rootMoveRefused for a file that no checkout
// owns, or that lies in this workspace. The move keeps one repository.
func rootMoveRefusedWithMove(recorded, here, db string) string {
	return rootMoveRefused(recorded, here, db) +
		", or run `seamark index --move-root --db " + db + "` in " + here + " to move the root"
}

// rootMoveFiles reads every state file of the roots, the databases
// included, as rootWriteFiles does.
func rootMoveFiles(t *testing.T, roots []string, extra ...string) func() map[string]string {
	t.Helper()

	return func() map[string]string { return rootWriteFiles(t, roots, extra...) }
}

// rootMoveRecorded returns the root that the database at path records.
func rootMoveRecorded(t *testing.T, path string) string {
	t.Helper()

	root, err := store.ProbeRoot(path)
	require.NoError(t, err)

	return root
}

// rootMoveVersion returns the schema version of the database at path.
func rootMoveVersion(t *testing.T, path string) int {
	t.Helper()

	version, err := store.ProbeVersion(path)
	require.NoError(t, err)

	return version
}

// TestIndexRefusesTheDatabaseOfAnotherRepository covers the run that
// moved the recorded root. The origin then read no pins and lost its
// maintenance. --force keeps its meaning, so it does not pass the guard.
// The test ends with a pin write in the origin, which shows that the
// origin kept them.
func TestIndexRefusesTheDatabaseOfAnotherRepository(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)
	files := rootMoveFiles(t, []string{origin, third})

	for name, flags := range map[string][]string{
		"a plain run":   nil,
		"--force":       {"--force"},
		"--fixes-only":  {"--fixes-only"},
		"--max-commits": {"--max-commits", "5"},
	} {
		t.Run(name, func(t *testing.T) {
			rootWriteRefused(t, rootMoveRefused(origin, third, db), files,
				append([]string{"-C", third, "--db", db, "index"}, flags...)...)
		})
	}

	t.Run("a --db through a link", func(t *testing.T) {
		link := filepath.Join(rootWriteDir(t), "link")

		require.NoError(t, os.Symlink(origin, link))

		spelled := store.DefaultPath(link)

		rootWriteRefused(t, rootMoveRefused(origin, third, spelled), files, "-C", third, "--db", spelled, "index")
	})

	t.Run("a relative --db reads as an absolute route", func(t *testing.T) {
		t.Chdir(filepath.Dir(db))

		rootWriteRefused(t, rootMoveRefused(origin, third, db), files, "-C", third, "--db", "index.db", "index")
	})

	t.Run("the route of the recorded root works", func(t *testing.T) {
		_, err := run(t, "-C", origin, "--db", db, "index", "--force")
		require.NoError(t, err)
		assert.Equal(t, origin, rootMoveRecorded(t, db))
	})

	t.Run("the origin keeps its maintenance", func(t *testing.T) {
		out, err := run(t, "-C", origin, "lessons", "--apply", "p1")
		require.NoError(t, err)
		assert.Contains(t, out, "applied 1 pin(s)")
	})
}

// TestIndexRefusesADatabaseInThisWorkspace covers the file of a copied
// checkout. The move needs the flag there too. The file is no part of the
// origin, so the refusal names only the move and no route to the origin.
func TestIndexRefusesADatabaseInThisWorkspace(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	custom := filepath.Join(copied, "custom.db")

	require.NoError(t, os.Rename(store.DefaultPath(copied), custom))

	want := "this database lies in " + copied + " but records another repository (" + origin + "), so a rebuild " +
		"here would move its recorded root; nothing changed; run `seamark index --move-root --db " + custom +
		"` in " + copied + " to move the root"

	rootWriteRefused(t, want, rootMoveFiles(t, []string{origin, copied}, custom),
		"-C", copied, "--db", custom, "index", "--force")
}

// TestIndexRefusesADirectoryInsideARootWithoutGit bounds the rule of a
// pin write, which counts such a directory as the root. A build records
// the directory itself, so it moves the root.
func TestIndexRefusesADirectoryInsideARootWithoutGit(t *testing.T) {
	root := rootWritePlainRoot(t)
	inside := filepath.Join(root, "pkg")
	custom := filepath.Join(rootWriteDir(t), "custom.db")

	require.NoError(t, os.MkdirAll(inside, 0o755))
	require.NoError(t, os.Rename(store.DefaultPath(root), custom))

	rootWriteRefused(t, rootMoveRefusedWithMove(root, inside, custom), rootMoveFiles(t, []string{root}, custom),
		"-C", inside, "--db", custom, "index")
}

// TestIndexMoveRootMovesTheRecordedRoot covers the flag. The user asks
// for the move, so the run records this workspace, also where a skip
// would keep the old root.
func TestIndexMoveRootMovesTheRecordedRoot(t *testing.T) {
	t.Run("from another repository", func(t *testing.T) {
		origin := rootWriteRepo(t)
		third := rootMoveThird(t)
		db := store.DefaultPath(origin)

		out, err := run(t, "-C", third, "--db", db, "index", "--move-root")
		require.NoError(t, err)
		assert.Contains(t, out, "indexed "+third)
		assert.Equal(t, third, rootMoveRecorded(t, db))
	})

	t.Run("from a sibling worktree at one commit", func(t *testing.T) {
		origin := rootWriteRepo(t)
		tree := rootWriteWorktree(t, origin)
		db := store.DefaultPath(origin)

		require.Equal(t, index.WorkspaceState(origin), index.WorkspaceState(tree), "a plain run would skip")

		_, err := run(t, "-C", tree, "--db", db, "index", "--move-root")
		require.NoError(t, err)
		assert.Equal(t, tree, rootMoveRecorded(t, db))
	})

	t.Run("from an older schema", func(t *testing.T) {
		origin := rootWriteRepo(t)
		third := rootMoveThird(t)
		db := store.DefaultPath(origin)

		writeIndexDB(t, db, schemaMeta(-1))

		_, err := run(t, "-C", third, "--db", db, "index", "--move-root")
		require.NoError(t, err)
		assertSchemaSupported(t, db)
		assert.Equal(t, third, rootMoveRecorded(t, db))
	})

	t.Run("without another root it is a plain run", func(t *testing.T) {
		root := rootWriteRepo(t)

		out, err := run(t, "-C", root, "index", "--move-root")
		require.NoError(t, err)
		assert.Contains(t, out, "indexed "+root)
		assert.Equal(t, root, rootMoveRecorded(t, store.DefaultPath(root)))
	})
}

// TestIndexRefusesAnOlderDatabaseBeforeTheOpen covers the schema
// migration of the open, which is a write too. The rule reads the root
// of every schema, so an older file refuses and stays older. A plain run
// from a sibling would skip with the current schema. It cannot prove that
// without a migration, so it refuses too.
func TestIndexRefusesAnOlderDatabaseBeforeTheOpen(t *testing.T) {
	origin := rootWriteRepo(t)
	tree := rootWriteWorktree(t, origin)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	writeIndexDB(t, db, schemaMeta(-1))

	want := rootMoveVersion(t, db)
	files := rootMoveFiles(t, []string{origin, third, tree})

	for name, here := range map[string]string{"another repository": third, "a sibling worktree": tree} {
		for _, flags := range [][]string{nil, {"--force"}} {
			t.Run(name+" "+strings.Join(flags, " "), func(t *testing.T) {
				rootWriteRefused(t, rootMoveRefused(origin, here, db), files,
					append([]string{"-C", here, "--db", db, "index"}, flags...)...)
				assert.Equal(t, want, rootMoveVersion(t, db), "the refusal does not migrate the schema")
			})
		}
	}
}

// TestIndexKeepsTheUpgradeMessageOfANewerDatabase covers a schema that
// this binary cannot read. No route of the guard can run on it. So the
// open answers: it refuses without a write and names the upgrade.
func TestIndexKeepsTheUpgradeMessageOfANewerDatabase(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	writeIndexDB(t, db, schemaMeta(+1))

	want := storeOpenErr(t, db)
	require.Contains(t, want, "upgrade seamark")

	files := rootMoveFiles(t, []string{origin, third})

	for _, flags := range [][]string{nil, {"--force"}, {"--move-root"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			rootWriteRefused(t, want, files, append([]string{"-C", third, "--db", db, "index"}, flags...)...)
		})
	}
}

// TestIndexFailsClosedOnARootThatDoesNotRead covers a file with content
// that gives no root. It can belong to another repository, so the run
// refuses. The flag allows a move of a known root, so it changes nothing.
func TestIndexFailsClosedOnARootThatDoesNotRead(t *testing.T) {
	third := rootMoveThird(t)
	path := filepath.Join(rootWriteDir(t), "garbage.db")

	writeGarbageDB(t, path)

	before, err := os.ReadFile(path)
	require.NoError(t, err)

	for _, flags := range [][]string{nil, {"--force"}, {"--move-root"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			_, err := run(t, append([]string{"-C", third, "--db", path, "index"}, flags...)...)
			require.ErrorContains(t, err, "the recorded root of this database cannot be read ("+path+": ")
			assert.ErrorContains(t, err, "so a rebuild could move the root of another repository; nothing changed; "+
				"run it again, fix the file, or use another --db")
			assert.ErrorIs(t, err, index.ErrRootMove)

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

// TestIndexFailsClosedBehindALock covers a read that a real lock stops.
// The open waits longer than the read, so without the refusal a build
// would pass the lock and move the root.
func TestIndexFailsClosedBehindALock(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)
	wait := rootWriteLock(t, db)

	_, err := run(t, "-C", third, "--db", db, "index")
	require.ErrorContains(t, err, "the recorded root of this database cannot be read ("+db+": ")

	wait()

	assert.Equal(t, origin, rootMoveRecorded(t, db))
}

// TestIndexSanitizesTheRecordedRoot covers a root that the database
// supplies. A copied file can hold any text, and an escape sequence in
// an error must not reach the terminal.
func TestIndexSanitizesTheRecordedRoot(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	require.NoError(t, os.Mkdir(origin+"\x1b[2J", 0o755))
	writeIndexDB(t, db, map[string]string{store.MetaRepoRoot: origin + "\x1b[2J"})

	_, err := run(t, "-C", third, "--db", db, "index")
	require.ErrorIs(t, err, index.ErrRootMove)
	assert.NotContains(t, err.Error(), "\x1b")
}

// TestIndexKeepsEveryDatabaseWithoutAnotherOwner holds the states that
// the guard must not reach. Each one runs without the flag. Each setup
// returns the flags of the run, the database, and the root it must record.
func TestIndexKeepsEveryDatabaseWithoutAnotherOwner(t *testing.T) {
	// own builds a database outside root that records root.
	own := func(t *testing.T, root string) string {
		t.Helper()

		path := filepath.Join(rootWriteDir(t), "own.db")

		_, err := run(t, "-C", root, "--db", path, "index")
		require.NoError(t, err)

		return path
	}

	cases := []struct {
		name  string
		setup func(t *testing.T) (flags []string, db, root string)
	}{
		{"the default database", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)

			return []string{"-C", root}, store.DefaultPath(root), root
		}},
		{"the default database of a copied checkout", func(t *testing.T) ([]string, string, string) {
			copied := rootWriteCopy(t, rootWriteRepo(t))

			return []string{"-C", copied}, store.DefaultPath(copied), copied
		}},
		{"the default database of a copy named with --db", func(t *testing.T) ([]string, string, string) {
			copied := rootWriteCopy(t, rootWriteRepo(t))

			return []string{"-C", copied, "--db", store.DefaultPath(copied)}, store.DefaultPath(copied), copied
		}},
		{"the default database of a copy through a link", func(t *testing.T) ([]string, string, string) {
			copied := rootWriteCopy(t, rootWriteRepo(t))
			link := filepath.Join(rootWriteDir(t), "link")

			require.NoError(t, os.Symlink(copied, link))

			return []string{"-C", copied, "--db", store.DefaultPath(link)}, store.DefaultPath(copied), copied
		}},
		{"the default database of a copy with a relative --db", func(t *testing.T) ([]string, string, string) {
			copied := rootWriteCopy(t, rootWriteRepo(t))

			t.Chdir(copied)

			return []string{"--db", filepath.Join(store.DefaultDir, store.DefaultDBName)}, store.DefaultPath(copied), copied
		}},
		{"a --db that does not exist", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := filepath.Join(rootWriteDir(t), "new.db")

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"an empty --db file", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := filepath.Join(rootWriteDir(t), "empty.db")

			require.NoError(t, os.WriteFile(path, nil, 0o644))

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"a --db with an empty recorded root", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := filepath.Join(rootWriteDir(t), "blank.db")

			writeIndexDB(t, path, map[string]string{store.MetaRepoRoot: ""})

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"a --db that records this workspace", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := own(t, root)

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"a --db that records this workspace through a link", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := own(t, root)
			link := filepath.Join(rootWriteDir(t), "link")

			require.NoError(t, os.Symlink(root, link))
			writeIndexDB(t, path, map[string]string{store.MetaRepoRoot: link})

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"a --db that records this workspace in another case", func(t *testing.T) ([]string, string, string) {
			root := rootMoveThird(t)
			path := own(t, root)
			variant := strings.ToUpper(root)

			if _, err := os.Stat(variant); err != nil {
				t.Skip("the volume is case-sensitive")
			}

			writeIndexDB(t, path, map[string]string{store.MetaRepoRoot: variant})

			return []string{"-C", root, "--db", path}, path, root
		}},
		{"a moved repository with a custom database inside", func(t *testing.T) ([]string, string, string) {
			origin := rootWriteRepo(t)
			moved := filepath.Join(rootWriteDir(t), "moved")
			custom := filepath.Join(moved, "custom.db")

			require.NoError(t, os.Rename(origin, moved))
			require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

			return []string{"-C", moved, "--db", custom}, custom, moved
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, db, root := tc.setup(t)

			_, err := run(t, append(flags, "index", "--force")...)
			require.NoError(t, err)
			assert.Equal(t, root, rootMoveRecorded(t, db))
		})
	}
}

// TestIndexKeepsAnOlderDatabaseOfThisWorkspace bounds the migration
// guard. A database of this workspace migrates and builds, as it did.
func TestIndexKeepsAnOlderDatabaseOfThisWorkspace(t *testing.T) {
	root := rootMoveThird(t)
	path := filepath.Join(rootWriteDir(t), "own.db")

	_, err := run(t, "-C", root, "--db", path, "index")
	require.NoError(t, err)

	writeIndexDB(t, path, schemaMeta(-1))

	_, err = run(t, "-C", root, "--db", path, "index")
	require.NoError(t, err)
	assertSchemaSupported(t, path)
}

// TestIndexInASiblingWorktree covers two worktrees at one commit. They
// share a fingerprint, so a plain run skips, writes nothing, and keeps
// the root. That must not start to refuse. A real rebuild is the move.
func TestIndexInASiblingWorktree(t *testing.T) {
	origin := rootWriteRepo(t)
	tree := rootWriteWorktree(t, origin)
	db := store.DefaultPath(origin)

	require.Equal(t, index.WorkspaceState(origin), index.WorkspaceState(tree))

	before := rootWriteFiles(t, []string{origin})

	out, err := run(t, "-C", tree, "--db", db, "index")
	require.NoError(t, err)
	assert.Equal(t, "index already up to date ("+db+"); it records "+origin+"; to rebuild, run `seamark index --db "+
		db+"` in "+origin+"\n", out, "--force refuses here, and a move would split the file from its repository")
	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "a skip writes nothing")

	files := rootMoveFiles(t, []string{origin})

	rootWriteRefused(t, rootMoveRefused(origin, tree, db), files, "-C", tree, "--db", db, "index", "--force")

	require.NoError(t, os.WriteFile(filepath.Join(tree, "side.go"), []byte("package main\n"), 0o644))

	rootWriteRefused(t, rootMoveRefused(origin, tree, db), files, "-C", tree, "--db", db, "index")
	assert.Equal(t, origin, rootMoveRecorded(t, db))
}

// TestIndexRefusesALinkedDefaultPath covers a worktree that shares the
// database of another worktree. No --db is set, but the file belongs to
// the other worktree. The console refuses this file too.
func TestIndexRefusesALinkedDefaultPath(t *testing.T) {
	origin := rootWriteRepo(t)
	tree := rootWriteWorktree(t, origin)
	db := store.DefaultPath(origin)

	require.NoError(t, os.MkdirAll(filepath.Dir(store.DefaultPath(tree)), 0o755))
	require.NoError(t, os.Symlink(db, store.DefaultPath(tree)))

	out, err := run(t, "-C", tree, "index")
	require.NoError(t, err)
	assert.Contains(t, out, "index already up to date", "one commit still skips")

	require.NoError(t, os.WriteFile(filepath.Join(tree, "side.go"), []byte("package main\n"), 0o644))

	rootWriteRefused(t, rootMoveRefused(origin, tree, store.DefaultPath(tree)), rootMoveFiles(t, []string{origin}),
		"-C", tree, "index")
}

// TestCheckRefusesToMoveTheRootOfAnotherRepository covers the refresh of
// `seamark check`. Its verdict needs a current graph, and a refresh that
// fails has always stopped the command with exit code 1. The refusal
// comes first, so the run appends no audit line. A blocked policy keeps
// exit code 2, so this exit code cannot read as a policy block.
func TestCheckRefusesToMoveTheRootOfAnotherRepository(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	before := rootWriteFiles(t, []string{origin, third})

	_, _, err := runIn(t, "", "-C", third, "--db", db, "check")
	require.EqualError(t, err, rootMoveRefused(origin, third, db))

	assert.Equal(t, before, rootWriteFiles(t, []string{origin, third}))
	assert.NoFileExists(t, filepath.Join(third, ".seamark", "audit.jsonl"))
}

// TestCheckKeepsASiblingWorktreeAtOneCommit bounds the rule above. The
// refresh skips, so the verdict comes from a current graph.
func TestCheckKeepsASiblingWorktreeAtOneCommit(t *testing.T) {
	origin := rootWriteRepo(t)
	tree := rootWriteWorktree(t, origin)
	db := store.DefaultPath(origin)

	_, _, err := runIn(t, "", "-C", tree, "--db", db, "check")
	require.NoError(t, err)
	assert.Equal(t, origin, rootMoveRecorded(t, db))
}

// TestHooksNeverMeetTheRootMoveGuard keeps every managed hook under the
// database of another repository. No hook starts an index run, so each
// keeps its exit code, and the recorded root stays.
func TestHooksNeverMeetTheRootMoveGuard(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	edit, err := json.Marshal(map[string]any{
		"cwd": third, "tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(third, "third.go")},
	})
	require.NoError(t, err)

	shell, err := json.Marshal(map[string]any{
		"cwd": third, "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"},
	})
	require.NoError(t, err)

	for _, tc := range []struct {
		stdin []byte
		args  []string
	}{
		{edit, []string{"lessons", "--hook"}},
		{edit, []string{"lessons", "--hook", "--guard"}},
		{edit, []string{"lessons", "--hook", "--baseline"}},
		{shell, []string{"gate", "--hook"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			_, _, err := runIn(t, string(tc.stdin), append([]string{"-C", third, "--db", db}, tc.args...)...)
			require.NoError(t, err)
		})
	}

	assert.Equal(t, origin, rootMoveRecorded(t, db))
	assert.Equal(t, model.ProposalProposed, rootWriteStatus(t, db, 1), "the ledger stays")
}

// TestRootWriteRouteOfAMovedRepositoryNamesARunThatWorks follows the
// route that a pin write names for a moved repository. The guard must let
// that index run pass, because the old root no longer exists.
func TestRootWriteRouteOfAMovedRepositoryNamesARunThatWorks(t *testing.T) {
	origin := rootWriteRepo(t)
	moved := filepath.Join(rootWriteDir(t), "moved")
	custom := filepath.Join(moved, "custom.db")

	require.NoError(t, os.Rename(origin, moved))
	require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

	_, err := run(t, "-C", moved, "--db", custom, "lessons", "--apply", "p1")
	require.ErrorContains(t, err, "run `seamark index --move-root --db "+custom+"` in "+moved+" first")

	_, err = run(t, "-C", moved, "--db", custom, "index", "--move-root")
	require.NoError(t, err)

	out, err := run(t, "-C", moved, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 1 pin(s)")
	assert.NoDirExists(t, origin)
}

// TestIndexRefusalLeavesTheWorkTree covers the workspace of a refused
// run. The run stops before it creates any state there.
func TestIndexRefusalLeavesTheWorkTree(t *testing.T) {
	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	db := store.DefaultPath(origin)

	_, err := run(t, "-C", third, "--db", db, "index")
	require.ErrorIs(t, err, index.ErrRootMove)

	out, err := exec.Command("git", "-C", third, "status", "--porcelain").CombinedOutput()
	require.NoError(t, err, string(out))
	assert.Empty(t, string(out))
	assert.NoDirExists(t, filepath.Join(third, ".seamark"))
}

// TestIndexRefusalNamesNoMoveThatSplits covers the route of the flag. After
// a move, a pin command here writes its pin file here and its ledger in
// the file. A file of another checkout then lies in a second repository,
// so the refusal must not recommend the flag. A file without an owner
// keeps the recommendation, and the flag still works for every file.
func TestIndexRefusalNamesNoMoveThatSplits(t *testing.T) {
	type setup func(t *testing.T, origin string) string

	cases := []struct {
		name     string
		db       setup
		wantMove bool
	}{
		{"the default database of the recorded root", func(_ *testing.T, origin string) string {
			return store.DefaultPath(origin)
		}, false},
		{"a file in the checkout of the recorded root", func(t *testing.T, origin string) string {
			path := filepath.Join(origin, "custom.db")

			data, err := os.ReadFile(store.DefaultPath(origin))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, data, 0o644))

			return path
		}, false},
		{"a file that the default path of the recorded root links to", func(t *testing.T, origin string) string {
			path := filepath.Join(rootWriteDir(t), "shared.db")

			require.NoError(t, os.Rename(store.DefaultPath(origin), path))
			require.NoError(t, os.Symlink(path, store.DefaultPath(origin)))

			return path
		}, false},
		{"a file of a third checkout", func(t *testing.T, origin string) string {
			copied := rootWriteCopy(t, origin)
			path := filepath.Join(copied, "custom.db")

			require.NoError(t, os.Rename(store.DefaultPath(copied), path))

			return path
		}, false},
		{"a file that no checkout owns", func(t *testing.T, origin string) string {
			path := filepath.Join(rootWriteDir(t), "custom.db")

			data, err := os.ReadFile(store.DefaultPath(origin))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, data, 0o644))

			return path
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := rootWriteRepo(t)
			third := rootMoveThird(t)
			db := tc.db(t, origin)

			want := rootMoveRefused(origin, third, db)
			if tc.wantMove {
				want = rootMoveRefusedWithMove(origin, third, db)
			}

			_, err := run(t, "-C", third, "--db", db, "index")
			require.EqualError(t, err, want)

			_, err = run(t, "-C", third, "--db", db, "index", "--move-root")
			require.NoError(t, err, "the flag works, also where no refusal names it")
			assert.Equal(t, third, rootMoveRecorded(t, db))
		})
	}
}

// TestIndexRefusalNamesNoMoveForAFileOfAPlainRoot covers a recorded root
// that shows no checkout: no git and no state directory. The file lies
// in that root all the same, so the move would split it from that root.
func TestIndexRefusalNamesNoMoveForAFileOfAPlainRoot(t *testing.T) {
	plain := rootWriteDir(t)
	third := rootMoveThird(t)
	custom := filepath.Join(plain, "custom.db")

	writeIndexDB(t, custom, map[string]string{store.MetaRepoRoot: plain})

	_, err := run(t, "-C", third, "--db", custom, "index")
	require.EqualError(t, err, rootMoveRefused(plain, third, custom))
}

// TestIndexMoveRootDoesNotSkipBehindALock covers a run that asks for the
// move while a lock stops the read of the root. A read-only open waits
// longer than that read, so a skip could report success. It must not:
// the user asked for a move, and nothing moved.
func TestIndexMoveRootDoesNotSkipBehindALock(t *testing.T) {
	origin := rootWriteRepo(t)
	tree := rootWriteWorktree(t, origin)
	db := store.DefaultPath(origin)

	require.Equal(t, index.WorkspaceState(origin), index.WorkspaceState(tree), "a skip must be possible")

	wait := rootWriteLock(t, db)

	_, err := run(t, "-C", tree, "--db", db, "index", "--move-root")
	require.ErrorIs(t, err, index.ErrRootMove)
	assert.ErrorContains(t, err, "the recorded root of this database cannot be read")

	wait()

	assert.Equal(t, origin, rootMoveRecorded(t, db))
}

// TestIndexRecordsAMovedRepositoryWithoutForce covers a git repository
// that moved with its custom file. Its fingerprint holds no path, so a
// skip kept the old root for ever, and every pin write then refused. The
// file lies inside the workspace, so a plain run records the workspace.
func TestIndexRecordsAMovedRepositoryWithoutForce(t *testing.T) {
	origin := rootWriteRepo(t)
	moved := filepath.Join(rootWriteDir(t), "moved")
	custom := filepath.Join(moved, "custom.db")

	require.NoError(t, os.Rename(origin, moved))
	require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

	out, err := run(t, "-C", moved, "--db", custom, "index")
	require.NoError(t, err)
	assert.Contains(t, out, "indexed "+moved)
	assert.Equal(t, moved, rootMoveRecorded(t, custom))

	out, err = run(t, "-C", moved, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 1 pin(s)")
	assert.NoDirExists(t, origin)
}

// rootMoveGoneLead is the refusal of a build in here up to its route,
// for a recorded root that a stat cannot find.
func rootMoveGoneLead(recorded, here string) string {
	return "this database records a directory that does not exist now (" + recorded + "), which can be a moved " +
		"repository or a detached disk, so a rebuild in " + here + " would replace that root; nothing changed; "
}

// rootMoveDetached builds an origin with a shared database on another
// disk and a sibling copy at one commit. The test moves the origin away,
// as a detached disk does. The returned function brings it back.
func rootMoveDetached(t *testing.T) (origin, sibling, shared string, attach func()) {
	t.Helper()

	origin = rootWriteRepo(t)
	shared = filepath.Join(rootWriteDir(t), "shared.db")

	require.NoError(t, os.Rename(store.DefaultPath(origin), shared))

	sibling = rootWriteCopy(t, origin)
	require.Equal(t, index.WorkspaceState(origin), index.WorkspaceState(sibling), "a plain run must be able to skip")

	away := filepath.Join(rootWriteDir(t), "away")
	require.NoError(t, os.Rename(origin, away))

	return origin, sibling, shared, func() { require.NoError(t, os.Rename(away, origin)) }
}

// TestIndexKeepsARootOnADetachedDisk covers a recorded root that is gone
// with a file outside this workspace. A stat cannot tell a detached disk
// from a moved repository. A plain run took the root, and a pin write
// landed in the sibling when the disk came back. So the skip stays, a
// rebuild refuses, and only the user moves the root.
func TestIndexKeepsARootOnADetachedDisk(t *testing.T) {
	origin, sibling, shared, attach := rootMoveDetached(t)
	move := "run `seamark index --move-root --db " + shared + "` in " + sibling

	out, err := run(t, "-C", sibling, "--db", shared, "index")
	require.NoError(t, err)
	assert.Equal(t, "index already up to date ("+shared+"); it records "+origin+"; to rebuild, "+move+
		" to move the root\n", out)
	assert.Equal(t, origin, rootMoveRecorded(t, shared))

	_, _, err = runIn(t, "", "-C", sibling, "--db", shared, "check")
	require.NoError(t, err, "a check at one commit keeps its verdict")
	assert.Equal(t, origin, rootMoveRecorded(t, shared))

	rootWriteRefused(t, "this database records a directory that no longer exists ("+origin+"), and a write would "+
		"recreate it; nothing changed; "+move+" first", rootMoveFiles(t, []string{sibling}, shared),
		"-C", sibling, "--db", shared, "lessons", "--apply", "p1")

	files := rootMoveFiles(t, []string{sibling}, shared)

	rootWriteRefused(t, rootMoveGoneLead(origin, sibling)+move+" to move the root", files,
		"-C", sibling, "--db", shared, "index", "--force")

	require.NoError(t, os.WriteFile(filepath.Join(sibling, "b.go"), []byte("package main\n"), 0o644))

	rootWriteRefused(t, rootMoveGoneLead(origin, sibling)+move+" to move the root", files,
		"-C", sibling, "--db", shared, "index")

	_, _, err = runIn(t, "", "-C", sibling, "--db", shared, "check")
	require.EqualError(t, err, rootMoveGoneLead(origin, sibling)+move+" to move the root")
	assert.Equal(t, origin, rootMoveRecorded(t, shared))

	attach()

	out, err = run(t, "-C", origin, "--db", shared, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 1 pin(s)")

	lessons, _ := maintainLessons(t, origin)
	assert.Contains(t, lessons, "new-rule", "the pin lands in the origin")

	lessons, _ = maintainLessons(t, sibling)
	assert.NotContains(t, lessons, "new-rule", "the sibling stays as it is")
}

// TestIndexMoveRootTakesAGoneRoot follows the route of the refusal. The
// user knows that the repository moved, so the flag records this
// workspace, and a pin write then stays here.
func TestIndexMoveRootTakesAGoneRoot(t *testing.T) {
	_, sibling, shared, _ := rootMoveDetached(t)

	_, err := run(t, "-C", sibling, "--db", shared, "index", "--move-root")
	require.NoError(t, err)
	assert.Equal(t, sibling, rootMoveRecorded(t, shared))

	out, err := run(t, "-C", sibling, "--db", shared, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 1 pin(s)")
}

// TestIndexKeepsTheFileOfAnotherCheckoutOfAGoneRoot covers a repository
// that moved with its file, while a third checkout at one commit names
// that file. A live custom file of a copy reads the same. The move would
// split two repositories, so the only true step is another file.
func TestIndexKeepsTheFileOfAnotherCheckoutOfAGoneRoot(t *testing.T) {
	cases := map[string]func(t *testing.T, origin, moved string) string{
		"the default database of a moved repository": func(_ *testing.T, _, moved string) string {
			return store.DefaultPath(moved)
		},
		"a custom file of a moved repository": func(t *testing.T, _, moved string) string {
			custom := filepath.Join(moved, "custom.db")

			require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

			return custom
		},
	}

	for name, place := range cases {
		t.Run(name, func(t *testing.T) {
			origin := rootWriteRepo(t)
			third := rootWriteCopy(t, origin)
			moved := filepath.Join(rootWriteDir(t), "moved")

			require.NoError(t, os.Rename(origin, moved))
			require.NoError(t, os.Remove(store.DefaultPath(third)))

			db := place(t, origin, moved)

			require.Equal(t, index.WorkspaceState(moved), index.WorkspaceState(third), "a plain run must be able to skip")

			out, err := run(t, "-C", third, "--db", db, "index")
			require.NoError(t, err)
			assert.Contains(t, out, "use another --db")
			assert.Equal(t, origin, rootMoveRecorded(t, db))

			require.NoError(t, os.WriteFile(filepath.Join(third, "b.go"), []byte("package main\n"), 0o644))

			files := rootMoveFiles(t, []string{third}, db)

			rootWriteRefused(t, rootMoveGoneLead(origin, third)+"use another --db", files,
				"-C", third, "--db", db, "index")

			rootWriteRefused(t, "this database records a directory that no longer exists ("+origin+"), and a write "+
				"would recreate it; nothing changed; run `seamark index --move-root --db "+db+"` in "+moved+" first",
				files, "-C", third, "--db", db, "lessons", "--apply", "p1")
		})
	}
}

// TestIndexKeepsTheSkipOfAFileThatRefusesWrites covers a read-only file
// with a gone root. A skip reads, so it stays. A rebuild would fail on
// every run, inside the workspace too, where the move is automatic.
func TestIndexKeepsTheSkipOfAFileThatRefusesWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a file with mode 0444")
	}

	t.Run("a file outside the workspace", func(t *testing.T) {
		origin, sibling, shared, _ := rootMoveDetached(t)

		require.NoError(t, os.Chmod(shared, 0o444))

		out, err := run(t, "-C", sibling, "--db", shared, "index")
		require.NoError(t, err)
		assert.Contains(t, out, "index already up to date")

		_, _, err = runIn(t, "", "-C", sibling, "--db", shared, "check")
		require.NoError(t, err)
		assert.Equal(t, origin, rootMoveRecorded(t, shared))
	})

	t.Run("a file inside the workspace", func(t *testing.T) {
		origin := rootWriteRepo(t)
		moved := filepath.Join(rootWriteDir(t), "moved")
		custom := filepath.Join(moved, "custom.db")

		require.NoError(t, os.Rename(origin, moved))
		require.NoError(t, os.Rename(store.DefaultPath(moved), custom))
		require.NoError(t, os.Chmod(custom, 0o444))

		for range 2 {
			out, err := run(t, "-C", moved, "--db", custom, "index")
			require.NoError(t, err, "a run must converge")
			assert.Contains(t, out, "index already up to date")
		}

		_, _, err := runIn(t, "", "-C", moved, "--db", custom, "check")
		require.NoError(t, err)
		assert.Equal(t, origin, rootMoveRecorded(t, custom))
	})
}

// rootMoveHidden returns a root that a stat cannot reach, because its
// parent directory gives no permission. The test skips as root, because
// root ignores the file mode.
func rootMoveHidden(t *testing.T) string {
	t.Helper()

	if os.Geteuid() == 0 {
		t.Skip("root reads a directory with mode 0000")
	}

	parent := rootWriteDir(t)
	root := filepath.Join(parent, "hidden")

	require.NoError(t, os.Mkdir(root, 0o755))
	require.NoError(t, os.Chmod(parent, 0o000))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	return root
}

// TestIndexTreatsARootBelowAFileAsGone covers ENOTDIR. A part of the
// recorded path is a file now, so nothing can exist at that path. It
// follows the rule of a root that is gone.
func TestIndexTreatsARootBelowAFileAsGone(t *testing.T) {
	root := rootMoveThird(t)
	file := filepath.Join(rootWriteDir(t), "file")
	old := filepath.Join(file, "old")
	outside := filepath.Join(rootWriteDir(t), "custom.db")
	inside := filepath.Join(root, "custom.db")

	require.NoError(t, os.WriteFile(file, nil, 0o644))
	writeIndexDB(t, outside, map[string]string{store.MetaRepoRoot: old})
	writeIndexDB(t, inside, map[string]string{store.MetaRepoRoot: old})

	_, err := run(t, "-C", root, "--db", outside, "index")
	require.EqualError(t, err, rootMoveGoneLead(old, root)+
		"run `seamark index --move-root --db "+outside+"` in "+root+" to move the root")

	_, err = run(t, "-C", root, "--db", inside, "index")
	require.NoError(t, err)
	assert.Equal(t, root, rootMoveRecorded(t, inside))
}

// TestIndexDropsTheRouteOfARootThatCannotBeReached covers EACCES. The
// root can still exist, so the refusal stays. A route into it cannot run,
// so only the move remains. A file of that root has no route at all
// through the workspace, so the text names another file.
func TestIndexDropsTheRouteOfARootThatCannotBeReached(t *testing.T) {
	t.Run("a file that no checkout owns", func(t *testing.T) {
		third := rootMoveThird(t)
		custom := filepath.Join(rootWriteDir(t), "custom.db")
		hidden := rootMoveHidden(t)

		writeIndexDB(t, custom, map[string]string{store.MetaRepoRoot: hidden})

		_, err := run(t, "-C", third, "--db", custom, "index")
		require.EqualError(t, err, rootMoveLead(hidden, third)+
			"run `seamark index --move-root --db "+custom+"` in "+third+" to move the root")

		_, err = run(t, "-C", third, "--db", custom, "index", "--move-root")
		require.NoError(t, err, "the route of the refusal works")
		assert.Equal(t, third, rootMoveRecorded(t, custom))
	})

	t.Run("a file that the move would split", func(t *testing.T) {
		origin := rootWriteRepo(t)
		third := rootMoveThird(t)
		custom := filepath.Join(origin, "custom.db")
		hidden := rootMoveHidden(t)

		writeIndexDB(t, custom, map[string]string{store.MetaRepoRoot: hidden})

		_, err := run(t, "-C", third, "--db", custom, "index")
		require.EqualError(t, err, rootMoveLead(hidden, third)+"use another --db")
	})

	t.Run("a pin write", func(t *testing.T) {
		third := rootMoveThird(t)
		custom := filepath.Join(rootWriteDir(t), "custom.db")
		hidden := rootMoveHidden(t)

		writeIndexDB(t, custom, map[string]string{store.MetaRepoRoot: hidden})

		_, err := run(t, "-C", third, "--db", custom, "lessons", "--apply", "p1")
		require.EqualError(t, err, "this database records another repository ("+hidden+") that does not own it, "+
			"so this command would change the pin file there and the ledger in this database; nothing changed; "+
			"use another --db")
	})
}

// TestIndexRefusalNamesTheNestedOwner covers a file in a repository nested
// in this workspace, with a third repository as the recorded root. The
// nearest mark above the file names its checkout, so the outer workspace
// does not own it, and a move would split the two repositories.
func TestIndexRefusalNamesTheNestedOwner(t *testing.T) {
	for name, mark := range map[string]string{"a git repository": ".git", "a Seamark checkout": store.DefaultDir} {
		t.Run(name, func(t *testing.T) {
			third := rootWriteRepo(t)
			outer := rootMoveThird(t)
			nested := filepath.Join(outer, "nested")
			custom := filepath.Join(nested, "custom.db")

			require.NoError(t, os.MkdirAll(filepath.Join(nested, mark), 0o755))
			writeIndexDB(t, custom, map[string]string{store.MetaRepoRoot: third})

			_, err := run(t, "-C", outer, "--db", custom, "index")
			require.EqualError(t, err, rootMoveRefused(third, outer, custom))
		})
	}
}
