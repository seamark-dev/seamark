package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/mcp"
	"github.com/seamark-dev/seamark/internal/store"
)

func newMCPCmd(opts *options) *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Serve the index over MCP (stdio) for agent clients",
		Long: `Speaks the Model Context Protocol on stdin/stdout. Exposes five tools
(orient, why, change_set, check, expand), an orientation resource, and an
onboarding prompt. Every tool call self-repairs index freshness first.

Register in Claude Code via .mcp.json:
  {"mcpServers": {"seamark": {"command": "seamark", "args": ["mcp"]}}}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := index.ResolveRoot(opts.workspace)
			if err != nil {
				return err
			}

			dbPath := opts.dbPath
			if dbPath == "" {
				dbPath = store.DefaultPath(root)
			}

			// Logs go to stderr: stdout carries only protocol frames.
			logf := func(format string, a ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
			}

			return mcp.New(root, dbPath, version, logf).Serve(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
