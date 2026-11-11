package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// These tests drive `lessons --apply`, `--retarget`, and `--prune`
// through the command tree, on real git repositories with a real index.
// A pin write goes to the file under the recorded root, and its ledger
// write goes to the database. The tests pin the states that the rule
// refuses and the states that it keeps.

const (
	// rootWriteConfig turns the write gate on.
	rootWriteConfig = "distill:\n  write: true\n"
	// rootWriteConfigOff turns the write gate off.
	rootWriteConfigOff = "distill:\n  write: false\n"
	// rootWriteLessons holds the pins of the applied rows of the fixture.
	rootWriteLessons = `pin:
  - rule: retire-me
    region: web
    note: A pin to prune.
  - rule: retarget-me
    region: api
    note: A pin to retarget.
`
	// rootWriteIgnore keeps the state of a run out of git, as `seamark
	// init` does, and leaves the pin file and the config tracked. An
	// ignored custom database leaves the workspace fingerprint as it is.
	rootWriteIgnore = ".seamark/*\n!.seamark/lessons.yaml\n!.seamark/config.yaml\ncustom.db*\n"
	// rootWriteHold is longer than the wait of a read before the open and
	// shorter than the wait of the open.
	rootWriteHold = 3 * time.Second
)

// rootWriteCommand is one command that writes under the recorded root.
type rootWriteCommand struct {
	name string
	args []string
	// done is a part of the output of a run that reached the pin file.
	done string
}

// rootWriteCommands lists the commands that the rule guards. Each one
// names a row that the fixture holds in the state that the command wants.
var rootWriteCommands = []rootWriteCommand{
	{"apply", []string{"lessons", "--apply", "p1"}, "applied 1 pin(s)"},
	{"retarget", []string{"lessons", "--retarget", "p3"}, "evidence aged out"},
	{"prune", []string{"lessons", "--prune", "p2"}, "pruned 1 pin(s)"},
}

// rootWriteDir returns a new directory by its resolved path, because git
// and the index name the resolved root.
func rootWriteDir(t *testing.T) string {
	t.Helper()

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	return dir
}

// rootWriteRepo builds a real repository in a new directory: one commit,
// the write gate on, a real index, a pending p1, and the applied rows p2
// and p3 with their pins.
func rootWriteRepo(t *testing.T) string {
	t.Helper()

	root := rootWriteTree(t)

	gitify(t, root)
	rootWriteIndex(t, root)

	return root
}

// rootWritePlainRoot is rootWriteRepo for a directory outside git.
func rootWritePlainRoot(t *testing.T) string {
	t.Helper()

	root := rootWriteTree(t)

	rootWriteIndex(t, root)

	return root
}

// rootWriteTree writes the files of the fixture in a new directory.
func rootWriteTree(t *testing.T) string {
	t.Helper()

	root := filepath.Join(rootWriteDir(t), "origin")

	for rel, content := range map[string]string{
		"go.mod":                "module example.com/fix\n",
		"a.go":                  "package main\n\nfunc main() {}\n",
		".gitignore":            rootWriteIgnore,
		".seamark/config.yaml":  rootWriteConfig,
		".seamark/lessons.yaml": rootWriteLessons,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))

		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	return root
}

// rootWriteIndex runs a real index in root and stores the fixture rows.
func rootWriteIndex(t *testing.T, root string) {
	t.Helper()

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	rootWriteSeed(t, root)
}

// rootWriteSeed stores the rows of the fixture in the default database
// of root.
func rootWriteSeed(t *testing.T, root string) {
	t.Helper()

	row := func(signature, rule, region, note, status string, member int64) model.Proposal {
		return model.Proposal{Signature: signature, Rule: rule, Region: region, Note: note,
			Members: []int64{member}, Agent: "claude/v6", Status: status}
	}

	goldenSeedProposals(t, root,
		row("s1", "new-rule", "api", "A pending pin.", model.ProposalProposed, 1),
		row("s2", "retire-me", "web", "A pin to prune.", model.ProposalApplied, 2),
		row("s3", "retarget-me", "api", "A pin to retarget.", model.ProposalApplied, 3),
	)
}

// rootWriteCopy copies a whole checkout with `cp -R`, as a person does.
// The copy keeps the index, so it still records the root of origin.
func rootWriteCopy(t *testing.T, origin string) string {
	t.Helper()

	dst := filepath.Join(rootWriteDir(t), "copy")

	out, err := exec.Command("cp", "-R", origin, dst).CombinedOutput()
	require.NoError(t, err, string(out))

	return dst
}

// rootWriteFiles reads every state file of the roots, and each extra
// file. A refusal must leave all of them byte for byte as they are. A
// file that does not exist has its own value, so it cannot appear.
func rootWriteFiles(t *testing.T, roots []string, extra ...string) map[string]string {
	t.Helper()

	paths := append([]string{}, extra...)

	for _, root := range roots {
		paths = append(paths, store.DefaultPath(root),
			filepath.Join(root, ".seamark", "lessons.yaml"), filepath.Join(root, ".seamark", "config.yaml"))
	}

	files := map[string]string{}

	for _, p := range paths {
		data, err := os.ReadFile(p)

		switch {
		case os.IsNotExist(err):
			files[p] = "<absent>"
		default:
			require.NoError(t, err)

			files[p] = string(data)
		}
	}

	return files
}

// rootWriteStatus returns the ledger state of row id in the database at
// path.
func rootWriteStatus(t *testing.T, path string, id int64) string {
	t.Helper()

	st, err := store.OpenReadOnly(path)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	for _, status := range []string{
		model.ProposalProposed, model.ProposalApplied, model.ProposalDismissed, model.ProposalSuperseded,
	} {
		rows, err := st.Proposals(status)
		require.NoError(t, err)

		for _, row := range rows {
			if row.ID == id {
				return status
			}
		}
	}

	require.Failf(t, "no such row", "p%d is not in %s", id, path)

	return ""
}

