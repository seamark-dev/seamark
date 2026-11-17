package mcp

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// newerAdvice is the line that MCP adds to the refusal of a newer
// database. The test server runs version "test".
const newerAdvice = "\nThis MCP server runs seamark test until it restarts. If a newer seamark is installed, " +
	"restart the agent or reconnect its MCP server; otherwise upgrade seamark first."

// stampSchema writes version into the meta table of the database at path
// with plain SQL. store.Open refuses a newer stamp, so it cannot write one.
func stampSchema(t *testing.T, path string, version int) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	_, err = db.Exec(`UPDATE meta SET value = ? WHERE key = ?`, strconv.Itoa(version), store.MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())
}

// readSchema reads the schema stamp of the database at path.
func readSchema(t *testing.T, path string) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)

	defer func() { _ = db.Close() }()

	var v string
	require.NoError(t, db.QueryRow(`SELECT value FROM meta WHERE key = ?`, store.MetaSchemaVersion).Scan(&v))

	return v
}

func TestToolsAdviseARestartForANewerSchema(t *testing.T) {
	root := writeFixture(t)
	dbPath := filepath.Join(root, ".seamark", "index.db")

	serve(t, root, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"orient","arguments":{}}}`)

	newer := store.SupportedSchema() + 1
	stampSchema(t, dbPath, newer)

	refusal := (&store.NewerSchemaError{Stored: newer, Supported: store.SupportedSchema()}).Error()

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"orient","arguments":{}}}`,
		changeSetCall(t, 3, "a.go"),
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"expand","arguments":{"ref":"helper"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"check","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/read","params":{"uri":"seamark://status"}}`,
	)

	for _, id := range []string{"1", "2", "3", "4", "5"} {
		text, isErr := toolText(t, resps[id])
		assert.True(t, isErr, "call %s", id)
		assert.Contains(t, text, refusal, "call %s keeps the store text", id)
		assert.Contains(t, text, refusal+newerAdvice, "call %s ends with the advice", id)
	}

	require.NotNil(t, resps["6"].Error)
	assert.Contains(t, resps["6"].Error.Message, refusal+newerAdvice)

	assert.Equal(t, strconv.Itoa(newer), readSchema(t, dbPath), "the database keeps its stamp")
}
