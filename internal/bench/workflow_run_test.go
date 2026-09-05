package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/skills"
)

func TestSetDifferencePreservesFirstMismatch(t *testing.T) {
	for _, tc := range []struct {
		name               string
		expected, observed []string
		want               string
	}{
		{"empty sets", nil, nil, ""},
		{"order and repeats", []string{"Read", "Edit", "Read"}, []string{"Edit", "Read", "Edit"}, ""},
		{"empty observed", []string{"Read"}, nil, "agent initialized without the required tool set"},
		{"unexpected wins over missing", []string{"Read", "Edit"}, []string{"Write", "Bash"}, "unexpected tool loaded: Write"},
		{"first missing", []string{"Read", "Edit", "Write"}, []string{"Write"}, "required tool missing: Read"},
		{"nothing expected", nil, []string{"Read"}, "unexpected tool loaded: Read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, setDifference("tool", tc.expected, tc.observed))
		})
	}
}

func TestRunWorkflowMeansExcludeUnmeasuredAndInvalidRows(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
if [ -d .claude/skills ]; then
  echo '`+skillsArmInit+`'
else
  echo '`+onlyArmInit+`'
fi
case "$PWD" in
  *mcp-skills-01) tokens=100 ;;
  *mcp-skills-02) tokens=301 ;;
  *mcp-only-01) tokens=20 ;;
  *mcp-only-02) tokens=0 ;;
  *) echo '{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}'; tokens=9000 ;;
esac
printf '{"type":"result","subtype":"success","usage":{"input_tokens":%s}}\n' "$tokens"
`), 0o755))
	work := t.TempDir()
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	sum, err := RunWorkflow(context.Background(), WorkflowConfig{
		Trials: 3, Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{stub}),
		SeamarkBin: "/opt/seamark/bin/seamark", WorkDir: work, Out: out,
		Model: "claude-test", RequireStructuredResult: true, RequireExpectedInit: true,
	})
	require.ErrorContains(t, err, "benchmark stopped:")
	require.Len(t, sum.Rows, 5)
	assert.Equal(t, int64(200), sum.ByArm[ArmMCPSkills].MeanInput, "positive measurements use integer division")
	assert.Equal(t, int64(20), sum.ByArm[ArmMCPOnly].MeanInput, "zero usage does not dilute the mean")
	assert.Equal(t, 2, sum.ByArm[ArmMCPSkills].Ran)
	assert.Equal(t, 1, sum.ByArm[ArmMCPSkills].Invalid)
	for i, arm := range []WorkflowArm{ArmMCPSkills, ArmMCPOnly, ArmMCPOnly, ArmMCPSkills, ArmMCPSkills} {
		assert.Equal(t, arm, sum.Rows[i].Arm)
		assert.NoDirExists(t, filepath.Join(work, fmt.Sprintf("%s-%02d", arm, sum.Rows[i].Trial)))
	}
	rows, _, readErr := ReadWorkflowRowsStrict(out)
	require.NoError(t, readErr)
	assert.Equal(t, sum.Rows, rows, "the failed session is retained as evidence")
}

// canned transcripts: one per arm, differing in the init record and in
// whether a skill activates.
const (
	skillsArmInit = `{"type":"system","subtype":"init","model":"claude-test","tools":["Bash","Edit","Read","Write","mcp__seamark__orient","mcp__seamark__why","mcp__seamark__change_set","mcp__seamark__check","mcp__seamark__expand","Skill"],"mcp_servers":[{"name":"seamark","status":"connected"}],"skills":["seamark-plan-change","seamark-review-change","seamark-understand-repo"],"plugins":[]}`
	onlyArmInit   = `{"type":"system","subtype":"init","model":"claude-test","tools":["Bash","Edit","Read","Write","mcp__seamark__orient","mcp__seamark__why","mcp__seamark__change_set","mcp__seamark__check","mcp__seamark__expand"],"mcp_servers":[{"name":"seamark","status":"connected"}],"skills":[],"plugins":[]}`
	resultLine    = `{"type":"result","subtype":"success","is_error":false,"duration_ms":9,"num_turns":6,"total_cost_usd":0.02,"modelUsage":{"claude-test":{}},"usage":{"input_tokens":50,"output_tokens":5,"cache_read_input_tokens":150}}`
)

func skillsArmTranscript() []byte {
	return transcriptLines(
		skillsArmInit,
		toolUseLine("t1", "Skill", `{"skill":"seamark-plan-change"}`),
		toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py","server/presenters.py"]}`),
		toolResultLine("t2", `[{"type":"text","text":"`+changeSetResultText+`"}]`),
		toolUseLine("t3", "mcp__seamark__why", `{"query":"web/src/api/generated.ts"}`),
		toolUseLine("t4", "Edit", `{"file_path":"server/schema.py"}`),
		toolUseLine("t5", "mcp__seamark__check", `{}`),
		toolResultLine("t5", `"verdict  allow (mode: warn)\n"`),
		resultLine,
	)
}

