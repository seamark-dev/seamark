package bench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBenchmarkReportEvaluatesFrozenThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	for trial := 1; trial <= 3; trial++ {
		for _, row := range []Row{
			validReportRow("run-a", ArmHookOn, trial, true),
			validReportRow("run-a", ArmHookOff, trial, false),
		} {
			require.NoError(t, appendRow(path, row))
		}
	}

	registry := ClaimRegistry{SchemaVersion: 1, Claims: []Claim{{
		ID: "test-claim", Claim: "treatment improves owner-invariant outcomes",
		PrimaryMetric: "invariant_pass_rate_among_task_complete",
		Comparison:    "hook-on_vs_hook-off", Direction: "higher",
		RequiredModel: "model-a", RequiredEffort: "medium", RequireCleanSeamark: true,
		MinimumEffect: 0.30, MaximumHarmfulInterference: 0.05,
		MinimumInstances: 1, MinimumValidPairsPerInstance: 3,
		Instances: []string{SchemaSyncInstanceID},
	}}}

	report, err := BuildBenchmarkReport([]string{path}, registry)
	require.NoError(t, err)
	assert.Equal(t, ResultSchemaVersion, report.ResultSchemaVersion)
	assert.Equal(t, 1, report.ClaimSchemaVersion)
	assert.Equal(t, "2026-08-08T12:00:00Z", report.EvidenceFrom)
	assert.Equal(t, report.EvidenceFrom, report.EvidenceTo)
	require.Len(t, report.Cohorts, 1)
	cohort := report.Cohorts[0]
	assert.Equal(t, 3, cohort.ValidPairs)
	assert.Equal(t, 3, cohort.FavorablePairs)
	assert.Zero(t, cohort.UnfavorablePairs)
	effect, ok := cohort.Effect()
	assert.True(t, ok)
	assert.InDelta(t, 1, effect, 1e-9)
	low, high, ok := cohort.EffectInterval95()
	assert.True(t, ok)
	assert.InDelta(t, 0.205, low, 0.001)
	assert.InDelta(t, 1, high, 1e-9)

	require.Len(t, report.Assessments, 1)
	assert.Equal(t, "passes frozen threshold", report.Assessments[0].Status)
	markdown := report.Markdown()
	assert.Contains(t, markdown, "3 favorable, 0 unfavorable")
	assert.Contains(t, markdown, "+100 pp")
	assert.Contains(t, markdown, "Approximate 95% Wilson score interval")
	assert.Contains(t, markdown, "Result schema: v5; claim schema: v1")
	assert.Contains(t, markdown, "model-a")
	assert.Contains(t, markdown, strings.Repeat("b", 64))
	assert.Contains(t, markdown, "3 valid pairs; mean effect ≥ +30 pp")
	assert.Contains(t, markdown, "passes frozen threshold")
}

func TestBuildBenchmarkReportRejectsInvalidRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	row := validReportRow("run-a", ArmHookOn, 1, true)
	row.ContextTokens++
	require.NoError(t, appendRow(path, row))

	_, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context_tokens")
}

func TestBuildBenchmarkReportRejectsUnknownResultFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	data, err := json.Marshal(validReportRow("run-a", ArmHookOn, 1, true))
	require.NoError(t, err)
	data = append(data[:len(data)-1], []byte(`,"unregistered_metric":1}`)...)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))

	_, err = BuildBenchmarkReport([]string{path}, testClaimRegistry())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown field")
}

func TestBuildBenchmarkReportRejectsDuplicateEvidence(t *testing.T) {
	t.Run("input path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		require.NoError(t, appendRow(path, validReportRow("run-a", ArmHookOn, 1, true)))

		_, err := BuildBenchmarkReport([]string{path, path}, testClaimRegistry())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "supplied more than once")
	})

	t.Run("input content", func(t *testing.T) {
		dir := t.TempDir()
		first := filepath.Join(dir, "first.jsonl")
		second := filepath.Join(dir, "second.jsonl")
		require.NoError(t, appendRow(first, validReportRow("run-a", ArmHookOn, 1, true)))
		data, err := os.ReadFile(first)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(second, data, 0o600))

		_, err = BuildBenchmarkReport([]string{first, second}, testClaimRegistry())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate evidence content")
	})

	t.Run("trial arm", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		row := validReportRow("run-a", ArmHookOn, 1, true)
		require.NoError(t, appendRow(path, row))
		require.NoError(t, appendRow(path, row))

		_, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate result")
	})

	t.Run("conflicting identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		on := validReportRow("run-a", ArmHookOn, 1, true)
		off := validReportRow("run-a", ArmHookOff, 1, false)
		off.Effort = "different-effort"
		require.NoError(t, appendRow(path, on))
		require.NoError(t, appendRow(path, off))

		_, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting experiment identity")
	})

	t.Run("conflicting observed model", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		on := validReportRow("run-a", ArmHookOn, 1, true)
		off := validReportRow("run-a", ArmHookOff, 1, false)
		off.Model = "different-model"
		require.NoError(t, appendRow(path, on))
		require.NoError(t, appendRow(path, off))

		_, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting observed models")
	})

	t.Run("invalid observed model", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "results.jsonl")
		on := validReportRow("run-a", ArmHookOn, 1, true)
		off := validReportRow("run-a", ArmHookOff, 1, false)
		off.Valid = false
		off.PairValid = false
		off.Model = "provider-error-without-trusted-model"
		require.NoError(t, appendRow(path, on))
		require.NoError(t, appendRow(path, off))

		report, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
		require.NoError(t, err)
		require.Len(t, report.Cohorts, 1)
		assert.Zero(t, report.Cohorts[0].ValidPairs)
	})
}

