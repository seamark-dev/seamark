//go:build unix

package integration

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The umask is process-wide state, so this test must not run in
// parallel with another test of the package. None of them is parallel.
func TestANewFileHonoursTheUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	root := t.TempDir()

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: ClaudeID, RegisterMCP: true}},
	}), ApplyOptions{})
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(root, ".mcp.json"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a private umask yields a private file, the same as a plain write")
}