// rootWriteRefused runs one command, requires the exact refusal, and
// requires that the run wrote nothing. A refusal uses the exit code 1 of
// every other refusal of these commands, so it matches neither policy
// block nor lesson violation, the two sentinels of exit code 2.
func rootWriteRefused(t *testing.T, want string, files func() map[string]string, args ...string) {
	t.Helper()

	before := files()

	stdout, stderr, err := runErr(t, args...)

	require.EqualError(t, err, want)
	assert.NotErrorIs(t, err, gate.ErrBlocked)
	assert.NotErrorIs(t, err, lessoncheck.ErrViolation)
	assert.Empty(t, stdout, "a refusal prints no change to make by hand")
	assert.Empty(t, stderr)
	assert.Equal(t, before, files(), "a refusal writes nothing")
}

// rootWriteCopied is the refusal of the default database of a copy. The
// only route is an index run there. A route to the origin would resolve
// the id of the command in another ledger.
func rootWriteCopied(origin, copied string) string {
	return "the index of this checkout records another repository (" + origin + "), so this command would " +
		"change the pin file there and the ledger in this database; nothing changed; " +
		"run `seamark index` in " + copied + " first"
}

// rootWriteLock holds an exclusive lock on the database at path for
// rootWriteHold, and it returns the function that waits for the release.
// A rollback journal holds the lock, because a write-ahead log lets a
// reader pass.
func rootWriteLock(t *testing.T, path string) (wait func()) {
	t.Helper()

	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	db.SetMaxOpenConns(1)

	_, err = db.Exec(`PRAGMA journal_mode = DELETE`)
	require.NoError(t, err)

	_, err = db.Exec(`BEGIN EXCLUSIVE`)
	require.NoError(t, err)

	released := make(chan error, 1)

	timer := time.AfterFunc(rootWriteHold, func() {
		_, err := db.Exec(`COMMIT`)
		released <- err
	})

	t.Cleanup(func() { timer.Stop() })

	return func() { require.NoError(t, <-released) }
}

// rootWriteUnowned is the refusal of a database that the recorded root
// does not own. route is the rest of the sentence after the root.
func rootWriteUnowned(origin, route string) string {
	return "this database records another repository (" + origin + ") that does not own it, so this command " +
		"would change the pin file there and the ledger in this database; nothing changed; " +
		"run this command in " + origin + route
}

// TestRootWriteRefusesACopiedCheckout covers the first state of the
// rule: the default database of this workspace records another
// directory. A directory inside the copy names the same workspace.
func TestRootWriteRefusesACopiedCheckout(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	inside := filepath.Join(copied, "pkg")

	require.NoError(t, os.MkdirAll(inside, 0o755))

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	for _, c := range rootWriteCommands {
		for name, dir := range map[string]string{"the top level": copied, "a directory inside": inside} {
			t.Run(c.name+" from "+name, func(t *testing.T) {
				rootWriteRefused(t, rootWriteCopied(origin, copied), files, append([]string{"-C", dir}, c.args...)...)
			})
		}
	}
}

// TestRootWriteRefusesBeforeTheSelection covers the selection
// grammar. The refusal comes first, so a selection without a pending row
// names the root and not the selection.
func TestRootWriteRefusesBeforeTheSelection(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	rootWriteRefused(t, rootWriteCopied(origin, copied), files, "-C", copied, "lessons", "--apply", "p50..p60")
}

// TestRootWriteRefusesWithTheGateOff covers the answers without a write.
// With distill.write off, each command prints a change to make by hand.
// That answer reads the gate of the recorded root and names a file that
// the copy does not use, so it refuses too.
func TestRootWriteRefusesWithTheGateOff(t *testing.T) {
	origin := rootWriteRepo(t)
	require.NoError(t, os.WriteFile(filepath.Join(origin, ".seamark", "config.yaml"), []byte(rootWriteConfigOff), 0o644))

	copied := rootWriteCopy(t, origin)

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			rootWriteRefused(t, rootWriteCopied(origin, copied), files, append([]string{"-C", copied}, c.args...)...)
		})
	}
}

// TestRootWriteRouteOfACopyWorks follows the refusal. An index run in
// the copy records the copy, and each command then changes the pin file
// and the ledger of the copy, and nothing of the origin.
func TestRootWriteRouteOfACopyWorks(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)

	_, err := run(t, "-C", copied, "lessons", "--apply", "p1")
	require.Error(t, err)

	_, err = run(t, "-C", copied, "index")
	require.NoError(t, err)

	before := rootWriteFiles(t, []string{origin})

	for _, c := range rootWriteCommands {
		_, err := run(t, append([]string{"-C", copied}, c.args...)...)
		require.NoError(t, err, c.name)
	}

	lessons, _ := maintainLessons(t, copied)
	assert.Contains(t, lessons, "new-rule", "the apply writes the pin file of the copy")
	assert.NotContains(t, lessons, "retire-me", "the prune removes the pin of the copy")
	assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, store.DefaultPath(copied), 1))
	assert.Equal(t, model.ProposalSuperseded, rootWriteStatus(t, store.DefaultPath(copied), 2))

	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
}

// TestRootWriteRefusesAnOlderCopyBeforeTheOpen covers the schema
// migration of the open, which is a write too. The rule reads the root
// of every schema, so an older copy refuses and stays older.
func TestRootWriteRefusesAnOlderCopyBeforeTheOpen(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	db := store.DefaultPath(copied)
	older := schemaMeta(-1)

	writeIndexDB(t, db, older)

	want, err := store.ProbeVersion(db)
	require.NoError(t, err)
	require.Less(t, want, store.SupportedSchema())

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			rootWriteRefused(t, rootWriteCopied(origin, copied), files, append([]string{"-C", copied}, c.args...)...)

			got, err := store.ProbeVersion(db)
			require.NoError(t, err)
			assert.Equal(t, want, got, "the refusal does not migrate the schema")
		})
	}
}

