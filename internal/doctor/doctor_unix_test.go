//go:build unix

package doctor

// The tests in this file need unix. They create symbolic links, and
// os.Symlink needs special rights on Windows.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
)

// linkedRegistration links .mcp.json to a file outside the workspace.
func linkedRegistration(t *testing.T, body string) inspecttest.Fixture {
	t.Helper()

	return linkedDocument(t, integration.ClaudeID, ".mcp.json", body, nil)
}

// linkedDocument links rel to a file outside the workspace that holds
// body, and writes the other files of the layout as real files.
func linkedDocument(t *testing.T, client, rel, body string, files map[string]string) inspecttest.Fixture {
	t.Helper()

	target := filepath.Join(t.TempDir(), filepath.Base(rel))
	require.NoError(t, os.WriteFile(target, []byte(body), 0o644))

	return inspecttest.Fixture{
		Name:   "linked " + rel,
		Client: client,
		Write: func(root string) error {
			for path, content := range files {
				abs := filepath.Join(root, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
					return err
				}

				if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
					return err
				}
			}

			link := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				return err
			}

			return os.Symlink(target, link)
		},
	}
}

func TestRunAgreesWithInitOnALinkedRegistration(t *testing.T) {
	// init reads a linked .mcp.json and keeps a registration it finds
	// there. doctor reads the file by the same rule, so it calls the
	// registration current and names no fix.
	_, checks := runFixture(t, linkedRegistration(t, `{"mcpServers":{"seamark":{"command":"seamark","args":["mcp"]}}}`))
	assert.Contains(t, checks["mcp"].Detail, `claude registered in .mcp.json as "seamark"`)
	assert.Equal(t, StateOK, checks["mcp"].State)
	assert.Empty(t, checks["mcp"].Fix)

	// Without seamark in the linked file, a re-run of init cannot register
	// it, so the fix names the link.
	_, checks = runFixture(t, linkedRegistration(t, `{"mcpServers":{}}`))
	assert.Contains(t, checks["mcp"].Fix, "setup never writes through the symlink at .mcp.json")
	assert.NotContains(t, checks["mcp"].Fix, "`seamark init --client claude` to register")
}

func TestRunNamesAHookFixThatALinkedDocumentDoesNotStop(t *testing.T) {
	// The explicit setup of a client stops at a linked document that it
	// must write. A half-installed client then needs a fix that does not
	// run into the link.
	claudeGate := `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[` +
		`{"type":"command","command":"` + inspecttest.Binary + ` gate --hook"}]}]}}`

	// The plain form of init never reads .mcp.json, so it restores the
	// missing Claude Code hook.
	_, checks := runFixture(t, linkedDocument(t, integration.ClaudeID, ".mcp.json", `{"mcpServers":{}}`,
		map[string]string{".claude/settings.json": claudeGate}))
	assert.Equal(t, StateWarn, checks["hooks"].State)
	assert.Contains(t, checks["hooks"].Detail, "claude gate hook installed (warn), lessons hook missing")
	assert.Equal(t, "run `seamark init` to install the missing hook: the plain form never reads .mcp.json, "+
		"where `seamark init --client claude` stops", checks["hooks"].Fix)

	// Every Codex setup that installs hooks plans .codex/config.toml, so
	// the fix of the link comes first.
	codexLessons := `{"hooks":{"PreToolUse":[{"matcher":"apply_patch","hooks":[` +
		`{"type":"command","command":"` + inspecttest.Binary + ` lessons --hook --client codex"}]}]}}`

	_, checks = runFixture(t, linkedDocument(t, integration.CodexID, ".codex/config.toml",
		"[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n",
		map[string]string{".codex/hooks.json": codexLessons}))
	assert.Equal(t, StateWarn, checks["hooks"].State)
	assert.Contains(t, checks["hooks"].Detail, "codex lessons hook installed, gate hook missing")
	assert.Contains(t, checks["hooks"].Fix, "replace the symlink at .codex/config.toml with the real file or directory")
	assert.NotContains(t, checks["hooks"].Fix, "to install the missing hook")
}
