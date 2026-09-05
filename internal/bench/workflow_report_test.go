package bench

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStrictWorkflowReadersPreserveRowsAndErrors(t *testing.T) {
	workflow := validWorkflowRow("run-a", ArmMCPSkills, 1, true)
	activation := validActivationRow("plan-field")
	for _, reader := range []struct {
		name, emptyError string
		row, rows        any
		read             func(string) (any, string, error)
	}{
		{"workflow", "no result rows", workflow, []WorkflowRow{workflow, workflow}, func(path string) (any, string, error) {
			return ReadWorkflowRowsStrict(path)
		}},
		{"activation", "no activation rows", activation, []ActivationRow{activation, activation}, func(path string) (any, string, error) {
			return ReadActivationRows(path)
		}},
	} {
		t.Run(reader.name, func(t *testing.T) {
			encoded, err := json.Marshal(reader.row)
			require.NoError(t, err)
			line := string(encoded)
			for _, tc := range []struct {
				name, content, wantError string
				cause                    error
			}{
				{"ordered rows and blank lines", "\n" + line + "\r\n \t\n" + line, "", nil},
				{"empty file", " \t\n", reader.emptyError, nil},
				{"unknown field", line + "\n\n" + line[:len(line)-1] + `,"unknown":true}`, `3: json: unknown field "unknown"`, nil},
				{"truncated JSON", line + "\n\n{", "3: unexpected EOF", io.ErrUnexpectedEOF},
				{"wrong field type", `{"schema_version":"one"}`, "1: json: cannot unmarshal string into Go struct field", new(json.UnmarshalTypeError)},
				{"multiple values", line + " {}", "1: trailing content: multiple JSON values", nil},
				{"malformed trailing content", line + " !", "1: trailing content: invalid character '!' looking for beginning of value", new(json.SyntaxError)},
				{"validation error", `{"schema_version":2}`, "1: schema_version is 2, want 1", nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "rows.jsonl")
					require.NoError(t, os.WriteFile(path, []byte(tc.content), 0o600))
					rows, digest, err := reader.read(path)
					if tc.wantError == "" {
						require.NoError(t, err)
						assert.Equal(t, reader.rows, rows)
						assert.Equal(t, hashBytes([]byte(tc.content)), digest)
						return
					}

					require.ErrorContains(t, err, tc.wantError)
					assert.True(t, strings.HasPrefix(err.Error(), path+":"), err)
					assert.Empty(t, rows, "a bad line rejects the whole file")
					assert.Empty(t, digest)
					switch cause := tc.cause.(type) {
					case *json.SyntaxError:
						assert.True(t, errors.As(err, &cause))
					case *json.UnmarshalTypeError:
						assert.True(t, errors.As(err, &cause))
					default:
						if cause != nil {
							assert.ErrorIs(t, err, cause)
						}
					}
				})
			}

			path := filepath.Join(t.TempDir(), "missing.jsonl")
			rows, digest, err := reader.read(path)
			assert.ErrorIs(t, err, os.ErrNotExist)
			assert.Empty(t, rows)
			assert.Empty(t, digest)
		})
	}
}

