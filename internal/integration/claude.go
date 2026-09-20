package integration

import (
	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/skills"
)

// ClaudeID is the registry ID of Claude Code.
const ClaudeID = "claude"

// claudeClient describes Claude Code. The setup adapter plans the
// hooks, the allow rules, and the MCP registration. The edit and reset
// codecs translate the lessons hook events; the command codec joins the
// descriptor with the gate slice. The invocation capability reuses the
// existing one-shot preset so the compatibility resolver and the
// registry cannot disagree about the command.
func claudeClient() Client {
	return Client{
		ID:        ClaudeID,
		Name:      "Claude Code",
		SkillDirs: []string{skills.ClaudeDir},
		Setup:     claudeSetup{},
		SetupOps:  SetupSupport{Hooks: true, GateHook: true, RegisterMCP: true, ApproveTools: true},
		Edits:     claudeEdits{},
		Resets:    claudeResets{},
		Invocation: func(string) (agent.CommandSpec, error) {
			// Dir stays empty: the legacy preset inherits the caller's
			// working directory, and this slice preserves that.
			return agent.ClaudeCommand(), nil
		},
	}
}
