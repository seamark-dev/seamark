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
	assert.Contains(t, markdown, "Result schema: v6; claim schema: v1")
	assert.Contains(t, markdown, "model-a")
	assert.Contains(t, markdown, strings.Repeat("b", 64))
	assert.Contains(t, markdown, "3 valid pairs; mean effect ≥ +30 pp")
	assert.Contains(t, markdown, "passes frozen threshold")
	assert.Contains(t, markdown, "supports only the committed synthetic claim")
	assert.NotContains(t, markdown, "Threshold assessment remains insufficient")
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

func TestBuildBenchmarkReportPreservesFrozenV5Evidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results-v5.jsonl")
	for _, arm := range []Arm{ArmHookOn, ArmHookOff} {
		row := validReportRow("run-v5", arm, 1, arm == ArmHookOn)
		row.SchemaVersion = 5
		row.HookDelivery = ""
		row.HookMatches = 0
		row.HookInjections = 0
		require.NoError(t, appendRow(path, row))
	}

	report, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
	require.NoError(t, err)
	assert.Equal(t, 5, report.ResultSchemaVersion)
	assert.Contains(t, report.Markdown(), "Result schema: v5; claim schema: v1")
}

func TestBuildBenchmarkReportRejectsMixedResultSchemas(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	v5 := validReportRow("run-v5", ArmHookOn, 1, true)
	v5.SchemaVersion = 5
	v5.HookDelivery = ""
	v5.HookMatches = 0
	v5.HookInjections = 0
	require.NoError(t, appendRow(path, v5))
	require.NoError(t, appendRow(path, validReportRow("run-v6", ArmHookOff, 1, false)))

	_, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
	require.ErrorContains(t, err, "mixed result schema versions")
}

func TestBenchmarkReportIdentifiesDeliveryIntensityCohorts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.jsonl")
	alwaysFingerprint := strings.Repeat("a", 64)
	onceFingerprint := strings.Repeat("b", 64)

	for _, arm := range []Arm{ArmHookOn, ArmHookOff} {
		always := validReportRow("run-always", arm, 1, arm == ArmHookOn)
		always.Fingerprint = alwaysFingerprint
		always.HookAuditRows = 2
		require.NoError(t, appendRow(path, always))

		once := validReportRow("run-once", arm, 1, arm == ArmHookOn)
		once.Fingerprint = onceFingerprint
		once.HookDelivery = HookDeliveryOncePerContext
		once.HookAuditRows = 2
		if arm == ArmHookOn {
			once.HookMatches = 2
			once.HookSuppressed = 1
		}
		require.NoError(t, appendRow(path, once))
	}

	report, err := BuildBenchmarkReport([]string{path}, testClaimRegistry())
	require.NoError(t, err)
	require.Len(t, report.Cohorts, 2)

	markdown := report.Markdown()
	assert.Contains(t, markdown, "| Instance | Fingerprint | Delivery | Matches |")
	assert.Contains(t, markdown, "| "+SchemaSyncInstanceID+" | `"+alwaysFingerprint+"` | always | 1 | 1 | 0 | 0 |")
	assert.Contains(t, markdown, "| "+SchemaSyncInstanceID+" | `"+onceFingerprint+"` | once-per-context | 2 | 1 | 0 | 1 |")
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

func TestBuildBenchmarkReportRejectsTrailingJSONLContent(t *testing.T) {
	for name, trailing := range map[string]string{
		"second value": ` {}`,
		"invalid text": ` trailing`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "results.jsonl")
			data, err := json.Marshal(validReportRow("run-a", ArmHookOn, 1, true))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, append(append(data, trailing...), '\n'), 0o600))

			_, err = BuildBenchmarkReport([]string{path}, testClaimRegistry())
			require.ErrorContains(t, err, path+":1: trailing content")
		})
	}
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

func TestValidateResultRowRequiresConsistentV6HookIntensity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Row)
	}{
		{"unknown delivery", func(row *Row) { row.HookDelivery = "sometimes" }},
		{"matches", func(row *Row) { row.HookMatches = 2 }},
		{"repeats", func(row *Row) { row.HookRepeated = 2 }},
		{"always suppression", func(row *Row) { row.HookMatches = 2; row.HookSuppressed = 1 }},
		{"legacy mismatch", func(row *Row) { row.HookFirings = 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := validReportRow("run-a", ArmHookOn, 1, true)
			tc.mutate(&row)
			require.Error(t, ValidateResultRow(row))
		})
	}

	row := validReportRow("run-a", ArmHookOn, 1, true)
	row.HookDelivery = HookDeliveryOncePerContext
	row.HookAuditRows = 2
	row.HookMatches = 2
	row.HookSuppressed = 1
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

	data, err := os.ReadFile(filepath.Join("..", "..", "bench", "result-v6.schema.json"))
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
	for _, field := range []string{
		"fixture", "checks", "fingerprint", "hook_matches", "hook_injections",
		"hook_repeated_injections", "hook_suppressed", "hook_context_bytes", "hook_delivery",
	} {
		assert.Contains(t, required, field)
	}
	dependent, ok := schema["dependentRequired"].(map[string]any)
	require.True(t, ok)
	for _, field := range []string{
		"transcript", "transcript_sha256", "stderr", "stderr_sha256", "patch", "patch_sha256",
	} {
		assert.Contains(t, dependent, field)
	}
	allOf, ok := schema["allOf"].([]any)
	require.True(t, ok)
	require.Len(t, allOf, 6)
	alwaysConstraint, ok := allOf[0].(map[string]any)
	require.True(t, ok)
	ifSchema, ok := alwaysConstraint["if"].(map[string]any)
	require.True(t, ok)
	ifProperties, ok := ifSchema["properties"].(map[string]any)
	require.True(t, ok)
	ifDelivery, ok := ifProperties["hook_delivery"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "always", ifDelivery["const"])
	thenSchema, ok := alwaysConstraint["then"].(map[string]any)
	require.True(t, ok)
	thenProperties, ok := thenSchema["properties"].(map[string]any)
	require.True(t, ok)
	thenSuppressed, ok := thenProperties["hook_suppressed"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(0), thenSuppressed["const"])

	v5, err := os.ReadFile(filepath.Join("..", "..", "bench", "result.schema.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(v5, &schema))
	properties = schema["properties"].(map[string]any)
	version = properties["schema_version"].(map[string]any)
	assert.Equal(t, float64(5), version["const"], "the published v5 schema remains frozen")
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
		HookDelivery:   HookDeliveryAlways,
		MaxBudgetUSD:   0.25,
		RuntimeID:      "test-runtime",
		Fingerprint:    strings.Repeat("b", 64),
	}
	if arm == ArmHookOn || arm == ArmPlacebo {
		row.HookFirings = 1
		row.HookAuditRows = 1
		row.HookMatches = 1
		row.HookInjections = 1
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
