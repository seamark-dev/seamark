package main

import (
	"slices"
	"testing"

	"github.com/seamark-dev/seamark/internal/bench"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentCommandRequiresExactModel(t *testing.T) {
	for _, model := range []string{"", "opus", "sonnet", "haiku", "default"} {
		_, _, err := agentCommand(options{model: model, maxBudgetUSD: 1})
		assert.Error(t, err, model)
	}
}

func TestAgentCommandIsPinnedMinimalAndHookCompatible(t *testing.T) {
	argv, managed, err := agentCommand(options{
		model: "claude-sonnet-4-20250514", effort: "medium", maxBudgetUSD: 0.75,
	})
	require.NoError(t, err)
	assert.True(t, managed)

	joined := slices.Concat([]string{}, argv)
	assert.Contains(t, joined, "--model")
	assert.Contains(t, joined, "claude-sonnet-4-20250514")
	assert.Contains(t, joined, "--setting-sources")
	assert.Contains(t, joined, "project")
	assert.Contains(t, joined, "--strict-mcp-config")
	assert.Contains(t, joined, "--disable-slash-commands")
	assert.Contains(t, joined, "--no-session-persistence")
	assert.Contains(t, joined, "--include-hook-events")
	assert.Contains(t, joined, "Read,Edit,Write,Bash")
	assert.NotContains(t, joined, "--safe-mode", "safe mode would disable the measured hook")
	assert.NotContains(t, joined, "--bare", "bare mode would disable the measured hook")
}

func TestRunRejectsUnknownInstanceBeforeSetup(t *testing.T) {
	err := run(options{instance: "missing"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown benchmark instance")
}

func TestAllInstancesRefusesPaidRun(t *testing.T) {
	err := run(options{instance: "all", trials: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "preflight-only")
}

func TestRunRejectsInvalidTrialCountBeforeSetup(t *testing.T) {
	for _, opts := range []options{
		{trials: 0, dryRun: true},
		{trials: -1, preflightOnly: true},
	} {
		err := run(opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "-trials must be at least 1")
		assert.NotContains(t, err.Error(), "seamark binary not found")
	}
}

func TestLocalRuntimeIDIncludesFixtureToolchains(t *testing.T) {
	goOnly := localRuntimeID("agent-test", bench.Instance{
		Checks: []bench.Command{{Name: "go"}},
	})
	assert.Contains(t, goOnly, "go=")
	assert.NotContains(t, goOnly, "python3=")
	assert.NotContains(t, goOnly, "make=")
	assert.Contains(t, goOnly, "agent=agent-test")

	schema := localRuntimeID("agent-test", bench.SchemaSyncInstance())
	assert.Contains(t, schema, "python3=")
	assert.Contains(t, schema, "make=")
}

func TestCLIVersionRejectsEmptyOutput(t *testing.T) {
	assert.Equal(t, "unknown", cliVersion("/usr/bin/true"))
}
