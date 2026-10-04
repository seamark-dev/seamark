// Package agent invokes the user's own coding-agent CLI for one-shot
// prompted tasks — the inference seam behind features like lesson
// distillation. Seamark never holds credentials and never speaks a
// model API: it shells out to a CLI the user already has and has
// already authenticated, the same trust boundary as `gh` for review
// mining. An absent or failing CLI degrades to an error the caller
// surfaces as a note, never a broken feature.
//
// Callers use a shared Invoker interface. This package provides the
// Claude preset; internal/integration registers other clients through
// CommandSpec. Custom argv supports additional CLIs.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/render"
)

// Invoker runs one prompted task against the configured agent CLI.
type Invoker interface {
	// Invoke sends prompt on the agent's stdin and returns its text
	// reply. The context bounds the run: a distillation batch is worth
	// minutes, not hours.
	Invoke(ctx context.Context, prompt string) (string, error)
	// Name identifies the adapter for provenance ("distilled by …").
	Name() string
}

// presets maps an agent name to the argv that runs it in one-shot
// print mode, prompt on stdin. Only CLIs with a verified non-interactive
// mode belong here; anything else goes through the config's custom argv.
var presets = map[string][]string{
	"claude": {"claude", "-p"},
}

// Config selects the agent CLI (`agent:` in .seamark/config.yaml —
// committed, like every seamark overlay, so a team shares one choice).
// An absent section means the claude preset.
type Config struct {
	Agent struct {
		// CLI names a preset ("claude"). Ignored when Argv is set.
		CLI string `yaml:"cli"`
		// Argv is the escape hatch for any other agent: the exact
		// command to run, prompt delivered on stdin.
		Argv []string `yaml:"argv"`
	} `yaml:"agent"`
}

// LoadConfig reads the agent section of <root>/.seamark/config.yaml.
// The file is shared with the indexing options; each package reads its
// own section. A missing file yields defaults; a malformed one is an
// error (the same contract as everywhere else: a typo'd config must
// not be silently ignored).
func LoadConfig(root string) (*Config, error) {
	cfg := &Config{}

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "config.yaml"))
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}

		return nil, err
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("agent config: %w", err)
	}

	return cfg, nil
}

// Resolve wraps ResolveCommand for callers that need a provenance name
// and argv. It validates configuration without checking PATH.
func Resolve(cfg *Config) (name string, argv []string, err error) {
	spec, err := ResolveCommand(cfg)
	if err != nil {
		return "", nil, err
	}

	return spec.Name, spec.Argv, nil
}

// New wraps ResolveCommand and NewCommand for this package's presets.
// Invalid configuration or a missing executable fails before invocation.
func New(cfg *Config) (Invoker, error) {
	spec, err := ResolveCommand(cfg)
	if err != nil {
		return nil, err
	}

	return NewCommand(spec)
}

// cliInvoker shells out to an agent CLI in one-shot mode.
type cliInvoker struct {
	name string
	argv []string
	// dir defaults to the caller's working directory.
	dir string
	// diagnostic selects the output included in errors.
	diagnostic Diagnostic
}

// Keep CLI errors short enough to display as notes.
const (
	diagnosticTailLines = 3
	maxDiagnostic       = 400
)

// waitDelay bounds pipe cleanup after exit or cancellation, even when
// descendants keep pipes open. Cancellation kills only the client;
// it shares the caller's process group so terminal interrupts still reach it.
// Tests can shorten this delay.
var waitDelay = 5 * time.Second

func (c *cliInvoker) Name() string { return c.name }

// Output caps: a well-behaved agent reply is kilobytes; a runaway CLI
// must not grow seamark's memory without limit. Overflow is discarded —
// a truncated reply fails downstream validation, which retries.
const (
	maxStdout = 4 << 20
	maxStderr = 64 << 10
)

// boundedBuffer keeps at most max bytes and silently discards the rest,
// always reporting success so the subprocess never sees a write error.
type boundedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}

		b.buf.Write(p)
	}

	return n, nil
}

func (b *boundedBuffer) String() string { return b.buf.String() }

// Invoke sends the prompt on stdin to avoid argv limits and process-table
// exposure. Cancellation kills the client; waitDelay bounds pipe cleanup.
func (c *cliInvoker) Invoke(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	cmd.Dir = c.dir
	cmd.WaitDelay = waitDelay
	cmd.Stdin = strings.NewReader(prompt)

	out := &boundedBuffer{max: maxStdout}
	errb := &boundedBuffer{max: maxStderr}
	cmd.Stdout = out
	cmd.Stderr = errb

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("agent %s: %w", c.name, ctx.Err())
		}

		msg := diagnostic(errb.String(), c.diagnostic)
		if msg == "" {
			// Some CLIs put the complaint on stdout in pipe mode —
			// claude -p reports usage limits and auth errors there.
			// Without this, the user sees a bare exit status.
			msg = diagnostic(out.String(), c.diagnostic)
		}

		if msg == "" {
			msg = err.Error()
		} else {
			msg = fmt.Sprintf("%s (%s)", msg, err.Error())
		}

		return "", fmt.Errorf("agent %s: %s", c.name, msg)
	}

	return out.String(), nil
}

// diagnostic selects the first non-empty line or the last few, in order,
// then sanitizes and truncates the text for terminal display.
// Blank output returns an empty string.
func diagnostic(output string, rule Diagnostic) string {
	var lines []string

	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}

	if len(lines) == 0 {
		return ""
	}

	switch rule {
	case DiagnosticTail:
		if len(lines) > diagnosticTailLines {
			lines = lines[len(lines)-diagnosticTailLines:]
		}
	default:
		lines = lines[:1]
	}

	return render.Truncate(render.Sanitize(strings.Join(lines, " | ")), maxDiagnostic)
}

func presetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}

	return names
}
