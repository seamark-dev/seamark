package mcp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orientText calls the orient tool once and returns its text.
func orientText(t *testing.T, resps map[string]rpcResp) string {
	t.Helper()

	text, isError := rootMoveText(t, resps["1"])
	require.False(t, isError, text)

	return text
}

// TestOrientHealthHeaderAfterARefresh covers the freshness word of the
// orient header in the three refresh states of the server. A refresh
// note, when one exists, still opens the text once, and the header
// follows the title.
func TestOrientHealthHeaderAfterARefresh(t *testing.T) {
	t.Run("a refresh that succeeds", func(t *testing.T) {
		root := changeSetPrepFixture(t)

		text := orientText(t, serve(t, root, rootMoveCall(1, "orient", `{}`)))

		assert.True(t, strings.HasPrefix(text, filepath.Base(root)+" — orientation\n"), text)

		// This test builds the index, so its age is a count of minutes.
		// A tool without the real clock prints "(age unknown)".
		assert.Regexp(t, `\n  health indexed \d{4}-\d\d-\d\d \d\d:\d\d UTC \(\d+m ago\) · freshness current\n`, text)
		assert.NotContains(t, text, "note: seamark")
	})

	t.Run("a refresh that fails after an edit", func(t *testing.T) {
		root := changeSetPrepFixture(t)

		// The first call builds the index. The broken configuration then
		// stops each later refresh, and the edit makes that index stale.
		orientText(t, serve(t, root, rootMoveCall(1, "orient", `{}`)))
		breakRefresh(t, root)
		fixtureWrite(t, root, "store.go", "package main\n\n// save is gone.\n")

		text := orientText(t, serve(t, root, rootMoveCall(1, "orient", `{}`)))

		require.True(t, strings.HasPrefix(text, brokenNote), text)
		assert.Equal(t, 1, strings.Count(text, brokenNote))

		body := strings.TrimPrefix(text, brokenNote)
		assert.True(t, strings.HasPrefix(body, filepath.Base(root)+" — orientation\n"), text)
		assert.Contains(t, body, " · freshness stale (the workspace changed since this index)\n")
	})

	t.Run("a refused move of another root", func(t *testing.T) {
		_, other, dbPath := rootMoveFixture(t)

		text := orientText(t, serveDB(t, other, dbPath, t.Logf, rootMoveCall(1, "orient", `{}`)))

		require.True(t, strings.HasPrefix(text, staleNote), text)
		assert.Equal(t, 1, strings.Count(text, staleNote))

		lines := strings.Split(strings.TrimPrefix(text, staleNote), "\n")
		require.GreaterOrEqual(t, len(lines), 3, text)
		assert.Equal(t, filepath.Base(other)+" — orientation", lines[0], "the title names the workspace of the server")
		assert.True(t, strings.HasPrefix(lines[1], "  index  "), text)
		assert.True(t, strings.HasPrefix(lines[2], "  health indexed "), text)
		assert.Contains(t, lines[2], " · freshness stale (", "the workspace has another fingerprint")
	})
}
