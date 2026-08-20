package bench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func noopAgent(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "noop-agent")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	return path
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
	assert.NotEmpty(t, sum.Rows[0].Fingerprint)
	assert.NotEmpty(t, sum.Rows[0].ProtocolFingerprint)
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

	sum, err := Run(ctx, RunConfig{Trials: 3, AgentArgv: []string{noopAgent(t)}})
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
		AgentArgv: []string{noopAgent(t)}, Out: out,
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
	trialGenerations := 0
	instance.Generate = func(dir string) error {
		// Run regenerates fingerprint repositories before trial setup. Count
		// only the two assigned trial directories this test intends to exercise.
		switch filepath.Base(dir) {
		case "hook-off-01", "file-only-01":
			trialGenerations++
			if trialGenerations == 2 {
				return assert.AnError
			}
		}

		return realGenerate(dir)
	}
	out := filepath.Join(t.TempDir(), "results.jsonl")

	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff, ArmFileOnly}, Instance: instance,
		AgentArgv: []string{noopAgent(t)}, Out: out,
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
		AgentArgv: []string{noopAgent(t)},
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

func TestRunAgentAppendsWorkingTreeInstruction(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s' \"$1\"\n"), 0o755))
	instance := SchemaSyncInstance()

	stdout, stderr, exit, timedOut, err := runAgent(context.Background(), RunConfig{
		AgentArgv: []string{stub}, Timeout: time.Second,
	}, instance, t.TempDir())

	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Zero(t, exit)
	assert.False(t, timedOut)
	assert.Equal(t, agentPrompt(instance.Task), string(stdout))
	assert.Contains(t, string(stdout), agentWorkingTreeInstruction)
}