// TestRootWriteKeepsTheUpgradeMessageOfANewerCopy covers a schema that
// this binary cannot read. The route of the rule is an index run, and
// that run fails on this schema. So the open answers: it refuses without
// a write and names the upgrade.
func TestRootWriteKeepsTheUpgradeMessageOfANewerCopy(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	db := store.DefaultPath(copied)

	writeIndexDB(t, db, schemaMeta(+1))

	want := storeOpenErr(t, db)
	require.Contains(t, want, "upgrade seamark")

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			rootWriteRefused(t, want, files, append([]string{"-C", copied}, c.args...)...)
		})
	}
}

// TestRootWriteUpgradesAnOlderSchemaInACheckout covers a normal checkout
// with an older schema. The rule runs before the open that migrates, so
// the rule must read that schema and let each command through.
func TestRootWriteUpgradesAnOlderSchemaInACheckout(t *testing.T) {
	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			root := rootWriteRepo(t)
			db := store.DefaultPath(root)

			writeIndexDB(t, db, schemaMeta(-1))

			out, err := run(t, append([]string{"-C", root}, c.args...)...)
			require.NoError(t, err)
			assert.Contains(t, out, c.done)
			assertSchemaSupported(t, db)
		})
	}
}

// TestRootWriteRefusesEverySpellingOfACopyDatabase removes the default
// database of the origin, so the origin owns any file that records it.
// Only the rule for this workspace can then refuse. Each spelling names
// the default database of the copy, so each must read as that file.
func TestRootWriteRefusesEverySpellingOfACopyDatabase(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	inside := filepath.Join(copied, "pkg")
	link := filepath.Join(rootWriteDir(t), "link")

	require.NoError(t, os.MkdirAll(inside, 0o755))
	require.NoError(t, os.Remove(store.DefaultPath(origin)))
	require.NoError(t, os.Symlink(copied, link))

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}) }

	t.Run("a relative --db", func(t *testing.T) {
		t.Chdir(copied)

		rootWriteRefused(t, rootWriteCopied(origin, copied), files,
			"--db", filepath.Join(".seamark", "index.db"), "lessons", "--apply", "p1")
	})

	t.Run("a --db from a directory inside the copy", func(t *testing.T) {
		rootWriteRefused(t, rootWriteCopied(origin, copied), files,
			"-C", inside, "--db", store.DefaultPath(copied), "lessons", "--apply", "p1")
	})

	t.Run("a --db through a link", func(t *testing.T) {
		rootWriteRefused(t, rootWriteCopied(origin, copied), files,
			"-C", copied, "--db", store.DefaultPath(link), "lessons", "--prune", "p2")
	})
}

// TestRootWriteRefusesADatabaseTheRootDoesNotOwn covers the second state
// of the rule: a file that the recorded root does not own. The route
// names --db only for a custom database. A file of another checkout
// with a command in the origin would write two repositories.
func TestRootWriteRefusesADatabaseTheRootDoesNotOwn(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	third := rootWriteDir(t)
	custom := filepath.Join(third, "custom.db")
	renamed := filepath.Join(copied, ".seamark", "renamed.db")

	db, err := os.ReadFile(store.DefaultPath(origin))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(custom, db, 0o644))
	require.NoError(t, os.WriteFile(renamed, db, 0o644))

	files := func() map[string]string { return rootWriteFiles(t, []string{origin, copied}, custom, renamed) }

	cases := []struct {
		name, dir, db, want string
	}{
		{"a custom file outside any checkout", third, custom, rootWriteUnowned(origin, " with --db "+custom)},
		{"the database of a copy from a third directory", third, store.DefaultPath(copied), rootWriteClosed(origin)},
		{"a renamed database in a copy", copied, renamed, rootWriteMoved(origin, copied, renamed)},
	}

	for _, tc := range cases {
		for _, c := range rootWriteCommands {
			t.Run(tc.name+" with "+c.name, func(t *testing.T) {
				rootWriteRefused(t, tc.want, files, append([]string{"-C", tc.dir, "--db", tc.db}, c.args...)...)
			})
		}
	}

	t.Run("a relative --db reads as an absolute route", func(t *testing.T) {
		t.Chdir(third)

		rootWriteRefused(t, rootWriteUnowned(origin, " with --db "+custom), files,
			"--db", "custom.db", "lessons", "--apply", "p1")
	})

	t.Run("the route of a custom file works", func(t *testing.T) {
		_, err := run(t, "-C", origin, "--db", custom, "lessons", "--apply", "p1")
		require.NoError(t, err)

		lessons, _ := maintainLessons(t, origin)
		assert.Contains(t, lessons, "new-rule")
		assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, custom, 1))
	})
}

// rootWriteMoved is the refusal of a file of this workspace. Its route
// records this workspace in the file, because a route to the recorded root
// would write two repositories.
func rootWriteMoved(origin, here, db string) string {
	return "this database records another repository (" + origin + ") that does not own it, so this command " +
		"would change the pin file there and the ledger in this database; nothing changed; " +
		"run `seamark index --move-root --db " + db + "` in " + here + " first"
}

// rootWriteClosed is the refusal of a file of a third checkout. No command
// can take this file, so only another file remains.
func rootWriteClosed(origin string) string {
	return "this database records another repository (" + origin + ") that does not own it, so this command " +
		"would change the pin file there and the ledger in this database; nothing changed; use another --db"
}

// rootWriteCustomCopy returns the origin, a copy, and a custom file of
// that copy. The file is the database of the copy under another name, so
// it records the origin and holds the rows of the fixture.
func rootWriteCustomCopy(t *testing.T) (origin, copied, custom string) {
	t.Helper()

	origin = rootWriteRepo(t)
	copied = rootWriteCopy(t, origin)
	custom = filepath.Join(copied, "custom.db")

	require.NoError(t, os.Rename(store.DefaultPath(copied), custom))

	return origin, copied, custom
}