// validWorkflowRow is a report-ready row of the schema-sync co-change
// instance. The skills arm carries the full process trace; the MCP-only arm
// edited without a seamark call.
func validWorkflowRow(runID string, arm WorkflowArm, trial int, avoided bool) WorkflowRow {
	instance := SchemaSyncCochangeInstance()
	row := WorkflowRow{
		SchemaVersion: WorkflowResultSchemaVersion,
		TS:            "2026-09-04T12:00:00Z",
		RunID:         runID,
		Instance:      instance.ID,
		TaskSHA:       instance.TaskSHA(),
		Trigger:       instance.Trigger,
		Companion:     instance.Companion,
		Arm:           arm,
		Trial:         trial,
		Fixture:       strings.Repeat("c", 40),
		Fingerprint:   strings.Repeat("b", 64),
		Valid:         true,
		PairValid:     true,
		InitSeen:      true,
		ResultSeen:    true,
		Tools:         WorkflowTools(arm),
		MCPServers:    []MCPServerState{{Name: "seamark", Status: "connected"}},
		WorkflowTrace: WorkflowTrace{Edits: 2, FirstEditSeq: 1},
		TaskDone:      true,
		Avoided:       avoided,
		ChecksPass:    true,
		Checks:        []CheckResult{{Command: "make test", Pass: true}},
		AgentUsage: AgentUsage{
			RequestedModel: "model-a", Model: "model-a", InputTokens: 10, ContextTokens: 10,
			CostUSD: 0.01, Turns: 4,
		},
		SeamarkVersion: "seamark test",
		SeamarkSHA:     strings.Repeat("d", 64),
		AgentVersion:   "agent test",
		Effort:         "medium",
		MaxBudgetUSD:   0.25,
		RuntimeID:      "test-runtime",
		Transcript:     "/tmp/skills/" + string(arm) + ".jsonl",
		TranscriptSHA:  strings.Repeat("e", 64),
	}

	if arm == ArmMCPSkills {
		row.Skills = []string{"seamark-plan-change", "seamark-review-change", "seamark-understand-repo"}
		row.WorkflowTrace = WorkflowTrace{
			ChangeSetBeforeFirstEdit: true, ChangeSetFiles: []string{"server/schema.py"},
			CompanionNamedByChangeSet: true, CompanionNamedByCheck: true, CompanionOpenedAfterNamed: true,
			WhyFollowedCompanion: true, CheckAfterLastEdit: true,
			CheckVerdict: "allow (mode: warn)", Activations: []string{"seamark-plan-change"},
			SeamarkCalls: 3, SeamarkToolCalls: map[string]int{"change_set": 1, "why": 1, "check": 1},
			Edits: 2, FirstEditSeq: 3,
		}
	}

	return row
}

func testWorkflowRegistry() WorkflowClaimRegistry {
	return WorkflowClaimRegistry{
		SchemaVersion: 1,
		Claims: []WorkflowClaim{{
			ID: "test-claim", Claim: "skills raise invariant preservation over MCP alone",
			PrimaryMetric: "invariant_pass_rate_among_task_complete", Comparison: comparisonSkillsVsMCPOnly,
			Direction: "higher", RequiredModel: "model-a", RequiredEffort: "medium", RequireCleanSeamark: true,
			MinimumEffect: 0.30, MinimumInstanceEffect: 0, MaximumHarmfulInterference: 0.05,
			MinimumInstances: 1, MinimumValidPairsPerInstance: 3,
			Instances:      []string{SchemaSyncCochangeInstanceID},
			ProcessMetrics: []string{"change_set_before_first_edit_rate", "check_after_last_edit_rate"},
		}},
		Activation: ActivationCriteria{
			MinimumRecall: map[string]float64{
				"seamark-understand-repo": 0.8, "seamark-plan-change": 0.8, "seamark-review-change": 0.8,
			},
			MaximumFalseActivation: 0.25,
		},
	}
}

func TestBuildWorkflowReportEvaluatesFrozenThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.jsonl")
	for trial := 1; trial <= 3; trial++ {
		require.NoError(t, appendJSONL(path, validWorkflowRow("run-a", ArmMCPSkills, trial, true)))
		require.NoError(t, appendJSONL(path, validWorkflowRow("run-a", ArmMCPOnly, trial, false)))
	}

	report, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
	require.NoError(t, err)
	assert.Equal(t, WorkflowResultSchemaVersion, report.ResultSchemaVersion)
	assert.Equal(t, "2026-09-04T12:00:00Z", report.EvidenceFrom)
	require.Len(t, report.Cohorts, 1)

	cohort := report.Cohorts[0]
	assert.Equal(t, 3, cohort.ValidPairs)
	assert.Equal(t, 3, cohort.FavorablePairs)
	assert.Equal(t, 3, cohort.Skills.ChangeSetFirst)
	assert.Equal(t, 3, cohort.Skills.NamedByCheck)
	assert.Equal(t, 3, cohort.Skills.Opened)
	assert.Equal(t, 3, cohort.Skills.CheckLast)
	assert.Zero(t, cohort.Only.ChangeSetFirst)
	assert.Equal(t, map[string]int{"seamark-plan-change": 3}, cohort.Skills.Activations)
	assert.Equal(t, []string{"/tmp/skills"}, cohort.TranscriptDirs)

	effect, ok := cohort.Effect()
	require.True(t, ok)
	assert.InDelta(t, 1, effect, 1e-9)
	low, high, ok := cohort.EffectInterval95()
	require.True(t, ok)
	assert.InDelta(t, 0.205, low, 0.001, "the interval is the lessons report's interval")
	assert.InDelta(t, 1, high, 1e-9)

	require.Len(t, report.Assessments, 1)
	assert.Equal(t, "passes frozen threshold", report.Assessments[0].Status)
	assert.Empty(t, report.Assessments[0].Reasons)

	markdown := report.Markdown()
	assert.Contains(t, markdown, "# Skills workflow benchmark report")
	assert.Contains(t, markdown, "| Instance | Fingerprint | Model | Valid pairs | MCP + skills invariant | MCP-only invariant | Effect |")
	assert.Contains(t, markdown, "| Instance | Arm | change_set before first edit | Companion named | Named by check | Companion opened | why followed companion | check after last edit |")
	assert.Contains(t, markdown, "| "+SchemaSyncCochangeInstanceID+" | mcp-skills | 3/3 (100%) | 3/3 (100%) | 3/3 (100%) | 3/3 (100%) | 3/3 (100%) | 3/3 (100%) | 3.0 | seamark-plan-change×3 |")
	assert.Contains(t, markdown, "| "+SchemaSyncCochangeInstanceID+" | mcp-only | 0/3 (0%) | 0/3 (0%) | 0/3 (0%) | 0/3 (0%) | 0/3 (0%) | 0/3 (0%) | 0.0 | none |")
	assert.Contains(t, markdown, "+100 pp")
	assert.Contains(t, markdown, "3 favorable, 0 unfavorable")
	assert.Contains(t, markdown, "passes frozen threshold")
	assert.Contains(t, markdown, "Recorded process metrics (not gating): change_set_before_first_edit_rate, check_after_last_edit_rate")
	assert.Contains(t, markdown, "Transcripts: `/tmp/skills`")
	assert.Contains(t, markdown, "trigger `server/schema.py`; companion `web/src/api/generated.ts`")
	assert.NotContains(t, markdown, "Why not")
	assert.NotContains(t, markdown, "## Activation evaluation")
}

