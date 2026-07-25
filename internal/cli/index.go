package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/index"
)

func newIndexCmd(opts *options) *cobra.Command {
	histOpts := history.Options{}
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the workspace index",
		Long: `Parses the workspace into a symbol/edge graph and mines git history
for co-change coupling and decisions. The index is a single SQLite file
under .seamark/; re-running replaces derived data atomically.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			sum, err := index.Run(index.Options{
				Root:    opts.workspace,
				DBPath:  opts.dbPath,
				History: histOpts,
				Logf: func(format string, a ...any) {
					fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
				},
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "indexed %s in %s\n", sum.Root, sum.Duration.Round(1e6))
			fmt.Fprintf(out, "  files    %d seen, %d parsed", sum.FilesSeen, sum.FilesParsed)

			if sum.ParseErrors > 0 {
				fmt.Fprintf(out, " (%d errors)", sum.ParseErrors)
			}

			fmt.Fprintln(out)
			fmt.Fprintf(out, "  symbols  %d\n", sum.Stats.Symbols)
			fmt.Fprintf(out, "  edges    %d\n", sum.Stats.Edges)

			if sum.HistoryMined {
				fmt.Fprintf(out, "  history  %d decisions, %d co-change pairs\n",
					sum.Stats.Decisions, sum.Stats.CoChanges)
			} else {
				fmt.Fprintf(out, "  history  skipped (%s)\n", sum.HistorySkipNote)
			}

			fmt.Fprintf(out, "  index    %s\n", sum.DBPath)
			return nil
		},
	}

	cmd.Flags().IntVar(&histOpts.MaxCommits, "max-commits", 0,
		"git history window for mining (default 5000)")
	cmd.Flags().IntVar(&histOpts.MaxFilesPerCommit, "max-files-per-commit", 0,
		"commits touching more files than this are excluded from co-change (default 30)")

	return cmd
}