// TestRootWriteRouteOfACustomFileInACopyWorks follows the refusal of the
// custom file of a copied checkout. A route to the origin with this file
// changed the pin file of the origin and the ledger of the copy. The test
// runs the route that the refusal prints, and then each command. Each one
// changes the copy and nothing of the origin.
func TestRootWriteRouteOfACustomFileInACopyWorks(t *testing.T) {
	origin, copied, custom := rootWriteCustomCopy(t)
	flags := []string{"-C", copied, "--db", custom}
	files := rootMoveFiles(t, []string{origin, copied}, custom)

	for _, c := range rootWriteCommands {
		t.Run("refuses "+c.name, func(t *testing.T) {
			rootWriteRefused(t, rootWriteMoved(origin, copied, custom), files, append(flags, c.args...)...)
		})
	}

	t.Run("a relative --db reads as an absolute route", func(t *testing.T) {
		t.Chdir(copied)

		rootWriteRefused(t, rootWriteMoved(origin, copied, custom), files, "--db", "custom.db", "lessons", "--apply", "p1")
	})

	before := rootWriteFiles(t, []string{origin})

	_, err := run(t, "-C", copied, "index", "--move-root", "--db", custom)
	require.NoError(t, err)
	assert.Equal(t, copied, rootMoveRecorded(t, custom))

	for _, c := range rootWriteCommands {
		out, err := run(t, append(flags, c.args...)...)
		require.NoError(t, err, c.name)
		assert.Contains(t, out, c.done, c.name)
	}

	lessons, _ := maintainLessons(t, copied)
	assert.Contains(t, lessons, "new-rule", "the apply writes the pin file of the copy")
	assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, custom, 1))
	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
}

// TestConsoleRouteOfACustomFileInACopyWorks follows the text of the
// console through the CLI. The command and its directory come from the
// refusal of the service, so a change of that text fails here.
func TestConsoleRouteOfACustomFileInACopyWorks(t *testing.T) {
	origin, copied, custom := rootWriteCustomCopy(t)

	ws, err := workspace.Resolve(copied, custom)
	require.NoError(t, err)

	refusal := services.New(ws, "test", "/usr/local/bin/seamark").Pins().Database.WriteBlock
	require.ErrorIs(t, refusal, services.ErrForeignRoot)

	route := regexp.MustCompile("run `seamark ([^`]*)` in (\\S+) first").FindStringSubmatch(refusal.Error())
	require.Len(t, route, 3, refusal.Error())

	before := rootWriteFiles(t, []string{origin})

	_, err = run(t, append([]string{"-C", route[2]}, strings.Fields(route[1])...)...)
	require.NoError(t, err)
	assert.Equal(t, copied, rootMoveRecorded(t, custom))

	again := services.New(ws, "test", "/usr/local/bin/seamark").Pins().Database
	require.NoError(t, again.WriteBlock, "the route ends the refusal")
	assert.Empty(t, again.ForeignRoot)

	_, err = run(t, "-C", copied, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
}

// TestRootWriteNamesNoRouteThatSplitsAFileOfAnotherCheckout covers the
// custom file of a copy from a third directory. A route to the origin
// with this file would write two repositories. One without it would
// resolve the id in the ledger of the origin. So the route names another
// file. The move in the copy still works, so the test runs it and repeats
// the command.
func TestRootWriteNamesNoRouteThatSplitsAFileOfAnotherCheckout(t *testing.T) {
	origin, copied, custom := rootWriteCustomCopy(t)
	third := rootWriteDir(t)

	rootWriteRefused(t, rootWriteClosed(origin), rootMoveFiles(t, []string{origin}, custom),
		"-C", third, "--db", custom, "lessons", "--apply", "p1")

	before := rootWriteFiles(t, []string{origin})

	_, err := run(t, "-C", copied, "index", "--move-root", "--db", custom)
	require.NoError(t, err)

	_, err = run(t, "-C", third, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err)

	lessons, _ := maintainLessons(t, copied)
	assert.Contains(t, lessons, "new-rule", "the pin lands in the owner of the file")
	assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, custom, 1))
	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
}

// rootWriteRow stores one pending row in the database at path and
// returns its id.
func rootWriteRow(t *testing.T, path, rule string) int64 {
	t.Helper()

	st, err := store.Open(path)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	row := model.Proposal{Signature: rule, Rule: rule, Region: "api", Note: "A pending pin.",
		Members: []int64{1}, Agent: "claude/v6", Status: model.ProposalProposed}
	require.NoError(t, storetest.InsertProposal(st, &row))

	return row.ID
}

// TestRootWriteRouteNeverNamesAnotherProposal covers one id in two
// ledgers. Each ledger holds another row under that id, so a route that
// drops --db applies a proposal that the user did not name. Each case
// follows the route, repeats the command, and reads the pin that it wrote.
func TestRootWriteRouteNeverNamesAnotherProposal(t *testing.T) {
	cases := []struct {
		name string
		// setup returns the ledger of the copy and the flags of the command.
		setup func(t *testing.T, copied string) (ledger string, flags []string)
		// want is the refusal for the origin, the copy, and the ledger.
		want func(origin, copied, ledger string) string
		// follow returns the arguments of the route.
		follow func(copied, ledger string) []string
	}{
		{"a renamed database in a copy", func(t *testing.T, copied string) (string, []string) {
			renamed := filepath.Join(copied, store.DefaultDir, "renamed.db")

			require.NoError(t, os.Rename(store.DefaultPath(copied), renamed))

			return renamed, []string{"-C", copied, "--db", renamed}
		}, rootWriteMoved, func(copied, ledger string) []string {
			return []string{"-C", copied, "index", "--move-root", "--db", ledger}
		}},
		{"a renamed database from a third directory", func(t *testing.T, copied string) (string, []string) {
			renamed := filepath.Join(copied, store.DefaultDir, "renamed.db")

			require.NoError(t, os.Rename(store.DefaultPath(copied), renamed))

			return renamed, []string{"-C", rootWriteDir(t), "--db", renamed}
		}, func(origin, _, _ string) string { return rootWriteClosed(origin) }, func(copied, ledger string) []string {
			return []string{"-C", copied, "index", "--move-root", "--db", ledger}
		}},
		{"the default database of a copy", func(_ *testing.T, copied string) (string, []string) {
			return store.DefaultPath(copied), []string{"-C", copied}
		}, func(origin, copied, _ string) string { return rootWriteCopied(origin, copied) },
			func(copied, _ string) []string { return []string{"-C", copied, "index"} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin := rootWriteRepo(t)
			copied := rootWriteCopy(t, origin)
			ledger, flags := tc.setup(t, copied)

			id := rootWriteRow(t, store.DefaultPath(origin), "origin-rule")
			require.Equal(t, id, rootWriteRow(t, ledger, "copy-rule"), "one id must name two rows")

			command := slices.Concat(flags, []string{"lessons", "--apply", fmt.Sprintf("p%d", id)})

			rootWriteRefused(t, tc.want(origin, copied, ledger),
				rootMoveFiles(t, []string{origin, copied}, ledger), command...)

			before := rootWriteFiles(t, []string{origin})

			_, err := run(t, tc.follow(copied, ledger)...)
			require.NoError(t, err)

			_, err = run(t, command...)
			require.NoError(t, err)

			lessons, _ := maintainLessons(t, copied)
			assert.Contains(t, lessons, "copy-rule", "the command applies the row that it named")
			assert.NotContains(t, lessons, "origin-rule")
			assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
		})
	}
}

