package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCommandKeepsTheLegacyPrecedence(t *testing.T) {
	// Custom argv takes precedence and is copied to avoid mutating config.
	cfg := &Config{}
	cfg.Agent.CLI = "claude"
	cfg.Agent.Argv = []string{"my-llm", "--stdin"}

	spec, err := ResolveCommand(cfg)
	require.NoError(t, err)
	assert.Equal(t, CommandSpec{Name: "custom", Argv: []string{"my-llm", "--stdin"}}, spec)

	spec.Argv[0] = "mutated"
	assert.Equal(t, "my-llm", cfg.Agent.Argv[0])

	// Default to the shared Claude preset.
	spec, err = ResolveCommand(&Config{})
	require.NoError(t, err)
	assert.Equal(t, ClaudeCommand(), spec)

	// An unknown preset and an empty custom command are errors.
	cfg = &Config{}
	cfg.Agent.CLI = "hal9000"
	_, err = ResolveCommand(cfg)
	require.ErrorContains(t, err, `unknown agent cli "hal9000"`)
	assert.Contains(t, err.Error(), "claude")

	cfg = &Config{}
	cfg.Agent.Argv = []string{""}
	_, err = ResolveCommand(cfg)
	require.Error(t, err)

	// The compatibility resolver returns the same preset.
	name, argv, err := Resolve(&Config{})
	require.NoError(t, err)
	assert.Equal(t, "claude", name)
	assert.Equal(t, ClaudeCommand().Argv, argv)
}

func TestNewCommandChecksTheSpecAndThePath(t *testing.T) {
	_, err := NewCommand(CommandSpec{Name: "n"})
	require.Error(t, err, "an invalid spec fails before any PATH lookup")

	_, err = NewCommand(CommandSpec{Name: "n", Argv: []string{"definitely-not-a-binary-xyz"}})
	require.ErrorContains(t, err, `"definitely-not-a-binary-xyz" not found on PATH`)

	inv, err := NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", "cat"}})
	require.NoError(t, err)
	assert.Equal(t, "fake", inv.Name(), "provenance carries the spec's name")

	out, err := inv.Invoke(context.Background(), "prompt on stdin")
	require.NoError(t, err)
	assert.Equal(t, "prompt on stdin", out)
}

func TestNewCommandRunsInTheSpecDirectory(t *testing.T) {
	dir := t.TempDir()

	inv, err := NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", "cat >/dev/null; pwd"}, Dir: dir})
	require.NoError(t, err)

	out, err := inv.Invoke(context.Background(), "x")
	require.NoError(t, err)

	got, err := filepath.EvalSymlinks(strings.TrimSpace(out))
	require.NoError(t, err)
	want, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// Empty Dir inherits the caller's directory.
	here, err := os.Getwd()
	require.NoError(t, err)

	inv, err = NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", "cat >/dev/null; pwd"}})
	require.NoError(t, err)

	out, err = inv.Invoke(context.Background(), "x")
	require.NoError(t, err)

	got, err = filepath.EvalSymlinks(strings.TrimSpace(out))
	require.NoError(t, err)
	want, err = filepath.EvalSymlinks(here)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	// A missing directory fails at invocation.
	inv, err = NewCommand(CommandSpec{Name: "fake", Argv: []string{"sh", "-c", "pwd"}, Dir: filepath.Join(dir, "missing")})
	require.NoError(t, err, "construction checks the binary, not the directory")

	_, err = inv.Invoke(context.Background(), "x")
	require.Error(t, err)
}

func TestInvokeCancellationIsBoundedWhenADescendantHoldsThePipes(t *testing.T) {
	// A surviving descendant holds stdout open after cancellation.
	// waitDelay must let Invoke return before that descendant exits.
	previous := waitDelay
	waitDelay = 200 * time.Millisecond

	t.Cleanup(func() { waitDelay = previous })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := fake("sleep 10 & sleep 10").Invoke(ctx, "x")
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, elapsed, 5*time.Second, "the wait is bounded by waitDelay, not by the descendant")
}

func TestInvokeReturnsTheFinalTextAndKeepsDiagnosticsApart(t *testing.T) {
	// Successful replies exclude progress written to stderr.
	out, err := fake("cat >/dev/null; echo 'thinking…' >&2; echo 'final reply'").Invoke(context.Background(), "x")
	require.NoError(t, err)
	assert.Equal(t, "final reply\n", out)

	// The caller's parser decides how to handle an empty reply.
	out, err = fake("cat >/dev/null").Invoke(context.Background(), "x")
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestDiagnosticRuleKeepsTheErrorAfterABanner(t *testing.T) {
	// Codex errors follow a banner, so the tail rule must retain them.
	banner := "cat >/dev/null; echo 'OpenAI Codex v0.157.0 (research preview)' >&2; echo '' >&2; " +
		"echo 'Error: HTTP 401 Unauthorized' >&2; exit 1"

	first, err := NewCommand(CommandSpec{Name: "first", Argv: []string{"sh", "-c", banner}})
	require.NoError(t, err)
	_, err = first.Invoke(context.Background(), "x")
	require.ErrorContains(t, err, "OpenAI Codex v0.157.0")
	assert.NotContains(t, err.Error(), "401", "the legacy rule is the first line")

	tail, err := NewCommand(CommandSpec{Name: "tail", Argv: []string{"sh", "-c", banner}, Diagnostic: DiagnosticTail})
	require.NoError(t, err)
	_, err = tail.Invoke(context.Background(), "x")
	require.ErrorContains(t, err, "HTTP 401 Unauthorized")
	assert.Contains(t, err.Error(), "exit status 1", "the exit code stays visible beside the message")

	// Bound the tail, preserve line order, and also handle errors on stdout.
	many := "cat >/dev/null; for i in 1 2 3 4 5; do echo \"line $i\" >&2; done; exit 2"
	tail, err = NewCommand(CommandSpec{Name: "tail", Argv: []string{"sh", "-c", many}, Diagnostic: DiagnosticTail})
	require.NoError(t, err)
	_, err = tail.Invoke(context.Background(), "x")
	require.ErrorContains(t, err, "line 3 | line 4 | line 5")
	assert.NotContains(t, err.Error(), "line 2")

	tail, err = NewCommand(CommandSpec{Name: "tail", Argv: []string{"sh", "-c", "cat >/dev/null; echo banner; echo 'quota exceeded'; exit 1"}, Diagnostic: DiagnosticTail})
	require.NoError(t, err)
	_, err = tail.Invoke(context.Background(), "x")
	require.ErrorContains(t, err, "banner | quota exceeded")

	require.Error(t, CommandSpec{Name: "n", Argv: []string{"x"}, Diagnostic: "middle"}.Validate())
}

func TestDiagnosticIsBoundedAndSanitized(t *testing.T) {
	long := strings.Repeat("x", 1000)
	assert.Len(t, []rune(diagnostic(long, DiagnosticFirstLine)), maxDiagnostic)
	assert.Equal(t, "not logged in", diagnostic("\x1bnot logged in\x07\nrun /login\n", DiagnosticFirstLine), "control characters are removed")
	assert.Equal(t, "a | b", diagnostic("\n\n a \n\n b \n", DiagnosticTail))
	assert.Empty(t, diagnostic(" \n \n", DiagnosticTail))
}