func TestAgentEnvironmentPinsCachesInsideTrial(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", "/host/home")
	t.Setenv("ANTHROPIC_MODEL", "unwanted")
	t.Setenv("ANTHROPIC_BASE_URL", "https://gateway.invalid")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "unwanted-helper")
	t.Setenv("MAX_THINKING_TOKENS", "1")
	t.Setenv("DISABLE_PROMPT_CACHING", "1")
	t.Setenv("ANTHROPIC_API_KEY", "test-api-auth")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "test-oauth-auth")
	t.Setenv("CLAUDE_CONFIG_DIR", "/test/claude-auth-config")
	t.Setenv("CLAUDE_CODE_EFFORT_LEVEL", "max")
	t.Setenv("PYTHONPATH", "/host/python")
	t.Setenv("PYTHONHOME", "/host/python-home")
	t.Setenv("BASH_ENV", "/host/bash-env")
	t.Setenv("MAKEFLAGS", "--jobs=99")
	t.Setenv("VIRTUAL_ENV", "/host/venv")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", "/host/hooks")
	t.Setenv("GIT_AUTHOR_NAME", "host author")
	t.Setenv("GIT_COMMITTER_EMAIL", "host@example.com")
	t.Setenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY", "0")
	t.Setenv("CLAUDE_CODE_SHELL_PREFIX", "/host/prefix")
	t.Setenv("ENABLE_CLAUDEAI_MCP_SERVERS", "true")
	t.Setenv("SEAMARK_BENCH_TOOL_HOME", "/host/tool-home")
	t.Setenv("SEAMARK_BENCH_TOOL_XDG", "/host/tool-xdg")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOENV", "/host/go/env")
	t.Setenv("GOTELEMETRY", "on")
	t.Setenv("GOTELEMETRYDIR", "/host/go/telemetry")
	t.Setenv("GOWORK", "/host/go.work")
	t.Setenv("XDG_CONFIG_HOME", "/host/xdg-config")

	env := environmentByKey(t, agentEnvironment(dir))
	for _, key := range []string{
		"ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK",
		"ANTHROPIC_SMALL_FAST_MODEL", "MAX_THINKING_TOKENS", "DISABLE_PROMPT_CACHING",
		"CLAUDE_CODE_EFFORT_LEVEL", "PYTHONPATH", "PYTHONHOME", "BASH_ENV", "MAKEFLAGS",
		"SEAMARK_BENCH_CACHE_DIR", "GOFLAGS", "GOTELEMETRY", "GOTELEMETRYDIR",
		"VIRTUAL_ENV", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"GIT_AUTHOR_NAME", "GIT_COMMITTER_EMAIL",
	} {
		assert.NotContains(t, env, key)
	}

	assert.Equal(t, "test-api-auth", env["ANTHROPIC_API_KEY"],
		"authentication must survive environment normalization")
	assert.Equal(t, "test-oauth-auth", env["CLAUDE_CODE_OAUTH_TOKEN"],
		"subscription authentication must survive environment normalization")
	assert.Equal(t, "/test/claude-auth-config", env["CLAUDE_CONFIG_DIR"],
		"custom configuration directories may contain the operator's saved credentials")
	assert.Equal(t, "/host/home", env["HOME"],
		"the agent process needs the operator home for keychain-backed authentication")
	assert.Equal(t, "/host/xdg-config", env["XDG_CONFIG_HOME"])
	assert.Equal(t, filepath.Join(dir, ".bench-cache", "tool-environment.sh"),
		env["CLAUDE_CODE_SHELL_PREFIX"])
	assert.Equal(t, filepath.Join(dir, ".bench-cache", "home"), env["SEAMARK_BENCH_TOOL_HOME"])
	assert.Equal(t, filepath.Join(dir, ".bench-cache", "xdg-config"), env["SEAMARK_BENCH_TOOL_XDG"])
	assert.Equal(t, filepath.Join(dir, ".bench-cache", "go-build"), env["GOCACHE"])
	assert.Equal(t, "off", env["GOENV"])
	assert.Equal(t, "off", env["GOPROXY"])
	assert.Equal(t, "off", env["GOWORK"])
	assert.Equal(t, "1", env["CLAUDE_CODE_DISABLE_AUTO_MEMORY"])
	assert.Equal(t, "1", env["CLAUDE_CODE_DISABLE_TERMINAL_TITLE"])
	assert.Equal(t, "1", env["CLAUDE_CODE_DISABLE_POLICY_SKILLS"])
	assert.Equal(t, "false", env["ENABLE_CLAUDEAI_MCP_SERVERS"])
	assert.Equal(t, "1", env["PYTHONNOUSERSITE"])
	assert.Equal(t, "0", env["PYTHONHASHSEED"])
	assert.Equal(t, "1", env["GIT_CONFIG_NOSYSTEM"])
	assert.Equal(t, os.DevNull, env["GIT_CONFIG_GLOBAL"])
}

func TestAgentEnvironmentPreservesDefaultClaudeHome(t *testing.T) {
	dir := t.TempDir()
	hostHome := filepath.Join(t.TempDir(), "operator-home")
	t.Setenv("HOME", hostHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")

	env := environmentByKey(t, agentEnvironment(dir))

	assert.Equal(t, hostHome, env["HOME"])
	assert.Empty(t, env["CLAUDE_CONFIG_DIR"])
}

func TestAgentEnvironmentKeepsGoTelemetryInsideTrial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Go uses Windows application-data variables instead of HOME")
	}

	dir := t.TempDir()
	require.NoError(t, writeAgentToolEnvironment(dir))
	env := agentEnvironment(dir)
	byKey := environmentByKey(t, env)
	cmd := exec.Command(byKey["CLAUDE_CODE_SHELL_PREFIX"], "go env GOTELEMETRYDIR")
	cmd.Env = env
	out, err := cmd.Output()
	require.NoError(t, err)

	telemetryDir := strings.TrimSpace(string(out))
	rel, err := filepath.Rel(filepath.Join(dir, ".bench-cache"), telemetryDir)
	require.NoError(t, err)
	assert.True(t, filepath.IsLocal(rel), "Go telemetry escaped the trial cache: %s", telemetryDir)
}

func environmentByKey(t *testing.T, entries []string) map[string]string {
	t.Helper()

	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		require.True(t, ok, "environment entry lacks '=': %q", entry)
		require.NotContains(t, out, key, "duplicate environment key %q", key)
		out[key] = value
	}

	return out
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

