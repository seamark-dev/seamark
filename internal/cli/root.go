// Package cli wires the seamark commands. Commands are thin: all logic
// lives in internal packages so the future LSP/MCP surfaces reuse it
// (RFC-001 §3: one engine, three protocols).
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/gate"
)

// version is stamped by the Makefile via -ldflags.
var version = "dev"

// options shared by all commands via persistent flags.
type options struct {
	workspace string
	dbPath    string
}

// New builds the root command tree.
func New() *cobra.Command {
	opts := &options{}
	root := &cobra.Command{
		Use:   "seamark",
		Short: "An effects-and-history graph for your repository",
		Long: `Seamark indexes what your repo's history knows and what your code can
actually do: which files really change together, why the weird parts are
weird, and which paths can reach production.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&opts.workspace, "workspace", "C", ".",
		"workspace directory (defaults to the enclosing git repo of the cwd)")
	root.PersistentFlags().StringVar(&opts.dbPath, "db", "",
		"index database path (default <workspace>/.seamark/index.db)")

	root.AddCommand(newInitCmd(opts), newIndexCmd(opts), newWhyCmd(opts),
		newOrientCmd(opts), newLessonsCmd(opts), newReportCmd(opts), newLSPCmd(opts),
		newGateCmd(opts), newCheckCmd(opts), newMCPCmd(opts), newStateCmd(opts),
		newStatusCmd(opts), newDoctorCmd(opts), newVersionCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the seamark version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), "seamark", version)
		},
	}
}

// Execute runs the CLI and returns a process exit code. Policy blocks get
// a distinct code (2) so PreToolUse hooks and CI can tell "denied" from
// "broken".
func Execute() int {
	if err := New().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "seamark:", err)

		if errors.Is(err, gate.ErrBlocked) {
			return 2
		}

		return 1
	}

	return 0
}
