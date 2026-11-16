package index

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// TestIndexEditDuringRunRebuildsNextRun proves that Run records the
// workspace state it observed before its reads. An edit after the read
// loop adds a function that the run did not parse. The next run must not
// skip, because the recorded state is older than the workspace, and it
// indexes the function. Before the fix Run recorded the state after the
// edit, so every later run skipped and the function stayed unindexed.
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
