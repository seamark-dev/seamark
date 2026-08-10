package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaSyncFixtureIsDeterministicAndHealthy(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "host author")
	t.Setenv("GIT_AUTHOR_EMAIL", "host-author@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "host committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "host-committer@example.com")
	t.Setenv("GIT_AUTHOR_DATE", "2030-01-01T00:00:00Z")
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-01T00:00:00Z")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "host config")

	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	require.NoError(t, GenerateSchemaSyncFixture(a))
	require.NoError(t, GenerateSchemaSyncFixture(b))
	assert.Equal(t, head(t, a), head(t, b))
	identity, err := exec.Command("git", "-C", a, "show", "-s", "--format=%an|%ae|%cn|%ce|%aI|%cI").Output()
	require.NoError(t, err)
	assert.Equal(t, "bench|bench@seamark.dev|bench|bench@seamark.dev|"+
		commitDate+"|"+commitDate+"\n", string(identity))

	verdict, err := JudgeSchemaSync(a)
	require.NoError(t, err)
	assert.False(t, verdict.TaskDone)
	assert.False(t, verdict.Avoided)
	synchronized, err := runJudgeCommand(a, "python3", "tools/sync_api.py", "--check")
	require.NoError(t, err)
	assert.True(t, synchronized, "the untouched fixture must start synchronized")

	results := runChecks(context.Background(), a, SchemaSyncInstance().Checks)
	assert.True(t, checksPass(results), failedChecks(results))

	for _, rel := range []string{".seamark", ".claude"} {
		_, err := os.Stat(filepath.Join(a, rel))
		assert.True(t, os.IsNotExist(err), rel)
	}
}

func TestSchemaSyncNaiveAndGoldSolutionsDiscriminateInvariant(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, GenerateSchemaSyncFixture(dir))
	require.NoError(t, applySchemaSyncNaive(dir))

	verdict, err := JudgeSchemaSync(dir)
	require.NoError(t, err)
	assert.True(t, verdict.TaskDone)
	assert.False(t, verdict.Avoided)
	assert.Contains(t, verdict.Notes, "stale")
	assert.True(t, checksPass(runChecks(context.Background(), dir, SchemaSyncInstance().Checks)))

	generated := filepath.Join(dir, "web", "src", "api", "generated.ts")
	data, err := os.ReadFile(generated)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "billingCurrency")

	require.NoError(t, applySchemaSyncGold(dir))
	verdict, err = JudgeSchemaSync(dir)
	require.NoError(t, err)
	assert.True(t, verdict.TaskDone)
	assert.True(t, verdict.Avoided)

	data, err = os.ReadFile(generated)
	require.NoError(t, err)
	assert.Contains(t, string(data), "billingCurrency: string")
}

func TestSchemaSyncFixtureCarriesHistoricalOwnerEvidenceWithoutPromptLeakage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, GenerateSchemaSyncFixture(dir))

	log, err := exec.Command("git", "-C", dir, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Contains(t, string(log), "refresh web types after workspace schema change")

	instance := SchemaSyncInstance()
	assert.NotContains(t, instance.Task, "sync-api")
	assert.NotContains(t, instance.Task, "generated.ts")
	assert.Contains(t, instance.LessonYAML, "make sync-api")
	assert.Contains(t, instance.LessonYAML, "generated.ts")
}

func TestSchemaSyncFixtureDeclaresPythonRequirement(t *testing.T) {
	instance := SchemaSyncInstance()
	require.NotEmpty(t, instance.Checks)
	assert.Equal(t, "python3", instance.Checks[0].Name)
	assert.Contains(t, instance.Checks[0].String(), "3, 10")
	assert.Contains(t, schemaSyncREADME, "Python 3.10+")
}

func TestSchemaSyncPreflight(t *testing.T) {
	err := Preflight(context.Background(), RunConfig{
		Instance: SchemaSyncInstance(), SeamarkBin: "/opt/seamark/bin/seamark",
		PrepareIndex: false,
	})
	require.NoError(t, err)
}