func TestValidateResultRowRequiresArtifactPathDigestPairs(t *testing.T) {
	digest := strings.Repeat("e", 64)
	for _, tc := range []struct {
		name   string
		mutate func(*Row)
	}{
		{"transcript path", func(row *Row) { row.Transcript = "trial.jsonl" }},
		{"transcript digest", func(row *Row) { row.TranscriptSHA = digest }},
		{"stderr path", func(row *Row) { row.StderrLog = "trial.stderr.log" }},
		{"stderr digest", func(row *Row) { row.StderrSHA = digest }},
		{"patch path", func(row *Row) { row.Patch = "trial.patch" }},
		{"patch digest", func(row *Row) { row.PatchSHA = digest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := validReportRow("run-a", ArmHookOff, 1, false)
			tc.mutate(&row)

			err := ValidateResultRow(row)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be present together")
		})
	}

	row := validReportRow("run-a", ArmHookOff, 1, false)
	row.Transcript, row.TranscriptSHA = "trial.jsonl", digest
	row.StderrLog, row.StderrSHA = "trial.stderr.log", digest
	row.Patch, row.PatchSHA = "trial.patch", digest
	assert.NoError(t, ValidateResultRow(row))
}

func TestCommittedClaimsAndResultSchemaAreValid(t *testing.T) {
	registry, err := LoadClaimRegistry(filepath.Join("..", "..", "bench", "claims.yaml"))
	require.NoError(t, err)
	require.Len(t, registry.Claims, 1)
	assert.Equal(t, 3, registry.Claims[0].MinimumInstances)
	assert.Equal(t, "claude-haiku-4-5-20251001", registry.Claims[0].RequiredModel)
	assert.Equal(t, "medium", registry.Claims[0].RequiredEffort)
	assert.True(t, registry.Claims[0].RequireCleanSeamark)

	data, err := os.ReadFile(filepath.Join("..", "..", "bench", "result.schema.json"))
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(data, &schema))
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema["$schema"])
	assert.Equal(t, false, schema["additionalProperties"])
	properties, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	version, ok := properties["schema_version"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(ResultSchemaVersion), version["const"])
	requiredValues, ok := schema["required"].([]any)
	require.True(t, ok)
	required := make([]string, 0, len(requiredValues))
	for _, value := range requiredValues {
		required = append(required, value.(string))
	}
	for _, field := range []string{"fixture", "checks", "fingerprint"} {
		assert.Contains(t, required, field)
	}
	dependent, ok := schema["dependentRequired"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{
		"transcript", "transcript_sha256", "stderr", "stderr_sha256", "patch", "patch_sha256",
	} {
		assert.Contains(t, dependent, field)
	}
}

func TestLoadClaimRegistryRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claims.yaml")
	contents := `schema_version: 1
claims:
  - id: typo
    unknown_threshold: 0.5
`
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	_, err := LoadClaimRegistry(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field unknown_threshold not found")
}

func TestClaimAssessmentStaysInsufficientForOneSmallPilot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	for trial := 1; trial <= 3; trial++ {
		require.NoError(t, appendRow(path, validReportRow("run-a", ArmHookOn, trial, true)))
		require.NoError(t, appendRow(path, validReportRow("run-a", ArmHookOff, trial, false)))
	}

	report, err := BuildBenchmarkReport([]string{path}, ClaimRegistry{
		SchemaVersion: 1,
		Claims: []Claim{{
			ID: "test-claim", Claim: "claim", PrimaryMetric: "invariant_pass_rate_among_task_complete",
			Comparison: "hook-on_vs_hook-off", Direction: "higher", MinimumEffect: 0.30,
			RequiredModel: "model-a", RequiredEffort: "medium", RequireCleanSeamark: true,
			MaximumHarmfulInterference: 0.05, MinimumInstances: 3,
			MinimumValidPairsPerInstance: 5,
			Instances:                    []string{SchemaSyncInstanceID, CacheVersionInstanceID, ExportRegistryInstanceID},
		}},
	})
	require.NoError(t, err)
	require.Len(t, report.Assessments, 1)
	assert.Equal(t, "insufficient evidence", report.Assessments[0].Status)
	assert.Contains(t, report.Assessments[0].Reason, "0/3 independent instances")
}

