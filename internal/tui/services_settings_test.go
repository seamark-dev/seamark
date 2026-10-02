package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// engConfig reads the config.yaml of ws.
func engConfig(t *testing.T, ws workspace.Workspace) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(ws.Root, filepath.FromSlash(settings.File)))
	require.NoError(t, err)

	return string(data)
}

func TestEnginePlansAndSavesOneSettingsGroup(t *testing.T) {
	ws, _ := engWorkspace(t, "")
	e := engNew(ws, engineDeps{})
	before := engConfig(t, ws)

	snap, err := e.Settings()
	require.NoError(t, err)

	write, ok := snap.Field(settings.DistillWrite)
	require.True(t, ok)
	assert.Equal(t, "off", write.Effective)

	plan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
	require.NoError(t, err)
	assert.Equal(t, settings.GroupPinWrite, plan.Group)
	assert.Equal(t, before, engConfig(t, ws), "a plan writes nothing")

	res, err := e.SaveSettings(plan)
	require.NoError(t, err)
	assert.True(t, res.Written)
	assert.Contains(t, engConfig(t, ws), "write: true")
	assert.Contains(t, engConfig(t, ws), "argv:", "the save keeps the other sections")
	engUnlocked(t, e)

	again, err := e.Settings()
	require.NoError(t, err)

	write, _ = again.Field(settings.DistillWrite)
	assert.Equal(t, "on", write.Effective)

	// The saved plan binds the old bytes, so a second save is stale.
	res, err = e.SaveSettings(plan)
	require.ErrorIs(t, err, settings.ErrStale)
	assert.False(t, res.Written)
	engUnlocked(t, e)
}

func TestEngineSaveSettingsRefusesWithoutWrite(t *testing.T) {
	ws, _ := engWorkspace(t, "")
	e := engNew(ws, engineDeps{})
	before := engConfig(t, ws)

	snap, err := e.Settings()
	require.NoError(t, err)

	plan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.ReviewWindow, Days: 30}})
	require.NoError(t, err)

	e.mu.Lock()
	res, err := e.SaveSettings(plan)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	assert.False(t, res.Written)
	assert.Equal(t, snap.Path, res.Path)
	assert.Equal(t, before, engConfig(t, ws), "a busy save writes nothing")

	// A plan of another root never writes here: the file that the next
	// run reads is under the root of this engine.
	other := t.TempDir()
	otherWS, err := workspace.Resolve(other, "")
	require.NoError(t, err)

	foreignSnap, err := engNew(otherWS, engineDeps{}).Settings()
	require.NoError(t, err)

	foreign, err := e.PlanSettings(foreignSnap, []settings.Edit{{Field: settings.ReviewWindow, Days: 30}})
	require.NoError(t, err)

	res, err = e.SaveSettings(foreign)
	require.ErrorIs(t, err, settings.ErrStale)
	assert.False(t, res.Written)
	assert.NoFileExists(t, filepath.Join(other, filepath.FromSlash(settings.File)), "nothing is created")
	assert.Equal(t, before, engConfig(t, ws))
	engUnlocked(t, e)
}

// TestEngineSettingsUseTheRootThatTheIndexRecords proves the root
// choice: a console started in another directory with the same database
// edits the config.yaml that the runs of that index read.
func TestEngineSettingsUseTheRootThatTheIndexRecords(t *testing.T) {
	ws, _ := engWorkspace(t, "")
	elsewhere := workspace.Workspace{Root: t.TempDir(), DBPath: ws.DBPath}

	snap, err := engNew(elsewhere, engineDeps{}).Settings()
	require.NoError(t, err)

	assert.Equal(t, workspace.Probe(ws).Root, snap.Root)
	assert.NotEqual(t, elsewhere.Root, snap.Root)

	// Without an index the workspace root applies.
	fresh := workspace.Workspace{Root: t.TempDir(), DBPath: filepath.Join(t.TempDir(), "index.db")}

	snap, err = engNew(fresh, engineDeps{}).Settings()
	require.NoError(t, err)
	assert.Equal(t, fresh.Root, snap.Root)
}
