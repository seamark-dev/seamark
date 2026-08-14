package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fake builds an Invoker from a shell snippet — the injectable-CLI
// pattern; tests never touch a real agent.
func fake(script string) Invoker {
	return &cliInvoker{name: "custom", argv: []string{"sh", "-c", script}}
}

func TestInvokePipesPromptAndReturnsOutput(t *testing.T) {
	out, err := fake("tr a-z A-Z").Invoke(context.Background(), "hello agent")
	require.NoError(t, err)
	assert.Equal(t, "HELLO AGENT", out)
}

func TestInvokeSurfacesFirstStderrLine(t *testing.T) {
	_, err := fake("echo 'not logged in' >&2; echo 'run /login' >&2; exit 3").
		Invoke(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not logged in")
	assert.NotContains(t, err.Error(), "/login", "only the first line; CLIs get chatty")
}

func TestInvokeFallsBackToStdoutComplaint(t *testing.T) {
	// claude -p reports usage limits and auth errors on STDOUT in pipe
	// mode with an empty stderr; the user must see the message, not a
	// bare exit status.
	_, err := fake("cat >/dev/null; echo 'Claude AI usage limit reached'; exit 1").
		Invoke(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage limit reached")
	assert.Contains(t, err.Error(), "exit status 1", "the exit code stays visible beside the message")
}

func TestInvokeSilentFailureKeepsExitStatus(t *testing.T) {
	_, err := fake("cat >/dev/null; exit 7").Invoke(context.Background(), "x")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 7")
}

func TestInvokeHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := fake("sleep 5").Invoke(ctx, "x")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestBoundedBufferCapsWithoutFailingWrites(t *testing.T) {
	b := &boundedBuffer{max: 8}

	n, err := b.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, n, "the writer must never see a short write")
	assert.Equal(t, "01234567", b.String(), "overflow is discarded at the cap")

	n, err = b.Write([]byte("more"))
	require.NoError(t, err)
	assert.Equal(t, 4, n)
	assert.Equal(t, "01234567", b.String())
}

func TestInvokeCapsRunawayOutput(t *testing.T) {
	// 8 MB of output must cost at most the cap, not the whole payload.
	out, err := fake("head -c 8388608 /dev/zero | tr '\\0' 'x'").Invoke(context.Background(), "")
	require.NoError(t, err)
	assert.Equal(t, maxStdout, len(out))
}

func TestNewResolvesConfig(t *testing.T) {
	// Custom argv wins and is usable as-is.
	cfg := &Config{}
	cfg.Agent.Argv = []string{"sh", "-c", "cat"}

	inv, err := New(cfg)
	require.NoError(t, err)
	assert.Equal(t, "custom", inv.Name())

	// An unknown preset fails fast and names the known ones.
	cfg = &Config{}
	cfg.Agent.CLI = "hal9000"
	_, err = New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "claude")

	// A missing binary fails at construction, not at first use.
	cfg = &Config{}
	cfg.Agent.Argv = []string{"definitely-not-a-binary-xyz"}
	_, err = New(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found on PATH")

	// The default is the claude preset; whether that constructs depends
	// on the machine, but the resolution must point at claude either way.
	inv, err = New(&Config{})
	if _, lookErr := exec.LookPath("claude"); lookErr == nil {
		require.NoError(t, err)
		assert.Equal(t, "claude", inv.Name())
	} else {
		require.Error(t, err)
		assert.Contains(t, err.Error(), "claude")
	}
}

func TestLoadConfigSharesFileWithIndexSection(t *testing.T) {
	// Absent file: defaults.
	cfg, err := LoadConfig(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, cfg.Agent.CLI)
	assert.Empty(t, cfg.Agent.Argv)

	// One config.yaml, two sections, two readers: the agent section
	// parses and the index section is ignored here (and vice versa —
	// locked from the index side in its own tests).
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"), []byte(
		"index:\n  generated: true\nagent:\n  cli: claude\n  argv: [\"my-llm\", \"--print\"]\n"), 0o644))

	cfg, err = LoadConfig(root)
	require.NoError(t, err)
	assert.Equal(t, "claude", cfg.Agent.CLI)
	assert.Equal(t, []string{"my-llm", "--print"}, cfg.Agent.Argv)

	// Malformed YAML is loud, same contract as every seamark overlay.
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "config.yaml"),
		[]byte("agent: [broken\n"), 0o644))
	_, err = LoadConfig(root)
	require.Error(t, err)
}
