package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/bench"
)

func TestRunRejectsUnknownInstanceBeforeSetup(t *testing.T) {
	err := run(options{instance: "missing", trials: 1})
	require.ErrorContains(t, err, "unknown workflow instance")
}

func TestAllInstancesRefusesPaidRun(t *testing.T) {
	err := run(options{instance: "all", trials: 1})
	require.ErrorContains(t, err, "preflight-only")
}

func TestRunRejectsUnknownArmBeforeSetup(t *testing.T) {
	err := run(options{instance: bench.SchemaSyncCochangeInstanceID, arm: "hook-on", trials: 1})
	require.ErrorContains(t, err, "unknown -arm")
}

func TestRunRejectsInvalidCountsBeforeSetup(t *testing.T) {
	err := run(options{instance: bench.SchemaSyncCochangeInstanceID, trials: 0, dryRun: true})
	require.ErrorContains(t, err, "-trials must be at least 1")
	assert.NotContains(t, err.Error(), "seamark binary not found")

	err = run(options{instance: bench.SchemaSyncCochangeInstanceID, trials: 1, maxTurns: -1, dryRun: true})
	require.ErrorContains(t, err, "-max-turns must not be negative")
}

func TestAgentCommandsDifferPerArm(t *testing.T) {
	argv, managed, err := agentCommands(options{model: "claude-haiku-4-5-20251001", effort: "medium", maxBudgetUSD: 0.25})
	require.NoError(t, err)
	assert.True(t, managed)

	only := strings.Join(argv[bench.ArmMCPOnly], " ")
	withSkills := strings.Join(argv[bench.ArmMCPSkills], " ")
	assert.Contains(t, only, "--tools Read,Edit,Write,Bash,mcp__seamark__orient")
	assert.NotContains(t, only, "Skill")
	assert.Contains(t, withSkills, "mcp__seamark__expand,Skill")
	assert.NotContains(t, withSkills, "--disable-slash-commands")
	assert.NotContains(t, withSkills, "--mcp-config")

	custom, managed, err := agentCommands(options{agent: "/bin/true --flag"})
	require.NoError(t, err)
	assert.False(t, managed)
	assert.Equal(t, []string{"/bin/true", "--flag"}, custom[bench.ArmMCPOnly])
	assert.Equal(t, custom[bench.ArmMCPOnly], custom[bench.ArmMCPSkills])

	for _, opts := range []options{
		{model: "haiku", maxBudgetUSD: 1},
		{model: "claude-haiku-4-5-20251001", maxBudgetUSD: 0},
		{agent: "   "},
	} {
		_, _, err := agentCommands(opts)
		assert.Error(t, err)
	}
}

func TestParseArms(t *testing.T) {
	both, err := parseArms("both")
	require.NoError(t, err)
	assert.Equal(t, []bench.WorkflowArm{bench.ArmMCPSkills, bench.ArmMCPOnly}, both)

	only, err := parseArms("mcp-only")
	require.NoError(t, err)
	assert.Equal(t, []bench.WorkflowArm{bench.ArmMCPOnly}, only)

	_, err = parseArms("placebo")
	require.Error(t, err)
}

func TestResolveOutDefaultsPerMode(t *testing.T) {
	assert.Equal(t, defaultWorkflowOut, resolveOut(options{}))
	assert.Equal(t, defaultActivationOut, resolveOut(options{activation: "prompts.yaml"}))
	assert.Equal(t, "custom.jsonl", resolveOut(options{activation: "prompts.yaml", out: "custom.jsonl"}))
}

// jsonLines writes values as one JSON document per line.
func jsonLines(t *testing.T, path string, values ...any) {
	t.Helper()

	var data []byte
	for _, value := range values {
		line, err := json.Marshal(value)
		require.NoError(t, err)
		data = append(append(data, line...), '\n')
	}

	require.NoError(t, os.WriteFile(path, data, 0o600))
}

