package approve

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registrationOnly is explicit client setup without --approve-tools.
var registrationOnly = CodexOptions{Register: true}

func TestPlanCodexDataRegistersWithoutGrants(t *testing.T) {
	p, err := PlanCodexData(nil, false, registrationOnly)
	require.NoError(t, err)

	assert.True(t, p.Register)
	assert.Empty(t, p.Missing, "registration does not imply approval")
	require.True(t, p.Changed())

	doc := string(p.Document(nil))
	assert.Contains(t, doc, "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")
	assert.NotContains(t, doc, "approval_mode", "no tool table without the grants intent")
	assert.Contains(t, doc, "added by `seamark init`;", "the comment names the command that added the table")
	assert.NotContains(t, doc, "--approve-tools")

	// The written file is complete setup: a second plan adds nothing.
	again, err := PlanCodexData([]byte(doc), true, registrationOnly)
	require.NoError(t, err)
	assert.True(t, again.Registered)
	assert.False(t, again.Changed(), "registration-only setup is idempotent")

	// The opt-in then adds exactly the five tool tables to that file.
	grants, err := PlanCodexData([]byte(doc), true, CodexOptions{Register: true, Approve: true})
	require.NoError(t, err)
	assert.Equal(t, Tools, grants.Missing)
	assert.False(t, grants.Register)
}

func TestPlanCodexDataKeepsARegisteredFileWithoutGrants(t *testing.T) {
	existing := []byte("# mine\n[mcp_servers.sm]\ncommand = \"/opt/seamark\"\nargs = [\"mcp\"]\n")

	p, err := PlanCodexData(existing, true, registrationOnly)
	require.NoError(t, err)

	assert.Equal(t, "sm", p.Server, "an alternate registration name is reused")
	assert.True(t, p.Registered)
	assert.False(t, p.Changed())
	assert.Equal(t, existing, p.Document(existing), "an unchanged plan returns the input bytes")
}

func TestPlanCodexDataNeverRegistersForGrantsAlone(t *testing.T) {
	// A tool table needs its server table. Grants without the
	// registration intent therefore add nothing to an unregistered file.
	p, err := PlanCodexData([]byte("model = \"x\"\n"), true, CodexOptions{Approve: true})
	require.NoError(t, err)

	assert.False(t, p.Register)
	assert.False(t, p.Registered)
	assert.Empty(t, p.Server)
	assert.Empty(t, p.Missing)
	assert.False(t, p.Changed())

	// A registration that something else made takes the grants.
	registered := []byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n")

	p, err = PlanCodexData(registered, true, CodexOptions{Approve: true})
	require.NoError(t, err)
	assert.Equal(t, Tools, p.Missing)
	assert.True(t, p.Changed())
}

func TestPlanCodexDataCompletesAHeaderOnlyTableWithoutGrants(t *testing.T) {
	existing := []byte("[mcp_servers.seamark]\n\n[other]\nkey = 1\n")

	p, err := PlanCodexData(existing, true, registrationOnly)
	require.NoError(t, err)
	require.True(t, p.Register)

	doc := p.Document(existing)
	assert.Contains(t, string(doc), "[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[other]")
	assert.NotContains(t, string(doc), "approval_mode")

	var parsed map[string]any

	_, err = toml.Decode(string(doc), &parsed)
	require.NoError(t, err, "the composed file must parse")
}

func TestCodexDocumentEqualsWhatApplyCodexWrites(t *testing.T) {
	// The coordinator writes Document; legacy init appends through
	// ApplyCodex. Both must leave the same bytes, for the append case and
	// for the insert case.
	for name, body := range map[string]string{
		"absent":      "",
		"append":      "model = \"x\"",
		"header only": "[mcp_servers.seamark]\n\n[other]\nkey = 1\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()

			var existing []byte

			if body != "" {
				writeCodex(t, root, body)
				existing = []byte(body)
			}

			p, err := PlanCodex(root)
			require.NoError(t, err)

			want := p.Document(existing)

			require.NoError(t, ApplyCodex(&bytes.Buffer{}, root, p, false))

			got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(CodexConfig)))
			require.NoError(t, err)
			assert.Equal(t, string(want), string(got))
		})
	}
}

func TestPlanCodexDataIgnoresBytesOfAnAbsentFile(t *testing.T) {
	p, err := PlanCodexData([]byte("not toml at all ["), false, registrationOnly)
	require.NoError(t, err, "an absent file has no content to reject")
	assert.True(t, p.Register)
}