func TestSchemaSyncRunEndToEnd(t *testing.T) {
	stubDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "schema.py"),
		[]byte(schemaSyncSchemaGold), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(stubDir, "presenters.py"),
		[]byte(schemaSyncPresentersGold), 0o644))

	stub := filepath.Join(stubDir, "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
cp "$SCHEMA_SYNC_STUB/schema.py" server/schema.py
cp "$SCHEMA_SYNC_STUB/presenters.py" server/presenters.py
if grep -q 'lessons --hook' .claude/settings.json; then
  make sync-api
  mkdir -p .seamark
  echo '{"file":"server/schema.py","tool":"Edit","fired":[{"region":"server","symptom":"sync-generated-api-client — After changing a public response schema, run make sync-api and commit web/src/api/generated.ts; backend tests do not validate this generated contract."}]}' >> .seamark/lessons-audit.jsonl
  echo '{"type":"result","modelUsage":{"stub-1":{}},"duration_ms":42,"total_cost_usd":0.01,"usage":{"input_tokens":300,"output_tokens":10,"cache_read_input_tokens":900}}'
else
  echo '{"type":"result","modelUsage":{"stub-1":{}},"duration_ms":42,"total_cost_usd":0.01,"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":900}}'
fi
`), 0o755))
	t.Setenv("SCHEMA_SYNC_STUB", stubDir)

	work := filepath.Join(t.TempDir(), "trials")
	out := filepath.Join(t.TempDir(), "results.jsonl")
	sum, err := Run(context.Background(), RunConfig{
		Trials:        2,
		AgentArgv:     []string{stub},
		SeamarkBin:    "/opt/seamark/bin/seamark",
		Out:           out,
		WorkDir:       work,
		TranscriptDir: filepath.Join(t.TempDir(), "transcripts"),
		Keep:          true,
		Version:       "test",
	})
	require.NoError(t, err)
	require.Len(t, sum.Rows, 4)
	assert.Equal(t, ArmHookOn, sum.Rows[0].Arm)
	assert.Equal(t, ArmHookOff, sum.Rows[2].Arm)
	assert.NotEmpty(t, sum.RunID)
	assert.Equal(t, sum.RunID, sum.Rows[0].RunID)
	assert.Equal(t, ResultSchemaVersion, sum.Rows[0].SchemaVersion)

	assert.Equal(t, Tally{
		Attempted: 2, Ran: 2, Completed: 2, Avoided: 2, Firings: 2, MeanInput: 1200,
		Matches: 2, Injections: 2,
	},
		sum.ByArm[ArmHookOn])
	assert.Equal(t, Tally{Attempted: 2, Ran: 2, Completed: 2, MeanInput: 1000},
		sum.ByArm[ArmHookOff])
	assert.Equal(t, SchemaSyncRule, sum.Rule)

	lines := sum.Lines()
	require.Len(t, lines, 3)
	assert.Equal(t,
		"sync-generated-api-client — hook-on: 2/2 avoided (2/2 completed); hook-off: 0/2 avoided (2/2 completed)",
		lines[0])
	assert.Equal(t,
		"context processed — hook-on mean 1200 vs hook-off 1000 (+200 per trial)",
		lines[1])
	assert.Equal(t,
		"hook delivery — 2 matches, 2 injections, 0 repeated, 0 suppressed, 0 context bytes",
		lines[2])

	rows, err := ReadRows(out)
	require.NoError(t, err)
	require.Len(t, rows, 4)
	assert.Equal(t, SchemaSyncRule, rows[0].Pin)
	assert.Equal(t, SchemaSyncInstanceID, rows[0].Instance)
	assert.Equal(t, "stub-1", rows[0].Model)
	assert.Equal(t, "test", rows[0].SeamarkVersion)
	assert.NotEmpty(t, rows[0].Fixture)
	assert.Equal(t, rows[0].Fixture, rows[3].Fixture)

	settings, err := os.ReadFile(filepath.Join(work, "hook-on-01", ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settings), `\"/opt/seamark/bin/seamark\" lessons --hook`)
	assert.Contains(t, string(settings), "PreToolUse")

	lesson, err := os.ReadFile(filepath.Join(work, "hook-on-01", ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(lesson), SchemaSyncRule)

	offSettings, err := os.ReadFile(filepath.Join(work, "hook-off-01", ".claude", "settings.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(offSettings), "PreToolUse")
	assert.Contains(t, string(offSettings), `"failIfUnavailable": true`)

	_, err = os.Stat(filepath.Join(work, "hook-off-01", ".seamark", "lessons.yaml"))
	assert.True(t, os.IsNotExist(err), "the control must not carry the lesson")

	assert.NotEmpty(t, rows[0].Patch)
	assert.NotEmpty(t, rows[0].PatchSHA)
	assert.NotEmpty(t, rows[0].TranscriptSHA)
	patch, err := os.ReadFile(rows[0].Patch)
	require.NoError(t, err)
	assert.Equal(t, hashBytes(patch), rows[0].PatchSHA)
	assert.Contains(t, string(patch), "billingCurrency")
	transcript, err := os.ReadFile(rows[0].Transcript)
	require.NoError(t, err)
	assert.Equal(t, hashBytes(transcript), rows[0].TranscriptSHA)
}

func TestInstanceByID(t *testing.T) {
	defaultInstance, err := InstanceByID("")
	require.NoError(t, err)
	assert.Equal(t, SchemaSyncInstanceID, defaultInstance.ID)

	schemaInstance, err := InstanceByID(SchemaSyncInstanceID)
	require.NoError(t, err)
	assert.Equal(t, SchemaSyncRule, schemaInstance.Rule)
	assert.Equal(t, []string{
		SchemaSyncInstanceID, CacheVersionInstanceID, ExportRegistryInstanceID,
	}, InstanceIDs())
	for _, instance := range Instances() {
		assert.NotEmpty(t, instance.sourceFile)
		_, sourceErr := instanceSources.ReadFile(instance.sourceFile)
		require.NoError(t, sourceErr)
	}

	_, err = InstanceByID("missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), SchemaSyncInstanceID)
}