// TestRootWriteRefusesADatabaseOfANestedCopy covers a copy nested in a
// root outside git. The directory read as the root, but its state
// directory marks another checkout. A pin went to the root and its ledger
// to the nested file. The route records the copy in the file.
func TestRootWriteRefusesADatabaseOfANestedCopy(t *testing.T) {
	for name, rel := range map[string]string{
		"a renamed database":               filepath.Join(store.DefaultDir, "renamed.db"),
		"a custom file":                    "custom.db",
		"a file below the state directory": filepath.Join(store.DefaultDir, "sub", "x.db"),
	} {
		t.Run(name, func(t *testing.T) {
			root := rootWritePlainRoot(t)
			backup := filepath.Join(root, "backup")
			file := filepath.Join(backup, rel)

			require.NoError(t, os.Rename(rootWriteCopy(t, root), backup))
			require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
			require.NoError(t, os.Rename(store.DefaultPath(backup), file))

			before := rootWriteFiles(t, []string{root})

			rootWriteRefused(t, rootWriteMoved(root, backup, file), rootMoveFiles(t, []string{root}, file),
				"-C", backup, "--db", file, "lessons", "--apply", "p1")

			_, err := run(t, "-C", backup, "index", "--move-root", "--db", file)
			require.NoError(t, err)

			out, err := run(t, "-C", backup, "--db", file, "lessons", "--apply", "p1")
			require.NoError(t, err)
			assert.Contains(t, out, "applied 1 pin(s)")

			assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, file, 1))
			assert.Equal(t, before, rootWriteFiles(t, []string{root}), "the root stays as it is")
		})
	}
}

// TestRootWriteKeepsTheRouteOfAFileThatNoCheckoutOwns covers a git work
// tree that is no Seamark checkout, such as a home directory that holds
// a shared database. A route to --move-root there took the root from the
// repository. A file in a submodule or a linked worktree of the recorded
// root has the same answer: the route is the recorded root with this file.
func TestRootWriteKeepsTheRouteOfAFileThatNoCheckoutOwns(t *testing.T) {
	cases := map[string]func(t *testing.T, origin string) string{
		"a home directory that is a git work tree": func(t *testing.T, _ string) string {
			home := rootWriteDir(t)

			require.NoError(t, os.WriteFile(filepath.Join(home, ".bashrc"), []byte("x\n"), 0o644))
			gitify(t, home)

			return filepath.Join(home, ".cache", "seamark", "shared.db")
		},
		"a submodule of the recorded root": func(t *testing.T, origin string) string {
			sub := filepath.Join(origin, "sub")

			require.NoError(t, os.MkdirAll(filepath.Join(sub, store.DefaultDir), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(sub, "s.go"), []byte("package sub\n"), 0o644))
			gitify(t, sub)

			return filepath.Join(sub, "shared.db")
		},
		"a linked worktree without a state directory": func(t *testing.T, origin string) string {
			tree := rootWriteWorktree(t, origin)

			require.NoError(t, os.RemoveAll(filepath.Join(tree, store.DefaultDir)))

			return filepath.Join(tree, "shared.db")
		},
	}

	for name, place := range cases {
		t.Run(name, func(t *testing.T) {
			origin := rootWriteRepo(t)
			third := rootWriteDir(t)
			shared := place(t, origin)

			data, err := os.ReadFile(store.DefaultPath(origin))
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o755))
			require.NoError(t, os.WriteFile(shared, data, 0o644))

			rootWriteRefused(t, rootWriteUnowned(origin, " with --db "+shared), rootMoveFiles(t, []string{origin}, shared),
				"-C", third, "--db", shared, "lessons", "--apply", "p1")

			out, err := run(t, "-C", origin, "--db", shared, "lessons", "--apply", "p1")
			require.NoError(t, err, "the route of the refusal works")
			assert.Contains(t, out, "applied 1 pin(s)")
			assert.Equal(t, origin, rootMoveRecorded(t, shared), "the root stays with its repository")
		})
	}
}

// TestRootWriteRouteOfACustomFileInACopyWorksOutsideGit follows the
// refusal for a copy without git. The state directory names the checkout,
// so no route takes this file to the origin.
func TestRootWriteRouteOfACustomFileInACopyWorksOutsideGit(t *testing.T) {
	origin := rootWritePlainRoot(t)
	copied := rootWriteCopy(t, origin)
	custom := filepath.Join(copied, "custom.db")
	third := rootWriteDir(t)

	require.NoError(t, os.Rename(store.DefaultPath(copied), custom))

	files := rootMoveFiles(t, []string{origin, copied}, custom)

	rootWriteRefused(t, rootWriteClosed(origin), files, "-C", third, "--db", custom, "lessons", "--apply", "p1")
	rootWriteRefused(t, rootWriteMoved(origin, copied, custom), files, "-C", copied, "--db", custom, "lessons", "--apply", "p1")

	before := rootWriteFiles(t, []string{origin})

	_, err := run(t, "-C", copied, "index", "--move-root", "--db", custom)
	require.NoError(t, err)

	_, err = run(t, "-C", copied, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err)

	lessons, _ := maintainLessons(t, copied)
	assert.Contains(t, lessons, "new-rule")
	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
}

