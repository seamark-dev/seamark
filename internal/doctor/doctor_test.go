package doctor

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/approve"
	"github.com/seamark-dev/seamark/internal/skills"
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

// installSkills writes the shipped skills into one client directory of
// the fixture, the way `seamark init --skills=<client>` would.
func installSkills(t *testing.T, root, mode string) {
	t.Helper()

	targets, err := skills.Targets(root, mode)
	require.NoError(t, err)
	require.NoError(t, skills.Install(&bytes.Buffer{}, root, targets, false))
}

func TestRunReportsSkillsNotInstalled(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateInfo, checks["skills"].State, "opt-in skills are a fact, not a fault")
	assert.Contains(t, checks["skills"].Detail, "not installed")
	assert.Contains(t, checks["skills"].Fix, "seamark init --skills")
}

func TestRunReportsSkillsInstalled(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	installSkills(t, root, skills.ModeClaude)

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateOK, checks["skills"].State, checks["skills"].Detail)
	assert.Contains(t, checks["skills"].Detail, "claude 3/3 current")
	assert.Contains(t, checks["skills"].Detail, "codex not installed")
	assert.Empty(t, checks["skills"].Fix)
	assert.Zero(t, r.Warns, "%+v", r.Checks)
}

func TestRunDetectsStaleSkills(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	installSkills(t, root, skills.ModeClaude)

	// A managed copy whose body drifted, as after a seamark upgrade.
	skillMD := filepath.Join(root, ".claude", "skills", "seamark-plan-change", "SKILL.md")
	require.NoError(t, os.WriteFile(skillMD,
		[]byte("---\nname: seamark-plan-change\nmetadata:\n  seamark: managed\n---\nold body\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "1 stale")
	assert.Contains(t, checks["skills"].Fix, "seamark init --skills")
}

func TestRunIgnoresForeignSkillDir(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	// The user's own skill under a shipped name, nothing else installed:
	// named, never a warning, never touched.
	dir := filepath.Join(root, ".claude", "skills", "seamark-plan-change")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: seamark-plan-change\ndescription: mine\n---\nMine.\n"), 0o644))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateInfo, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "1 not managed")
	assert.Contains(t, checks["skills"].Fix, "rename or remove",
		"init --skills alone cannot replace the directory, so the fix must say so first")
	assert.Contains(t, checks["skills"].Fix, "seamark init --skills")
	assert.Zero(t, r.Warns, "%+v", r.Checks)

	// Beside a complete managed install, the foreign directory is still
	// only information, with the corrective choice spelled out.
	installSkills(t, root, skills.ModeCodex)

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateInfo, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "codex 3/3 current")
	assert.Contains(t, checks["skills"].Fix, "not seamark's")
}

func TestRunWarnsOnUnreadableSkillDir(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "skills"), []byte("oops"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["skills"].State)
	assert.Contains(t, checks["skills"].Detail, "codex unreadable")
}

// approveClaude writes the eight Claude Code allow rules into the fixture.
func approveClaude(t *testing.T, root string) {
	t.Helper()

	rules, err := approve.ClaudeRules()
	require.NoError(t, err)

	quoted := make([]string, 0, len(rules))
	for _, r := range rules {
		quoted = append(quoted, `"`+r+`"`)
	}

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":[`+strings.Join(quoted, ",")+`]}}`), 0o644))
}

func TestRunReportsApprovalsNotConfigured(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateInfo, checks["approvals"].State, "opt-in approvals are a fact, not a fault")
	assert.Contains(t, checks["approvals"].Detail, "not configured")
	assert.Contains(t, checks["approvals"].Fix, "seamark init --approve-tools")
	assert.Contains(t, checks["approvals"].Fix, "user or managed policy")
}

func TestRunReportsApprovalsConfigured(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	approveClaude(t, root)

	p, err := approve.PlanCodex(root)
	require.NoError(t, err)
	require.NoError(t, approve.ApplyCodex(&bytes.Buffer{}, root, p, false))

	r := Run(root, dbPath, "test")
	checks := byName(r)

	assert.Equal(t, StateOK, checks["approvals"].State, checks["approvals"].Detail)
	assert.Contains(t, checks["approvals"].Detail, "claude 8/8 rules")
	assert.Contains(t, checks["approvals"].Detail, "codex registered as \"seamark\", 5/5 tools approved")
	assert.Contains(t, checks["approvals"].Detail, "project configuration")
}

func TestRunDetectsPartialApprovals(t *testing.T) {
	root, dbPath := fixtureRoot(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".claude", "settings.json"),
		[]byte(`{"permissions":{"allow":["mcp__seamark__orient"]}}`), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "claude 1/8 rules")
	assert.Contains(t, checks["approvals"].Fix, "seamark init --approve-tools")
}

func TestRunReportsConflictingAndUnreadableApprovals(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"prompt\"\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "tools.why.approval_mode = \"prompt\"")
	assert.Contains(t, checks["approvals"].Fix, "by hand")

	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"), []byte("not toml [\n"), 0o644))

	checks = byName(Run(root, dbPath, "test"))
	assert.Equal(t, StateWarn, checks["approvals"].State)
	assert.Contains(t, checks["approvals"].Detail, "codex unreadable")
}

func TestRunReportsDisabledCodexServerAsConflict(t *testing.T) {
	root, dbPath := fixtureRoot(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".codex"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".codex", "config.toml"),
		[]byte("[mcp_servers.seamark]\ncommand = \"seamark\"\nargs = [\"mcp\"]\nenabled = false\n\n[mcp_servers.seamark.tools.why]\napproval_mode = \"approve\"\n"), 0o644))

	checks := byName(Run(root, dbPath, "test"))

	assert.Equal(t, StateWarn, checks["approvals"].State, "a disabled server must not read as approved")
	assert.Contains(t, checks["approvals"].Detail, "enabled = false")
}
