package integration

import (
	"github.com/seamark-dev/seamark/internal/skills"
)

// CodexID is the registry ID of Codex.
const CodexID = "codex"

// codexClient describes Codex. The setup adapter plans the lesson
// hooks, the MCP registration, and the tool approvals. The edit and
// reset codecs translate the lesson hook events. The command codec and
// the invocation preset arrive with their native evidence in later
// slices; an undeclared capability reports as unsupported, never as
// verified.
func codexClient() Client {
	return Client{
		ID:        CodexID,
		Name:      "Codex",
		SkillDirs: []string{skills.AgentsDir},
		Setup:     codexSetup{},
		SetupOps:  SetupSupport{Hooks: true, RegisterMCP: true, ApproveTools: true},
		Edits:     codexEdits{},
		Resets:    codexResets{},
	}
}