func TestBuildWorkflowReportNamesEveryFailedThreshold(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.jsonl")

	// Skills arm: task done in trials 1-3, invariant only in trial 1; trials
	// 4-5 never finish the task while MCP-only does (harmful interference).
	// MCP-only: task done everywhere, invariant in trial 1.
	for trial := 1; trial <= 5; trial++ {
		withSkills := validWorkflowRow("run-a", ArmMCPSkills, trial, trial == 1)
		if trial > 3 {
			withSkills.TaskDone = false
			withSkills.Avoided = false
		}

		require.NoError(t, appendJSONL(path, withSkills))
		require.NoError(t, appendJSONL(path, validWorkflowRow("run-a", ArmMCPOnly, trial, trial == 1)))
	}

	registry := testWorkflowRegistry()
	registry.Claims[0].MinimumValidPairsPerInstance = 5

	report, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, registry)
	require.NoError(t, err)
	require.Len(t, report.Assessments, 1)

	assessment := report.Assessments[0]
	assert.Equal(t, "does not pass frozen threshold", assessment.Status)
	assert.InDelta(t, 1.0/3-1.0/5, assessment.MeanEffect, 1e-9)
	assert.InDelta(t, 0.4, assessment.HarmfulInterference, 1e-9)
	require.Len(t, assessment.Reasons, 2)
	assert.Contains(t, assessment.Reasons[0], "mean effect +13.3 pp is below the minimum +30 pp")
	assert.Contains(t, assessment.Reasons[1], "harmful task interference 40.0% is above the maximum 5.0%")

	markdown := report.Markdown()
	assert.Contains(t, markdown, "Why not: mean effect +13.3 pp is below the minimum +30 pp; harmful task interference 40.0% is above the maximum 5.0%.")
	assert.Contains(t, markdown, "0 favorable, 0 unfavorable, 5 tied; 2 harmful task regressions")
}

