// Package agent invokes the user's own coding-agent CLI for one-shot
// prompted tasks — the inference seam behind features like lesson
// distillation. Seamark never holds credentials and never speaks a
// model API: it shells out to a CLI the user already has and has
// already authenticated, the same trust boundary as `gh` for review
// mining. An absent or failing CLI degrades to an error the caller
// surfaces as a note, never a broken feature.
//
// The adapter layer keeps every caller agnostic: nothing outside this
// package knows which agent is configured. "claude" is the first
// preset; other agent CLIs arrive either as presets here or, today,
// through the custom argv escape hatch in the config.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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

// Resolve maps the config to the exact argv it would run, erroring on
// an unknown preset or an empty custom command — but without requiring
// the binary on PATH, so the pre-flight disclosure works even when the
// CLI is missing. The returned name identifies the adapter for
// provenance.
func Resolve(cfg *Config) (name string, argv []string, err error) {
	if len(cfg.Agent.Argv) > 0 {
		if cfg.Agent.Argv[0] == "" {
			return "", nil, fmt.Errorf("agent.argv must start with a command")
		}

		return "custom", cfg.Agent.Argv, nil
	}

	name = cfg.Agent.CLI
	if name == "" {
		name = "claude"
	}

	preset, ok := presets[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown agent cli %q (known: %s; or set agent.argv)",
			name, strings.Join(presetNames(), ", "))
	}

	return name, preset, nil
}

// New resolves the configured agent into an Invoker. It fails fast —
// unknown preset, empty custom argv, or a binary not on PATH — so the
// caller can say "distill unavailable: …" before any work is done.
func New(cfg *Config) (Invoker, error) {
	name, argv, err := Resolve(cfg)
	if err != nil {
		return nil, err
	}

	if _, err := exec.LookPath(argv[0]); err != nil {
		return nil, fmt.Errorf("agent cli %q not found on PATH", argv[0])
	}

	return &cliInvoker{name: name, argv: argv}, nil
}

// cliInvoker shells out to an agent CLI in one-shot mode.
type cliInvoker struct {
	name string
	argv []string
}

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

// Invoke runs the CLI with prompt on stdin. Stdin (not an argument)
// keeps large prompts off the process table and clear of argv limits.
func (c *cliInvoker) Invoke(ctx context.Context, prompt string) (string, error) {
	cmd := exec.CommandContext(ctx, c.argv[0], c.argv[1:]...)
	cmd.Stdin = strings.NewReader(prompt)

	out := &boundedBuffer{max: maxStdout}
	errb := &boundedBuffer{max: maxStderr}
	cmd.Stdout = out
	cmd.Stderr = errb

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("agent %s: %w", c.name, ctx.Err())
		}

		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			// Some CLIs put the complaint on stdout in pipe mode —
			// claude -p reports usage limits and auth errors there.
			// Without this, the user sees a bare exit status.
			msg = strings.TrimSpace(out.String())
		}

		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i] // first line carries the point; CLIs get chatty
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

func presetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}

	return names
}
