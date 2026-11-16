package index

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/indexhealth"
	"github.com/seamark-dev/seamark/internal/store"
)

// racedFile is the tracked file that the tests edit after the read loop.
const racedFile = "internal/util/util.go"

// racedNote is the log line of a run that saw the workspace change.
const racedNote = "note: the workspace changed during indexing; the next run rebuilds"

// onAfterRead runs fn after the read loop of every Run call of the test.
func onAfterRead(t *testing.T, fn func()) {
	t.Helper()

	afterRead = fn

	t.Cleanup(func() { afterRead = nil })
}

// racedRepo commits the Go fixture into a new repository.
func racedRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	writeFixture(t, root)
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "initial")

	return root
}

// addRacedFunc appends the function Raced to racedFile.
func addRacedFunc(t *testing.T, root string) {
	t.Helper()

	p := filepath.Join(root, racedFile)
	src, err := os.ReadFile(p)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, append(src, []byte("\nfunc Raced() {}\n")...), 0o644))
}

// racedSymbols returns the indexed symbols named Raced.
func racedSymbols(t *testing.T, dbPath string) []string {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	syms, err := st.FindSymbols("Raced", 5)
	require.NoError(t, err)

	var fqns []string

	for _, s := range syms {
		if s.Name == "Raced" {
			fqns = append(fqns, s.FQN)
		}
	}

	return fqns
}

// TestIndexEditDuringRunRebuildsNextRun proves that Run never records
// the workspace state from after its reads. An edit after the read loop
// adds a function that the run did not parse. The next run must not skip,
// because the record does not match the workspace, and it indexes the
// function. Before the fix Run recorded the state after the edit, so
// every later run skipped and the function stayed unindexed.
func TestIndexEditDuringRunRebuildsNextRun(t *testing.T) {
	root := racedRepo(t)

	fired := false

	onAfterRead(t, func() {
		if fired {
			return
		}

		fired = true

		addRacedFunc(t, root)
	})

	var logs []string

	first, err := Run(Options{Root: root, Logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}})
	require.NoError(t, err)
	require.True(t, fired, "the seam ran")
	require.False(t, first.Skipped)
	assert.Empty(t, racedSymbols(t, first.DBPath), "the run read the file before the edit")
	assert.Contains(t, logs, racedNote)

	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, second.Skipped, "an edit during the run must make the next run rebuild")
	assert.Equal(t, []string{"internal/util.Raced"}, racedSymbols(t, first.DBPath))

	third, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, third.Skipped, "an unchanged workspace skips again")
}

// TestIndexOlderRunCommittingLastRebuildsNextRun covers two overlapping
// runs, as the MCP server and the CLI can start. The outer run reads the
// file before the edit. The inner run starts after the edit and commits
// first. The outer run commits last, so the stored graph lacks the new
// function, and the next run must rebuild. Before the fix the outer run
// recorded the state after the edit, which marked its older graph current.
func TestIndexOlderRunCommittingLastRebuildsNextRun(t *testing.T) {
	root := racedRepo(t)

	fired := false

	onAfterRead(t, func() {
		if fired {
			return
		}

		fired = true

		addRacedFunc(t, root)

		inner, err := Run(Options{Root: root})
		require.NoError(t, err)
		require.False(t, inner.Skipped)
		require.Equal(t, []string{"internal/util.Raced"}, racedSymbols(t, inner.DBPath))
	})

	outer, err := Run(Options{Root: root})
	require.NoError(t, err)
	require.True(t, fired, "the seam ran")
	assert.Empty(t, racedSymbols(t, outer.DBPath), "the outer graph replaced the inner one")

	next, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, next.Skipped, "an older graph must not read as current")
	assert.Equal(t, []string{"internal/util.Raced"}, racedSymbols(t, outer.DBPath))
}

// racedHealth reads the index health as status reads it: with the
// workspace fingerprint that leaves out the database file it reads.
func racedHealth(t *testing.T, root, dbPath string) indexhealth.Health {
	t.Helper()

	st, err := store.OpenReadOnly(dbPath)
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	health, err := indexhealth.Read(st, root, func(root string) string { return WorkspaceState(root, st.Path()) },
		OtherVersion)
	require.NoError(t, err)

	return health
}

