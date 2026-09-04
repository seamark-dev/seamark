package bench

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ClaudeArgv builds the headless Claude Code command for the skills workflow
// experiment. It is the lessons adapter (cmd/lessons-bench) with three
// differences: no --disable-slash-commands, because that flag also hides
// project skills; --tools takes the caller's list, because the arms differ in
// the tools they expose; and neither --strict-mcp-config nor --mcp-config,
// because the runner appends both per trial with the binary installed inside
// the trial. The task prompt is appended by the runner as the last argument.
func ClaudeArgv(model, effort string, budget float64, tools []string) []string {
	return []string{
		"claude", "-p",
		"--model", model,
		"--effort", effort,
		"--max-budget-usd", strconv.FormatFloat(budget, 'f', -1, 64),
		"--permission-mode", "acceptEdits",
		"--setting-sources", "project",
		"--tools", strings.Join(tools, ","),
		"--no-chrome",
		"--no-session-persistence",
		"--prompt-suggestions", "false",
		"--output-format", "stream-json",
		"--include-hook-events",
		"--verbose",
	}
}

// ExactModelID reports whether model is an exact Claude model ID rather than
// an alias. A benchmark row must name the model it ran on; an alias resolves
// to different models over time and would pool rows that never shared one.
func ExactModelID(model string) bool {
	if model == "" {
		return false
	}

	if strings.Contains(strings.ToLower(model), "latest") {
		return false
	}

	switch strings.ToLower(model) {
	case "default", "opus", "sonnet", "haiku", "fable":
		return false
	default:
		return strings.HasPrefix(model, "claude-")
	}
}

// LocalRuntimeID describes the host the agent and the fixture's checks run
// on: the sandbox generation, the platform, the agent version, and the
// version of every toolchain the checks call. It is part of the fingerprint,
// so rows from different toolchains are never pooled.
func LocalRuntimeID(agentVersion string, checks []Command) string {
	parts := []string{
		"claude-native-sandbox-v2",
		runtime.GOOS + "/" + runtime.GOARCH,
		"agent=" + agentVersion,
	}

	seen := make(map[string]bool)

	for _, check := range checks {
		if seen[check.Name] {
			continue
		}

		seen[check.Name] = true

		versionArgs := []string{"--version"}
		if check.Name == "go" {
			versionArgs = []string{"version"}
		}

		parts = append(parts, check.Name+"="+CommandVersion(check.Name, versionArgs...))
	}

	return strings.Join(parts, ";")
}

// CommandVersion asks a binary for its version and returns the first output
// line, or "unknown" when the binary has no such flag or does not answer.
func CommandVersion(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = processWaitDelay

	out, err := cmd.CombinedOutput()
	if err != nil {
		return "unknown"
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return "unknown"
	}

	line, _, _ := strings.Cut(output, "\n")

	return line
}
