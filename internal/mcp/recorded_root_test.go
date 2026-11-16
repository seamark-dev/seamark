package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// resourceRead is one resources/read request.
func resourceRead(id int, uri string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/read","params":{"uri":%q}}`, id, uri)
}

// resourceText returns the text of a resources/read result.
func resourceText(t *testing.T, r rpcResp) string {
	t.Helper()

	require.Nil(t, r.Error, "expected a resource, got a protocol error")

	var res struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}

	require.NoError(t, json.Unmarshal(r.Result, &res))
	require.Len(t, res.Contents, 1)

	return res.Contents[0].Text
}

// rootLines returns the root lines of a status text.
func rootLines(text string) []string {
	var lines []string

	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "root ") {
			lines = append(lines, line)
		}
	}

	return lines
}

// foreignRootLine is the root line of seamark status for a workspace
// that reads the database of origin, which origin owns. A route into
// origin would print the same report again, so the line has none.
func foreignRootLine(origin string) string {
	return "root           this database records another repository (" + origin + "), so this report describes " +
		origin + ", not this workspace"
}

// foreignReads are the read requests of one session: both resources and
// every read tool. The ids map to the names in foreignNames.
func foreignReads(t *testing.T) []string {
	t.Helper()

	return []string{
		initializeLine("2025-06-18"),
		resourceRead(1, statusURI),
		resourceRead(2, orientURI),
		rootMoveCall(3, "why", `{"query":"helper"}`),
		rootMoveCall(4, "orient", `{}`),
		rootMoveCall(5, "expand", `{"ref":"helper"}`),
		changeSetCall(t, 6, "a.go"),
		rootMoveCall(7, "check", `{}`),
	}
}

// foreignTexts returns the text of each read of foreignReads by name.
func foreignTexts(t *testing.T, resps map[string]rpcResp) map[string]string {
	t.Helper()

	texts := map[string]string{
		"status resource": resourceText(t, resps["1"]),
		"orient resource": resourceText(t, resps["2"]),
	}

	for id, name := range map[string]string{"3": "why", "4": "orient", "5": "expand", "6": "change_set"} {
		text, isError := rootMoveText(t, resps[id])
		require.False(t, isError, "%s answers from the existing index: %s", name, text)

		texts[name] = text
	}

	return texts
}

// TestReadsNameTheRootOfAnotherDatabase covers a server whose --db
// records another repository, in the two states of the refresh. Each
// read opens once with a note that names that repository, the status
// resource prints the root line of seamark status once, and orient names
// that repository in its title. A current index keeps the verdict of
// check, and the verdict carries the note. A refused refresh keeps the
// refusal of check. No call writes to the database of the origin.
func TestReadsNameTheRootOfAnotherDatabase(t *testing.T) {
	t.Run("a matching fingerprint", func(t *testing.T) {
		origin, tree := rootMoveLinked(t, false)
		dbPath := store.DefaultPath(origin)

		require.Equal(t, index.WorkspaceState(origin, ""), index.WorkspaceState(tree, ""), "the skip needs one fingerprint")

		before, err := os.ReadFile(dbPath)
		require.NoError(t, err)

		resps := serveDB(t, tree, dbPath, t.Logf, foreignReads(t)...)
		note := matchText(origin)

		for name, text := range foreignTexts(t, resps) {
			assert.True(t, strings.HasPrefix(text, note), "%s opens with the note: %s", name, text)
			assert.Equal(t, 1, strings.Count(text, note), "%s names it once", name)
			assert.NotContains(t, text, "did not refresh", "%s: a skip is current", name)
		}

		status := resourceText(t, resps["1"])
		assert.Equal(t, []string{foreignRootLine(origin)}, rootLines(status))
		assert.True(t, strings.HasPrefix(status, note+foreignRootLine(origin)+"\nworkspace      current "), status)

		orient, _ := rootMoveText(t, resps["4"])
		assert.True(t, strings.HasPrefix(orient, note+filepath.Base(origin)+" — orientation\n"), orient)

		res := decodeToolParts(t, resps["6"])
		assert.Equal(t, res.Content[0].Text, structuredReport(t, res), "the key carries the note")

		check, isError := rootMoveText(t, resps["7"])
		assert.False(t, isError, check)
		assert.True(t, strings.HasPrefix(check, note), check)
		assert.Equal(t, 1, strings.Count(check, note), check)
		assert.Contains(t, check, "verdict")

		after, err := os.ReadFile(dbPath)
		require.NoError(t, err)
		assert.Equal(t, before, after, "no call writes to the database of the origin")
	})

	t.Run("a differing fingerprint", func(t *testing.T) {
		origin, other, dbPath := rootMoveFixture(t)

		before, err := os.ReadFile(dbPath)
		require.NoError(t, err)

		resps := serveDB(t, other, dbPath, t.Logf, foreignReads(t)...)
		note := refusedText(origin)

		for name, text := range foreignTexts(t, resps) {
			assert.True(t, strings.HasPrefix(text, note), "%s opens with the note: %s", name, text)
			assert.Equal(t, 1, strings.Count(text, note), "%s names it once", name)
			assert.NotContains(t, text, staleNote, name)
		}

		// A plain index run is refused here, so the stale line names the
		// route of this state.
		status := resourceText(t, resps["1"])
		assert.Equal(t, []string{foreignRootLine(origin)}, rootLines(status))
		assert.True(t, strings.HasPrefix(status, note+foreignRootLine(origin)+
			"\nworkspace      STALE — run `seamark index` in "+origin+" (schema v"), status)

		res := decodeToolParts(t, resps["6"])
		assert.Equal(t, res.Content[0].Text, structuredReport(t, res), "the key carries the note")

		check, isError := rootMoveText(t, resps["7"])
		assert.True(t, isError, "a verdict needs a current graph: %s", check)
		assert.Contains(t, check, "this database records another repository ("+origin+")")
		assert.NotContains(t, check, "verdict")

		after, err := os.ReadFile(dbPath)
		require.NoError(t, err)
		assert.Equal(t, before, after, "no call writes to the database of the origin")
	})
}

// TestStatusResourceKeepsItsOwnDatabaseText bounds the rule. A server on
// its own database prints no root line and no new note, and a stale index
// still names the plain index run, byte for byte.
func TestStatusResourceKeepsItsOwnDatabaseText(t *testing.T) {
	root := changeSetPrepFixture(t)

	current := resourceText(t, serve(t, root, resourceRead(1, statusURI))["1"])
	assert.True(t, strings.HasPrefix(current, "workspace      current (schema v"), current)
	assert.Empty(t, rootLines(current))

	// The broken configuration stops the refresh, so the edit leaves the
	// index stale.
	breakRefresh(t, root)
	fixtureWrite(t, root, "store.go", "package main\n\n// save is gone.\n")

	stale := resourceText(t, serve(t, root, resourceRead(1, statusURI))["1"])
	assert.True(t, strings.HasPrefix(stale, brokenNote+"workspace      STALE — run `seamark index` (schema v"), stale)
	assert.Empty(t, rootLines(stale))
}