// TestIndexEditRevertedAfterRunRebuildsNextRun covers an edit that lands
// after the run observes the workspace and before the run reads the file.
// The user takes the edit back after the run, as with git stash and git
// stash pop. The run parses the edit, so its graph holds content that no
// file holds after the revert, although the workspace state equals the
// first observation again. The run must leave a record that every reader
// calls stale, so the next run rebuilds and drops the edit.
func TestIndexEditRevertedAfterRunRebuildsNextRun(t *testing.T) {
	root := racedRepo(t)

	p := filepath.Join(root, racedFile)
	orig, err := os.ReadFile(p)
	require.NoError(t, err)

	// The scan phase comes after the first observation and before the
	// read loop, so the run reads the edited file.
	edited := false

	var logs []string

	first, err := Run(Options{
		Root: root,
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		Progress: func(phase string, _, _ int) {
			if phase != "scan" || edited {
				return
			}

			edited = true

			addRacedFunc(t, root)
		},
	})
	require.NoError(t, err)
	require.True(t, edited, "the edit ran during the run")
	require.False(t, first.Skipped)
	assert.Equal(t, []string{"internal/util.Raced"}, racedSymbols(t, first.DBPath), "the run read the edited file")
	assert.Contains(t, logs, racedNote)

	require.NoError(t, os.WriteFile(p, orig, 0o644))

	health := racedHealth(t, first.Root, first.DBPath)
	assert.Equal(t, indexhealth.FreshStale, health.Freshness, "the graph holds an edit that no file holds")
	assert.Equal(t, indexhealth.StaleWorkspaceChanged, health.StaleReason)

	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.False(t, second.Skipped, "a run that saw the workspace change must make the next run rebuild")
	assert.Empty(t, racedSymbols(t, first.DBPath), "the rebuild drops the reverted edit")

	third, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, third.Skipped, "an unchanged workspace skips again")
	assert.Equal(t, indexhealth.FreshCurrent, racedHealth(t, first.Root, first.DBPath).Freshness)
}

// TestIndexUnchangedRunRecordsItsState covers a run that sees no change
// during its reads. It records the workspace state and logs no note, the
// health reader calls the index current, and the next run skips.
func TestIndexUnchangedRunRecordsItsState(t *testing.T) {
	root := racedRepo(t)

	var logs []string

	first, err := Run(Options{Root: root, Logf: func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}})
	require.NoError(t, err)
	require.False(t, first.Skipped)
	assert.NotContains(t, logs, racedNote)

	st, err := store.OpenReadOnly(first.DBPath)
	require.NoError(t, err)

	stored, err := st.GetMeta(store.MetaIndexedState)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	assert.Equal(t, WorkspaceState(first.Root, first.DBPath), stored)
	assert.Equal(t, indexhealth.FreshCurrent, racedHealth(t, first.Root, first.DBPath).Freshness)

	second, err := Run(Options{Root: root})
	require.NoError(t, err)
	assert.True(t, second.Skipped, "an unchanged workspace skips")
}

// TestChangedDuringRunMatchesNoWorkspaceState proves that the mark of a
// run that saw the workspace change reads as stale against every current
// state. WorkspaceState gives hex digits or "", and the mark holds other
// letters.
func TestChangedDuringRunMatchesNoWorkspaceState(t *testing.T) {
	state := WorkspaceState(racedRepo(t), "")
	require.NotEmpty(t, state)

	_, err := hex.DecodeString(state)
	require.NoError(t, err, "a workspace state is a hex string")

	_, err = hex.DecodeString(changedDuringRun)
	require.Error(t, err, "the mark is no hex string")

	assert.Equal(t, indexhealth.FreshStale, indexhealth.Freshness(changedDuringRun, state))
	assert.Equal(t, indexhealth.FreshStale, indexhealth.Freshness(changedDuringRun, ""),
		"a workspace without git does not make the mark current")
}

