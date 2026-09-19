package approve

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanClaudeMCPRegistersInAnAbsentFile(t *testing.T) {
	p, err := PlanClaudeMCP(nil, false)
	require.NoError(t, err)

	assert.True(t, p.Register)
	assert.Equal(t, ClaudeServer, p.Server)

	doc, err := p.Document()
	require.NoError(t, err)
	assert.JSONEq(t, `{"mcpServers":{"seamark":{"command":"seamark","args":["mcp"]}}}`, string(doc))
	assert.Equal(t, byte('\n'), doc[len(doc)-1])

	// The shared file holds the bare command, never one machine's path.
	assert.NotContains(t, string(doc), "/")

	// Its own output is complete: a second plan adds nothing.
	again, err := PlanClaudeMCP(doc, true)
	require.NoError(t, err)
	assert.True(t, again.Registered)
	assert.False(t, again.Register)

	none, err := again.Document()
	require.NoError(t, err)
	assert.Nil(t, none)
}

func TestPlanClaudeMCPPreservesForeignContent(t *testing.T) {
	existing := []byte(`{
  "note": "a <b> & c",
  "mcpServers": {
    "db": {"command": "db-mcp", "env": {"PORT": 9007199254740993}}
  }
}`)

	p, err := PlanClaudeMCP(existing, true)
	require.NoError(t, err)
	require.True(t, p.Register)

	doc, err := p.Document()
	require.NoError(t, err)

	assert.Contains(t, string(doc), `"a <b> & c"`, "HTML characters keep their spelling")
	assert.Contains(t, string(doc), "9007199254740993", "a large integer stays exact")

	var parsed struct {
		Servers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}

	require.NoError(t, json.Unmarshal(doc, &parsed))
	assert.Equal(t, "db-mcp", parsed.Servers["db"].Command, "the foreign server stays")
	assert.Equal(t, "seamark", parsed.Servers[ClaudeServer].Command)
}

func TestPlanClaudeMCPReusesAnAlternateName(t *testing.T) {
	p, err := PlanClaudeMCP([]byte(`{"mcpServers":{"sm":{"command":"/opt/bin/seamark","args":["mcp"]}}}`), true)
	require.NoError(t, err)

	assert.Equal(t, "sm", p.Server, "the rules are spelled with the registered name")
	assert.True(t, p.Registered)
	assert.False(t, p.Register)
}

func TestPlanClaudeMCPLeavesAForeignEntryNamedSeamark(t *testing.T) {
	for body, want := range map[string]string{
		`{"mcpServers":{"seamark":{"command":"other-tool"}}}`:       `runs another command ("other-tool")`,
		`{"mcpServers":{"seamark":{"url":"https://example.test"}}}`: "has no command",
	} {
		p, err := PlanClaudeMCP([]byte(body), true)
		require.NoError(t, err)

		assert.False(t, p.Register, "a person's entry is never replaced")
		assert.False(t, p.Registered)
		assert.Empty(t, p.Server)
		require.Len(t, p.Conflicts, 1)
		assert.Contains(t, p.Conflicts[0], want)
	}
}

func TestPlanClaudeMCPRejectsMalformedAndWrongTypedFiles(t *testing.T) {
	for _, body := range []string{
		`{"mcpServers":{}} garbage`,
		`{"mcpServers":{}} {"second":"doc"}`,
		`{"mcpServers":`,
		`{"mcpServers":[]}`,
		`{"mcpServers":{"db":"nope"}}`,
		`{"mcpServers":{"db":{"command":7}}}`,
	} {
		_, err := PlanClaudeMCP([]byte(body), true)
		require.Error(t, err, body)
		assert.Contains(t, err.Error(), MCPConfig)
	}
}

func TestPlanClaudeMCPReadsNullLikeClaudeRegistration(t *testing.T) {
	// ClaudeRegistration accepts a null document, a null entry, and a null
	// command. The planner must agree, and it must never panic: doctor and
	// status call it through the inspection.
	for _, body := range []string{
		`null`,
		`{"mcpServers":null}`,
		`{"mcpServers":{"db":null}}`,
		`{"mcpServers":{"db":{"command":null}}}`,
	} {
		p, err := PlanClaudeMCP([]byte(body), true)
		require.NoError(t, err, body)
		assert.True(t, p.Register, body)

		doc, err := p.Document()
		require.NoError(t, err)
		assert.Contains(t, string(doc), `"seamark"`, body)
	}

	// A null entry under the conventional name is still a person's entry.
	p, err := PlanClaudeMCP([]byte(`{"mcpServers":{"seamark":null}}`), true)
	require.NoError(t, err)
	assert.False(t, p.Register)
	require.Len(t, p.Conflicts, 1)
	assert.Contains(t, p.Conflicts[0], "has no command")
}