func onlyArmTranscript() []byte {
	return transcriptLines(
		onlyArmInit,
		toolUseLine("t1", "Edit", `{"file_path":"server/schema.py"}`),
		resultLine,
	)
}

// cannedAgent writes a stub agent that records its arguments in the trial
// directory and prints the transcript for the arm it finds itself in: the
// skills arm is the one with .claude/skills installed.
func cannedAgent(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skills.jsonl"), skillsArmTranscript(), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "only.jsonl"), onlyArmTranscript(), 0o644))

	stub := filepath.Join(dir, "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
printf '%s\n' "$@" > argv.txt
if [ -d .claude/skills ]; then cat "`+dir+`/skills.jsonl"; else cat "`+dir+`/only.jsonl"; fi
`), 0o755))

	return stub
}

func TestRunWorkflowPairsArmsAndReadsTheTrace(t *testing.T) {
	work := filepath.Join(t.TempDir(), "trials")
	out := filepath.Join(t.TempDir(), "rows.jsonl")
	transcripts := filepath.Join(t.TempDir(), "transcripts")

	var logged []string
	sum, err := RunWorkflow(context.Background(), WorkflowConfig{
		Trials: 1, Instance: SchemaSyncCochangeInstance(),
		AgentArgv:  SameAgentArgv([]string{cannedAgent(t)}),
		SeamarkBin: "/opt/seamark/bin/seamark", WorkDir: work, Keep: true,
		Out: out, TranscriptDir: transcripts, Model: "claude-test",
		RequireStructuredResult: true, RequireExpectedInit: true,
		Log: func(format string, args ...any) { logged = append(logged, format) },
	})
	require.NoError(t, err)
	require.Len(t, sum.Rows, 2)
	assert.Len(t, logged, 2)

	byArm := map[WorkflowArm]WorkflowRow{}
	for _, row := range sum.Rows {
		byArm[row.Arm] = row
		assert.True(t, row.Valid, row.InvalidReason)
		assert.True(t, row.PairValid)
		assert.NoError(t, ValidateWorkflowRow(row), row.Arm)
		assert.Equal(t, "claude-test", row.Model)
		assert.Equal(t, schemaSyncTrigger, row.Trigger)
		assert.Equal(t, schemaSyncCompanion, row.Companion)
		assert.False(t, row.TaskDone, "the stub edits nothing")
		assert.FileExists(t, row.Transcript)
		assert.True(t, validSHA256(row.TranscriptSHA))
		assert.Equal(t, int64(200), row.ContextTokens)
	}

	withSkills := byArm[ArmMCPSkills]
	assert.True(t, withSkills.ChangeSetBeforeFirstEdit)
	assert.True(t, withSkills.CompanionNamedByChangeSet)
	assert.True(t, withSkills.WhyFollowedCompanion)
	assert.True(t, withSkills.CheckAfterLastEdit)
	assert.Equal(t, "allow (mode: warn)", withSkills.CheckVerdict)
	assert.Equal(t, []string{"seamark-plan-change"}, withSkills.Activations)
	assert.Equal(t, 3, withSkills.SeamarkCalls)
	assert.Equal(t, []string{"seamark-plan-change", "seamark-review-change", "seamark-understand-repo"}, withSkills.Skills)

	only := byArm[ArmMCPOnly]
	assert.False(t, only.ChangeSetBeforeFirstEdit)
	assert.Zero(t, only.SeamarkCalls)
	assert.Empty(t, only.Activations)
	assert.Empty(t, only.Skills)

	// The runner appended the trial's MCP configuration, its settings file,
	// and the task last. The boolean flag must sit between the variadic
	// --mcp-config and the prompt, or the Claude CLI reads the prompt as a
	// second configuration.
	argv, err := os.ReadFile(filepath.Join(work, "mcp-skills-01", "argv.txt"))
	require.NoError(t, err)
	args := strings.Split(strings.TrimSpace(string(argv)), "\n")
	require.GreaterOrEqual(t, len(args), 5)
	assert.Equal(t, "--mcp-config", args[0])
	assert.Contains(t, args[1], `"command":"/opt/seamark/bin/seamark"`)
	assert.Contains(t, args[1], `"-C"`)
	assert.Contains(t, args[1], `"mcp"`)
	assert.Equal(t, "--settings", args[2])
	assert.Equal(t, filepath.Join(work, "mcp-skills-01", ".claude", "settings.json"), args[3])
	assert.Equal(t, "--strict-mcp-config", args[4])
	assert.Equal(t, agentPrompt(SchemaSyncInstance().Task), strings.Join(args[5:], "\n"))

	skillsTally, onlyTally := sum.ByArm[ArmMCPSkills], sum.ByArm[ArmMCPOnly]
	assert.Equal(t, 1, skillsTally.Ran)
	assert.Equal(t, 1, skillsTally.ChangeSetFirst)
	assert.Equal(t, 1, skillsTally.CompanionNamed)
	assert.Equal(t, 1, skillsTally.WhyFollowed)
	assert.Equal(t, 1, skillsTally.CheckLast)
	assert.Equal(t, map[string]int{"seamark-plan-change": 1}, skillsTally.Activations)
	assert.Equal(t, 1, onlyTally.Ran)
	assert.Zero(t, onlyTally.ChangeSetFirst)

	lines := strings.Join(sum.Lines(), "\n")
	assert.Contains(t, lines, SchemaSyncCochangeInstanceID+" — mcp-skills: 0/1 avoided (0/1 completed); mcp-only: 0/1 avoided (0/1 completed)")
	assert.Contains(t, lines, "mcp-skills process — change_set before first edit 1/1, companion named 1/1, why followed 1/1, check after last edit 1/1, 3 seamark calls")
	assert.Contains(t, lines, "mcp-skills activations — seamark-plan-change×1")
	assert.Contains(t, lines, "note — no seamark tool call in any mcp-only trial")

	rows, err := ReadWorkflowRows(out)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, sum.Rows[0].RunID, rows[0].RunID)

	prior, meanInput, meanCost, ok := PriorWorkflowCostFor(out, sum.Rows[0].Fingerprint)
	require.True(t, ok)
	assert.Equal(t, 2, prior)
	assert.Equal(t, int64(200), meanInput)
	assert.InDelta(t, 0.02, meanCost, 1e-9)
}

func TestWireWorkflowArmsInstallExactlyTheirCondition(t *testing.T) {
	instance := SchemaSyncCochangeInstance()
	cfg := WorkflowConfig{SeamarkBin: "/opt/seamark/bin/seamark"}
	dirs := map[WorkflowArm]string{}

	for _, arm := range []WorkflowArm{ArmMCPOnly, ArmMCPSkills} {
		dir := filepath.Join(t.TempDir(), string(arm))
		require.NoError(t, instance.Generate(dir))

		bin, err := wireWorkflowArm(context.Background(), dir, cfg, arm)
		require.NoError(t, err)
		assert.Equal(t, cfg.SeamarkBin, bin, "hermetic wiring keeps the inert binary path")
		dirs[arm] = dir

		settings, err := readTrialSettings(dir)
		require.NoError(t, err)
		assert.NotContains(t, settings, "hooks")
		assert.NoFileExists(t, filepath.Join(dir, ".seamark", "lessons.yaml"))
		assert.NoFileExists(t, filepath.Join(dir, ".mcp.json"))

		// Claude Code's built-in skills are off in both arms, so the init
		// record can list only the seamark skills.
		assert.Equal(t, true, settings["disableBundledSkills"])
		assert.Equal(t, map[string]any{"doctor": "off"}, settings["skillOverrides"])

		// The sandbox block is the lessons writer's own output.
		lessons := filepath.Join(t.TempDir(), "lessons")
		require.NoError(t, writeAgentSettings(lessons, "", ""))
		expected, err := readTrialSettings(lessons)
		require.NoError(t, err)
		for _, key := range []string{"permissions", "disableBundledSkills", "skillOverrides"} {
			delete(settings, key)
			delete(expected, key)
		}
		assert.Equal(t, expected, settings)

		exclude, err := os.ReadFile(filepath.Join(dir, ".git", "info", "exclude"))
		require.NoError(t, err)
		assert.Contains(t, string(exclude), ".claude/")
	}

	onlySettings, err := readTrialSettings(dirs[ArmMCPOnly])
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{
		"mcp__seamark__orient": true, "mcp__seamark__why": true, "mcp__seamark__change_set": true,
		"mcp__seamark__check": true, "mcp__seamark__expand": true,
	}, allowRules(onlySettings))
	assert.NoDirExists(t, filepath.Join(dirs[ArmMCPOnly], ".claude", "skills"))

	skillsSettings, err := readTrialSettings(dirs[ArmMCPSkills])
	require.NoError(t, err)
	rules := allowRules(skillsSettings)
	assert.Len(t, rules, 8)
	for _, name := range mustSkillNames(t) {
		assert.True(t, rules["Skill("+name+")"], name)
		assert.FileExists(t, filepath.Join(dirs[ArmMCPSkills], ".claude", "skills", name, "SKILL.md"))
	}

	require.NoError(t, validateWorkflowWiring(dirs[ArmMCPOnly], dirs[ArmMCPSkills]))
}

func mustSkillNames(t *testing.T) []string {
	t.Helper()

	names, err := skills.Names()
	require.NoError(t, err)

	return names
}

func TestValidateWorkflowSessionRejectsUndeliveredArms(t *testing.T) {
	cfg := WorkflowConfig{Model: "claude-test", RequireStructuredResult: true, RequireExpectedInit: true}

	valid := func(arm WorkflowArm) WorkflowRow {
		row := WorkflowRow{Valid: true, InitSeen: true, ResultSeen: true,
			Tools:      WorkflowTools(arm),
			MCPServers: []MCPServerState{{Name: "seamark", Status: "connected"}},
			AgentUsage: AgentUsage{Model: "claude-test"},
		}
		if arm == ArmMCPSkills {
			row.Skills = mustSkillNames(t)
		}

		return row
	}

	for _, arm := range []WorkflowArm{ArmMCPOnly, ArmMCPSkills} {
		row := valid(arm)
		validateWorkflowSession(cfg, arm, &row)
		assert.True(t, row.Valid, "%s: %s", arm, row.InvalidReason)
	}

	cases := []struct {
		name   string
		arm    WorkflowArm
		mutate func(*WorkflowRow)
		want   string
	}{
		{"no init", ArmMCPOnly, func(r *WorkflowRow) { r.InitSeen = false }, "no initialization record"},
		{"plugin", ArmMCPOnly, func(r *WorkflowRow) { r.Plugins = []string{"extra"} }, "unexpected agent plugins"},
		{"no mcp server", ArmMCPOnly, func(r *WorkflowRow) { r.MCPServers = nil }, "seamark MCP server missing"},
		{"other mcp server", ArmMCPOnly, func(r *WorkflowRow) {
			r.MCPServers = append(r.MCPServers, MCPServerState{Name: "other", Status: "connected"})
		}, "unexpected MCP servers"},
		{"disconnected", ArmMCPOnly, func(r *WorkflowRow) { r.MCPServers[0].Status = "failed" }, "not connected: failed"},
		{"extra tool", ArmMCPOnly, func(r *WorkflowRow) { r.Tools = append(r.Tools, "Skill") }, "unexpected agent tool loaded: Skill"},
		{"missing tool", ArmMCPOnly, func(r *WorkflowRow) { r.Tools = r.Tools[1:] }, "required agent tool missing: Read"},
		{"no tools", ArmMCPOnly, func(r *WorkflowRow) { r.Tools = nil }, "without the required agent tool set"},
		{"skill in mcp-only", ArmMCPOnly, func(r *WorkflowRow) { r.Skills = []string{"seamark-plan-change"} }, "unexpected skill loaded"},
		{"missing skill", ArmMCPSkills, func(r *WorkflowRow) { r.Skills = r.Skills[:2] }, "required skill missing"},
		{"no skills", ArmMCPSkills, func(r *WorkflowRow) { r.Skills = nil }, "without the required skill set"},
		{"foreign skill", ArmMCPSkills, func(r *WorkflowRow) { r.Skills = append(r.Skills, "my-skill") }, "unexpected skill loaded: my-skill"},
		{"no result", ArmMCPSkills, func(r *WorkflowRow) { r.ResultSeen = false }, "no structured result"},
		{"model", ArmMCPSkills, func(r *WorkflowRow) { r.Model = "claude-other" }, "requested model"},
		{"seamark tool denied", ArmMCPOnly, func(r *WorkflowRow) {
			r.DeniedTools = []string{"WebFetch", "mcp__seamark__orient"}
		}, "denied mcp__seamark__orient: the allow rules were not in effect"},
		{"skill tool denied", ArmMCPSkills, func(r *WorkflowRow) { r.DeniedTools = []string{"Skill"} }, "denied Skill"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := valid(tc.arm)
			tc.mutate(&row)
			validateWorkflowSession(cfg, tc.arm, &row)

			assert.False(t, row.Valid)
			assert.True(t, row.InfrastructureFailure)
			assert.Contains(t, row.InvalidReason, tc.want)
		})
	}

	t.Run("a refused WebFetch is a measured outcome", func(t *testing.T) {
		row := valid(ArmMCPOnly)
		row.DeniedTools = []string{"WebFetch"}
		validateWorkflowSession(cfg, ArmMCPOnly, &row)
		assert.True(t, row.Valid)
	})

	t.Run("timeout keeps a row without result valid", func(t *testing.T) {
		row := valid(ArmMCPOnly)
		row.ResultSeen = false
		row.TimedOut = true
		validateWorkflowSession(cfg, ArmMCPOnly, &row)
		assert.True(t, row.Valid)
	})

	t.Run("custom adapters skip the init rules", func(t *testing.T) {
		row := WorkflowRow{Valid: true}
		validateWorkflowSession(WorkflowConfig{}, ArmMCPSkills, &row)
		assert.True(t, row.Valid)
	})
}

// TestRecordUnrunChecksKeepsTheRowReadable covers the path where the checks
// cannot start: the row must stay valid for the strict reader, list every
// configured check, and count the task as not done.
func TestRecordUnrunChecksKeepsTheRowReadable(t *testing.T) {
	row := validWorkflowRow("run-a", ArmMCPSkills, 1, true)
	row.Valid, row.PairValid = true, false
	row.Checks = nil
	row.ChecksPass = false

	instance := SchemaSyncCochangeInstance()
	recordUnrunChecks(&row, instance.Checks, assert.AnError)

	assert.False(t, row.Valid)
	assert.True(t, row.InfrastructureFailure)
	assert.Contains(t, row.InvalidReason, "cannot run repository checks")
	assert.False(t, row.TaskDone)
	assert.False(t, row.Avoided)
	assert.False(t, row.ChecksPass)
	require.Len(t, row.Checks, len(instance.Checks))
	for i, check := range row.Checks {
		assert.Equal(t, instance.Checks[i].String(), check.Command)
		assert.False(t, check.Pass)
		assert.Contains(t, check.Output, "not run: "+assert.AnError.Error())
	}

	assert.Contains(t, row.Notes, "repository checks could not run")
	require.NoError(t, ValidateWorkflowRow(row), "the invalid row must still pass the strict reader")
}

func TestRunWorkflowStopsOnInfrastructureFailure(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
echo '`+onlyArmInit+`'
echo '{"type":"result","is_error":true,"api_error_status":429,"result":"rate limited","usage":{}}'
`), 0o755))

	sum, err := RunWorkflow(context.Background(), WorkflowConfig{
		Trials: 3, Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{stub}),
		SeamarkBin: "/opt/seamark/bin/seamark", Model: "claude-test",
		RequireStructuredResult: true, RequireExpectedInit: true,
	})
	require.Error(t, err)
	require.Len(t, sum.Rows, 1, "provider failure stops before spending the paired arm")
	assert.Equal(t, ArmMCPSkills, sum.Rows[0].Arm, "the skills arm runs first in odd trials")
	assert.False(t, sum.Rows[0].PairValid)
	assert.Equal(t, 1, sum.ByArm[ArmMCPSkills].Invalid)
	assert.Contains(t, sum.StoppedReason, "HTTP 429")
	assert.Contains(t, strings.Join(sum.Lines(), "\n"), "stopped — agent provider error HTTP 429")
}