// TestIndexOwnDatabaseInTheWorkspaceKeepsTheSkip covers a database that
// is an untracked file inside the workspace, as with --db ./custom.db.
// The fingerprint hashed its size and mtime, and every run writes it, so
// every run rebuilt and logged that the workspace changed during
// indexing, an edit that the user never made. The fingerprint leaves the
// file and its sidecars out now: the run logs no such note, the second
// run skips, and a reader that passes the file it reads computes the
// stored fingerprint. A real edit beside the database still rebuilds.
func TestIndexOwnDatabaseInTheWorkspaceKeepsTheSkip(t *testing.T) {
	cases := map[string]struct {
		rel   string // the database, relative to the root
		other string // an untracked file beside it that the fingerprint keeps, or ""
	}{
		"a file at the root":                     {rel: "custom.db"},
		"a directory that the first run creates": {rel: "state/custom.db"},
		"a file beside an untracked file":        {rel: "notes/custom.db", other: "notes/todo.txt"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			// The fingerprint hashes the absolute path of an untracked file,
			// so the test uses the resolved root, as Run does.
			root, err := filepath.EvalSymlinks(racedRepo(t))
			require.NoError(t, err)

			db := filepath.Join(root, filepath.FromSlash(c.rel))

			if c.other != "" {
				require.NoError(t, os.MkdirAll(filepath.Dir(db), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(c.other)), []byte("todo\n"), 0o644))
			}

			var logs []string

			logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }

			first, err := Run(Options{Root: root, DBPath: db, Logf: logf})
			require.NoError(t, err)
			require.False(t, first.Skipped)

			second, err := Run(Options{Root: root, DBPath: db, Logf: logf})
			require.NoError(t, err)
			assert.True(t, second.Skipped, "the run's own database is no change of the workspace")
			assert.NotContains(t, logs, racedNote)

			st, err := store.OpenReadOnly(db)
			require.NoError(t, err)

			stored, err := st.GetMeta(store.MetaIndexedState)
			require.NoError(t, err)
			assert.Equal(t, stored, WorkspaceState(root, st.Path()), "a reader computes the stored fingerprint")
			assert.NotEqual(t, stored, WorkspaceState(root, ""), "the database is in the git listing")
			require.NoError(t, st.Close())

			if c.other != "" {
				// The mtime moves, so the file beside the database changes.
				later := time.Now().Add(time.Hour)
				require.NoError(t, os.Chtimes(filepath.Join(root, filepath.FromSlash(c.other)), later, later))

				third, err := Run(Options{Root: root, DBPath: db})
				require.NoError(t, err)
				assert.False(t, third.Skipped, "an untracked file beside the database still counts")
			}

			addRacedFunc(t, root)

			edited, err := Run(Options{Root: root, DBPath: db, Logf: logf})
			require.NoError(t, err)
			assert.False(t, edited.Skipped, "an edit still rebuilds")
			assert.Equal(t, []string{"internal/util.Raced"}, racedSymbols(t, db))
			assert.NotContains(t, logs, racedNote)
		})
	}
}

// TestWorkspaceStateKeepsOtherDatabases bounds the rule. The default
// database lies under .seamark, which the fingerprint leaves out already,
// and a database outside the workspace is not in the git listing. Both
// keep the fingerprint of a call that names no database.
func TestWorkspaceStateKeepsOtherDatabases(t *testing.T) {
	root, err := filepath.EvalSymlinks(racedRepo(t))
	require.NoError(t, err)

	sum, err := Run(Options{Root: root})
	require.NoError(t, err)
	require.Equal(t, store.DefaultPath(root), sum.DBPath)

	outside := filepath.Join(t.TempDir(), "outside.db")
	_, err = Run(Options{Root: root, DBPath: outside})
	require.NoError(t, err)

	plain := WorkspaceState(root, "")
	require.NotEmpty(t, plain)
	assert.Equal(t, plain, WorkspaceState(root, store.DefaultPath(root)))
	assert.Equal(t, plain, WorkspaceState(root, outside))
	assert.Nil(t, ownFiles(root, store.DefaultPath(root)))
	assert.Nil(t, ownFiles(root, outside))
}
