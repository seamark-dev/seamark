package integration

// Tests of the agent prompts and the case order of
// scripts/native-record.sh. They read the script text, or they run its
// functions with a stand-in for the client, so they need no model and no
// client CLI. Codex in code mode lists the agent tools with the
// multi_agent_v1__ prefix. A prompt with the short name makes the model
// call a tool that does not exist, and no worker starts. These tests keep
// every prompt on the full names, because a failed spawn costs a paid
// recording run. Each Claude Code child prompt sets the launch shape of
// its Agent call, because the replay asserts that shape.

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codexAgentPrompt names the shell function that records one Codex case,
// and the line of that function that opens the case prompt. The Claude Code
// functions use the same case names, so the search stays inside the Codex
// function.
type codexAgentPrompt struct {
	function string
	opener   string
}

// codexAgentPrompts holds each Codex case that starts a worker agent.
var codexAgentPrompts = map[string]codexAgentPrompt{
	"subagent-patch":         {"record_codex_timing", `timing_codex subagent-patch new "$steps`},
	"subagent-violation":     {"record_codex_guard", `guard_run enforce subagent-violation "$GUARD_STEPS`},
	"subagent-check-only":    {"record_codex_guard", `guard_run check-pins subagent-check-only "$GUARD_STEPS`},
	"slow-deny-subagent-add": {"record_codex_guard", `guard_run slow-deny slow-deny-subagent-add "$GUARD_STEPS`},
}

// scriptPrompt returns the prompt that starts at the opener line of body, up
// to the next blank line or the end of body. A blank line separates the case
// calls, and the last case call ends at the closing brace of the function.
func scriptPrompt(t *testing.T, body, opener string) string {
	t.Helper()

	marker := "\n    " + opener + "\n"
	require.Equal(t, 1, strings.Count(body, marker), "the function must open the prompt once: %s", opener)

	start := strings.Index(body, marker) + 1

	end := strings.Index(body[start:], "\n\n")
	if end < 0 {
		return body[start:]
	}

	return body[start : start+end]
}

// TestNativeRecordAgentPromptsUseFullToolNames proves that no prompt step
// uses the short agent tool names. The check reads every numbered step line,
// so a new prompt with a short name also fails. Comment lines stay out,
// because a comment can name the short form to explain the reason.
func TestNativeRecordAgentPromptsUseFullToolNames(t *testing.T) {
	script := nativeRecordScript(t)

	step := regexp.MustCompile(`^\d+\. `)

	// The word boundary does not match after the underscore of the prefix,
	// so the full names do not match this pattern.
	shortName := regexp.MustCompile(`\b(spawn_agent|wait_agent|close_agent)\b`)

	var short []string

	for _, line := range strings.Split(script, "\n") {
		if step.MatchString(line) && shortName.MatchString(line) {
			short = append(short, line)
		}
	}

	assert.Empty(t, short, "prompt steps must use the multi_agent_v1__ tool names")
}

// TestNativeRecordAgentPromptsNameWorkerTools proves that each Codex case
// that starts a worker names the spawn tool with the worker agent_type, and
// the wait and close tools. Without agent_type, the spawn tool starts a fork
// of the whole history, not a worker.
func TestNativeRecordAgentPromptsNameWorkerTools(t *testing.T) {
	script := nativeRecordScript(t)

	for name, c := range codexAgentPrompts {
		t.Run(name, func(t *testing.T) {
			prompt := scriptPrompt(t, scriptFunction(t, script, c.function), c.opener)

			assert.Contains(t, prompt, "1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to ")
			assert.Contains(t, prompt, "2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.")
		})
	}
}

// claudeChildPrompt names one Claude Code case that starts a subagent: the
// shell function that records it, the line that opens its prompt, and the
// launch shape that the prompt sets.
type claudeChildPrompt struct {
	name, function, opener, shape string
}

