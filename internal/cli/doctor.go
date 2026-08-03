package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/doctor"
)

func newDoctorCmd(opts *options) *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose the installation: binary, git, index, policy, hooks, integrations",
		Long: `Runs read-only checks over everything seamark needs to function —
git, the index database (schema and SQLite integrity), policy and
effect-catalogue compilation, Claude Code hook wiring, the distillation
agent, gh, and MCP registration — and prints an exact corrective action
for anything broken. Nothing is changed, and nothing touches the
network. Exit code 1 when any check fails.

Semantic health — coverage, edge confidence, freshness — is
` + "`seamark status`" + `'s job; doctor asks whether seamark can run at all.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath, root, err := resolveDBPath(opts)
			if err != nil {
				return err
			}

			report := doctor.Run(root, dbPath, version)

			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")

				if err := enc.Encode(report); err != nil {
					return err
				}
			} else {
				doctor.Print(cmd.OutOrStdout(), report)
			}

			if report.Fails > 0 {
				return fmt.Errorf("doctor: %d check(s) failed", report.Fails)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "machine-readable output")

	return cmd
}
