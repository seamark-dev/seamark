package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

func newWhyCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "why <symbol|file>",
		Short: "Explain a symbol or file: definition, callers, co-change, decisions",
		Long: `Answers, from the index: where is this defined, who calls it, which
files empirically change together with it, and which commits explain it.
Accepts a symbol name ("Store.Rebuild"), an FQN ("internal/store.Store.Rebuild"),
or a repo-relative file path.

In a partner line "N/M commits  lift L", N commits changed both files.
M is the number of commits that co-change counts in the mined window.
M is the same on every line; it is not the commit count of either file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, probe, ws, err := openIndexWorkspace(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			root := probe.Root

			staleNote(cmd.ErrOrStderr(), st, root, ws)

			return report.Why(cmd.OutOrStdout(), st, root, args[0])
		},
	}
}

func newOrientCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "orient",
		Short: "One-screen repo overview: modules, hot paths, decisions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, probe, ws, err := openIndexWorkspace(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			root := probe.Root

			staleNote(cmd.ErrOrStderr(), st, root, ws)

			return report.OrientCLI(cmd.OutOrStdout(), st, root)
		},
	}
}

// staleNote warns when the workspace changed since the index was built —
// answers from a stale graph should never look authoritative. root is the
// root that the index records, and ws is the workspace of the command.
func staleNote(w io.Writer, st *store.Store, root string, ws workspace.Workspace) {
	indexed, err := st.GetMeta(store.MetaIndexedState)
	if err != nil || indexed == "" {
		return
	}

	current := index.WorkspaceState(root, st.Path())
	if current == "" || current == indexed {
		return
	}

	route := "run `seamark index` to refresh"

	// Only an index of another root can refuse a build here, and a plain
	// index run would then fail. So the note names the route of that
	// state. The equal case reads no git.
	if !store.SameDirectory(root, ws.Root) {
		if refused := ownership.GatherCommand(ws.Root, ws.AbsoluteDB()).RefusedBuildRoute(); refused != "" {
			route = refused
		}
	}

	fmt.Fprintln(w, "note: the workspace changed since the last index — "+route)
}
