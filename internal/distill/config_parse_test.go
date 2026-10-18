package distill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// distillConfigErrorCases pins the exact LoadConfig error texts. The CLI
// prints them verbatim, so the bytes entry point must not change them.
var distillConfigErrorCases = []struct {
	name, yaml, want string
}{
	{"syntax", "distill: [\n", "distill config: yaml: line 1: did not find expected node content"},
	{
		"quoted bool", "distill:\n  write: \"true\"\n",
		"distill config: yaml: unmarshal errors:\n  line 2: cannot unmarshal !!str `true` into bool",
	},
	{
		"duplicate top-level key", "distill:\n  write: true\ndistill:\n  write: false\n",
		"distill config: yaml: unmarshal errors:\n  line 3: mapping key \"distill\" already defined at line 1",
	},
	{
		"sequence root", "- a\n",
		"distill config: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!seq into distill.Config",
	},
}

// writeDistillConfig writes text as <root>/.seamark/config.yaml.
func writeDistillConfig(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"), []byte(text), 0o644))
}

func TestLoadConfigErrorTextsAreStable(t *testing.T) {
	for _, c := range distillConfigErrorCases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeDistillConfig(t, root, c.yaml)

			_, err := LoadConfig(root)
			require.Error(t, err)
			assert.Equal(t, c.want, err.Error())
		})
	}
}

func TestParseConfigMatchesLoadConfig(t *testing.T) {
	for _, c := range distillConfigErrorCases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeDistillConfig(t, root, c.yaml)

			_, loadErr := LoadConfig(root)
			_, parseErr := ParseConfig([]byte(c.yaml))

			require.Error(t, parseErr)
			assert.Equal(t, loadErr.Error(), parseErr.Error(), "both entry points report one text")
		})
	}

	// YAML 1.1 forms such as `yes` still mean true through both paths.
	valid := "index:\n  generated: true\ndistill:\n  write: yes\n"

	root := t.TempDir()
	writeDistillConfig(t, root, valid)

	loaded, err := LoadConfig(root)
	require.NoError(t, err)

	parsed, err := ParseConfig([]byte(valid))
	require.NoError(t, err)
	assert.Equal(t, loaded, parsed)
	assert.True(t, parsed.Distill.Write)

	empty, err := ParseConfig(nil)
	require.NoError(t, err)
	assert.False(t, empty.Distill.Write, "empty input means defaults, as an absent file does")
}
