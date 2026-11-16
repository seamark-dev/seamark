package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
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
			// The workspace comes back with the index, so the root line
			// below starts no second git process.
			st, probe, ws, err := openIndexWorkspace(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only

			// The report reads the root that the index records, so the
			// workspace line compares the index with that root. The
			// lesson-check report identifies each hook binary against this
			// binary and its version.
			s, err := status.GatherAs(st, probe.Root, integration.SelfIdentity(version))
			if err != nil {
				return err
			}

			// The equal case starts no git process. Status runs often, so
			// that cost counts. The MCP status resource reads the same root
			// and the same facts, so for one workspace and database both
			// print the same root line. They print the same workspace line
			// too, unless the MCP refresh skips because the index matches
			// its workspace: that resource then reads current.
			if !store.SameDirectory(probe.Root, ws.Root) {
				s.NoteOwnership(ownership.Gather(ws.Root, ws.AbsoluteDB()))
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
