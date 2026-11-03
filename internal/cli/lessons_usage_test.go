package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/mcpusage"
	"github.com/seamark-dev/seamark/internal/store"
)

// TestLessonsStatsToolUsage runs the command once without a usage log,
// and each later run must keep those bytes in front of its own section.
func TestLessonsStatsToolUsage(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fix\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))

	_, err := run(t, "-C", root, "init")
	require.NoError(t, err)

	_, err = run(t, "-C", root, "index")
	require.NoError(t, err)

	base, err := run(t, "-C", root, "lessons", "--stats")
	require.NoError(t, err)
	assert.NotContains(t, base, "mcp tool usage", "no file prints no section")

	path := mcpusage.Path(root)

	t.Run("lines print a section after the existing output", func(t *testing.T) {
		body := `{"v":1,"at":"2026-11-03T10:00:00Z","tool":"why","client":"claude-code","ms":12,"bytes":300,"ok":true}` + "\n" +
			`{"v":1,"at":"2026-11-03T10:00:01Z","tool":"why","client":"claude-code","ms":30,"bytes":500,"ok":false}` + "\n" +
			"damaged\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

		t.Cleanup(func() { _ = os.Remove(path) })

		out, err := run(t, "-C", root, "lessons", "--stats")
		require.NoError(t, err)
		assert.Equal(t, base+`
mcp tool usage — calls: 2, failed: 1 (counts calls, not whether a call helped)
  why          claude-code          calls: 2, failed: 1; latency median 21 ms, largest 30 ms; reply median 400 bytes
  lines skipped, because they do not parse: 1
`, out)
	})

	t.Run("an unreadable file reads as unknown and keeps the output", func(t *testing.T) {
		require.NoError(t, os.Mkdir(path, 0o755))

		t.Cleanup(func() { _ = os.Remove(path) })

		out, err := run(t, "-C", root, "lessons", "--stats")
		require.NoError(t, err)
		assert.Contains(t, out, base+"\nmcp tool usage — unknown: read ")
		assert.NotContains(t, out, "calls: 0")
	})
}

// usageRepo builds a git repository with an index, so `lessons --stats`
// runs and the index records the root of the repository.
func usageRepo(t *testing.T) string {
	t.Helper()

	dir := rootWriteDir(t)
	root := filepath.Join(dir, "repo")

	require.NoError(t, os.Mkdir(root, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/fix\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc main() {}\n"), 0o644))
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	return root
}

// usageServe runs `seamark mcp` with flags for one session of client. The
// real command writes the log, so a test holds the root that the server
// uses, and not a copy of that rule.
func usageServe(t *testing.T, client string, calls int, flags ...string) {
	t.Helper()

	requests := []string{`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26",` +
		`"capabilities":{},"clientInfo":{"name":"` + client + `"}}}`}

	for id := 1; id <= calls; id++ {
		requests = append(requests, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call",`+
			`"params":{"name":"why","arguments":{"query":"main"}}}`, id))
	}

	_, stderr, err := runIn(t, strings.Join(requests, "\n")+"\n", append(flags, "mcp")...)
	require.NoError(t, err, stderr)
}

// TestLessonsStatsReadsTheLogOfTheServer holds one property. With the
// flags of `seamark mcp`, `lessons --stats` reads the file that the server
// writes. The index can record another root, so the recorded root can name
// the file of another checkout, or no file.
func TestLessonsStatsReadsTheLogOfTheServer(t *testing.T) {
	t.Run("a linked worktree reads its own log", func(t *testing.T) {
		origin := usageRepo(t)
		tree := rootWriteWorktree(t, origin)

		// The server of this worktree refuses the root move on each call,
		// so the database keeps the recorded root of origin.
		require.NoError(t, os.MkdirAll(filepath.Dir(store.DefaultPath(tree)), 0o755))
		require.NoError(t, os.Symlink(store.DefaultPath(origin), store.DefaultPath(tree)))

		usageServe(t, "origin-client", 2, "-C", origin)

		out, err := run(t, "-C", tree, "lessons", "--stats")
		require.NoError(t, err)
		assert.NotContains(t, out, "mcp tool usage", "the calls of another checkout are not the calls of this one")

		usageServe(t, "tree-client", 3, "-C", tree)

		out, err = run(t, "-C", tree, "lessons", "--stats")
		require.NoError(t, err)
		assert.Contains(t, out, "mcp tool usage — calls: 3, failed: 0")
		assert.Contains(t, out, "tree-client")
		assert.NotContains(t, out, "origin-client")
	})

	t.Run("a moved checkout reads the lines that it holds", func(t *testing.T) {
		root := usageRepo(t)

		usageServe(t, "moved-client", 2, "-C", root)

		// No command runs in the new place before the read, so the index
		// still records the directory that no longer exists.
		moved := filepath.Join(rootWriteDir(t), "moved")
		require.NoError(t, os.Rename(root, moved))

		out, err := run(t, "-C", moved, "lessons", "--stats")
		require.NoError(t, err)
		assert.Contains(t, out, "mcp tool usage — calls: 2, failed: 0")
		assert.Contains(t, out, "moved-client")
	})

	// The server widens -C to the git top level also with --db, and
	// resolveWorkspace does not. So this case fails on that resolver.
	t.Run("a subdirectory with a database flag reads the log at the top level", func(t *testing.T) {
		root := usageRepo(t)
		sub := filepath.Join(root, "sub")
		db := filepath.Join(rootWriteDir(t), "index.db")

		require.NoError(t, os.Mkdir(sub, 0o755))

		_, err := run(t, "-C", root, "--db", db, "index")
		require.NoError(t, err)

		usageServe(t, "sub-client", 1, "-C", sub, "--db", db)
		require.FileExists(t, mcpusage.Path(root))

		out, err := run(t, "-C", sub, "--db", db, "lessons", "--stats")
		require.NoError(t, err)
		assert.Contains(t, out, "mcp tool usage — calls: 1, failed: 0")
		assert.Contains(t, out, "sub-client")
	})
}
