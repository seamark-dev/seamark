package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the lessons settings: the mute list and the
// delivery keys of .seamark/lessons.yaml (P4-2). They prove the adapter
// rules: one lessons root, the foreign-root refusal, no index and no
// gate needed, and a save that changes lessons.yaml only.

// muteAdd is the edit that adds one mute entry.
func muteAdd(rule, region string) settings.Edit {
	return settings.Edit{Field: settings.Mutes, Mute: &settings.MuteEdit{Op: settings.MuteAdd, Rule: rule, Region: region}}
}

// lessonsPath is the absolute lessons.yaml of root.
func lessonsPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(pinfile.File))
}

// hardLink gives lessons.yaml under root a second name. It skips the
// test where the platform has no hard link or reports no link count,
// because integration.HardLinked then reports no link.
func hardLink(t *testing.T, root string) {
	t.Helper()

	if err := os.Link(lessonsPath(root), filepath.Join(root, "other.yaml")); err != nil {
		t.Skipf("no hard links here: %v", err)
	}

	if linked, _ := integration.HardLinked(root, pinfile.File); !linked {
		t.Skip("the platform reports no link count")
	}
}

// changedFiles returns the paths whose hash differs between two trees,
// new and removed paths included.
func changedFiles(before, after map[string]string) []string {
	var out []string

	for path, sum := range after {
		if before[path] != sum {
			out = append(out, path)
		}
	}

	for path := range before {
		if _, ok := after[path]; !ok {
			out = append(out, path)
		}
	}

	return out
}

