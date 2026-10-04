package integration

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
)

func TestResolveInvocationKeepsTheLegacyPrecedence(t *testing.T) {
	reg := Builtin()
	root := t.TempDir()

	// Custom argv overrides the client and inherits the caller's directory.
	cfg := &agent.Config{}
	cfg.Agent.CLI = "codex"
	cfg.Agent.Argv = []string{"my-llm", "--stdin"}

	spec, err := reg.ResolveInvocation(cfg, root)
	require.NoError(t, err)
	assert.Equal(t, agent.CommandSpec{Name: "custom", Argv: []string{"my-llm", "--stdin"}}, spec)

	// Both resolvers default to the same Claude preset.
	spec, err = reg.ResolveInvocation(&agent.Config{}, root)
	require.NoError(t, err)
	assert.Equal(t, agent.ClaudeCommand(), spec)

	legacy, err := agent.ResolveCommand(&agent.Config{})
	require.NoError(t, err)
	assert.Equal(t, legacy, spec)

	cfg = &agent.Config{}
	cfg.Agent.CLI = "claude"
	spec, err = reg.ResolveInvocation(cfg, root)
	require.NoError(t, err)
	assert.Equal(t, agent.ClaudeCommand(), spec)
	assert.Empty(t, spec.Dir, "the Claude Code preset keeps the caller's directory")
}

func TestResolveInvocationBuildsTheCodexPreset(t *testing.T) {
	root := t.TempDir()

	cfg := &agent.Config{}
	cfg.Agent.CLI = "codex"

	spec, err := Builtin().ResolveInvocation(cfg, root)
	require.NoError(t, err)

	assert.Equal(t, agent.CommandSpec{
		Name: "codex",
		Argv: []string{
			"codex", "exec", "--ephemeral", "--sandbox", "read-only", "-C", root,
			"--skip-git-repo-check", "--ignore-rules", "-c", "features.hooks=false", "-",
		},
		Dir:        root,
		Diagnostic: agent.DiagnosticTail,
	}, spec)

	// Guard sandbox restrictions, hook recursion prevention, and stdin/cwd setup.
	assert.NotContains(t, spec.Argv, "workspace-write")
	assert.NotContains(t, spec.Argv, "danger-full-access")
	assert.Contains(t, spec.Argv, "--ignore-rules")
	assert.Contains(t, spec.Argv, "features.hooks=false")
	assert.Equal(t, "-", spec.Argv[len(spec.Argv)-1])
	assert.Equal(t, spec.Dir, spec.Argv[6])

	for _, flag := range spec.Argv {
		assert.NotContains(t, flag, "dangerously", "no bypass flag ever joins the preset")
	}

	// A relative -C would resolve again from Dir.
	_, err = Builtin().ResolveInvocation(cfg, "relative/root")
	require.ErrorContains(t, err, `agent cli "codex"`)
	assert.Contains(t, err.Error(), "absolute")

	_, err = Builtin().ResolveInvocation(cfg, "")
	require.Error(t, err)
}

func TestResolveInvocationNeedsNoBinary(t *testing.T) {
	// Resolution works without Codex installed; invoker construction fails.
	t.Setenv("PATH", t.TempDir())

	cfg := &agent.Config{}
	cfg.Agent.CLI = "codex"

	spec, err := Builtin().ResolveInvocation(cfg, t.TempDir())
	require.NoError(t, err)
	assert.Equal(t, "codex", spec.Name)

	_, err = exec.LookPath("codex")
	require.Error(t, err, "the test PATH holds no codex")

	_, err = agent.NewCommand(spec)
	require.ErrorContains(t, err, `agent cli "codex" not found on PATH`)
}

func TestResolveInvocationNamesWhatWouldResolve(t *testing.T) {
	hooksOnly := Client{ID: "hooksonly", Name: "Hooks Only", Setup: fakeSetup{}, SetupOps: SetupSupport{Hooks: true}}

	reg, err := NewRegistry(append(Builtin().Clients(), hooksOnly)...)
	require.NoError(t, err)

	assert.Equal(t, []string{ClaudeID, CodexID}, reg.InvocationIDs(), "a client without a command is not a value of agent.cli")

	// Invalid clients report supported names and the custom argv option.
	cfg := &agent.Config{}
	cfg.Agent.CLI = "hal9000"
	_, err = reg.ResolveInvocation(cfg, t.TempDir())
	require.ErrorContains(t, err, `unknown agent cli "hal9000" (known: claude, codex; or set agent.argv)`)

	cfg.Agent.CLI = "hooksonly"
	_, err = reg.ResolveInvocation(cfg, t.TempDir())
	require.ErrorContains(t, err, `agent cli "hooksonly" has no one-shot command (known: claude, codex; or set agent.argv)`)

	// Additional clients resolve through their registered invocation factory.
	reg, err = NewRegistry(thirdClient())
	require.NoError(t, err)

	cfg.Agent.CLI = thirdID
	root := t.TempDir()
	spec, err := reg.ResolveInvocation(cfg, root)
	require.NoError(t, err)
	assert.Equal(t, thirdID, spec.Name)
	assert.Equal(t, root, spec.Dir)
	assert.Equal(t, filepath.Clean(root), spec.Dir)
}
