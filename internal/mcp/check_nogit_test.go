package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckWithoutGitDiffHead(t *testing.T) {
	// check {} has no diff where git diff HEAD cannot run. The reply must
	// say why and name the diff argument, which runs without git. The
	// reasons are the same text as in the CLI, which names its own route.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	t.Run("outside git", func(t *testing.T) {
		parent := t.TempDir()
		t.Setenv("GIT_CEILING_DIRECTORIES", parent)

		dir := filepath.Join(parent, "plain")
		require.NoError(t, os.Mkdir(dir, 0o755))

		text, isErr := callCheck(t, dir, `{}`)
		assert.True(t, isErr)
		assert.Equal(t, "no diff given, and "+dir+" is not inside a git work tree, so git diff HEAD cannot run"+
			"; pass the unified diff in the diff argument", text)

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		assert.Empty(t, entries, "the refusal builds no index and writes no audit record")
	})

	t.Run("no commit", func(t *testing.T) {
		root := t.TempDir()

		gitInit := exec.Command("git", "init", "-q", "-b", "main")
		gitInit.Dir = root
		out, err := gitInit.CombinedOutput()
		require.NoError(t, err, "%s", out)

		text, isErr := callCheck(t, root, `{}`)
		assert.True(t, isErr)
		assert.Equal(t, "no diff given, and the repository at "+root+
			" has no commit yet, so git diff HEAD has nothing to compare"+
			"; pass the unified diff in the diff argument", text)
	})
}
