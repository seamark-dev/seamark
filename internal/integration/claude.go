package integration

import (
	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/skills"
)

// ClaudeID is the registry ID of Claude Code.
const ClaudeID = "claude"

// claudeClient describes Claude Code. The lifecycle codecs and the
// setup adapter join the descriptor in later slices; the invocation
// capability reuses the existing one-shot preset so the compatibility
// resolver and the registry cannot disagree about the command.
func claudeClient() Client {
	return Client{
		ID:        ClaudeID,
		Name:      "Claude Code",
		SkillDirs: []string{skills.ClaudeDir},
		Invocation: func(string) (agent.CommandSpec, error) {
			// Dir stays empty: the legacy preset inherits the caller's
			// working directory, and this slice preserves that.
			return agent.ClaudeCommand(), nil
		},
	}
}
