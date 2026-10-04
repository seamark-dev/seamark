//go:build unix

package status

// The tests in this file need unix. They create symbolic links, and
// os.Symlink needs special rights on Windows.

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
)

// linkOut writes body to a file outside the workspace and links rel to it.
func linkOut(t *testing.T, root, rel, body string) {
	t.Helper()

	target := filepath.Join(t.TempDir(), filepath.Base(rel))
	require.NoError(t, os.WriteFile(target, []byte(body), 0o644))

	link := filepath.Join(root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(target, link))
}

func TestGatherReadsLinkedDocumentsAsInitDoes(t *testing.T) {
	// .mcp.json is an input: init reads it through a link, and so does
	// status. .claude/settings.json is a document that setup owns: init
	// refuses a link there, and so does every status field.
	st, root := seededStore(t)
	linkOut(t, root, ".mcp.json", `{"mcpServers":{"seamark":{"command":"seamark","args":["mcp"]}}}`)
	linkOut(t, root, ".claude/settings.json", `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
		`{"type":"command","command":"`+inspecttest.Binary+` gate --enforce --hook"}]}]},`+
		`"permissions":{"allow":["mcp__seamark__orient"]}}`)

	s, err := gather(inspecttest.Registry(), st, root)
	require.NoError(t, err)

	i := slices.IndexFunc(s.Clients, func(insp integration.Inspection) bool { return insp.ClientID == integration.ClaudeID })
	require.GreaterOrEqual(t, i, 0)

	registration, ok := s.Clients[i].Entry(integration.CapabilityMCPRegistration)
	require.True(t, ok)
	assert.Equal(t, integration.StateCurrent, registration.State, registration.Detail)

	grants, ok := s.Clients[i].Entry(integration.CapabilityToolGrants)
	require.True(t, ok)
	assert.Equal(t, integration.StateUnreadable, grants.State, grants.Detail)

	assert.Empty(t, s.GateHookMode, "the refused file has no managed hook")
	assert.Equal(t, ".claude/settings.json: symlink at .claude/settings.json; seamark writes only real paths inside the repository",
		s.GateHookError, "the legacy field holds the bare reason")

	j := slices.IndexFunc(s.Approvals, func(c approve.ClientApproval) bool { return c.Client == approve.ClientClaude })
	require.GreaterOrEqual(t, j, 0)
	assert.Contains(t, s.Approvals[j].Err, "symlink at .claude/settings.json")

	var b bytes.Buffer
	Print(&b, s)
	assert.Contains(t, b.String(), `registered in .mcp.json as "seamark"`)
	assert.Contains(t, b.String(), "hook configuration UNREADABLE")
}
