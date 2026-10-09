package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

func newWhyCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "why <symbol|file>",
		Short: "Explain a symbol or file: definition, callers, co-change, decisions",
		Long: `Answers, from the index: where is this defined, who calls it, which
files empirically change together with it, and which commits explain it.
Accepts a symbol name ("Store.Rebuild"), an FQN ("internal/store.Store.Rebuild"),
or a repo-relative file path.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, root, err := openIndex(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			staleNote(cmd.ErrOrStderr(), st, root)

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
			st, root, err := openIndex(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			staleNote(cmd.ErrOrStderr(), st, root)

			return report.Orient(cmd.OutOrStdout(), st, root)
		},
	}
}

// staleNote warns when the workspace changed since the index was built —
// answers from a stale graph should never look authoritative.
func staleNote(w io.Writer, st *store.Store, root string) {
	indexed, err := st.GetMeta("indexed_state")
	if err != nil || indexed == "" {
		return
	}

	if current := index.WorkspaceState(root); current != "" && current != indexed {
		fmt.Fprintln(w, "note: the workspace changed since the last index — run `seamark index` to refresh")
	}
}