func TestBuildWorkflowReportRefusesBadEvidence(t *testing.T) {
	t.Run("dirty build stays insufficient", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		for trial := 1; trial <= 3; trial++ {
			for _, row := range []WorkflowRow{
				validWorkflowRow("run-a", ArmMCPSkills, trial, true),
				validWorkflowRow("run-a", ArmMCPOnly, trial, false),
			} {
				row.SeamarkVersion += "-dirty"
				require.NoError(t, appendJSONL(path, row))
			}
		}

		report, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.NoError(t, err)
		require.Len(t, report.Assessments, 1)
		assert.Equal(t, "insufficient evidence", report.Assessments[0].Status)
		assert.Contains(t, report.Assessments[0].Reason, "violate frozen model, effort, or clean-build conditions")
		assert.Equal(t, []string{report.Assessments[0].Reason}, report.Assessments[0].Reasons)
	})

	t.Run("model mismatch stays insufficient", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		for trial := 1; trial <= 3; trial++ {
			for _, row := range []WorkflowRow{
				validWorkflowRow("run-a", ArmMCPSkills, trial, true),
				validWorkflowRow("run-a", ArmMCPOnly, trial, false),
			} {
				row.RequestedModel = "model-b"
				row.Model = "model-b"
				require.NoError(t, appendJSONL(path, row))
			}
		}

		report, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.NoError(t, err)
		assert.Equal(t, "insufficient evidence", report.Assessments[0].Status)
	})

	t.Run("wrong schema version", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		row := validWorkflowRow("run-a", ArmMCPSkills, 1, true)
		row.SchemaVersion = 2
		require.NoError(t, appendJSONL(path, row))

		_, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.ErrorContains(t, err, "schema_version is 2, want 1")
	})

	t.Run("unknown field", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		data, err := json.Marshal(validWorkflowRow("run-a", ArmMCPSkills, 1, true))
		require.NoError(t, err)
		data = append(data[:len(data)-1], []byte(`,"hook_firings":1}`)...)
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))

		_, err = BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.ErrorContains(t, err, "unknown field")
	})

	t.Run("duplicate trial arm", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		row := validWorkflowRow("run-a", ArmMCPSkills, 1, true)
		require.NoError(t, appendJSONL(path, row))
		require.NoError(t, appendJSONL(path, row))

		_, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.ErrorContains(t, err, "duplicate result")
	})

	t.Run("conflicting identity", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		withSkills := validWorkflowRow("run-a", ArmMCPSkills, 1, true)
		only := validWorkflowRow("run-a", ArmMCPOnly, 1, false)
		only.Companion = "web/src/workspaces/card.ts"
		require.NoError(t, appendJSONL(path, withSkills))
		require.NoError(t, appendJSONL(path, only))

		_, err := BuildWorkflowReport([]string{path}, ActivationInputs{}, testWorkflowRegistry())
		require.ErrorContains(t, err, "conflicting experiment identity")
	})

	t.Run("same file twice", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "workflow.jsonl")
		require.NoError(t, appendJSONL(path, validWorkflowRow("run-a", ArmMCPSkills, 1, true)))

		_, err := BuildWorkflowReport([]string{path, path}, ActivationInputs{}, testWorkflowRegistry())
		require.ErrorContains(t, err, "supplied more than once")
	})
}

func TestWorkflowClaimRegistryValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*WorkflowClaimRegistry)
		want   string
	}{
		{"valid", func(*WorkflowClaimRegistry) {}, ""},
		{"lessons comparison", func(r *WorkflowClaimRegistry) { r.Claims[0].Comparison = comparisonHookOnVsOff }, "unsupported comparison"},
		{"unknown instance", func(r *WorkflowClaimRegistry) { r.Claims[0].Instances = []string{SchemaSyncInstanceID} }, "unknown instance"},
		{"dirty tolerated", func(r *WorkflowClaimRegistry) { r.Claims[0].RequireCleanSeamark = false }, "clean Seamark build"},
		{"zero effect", func(r *WorkflowClaimRegistry) { r.Claims[0].MinimumEffect = 0 }, "minimum_effect"},
		{"unknown metric", func(r *WorkflowClaimRegistry) { r.Claims[0].ProcessMetrics = []string{"vibes_rate"} }, "unknown process metric"},
		{"repeated metric", func(r *WorkflowClaimRegistry) {
			r.Claims[0].ProcessMetrics = []string{"check_after_last_edit_rate", "check_after_last_edit_rate"}
		}, "repeats process metric"},
		{"fewer instances than minimum", func(r *WorkflowClaimRegistry) { r.Claims[0].MinimumInstances = 3 }, "fewer instances"},
		{"duplicate claim", func(r *WorkflowClaimRegistry) { r.Claims = append(r.Claims, r.Claims[0]) }, "duplicate workflow claim"},
		{"no activation criteria", func(r *WorkflowClaimRegistry) { r.Activation = ActivationCriteria{} }, "minimum_recall for every shipped skill"},
		{"missing skill recall", func(r *WorkflowClaimRegistry) { delete(r.Activation.MinimumRecall, "seamark-plan-change") }, "lack minimum_recall for seamark-plan-change"},
		{"unknown skill recall", func(r *WorkflowClaimRegistry) { r.Activation.MinimumRecall["other"] = 0.5 }, "unknown skill"},
		{"recall above one", func(r *WorkflowClaimRegistry) { r.Activation.MinimumRecall["seamark-plan-change"] = 1.5 }, "must be in (0, 1]"},
		{"false activation of one", func(r *WorkflowClaimRegistry) { r.Activation.MaximumFalseActivation = 1 }, "must be in [0, 1)"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registry := testWorkflowRegistry()
			tc.mutate(&registry)

			err := registry.Validate()
			if tc.want == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			}
		})
	}
}

func TestLoadWorkflowClaimRegistryFromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow-claims.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`schema_version: 1

claims:
  - id: skills-companion-workflow
    claim: MCP + skills preserves the companion-file invariant more often than the MCP server alone.
    primary_metric: invariant_pass_rate_among_task_complete
    comparison: mcp-skills_vs_mcp-only
    direction: higher
    required_model: claude-haiku-4-5-20251001
    required_effort: medium
    require_clean_seamark: true
    minimum_effect: 0.30
    minimum_instance_effect: 0.0
    maximum_harmful_interference: 0.05
    minimum_instances: 3
    minimum_valid_pairs_per_instance: 5
    instances:
      - python-ts-schema-sync-cochange-v1
      - python-cache-version-cochange-v1
      - go-export-registry-cochange-v1
    process_metrics:
      - change_set_before_first_edit_rate
      - check_after_last_edit_rate

activation:
  minimum_recall:
    seamark-understand-repo: 0.8
    seamark-plan-change: 0.8
    seamark-review-change: 0.8
  maximum_false_activation: 0.25
`), 0o600))

	registry, err := LoadWorkflowClaimRegistry(path)
	require.NoError(t, err)
	require.Len(t, registry.Claims, 1)
	assert.Equal(t, 3, registry.Claims[0].MinimumInstances)
	assert.InDelta(t, 0.25, registry.Activation.MaximumFalseActivation, 1e-9)

	require.NoError(t, os.WriteFile(path, []byte("schema_version: 1\nclaims:\n  - id: typo\n    unknown_threshold: 1\n"), 0o600))
	_, err = LoadWorkflowClaimRegistry(path)
	require.ErrorContains(t, err, "field unknown_threshold not found")
}

