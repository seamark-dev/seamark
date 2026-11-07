package integration

// Tests of the agent prompts of scripts/native-record.sh. They read the
// script text only, so they need no model and no client CLI. Codex in
// code mode lists the agent tools with the multi_agent_v1__ prefix. A
// prompt with the short name makes the model call a tool that does not
// exist, and no worker starts. These tests keep every prompt on the full
// names, because a failed spawn costs a paid recording run. Each Claude
// Code child prompt sets the launch shape of its Agent call, because the
// replay asserts that shape.

import (
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
