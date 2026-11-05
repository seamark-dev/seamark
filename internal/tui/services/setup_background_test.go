package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
)

func TestSetupPreviewNamesTheRemovedBackgroundKey(t *testing.T) {
	// The setup wizard runs no narrator, so the removal of a background
	// key must reach the user through the findings of the plan. The
	// wizard asks no gate mode, so the run keeps the installed one.
	ws := archiveWorkspace(t)
	require.NoError(t, os.MkdirAll(filepath.Join(ws.Root, ".claude"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ws.Root, ".claude", "settings.json"), []byte(
		`{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[`+
			`{"type":"command","command":"/usr/local/bin/seamark gate --enforce --hook","async":true}]}]}}`), 0o644))

	preview, err := New(ws, "test", "/usr/local/bin/seamark").PlanSetup([]integration.ClientSetup{
		{ClientID: integration.ClaudeID, Hooks: true, CheckHookSources: true},
	})
	require.NoError(t, err)

	var removed []integration.Finding

	for _, f := range preview.Plan.Findings {
		if strings.Contains(f.Reason, "setup removed `async: true` from the seamark gate hook") {
			removed = append(removed, f)
		}
	}

	require.Len(t, removed, 1)
	assert.Equal(t, integration.FindingWarning, removed[0].Level)
	assert.Equal(t, "enforce", preview.GateMode, "the run keeps the installed mode")
}