func TestRunWorkflowRejectsBadConfigurationBeforeStarting(t *testing.T) {
	instance := SchemaSyncCochangeInstance()
	argv := SameAgentArgv([]string{noopAgent(t)})

	for name, cfg := range map[string]WorkflowConfig{
		"no trials":       {Instance: instance, AgentArgv: argv},
		"unknown arm":     {Trials: 1, Instance: instance, AgentArgv: argv, Arms: []WorkflowArm{"future"}},
		"duplicate arm":   {Trials: 1, Instance: instance, AgentArgv: argv, Arms: []WorkflowArm{ArmMCPOnly, ArmMCPOnly}},
		"missing command": {Trials: 1, Instance: instance, AgentArgv: map[WorkflowArm][]string{ArmMCPOnly: {"x"}}},
		"no instance":     {Trials: 1, AgentArgv: argv},
		"bad run id":      {Trials: 1, Instance: instance, AgentArgv: argv, RunID: "../escape"},
		"fingerprint":     {Trials: 1, Instance: instance, AgentArgv: argv, Fingerprint: strings.Repeat("0", 64)},
	} {
		t.Run(name, func(t *testing.T) {
			work := t.TempDir()
			cfg.WorkDir = work
			cfg.Keep = true

			sum, err := RunWorkflow(context.Background(), cfg)
			require.Error(t, err)
			assert.Empty(t, sum.Rows)

			entries, readErr := os.ReadDir(work)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "an invalid configuration must not create a trial")
		})
	}
}