// claudeChildPrompts holds each Claude Code case that starts a subagent.
var claudeChildPrompts = []claudeChildPrompt{
	{"subagent-violation", "record_claude_guard", `guard_run enforce subagent-violation "$GUARD_STEPS`, launchForeground},
	{"subagent-edit-violation", "record_claude_guard", `guard_run enforce subagent-edit-violation "$GUARD_STEPS`, launchForeground},
	{"subagent-multiedit-violation", "record_claude_guard", `guard_run enforce subagent-multiedit-violation "$GUARD_STEPS`,
		launchForeground},
	{"subagent-check-only", "record_claude_guard", `guard_run check-pins subagent-check-only "$GUARD_STEPS`, launchBackground},
	{"slow-deny-subagent-write", "record_claude_guard", `guard_run slow-deny slow-deny-subagent-write "$GUARD_STEPS`,
		launchBackground},
	{"slow-deny-subagent-edit", "record_claude_guard", `guard_run slow-deny slow-deny-subagent-edit "$GUARD_STEPS`,
		launchBackground},
	{"slow-deny-subagent-multiedit", "record_claude_guard", `guard_run slow-deny slow-deny-subagent-multiedit "$GUARD_STEPS`,
		launchBackground},
	{"subagent-read", "record_claude_timing", `timing_claude subagent-read new "$steps`, launchBackground},
}

// TestNativeRecordClaudeChildPromptsSetTheLaunchShape proves that each
// Claude Code child prompt sets run_in_background to the shape of its
// case, and that every Agent step of the Claude Code functions sets it.
// The search stays inside the Claude Code functions, because the Codex
// functions use the same case names. The shapes of the guard extension
// table agree with the prompts.
func TestNativeRecordClaudeChildPromptsSetTheLaunchShape(t *testing.T) {
	script := nativeRecordScript(t)
	field := map[string]string{launchForeground: "false", launchBackground: "true"}

	for _, c := range claudeChildPrompts {
		t.Run(c.name, func(t *testing.T) {
			prompt := scriptPrompt(t, scriptFunction(t, script, c.function), c.opener)
			assert.Contains(t, prompt, " and run_in_background set to "+field[c.shape]+", and ask it to ")
		})
	}

	steps := 0
	setsShape := regexp.MustCompile(`^1\. Use the Agent tool with subagent_type \\"[a-z]+\\" and run_in_background set to (true|false), `)

	for _, function := range []string{"record_claude_guard", "record_claude_timing"} {
		for _, line := range strings.Split(scriptFunction(t, script, function), "\n") {
			if strings.Contains(line, "Use the Agent tool") {
				steps++
				assert.Regexp(t, setsShape, line, "the Agent step of %s sets the launch shape", function)
			}
		}
	}

	assert.Equal(t, len(claudeChildPrompts), steps, "the table holds every Claude Code Agent step")

	for _, x := range claudeGuardExtensions {
		if x.shape == "" {
			continue
		}

		i := slices.IndexFunc(claudeChildPrompts, func(c claudeChildPrompt) bool { return c.name == x.name })
		if assert.GreaterOrEqual(t, i, 0, "the prompt table has the case %s", x.name) {
			assert.Equal(t, claudeChildPrompts[i].shape, x.shape, "the table and the prompt of %s set one shape", x.name)
		}
	}
}

// TestNativeRecordClaudeMultiEditPromptsNameTheReplyWord proves that each
// Claude Code prompt asks a model without a MultiEdit tool for the word
// that the replay checks. The replay compares the word exactly, so another
// word in one prompt fails its MultiEdit cases after a paid recording. The
// two root prompts ask the parent. The editor agent prompt asks the
// subagent of the two child cases, because the subagent holds the tool list.
func TestNativeRecordClaudeMultiEditPromptsNameTheReplyWord(t *testing.T) {
	body := scriptFunction(t, nativeRecordScript(t), "record_claude_guard")

	for _, opener := range []string{
		`guard_run enforce multiedit-violation "$GUARD_STEPS`,
		`guard_run slow-deny slow-deny-multiedit "$GUARD_STEPS`,
	} {
		t.Run(opener, func(t *testing.T) {
			assert.Contains(t, scriptPrompt(t, body, opener),
				"\nIf you have no MultiEdit tool, use no other tool for step 2 and reply with the single word: "+guardNoMultiEdit+".\n")
		})
	}

	assert.Contains(t, scriptLine(t, body, `    agents=$agents'"editor":`),
		`, once. If you do not have that tool, use no other tool and reply with the single word `+guardNoMultiEdit+`. `,
		"the editor agent prompt asks the subagent for the word")

	// Each instruction for a missing tool names the same word. So a new
	// prompt with another word fails too.
	ask := regexp.MustCompile(`reply with the single word:? ` + regexp.QuoteMeta(guardNoMultiEdit) + `\.`)
	asks := 0

	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "no MultiEdit tool") || strings.Contains(line, "do not have that tool") {
			asks++
			assert.Regexp(t, ask, line)
		}
	}

	assert.Equal(t, 3, asks, "the two root prompts and the editor agent prompt")
}

