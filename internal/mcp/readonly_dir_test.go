package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// readOnlyDirRefusal is the refusal for an index whose directory refuses
// writes. The CLI test of the same state builds the same literal, so the
// two surfaces keep one text.
func readOnlyDirRefusal(db string) string {
	return "the index database cannot be read, because its directory refuses writes (" + db +
		": attempt to write a readonly database (1544)), and SQLite must create its WAL/SHM sidecars there " +
		"to read it; nothing changed; make " + filepath.Dir(db) + " writable or use another --db"
}

// TestToolsNameADirectoryThatRefusesWrites covers an index in a .seamark
// directory with mode 0555 and no sidecars. SQLite cannot read it, so each
// tool refuses with the text of the CLI and writes nothing.
func TestToolsNameADirectoryThatRefusesWrites(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a directory with mode 0555")
	}

	root, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: root})
	require.NoError(t, err)

	db := store.DefaultPath(root)
	dir := filepath.Dir(db)

	require.NoFileExists(t, db+"-wal", "a normal close removes the sidecars")

	before, err := os.ReadFile(db)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	resps := serveDB(t, root, db, t.Logf,
		rootMoveCall(1, "why", `{"query":"helper"}`),
		rootMoveCall(2, "check", `{"diff":"diff --git a/a.go b/a.go\n"}`),
	)

	for _, id := range []string{"1", "2"} {
		text, isError := rootMoveText(t, resps[id])

		assert.True(t, isError, "tool %s refuses", id)
		assert.Equal(t, readOnlyDirRefusal(db), text, "tool %s", id)
	}

	// The CLI index command prints the error of index.Run. The same text
	// here keeps the two surfaces in one form.
	_, err = index.Run(index.Options{Root: root})
	require.EqualError(t, err, readOnlyDirRefusal(db), "the CLI build gives the same text")

	after, err := os.ReadFile(db)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a tool call writes nothing to this database")
	assert.NoFileExists(t, db+"-wal")
}