func TestClaimAssessmentExcludesDirtyOrMismatchedConditions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	for trial := 1; trial <= 5; trial++ {
		for _, row := range []Row{
			validReportRow("run-a", ArmHookOn, trial, true),
			validReportRow("run-a", ArmHookOff, trial, false),
		} {
			row.SeamarkVersion += "-dirty"
			require.NoError(t, appendRow(path, row))
		}
	}
	registry := testClaimRegistry()
	registry.Claims[0].RequiredModel = "model-a"
	registry.Claims[0].RequiredEffort = "medium"
	registry.Claims[0].RequireCleanSeamark = true

	report, err := BuildBenchmarkReport([]string{path}, registry)
	require.NoError(t, err)
	require.Len(t, report.Assessments, 1)
	assert.Equal(t, "insufficient evidence", report.Assessments[0].Status)
	assert.Contains(t, report.Assessments[0].Reason, "violate frozen model, effort, or clean-build conditions")
}

func TestClaimAssessmentDoesNotHideARegressedInstanceInTheMean(t *testing.T) {
	claim := Claim{
		ID: "test-claim", Claim: "claim", PrimaryMetric: "invariant_pass_rate_among_task_complete",
		Comparison: "hook-on_vs_hook-off", Direction: "higher", MinimumEffect: 0.30,
		MinimumInstanceEffect: 0, MaximumHarmfulInterference: 0.05,
		MinimumInstances: 2, MinimumValidPairsPerInstance: 5,
		Instances: []string{SchemaSyncInstanceID, CacheVersionInstanceID},
	}
	cohorts := []CohortReport{
		{
			Instance: SchemaSyncInstanceID, ValidPairs: 10,
			HookOn:  ArmReport{TaskDone: 10, InvariantPass: 10},
			HookOff: ArmReport{TaskDone: 10},
		},
		{
			Instance: CacheVersionInstanceID, ValidPairs: 10,
			HookOn:  ArmReport{TaskDone: 10},
			HookOff: ArmReport{TaskDone: 10, InvariantPass: 2},
		},
	}

	assessments := assessClaims([]Claim{claim}, cohorts)
	require.Len(t, assessments, 1)
	assert.InDelta(t, 0.4, assessments[0].MeanEffect, 1e-9)
	assert.InDelta(t, -0.2, assessments[0].WorstInstanceEffect, 1e-9)
	assert.Equal(t, "does not pass frozen threshold", assessments[0].Status)
}

func validReportRow(runID string, arm Arm, trial int, avoided bool) Row {
	row := Row{
		SchemaVersion:  ResultSchemaVersion,
		TS:             "2026-08-08T12:00:00Z",
		RunID:          runID,
		Instance:       SchemaSyncInstanceID,
		TaskSHA:        SchemaSyncInstance().TaskSHA(),
		Pin:            SchemaSyncRule,
		Arm:            arm,
		Trial:          trial,
		TaskDone:       true,
		Avoided:        avoided,
		Valid:          true,
		PairValid:      true,
		Fixture:        strings.Repeat("c", 40),
		ChecksPass:     true,
		Checks:         []CheckResult{{Command: "make test", Pass: true}},
		RequestedModel: "model-a",
		Model:          "model-a",
		InputTokens:    10,
		ContextTokens:  10,
		CostUSD:        0.01,
		AgentExit:      0,
		SeamarkVersion: "seamark test",
		SeamarkSHA:     strings.Repeat("d", 64),
		AgentVersion:   "agent test",
		Effort:         "medium",
		MaxBudgetUSD:   0.25,
		RuntimeID:      "test-runtime",
		Fingerprint:    strings.Repeat("b", 64),
	}
	if arm == ArmHookOn || arm == ArmPlacebo {
		row.HookFirings = 1
		row.HookAuditRows = 1
	}

	return row
}

func testClaimRegistry() ClaimRegistry {
	return ClaimRegistry{SchemaVersion: 1, Claims: []Claim{{
		ID: "test-claim", Claim: "claim", PrimaryMetric: "invariant_pass_rate_among_task_complete",
		Comparison: "hook-on_vs_hook-off", Direction: "higher", MinimumEffect: 0.30,
		RequiredModel: "model-a", RequiredEffort: "medium", RequireCleanSeamark: true,
		MaximumHarmfulInterference: 0.05, MinimumInstances: 1,
		MinimumValidPairsPerInstance: 1, Instances: []string{SchemaSyncInstanceID},
	}}}
}