// TestRootWriteKeepsTheRouteOfACustomFileInTheRecordedRoot bounds the rule
// above. A custom file that lies in the recorded root belongs to that
// root, so the route keeps --db, also from a workspace around that root.
func TestRootWriteKeepsTheRouteOfACustomFileInTheRecordedRoot(t *testing.T) {
	origin := rootWriteRepo(t)
	custom := filepath.Join(origin, "custom.db")

	data, err := os.ReadFile(store.DefaultPath(origin))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(custom, data, 0o644))

	files := rootMoveFiles(t, []string{origin}, custom)

	for name, dir := range map[string]string{"a third directory": rootWriteDir(t), "the parent": filepath.Dir(origin)} {
		t.Run(name, func(t *testing.T) {
			rootWriteRefused(t, rootWriteUnowned(origin, " with --db "+custom), files,
				"-C", dir, "--db", custom, "lessons", "--apply", "p1")
		})
	}

	out, err := run(t, "-C", origin, "--db", custom, "lessons", "--apply", "p1")
	require.NoError(t, err, "the route of the refusal works")
	assert.Contains(t, out, "applied 1 pin(s)")
}

// TestRootWriteIgnoresAMarkInTheHomeDirectory covers a shared file below
// a home directory with a mark. An earlier run leaves a state directory
// there. The route named --move-root in the home directory, which moved a
// live root to the home directory, and the next pin command passed the
// guard. The file shows no owner, so the route is the recorded root.
func TestRootWriteIgnoresAMarkInTheHomeDirectory(t *testing.T) {
	home := rootWriteDir(t)

	t.Setenv("HOME", home)

	require.NoError(t, os.MkdirAll(filepath.Join(home, store.DefaultDir), 0o755))

	origin := rootWriteRepo(t)
	third := rootMoveThird(t)
	shared := filepath.Join(home, "dbs", "shared.db")

	data, err := os.ReadFile(store.DefaultPath(origin))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(shared), 0o755))
	require.NoError(t, os.WriteFile(shared, data, 0o644))

	rootWriteRefused(t, rootWriteUnowned(origin, " with --db "+shared), rootMoveFiles(t, []string{origin}, shared),
		"-C", third, "--db", shared, "lessons", "--prune", "p2")

	assert.Equal(t, ownership.RouteRunInRootDB, ownership.Decide(ownership.Gather(third, shared).Facts).Route,
		"the route of the console keeps this file")

	_, err = run(t, "-C", origin, "--db", shared, "lessons", "--apply", "p1")
	require.NoError(t, err, "the route of the refusal works")
	assert.Equal(t, origin, rootMoveRecorded(t, shared))

	t.Run("a gone root keeps its move", func(t *testing.T) {
		require.NoError(t, os.Rename(origin, origin+".away"))

		_, err := run(t, "-C", third, "--db", shared, "lessons", "--apply", "p1")
		require.ErrorContains(t, err, "run `seamark index --move-root --db "+shared+"` in "+third+" first")
	})
}

// TestRootWriteRefusesARootThatNoLongerExists covers a recorded root
// that is gone. A route into it cannot run, so the words differ. A write
// would recreate the old path, so the test proves that it stays absent.
func TestRootWriteRefusesARootThatNoLongerExists(t *testing.T) {
	t.Run("a repository that moved without a link", func(t *testing.T) {
		origin := rootWriteRepo(t)
		moved := filepath.Join(rootWriteDir(t), "moved")

		require.NoError(t, os.Rename(origin, moved))

		files := func() map[string]string { return rootWriteFiles(t, []string{moved}) }
		want := "the index of this checkout records a directory that no longer exists (" + origin +
			"), and a write would recreate it; nothing changed; run `seamark index` in " + moved + " first"

		for _, c := range rootWriteCommands {
			rootWriteRefused(t, want, files, append([]string{"-C", moved}, c.args...)...)
		}

		assert.NoDirExists(t, origin)

		_, err := run(t, "-C", moved, "index")
		require.NoError(t, err)

		_, err = run(t, "-C", moved, "lessons", "--apply", "p1")
		require.NoError(t, err, "the route of the refusal works")
		assert.NoDirExists(t, origin)
	})

	t.Run("a custom database outside the workspace", func(t *testing.T) {
		origin := rootWriteRepo(t)
		third := rootWriteDir(t)
		custom := filepath.Join(rootWriteDir(t), "custom.db")

		require.NoError(t, os.Rename(store.DefaultPath(origin), custom))
		require.NoError(t, os.RemoveAll(origin))

		files := func() map[string]string { return rootWriteFiles(t, nil, custom) }
		want := "this database records a directory that no longer exists (" + origin +
			"), and a write would recreate it; nothing changed; run `seamark index --move-root --db " + custom +
			"` in " + third + " first"

		for _, c := range rootWriteCommands {
			rootWriteRefused(t, want, files, append([]string{"-C", third, "--db", custom}, c.args...)...)
		}

		assert.NoDirExists(t, origin)
	})

	// The file moved with the repository. git ignores the file, so the
	// workspace fingerprint stays the same. The route still works, and a
	// plain index run also records this workspace (index_root_move_test).
	t.Run("a custom database inside the moved repository", func(t *testing.T) {
		origin := rootWriteRepo(t)
		moved := filepath.Join(rootWriteDir(t), "moved")
		custom := filepath.Join(moved, "custom.db")

		require.NoError(t, os.Rename(origin, moved))
		require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

		files := func() map[string]string { return rootWriteFiles(t, []string{moved}, custom) }
		want := "this database records a directory that no longer exists (" + origin +
			"), and a write would recreate it; nothing changed; run `seamark index --db " + custom +
			"` in " + moved + " first"

		for _, c := range rootWriteCommands {
			rootWriteRefused(t, want, files, append([]string{"-C", moved, "--db", custom}, c.args...)...)
		}

		_, err := run(t, "-C", moved, "--db", custom, "index")
		require.NoError(t, err)

		out, err := run(t, "-C", moved, "--db", custom, "lessons", "--apply", "p1")
		require.NoError(t, err, "the route of the refusal works")
		assert.Contains(t, out, "applied 1 pin(s)")
		assert.NoDirExists(t, origin)
	})
}

