package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

func newGateCmd(opts *options) *cobra.Command {
	var (
		commandLine string
		hookMode    bool
		hookClient  string
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
enforce mode a deny/approval verdict exits with code 2.

With --hook the command reads the agent's PreToolUse JSON payload from
stdin. Without --client it is a Claude Code event; with --client codex it
is a Codex Bash event.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// --client selects the native event format of a hook run and
			// has no meaning for a command given on the command line.
			if hookClient != "" && !hookMode {
				return errors.New("--client applies to --hook only")
			}

			// Under enforcement, the gate's OWN failures must block too: a
			// security hook that fails open on a malformed payload or a
			// broken policy is itself a bypass. Before the policy loads only
			// the explicit flag enforces; gate.Decide applies the policy's
			// own mode after.
			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return gate.FailClosed(err, enforce)
			}

			if hookMode {
				return runGateHook(cmd, root, hookClient, enforce, asJSON)
			}

			decision, err := gate.Decide(gate.CommandRequest{Root: root, Command: commandLine, Enforce: enforce}, auditWarning(cmd))
			if err != nil {
				return err
			}

			return renderDecision(cmd.OutOrStdout(), decision, asJSON)
		},
	}

	cmd.Flags().StringVar(&commandLine, "command", "", "the shell command to evaluate")
	cmd.Flags().BoolVar(&hookMode, "hook", false,
		"read an agent's PreToolUse JSON payload from stdin (replaces --command; no jq needed)")
	cmd.Flags().StringVar(&hookClient, "client", "",
		"with --hook: the agent whose hook event to read ("+strings.Join(integration.Builtin().IDs(), ", ")+
			"); without it the event is a Claude Code event")
	cmd.Flags().BoolVar(&enforce, "enforce", false, "override policy mode: block on deny/approval verdicts")
	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}

// runGateHook is the PreToolUse path. The client adapter decodes the
// native event and encodes the reply; the gate decides. The verdict is
// still printed as the plain command prints it: both clients ignore
// plain text on stdout at exit 0, and a person who runs the hook by
// hand sees what the agent's hook saw.
//
// A failure before the policy loads blocks under --enforce only, in the
// client's own form. A failure that gate.Decide blocks is translated
// the same way; any other failure is a plain error, and the command
// proceeds.
func runGateHook(cmd *cobra.Command, root, clientID string, enforce, asJSON bool) error {
	client, err := gateHookClient(clientID)
	if err != nil {
		return gate.FailClosed(err, enforce)
	}

	fail := func(err error) error {
		if !enforce {
			return err
		}

		return hookReply(cmd, client.Commands.EncodeFailure(err))
	}

	payload, err := integration.ReadHookPayload(cmd.InOrStdin())
	if err != nil {
		return fail(err)
	}

	event, err := client.Commands.DecodeCommand(payload)
	if err != nil {
		return fail(err)
	}

	decision, err := gate.Decide(gate.CommandRequest{Root: root, Command: event.Command, Enforce: enforce}, auditWarning(cmd))
	if err != nil {
		var blocked *gate.Blocked
		if errors.As(err, &blocked) {
			return hookReply(cmd, client.Commands.EncodeFailure(blocked.Cause))
		}

		return err
	}

	if err := printDecision(cmd.OutOrStdout(), decision, asJSON); err != nil {
		return err
	}

	reply, err := client.Commands.EncodeDecision(decision)
	if err != nil {
		// The adapter could not say what it decided. Under enforcement
		// that is the gate's own failure and blocks.
		return gate.FailClosed(err, decision.Mode == hooks.ModeEnforce)
	}

	return hookReply(cmd, reply)
}

// gateHookClient returns the client whose native events a gate hook
// run translates. The ID comes from the installed hook command, never
// from the payload, so a payload cannot choose its decoder. A command
// without --client keeps Claude Code semantics, because every installed
// Claude Code hook runs exactly that command.
//
// An unknown ID, or a client without a command codec, is a broken hook
// command. Unlike the lessons hook, the gate reports it as an error: a
// gate that runs nothing must not look like a gate that allowed.
func gateHookClient(clientID string) (integration.Client, error) {
	if clientID == "" {
		clientID = integration.ClaudeID
	}

	registry := integration.Builtin()

	client, ok := registry.Lookup(clientID)
	if !ok {
		return integration.Client{}, fmt.Errorf("unknown hook client %q (known: %s)",
			render.Sanitize(clientID), strings.Join(registry.IDs(), ", "))
	}

	if err := client.Require(integration.CapabilityCommands); err != nil {
		return integration.Client{}, err
	}

	return client, nil
}

// hookReply writes the client's reply. A blocking reply travels as a
// gate.Blocked error with the reason as its cause: Execute prints it
// after "seamark:" on stderr, where both clients read the reason, and
// maps it to exit 2, the code both clients read as a block.
func hookReply(cmd *cobra.Command, reply integration.HookReply) error {
	if _, err := cmd.OutOrStdout().Write(reply.Stdout); err != nil {
		return err
	}

	if reply.ExitCode == 0 {
		_, err := cmd.ErrOrStderr().Write(reply.Stderr)

		return err
	}

	if reason := strings.TrimSpace(string(reply.Stderr)); reason != "" {
		return &gate.Blocked{Cause: errors.New(reason)}
	}

	return &gate.Blocked{}
}

// auditWarning reports a failed audit append on stderr. The audit is
// best effort: a full disk must not change a verdict.
func auditWarning(cmd *cobra.Command) func(error) {
	return func(err error) {
		fmt.Fprintf(cmd.ErrOrStderr(), "seamark: audit log: %v\n", err)
	}
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

			// The diff itself is the input: hashed by default, redacted
			// and truncated when raw logging is opted into.
			if err := gate.Audit(root, "check", diffText, policy, decision); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "seamark: audit log: %v\n", err)
			}

			renderErr := renderDecision(cmd.OutOrStdout(), decision, asJSON)

			// A blocked check still gets its advisory — a deny is
			// exactly when the lessons for the touched files matter
			// most; only a genuine render failure skips it. JSON stays
			// verdict-shaped either way.
			if renderErr != nil && !errors.Is(renderErr, gate.ErrBlocked) {
				return renderErr
			}

			if !asJSON {
				changed := gate.ChangedPaths(diffText)
				report.CheckCompanions(cmd.OutOrStdout(), st, root, changed)
				report.CheckAdvisory(cmd.OutOrStdout(), st, root, changed)
			}

			return renderErr
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

// printDecision prints the verdict in the human or the JSON form.
func printDecision(w io.Writer, d *gate.Decision, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(d)
	}

	report.Decision(w, d)

	return nil
}

// renderDecision prints the verdict and returns a gate.Blocked error
// when the decision must stop the caller (mapped to exit code 2 by
// Execute).
func renderDecision(w io.Writer, d *gate.Decision, asJSON bool) error {
	if err := printDecision(w, d, asJSON); err != nil {
		return err
	}

	if d.Blocking() {
		// The reasons must travel on stderr: that is what PreToolUse hooks
		// feed back to the agent, and "blocked" without a why leaves it
		// guessing instead of correcting course.
		reasons := make([]string, 0, len(d.Matches))
		for _, m := range d.Matches {
			reasons = append(reasons, render.Sanitize(m.Message))
		}

		return &gate.Blocked{Cause: errors.New(strings.Join(reasons, "; "))}
	}

	return nil
}