func TestValidateWorkflowRowRejectsContradictions(t *testing.T) {
	cases := []struct {
		name   string
		arm    WorkflowArm
		mutate func(*WorkflowRow)
		want   string
	}{
		{"schema", ArmMCPSkills, func(r *WorkflowRow) { r.SchemaVersion = 7 }, "schema_version"},
		{"same trigger and companion", ArmMCPSkills, func(r *WorkflowRow) { r.Companion = r.Trigger }, "must differ"},
		{"fixture", ArmMCPSkills, func(r *WorkflowRow) { r.Fixture = "HEAD" }, "fixture"},
		{"arm", ArmMCPSkills, func(r *WorkflowRow) { r.Arm = "hook-on" }, "unknown arm"},
		{"invariant without task", ArmMCPOnly, func(r *WorkflowRow) { r.TaskDone = false; r.Avoided = true }, "invariant_pass requires task_pass"},
		{"pair valid without valid", ArmMCPOnly, func(r *WorkflowRow) { r.Valid = false }, "pair_valid requires valid"},
		{"valid with infrastructure failure", ArmMCPOnly, func(r *WorkflowRow) { r.InfrastructureFailure = true }, "cannot record an infrastructure failure"},
		{"change_set before an edit that came first", ArmMCPSkills, func(r *WorkflowRow) { r.FirstEditSeq = 1 }, "contradicts an edit as the first tool call"},
		{"calls sum", ArmMCPSkills, func(r *WorkflowRow) { r.SeamarkCalls = 9 }, "seamark_tool_calls sum"},
		{"first edit without edits", ArmMCPOnly, func(r *WorkflowRow) { r.Edits = 0 }, "first_edit_seq"},
		{"change_set flag without call", ArmMCPOnly, func(r *WorkflowRow) { r.ChangeSetBeforeFirstEdit = true }, "requires a change_set call"},
		{"companion without call", ArmMCPOnly, func(r *WorkflowRow) { r.CompanionNamedByChangeSet = true }, "requires a change_set call"},
		{"why without companion", ArmMCPSkills, func(r *WorkflowRow) {
			r.CompanionNamedByChangeSet, r.CompanionNamedByCheck, r.CompanionOpenedAfterNamed = false, false, false
		}, "why_followed_companion requires"},
		{"why after check naming only", ArmMCPSkills, func(r *WorkflowRow) { r.CompanionNamedByChangeSet = false }, ""},
		{"check naming without call", ArmMCPOnly, func(r *WorkflowRow) { r.CompanionNamedByCheck = true }, "companion_named_by_check requires"},
		{"opened without naming", ArmMCPOnly, func(r *WorkflowRow) { r.CompanionOpenedAfterNamed = true }, "companion_opened_after_named requires"},
		{"check flag without call", ArmMCPOnly, func(r *WorkflowRow) { r.CheckAfterLastEdit = true }, "requires a check call"},
		{"verdict without call", ArmMCPOnly, func(r *WorkflowRow) { r.CheckVerdict = "allow" }, "check_verdict requires"},
		{"activation in mcp-only", ArmMCPOnly, func(r *WorkflowRow) { r.Activations = []string{"seamark-plan-change"} }, "valid mcp-only rows cannot carry"},
		{"skills in mcp-only", ArmMCPOnly, func(r *WorkflowRow) { r.Skills = []string{"seamark-plan-change"} }, "valid mcp-only rows cannot carry"},
		{"nameless server", ArmMCPOnly, func(r *WorkflowRow) { r.MCPServers = []MCPServerState{{}} }, "need a name"},
		{"context sum", ArmMCPOnly, func(r *WorkflowRow) { r.ContextTokens++ }, "context_tokens"},
		{"transcript digest alone", ArmMCPOnly, func(r *WorkflowRow) { r.Transcript = "" }, "must be present together"},
		{"bad digest", ArmMCPOnly, func(r *WorkflowRow) { r.PatchSHA, r.Patch = "xyz", "p" }, "patch_sha256"},
		{"check summary", ArmMCPOnly, func(r *WorkflowRow) { r.Checks[0].Pass = false }, "checks_pass does not match"},
		{"timestamp", ArmMCPOnly, func(r *WorkflowRow) { r.TS = "yesterday" }, "ts:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := validWorkflowRow("run-a", tc.arm, 1, false)
			tc.mutate(&row)

			// An empty want marks a mutation the reader must accept.
			err := ValidateWorkflowRow(row)
			if tc.want == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			}
		})
	}

	// An invalid MCP-only row may carry anything: the facts explain why it
	// was invalidated.
	row := validWorkflowRow("run-a", ArmMCPOnly, 1, false)
	row.Valid, row.PairValid = false, false
	row.Skills = []string{"seamark-plan-change"}
	assert.NoError(t, ValidateWorkflowRow(row))
}

// TestWorkflowResultSchemaMatchesRow keeps bench/workflow-result-v1.schema.json
// and the row struct in step: every JSON field of the row is a schema
// property and the schema names no field the row does not have.
func TestWorkflowResultSchemaMatchesRow(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "bench", "workflow-result-v1.schema.json"))
	require.NoError(t, err)

	var schema struct {
		Schema               string         `json:"$schema"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Required             []string       `json:"required"`
		Properties           map[string]any `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(data, &schema))
	assert.Equal(t, "https://json-schema.org/draft/2020-12/schema", schema.Schema)
	assert.False(t, schema.AdditionalProperties)

	version, ok := schema.Properties["schema_version"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(WorkflowResultSchemaVersion), version["const"])

	fields := jsonFieldNames(reflect.TypeOf(WorkflowRow{}))
	for name := range schema.Properties {
		assert.Contains(t, fields, name, "schema property %q is not a row field", name)
	}

	for _, name := range fields {
		assert.Contains(t, schema.Properties, name, "row field %q is missing from the schema", name)
	}

	for _, name := range []string{
		"schema_version", "ts", "run_id", "instance", "task_sha256", "trigger", "companion", "arm",
		"trial", "fixture", "fingerprint", "valid", "pair_valid", "change_set_before_first_edit",
		"companion_named_by_change_set", "why_followed_companion", "check_after_last_edit",
		"seamark_calls", "edits", "task_pass", "invariant_pass", "checks_pass", "checks", "agent_exit",
	} {
		assert.Contains(t, schema.Required, name)
	}
}

// jsonFieldNames lists the JSON names a struct marshals, descending into
// embedded structs the way encoding/json flattens them.
func jsonFieldNames(typ reflect.Type) []string {
	var names []string

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			names = append(names, jsonFieldNames(field.Type)...)

			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			names = append(names, name)
		}
	}

	return names
}
