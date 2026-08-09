package bench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func head(t *testing.T, dir string) string {
	t.Helper()

	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	require.NoError(t, err)

	return strings.TrimSpace(string(out))
}

// TestRunArmsInstallTheRightLesson checks the two optional arms: file-only
// gets the treatment lesson without a hook, while placebo gets the hook and a
// distinct lesson.
func TestRunArmsInstallTheRightLesson(t *testing.T) {
	work := filepath.Join(t.TempDir(), "trials")
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
if grep -q 'lessons --hook' .claude/settings.json; then
  mkdir -p .seamark
  echo '{"ts":"2026-01-05T12:01:00Z","file":"server/schema.py","tool":"Write","fired":[{"region":"server","symptom":"follow-service-conventions — Keep response construction explicit, preserve the existing wire-name style, and prefer focused tests that match the neighboring service modules."}]}' >> .seamark/lessons-audit.jsonl
fi
`), 0o755))

	_, err := Run(context.Background(), RunConfig{
		Trials:     1,
		Arms:       []Arm{ArmFileOnly, ArmPlacebo},
		AgentArgv:  []string{stub},
		SeamarkBin: "/opt/seamark/bin/seamark",
		WorkDir:    work,
		Keep:       true,
	})
	require.NoError(t, err)

	lesson, err := os.ReadFile(filepath.Join(work, "file-only-01", ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(lesson), SchemaSyncRule)

	fileOnlySettings, err := os.ReadFile(filepath.Join(work, "file-only-01", ".claude", "settings.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(fileOnlySettings), "PreToolUse",
		"file-only gets common sandbox settings but no hook")

	placebo, err := os.ReadFile(filepath.Join(work, "placebo-01", ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(placebo), "sync-api")
	assert.NotContains(t, string(placebo), "generated.ts")

	_, err = os.Stat(filepath.Join(work, "placebo-01", ".claude", "settings.json"))
	assert.NoError(t, err, "placebo wires the hook")
}

// TestRunRecordsTimeouts checks that a hung agent becomes a recorded
// outcome and the run continues to the next trial.
func TestRunRecordsTimeouts(t *testing.T) {
	// The runner appends the task as an argument, so the hang must
	// come from a script that ignores its arguments.
	hang := filepath.Join(t.TempDir(), "hang.sh")
	require.NoError(t, os.WriteFile(hang, []byte("#!/bin/sh\nsleep 5\n"), 0o755))

	sum, err := Run(context.Background(), RunConfig{
		Trials:    1,
		Arms:      []Arm{ArmHookOff},
		AgentArgv: []string{hang},
		Timeout:   200 * time.Millisecond,
	})
	require.NoError(t, err, "a timeout is an outcome, not a run failure")

	require.Len(t, sum.Rows, 1)
	assert.Equal(t, -1, sum.Rows[0].AgentExit)
	assert.True(t, sum.Rows[0].TimedOut)
	assert.Contains(t, sum.Rows[0].Notes, "timed out")
}

func TestSummaryWarnsWhenHookNeverFired(t *testing.T) {
	sum := Summary{Rule: SchemaSyncRule, ByArm: map[Arm]Tally{
		ArmHookOn:  {Ran: 2, Completed: 2, Avoided: 2},
		ArmHookOff: {Ran: 2, Completed: 2, Avoided: 2},
	}}

	lines := sum.Lines()
	require.Len(t, lines, 2)
	assert.Contains(t, lines[1], "warning — the hook never fired",
		"identical arms must never read as a pin result")
}

func TestRunStopsCleanlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sum, err := Run(ctx, RunConfig{Trials: 3, AgentArgv: []string{"/usr/bin/true"}})
	require.NoError(t, err, "a cancelled run reports partial results, not an error")
	assert.Empty(t, sum.Rows)
}

func TestRunPersistsCompletedArmWhenPairIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	instance := SchemaSyncInstance()
	realJudge := instance.Judge
	instance.Judge = func(dir string) (Verdict, error) {
		verdict, err := realJudge(dir)
		cancel()

		return verdict, err
	}
	out := filepath.Join(t.TempDir(), "results.jsonl")

	sum, err := Run(ctx, RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff, ArmFileOnly}, Instance: instance,
		AgentArgv: []string{"/usr/bin/true"}, Out: out,
	})
	require.NoError(t, err)
	require.Len(t, sum.Rows, 1)
	assert.Equal(t, ArmHookOff, sum.Rows[0].Arm)
	assert.False(t, sum.Rows[0].PairValid)
	assert.Equal(t, 1, sum.ByArm[ArmHookOff].Attempted)

	rows, readErr := ReadRows(out)
	require.NoError(t, readErr)
	require.Len(t, rows, 1, "the completed paid arm must survive cancellation")
	assert.Equal(t, sum.Rows[0].RunID, rows[0].RunID)
}

func TestRunPersistsCompletedArmWhenPairedSetupFails(t *testing.T) {
	instance := SchemaSyncInstance()
	realGenerate := instance.Generate
	generations := 0
	instance.Generate = func(dir string) error {
		generations++
		if generations == 2 {
			return assert.AnError
		}

		return realGenerate(dir)
	}
	out := filepath.Join(t.TempDir(), "results.jsonl")

	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff, ArmFileOnly}, Instance: instance,
		AgentArgv: []string{"/usr/bin/true"}, Out: out,
	})
	require.Error(t, err)
	require.Len(t, sum.Rows, 1)
	assert.False(t, sum.Rows[0].PairValid)

	rows, readErr := ReadRows(out)
	require.NoError(t, readErr)
	require.Len(t, rows, 1, "an earlier paid arm must be flushed before returning the setup error")
}

func TestParseAgentOutputStream(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"server/schema.py"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"... server/routes.py mentioned in a tool RESULT must not count ..."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":".seamark/lessons.yaml"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"python3 tools/sync_api.py --check server/schema.py"}}]}}`,
		`{"type":"result","modelUsage":{"claude-test-1":{}},"duration_ms":9,"total_cost_usd":0.02,"usage":{"input_tokens":50,"output_tokens":5,"cache_read_input_tokens":150}}`,
	}, "\n")

	var row Row
	parseAgentOutput([]byte(stream), &row, SchemaSyncInstance())

	// Reading the lesson file is flagged: in a control arm this means
	// contamination.
	assert.True(t, row.LessonFileRead)

	assert.Equal(t, "claude-test-1", row.Model)
	assert.Equal(t, int64(50), row.InputTokens)
	assert.Equal(t, int64(150), row.CacheReadTokens)
	assert.Equal(t, int64(200), row.ContextTokens)
	assert.Equal(t, int64(5), row.OutputTokens)
	assert.InDelta(t, 0.02, row.CostUSD, 1e-9)

	// First-mention order, deduped, and nothing from tool results.
	assert.Equal(t, []string{"server/schema.py", "tools/sync_api.py"}, row.Explored)
}