// runClaudeTiming runs record_claude_timing of the script with a stand-in
// timing_claude that prints the case, the session mode, and the first
// prompt line of each run. So the test starts no client. It returns the
// output and the exit error.
func runClaudeTiming(t *testing.T, env ...string) (string, error) {
	t.Helper()

	script := nativeRecordScript(t)

	var defs strings.Builder
	defs.WriteString("OK=0\nFAILED=0\nBLOCKED=0\nSUMMARIZED=\nMODE=record\nCLIENT=claude\n")
	defs.WriteString(scriptLine(t, script, "blocked()"))
	defs.WriteString(scriptFunction(t, script, "summary"))
	defs.WriteString("timing_claude() { printf 'run %s %s %s\\n' \"$1\" \"$2\" \"$(printf '%s\\n' \"$3\" | head -1)\"; }\n")
	defs.WriteString(scriptFunction(t, script, "record_claude_timing"))

	cmd := exec.Command("sh", "-eu", "-c", defs.String()+"record_claude_timing\n")
	cmd.Env = append(os.Environ(), "BIN=/opt/seamark/bin/seamark", "DISABLE_COMPACT=")
	cmd.Env = append(cmd.Env, env...)

	got, err := cmd.CombinedOutput()

	return string(got), err
}

// TestNativeRecordClaudeTimingCompactsAfterTheEdit proves the order of the
// Claude Code timing cases. compact-reset resumes the session right after
// new-model-step, so the compaction resets a context that holds both kinds
// of advice. Its prompt is /compact with the instruction that the replay
// expects in PreCompact. The earlier cases keep their order.
func TestNativeRecordClaudeTimingCompactsAfterTheEdit(t *testing.T) {
	requireTools(t, "sh", "head")

	got, err := runClaudeTiming(t)
	require.NoError(t, err, got)

	var runs []string

	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		fields := strings.Fields(line)
		require.GreaterOrEqual(t, len(fields), 3, line)
		runs = append(runs, fields[1]+" "+fields[2])
	}

	assert.Equal(t, []string{
		timingPriorResult + " new", timingNewModelStep + " resume", timingCompactReset + " resume",
		timingReadThenEdit + " new", timingSameCall + " new", timingBatchAndProgram + " new",
		timingFailedRead + " new", timingSubagentRead + " new",
	}, runs)

	assert.Contains(t, got, "run "+timingCompactReset+" resume /compact "+timingCompactInstructions+"\n")
}

// TestNativeRecordClaudeTimingRefusesDisabledCompaction proves that the
// Claude Code timing scenario stops before its first run when
// DISABLE_COMPACT is set. Claude Code then refuses /compact, so the paid
// runs before the compaction case would give no reset evidence.
func TestNativeRecordClaudeTimingRefusesDisabledCompaction(t *testing.T) {
	requireTools(t, "sh", "head")

	got, err := runClaudeTiming(t, "DISABLE_COMPACT=1")

	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, got)
	assert.Equal(t, 3, exit.ExitCode(), "a missing precondition is blocked")
	assert.Contains(t, got, "DISABLE_COMPACT")
	assert.NotContains(t, got, "run ", "no client starts")
}
