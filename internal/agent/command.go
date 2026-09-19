package agent

import (
	"fmt"
	"slices"
)

// CommandSpec is the exact process one client invocation runs. It is a
// value, not a running invoker: callers disclose it in dry runs and
// diagnostics before any process starts, so it must not require the
// binary on PATH or any credential.
type CommandSpec struct {
	// Name identifies the client for provenance ("distilled by …").
	Name string
	// Argv is the command and its arguments, prompt delivered on stdin.
	Argv []string
	// Dir is the working directory for the process. Empty inherits the
	// caller's directory, which is the legacy custom-argv behavior.
	Dir string
}

// Validate checks the spec is runnable in principle: a name and a
// command are required. PATH lookup is a separate, later step.
func (s CommandSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("agent command: name is empty")
	}

	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return fmt.Errorf("agent %s: command must start with an executable", s.Name)
	}

	return nil
}

// ClaudeCommand returns the Claude Code one-shot preset as a spec. The
// compatibility resolver and the client registry share this one value,
// so the two paths cannot drift.
func ClaudeCommand() CommandSpec {
	return CommandSpec{Name: "claude", Argv: slices.Clone(presets["claude"])}
}