func TestRunRejectsCallerSuppliedFingerprintMismatch(t *testing.T) {
	base := RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
	}
	expectedFingerprint, err := Fingerprint(base)
	require.NoError(t, err)
	expectedProtocol, err := ProtocolFingerprint(base)
	require.NoError(t, err)

	differentFrom := func(value string) string {
		candidate := strings.Repeat("0", 64)
		if candidate == value {
			return strings.Repeat("1", 64)
		}

		return candidate
	}

	for _, tc := range []struct {
		name    string
		prepare func(*RunConfig)
		wantErr string
	}{
		{
			name: "full fingerprint",
			prepare: func(cfg *RunConfig) {
				cfg.Fingerprint = differentFrom(expectedFingerprint)
			},
			wantErr: "supplied benchmark fingerprint",
		},
		{
			name: "protocol fingerprint",
			prepare: func(cfg *RunConfig) {
				cfg.ProtocolFingerprint = differentFrom(expectedProtocol)
			},
			wantErr: "supplied benchmark protocol fingerprint",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.WorkDir = t.TempDir()
			tc.prepare(&cfg)

			sum, runErr := Run(context.Background(), cfg)
			require.ErrorContains(t, runErr, tc.wantErr)
			assert.Empty(t, sum.Rows)
			entries, readErr := os.ReadDir(cfg.WorkDir)
			require.NoError(t, readErr)
			assert.Empty(t, entries, "a mismatched identity must fail before creating a trial")
		})
	}
}

func TestRunAcceptsMatchingCallerSuppliedFingerprints(t *testing.T) {
	cfg := RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
	}
	var err error
	cfg.Fingerprint, err = Fingerprint(cfg)
	require.NoError(t, err)
	cfg.ProtocolFingerprint, err = ProtocolFingerprint(cfg)
	require.NoError(t, err)

	sum, err := Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Len(t, sum.Rows, 1)
	assert.Equal(t, cfg.Fingerprint, sum.Rows[0].Fingerprint)
	assert.Equal(t, cfg.ProtocolFingerprint, sum.Rows[0].ProtocolFingerprint)
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

	_, _, _, ok = PriorCostFor(path, "")
	assert.False(t, ok, "an empty fingerprint must never pool unrelated cohorts")

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
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
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
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
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
		Trials: 1, Arms: []Arm{"future-arm"}, AgentArgv: []string{noopAgent(t)},
		WorkDir: work, Keep: true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown benchmark arm")
	assert.Empty(t, sum.Rows)
	entries, readErr := os.ReadDir(work)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "invalid assignment must not create or mutate a trial")
}

func TestRunRejectsUnknownHookDeliveryBeforeStarting(t *testing.T) {
	work := t.TempDir()
	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
		WorkDir: work, Keep: true, HookDelivery: "sometimes",
	})
	require.ErrorContains(t, err, "unknown hook delivery mode")
	assert.Empty(t, sum.Rows)
}

func TestRunRejectsInvalidDeliveredLessonsBeforeStarting(t *testing.T) {
	work := t.TempDir()
	instance := SchemaSyncInstance()
	instance.LessonYAML = "hook_delivery: always\n" + instance.LessonYAML

	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOff}, AgentArgv: []string{noopAgent(t)},
		Instance: instance, WorkDir: work, Keep: true,
		HookDelivery: HookDeliveryOncePerContext,
	})
	require.ErrorContains(t, err, "validate delivered lessons")
	assert.Empty(t, sum.Rows)
	entries, readErr := os.ReadDir(work)
	require.NoError(t, readErr)
	assert.Empty(t, entries, "invalid delivered YAML must fail before fixture generation")
}

func TestPreflightRejectsMissingSeamarkBeforeGeneratingFixture(t *testing.T) {
	generated := false
	instance := SchemaSyncInstance()
	instance.Generate = func(string) error {
		generated = true

		return nil
	}

	err := Preflight(context.Background(), RunConfig{Instance: instance})
	require.ErrorContains(t, err, "preflight requires the seamark binary")
	assert.False(t, generated)
}