// TestRootWriteFailsClosedBehindALock covers a read before the open that
// a lock stops. The open waits longer, so the store reads the root. A
// locked copy then gets the usual refusal and writes no pin.
func TestRootWriteFailsClosedBehindALock(t *testing.T) {
	t.Parallel()

	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	path := store.DefaultPath(copied)
	before := rootWriteFiles(t, []string{origin})
	wait := rootWriteLock(t, path)

	_, err := run(t, "-C", copied, "lessons", "--apply", "p1")
	require.EqualError(t, err, rootWriteCopied(origin, copied))

	wait()

	assert.Equal(t, before, rootWriteFiles(t, []string{origin}), "the origin stays as it is")
	assert.Equal(t, model.ProposalProposed, rootWriteStatus(t, path, 1), "the ledger of the copy stays")
}

// TestRootWriteWaitsForALockInACheckout covers the same lock in a normal
// checkout. The command waits and writes, as it did before the rule.
func TestRootWriteWaitsForALockInACheckout(t *testing.T) {
	t.Parallel()

	root := rootWriteRepo(t)
	wait := rootWriteLock(t, store.DefaultPath(root))

	out, err := run(t, "-C", root, "lessons", "--apply", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "applied 1 pin(s)")

	wait()

	assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, store.DefaultPath(root), 1))
}

// TestRootWriteFailsClosedWithoutAStoredRoot covers the last read. The
// file can belong to another repository, so the command stops.
func TestRootWriteFailsClosedWithoutAStoredRoot(t *testing.T) {
	path := filepath.Join(rootWriteDir(t), "index.db")

	state := ownership.State{DB: path, ReadErr: errors.New("database is locked")}
	state.Root = ownership.RootUnreadable

	err := pinWriteRefusal(state)

	require.ErrorContains(t, err, "the recorded root of the index cannot be read ("+path+": ")
	assert.ErrorContains(t, err, "so this command could change two repositories; no pin changed; "+
		"run it again, fix the file or use another --db")
}

// TestRootWriteSanitizesTheRecordedRoot covers a root that the database
// supplies. A copied file can hold any text, and an escape sequence in
// an error must not reach the terminal.
func TestRootWriteSanitizesTheRecordedRoot(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)

	writeIndexDB(t, store.DefaultPath(copied), map[string]string{store.MetaRepoRoot: origin + "\x1b[2J"})

	_, err := run(t, "-C", copied, "lessons", "--apply", "p1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "\x1b")
	assert.Contains(t, err.Error(), "records a directory that no longer exists")
}

// TestRootWriteKeepsTheStatesOfOneRepository covers each state where the
// pin file and the ledger lie in one repository. Each command writes
// there, as it did before the rule. pinRoot holds the pin file and ledger
// the database. The setup returns the flags that start the commands.
func TestRootWriteKeepsTheStatesOfOneRepository(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) (flags []string, pinRoot, ledger string)
	}{
		{"a normal checkout", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)

			return []string{"-C", root}, root, store.DefaultPath(root)
		}},
		{"a directory inside a checkout", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)
			inside := filepath.Join(root, "pkg")

			require.NoError(t, os.MkdirAll(inside, 0o755))

			return []string{"-C", inside}, root, store.DefaultPath(root)
		}},
		{"--db of the default database of the recorded root", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)

			return []string{"-C", rootWriteDir(t), "--db", store.DefaultPath(root)}, root, store.DefaultPath(root)
		}},
		{"--db of a root without a default database", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)
			custom := filepath.Join(rootWriteDir(t), "custom.db")

			require.NoError(t, os.Rename(store.DefaultPath(root), custom))

			return []string{"-C", rootWriteDir(t), "--db", custom}, root, custom
		}},
		{"a moved repository with a link left behind", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)
			moved := filepath.Join(rootWriteDir(t), "moved")

			require.NoError(t, os.Rename(root, moved))
			require.NoError(t, os.Symlink(moved, root))

			return []string{"-C", moved}, moved, store.DefaultPath(moved)
		}},
		{"a linked worktree database of its own root", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)
			tree := rootWriteWorktree(t, root)

			require.NoError(t, os.Symlink(store.DefaultPath(root), store.DefaultPath(tree)))

			return []string{"-C", tree}, root, store.DefaultPath(root)
		}},
		{"a worktree with its own index", func(t *testing.T) ([]string, string, string) {
			root := rootWriteRepo(t)
			tree := rootWriteWorktree(t, root)

			_, err := run(t, "-C", tree, "index")
			require.NoError(t, err)

			rootWriteSeed(t, tree)

			return []string{"-C", tree}, tree, store.DefaultPath(tree)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags, pinRoot, ledger := tc.setup(t)

			stdout, stderr, err := runErr(t, append(flags, "lessons", "--apply", "p1")...)
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Equal(t, "applied 1 pin(s) to .seamark/lessons.yaml — review the diff and commit it\n"+
				"  p1    new-rule\n", stdout)

			// p3 has no living evidence, so the plan names it and leaves it.
			stdout, stderr, err = runErr(t, append(flags, "lessons", "--retarget", "p3")...)
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "evidence aged out")

			stdout, stderr, err = runErr(t, append(flags, "lessons", "--prune", "p2")...)
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "pruned 1 pin(s) from .seamark/lessons.yaml")

			lessons, _ := maintainLessons(t, pinRoot)
			assert.Contains(t, lessons, "new-rule")
			assert.NotContains(t, lessons, "retire-me")
			assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, ledger, 1))
			assert.Equal(t, model.ProposalSuperseded, rootWriteStatus(t, ledger, 2))
		})
	}
}