func TestRunSingleArm(t *testing.T) {
	sum, err := Run(context.Background(), RunConfig{
		Trials:    1,
		Arms:      []Arm{ArmHookOff},
		AgentArgv: []string{"/usr/bin/true"},
	})
	require.NoError(t, err)

	assert.Equal(t, 1, sum.ByArm[ArmHookOff].Ran)
	assert.Zero(t, sum.ByArm[ArmHookOn].Ran)

	// With no hook-on trials there is nothing to warn about.
	for _, line := range sum.Lines() {
		assert.NotContains(t, line, "warning")
	}
}

func TestPreflightValidatesBaseGoldAndWiring(t *testing.T) {
	err := Preflight(context.Background(), RunConfig{
		SeamarkBin: "/opt/seamark/bin/seamark",
		// A hermetic preflight test validates settings without invoking the
		// nonexistent fake binary for indexing.
		PrepareIndex: false,
	})
	require.NoError(t, err)
}

func TestParseAgentOutputKeepsPrimaryModelSeparateFromHelpers(t *testing.T) {
	stream := `{"type":"system","subtype":"init","model":"claude-opus-5[1m]","tools":["Read","Edit","Write","Bash"],"mcp_servers":[],"plugins":[]}` + "\n" +
		`{"type":"result","modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":10,"costUSD":0.01},"claude-opus-5[1m]":{"inputTokens":20,"costUSD":0.20}},"duration_ms":9,"num_turns":3,"total_cost_usd":0.21,"usage":{"input_tokens":30,"output_tokens":5,"cache_read_input_tokens":150,"cache_creation_input_tokens":20}}`

	row := Row{Valid: true, RequestedModel: "claude-opus-5"}
	parseAgentOutput([]byte(stream), &row, SchemaSyncInstance())
	validateAgentResult(RunConfig{
		Model: "claude-opus-5", RequireStructuredResult: true, RequireCleanInit: true,
	}, &row)

	assert.True(t, row.Valid)
	assert.Equal(t, "claude-opus-5[1m]", row.Model)
	assert.Len(t, row.ModelUsage, 2)
	assert.Equal(t, int64(30), row.InputTokens)
	assert.Equal(t, int64(150), row.CacheReadTokens)
	assert.Equal(t, int64(20), row.CacheCreationTokens)
	assert.Equal(t, int64(200), row.ContextTokens)
	assert.Equal(t, 3, row.Turns)
}