// TestRunWorkflowTreatsInterruptedSessionAsCancellation checks that Ctrl-C
// during a session ends the run as a cancellation: no row for the killed
// session, no stop reason, no error.
func TestRunWorkflowTreatsInterruptedSessionAsCancellation(t *testing.T) {
	hang := filepath.Join(t.TempDir(), "hang.sh")
	require.NoError(t, os.WriteFile(hang, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755))
	out := filepath.Join(t.TempDir(), "rows.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(300*time.Millisecond, cancel)

	started := time.Now()
	sum, err := RunWorkflow(ctx, WorkflowConfig{
		Trials: 2, Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{hang}),
		SeamarkBin: "/opt/seamark/bin/seamark", Out: out, Model: "claude-test",
		RequireStructuredResult: true, RequireExpectedInit: true,
	})
	require.NoError(t, err, "an interrupt is a stop, not a failure")
	assert.Empty(t, sum.Rows, "a killed session is not a measurement")
	assert.Empty(t, sum.StoppedReason)
	assert.NoFileExists(t, out)
	assert.Less(t, time.Since(started), 5*time.Second)
}

func TestRunWorkflowStopsCleanlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sum, err := RunWorkflow(ctx, WorkflowConfig{
		Trials: 3, Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{noopAgent(t)}),
	})
	require.NoError(t, err, "a cancelled run reports partial results, not an error")
	assert.Empty(t, sum.Rows)
}