// TestRootWriteKeepsADirectoryInsideARootWithoutGit covers a root outside
// git. Without a top level, -C stays as given, but the command still runs
// in the recorded root. So a custom file of that root keeps working.
func TestRootWriteKeepsADirectoryInsideARootWithoutGit(t *testing.T) {
	root := rootWritePlainRoot(t)
	inside := filepath.Join(root, "pkg")
	custom := filepath.Join(rootWriteDir(t), "custom.db")

	require.NoError(t, os.MkdirAll(inside, 0o755))

	db, err := os.ReadFile(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(custom, db, 0o644))

	for _, c := range rootWriteCommands {
		out, err := run(t, append([]string{"-C", inside, "--db", custom}, c.args...)...)
		require.NoError(t, err, c.name)
		assert.Contains(t, out, c.done, c.name)
	}

	lessons, _ := maintainLessons(t, root)
	assert.Contains(t, lessons, "new-rule")
	assert.NotContains(t, lessons, "retire-me")
	assert.Equal(t, model.ProposalApplied, rootWriteStatus(t, custom, 1))
	assert.Equal(t, model.ProposalProposed, rootWriteStatus(t, store.DefaultPath(root), 1),
		"the default ledger of the root stays")
}

// TestRootWriteRefusesAnotherWorkspaceInsideARootWithoutGit bounds the
// rule above. A directory with its own git top level is another
// repository. The default database of a directory is a copy there.
func TestRootWriteRefusesAnotherWorkspaceInsideARootWithoutGit(t *testing.T) {
	root := rootWritePlainRoot(t)
	custom := filepath.Join(rootWriteDir(t), "custom.db")

	db, err := os.ReadFile(store.DefaultPath(root))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(custom, db, 0o644))

	nested := filepath.Join(root, "nested")

	require.NoError(t, os.MkdirAll(nested, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(nested, "b.go"), []byte("package b\n"), 0o644))
	gitify(t, nested)

	copied := filepath.Join(root, "pkg")

	require.NoError(t, os.MkdirAll(filepath.Dir(store.DefaultPath(copied)), 0o755))
	require.NoError(t, os.WriteFile(store.DefaultPath(copied), db, 0o644))

	files := func() map[string]string { return rootWriteFiles(t, []string{root, copied}, custom) }

	for _, c := range rootWriteCommands {
		t.Run(c.name+" in a nested repository", func(t *testing.T) {
			rootWriteRefused(t, rootWriteUnowned(root, " with --db "+custom), files,
				append([]string{"-C", nested, "--db", custom}, c.args...)...)
		})

		t.Run(c.name+" with a default database inside", func(t *testing.T) {
			rootWriteRefused(t, rootWriteCopied(root, copied), files, append([]string{"-C", copied}, c.args...)...)
		})
	}
}

// rootWriteWorktree adds a linked worktree of root. git checks out the
// tracked pin file and config there, so the worktree holds its own copy.
func rootWriteWorktree(t *testing.T, root string) string {
	t.Helper()

	tree := filepath.Join(rootWriteDir(t), "tree")

	out, err := exec.Command("git", "-C", root, "worktree", "add", "-q", "-b", "side", tree).CombinedOutput()
	require.NoError(t, err, string(out))

	return tree
}

// TestRootWriteKeepsTheRefusalOfAMissingIndex covers a repository with
// no index. The rule has nothing to read, so the open refuses in its
// usual words and creates no database.
func TestRootWriteKeepsTheRefusalOfAMissingIndex(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			_, err := run(t, append([]string{"-C", root}, c.args...)...)
			require.ErrorIs(t, err, errNoIndex)
			assert.NoFileExists(t, store.DefaultPath(root))
		})
	}
}

// TestRootWriteKeepsTheErrorOfABrokenDatabase covers a file that SQLite
// cannot read. The rule cannot read a root from it, and the open reports
// it in the words that every other command prints.
func TestRootWriteKeepsTheErrorOfABrokenDatabase(t *testing.T) {
	root := writeFixture(t)
	path := store.DefaultPath(root)

	writeGarbageDB(t, path)

	want := storeOpenErr(t, path)

	for _, c := range rootWriteCommands {
		t.Run(c.name, func(t *testing.T) {
			_, err := run(t, append([]string{"-C", root}, c.args...)...)
			require.EqualError(t, err, want)
		})
	}
}

// TestRootWriteLeavesTheOtherCommandsOfACopy keeps the commands that this
// rule does not cover. A dismissal writes only this ledger, and a read
// command and a hook write no pin. None of them may start to refuse.
func TestRootWriteLeavesTheOtherCommandsOfACopy(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	before := rootWriteFiles(t, []string{origin})

	out, err := run(t, "-C", copied, "lessons", "--proposals")
	require.NoError(t, err)
	assert.Contains(t, out, "new-rule")

	out, err = run(t, "-C", copied, "lessons", "--dismiss", "p1")
	require.NoError(t, err)
	assert.Contains(t, out, "dismissed 1 proposal(s)")
	assert.Equal(t, model.ProposalDismissed, rootWriteStatus(t, store.DefaultPath(copied), 1))

	event, err := json.Marshal(map[string]any{
		"cwd": copied, "tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(copied, "a.go")},
	})
	require.NoError(t, err)

	for _, args := range [][]string{
		{"lessons", "--hook"},
		{"lessons", "--hook", "--guard"},
		{"lessons", "--hook", "--baseline"},
	} {
		_, _, err := runIn(t, string(event), append([]string{"-C", copied}, args...)...)
		assert.NoError(t, err, "a hook never meets this rule: %v", args)
	}

	assert.Equal(t, before[store.DefaultPath(origin)], rootWriteFiles(t, []string{origin})[store.DefaultPath(origin)],
		"the ledger of the origin stays")
}
