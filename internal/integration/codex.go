package integration

import (
	"github.com/seamark-dev/seamark/internal/skills"
)

// CodexID is the registry ID of Codex.
const CodexID = "codex"

// codexClient describes Codex. Only identity and the shared skill
// directory are declared here. Setup, lifecycle codecs, and the
// invocation preset arrive with their native evidence in later slices;
// an undeclared capability reports as unsupported, never as verified.
func codexClient() Client {
	return Client{
		ID:        CodexID,
		Name:      "Codex",
		SkillDirs: []string{skills.AgentsDir},
	}
}