func TestClaudeArgvAndWorkflowTools(t *testing.T) {
	argv := ClaudeArgv("claude-haiku-4-5-20251001", "medium", 0.25, WorkflowTools(ArmMCPSkills))
	joined := strings.Join(argv, " ")

	assert.Equal(t, "claude", argv[0])
	assert.NotContains(t, joined, "--disable-slash-commands", "the flag would hide project skills")
	assert.NotContains(t, joined, "--mcp-config", "the runner appends the per-trial configuration")
	assert.Contains(t, joined, "--model claude-haiku-4-5-20251001 --effort medium --max-budget-usd 0.25")
	assert.Contains(t, joined, "--setting-sources project")
	assert.Contains(t, joined, "--tools Read,Edit,Write,Bash,mcp__seamark__orient,mcp__seamark__why,mcp__seamark__change_set,mcp__seamark__check,mcp__seamark__expand,Skill")
	assert.Contains(t, joined, "--output-format stream-json")

	assert.Equal(t, []string{
		"Read", "Edit", "Write", "Bash", "mcp__seamark__orient", "mcp__seamark__why",
		"mcp__seamark__change_set", "mcp__seamark__check", "mcp__seamark__expand",
	}, WorkflowTools(ArmMCPOnly))

	for model, exact := range map[string]bool{
		"claude-haiku-4-5-20251001": true, "claude-opus-5": true,
		"haiku": false, "claude-3-5-sonnet-latest": false, "": false, "gpt-5": false,
	} {
		assert.Equal(t, exact, ExactModelID(model), model)
	}
}

