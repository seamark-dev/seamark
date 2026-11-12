package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/hooks"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
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
			//
			// Resolve refuses a -C that names no directory. Without the
			// check, the gate reads no policy there, allows under the
			// default, and its audit creates the mistyped directory.
			ws, err := workspace.Resolve(opts.workspace, "")

			if hookMode {
				return runGateHook(cmd, gateHookRun{root: ws.Root, rootErr: err,
					clientID: hookClient, enforce: enforce, asJSON: asJSON})
			}

			if err != nil {
				return gate.FailClosed(err, enforce)
			}

			decision, err := gate.Decide(gate.CommandRequest{Root: ws.Root, Command: commandLine, Enforce: enforce}, auditWarning(cmd))
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
func runGateHook(cmd *cobra.Command, run gateHookRun) error {
	enforce, asJSON := run.enforce, run.asJSON

	client, err := gateHookClient(run.clientID)
	if err != nil {
		return gate.FailClosed(err, enforce)
	}

	fail := func(err error) error {
		if !enforce {
			return err
		}

		return hookReply(cmd, client.Commands.EncodeFailure(err))
	}

	// A workspace that names no directory is a failure before the policy
	// loads. It takes the client's failure reply, as a bad payload does,
	// so each client reads the block in its own form.
	if run.rootErr != nil {
		return fail(run.rootErr)
	}

	root := run.root

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

// gateHookRun holds the inputs of one gate hook run. rootErr is the
// workspace resolve error. The hook reports it only after the client
// lookup, because the failure reply needs the client.
type gateHookRun struct {
	root     string
	rootErr  error
	clientID string
	enforce  bool
	asJSON   bool
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
		lessons bool
	)

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Evaluate the blast radius of a diff against policy",
		Long: `Maps a unified diff's changed lines to symbols and unions their
transitively-propagated effect tags: what can this change ultimately
reach? Reads the diff from stdin when piped, else runs "git diff HEAD".
A pipe that sends no byte within 1s also falls back to "git diff HEAD";
under enforcement such a pipe blocks instead.
Policy rules over diff.* decide the verdict.

With --lessons, check also verifies the working tree against the
executable lesson checks, as "seamark lessons --verify" does, whatever
diff the policy read. A lesson violation exits 2, as a policy block does.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve refuses a -C that names no directory before any read
			// or write. The index refresh would otherwise name a file or a
			// missing path in the words of its own walk or root check.
			ws, err := workspace.Resolve(opts.workspace, opts.dbPath)
			if err != nil {
				return err
			}

			root, dbPath := ws.Root, ws.DBPath

			// The lesson checks read a git work tree. A directory outside
			// git is refused before the index refresh and the audit
			// append, so the refusal writes nothing.
			var lessonsRoot string

			if lessons {
				if lessonsRoot, err = verifyRoot(opts.workspace); err != nil {
					return err
				}
			}

			// A terminal or /dev/null on stdin sends no diff, so check reads
			// git diff HEAD. It reads it before the index refresh, as MCP
			// does, so a folder outside git or before the first commit gets
			// no .seamark folder.
			var diffText, policyInput string

			if stdinIsDevice(cmd.InOrStdin()) {
				if diffText, err = checkGitDiffHead(root); err != nil {
					return err
				}

				policyInput = report.PolicyInputGitDiffHead
			}

			// A stale index makes blast-radius answers WRONG (line spans
			// drift); check repairs freshness instead of warning about it.
			// Run's internal fast path makes this free when nothing changed.
			// A refresh that fails stops the command, also the refusal of a
			// move of another root, because a verdict needs a current graph.
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

			// The policy loads before stdin is read, because the mode
			// decides what an idle stdin means.
			policy, err := gate.LoadPolicy(root)
			if err != nil {
				return err
			}

			if enforce {
				policy.Mode = "enforce"
			}

			if policyInput == "" {
				diffText, policyInput, err = readDiff(cmd.InOrStdin(), cmd.ErrOrStderr(), root, stdinIdleWait,
					policy.Mode == "enforce")
				if err != nil {
					return err
				}
			}

			source := gate.SourceGitDiffHead
			if policyInput == report.PolicyInputStdinDiff {
				source = gate.SourceStdin
			}

			decision, err := gate.EvalDiff(policy, st, diffText, source)
			if err != nil {
				return err
			}

			// The diff itself is the input: hashed by default, redacted
			// and truncated when raw logging is opted into.
			if err := gate.Audit(root, "check", diffText, policy, decision); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "seamark: audit log: %v\n", err)
			}

			if lessons {
				return runCheckLessons(cmd, checkLessonsRun{
					root:        root,
					lessonsRoot: lessonsRoot,
					st:          st,
					diff:        diffText,
					policyInput: policyInput,
					decision:    decision,
					asJSON:      asJSON,
				})
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
	cmd.Flags().BoolVar(&lessons, "lessons", false,
		"also verify the working tree against the executable lesson checks; with --json print the check-lessons/1 document")

	return cmd
}

// stdinIdleWait is how long check waits for the first byte of a piped
// diff. An agent harness can give check a stdin pipe that stays open and
// sends nothing. Without a limit, check then waits forever.
const stdinIdleWait = time.Second

// readDiff prefers piped stdin; otherwise asks git for the working-tree
// diff against HEAD. input is the source of the diff as a
// check-lessons/1 policy input. Empty stdin falls back to git, so input
// names the branch that gave the diff.
//
// Stdin that sends no byte within wait is idle. Without enforcement,
// idle stdin also falls back to git, and stderr names the fallback,
// because the source of the diff changes the verdict.
//
// Under enforcement, idle stdin blocks. A diff producer that starts
// late would otherwise get the verdict of `git diff HEAD`, which can
// allow what its own diff blocks. This follows the gate rule that a
// failure under enforcement must not fail open.
func readDiff(stdin io.Reader, stderr io.Writer, root string, wait time.Duration, enforced bool) (diff, input string, err error) {
	if !stdinIsDevice(stdin) {
		data, idle, err := readStdinDiff(stdin, wait)
		if err != nil {
			return "", "", err
		}

		switch {
		case idle && enforced:
			return "", "", gate.FailClosed(fmt.Errorf("stdin idle after %s; "+
				"enforce mode does not fall back to git diff HEAD (redirect stdin from /dev/null to check it)", wait), true)
		case idle:
			fmt.Fprintf(stderr, "seamark: stdin idle after %s; checking git diff HEAD\n", wait)
		}

		if len(data) > 0 {
			return string(data), report.PolicyInputStdinDiff, nil
		}
	}

	out, err := checkGitDiffHead(root)
	if err != nil {
		return "", "", err
	}

	return out, report.PolicyInputGitDiffHead, nil
}

// checkGitDiffHead runs gate.GitDiffHead and adds the CLI route to its
// reason. The route needs no git, so it can run when git diff HEAD fails.
func checkGitDiffHead(root string) (string, error) {
	out, err := gate.GitDiffHead(root)
	if err != nil {
		return "", fmt.Errorf("%w; %s", err, gate.DiffRouteCLI)
	}

	return out, nil
}

// stdinIsDevice reports a stdin that is a character device, such as a
// terminal or /dev/null. check does not read such a stdin, because a
// terminal sends no diff and would wait for a key.
func stdinIsDevice(stdin io.Reader) bool {
	f, ok := stdin.(*os.File)

	return ok && isTerminal(f)
}

// readStdinDiff reads stdin to EOF when its first byte arrives within
// wait. idle is true when no byte and no EOF arrive within wait.
//
// A goroutine does the first read, because Go cannot stop a read from a
// blocking pipe: os.Stdin usually has no read deadline. After a timeout,
// that read continues until the pipe sends a byte or closes, or the
// process exits. The channel holds the one result, so the goroutine
// never blocks on its send and always ends after its read. check reads
// stdin once per process, so bytes of that late read belong to no diff.
func readStdinDiff(stdin io.Reader, wait time.Duration) (data []byte, idle bool, err error) {
	first := make(chan firstRead, 1)

	go func() {
		buf := make([]byte, 32*1024)
		n, err := io.ReadAtLeast(stdin, buf, 1)
		first <- firstRead{data: buf[:n], err: err}
	}()

	timer := time.NewTimer(wait)
	defer timer.Stop()

	got, ok := awaitFirstRead(first, timer.C)
	if !ok {
		return nil, true, nil
	}

	// EOF before the first byte is an empty stdin, for example a closed
	// pipe. The caller then uses git without a notice.
	if errors.Is(got.err, io.EOF) {
		return nil, false, nil
	}

	if got.err != nil {
		return nil, false, got.err
	}

	// The first read has ended, so this is the only reader now. The bytes
	// of the first read start the diff.
	rest, err := io.ReadAll(stdin)
	if err != nil {
		return nil, false, err
	}

	return append(got.data, rest...), false, nil
}

// firstRead is the result of the first read of a piped diff.
type firstRead struct {
	data []byte
	err  error
}

// awaitFirstRead returns the first read, or false when the deadline
// comes first.
//
// A select picks at random when both channels are ready. The second
// select therefore takes a first read that is ready at the deadline.
// Without it, the verdict could depend on chance, not on the input.
func awaitFirstRead(first <-chan firstRead, deadline <-chan time.Time) (firstRead, bool) {
	select {
	case got := <-first:
		return got, true
	case <-deadline:
	}

	select {
	case got := <-first:
		return got, true
	default:
		return firstRead{}, false
	}
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

	return blockedError(d)
}

// blockedError returns the gate.Blocked error of a blocking decision,
// or nil. check --lessons --json needs the error without the printed
// verdict, so the error has its own function.
func blockedError(d *gate.Decision) error {
	if !d.Blocking() {
		return nil
	}

	// The reasons must travel on stderr: that is what PreToolUse hooks
	// feed back to the agent, and "blocked" without a why leaves it
	// guessing instead of correcting course.
	reasons := make([]string, 0, len(d.Matches))
	for _, m := range d.Matches {
		reasons = append(reasons, render.Sanitize(m.Message))
	}

	return &gate.Blocked{Cause: errors.New(strings.Join(reasons, "; "))}
}
