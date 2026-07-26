package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/effects"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

func newGateCmd(opts *options) *cobra.Command {
	var (
		commandLine string
		hookMode    bool
		enforce     bool
		asJSON      bool
	)

	cmd := &cobra.Command{
		Use:   "gate --command <shell command>",
		Short: "Classify a command's effects and evaluate policy before it runs",
		Long: `Parses the command with a real shell parser (pipelines, chains and
substitutions included, variable indirection detected), classifies it
against the effect catalogue, and evaluates .seamark/policy.yaml over the
declared environment. Designed for agent PreToolUse hooks and CI: in
enforce mode a deny/approval verdict exits with code 2.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Under enforcement, the gate's OWN failures must block too: a
			// security hook that fails open on a malformed payload or a
			// broken policy is itself a bypass.
			enforced := enforce
			failClosed := func(err error) error {
				if enforced {
					return fmt.Errorf("%w: %v", gate.ErrBlocked, err)
				}

				return err
			}

			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return failClosed(err)
			}

			if hookMode {
				payload, err := readHookCommand(cmd.InOrStdin())
				if err != nil {
					return failClosed(err)
				}

				commandLine = payload
			}

			if strings.TrimSpace(commandLine) == "" {
				return failClosed(errors.New("empty command"))
			}

			policy, err := gate.LoadPolicy(root)
			if err != nil {
				return failClosed(err)
			}

			if enforce {
				policy.Mode = "enforce"
			}

			enforced = policy.Mode == "enforce"

			catalog, err := effects.Load(root)
			if err != nil {
				return failClosed(err)
			}

			decision, err := gate.EvalCommand(policy, catalog, root, commandLine)
			if err != nil {
				return failClosed(err)
			}

			if err := gate.Audit(root, "gate", commandLine, decision); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "seamark: audit log: %v\n", err)
			}

			return renderDecision(cmd.OutOrStdout(), decision, asJSON)
		},
	}

	cmd.Flags().StringVar(&commandLine, "command", "", "the shell command to evaluate")
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"read a Claude Code PreToolUse JSON payload from stdin (replaces --command; no jq needed)")
	cmd.Flags().BoolVar(&enforce, "enforce", false, "override policy mode: block on deny/approval verdicts")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}

// readHookCommand extracts tool_input.command from a PreToolUse hook
// payload — natively, so a missing jq can never collapse the command to
// an empty string and fail open.
func readHookCommand(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return "", err
	}

	var payload struct {
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}

	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("hook payload: %w", err)
	}

	return payload.ToolInput.Command, nil
}

func newCheckCmd(opts *options) *cobra.Command {
	var (
		enforce bool
		asJSON  bool
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Evaluate the blast radius of a diff against policy",
		Long: `Maps a unified diff's changed lines to symbols and unions their
transitively-propagated effect tags: what can this change ultimately
reach? Reads the diff from stdin when piped, else runs "git diff HEAD".
Policy rules over diff.* decide the verdict.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return err
			}

			dbPath := opts.dbPath
			if dbPath == "" {
				dbPath = store.DefaultPath(root)
			}

			// A stale index makes blast-radius answers WRONG (line spans
			// drift); check repairs freshness instead of warning about it.
			// Run's internal fast path makes this free when nothing changed.
			sum, err := index.Run(index.Options{Root: root, DBPath: dbPath,
				Logf: func(format string, a ...any) {
					fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
				}})
			if err != nil {
				return err
			}

			if !sum.Skipped {
				fmt.Fprintln(cmd.ErrOrStderr(), "seamark: index refreshed")
			}

			st, err := store.Open(dbPath)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only

			diffText, err := readDiff(cmd.InOrStdin(), root)
			if err != nil {
				return err
			}

			policy, err := gate.LoadPolicy(root)
			if err != nil {
				return err
			}

			if enforce {
				policy.Mode = "enforce"
			}

			decision, err := gate.EvalDiff(policy, st, diffText)
			if err != nil {
				return err
			}

			if err := gate.Audit(root, "check", fmt.Sprintf("diff (%d bytes)", len(diffText)), decision); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "seamark: audit log: %v\n", err)
			}

			return renderDecision(cmd.OutOrStdout(), decision, asJSON)
		},
	}

	cmd.Flags().BoolVar(&enforce, "enforce", false, "override policy mode: block on deny/approval verdicts")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}

// readDiff prefers piped stdin; otherwise asks git for the working-tree
// diff against HEAD.
func readDiff(stdin io.Reader, root string) (string, error) {
	if f, ok := stdin.(*os.File); !ok || (f == os.Stdin && !isTerminal(f)) {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}

		if len(data) > 0 {
			return string(data), nil
		}
	}

	out, err := gitOutput(root, "diff", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git diff: %w", err)
	}

	return out, nil
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeCharDevice != 0
}

// renderDecision prints the verdict and returns gate.ErrBlocked when the
// decision must stop the caller (mapped to exit code 2 by Execute).
func renderDecision(w io.Writer, d *gate.Decision, asJSON bool) error {
	if asJSON {
		if err := json.NewEncoder(w).Encode(d); err != nil {
			return err
		}
	} else {
		report.Decision(w, d)
	}

	if d.Blocking() {
		// The reasons must travel on stderr: that is what PreToolUse hooks
		// feed back to the agent, and "blocked" without a why leaves it
		// guessing instead of correcting course.
		reasons := make([]string, 0, len(d.Matches))
		for _, m := range d.Matches {
			reasons = append(reasons, render.Sanitize(m.Message))
		}

		return fmt.Errorf("%w: %s", gate.ErrBlocked, strings.Join(reasons, "; "))
	}

	return nil
}
