package mcp

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
)

// TestCheckSeesAnEditMadeDuringAnIndexRun covers a file that changes
// after an index run read it and before the run wrote the graph. The run
// sees the change and records a state that matches no workspace, so the
// next check refreshes the index instead of skipping, and it reports the
// new effect without a forced rebuild. Before the fix the run recorded
// the state after the edit, the check skipped the refresh, and the effect
// stayed unseen.
func TestCheckSeesAnEditMadeDuringAnIndexRun(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()
	fixtureWrite(t, root, "go.mod", "module example.com/raced\n")
	fixtureWrite(t, root, "store/save.go", "package store\n\n// Save keeps the payload.\n"+
		"func Save(path string, data []byte) error {\n\treturn nil\n}\n")
	fixtureGit(t, root, "init", "-q", "-b", "main")
	fixtureGit(t, root, "add", "-A")
	fixtureGit(t, root, "commit", "-q", "-m", "Add the store")

	// The write phase starts after the read loop, so this edit lands
	// between the read of save.go and the write of the graph.
	edited := false

	_, err := index.Run(index.Options{Root: root, Progress: func(phase string, done, _ int) {
		if phase != "write" || done != 0 || edited {
			return
		}

		edited = true

		fixtureWrite(t, root, "store/save.go", "package store\n\nimport \"os\"\n\n// Save keeps the payload.\n"+
			"func Save(path string, data []byte) error {\n\treturn os.WriteFile(path, data, 0o600)\n}\n")
	}})
	require.NoError(t, err)
	require.True(t, edited, "the edit ran during the index run")

	text, isErr := callCheck(t, root, "{}")
	require.False(t, isErr, text)
	assert.Contains(t, text, "effects  [fs:write]\n")
}