func TestTrialMCPConfigNamesTheTrialBinary(t *testing.T) {
	var config struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}

	require.NoError(t, json.Unmarshal([]byte(trialMCPConfig("/trial/.seamark/bin/seamark", "/trial")), &config))
	require.Contains(t, config.Servers, "seamark")
	assert.Equal(t, "/trial/.seamark/bin/seamark", config.Servers["seamark"].Command)
	assert.Equal(t, []string{"-C", "/trial", "mcp"}, config.Servers["seamark"].Args)
}

func TestWorkflowFingerprintBindsTheExperiment(t *testing.T) {
	base := WorkflowConfig{Instance: SchemaSyncCochangeInstance(), AgentArgv: SameAgentArgv([]string{"agent"})}
	baseFingerprint, err := WorkflowFingerprint(base)
	require.NoError(t, err)
	assert.True(t, validSHA256(baseFingerprint))

	again, err := WorkflowFingerprint(base)
	require.NoError(t, err)
	assert.Equal(t, baseFingerprint, again, "the fingerprint is deterministic")

	explicitArms := base
	explicitArms.Arms = []WorkflowArm{ArmMCPSkills, ArmMCPOnly}
	explicitFingerprint, err := WorkflowFingerprint(explicitArms)
	require.NoError(t, err)
	assert.Equal(t, baseFingerprint, explicitFingerprint, "implicit and explicit default arms are one experiment")

	for name, mutate := range map[string]func(*WorkflowConfig){
		"instance":    func(c *WorkflowConfig) { c.Instance = CacheVersionCochangeInstance() },
		"one arm":     func(c *WorkflowConfig) { c.Arms = []WorkflowArm{ArmMCPOnly} },
		"arm command": func(c *WorkflowConfig) { c.AgentArgv[ArmMCPSkills] = []string{"agent", "--tools", "Skill"} },
		"model":       func(c *WorkflowConfig) { c.Model = "claude-test" },
		"effort":      func(c *WorkflowConfig) { c.Effort = "high" },
		"budget":      func(c *WorkflowConfig) { c.MaxBudgetUSD = 1 },
		"seamark":     func(c *WorkflowConfig) { c.SeamarkSHA = strings.Repeat("a", 64) },
		"index":       func(c *WorkflowConfig) { c.PrepareIndex = true },
		"init rule":   func(c *WorkflowConfig) { c.RequireExpectedInit = true },
		"result rule": func(c *WorkflowConfig) { c.RequireStructuredResult = true },
		"companion":   func(c *WorkflowConfig) { c.Instance.Companion = "web/src/workspaces/card.ts" },
		"judge":       func(c *WorkflowConfig) { c.Instance.JudgeVersion = "other" },
		"max turns":   func(c *WorkflowConfig) { c.MaxTurns = 8 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.AgentArgv = SameAgentArgv([]string{"agent"})
			mutate(&cfg)

			fingerprint, err := WorkflowFingerprint(cfg)
			require.NoError(t, err)
			assert.NotEqual(t, baseFingerprint, fingerprint)
		})
	}

	// Operator conveniences stay outside the identity.
	convenience := base
	convenience.Keep = true
	convenience.WorkDir = "/tmp/elsewhere"
	convenience.Out = "/tmp/rows.jsonl"
	convenience.TranscriptDir = "/tmp/transcripts"
	convenienceFingerprint, err := WorkflowFingerprint(convenience)
	require.NoError(t, err)
	assert.Equal(t, baseFingerprint, convenienceFingerprint)
}

