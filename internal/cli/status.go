package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/status"
)

func newStatusCmd(opts *options) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report the index's semantic health: coverage, confidence, freshness",
		Long: `How much of the workspace the index actually covers, how confident
its call edges are, how old the history evidence is, and which
integrations are live (review mining, distillation agent, gate hook).

This is the context every safety-sensitive answer needs: "no effects
found" from a half-parsed index is not "no effects". Installation
problems belong to ` + "`seamark doctor`" + `; status assumes seamark runs
and asks how much its answers can be trusted.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, root, err := openIndex(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only

			s, err := status.Gather(st, root)
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")

				return enc.Encode(s)
			}

			status.Print(cmd.OutOrStdout(), s)

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}
