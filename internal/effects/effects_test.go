package effects

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaultCatalog(t *testing.T) {
	c, err := Load(t.TempDir()) // no overlay
	require.NoError(t, err)

	assert.Equal(t, []string{"proc:exec"}, c.MatchImport("go", "os/exec", "Command"))
	assert.Equal(t, []string{"proc:exec"}, c.MatchImport("python", "subprocess", "run"))
	assert.Equal(t, []string{"db:write"}, c.MatchMethod("python", "execute"))
	assert.Equal(t, []string{"net:egress"}, c.MatchCall("ecma", "fetch"))

	assert.Equal(t, []string{"proc:exec"}, c.MatchImport("python", "os", "system"),
		"os.system shells out; it must not also carry fs:write")
	assert.Empty(t, c.MatchImport("go", "os/exec", "LookPath"), "non-sink names stay untagged")
	assert.Empty(t, c.MatchMethod("go", "execute"), "matchers are family-scoped")
}

func TestLoadWorkspaceOverlay(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"), []byte(`
sinks:
  - language: python
    import: my_infra
    names: [apply]
    tag: infra:mutate
`), 0o644))

	c, err := Load(root)
	require.NoError(t, err)

	assert.Equal(t, []string{"infra:mutate"}, c.MatchImport("python", "my_infra", "apply"),
		"workspace sinks merge on top of the defaults")
	assert.Equal(t, []string{"proc:exec"}, c.MatchImport("go", "os/exec", "Command"),
		"defaults survive the overlay")
}

func TestLoadMalformedOverlayFails(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"),
		[]byte("sinks: [not: valid: yaml:"), 0o644))

	_, err := Load(root)
	require.Error(t, err, "silently dropping the user's security config is worse than failing")
	assert.Contains(t, err.Error(), "effects.yaml")
}