func TestWorkflowFingerprintSourceBoundary(t *testing.T) {
	entries, err := workflowSources.ReadDir(".")
	require.NoError(t, err)

	names := map[string]bool{}
	for _, entry := range entries {
		names[entry.Name()] = true
		assert.False(t, strings.HasSuffix(entry.Name(), "_test.go"), "tests must not move the fingerprint")
	}

	for _, name := range []string{
		"adapter.go", "workflow_run.go", "workflow_trace.go", "workflow_results.go",
		"workflow_preflight.go", "workflow_fingerprint.go", "workflow_activation.go",
	} {
		assert.True(t, names[name], "%s must bind the run identity", name)
	}

	for _, name := range []string{"workflow_report.go", "workflow_claims.go", "workflow_instance.go", "run.go"} {
		assert.False(t, names[name], "%s must not move the workflow fingerprint (it is bound elsewhere or not at all)", name)
	}

	skillsSHA, err := fingerprintSkillsTree()
	require.NoError(t, err)
	assert.True(t, validSHA256(skillsSHA))

	// A lessons harness edit moves the workflow fingerprint too, because the
	// runner reuses that plumbing; the lessons digest is the same one.
	lessonsSHA, err := fingerprintHarnessSources()
	require.NoError(t, err)
	assert.True(t, validSHA256(lessonsSHA))
}

