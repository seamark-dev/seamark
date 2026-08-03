package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/store"
)

// fixtureRoot builds a git workspace with a healthy index database.
func fixtureRoot(t *testing.T) (root, dbPath string) {
	t.Helper()

	root = t.TempDir()

	for _, args := range [][]string{{"init", "-q"}, {"commit", "--allow-empty", "-m", "seed"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	dbPath = store.DefaultPath(root)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	return root, dbPath
}

// byName indexes a report's checks.
func byName(r *Report) map[string]Check {
	out := map[string]Check{}
	for _, c := range r.Checks {
		out[c.Name] = c
	}

	return out
}

func TestRunHealthyWorkspace(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Zero(t, r.Fails, "a healthy fixture must not fail: %+v", r.Checks)

	for _, name := range []string{"binary", "git", "index", "integrity", "policy", "effects"} {
		assert.Equal(t, StateOK, checks[name].State, "%s: %s", name, checks[name].Detail)
	}

	assert.Equal(t, StateInfo, checks["hooks"].State, "no hooks installed yet is a fact, not a fault")
	assert.Contains(t, checks["hooks"].Fix, "seamark init")
}

func TestRunMissingIndexIsInfoNotFailure(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.RemoveAll(filepath.Join(root, ".seamark")))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "seamark index")
	assert.Zero(t, r.Fails)
}

func TestRunDetectsNewerDatabase(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta("schema_version", strconv.Itoa(store.SupportedSchema()+5)))
	require.NoError(t, st.Close())

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "do not delete")
	assert.Positive(t, r.Fails)
}

func TestRunDetectsCorruptDatabase(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// Garbage where SQLite expects a header: probe must fail with the
	// export-first recovery path, never a crash.
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not a database"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["index"].State)
	assert.Contains(t, checks["index"].Fix, "state export")
}

func TestRunDetectsBrokenPolicyAndEffects(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: [broken\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "effects.yaml"),
		[]byte("sinks: [broken\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateFail, checks["policy"].State)
	assert.Contains(t, checks["policy"].Fix, "fails closed")
	assert.Equal(t, StateFail, checks["effects"].State)
}

func TestRunDetectsIgnoredPolicyOverlays(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// A .gitignore swallowing .seamark whole, without the carve-outs
	// init installs: the policy silently leaves review.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(".seamark/*\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: warn\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["gitignore"].State)
	assert.Contains(t, checks["gitignore"].Detail, "policy.yaml")
	assert.Contains(t, checks["gitignore"].Fix, "seamark init")
}

func TestRunDetectsPartialHooks(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"/bin/seamark gate --hook"}]}]}}`), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["hooks"].State)
	assert.Contains(t, checks["hooks"].Detail, "lessons hook missing")
}

func TestRunDetectsMissingAgentBinary(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent:\n  argv: [\"no-such-agent-binary-xyz\"]\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateWarn, checks["agent"].State)
	assert.Contains(t, checks["agent"].Detail, "no-such-agent-binary-xyz")
}

func TestRunGitignoreUndeterminedOutsideGit(t *testing.T) {
	// No git repository: ignore status cannot be determined, and an
	// undetermined status must never masquerade as a clean OK.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"),
		[]byte("mode: warn\n"), 0o644))

	r := Run(root, store.DefaultPath(root), "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["gitignore"].State)
	assert.Contains(t, checks["gitignore"].Detail, "could not determine")
}

func TestPrintAndJSON(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	r := Run(root, dbPath, "test")

	var b bytes.Buffer
	Print(&b, r)
	assert.Contains(t, b.String(), "installation health")
	assert.Contains(t, b.String(), "seamark test", "the binary line is host-independent")

	data, err := json.Marshal(r)
	require.NoError(t, err)

	var back Report
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, r, &back, "the report must survive the JSON round trip")
}