func TestWireArmOncePerContextInstallsPolicyAndReset(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixture")
	instance := SchemaSyncInstance()
	require.NoError(t, instance.Generate(dir))
	require.NoError(t, wireArm(context.Background(), dir, RunConfig{
		SeamarkBin: "/opt/seamark", HookDelivery: HookDeliveryOncePerContext,
	}, instance, ArmHookOn))

	lessons, err := os.ReadFile(filepath.Join(dir, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(lessons), "hook_delivery: once-per-context")

	settings, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Contains(t, string(settings), "lessons --hook")
	assert.Contains(t, string(settings), "PostCompact")
	assert.Contains(t, string(settings), "lessons --hook-reset")
	assert.Contains(t, string(settings), `"timeout": 10`)
	assert.Contains(t, string(settings), `"statusMessage": "seamark: resetting lesson delivery"`)
}

func TestWireArmFileOnlyDoesNotInstallHookDeliveryPolicy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fixture")
	instance := SchemaSyncInstance()
	require.NoError(t, instance.Generate(dir))
	require.NoError(t, wireArm(context.Background(), dir, RunConfig{
		SeamarkBin: "/opt/seamark", HookDelivery: HookDeliveryOncePerContext,
	}, instance, ArmFileOnly))

	lessons, err := os.ReadFile(filepath.Join(dir, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Equal(t, instance.LessonYAML, string(lessons))

	configured, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	assert.Equal(t, reviews.HookDeliveryAlways, configured.HookDelivery(),
		"file-only keeps the lesson file's default delivery policy")

	settingsData, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(settingsData, &settings))
	assert.NotContains(t, settings, "hooks", "file-only must not install delivery lifecycle hooks")

	assert.NoFileExists(t, filepath.Join(dir, ".seamark", "lessons-hook-state.json"))
	assert.NoFileExists(t, filepath.Join(dir, ".seamark", "lessons-hook-state.lock"))
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

	intensity, err := matchingTreatmentFirings(dir, cfg.Pin[0])
	require.NoError(t, err)
	assert.Equal(t, 2, intensity.Matches)
	assert.Equal(t, 1, intensity.Injections)
	assert.Equal(t, 1, intensity.Suppressed)
	assert.Equal(t, 6, intensity.AuditRows)
}

func TestMatchingTreatmentFiringsMeasuresRepeatedAndGroupedDelivery(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeLessons(dir, schemaSyncLessonYAML))
	cfg, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	expected := reviews.PinIdentity(cfg.Pin[0])

	firings := []reviews.Firing{
		{File: "server/schema.py", Tool: "Edit", Delivery: reviews.DeliveryInjected,
			SessionSHA: "session", MatchSHA: "match-1", Generation: 1, ContextBytes: 400,
			Fired: []reviews.FiredLesson{expected}},
		{File: "server/presenters.py", Tool: "Edit", Delivery: reviews.DeliveryInjected,
			SessionSHA: "session", MatchSHA: "match-2", Generation: 1, ContextBytes: 420,
			Fired: []reviews.FiredLesson{expected}},
		{File: "server/presenters.py", Tool: "Edit", Delivery: reviews.DeliverySuppressedRepeat,
			SessionSHA: "session", MatchSHA: "match-2", Generation: 1,
			Fired: []reviews.FiredLesson{expected}},
		{File: "server/schema.py", Tool: "Edit", Delivery: reviews.DeliveryInjected,
			SessionSHA: "session", MatchSHA: "match-3", Generation: 2, ContextBytes: 410,
			Fired: []reviews.FiredLesson{expected}},
		{File: "server/schema.py", Tool: "Edit", Delivery: reviews.DeliverySuppressedRepeat,
			SessionSHA: "session", MatchSHA: "match-4", Generation: 2,
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

	intensity, err := matchingTreatmentFirings(dir, cfg.Pin[0])
	require.NoError(t, err)
	assert.Equal(t, 4, intensity.Matches)
	assert.Equal(t, 3, intensity.Injections)
	assert.Equal(t, 1, intensity.Repeated)
	assert.Equal(t, 1, intensity.Suppressed,
		"a partly suppressed match that still injected context is not fully suppressed")
	assert.Equal(t, 1230, intensity.ContextBytes)
}

func TestMatchingTreatmentFiringsRejectsUnknownDeliveryStatus(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeLessons(dir, schemaSyncLessonYAML))
	cfg, err := reviews.LoadConfig(dir)
	require.NoError(t, err)
	expected := reviews.PinIdentity(cfg.Pin[0])

	data, err := json.Marshal(reviews.Firing{
		File: "server/schema.py", Tool: "Edit", Delivery: "future-status",
		Fired: []reviews.FiredLesson{expected},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, ".seamark", "lessons-audit.jsonl"), append(data, '\n'), 0o644))

	_, err = matchingTreatmentFirings(dir, cfg.Pin[0])
	require.ErrorContains(t, err, "unknown hook delivery status")
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

func TestOptionalExposureScopeControlAcceptsNoMatchingFiring(t *testing.T) {
	stub := filepath.Join(t.TempDir(), "agent.sh")
	require.NoError(t, os.WriteFile(stub, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	sum, err := Run(context.Background(), RunConfig{
		Trials: 1, Arms: []Arm{ArmHookOn}, Instance: SchemaSyncRepairInstance(),
		AgentArgv: []string{stub}, Keep: true, WorkDir: t.TempDir(),
	})
	require.NoError(t, err)
	require.Len(t, sum.Rows, 1)
	row := sum.Rows[0]
	assert.True(t, row.Valid)
	assert.True(t, row.PairValid)
	assert.Equal(t, HookExposureOptional, row.HookExposure)
	assert.Zero(t, row.HookInjections)
	assert.False(t, row.InfrastructureFailure)
	assert.Contains(t, strings.Join(sum.Lines(), "\n"),
		"scope control — the hook matched no hook-on edit")
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
		{Instance: base, HookDelivery: HookDeliveryOncePerContext},
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

	explicitDefaultDelivery, err := Fingerprint(RunConfig{
		Instance: base, HookDelivery: HookDeliveryAlways,
	})
	require.NoError(t, err)
	assert.Equal(t, baseFingerprint, explicitDefaultDelivery,
		"implicit and explicit always delivery are the same experiment")
}

func TestFingerprintSourceBoundary(t *testing.T) {
	entries, err := harnessSources.ReadDir(".")
	require.NoError(t, err)
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	assert.True(t, names["fingerprint.go"], "fingerprint logic must bind its own implementation")
	assert.True(t, names["results.go"], "row validation semantics must be part of the run identity")
	assert.False(t, names["catalog.go"], "catalogue-only additions must not invalidate existing cohorts")

	instance := SchemaSyncInstance()
	digest, err := fingerprintInstanceSource(instance)
	require.NoError(t, err)
	assert.True(t, validSHA256(digest))

	repairDigest, err := fingerprintInstanceSource(SchemaSyncRepairInstance())
	require.NoError(t, err)
	assert.NotEqual(t, digest, repairDigest,
		"the repair variant source must affect its full cohort fingerprint")

	baseProtocol, err := ProtocolFingerprint(RunConfig{Instance: instance})
	require.NoError(t, err)
	repairProtocol, err := ProtocolFingerprint(RunConfig{Instance: SchemaSyncRepairInstance()})
	require.NoError(t, err)
	assert.Equal(t, baseProtocol, repairProtocol,
		"the intentional scope variant must retain the shared experiment protocol")

	baseFingerprint, err := Fingerprint(RunConfig{Instance: instance})
	require.NoError(t, err)
	repairFingerprint, err := Fingerprint(RunConfig{Instance: SchemaSyncRepairInstance()})
	require.NoError(t, err)
	assert.NotEqual(t, baseFingerprint, repairFingerprint,
		"variant rows must remain in separate immutable cohorts")

	custom := instance
	custom.sourceFile = ""
	custom.ComparisonFamily = ""
	custom.ProtocolInstance = ""
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
