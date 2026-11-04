package integration

// Tests of the Codex agent prompts of scripts/capture-native.sh. They read
// the script text only, so they need no model and no client CLI. Codex in
// code mode lists the agent tools with the multi_agent_v1__ prefix. A
// prompt with the short name makes the model call a tool that does not
// exist, and no worker starts. These tests keep every prompt on the full
// names, because a failed spawn costs a paid recording run.

import (
	"regexp"
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

// TestCaptureNativeAgentPromptsUseFullToolNames proves that no prompt step
// uses the short agent tool names. The check reads every numbered step line,
// so a new prompt with a short name also fails. Comment lines stay out,
// because a comment can name the short form to explain the reason.
func TestCaptureNativeAgentPromptsUseFullToolNames(t *testing.T) {
	script := captureNativeScript(t)

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

// TestCaptureNativeAgentPromptsNameWorkerTools proves that each Codex case
// that starts a worker names the spawn tool with the worker agent_type, and
// the wait and close tools. Without agent_type, the spawn tool starts a fork
// of the whole history, not a worker.
func TestCaptureNativeAgentPromptsNameWorkerTools(t *testing.T) {
	script := captureNativeScript(t)

	for name, c := range codexAgentPrompts {
		t.Run(name, func(t *testing.T) {
			prompt := scriptPrompt(t, scriptFunction(t, script, c.function), c.opener)

			assert.Contains(t, prompt, "1. Use multi_agent_v1__spawn_agent with agent_type worker to start one worker agent. In its message, ask it to ")
			assert.Contains(t, prompt, "2. Wait for the agent with multi_agent_v1__wait_agent, then close it with multi_agent_v1__close_agent.")
		})
	}
}
