package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/lsp"
)

func newLSPCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "lsp",
		Short: "Run the Language Server Protocol server over stdio",
		Long: `Serves hover (decision layer), code lenses (caller counts, empirical
coupling), and co-change omission diagnostics to any LSP client. Run it as
a secondary server alongside your language's own; stdout carries the
protocol, all logging goes to stderr. Builds the index on first start.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return lsp.Serve(lsp.Options{
				Root:    opts.workspace,
				DBPath:  opts.dbPath,
				Version: version,
				Logf: func(format string, a ...any) {
					fmt.Fprintf(cmd.ErrOrStderr(), "seamark lsp: "+format+"\n", a...)
				},
			}, cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