func TestParseAgentOutputRejectsProviderFailure(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-opus-5","tools":[],"mcp_servers":[],"plugins":[]}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","overageStatus":"rejected"}}`,
		`{"type":"result","is_error":true,"api_error_status":429,"result":"session limit","usage":{}}`,
	}, "\n")

	row := Row{Valid: true}
	parseAgentOutput([]byte(stream), &row, SchemaSyncInstance())

	assert.False(t, row.Valid)
	assert.True(t, row.InfrastructureFailure)
	assert.Contains(t, row.InvalidReason, "rate limit")
}

func TestParseAgentOutputAcceptsAllowedQuotaWithoutOverage(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-haiku-4-5-20251001","tools":["Read","Edit","Write","Bash"],"mcp_servers":[],"plugins":[]}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","overageStatus":"rejected","overageDisabledReason":"out_of_credits","isUsingOverage":false}}`,
		`{"type":"result","subtype":"success","is_error":false,"modelUsage":{"claude-haiku-4-5-20251001":{}},"usage":{"input_tokens":10}}`,
	}, "\n")

	row := Row{Valid: true}
	parseAgentOutput([]byte(stream), &row, SchemaSyncInstance())

	assert.True(t, row.Valid)
	assert.False(t, row.InfrastructureFailure)
	assert.Empty(t, row.InvalidReason)
	assert.True(t, row.ResultSeen)
}