// TestReadAgentSessionRecordsDeniedTools proves the result record's
// permission_denials reach the row by name, once each, so the validator can
// tell a refused seamark tool from a refused WebFetch.
func TestReadAgentSessionRecordsDeniedTools(t *testing.T) {
	stdout := []byte(`{"type":"system","subtype":"init","model":"claude-test","tools":["Read"],"mcp_servers":[{"name":"seamark","status":"connected"}],"skills":[],"plugins":[]}
{"type":"result","subtype":"success","is_error":false,"num_turns":3,"total_cost_usd":0.01,"permission_denials":[{"tool_name":"mcp__seamark__orient","tool_use_id":"a","tool_input":{}},{"tool_name":"mcp__seamark__orient","tool_use_id":"b","tool_input":{}},{"tool_name":"WebFetch","tool_use_id":"c","tool_input":{}}]}
`)

	session := readAgentSession(stdout)
	assert.Equal(t, []string{"mcp__seamark__orient", "WebFetch"}, session.DeniedTools)

	var row WorkflowRow
	session.apply(&row)
	assert.Equal(t, []string{"mcp__seamark__orient", "WebFetch"}, row.DeniedTools)
	assert.Equal(t, 3, row.PermissionDenials)
	assert.Equal(t, "mcp__seamark__orient", deniedArmTool(row.DeniedTools))
	assert.Empty(t, deniedArmTool([]string{"WebFetch"}))
}
