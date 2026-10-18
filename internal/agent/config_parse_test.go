package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// configErrorCases pins the exact LoadConfig error texts. The CLI prints
// them verbatim, so the bytes entry point must not change one character.
var configErrorCases = []struct {
	name, yaml, want string
}{
	{"syntax", "agent: [\n", "agent config: yaml: line 1: did not find expected node content"},
	{"tab indent", "\tagent:\n  cli: x\n", "agent config: yaml: found character that cannot start any token"},
	{
		"scalar section", "agent: codex\n",
		"agent config: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!str `codex` into " +
			"struct { CLI string \"yaml:\\\"cli\\\"\"; Argv []string \"yaml:\\\"argv\\\"\" }",
	},
	{
		"duplicate top-level key", "agent:\n  cli: a\nagent:\n  cli: b\n",
		"agent config: yaml: unmarshal errors:\n  line 3: mapping key \"agent\" already defined at line 1",
	},
	{
		"sequence root", "- a\n",
		"agent config: yaml: unmarshal errors:\n  line 1: cannot unmarshal !!seq into agent.Config",
	},
	{
		"unquoted star pattern elsewhere", "index:\n  exclude:\n    - *.pb.go\n",
		"agent config: yaml: line 3: did not find expected alphabetic or numeric character",
	},
}

// writeAgentConfig writes text as <root>/.seamark/config.yaml.
func writeAgentConfig(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"), []byte(text), 0o644))
}

func TestLoadConfigErrorTextsAreStable(t *testing.T) {
	for _, c := range configErrorCases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeAgentConfig(t, root, c.yaml)

			_, err := LoadConfig(root)
			require.Error(t, err)
			assert.Equal(t, c.want, err.Error())
		})
	}
}

func TestParseConfigMatchesLoadConfig(t *testing.T) {
	for _, c := range configErrorCases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			writeAgentConfig(t, root, c.yaml)

			_, loadErr := LoadConfig(root)
			_, parseErr := ParseConfig([]byte(c.yaml))

			require.Error(t, parseErr)
			assert.Equal(t, loadErr.Error(), parseErr.Error(), "both entry points report one text")
		})
	}

	valid := "index:\n  generated: true\nagent:\n  cli: codex\n  argv: [\"my-llm\", \"--print\"]\n"

	root := t.TempDir()
	writeAgentConfig(t, root, valid)

	loaded, err := LoadConfig(root)
	require.NoError(t, err)

	parsed, err := ParseConfig([]byte(valid))
	require.NoError(t, err)
	assert.Equal(t, loaded, parsed)
	assert.Equal(t, "codex", parsed.Agent.CLI)
	assert.Equal(t, []string{"my-llm", "--print"}, parsed.Agent.Argv)
}

func TestParseConfigEmptyInputMeansDefaults(t *testing.T) {
	// An empty or comment-only file decodes to defaults, the same as an
	// absent file, so a new settings file starts from known values.
	for _, text := range []string{"", "# only a comment\n"} {
		cfg, err := ParseConfig([]byte(text))
		require.NoError(t, err, "input %q", text)
		assert.Equal(t, &Config{}, cfg)
	}
}

func TestLoadConfigReturnsNonMissingReadErrors(t *testing.T) {
	// A directory in place of the file is a read error, not defaults.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark", "config.yaml"), 0o755))

	_, err := LoadConfig(root)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "agent config:", "a read error carries no section prefix")
}
