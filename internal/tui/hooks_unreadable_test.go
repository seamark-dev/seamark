package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

func TestUnreadableHooksNameTheWordOnce(t *testing.T) {
	// The detail of an unreadable entry already starts with the word, so
	// the setup row and the client brief print it once, as doctor does.
	insp := ovClaude()

	for i := range insp.Capabilities {
		switch insp.Capabilities[i].Capability {
		case integration.CapabilityEdits, integration.CapabilityCommands, integration.CapabilityResets:
			insp.Capabilities[i].State = integration.StateUnreadable
			insp.Capabilities[i].Detail = "unreadable (.claude/settings.json: boom)"
		}
	}

	want := "hooks unreadable (.claude/settings.json: boom)"

	var claude integration.Client

	for _, c := range integration.Builtin().Clients() {
		if c.ID == integration.ClaudeID {
			claude = c
		}
	}

	require.Equal(t, integration.ClaudeID, claude.ID)

	hooks := ""

	for _, row := range setupRows(claude, insp) {
		if row.intent == setupHooks {
			hooks = row.describe
		}
	}

	assert.Equal(t, want, hooks)

	brief := ovClientBrief(insp, " · ")
	assert.Contains(t, brief, want)
	assert.NotContains(t, brief, "unreadable (unreadable")
}
