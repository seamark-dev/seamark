package integration

import (
	"github.com/seamark-dev/seamark/internal/skills"
)

// CodexID is the registry ID of Codex.
const CodexID = "codex"

// codexClient describes Codex. The setup adapter plans the gate and
// lesson hooks, the MCP registration, and the tool approvals. The edit
// and reset codecs translate the lesson hook events, and the command
// codec translates the gate hook event. The invocation preset arrives
// with its native evidence in a later slice; an undeclared capability
// reports as unsupported, never as verified.
func codexClient() Client {
	return Client{
		ID:        CodexID,
		Name:      "Codex",
		SkillDirs: []string{skills.AgentsDir},
		Setup:     codexSetup{},
		SetupOps:  SetupSupport{Hooks: true, GateHook: true, RegisterMCP: true, ApproveTools: true},
		Edits:     codexEdits{},
		Commands:  codexCommands{},
		Resets:    codexResets{},
	}
}