func TestEngineLessonsSaveRefusesAnotherRoot(t *testing.T) {
	// P4-16 has no --db exception, so a --db database refuses as the
	// default database does. The Phase 3 rule ownRoot skips a --db
	// database, so this case catches its use in a lessons check.
	for name, dbFlag := range map[string]bool{"the default database": false, "a --db database": true} {
		t.Run("a root that turns foreign after the review, with "+name, func(t *testing.T) {
			ws, _ := servicestest.Workspace(t, "")
			servicestest.Write(t, ws, pinfile.File, maintPins)

			if dbFlag {
				data, err := os.ReadFile(ws.DBPath)
				require.NoError(t, err)

				custom := filepath.Join(t.TempDir(), "custom.db")
				require.NoError(t, os.WriteFile(custom, data, 0o644))

				ws, err = workspace.Resolve(ws.Root, custom)
				require.NoError(t, err)
				require.NotEqual(t, store.DefaultPath(ws.Root), ws.DBPath, "the copy is a --db database")
			}

			e := engNew(ws, Deps{})

			snap, err := e.Settings()
			require.NoError(t, err)
			require.NoError(t, snap.Editable(settings.GroupMutes))

			plan, err := e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
			require.NoError(t, err)
			require.Equal(t, pinfile.File, plan.File)

			other := t.TempDir()
			recordRoot(t, ws, other)

			before := servicestest.Tree(t, ws.Root)

			res, err := e.SaveSettings(plan)
			require.ErrorIs(t, err, ErrForeignRoot, "the foreign root comes before the stale root")
			assert.Equal(t, settings.NotWritten, res.Outcome)
			assert.False(t, res.Written)
			assert.Equal(t, snap.Lessons.Path, res.Path, "the result names lessons.yaml")
			assert.Equal(t, before, servicestest.Tree(t, ws.Root), "the refusal writes nothing")
			assert.NoFileExists(t, lessonsPath(other), "nothing is created under the recorded root")
			engUnlocked(t, e)

			// A new plan from the old snapshot refuses too: the root turned
			// foreign after the inspection.
			_, err = e.PlanSettings(snap, []settings.Edit{muteAdd("RUF002", "")})
			require.ErrorIs(t, err, ErrForeignRoot)
		})
	}

	// The foreign root replaces a block of the file itself, because a fix
	// by hand would change the file of another repository. The block is
	// no settings.BlockedError, so a screen routes it to the index build.
	for name, block := range map[string]func(t *testing.T, root string){
		"CRLF line breaks": func(t *testing.T, root string) {
			require.NoError(t, os.WriteFile(lessonsPath(root), []byte("threshold: 2\r\n"), 0o644))
		},
		"a hard link": hardLink,
	} {
		t.Run("a foreign root replaces the block of "+name, func(t *testing.T) {
			origin, _ := maintWorkspace(t, true)
			block(t, origin.Root)

			own, err := engNew(origin, Deps{}).Settings()
			require.NoError(t, err)
			require.ErrorIs(t, own.Editable(settings.GroupMutes), settings.ErrBlocked, "the origin sees the file block")

			dbOf, err := workspace.Resolve(t.TempDir(), origin.DBPath)
			require.NoError(t, err)

			e := engNew(dbOf, Deps{})

			snap, err := e.Settings()
			require.NoError(t, err)

			for _, g := range []settings.Group{settings.GroupMutes, settings.GroupDelivery} {
				err := snap.Editable(g)
				require.ErrorIs(t, err, ErrForeignRoot, "%s names the foreign root, not the file block", g)
				assert.NotErrorIs(t, err, settings.ErrBlocked, "%s does not ask for a fix by hand", g)
			}

			_, err = e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
			require.ErrorIs(t, err, ErrForeignRoot)
		})
	}

	for name, tc := range foreignCases(t) {
		t.Run(name, func(t *testing.T) {
			snap, err := tc.e.Settings()
			require.NoError(t, err)
			assert.Equal(t, lessonsPath(tc.other), snap.Lessons.Path, "the lessons part is read under R")

			for _, g := range []settings.Group{settings.GroupMutes, settings.GroupDelivery} {
				require.ErrorIs(t, snap.Editable(g), ErrForeignRoot, "%s names the foreign root before a draft", g)
			}

			assert.NoError(t, snap.Editable(settings.GroupPinWrite), "config.yaml keeps its root rules")

			_, err = tc.e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
			require.ErrorIs(t, err, ErrForeignRoot)

			_, err = tc.e.PlanSettings(snap, []settings.Edit{{Field: settings.Threshold, Int: 3}})
			require.ErrorIs(t, err, ErrForeignRoot)

			config, err := tc.e.PlanSettings(snap, []settings.Edit{{Field: settings.DistillWrite, Bool: true}})
			require.NoError(t, err, "a config.yaml plan is not a lessons plan")
			assert.Equal(t, settings.File, config.File)
		})
	}
}

func TestEngineLessonsSaveWorksWithoutAnIndexAndWithTheGateOff(t *testing.T) {
	t.Run("without an index", func(t *testing.T) {
		root := t.TempDir()
		servicestest.Git(t, root, "init", "-b", "main")

		ws, err := workspace.Resolve(root, "")
		require.NoError(t, err)

		e := engNew(ws, Deps{})

		snap, err := e.Settings()
		require.NoError(t, err)
		require.NoError(t, snap.Editable(settings.GroupMutes))
		require.NoError(t, snap.Editable(settings.GroupDelivery))

		plan, err := e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "api")})
		require.NoError(t, err)

		res, err := e.SaveSettings(plan)
		require.NoError(t, err)
		assert.Equal(t, settings.Written, res.Outcome)
		assert.True(t, res.Created, "the first save creates the file (P4-9)")
		assert.Equal(t, lessonsPath(ws.Root), res.Path)
		assert.NoFileExists(t, ws.DBPath, "a lessons save needs no index and creates none")
		engUnlocked(t, e)

		cfg, err := reviews.LoadConfig(ws.Root)
		require.NoError(t, err)
		require.Len(t, cfg.Mute, 1)
		assert.Equal(t, reviews.MuteRule{Rule: "RUF001", Region: "api"}, cfg.Mute[0])
	})

	t.Run("with the pin-write gate off", func(t *testing.T) {
		ws, _ := maintWorkspace(t, false)
		e := engNew(ws, Deps{})

		snap, err := e.Settings()
		require.NoError(t, err)

		write, ok := snap.Field(settings.DistillWrite)
		require.True(t, ok)
		require.Equal(t, "off", write.Effective)

		plan, err := e.PlanSettings(snap, []settings.Edit{{Field: settings.Threshold, Int: 3}})
		require.NoError(t, err)

		res, err := e.SaveSettings(plan)
		require.NoError(t, err)
		assert.Equal(t, settings.Written, res.Outcome)
		assert.Contains(t, maintFile(t, ws), "threshold: 3")
		assert.Contains(t, maintFile(t, ws), "keep-docs-current", "the pins stay")
		engUnlocked(t, e)
	})
}

