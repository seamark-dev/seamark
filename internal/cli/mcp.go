package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/mcp"
	"github.com/seamark-dev/seamark/internal/workspace"
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
			// Locate does not test the directory, so the server starts
			// for any -C. Each tool call builds or refreshes the index.
			ws, err := workspace.Locate(opts.workspace, opts.dbPath)
			if err != nil {
				return err
			}

			// Logs go to stderr: stdout carries only protocol frames.
			logf := func(format string, a ...any) {
				fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
			}

			return mcp.New(ws.Root, ws.DBPath, version, logf).Serve(cmd.InOrStdin(), cmd.OutOrStdout())
		},
	}
}
