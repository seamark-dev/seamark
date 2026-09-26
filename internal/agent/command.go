package agent

import (
	"fmt"
	"os/exec"
	"slices"
	"strings"
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
	// Diagnostic selects error output; the default keeps the first non-empty line.
	Diagnostic Diagnostic
}

// Diagnostic selects which output Invoke includes in an error.
// Claude prints errors first; Codex prints them after a banner.
// Each client chooses a rule for the shared invoker to apply.
type Diagnostic string

const (
	// DiagnosticFirstLine keeps the first non-empty line, preserving legacy behavior.
	DiagnosticFirstLine Diagnostic = ""
	// DiagnosticTail keeps the last few non-empty lines, oldest first.
	DiagnosticTail Diagnostic = "tail"
)

// Validate requires a name, executable, and known diagnostic rule.
// It does not check PATH.
func (s CommandSpec) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("agent command: name is empty")
	}

	if len(s.Argv) == 0 || s.Argv[0] == "" {
		return fmt.Errorf("agent %s: command must start with an executable", s.Name)
	}

	if s.Diagnostic != DiagnosticFirstLine && s.Diagnostic != DiagnosticTail {
		return fmt.Errorf("agent %s: unknown diagnostic rule %q", s.Name, s.Diagnostic)
	}

	return nil
}

// ClaudeCommand returns the Claude Code one-shot preset as a spec. The
// compatibility resolver and the client registry share this one value,
// so the two paths cannot drift.
func ClaudeCommand() CommandSpec {
	return CommandSpec{Name: "claude", Argv: slices.Clone(presets["claude"])}
}

// ResolveCommand resolves this package's presets without checking PATH.
// Custom argv takes precedence, uses the name "custom", and inherits
// the caller's directory. The default preset is Claude.
// The client registry resolves additional clients separately.
func ResolveCommand(cfg *Config) (CommandSpec, error) {
	if len(cfg.Agent.Argv) > 0 {
		if cfg.Agent.Argv[0] == "" {
			return CommandSpec{}, fmt.Errorf("agent.argv must start with a command")
		}

		return CommandSpec{Name: "custom", Argv: slices.Clone(cfg.Agent.Argv)}, nil
	}

	name := cfg.Agent.CLI
	if name == "" {
		name = "claude"
	}

	preset, ok := presets[name]
	if !ok {
		return CommandSpec{}, fmt.Errorf("unknown agent cli %q (known: %s; or set agent.argv)",
			name, strings.Join(presetNames(), ", "))
	}

	return CommandSpec{Name: name, Argv: slices.Clone(preset)}, nil
}

// NewCommand validates the spec and checks PATH before returning an Invoker.
// It neither starts the client nor accesses credentials.
func NewCommand(spec CommandSpec) (Invoker, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	if _, err := exec.LookPath(spec.Argv[0]); err != nil {
		return nil, fmt.Errorf("agent cli %q not found on PATH", spec.Argv[0])
	}

	return &cliInvoker{name: spec.Name, argv: slices.Clone(spec.Argv), dir: spec.Dir, diagnostic: spec.Diagnostic}, nil
}