func TestEngineLessonsSaveChangesOnlyLessonsYAML(t *testing.T) {
	ws, _ := maintWorkspace(t, true)
	agent := &servicestest.Agent{}
	e := engNew(ws, Deps{Invoke: agent})

	probe := workspace.Probe(ws)
	ledger, err := e.Ledger()
	require.NoError(t, err)

	evidence := e.Learning()
	require.NoError(t, evidence.CountsErr)

	for _, edits := range [][]settings.Edit{
		{muteAdd("RUF001", "")},
		{{Field: settings.Threshold, Int: 3}},
		{{Field: settings.HookDelivery, String: string(reviews.HookDeliveryOncePerContext)}},
	} {
		before := servicestest.Tree(t, ws.Root)

		snap, err := e.Settings()
		require.NoError(t, err)

		plan, err := e.PlanSettings(snap, edits)
		require.NoError(t, err)

		res, err := e.SaveSettings(plan)
		require.NoError(t, err)
		require.Equal(t, settings.Written, res.Outcome)
		engUnlocked(t, e)

		assert.Equal(t, []string{lessonsPath(ws.Root)}, changedFiles(before, servicestest.Tree(t, ws.Root)),
			"the save of %s changes lessons.yaml only", edits[0].Field)
	}

	assert.Zero(t, agent.Count(), "a lessons save calls no agent")
	assert.NoFileExists(t, reviews.AuditLogPath(ws.Root), "a lessons save records no firing")
	assert.NoFileExists(t, filepath.Join(ws.Root, ".seamark", "lessons-hook-state.json"), "nor delivery state")

	after := workspace.Probe(ws)
	assert.Equal(t, probe.State, after.State, "the save builds no index")
	assert.Equal(t, probe.IndexedAt, after.IndexedAt)

	again, err := e.Ledger()
	require.NoError(t, err)
	assert.Equal(t, ledger, again, "the proposal rows stay")

	counts := e.Learning()
	require.NoError(t, counts.CountsErr)
	assert.Equal(t, evidence.ReviewFindings, counts.ReviewFindings, "no finding is mined or removed")
	assert.Equal(t, evidence.Lessons, counts.Lessons)

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	findings, err := st.FindingsMetaByIDs([]int64{21, 22})
	require.NoError(t, err)
	assert.Len(t, findings, 2, "the findings of the workspace stay")
}

