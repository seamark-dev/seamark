package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPromptSet() ActivationPromptSet {
	return ActivationPromptSet{SchemaVersion: 1, Instance: SchemaSyncCochangeInstanceID, Prompts: []ActivationPrompt{
		{ID: "understand-api", Prompt: "Explain how a workspace response is produced.", Expect: "seamark-understand-repo"},
		{ID: "plan-field", Prompt: "Add a locale field across the API and the client.", Expect: "seamark-plan-change"},
		{ID: "review-diff", Prompt: "Am I missing anything before I commit?", Expect: "seamark-review-change", Prepare: ActivationPrepareNaive},
		{ID: "typo", Prompt: "Fix the typo in README.md.", Expect: ActivationExpectNone, Note: "should stay quiet"},
	}}
}

func TestActivationPromptSetValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ActivationPromptSet)
		want   string
	}{
		{"valid", func(*ActivationPromptSet) {}, ""},
		{"schema", func(s *ActivationPromptSet) { s.SchemaVersion = 2 }, "schema_version"},
		{"unknown instance", func(s *ActivationPromptSet) { s.Instance = SchemaSyncInstanceID }, "unknown workflow instance"},
		{"empty", func(s *ActivationPromptSet) { s.Prompts = nil }, "empty"},
		{"duplicate id", func(s *ActivationPromptSet) { s.Prompts[1].ID = s.Prompts[0].ID }, "duplicate"},
		{"unsafe id", func(s *ActivationPromptSet) { s.Prompts[0].ID = "../typo" }, "must contain only"},
		{"blank prompt", func(s *ActivationPromptSet) { s.Prompts[0].Prompt = " " }, "no prompt text"},
		{"unknown skill", func(s *ActivationPromptSet) { s.Prompts[0].Expect = "seamark-deploy" }, "unknown skill"},
		{"unknown prepare", func(s *ActivationPromptSet) { s.Prompts[0].Prepare = "gold" }, "unknown prepare step"},
		{"no should-not", func(s *ActivationPromptSet) { s.Prompts = s.Prompts[:3] }, "no should-not-activate prompt"},
		{"uncovered skill", func(s *ActivationPromptSet) { s.Prompts[2].Expect = "seamark-plan-change" }, "no should-activate prompt for seamark-review-change"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set := testPromptSet()
			tc.mutate(&set)

			err := set.Validate()
			if tc.want == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.want)
			}
		})
	}
}

func TestCommittedActivationPromptsAreValid(t *testing.T) {
	set, err := LoadActivationPrompts(filepath.Join("..", "..", "bench", "activation", "prompts.yaml"))
	require.NoError(t, err)

	perSkill := map[string]int{}
	shouldNot := 0

	for _, prompt := range set.Prompts {
		assert.NotEmpty(t, prompt.Note, "%s: every prompt says why it is in the set", prompt.ID)

		if prompt.Expect == ActivationExpectNone {
			shouldNot++
		} else {
			perSkill[prompt.Expect]++
		}
	}

	for name, count := range perSkill {
		assert.GreaterOrEqual(t, count, 2, "%s needs at least two should-activate prompts for a recall estimate", name)
	}

	assert.GreaterOrEqual(t, shouldNot, 4, "typo, comment, single symbol, and one-line rename cases")

	sha, err := set.SHA256()
	require.NoError(t, err)
	assert.True(t, validSHA256(sha))
}