func TestRunStopsAndExcludesInfrastructureFailure(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
echo '{"type":"system","subtype":"init","model":"claude-test","tools":[],"mcp_servers":[],"plugins":[]}'
echo '{"type":"result","is_error":true,"api_error_status":429,"result":"rate limited","usage":{}}'
`), 0o755))

	sum, err := Run(context.Background(), RunConfig{
		Trials: 3, AgentArgv: []string{stub}, Model: "claude-test",
		RequireStructuredResult: true, RequireCleanInit: true,
	})
	require.Error(t, err)
	require.Len(t, sum.Rows, 1, "provider failure stops before spending the paired arm")
	assert.False(t, sum.Rows[0].PairValid)
	assert.Equal(t, 1, sum.ByArm[ArmHookOn].Attempted)
	assert.Equal(t, 1, sum.ByArm[ArmHookOn].Invalid)
	assert.Zero(t, sum.ByArm[ArmHookOn].Ran)
	assert.Contains(t, sum.StoppedReason, "HTTP 429")
}

func TestAgentEnvironmentPinsCachesInsideTrial(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ANTHROPIC_MODEL", "unwanted")
	t.Setenv("CLAUDE_CODE_EFFORT_LEVEL", "max")
	t.Setenv("PYTHONPATH", "/host/python")
	t.Setenv("PYTHONHOME", "/host/python-home")
	t.Setenv("BASH_ENV", "/host/bash-env")
	t.Setenv("MAKEFLAGS", "--jobs=99")
	t.Setenv("VIRTUAL_ENV", "/host/venv")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")

	env := agentEnvironment(dir)
	joined := strings.Join(env, "\n")
	assert.NotContains(t, joined, "ANTHROPIC_MODEL=unwanted")
	assert.NotContains(t, joined, "CLAUDE_CODE_EFFORT_LEVEL=max")
	assert.NotContains(t, joined, "PYTHONPATH=/host/python")
	assert.NotContains(t, joined, "PYTHONHOME=/host/python-home")
	assert.NotContains(t, joined, "BASH_ENV=/host/bash-env")
	assert.NotContains(t, joined, "MAKEFLAGS=--jobs=99")
	assert.NotContains(t, joined, "VIRTUAL_ENV=/host/venv")
	assert.NotContains(t, joined, "GIT_CONFIG_COUNT=1")
	assert.NotContains(t, joined, "GIT_CONFIG_KEY_0=core.hooksPath")
	assert.Contains(t, joined, "GOCACHE="+filepath.Join(dir, ".bench-cache", "go-build"))
	assert.Contains(t, joined, "GOPROXY=off")
	assert.Contains(t, joined, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	assert.Contains(t, joined, "PYTHONNOUSERSITE=1")
	assert.Contains(t, joined, "PYTHONHASHSEED=0")
	assert.Contains(t, joined, "GIT_CONFIG_NOSYSTEM=1")
	assert.Contains(t, joined, "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func TestWireHookHonorsParentDeadline(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(t.TempDir(), "seamark")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	err := wireHook(ctx, dir, RunConfig{PrepareIndex: true, SeamarkBin: bin}, bin)
	require.Error(t, err)
	assert.Less(t, time.Since(started), time.Second)
}

func TestRunJudgeCommandIgnoresStalePythonBytecode(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "fixture_module.py")
	require.NoError(t, os.WriteFile(source, []byte("VALUE = 1\n"), 0o644))
	info, err := os.Stat(source)
	require.NoError(t, err)

	prime := exec.Command("python3", "-c", "import fixture_module")
	prime.Dir = dir
	prime.Env = agentEnvironment(dir)
	require.NoError(t, prime.Run())

	// Keep source size and timestamp unchanged so normal Python imports would
	// accept the now-stale timestamp-based bytecode generated above.
	require.NoError(t, os.WriteFile(source, []byte("VALUE = 2\n"), 0o644))
	require.NoError(t, os.Chtimes(source, info.ModTime(), info.ModTime()))

	pass, err := runJudgeCommand(
		dir, "python3", "-c", "import fixture_module; assert fixture_module.VALUE == 2",
	)
	require.NoError(t, err)
	assert.True(t, pass)
}

func TestArtifactNamesIncludeInstanceAndRun(t *testing.T) {
	base := Row{Instance: "instance/v1", Arm: ArmHookOff, Trial: 1}
	first := base
	first.RunID = "run-a"
	second := base
	second.RunID = "run-b"

	assert.Equal(t, "instance_v1-run-a-hook-off-01", artifactBase(first))
	assert.NotEqual(t, artifactBase(first), artifactBase(second))
}

func TestRunRejectsUnsafeRunIDBeforeCreatingArtifacts(t *testing.T) {
	transcripts := t.TempDir()
	_, err := Run(context.Background(), RunConfig{
		Trials: 1, RunID: "../escape", TranscriptDir: transcripts,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run ID")

	entries, readErr := os.ReadDir(transcripts)
	require.NoError(t, readErr)
	assert.Empty(t, entries)
}

func TestPriorCostForFingerprint(t *testing.T) {
	_, _, _, ok := PriorCostFor(filepath.Join(t.TempDir(), "missing.jsonl"), "wanted")
	assert.False(t, ok)

	path := filepath.Join(t.TempDir(), "results.jsonl")
	for _, row := range []Row{
		{SchemaVersion: 2, Valid: true, PairValid: true, Fingerprint: "wanted", ContextTokens: 100, CostUSD: 0.10},
		{SchemaVersion: 2, Valid: true, PairValid: true, Fingerprint: "other", ContextTokens: 900, CostUSD: 0.90},
		{SchemaVersion: 2, Valid: false, Fingerprint: "wanted", ContextTokens: 500, CostUSD: 0.50},
		// A stub row without usage must not drag the means down.
		{SchemaVersion: 2, Valid: true, PairValid: true, Fingerprint: "wanted"},
	} {
		require.NoError(t, appendRow(path, row))
	}

	rows, meanContext, meanCost, ok := PriorCostFor(path, "wanted")
	require.True(t, ok)
	assert.Equal(t, 1, rows)
	assert.Equal(t, int64(100), meanContext)
	assert.InDelta(t, 0.10, meanCost, 1e-9)
}

func TestRunPreservesCallerWorkDirAndRemovesOnlyTrial(t *testing.T) {
	work := t.TempDir()
	sentinel := filepath.Join(work, "caller-owned.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o644))

	_, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{"/usr/bin/true"},
		WorkDir: work,
	})
	require.NoError(t, err)

	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(data))
	_, err = os.Stat(filepath.Join(work, "hook-off-01"))
	assert.True(t, os.IsNotExist(err), "only the runner-owned trial directory is removed")
}

func TestRunDoesNotReuseOrRemovePreexistingTrialDir(t *testing.T) {
	work := t.TempDir()
	trial := filepath.Join(work, "hook-off-01")
	require.NoError(t, os.MkdirAll(trial, 0o755))
	sentinel := filepath.Join(trial, "caller-owned.txt")
	require.NoError(t, os.WriteFile(sentinel, []byte("keep"), 0o644))

	_, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{"/usr/bin/true"},
		WorkDir: work,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trial directory already exists")
	data, readErr := os.ReadFile(sentinel)
	require.NoError(t, readErr)
	assert.Equal(t, "keep", string(data))
}

func TestRunRejectsUnknownArmBeforeStarting(t *testing.T) {
	work := t.TempDir()
	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{"future-arm"}, AgentArgv: []string{"/usr/bin/true"},
		WorkDir: work, Keep: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown benchmark arm")
	assert.Empty(t, sum.Rows)
	entries, readErr := os.ReadDir(work)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "invalid assignment must not create or mutate a trial")
}

func TestMatchingTreatmentFiringsRequiresIdentitySurfaceToolAndRegion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeLessons(dir, schemaSyncLessonYAML))

	cfg, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	require.Len(t, cfg.Pin, 1)
	expected := reviews.PinIdentity(cfg.Pin[0])

	firings := []reviews.Firing{
		{File: "server/schema.py", Tool: "Edit", Fired: []reviews.FiredLesson{{Region: "server", Symptom: "another lesson"}}},
		{Surface: "check", File: "server/schema.py", Tool: "Edit", Fired: []reviews.FiredLesson{expected}},
		{File: "docs/readme.md", Tool: "Edit", Fired: []reviews.FiredLesson{expected}},
		{File: "server/schema.py", Tool: "Read", Fired: []reviews.FiredLesson{expected}},
		{File: "server/schema.py", Tool: "MultiEdit", Fired: []reviews.FiredLesson{expected}},
		{File: "server/schema.py", Tool: "Edit", Delivery: reviews.DeliverySuppressedRepeat,
			Fired: []reviews.FiredLesson{expected}},
	}

	var audit strings.Builder
	for _, firing := range firings {
		data, marshalErr := json.Marshal(firing)
		require.NoError(t, marshalErr)
		audit.Write(data)
		audit.WriteByte('\n')
	}
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".seamark", "lessons-audit.jsonl"), []byte(audit.String()), 0o644))

	matching, total, err := matchingTreatmentFirings(dir, cfg.Pin[0])
	require.NoError(t, err)
	assert.Equal(t, 1, matching)
	assert.Equal(t, 6, total)
}

func TestRunStopsWhenSelectedTreatmentNeverFires(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte(`#!/bin/sh
mkdir -p .seamark
echo '{"file":"server/schema.py","tool":"Edit","fired":[{"region":"server","symptom":"unrelated lesson"}]}' >> .seamark/lessons-audit.jsonl
`), 0o755))

	sum, err := Run(context.Background(), RunConfig{
		Trials: 3, AgentArgv: []string{stub}, Keep: true, WorkDir: t.TempDir(),
	})
	require.Error(t, err)
	require.Len(t, sum.Rows, 1, "an invalid treatment stops before another paid arm")
	assert.True(t, sum.Rows[0].InfrastructureFailure)
	assert.Equal(t, 0, sum.Rows[0].HookFirings)
	assert.Equal(t, 1, sum.Rows[0].HookAuditRows)
	assert.Contains(t, sum.StoppedReason, "selected lesson hook never fired")
}

func TestStructuredTimeoutIsAnAgentOutcome(t *testing.T) {
	row := Row{
		Valid: true, TimedOut: true, InitSeen: true, Model: "claude-test",
		Tools: []string{"Read", "Edit", "Write", "Bash"},
	}
	validateAgentResult(RunConfig{
		Model: "claude-test", RequireStructuredResult: true, RequireCleanInit: true,
	}, &row)

	assert.True(t, row.Valid)
	assert.False(t, row.InfrastructureFailure)
	assert.False(t, row.ResultSeen)
}

func TestIsErrorWithoutProviderStatusIsAnAgentOutcome(t *testing.T) {
	row := Row{Valid: true}
	parseAgentOutput([]byte(`{"type":"result","is_error":true,"result":"budget exhausted"}`),
		&row, SchemaSyncInstance())

	assert.True(t, row.Valid)
	assert.True(t, row.AgentError)
	assert.False(t, row.InfrastructureFailure)
}

func TestCommandTimeoutIsBounded(t *testing.T) {
	started := time.Now()
	result := (Command{
		Name: "/bin/sh", Args: []string{"-c", "exec sleep 5"}, Timeout: 50 * time.Millisecond,
	}).run(context.Background(), t.TempDir())

	assert.False(t, result.Pass)
	assert.True(t, result.TimedOut)
	assert.Less(t, time.Since(started), time.Second)
}

func TestPreflightRejectsInvariantJudgeThatAlwaysPassesCompletedTasks(t *testing.T) {
	instance := SchemaSyncInstance()
	realJudge := instance.Judge
	instance.Judge = func(dir string) (Verdict, error) {
		verdict, err := realJudge(dir)
		if verdict.TaskDone {
			verdict.Avoided = true
		}

		return verdict, err
	}

	err := Preflight(context.Background(), RunConfig{
		Instance: instance, SeamarkBin: "/opt/seamark/bin/seamark",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "naive patch incorrectly passes")
}

func TestSummaryUsesSelectedInstanceRule(t *testing.T) {
	lines := (Summary{
		Rule: "schema-sync", ByArm: map[Arm]Tally{},
	}).Lines()
	require.NotEmpty(t, lines)
	assert.Contains(t, lines[0], "schema-sync")
}

func TestFingerprintBindsExperimentDefinition(t *testing.T) {
	base := SchemaSyncInstance()
	baseFingerprint, err := Fingerprint(RunConfig{Instance: base})
	require.NoError(t, err)

	lesson := base
	lesson.LessonYAML += "# changed treatment\n"
	lessonFingerprint, err := Fingerprint(RunConfig{Instance: lesson})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, lessonFingerprint)

	placebo := base
	placebo.PlaceboYAML += "# changed placebo\n"
	placeboFingerprint, err := Fingerprint(RunConfig{Instance: placebo})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, placeboFingerprint)

	judge := base
	judge.JudgeVersion = "python-ts-schema-sync-v2"
	judgeFingerprint, err := Fingerprint(RunConfig{Instance: judge})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, judgeFingerprint)

	gold := base
	applyBaseGold := gold.ApplyGold
	gold.ApplyGold = func(dir string) error {
		if err := applyBaseGold(dir); err != nil {
			return err
		}

		return os.WriteFile(filepath.Join(dir, "gold-marker.txt"), []byte("variant\n"), 0o644)
	}
	goldFingerprint, err := Fingerprint(RunConfig{Instance: gold})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, goldFingerprint)

	fixture := base
	generateBase := fixture.Generate
	fixture.Generate = func(dir string) error {
		if err := generateBase(dir); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "fixture-version.txt"), []byte("v2\n"), 0o644); err != nil {
			return err
		}
		if err := runGit(dir, "add", "fixture-version.txt"); err != nil {
			return err
		}

		return runGit(dir, "commit", "-q", "-m", "Change fixture version")
	}
	fixtureFingerprint, err := Fingerprint(RunConfig{Instance: fixture})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, fixtureFingerprint)

	controls := []RunConfig{
		{Instance: base, PrepareIndex: true},
		{Instance: base, RequireStructuredResult: true},
		{Instance: base, RequireCleanInit: true},
		{Instance: base, Arms: []Arm{ArmHookOff}},
	}
	for _, cfg := range controls {
		fingerprint, fingerprintErr := Fingerprint(cfg)
		require.NoError(t, fingerprintErr)
		assert.NotEqual(t, baseFingerprint, fingerprint)
	}

	explicitDefaultArms, err := Fingerprint(RunConfig{
		Instance: base, Arms: []Arm{ArmHookOn, ArmHookOff},
	})
	require.NoError(t, err)
	assert.Equal(t, baseFingerprint, explicitDefaultArms,
		"implicit and explicit default assignment are the same experiment")
}

func TestFingerprintSourceBoundary(t *testing.T) {
	entries, err := harnessSources.ReadDir(".")
	require.NoError(t, err)
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	assert.True(t, names["fingerprint.go"], "fingerprint logic must bind its own implementation")
	assert.False(t, names["catalog.go"], "catalogue-only additions must not invalidate existing cohorts")

	instance := SchemaSyncInstance()
	digest, err := fingerprintInstanceSource(instance)
	require.NoError(t, err)
	source, err := instanceSources.ReadFile(instance.sourceFile)
	require.NoError(t, err)
	assert.Equal(t, hashBytes(source), digest)

	custom := instance
	custom.sourceFile = ""
	digest, err = fingerprintInstanceSource(custom)
	require.NoError(t, err)
	assert.Equal(t, "custom-instance", digest)
}

func TestWriteArtifactExclusiveIsPrivateAndRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trial.patch")
	require.NoError(t, ensureArtifactsAbsent(dir, "trial"))
	require.NoError(t, writeArtifactExclusive(path, []byte("first\n")))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.Error(t, writeArtifactExclusive(path, []byte("second\n")))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "first\n", string(data))
	require.Error(t, ensureArtifactsAbsent(dir, "trial"))
}
