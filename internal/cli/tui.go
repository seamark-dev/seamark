package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/scaffold"
	"github.com/seamark-dev/seamark/internal/tui"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// errNotInteractive reports a console launch without a terminal. The
// command prints plain guidance first, so scripts and CI get the
// equivalent commands instead of escape sequences.
var errNotInteractive = errors.New("seamark tui needs an interactive terminal")

// tuiGuidance lists the plain commands behind every console action. The
// CLI selects by proposal ID, and its trigger extraction asks every
// eligible proposal. So the text calls the commands the closest actions.
// Each write command shares the engine of its console action. Apply and
// dismiss share the decision flow. Retarget and prune share the pin
// maintenance. The trigger extraction shares the executor and the
// guarded save. So the text names no other difference.
const tuiGuidance = `seamark tui is an interactive, full-screen console (experimental).
This session has no interactive terminal, so it did not start.

The same information and the closest actions are available as plain commands:
  seamark doctor                      installation health
  seamark status                      semantic health of the index
  seamark index                       build or refresh the local index
  seamark init --client <claude|codex>  set up an integration (--print previews)
  seamark index --fixes-only          collect evidence from local fix commits
  seamark index --reviews             also fetch GitHub reviews through gh
  seamark lessons --distill --dry-run  plan a distillation (drop --dry-run to run it)
  seamark lessons --proposals         review the proposal ledger
  seamark lessons --apply <ids>       apply proposals (needs distill.write)
  seamark lessons --dismiss <ids>     dismiss proposals
  seamark lessons --retarget <ids>    set applied pins to the computed regions (needs distill.write)
  seamark lessons --prune <ids>       remove applied pins (needs distill.write)
  seamark lessons --extract-triggers --dry-run  plan a trigger backfill of every
                                      eligible proposal (drop --dry-run to run it)
  seamark lessons --stats             lesson delivery statistics

Settings live in .seamark/config.yaml: agent.cli, distill.write,
reviews.window_days, index.generated, and index.exclude.
`

func newTUICmd(opts *options) *cobra.Command {
	var ascii bool

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive workspace console (experimental)",
		Long: `Open an interactive console for one workspace: diagnostics, index
state, Claude and Codex integrations, the learning workflow (evidence from
local fixes and GitHub reviews, distillation with a reviewed plan,
proposal decisions, the installed pins with a previewed retarget or
prune, and trigger extraction for selected proposals), lesson delivery
statistics, and the settings that these workflows read. Opening and
browsing never write; every change, fetch, and agent run goes through a
preview that you confirm. Experimental: the plain commands stay the
stable interface.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// An invalid workspace fails here, before full-screen mode,
			// so the error stays readable in the shell.
			ws, err := workspace.Resolve(opts.workspace, opts.dbPath)
			if err != nil {
				return err
			}

			in, out := cmd.InOrStdin(), cmd.OutOrStdout()
			if !interactive(in, out) {
				fmt.Fprint(out, tuiGuidance)

				return errNotInteractive
			}

			if err := useReadOnlyGit(); err != nil {
				return err
			}

			return tui.Run(tui.Options{
				Services: services.New(ws, version, scaffold.BinaryPath()),
				ASCII:    ascii,
				In:       in,
				Out:      out,
			})
		},
	}

	cmd.Flags().BoolVar(&ascii, "ascii", false, "use ASCII glyphs instead of box-drawing and symbol characters")

	return cmd
}

// interactive reports a terminal on both streams. TERM=dumb has no
// cursor addressing, so it counts as noninteractive.
func interactive(in io.Reader, out io.Writer) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}

	return terminalStream(in) && terminalStream(out)
}

// terminalStream reports a stream that is a terminal. A character-device
// mode check is not enough: /dev/null is a character device too, and a
// console on it never gets a key and never ends.
func terminalStream(s any) bool {
	f, ok := s.(*os.File)

	return ok && term.IsTerminal(f.Fd())
}

// gitConfigKey is the git setting that stops `git diff` from refreshing
// the stat cache in .git/index. GIT_OPTIONAL_LOCKS does not stop that
// refresh, because git diff writes the index without the optional-lock
// check.
const gitConfigKey = "diff.autoRefreshIndex"

// readOnlyGitEnv returns the environment changes that keep git from
// rewriting .git/index while the console inspects the workspace.
//
// The index freshness check runs `git status` and `git diff HEAD` on
// open, on each refresh, and after each mutation. Both commands refresh
// the stat cache of .git/index and take .git/index.lock. docs/tui.md
// says that opening and refreshing the console never write. Also, the
// lock can make a concurrent git command of the user fail.
// GIT_OPTIONAL_LOCKS=0 stops the write of `git status`. The
// diff.autoRefreshIndex=false setting stops the write of `git diff`.
// Neither setting changes the command output, so the freshness
// fingerprint stays the same. Both affect only the optional stat-cache
// write, so the git commands of an index build work as before.
//
// The setting goes in through GIT_CONFIG_COUNT, the git mechanism for
// configuration from the environment. The helper appends to entries that
// already exist. It adds nothing when the count is not a number, because
// git then refuses every command and a new entry cannot help.
func readOnlyGitEnv(lookup func(string) (string, bool)) [][2]string {
	env := [][2]string{{"GIT_OPTIONAL_LOCKS", "0"}}

	n := 0

	if v, ok := lookup("GIT_CONFIG_COUNT"); ok && v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			return env
		}

		n = parsed
	}

	i := strconv.Itoa(n)

	return append(env,
		[2]string{"GIT_CONFIG_KEY_" + i, gitConfigKey},
		[2]string{"GIT_CONFIG_VALUE_" + i, "false"},
		[2]string{"GIT_CONFIG_COUNT", strconv.Itoa(n + 1)})
}

// useReadOnlyGit sets readOnlyGitEnv for this process. The console runs
// git through child processes, and they inherit the environment. The
// change lasts only for this process, so the user's shell keeps its
// settings.
func useReadOnlyGit() error {
	for _, kv := range readOnlyGitEnv(os.LookupEnv) {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return fmt.Errorf("set %s: %w", kv[0], err)
		}
	}

	return nil
}
