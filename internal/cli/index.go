package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/index"
)

func newIndexCmd(opts *options) *cobra.Command {
	histOpts := history.Options{}

	var (
		force   bool
		reviews bool
	)
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Build or refresh the workspace index",
		Long: `Parses the workspace into a symbol/edge graph and mines git history
for co-change coupling and decisions. The index is a single SQLite file
under .seamark/; re-running replaces derived data atomically.

With --reviews, also mines pull-request review comments (CodeRabbit,
Copilot, humans) from GitHub into recurring "lessons" — needs the GitHub
CLI (gh) authenticated and a github.com remote.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			logf := func(format string, a ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
			}

			sum, err := index.Run(index.Options{
				Root:    opts.workspace,
				DBPath:  opts.dbPath,
				History: histOpts,
				Force:   force,
				Logf:    logf,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			// Review mining runs on the review cadence, not the structural
			// one, so it happens even when the structure is unchanged.
			mineReviews := func() error {
				if !reviews {
					return nil
				}

				res, err := index.RefreshReviews(sum.Root, opts.dbPath, logf)
				if err != nil {
					return err
				}

				switch {
				case len(res.Lessons) > 0:
					fmt.Fprintf(out, "  reviews  %d lessons mined from %d findings\n",
						len(res.Lessons), len(res.Findings))
				case res.Fetched:
					fmt.Fprintf(out, "  reviews  none (%s)\n", res.Note)
				default:
					// The fetch failed; existing lessons were left intact.
					fmt.Fprintf(out, "  reviews  skipped: %s (kept existing)\n", res.Note)
				}

				return nil
			}

			if sum.Skipped {
				fmt.Fprintf(out, "index already up to date (%s); --force rebuilds\n", sum.DBPath)
				return mineReviews()
			}

			fmt.Fprintf(out, "indexed %s in %s\n", sum.Root, sum.Duration.Round(1e6))
			fmt.Fprintf(out, "  files    %d seen, %d parsed", sum.FilesSeen, sum.FilesParsed)

			if sum.ParseErrors > 0 {
				fmt.Fprintf(out, " (%d errors)", sum.ParseErrors)
			}

			if cached := sum.FilesParsed - sum.FilesReparsed; cached > 0 {
				fmt.Fprintf(out, " — %d reparsed, %d from cache", sum.FilesReparsed, cached)
			}

			if sum.FilesSkipped > 0 {
				fmt.Fprintf(out, "; %d skipped (generated/excluded)", sum.FilesSkipped)
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

			if sum.Stats.Tagged > 0 {
				fmt.Fprintf(out, "  effects  %d symbols can reach a sink\n", sum.Stats.Tagged)
			}

			if err := mineReviews(); err != nil {
				return err
			}

			fmt.Fprintf(out, "  index    %s\n", sum.DBPath)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false,
		"rebuild even when the workspace is unchanged since the last index")
	cmd.Flags().BoolVar(&reviews, "reviews", false,
		"also mine PR review comments into lessons (needs gh + a GitHub remote)")
	cmd.Flags().IntVar(&histOpts.MaxCommits, "max-commits", 0,
		"git history window for mining (default 5000)")
	cmd.Flags().IntVar(&histOpts.MaxFilesPerCommit, "max-files-per-commit", 0,
		"commits touching more files than this are excluded from co-change (default 30)")

	return cmd
}