// TestEngineLessonsSaveRefusesAHardLinkOrCRLF drives the file refusals
// of P4-17 and P4-6 through SaveSettings. A hard link after the review
// refuses the save itself. SaveSettings has no CRLF check of its own:
// CRLF after the review changes the bytes, so the save refuses as stale.
// The next inspection then names the CRLF block, and the plan refuses.
func TestEngineLessonsSaveRefusesAHardLinkOrCRLF(t *testing.T) {
	t.Run("a hard link after the review", func(t *testing.T) {
		ws, _ := maintWorkspace(t, true)
		e := engNew(ws, Deps{})

		snap, err := e.Settings()
		require.NoError(t, err)

		plan, err := e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
		require.NoError(t, err)

		hardLink(t, ws.Root)

		res, err := e.SaveSettings(plan)
		require.ErrorIs(t, err, settings.ErrBlocked)
		assert.ErrorContains(t, err, integration.HardLinkReason)
		assert.Equal(t, settings.NotWritten, res.Outcome)
		assert.Equal(t, err, res.Err)
		assert.Equal(t, maintPins, maintFile(t, ws), "the refusal keeps the file and its link")
		engUnlocked(t, e)

		again, err := e.Settings()
		require.NoError(t, err)

		for _, g := range []settings.Group{settings.GroupMutes, settings.GroupDelivery} {
			assert.ErrorContains(t, again.Editable(g), integration.HardLinkReason, "%s", g)
		}
	})

	t.Run("CRLF line breaks after the review", func(t *testing.T) {
		ws, _ := maintWorkspace(t, true)
		e := engNew(ws, Deps{})

		snap, err := e.Settings()
		require.NoError(t, err)

		plan, err := e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
		require.NoError(t, err)

		crlf := strings.ReplaceAll(maintPins, "\n", "\r\n")
		servicestest.Write(t, ws, pinfile.File, crlf)

		res, err := e.SaveSettings(plan)
		require.ErrorIs(t, err, settings.ErrStale, "the engine sees other bytes than the review")
		assert.Equal(t, settings.NotWritten, res.Outcome)
		assert.Equal(t, err, res.Err)
		assert.Equal(t, crlf, maintFile(t, ws))
		engUnlocked(t, e)

		again, err := e.Settings()
		require.NoError(t, err)

		for _, g := range []settings.Group{settings.GroupMutes, settings.GroupDelivery} {
			err := again.Editable(g)
			require.ErrorIs(t, err, settings.ErrBlocked, "%s", g)
			assert.ErrorContains(t, err, "the file uses CRLF line breaks")
		}

		_, err = e.PlanSettings(again, []settings.Edit{muteAdd("RUF001", "")})
		require.ErrorIs(t, err, settings.ErrBlocked, "a CRLF file refuses the next plan")
		assert.Equal(t, crlf, maintFile(t, ws))
	})
}

func TestEngineLessonsSaveRefusalNamesLessonsYAML(t *testing.T) {
	ws, _ := servicestest.Workspace(t, "")
	e := engNew(ws, Deps{})

	snap, err := e.Settings()
	require.NoError(t, err)

	plan, err := e.PlanSettings(snap, []settings.Edit{muteAdd("RUF001", "")})
	require.NoError(t, err)

	e.mu.Lock()
	res, err := e.SaveSettings(plan)
	e.mu.Unlock()

	require.ErrorIs(t, err, ErrBusy)
	assert.Equal(t, settings.NotWritten, res.Outcome)
	assert.Equal(t, snap.Lessons.Path, res.Path, "a refused lessons save names lessons.yaml, not config.yaml")

	_, statErr := os.Stat(snap.Lessons.Path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a busy save writes nothing")
}

// TestEngineLessonsSettingsFollowTheFileThatPinsLists proves one lessons
// root (P4-16): the lessons part of Settings, the Pins read, and the
// extraction guard observe one file.
func TestEngineLessonsSettingsFollowTheFileThatPinsLists(t *testing.T) {
	ws, _ := maintWorkspace(t, true)
	e := engNew(ws, Deps{})

	snap, err := e.Settings()
	require.NoError(t, err)

	pins := e.Pins()
	require.NotNil(t, pins.Doc)
	assert.Equal(t, lessonsPath(pins.Index.Root), snap.Lessons.Path)
	assert.Equal(t, pins.Doc.Guard, snap.Lessons.Guard, "both editors observe one file")

	// Learning reads the same lessons.yaml for its guard.
	preview, err := e.PlanExtract(learning.ExtractRequest{})
	require.NoError(t, err)
	assert.Equal(t, preview.Lessons, snap.Lessons.Guard)
}