// validWorkflowRow is the smallest row the strict workflow reader accepts.
func validWorkflowRow() bench.WorkflowRow {
	return bench.WorkflowRow{
		SchemaVersion: bench.WorkflowResultSchemaVersion, TS: "2026-09-04T12:00:00Z", RunID: "run-a",
		Instance: bench.SchemaSyncCochangeInstanceID, TaskSHA: strings.Repeat("a", 64),
		Trigger: "server/schema.py", Companion: "web/src/api/generated.ts",
		Arm: bench.ArmMCPOnly, Trial: 1, Fixture: strings.Repeat("c", 40), Fingerprint: strings.Repeat("b", 64),
		Valid: true, PairValid: true, ChecksPass: true,
		Checks: []bench.CheckResult{{Command: "make test", Pass: true}},
	}
}

// validActivationRow is the smallest row the strict activation reader accepts.
func validActivationRow() bench.ActivationRow {
	return bench.ActivationRow{
		SchemaVersion: bench.ActivationResultSchemaVersion, TS: "2026-09-04T12:00:00Z", RunID: "run-a",
		Instance: bench.SchemaSyncCochangeInstanceID, Fixture: strings.Repeat("c", 40),
		Fingerprint: strings.Repeat("b", 64), PromptSetSHA: strings.Repeat("a", 64),
		PromptSHA: strings.Repeat("f", 64), PromptID: "typo", Expected: bench.ActivationExpectNone,
		Hit: true, Valid: true,
	}
}

func TestCheckExistingRowsGuardsTheResultsFile(t *testing.T) {
	dir := t.TempDir()
	workflow := filepath.Join(dir, "workflow.jsonl")
	activation := filepath.Join(dir, "activation.jsonl")
	jsonLines(t, workflow, validWorkflowRow())
	jsonLines(t, activation, validActivationRow())

	assert.NoError(t, checkExistingRows(filepath.Join(dir, "missing.jsonl"), true))
	assert.NoError(t, checkExistingRows(workflow, false))
	assert.NoError(t, checkExistingRows(activation, true))
	require.ErrorContains(t, checkExistingRows(workflow, true), "holds workflow rows")
	require.ErrorContains(t, checkExistingRows(activation, false), "holds activation rows")

	// A file the strict report reader would reject must not receive paid rows.
	malformed := filepath.Join(dir, "malformed.jsonl")
	line, err := json.Marshal(validWorkflowRow())
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(malformed, append(line, "\n{not json\n"...), 0o600))
	require.ErrorContains(t, checkExistingRows(malformed, false), "malformed.jsonl:2: not a JSON row")

	rowless := filepath.Join(dir, "rowless.jsonl")
	require.NoError(t, os.WriteFile(rowless, []byte("{}\n"), 0o600))
	require.ErrorContains(t, checkExistingRows(rowless, true), "rowless.jsonl:1: neither a workflow row nor an activation row")

	incomplete := filepath.Join(dir, "incomplete.jsonl")
	broken := validWorkflowRow()
	broken.TS = ""
	jsonLines(t, incomplete, broken)
	err = checkExistingRows(incomplete, false)
	require.ErrorContains(t, err, "ts is required")
	assert.Contains(t, err.Error(), "would leave it unreadable")

	brokenActivation := validActivationRow()
	brokenActivation.Hit = false
	jsonLines(t, incomplete, brokenActivation)
	require.ErrorContains(t, checkExistingRows(incomplete, true), "hit does not follow")
}

func TestGenerateFixtureWritesTheTreeOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixture")

	require.NoError(t, run(options{instance: bench.SchemaSyncCochangeInstanceID, generate: dir}))
	assert.FileExists(t, filepath.Join(dir, "server", "schema.py"))
	assert.FileExists(t, filepath.Join(dir, "web", "src", "api", "generated.ts"))
	assert.NoDirExists(t, filepath.Join(dir, ".claude"), "a manual fixture carries no arm wiring")

	err := run(options{instance: bench.SchemaSyncCochangeInstanceID, generate: dir})
	require.ErrorContains(t, err, "already exists")
}