func TestActivationStatsArithmetic(t *testing.T) {
	rows := []ActivationRow{
		{Valid: true, Expected: "seamark-plan-change", Activated: []string{"seamark-plan-change"}, Hit: true},
		{Valid: true, Expected: "seamark-plan-change", Activated: []string{"seamark-plan-change", "seamark-review-change"}, Hit: true},
		{Valid: true, Expected: "seamark-plan-change", Hit: false},
		{Valid: true, Expected: "seamark-review-change", Activated: []string{"seamark-plan-change"}, Hit: false},
		{Valid: true, Expected: ActivationExpectNone, Hit: true},
		{Valid: true, Expected: ActivationExpectNone, Activated: []string{"seamark-understand-repo"}, Hit: false},
		{Valid: false, Expected: ActivationExpectNone, Activated: []string{"seamark-understand-repo"}, Hit: false},
	}

	stats := ActivationStatsFor(rows)
	assert.Equal(t, ActivationRate{2, 3}, stats.Recall["seamark-plan-change"])
	assert.Equal(t, ActivationRate{0, 1}, stats.Recall["seamark-review-change"])
	assert.Equal(t, ActivationRate{1, 2}, stats.FalseActivation)
	assert.Equal(t, 2, stats.CrossActivations)
	assert.Equal(t, 6, stats.Valid)
	assert.Equal(t, 1, stats.Invalid)

	lines := strings.Join(stats.Lines(), "\n")
	assert.Contains(t, lines, "recall seamark-plan-change — 2/3 (67%)")
	assert.Contains(t, lines, "false activation — 1/2 (50%)")
	assert.Contains(t, lines, "cross-activation — 2")

	status, reasons := assessActivation(testWorkflowRegistry().Activation, stats, nil)
	assert.Equal(t, "insufficient evidence", status, "no valid seamark-understand-repo session")
	assert.Contains(t, strings.Join(reasons, "; "), "no valid should-activate session for seamark-understand-repo")
	assert.Contains(t, strings.Join(reasons, "; "), "recall for seamark-plan-change is 2/3 (67%), below the minimum 80%")
	assert.Contains(t, strings.Join(reasons, "; "), "false activation is 1/2 (50%), above the maximum 25%")

	perfect := ActivationStatsFor([]ActivationRow{
		{Valid: true, Expected: "seamark-plan-change", Activated: []string{"seamark-plan-change"}, Hit: true},
		{Valid: true, Expected: "seamark-review-change", Activated: []string{"seamark-review-change"}, Hit: true},
		{Valid: true, Expected: "seamark-understand-repo", Activated: []string{"seamark-understand-repo"}, Hit: true},
		{Valid: true, Expected: ActivationExpectNone, Hit: true},
	})
	status, reasons = assessActivation(testWorkflowRegistry().Activation, perfect, nil)
	assert.Equal(t, "passes frozen criteria", status)
	assert.Empty(t, reasons)
}

