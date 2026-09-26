package integration

import (
	"errors"
	"path/filepath"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/skills"
)

// CodexID is the registry ID of Codex.
const CodexID = "codex"

// codexClient registers Codex setup, hook codecs, and one-shot invocation.
func codexClient() Client {
	return Client{
		ID:         CodexID,
		Name:       "Codex",
		SkillDirs:  []string{skills.AgentsDir},
		Setup:      codexSetup{},
		SetupOps:   SetupSupport{Hooks: true, GateHook: true, RegisterMCP: true, ApproveTools: true},
		Edits:      codexEdits{},
		Commands:   codexCommands{},
		Resets:     codexResets{},
		Invocation: codexInvocation,
	}
}

// codexInvocation returns the Codex one-shot command for a workspace.
// Flags were checked against codex-cli 0.154.0 and 0.157.0; see the
// compatibility record.
//
//   - `--ephemeral` avoids persisting sessions.
//   - `--sandbox read-only` restricts model-generated commands.
//   - `-C <root>` and Dir keep Codex and its process in the same workspace.
//   - `--skip-git-repo-check` allows workspaces without .git.
//   - `--ignore-rules` blocks inherited execpolicy rules from bypassing the
//     sandbox. Without it, an allow rule permitted a workspace write on 0.157.0.
//   - `-c features.hooks=false` prevents recursive calls to Seamark hooks.
//   - `-` reads the prompt from stdin.
//
// User authentication, model, and provider settings still apply. Codex also
// loads AGENTS.md and project instructions. Stdout carries the final reply;
// stderr carries progress and errors after a banner, hence DiagnosticTail.
//
// root must be absolute so -C does not resolve a relative path again from Dir.
func codexInvocation(root string) (agent.CommandSpec, error) {
	if !filepath.IsAbs(root) {
		return agent.CommandSpec{}, errors.New("the workspace root must be an absolute path")
	}

	return agent.CommandSpec{
		Name: CodexID,
		Argv: []string{
			"codex", "exec", "--ephemeral", "--sandbox", "read-only", "-C", root,
			"--skip-git-repo-check", "--ignore-rules", "-c", "features.hooks=false", "-",
		},
		Dir:        root,
		Diagnostic: agent.DiagnosticTail,
	}, nil
}
