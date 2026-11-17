package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// otherVersionNote is the note of a read command on an index that another
// seamark version built, in a workspace that can rebuild it.
const otherVersionNote = "note: this index was built by another seamark version — run `seamark index` to rebuild it\n"

// ageParseCache records the parse cache version "2" in the database at
// dbPath, as the previous binary left it. The graph and the fingerprint
// stay as they are, so only the version can make a reader say stale.
func ageParseCache(t *testing.T, dbPath string) {
	t.Helper()

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaParseCacheVersion, "2"))
	require.NoError(t, st.Close())
}

// TestReadersCallAnIndexOfAnotherVersionStale proves that the CLI readers,
// which never rebuild, do not call an index of another seamark version
// current in an unchanged workspace. status (text and JSON), orient (the
// health line and the note), why, and report (the note and the page) say
// stale with the reason and the rebuild. After an index run every surface
// reads current again, and why keeps its bytes.
func TestReadersCallAnIndexOfAnotherVersionStale(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	whyBefore, whyNoteBefore, err := runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)

	ageParseCache(t, store.DefaultPath(root))

	status, err := run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(status, "workspace      STALE — this index was built by another seamark version; "+
		"run `seamark index` (schema v"), status)

	doc := statusJSONKeys(t, "-C", root)
	assert.Equal(t, "stale", doc["freshness"], "the documented value stays")
	assert.Equal(t, "other-version", doc["stale_reason"])

	orient, stderr, err := runErr(t, "-C", root, "orient")
	require.NoError(t, err)
	assert.Contains(t, orient, " · freshness stale (this index was built by another seamark version)\n")
	assert.Equal(t, otherVersionNote, stderr)

	_, stderr, err = runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.Equal(t, otherVersionNote, stderr)

	page, stderr, err := runErr(t, "-C", root, "report", "-o", "-")
	require.NoError(t, err)
	assert.Equal(t, otherVersionNote, stderr)
	assert.Contains(t, page, "This index was built by another seamark version")

	_, err = run(t, "-C", root, "index")
	require.NoError(t, err)

	status, err = run(t, "-C", root, "status")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(status, "workspace      current (schema v"), status)

	doc = statusJSONKeys(t, "-C", root)
	assert.Equal(t, "current", doc["freshness"])
	assert.NotContains(t, doc, "stale_reason", "a current index keeps its JSON")

	orient, stderr, err = runErr(t, "-C", root, "orient")
	require.NoError(t, err)
	assert.Contains(t, orient, " · freshness current\n")
	assert.Empty(t, stderr)

	whyAfter, whyNoteAfter, err := runErr(t, "-C", root, "why", "helper")
	require.NoError(t, err)
	assert.Equal(t, whyBefore, whyAfter)
	assert.Equal(t, whyNoteBefore, whyNoteAfter)
	assert.Empty(t, whyNoteAfter)

	page, stderr, err = runErr(t, "-C", root, "report", "-o", "-")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.NotContains(t, page, "class=\"stale\"")
}

// TestOtherVersionKeepsTheRouteOfTheDatabase proves that the version
// reason keeps the route of a database that records another repository:
// a plain index run in the copy would be refused, so the status line and
// the note name the run in the recorded root.
func TestOtherVersionKeepsTheRouteOfTheDatabase(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	flags := []string{"-C", copied, "--db", store.DefaultPath(origin)}

	ageParseCache(t, store.DefaultPath(origin))

	out, err := run(t, append(flags, "status")...)
	require.NoError(t, err)
	assert.Contains(t, out, "\nworkspace      STALE — this index was built by another seamark version; "+
		"run `seamark index` in "+origin+" (schema v", out)

	_, stderr, err := runErr(t, append(flags, "why", "main")...)
	require.NoError(t, err)
	assert.Contains(t, stderr, "note: this index was built by another seamark version — run `seamark index` in "+origin+"\n")
	assert.NotContains(t, stderr, "to rebuild it")
}