func TestRunActivationReplaysThePromptSet(t *testing.T) {
	work := filepath.Join(t.TempDir(), "sessions")
	out := filepath.Join(t.TempDir(), "activation.jsonl")

	// The canned skills-arm transcript activates seamark-plan-change on every
	// prompt, so exactly one should-activate prompt hits and the should-not
	// prompt counts as a false activation.
	sum, err := RunActivation(context.Background(), WorkflowConfig{
		Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{cannedAgent(t)}),
		SeamarkBin: "/opt/seamark/bin/seamark", WorkDir: work, Keep: true, Out: out,
		MaxTurns: 8, Model: "claude-test", RequireStructuredResult: true, RequireExpectedInit: true,
	}, testPromptSet())
	require.NoError(t, err)
	require.Len(t, sum.Rows, 4)

	byID := map[string]ActivationRow{}
	for _, row := range sum.Rows {
		byID[row.PromptID] = row
		assert.True(t, row.Valid, row.InvalidReason)
		assert.NoError(t, ValidateActivationRow(row), row.PromptID)
		assert.Equal(t, []string{"seamark-plan-change"}, row.Activated)
		assert.Equal(t, 8, row.MaxTurns)
		assert.Equal(t, 3, row.SeamarkCalls)
		assert.Equal(t, 1, row.Edits)
		assert.True(t, validSHA256(row.PromptSetSHA))
	}

	assert.True(t, byID["plan-field"].Hit)
	assert.False(t, byID["understand-api"].Hit)
	assert.False(t, byID["review-diff"].Hit)
	assert.False(t, byID["typo"].Hit)
	assert.Equal(t, ActivationPrepareNaive, byID["review-diff"].Prepare)

	// The naive patch reached the review session's tree, and --max-turns
	// reached the agent after the trial's MCP configuration.
	schema, err := os.ReadFile(filepath.Join(work, "activation-review-diff", "server", "schema.py"))
	require.NoError(t, err)
	assert.Contains(t, string(schema), "billingCurrency")

	argv, err := os.ReadFile(filepath.Join(work, "activation-typo", "argv.txt"))
	require.NoError(t, err)
	args := strings.Split(strings.TrimSpace(string(argv)), "\n")
	assert.Equal(t, []string{"--max-turns", "8"}, args[3:5])
	assert.Equal(t, agentPrompt("Fix the typo in README.md."), strings.Join(args[5:], "\n"))

	lines := strings.Join(sum.Lines(), "\n")
	assert.Contains(t, lines, "activation — 4 valid session(s), 0 invalid")
	assert.Contains(t, lines, "recall seamark-plan-change — 1/1 (100%)")
	assert.Contains(t, lines, "recall seamark-review-change — 0/1 (0%)")
	assert.Contains(t, lines, "false activation — 1/1 (100%)")

	rows, digest, err := ReadActivationRows(out)
	require.NoError(t, err)
	assert.Len(t, rows, 4)
	assert.True(t, validSHA256(digest))

	// The report renders the same rows against the frozen criteria.
	workflow := filepath.Join(t.TempDir(), "workflow.jsonl")
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPSkills, 1, true)))
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPOnly, 1, false)))

	report, err := BuildWorkflowReport([]string{workflow},
		ActivationInputs{Paths: []string{out}, Prompts: testPromptSet()}, testWorkflowRegistry())
	require.NoError(t, err)
	require.NotNil(t, report.Activation)
	assert.Empty(t, report.Activation.Missing, "every manifest prompt has a valid session")
	assert.Equal(t, "does not pass frozen criteria", report.Activation.Status)

	markdown := report.Markdown()
	assert.Contains(t, markdown, "## Activation evaluation")
	assert.Contains(t, markdown, "| plan-field | seamark-plan-change | seamark-plan-change | true | yes |")
	assert.Contains(t, markdown, "| typo | none | seamark-plan-change | false | yes |")
	assert.Contains(t, markdown, "Frozen criteria: **does not pass frozen criteria**")
}

func TestRunActivationRejectsBadInput(t *testing.T) {
	cfg := WorkflowConfig{Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{noopAgent(t)})}

	broken := testPromptSet()
	broken.Prompts = broken.Prompts[:1]
	_, err := RunActivation(context.Background(), cfg, broken)
	require.ErrorContains(t, err, "no should-activate prompt")

	noArgv := cfg
	noArgv.AgentArgv = nil
	_, err = RunActivation(context.Background(), noArgv, testPromptSet())
	require.ErrorContains(t, err, "has no agent command")

	negative := cfg
	negative.MaxTurns = -1
	_, err = RunActivation(context.Background(), negative, testPromptSet())
	require.ErrorContains(t, err, "max turns")

	other := cfg
	other.Instance = CacheVersionCochangeInstance()
	_, err = RunActivation(context.Background(), other, testPromptSet())
	require.ErrorContains(t, err, "written for instance "+SchemaSyncCochangeInstanceID)
}

// TestRunActivationTreatsInterruptedSessionAsCancellation checks that an
// interrupt during a session ends the run cleanly, with no row for the
// killed session.
func TestRunActivationTreatsInterruptedSessionAsCancellation(t *testing.T) {
	hang := filepath.Join(t.TempDir(), "hang.sh")
	require.NoError(t, os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755))
	out := filepath.Join(t.TempDir(), "activation.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)

	started := time.Now()
	sum, err := RunActivation(ctx, WorkflowConfig{
		Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{hang}),
		SeamarkBin: "/opt/seamark/bin/seamark", Out: out,
	}, testPromptSet())
	require.NoError(t, err, "an interrupt is a stop, not a failure")
	assert.Empty(t, sum.Rows)
	assert.Empty(t, sum.StoppedReason)
	assert.NoFileExists(t, out)
	assert.Less(t, time.Since(started), 5*time.Second)
}

