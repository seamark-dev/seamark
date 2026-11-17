package history

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestReadOnlyGitEnv proves the environment changes that keep git from
// writing .git/index, including the append to existing entries.
func TestReadOnlyGitEnv(t *testing.T) {
	lookup := func(env map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			v, ok := env[k]

			return v, ok
		}
	}

	assert.Equal(t, [][2]string{
		{"GIT_OPTIONAL_LOCKS", "0"},
		{"GIT_CONFIG_KEY_0", "diff.autoRefreshIndex"},
		{"GIT_CONFIG_VALUE_0", "false"},
		{"GIT_CONFIG_COUNT", "1"},
	}, ReadOnlyGitEnv(lookup(nil)))

	assert.Equal(t, [][2]string{
		{"GIT_OPTIONAL_LOCKS", "0"},
		{"GIT_CONFIG_KEY_2", "diff.autoRefreshIndex"},
		{"GIT_CONFIG_VALUE_2", "false"},
		{"GIT_CONFIG_COUNT", "3"},
	}, ReadOnlyGitEnv(lookup(map[string]string{"GIT_CONFIG_COUNT": "2"})), "existing entries stay")

	assert.Equal(t, [][2]string{{"GIT_OPTIONAL_LOCKS", "0"}},
		ReadOnlyGitEnv(lookup(map[string]string{"GIT_CONFIG_COUNT": "many"})),
		"a broken count gets no new entry")
}

// TestReadOnlyGitEnvironAppendsToTheProcess proves that the helper keeps
// the process environment and puts the read-only entries after it, so
// they replace an inherited GIT_OPTIONAL_LOCKS.
func TestReadOnlyGitEnvironAppendsToTheProcess(t *testing.T) {
	t.Setenv("GIT_OPTIONAL_LOCKS", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("SEAMARK_GITENV_PROBE", "kept")

	env := ReadOnlyGitEnviron()

	assert.Equal(t, os.Environ(), env[:len(os.Environ())], "the process environment comes first")
	assert.Equal(t, []string{
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_KEY_1=diff.autoRefreshIndex",
		"GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_COUNT=2",
	}, env[len(os.Environ()):])
	assert.Contains(t, env, "SEAMARK_GITENV_PROBE=kept")
}