// validActivationRow is a report-ready activation row for one prompt of
// testPromptSet, with the manifest's expectation met.
func validActivationRow(promptID string) ActivationRow {
	prompts := testPromptSet()
	promptSetSHA, _ := prompts.SHA256()

	row := ActivationRow{
		SchemaVersion: ActivationResultSchemaVersion, TS: "2026-09-04T12:00:00Z", RunID: "run-a",
		Instance: SchemaSyncCochangeInstanceID, Fixture: strings.Repeat("c", 40),
		Fingerprint: strings.Repeat("b", 64), PromptSetSHA: promptSetSHA,
		PromptSHA: strings.Repeat("f", 64), PromptID: promptID, Expected: ActivationExpectNone,
		Hit: true, Valid: true, MaxTurns: 8,
		SeamarkCalls: 1, SeamarkToolCalls: map[string]int{"check": 1},
		AgentUsage: AgentUsage{RequestedModel: "model-a", Model: "model-a"},
	}

	for _, prompt := range prompts.Prompts {
		if prompt.ID == promptID {
			row.Expected = prompt.Expect
		}
	}

	if row.Expected != ActivationExpectNone {
		row.Activated = []string{row.Expected}
	}

	return row
}

// activationRowsFor writes one valid row per manifest prompt to path.
func activationRowsFor(t *testing.T, path string, prompts ...string) {
	t.Helper()

	for _, id := range prompts {
		require.NoError(t, appendJSONL(path, validActivationRow(id)))
	}
}

func TestActivationReportRequiresTheCompleteManifest(t *testing.T) {
	workflow := filepath.Join(t.TempDir(), "workflow.jsonl")
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPSkills, 1, true)))
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPOnly, 1, false)))

	build := func(t *testing.T, path string) (WorkflowReport, error) {
		t.Helper()

		return BuildWorkflowReport([]string{workflow},
			ActivationInputs{Paths: []string{path}, Prompts: testPromptSet()}, testWorkflowRegistry())
	}

	t.Run("complete run passes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		activationRowsFor(t, path, "understand-api", "plan-field", "review-diff", "typo")

		report, err := build(t, path)
		require.NoError(t, err)
		assert.Empty(t, report.Activation.Missing)
		assert.Equal(t, "passes frozen criteria", report.Activation.Status)
	})

	t.Run("a run that stopped early is insufficient", func(t *testing.T) {
		// Perfect recall and no false activation on the prompts reached,
		// yet no verdict: the manifest was not covered.
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		activationRowsFor(t, path, "understand-api", "plan-field", "typo")

		report, err := build(t, path)
		require.NoError(t, err)
		assert.Equal(t, []string{"review-diff"}, report.Activation.Missing)
		assert.Equal(t, "insufficient evidence", report.Activation.Status)
		assert.Contains(t, strings.Join(report.Activation.Reasons, "; "), "no valid session for prompt(s) review-diff")
		assert.Contains(t, report.Markdown(), "Prompts without a valid session: `review-diff`.")
	})

	t.Run("an invalid session does not cover its prompt", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		activationRowsFor(t, path, "understand-api", "plan-field", "review-diff")
		failed := validActivationRow("typo")
		failed.Valid, failed.InfrastructureFailure, failed.InvalidReason = false, true, "agent provider error HTTP 429"
		require.NoError(t, appendJSONL(path, failed))

		report, err := build(t, path)
		require.NoError(t, err)
		assert.Equal(t, []string{"typo"}, report.Activation.Missing)
		assert.Equal(t, "insufficient evidence", report.Activation.Status)
	})

	t.Run("another manifest is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		row := validActivationRow("plan-field")
		row.PromptSetSHA = strings.Repeat("9", 64)
		require.NoError(t, appendJSONL(path, row))

		_, err := build(t, path)
		require.ErrorContains(t, err, "measured against prompt set")
	})

	t.Run("a prompt outside the manifest is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		row := validActivationRow("plan-field")
		row.PromptID = "ghost"
		require.NoError(t, appendJSONL(path, row))

		_, err := build(t, path)
		require.ErrorContains(t, err, `prompt "ghost", which is not in the manifest`)
	})

	t.Run("another expectation is refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		row := validActivationRow("typo")
		row.Expected, row.Activated = "seamark-plan-change", []string{"seamark-plan-change"}
		require.NoError(t, appendJSONL(path, row))

		_, err := build(t, path)
		require.ErrorContains(t, err, `the manifest says "none"`)
	})
}

func TestReadActivationRowsRejectsTrailingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activation.jsonl")
	data, err := json.Marshal(validActivationRow("plan-field"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(append(data, " {}"...), '\n'), 0o600))

	_, _, err = ReadActivationRows(path)
	require.ErrorContains(t, err, path+":1: trailing content")
}

func TestValidateActivationRowRejectsContradictions(t *testing.T) {
	assert.NoError(t, ValidateActivationRow(validActivationRow("plan-field")))

	for name, mutate := range map[string]func(*ActivationRow){
		"schema":         func(r *ActivationRow) { r.SchemaVersion = 2 },
		"hit":            func(r *ActivationRow) { r.Hit = false },
		"none with hit":  func(r *ActivationRow) { r.Expected = ActivationExpectNone },
		"prepare":        func(r *ActivationRow) { r.Prepare = "gold" },
		"valid failure":  func(r *ActivationRow) { r.InfrastructureFailure = true },
		"calls sum":      func(r *ActivationRow) { r.SeamarkCalls = 2 },
		"prompt sha":     func(r *ActivationRow) { r.PromptSHA = "short" },
		"transcript":     func(r *ActivationRow) { r.Transcript = "t.jsonl" },
		"context":        func(r *ActivationRow) { r.ContextTokens = 1 },
		"negative turns": func(r *ActivationRow) { r.MaxTurns = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			row := validActivationRow("plan-field")
			mutate(&row)
			require.Error(t, ValidateActivationRow(row))
		})
	}
}

func TestActivationReportRefusesMixedIdentities(t *testing.T) {
	workflow := filepath.Join(t.TempDir(), "workflow.jsonl")
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPSkills, 1, true)))
	require.NoError(t, appendJSONL(workflow, validWorkflowRow("run-a", ArmMCPOnly, 1, false)))

	cases := map[string]func(*ActivationRow){
		"fingerprint":     func(r *ActivationRow) { r.Fingerprint = strings.Repeat("e", 64) },
		"prompt set":      func(r *ActivationRow) { r.PromptSetSHA = strings.Repeat("e", 64) },
		"requested model": func(r *ActivationRow) { r.RequestedModel = "model-b" },
		"max turns":       func(r *ActivationRow) { r.MaxTurns = 3 },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			calibration := filepath.Join(t.TempDir(), "calibration.jsonl")
			cohort := filepath.Join(t.TempDir(), "cohort.jsonl")
			other := validActivationRow("plan-field")
			mutate(&other)
			require.NoError(t, appendJSONL(calibration, other))
			require.NoError(t, appendJSONL(cohort, validActivationRow("plan-field")))

			_, err := BuildWorkflowReport([]string{workflow},
				ActivationInputs{Paths: []string{calibration, cohort}, Prompts: testPromptSet()}, testWorkflowRegistry())
			require.ErrorContains(t, err, "mix experiment identities")
		})
	}

	t.Run("observed model", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		other := validActivationRow("plan-field")
		other.Model = "model-b"
		require.NoError(t, appendJSONL(path, validActivationRow("review-diff")))
		require.NoError(t, appendJSONL(path, other))

		_, err := BuildWorkflowReport([]string{workflow},
			ActivationInputs{Paths: []string{path}, Prompts: testPromptSet()}, testWorkflowRegistry())
		require.ErrorContains(t, err, "conflicting observed models")
	})

	t.Run("one identity renders", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "activation.jsonl")
		activationRowsFor(t, path, "understand-api", "plan-field", "review-diff", "typo")

		report, err := BuildWorkflowReport([]string{workflow},
			ActivationInputs{Paths: []string{path}, Prompts: testPromptSet()}, testWorkflowRegistry())
		require.NoError(t, err)
		assert.Equal(t, 8, report.Activation.MaxTurns)
		assert.Contains(t, report.Markdown(), "model requested `model-a`, observed `model-a`; turn cap 8 turns")
	})
}
