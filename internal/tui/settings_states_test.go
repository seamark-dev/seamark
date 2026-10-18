package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests in this file cover the unknown, blocked and hostile states
// of the Settings destination, and the fit of its rows at every size.

// settingsListRow returns the list pane part of the frame line that
// shows field, or "". The list is the left box of the wide layout and
// the only box of the compact layout, so it is the first cell.
func settingsListRow(frame, field string) string {
	for _, l := range strings.Split(frame, "\n") {
		cells := strings.Split(l, "│")
		if len(cells) > 1 && strings.Contains(cells[1], " "+field+" ") {
			return cells[1]
		}
	}

	return ""
}

// settingsCell returns the box cell of the frame that holds part, or "".
// It keeps the text of a box beside it out of the check.
func settingsCell(frame, part string) string {
	for _, l := range strings.Split(frame, "\n") {
		for _, c := range strings.Split(l, "│") {
			if strings.Contains(c, part) {
				return c
			}
		}
	}

	return ""
}

func TestSettingsShadowedPresetIsSanitized(t *testing.T) {
	// A bidi override and a C1 CSI survive render.Sanitize, so only the
	// console sanitizer keeps them out of the terminal.
	svc := settingsFake(t, "agent:\n  cli: \"\\u202eevil\\u009b2Jx\\ttab\"\n  argv: [my-agent, \"-\"]\n")
	h := settingsOpen(t, svc, 120, 40)

	for _, keys := range [][]string{nil, {"enter"}, {"esc", "down"}} {
		h.press(keys...)

		raw := h.m.View()
		assert.NotContains(t, raw, "\u202e", "no bidi override from the file")
		assert.NotContains(t, raw, "\u009b", "no one-byte CSI from the file")
		assert.NotContains(t, raw, "\t", "no tab from the file")
	}

	assert.Contains(t, settingsListRow(h.view(), "agent.cli"), "evil", "the preset text still shows")
	assert.Empty(t, svc.called())
}

func TestSettingsPreviewSanitizesTheFilePath(t *testing.T) {
	// The directory name holds a title sequence and a bell.
	const hostile = "repo\x1b]0;owned\a"

	root := filepath.Join(t.TempDir(), hostile)
	require.NoError(t, os.MkdirAll(root, 0o755))

	svc := settingsFakeAt(t, root, settingsRich)
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "up", "space", "enter", "s")
	require.Contains(t, h.view(), "Save .seamark/config.yaml")

	raw := h.m.View()
	assert.NotContains(t, raw, "\x1b]0;", "no title sequence from the workspace path")
	assert.NotContains(t, raw, "\a", "no bell from the workspace path")
	settingsNoWrites(t, svc)
}

func TestSettingsUnreadableSectionIsUnknownInTheEditor(t *testing.T) {
	// The index reader rejects the quoted day count, so the whole index
	// section is unknown, although the file holds exclude patterns.
	svc := settingsFake(t, "reviews:\n  window_days: \"30\"\nindex:\n  generated: true\n  exclude:\n"+
		"    - \"*.pb.go\"\n    - internal/gen/\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
	flat := settingsBox(h.view(), "reviews.window_days")
	assert.NotContains(t, flat, "(●)", "no window choice is marked while the value is unknown")
	assert.Contains(t, flat, "No choice is marked, because the saved value is unknown.")

	h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
	assert.NotContains(t, settingsBox(h.view(), "index.generated"), "(●)", "a stored true is not the skip default")

	h.press("down")
	flat = settingsBox(h.view(), "index.exclude")
	assert.NotContains(t, flat, "PATTERNS 0", "an unknown list has no count")
	assert.NotContains(t, flat, "No patterns")
	assert.Contains(t, flat, "The patterns are unknown: the index reader rejects the file.")
	assert.Contains(t, flat, "Stored [*.pb.go, internal/gen/]", "the stored text still shows")

	// The agent reader rejects a list for agent.cli.
	svc = settingsFake(t, "agent:\n  cli: [x]\n  argv: [my-agent, \"-\"]\n")
	h = settingsOpen(t, svc, 120, 40)

	flat = settingsBox(h.view(), "agent.cli")
	assert.NotContains(t, flat, "Custom agent.argv is not set")
	assert.Contains(t, flat, "Seamark cannot tell whether a custom agent.argv overrides this preset")

	h.press("down")
	flat = settingsBox(h.view(), "agent.argv")
	assert.NotContains(t, flat, "No custom command is set")
	assert.Contains(t, flat, "Seamark cannot tell whether a custom command is set")

	// The distill reader rejects a list for distill.write.
	svc = settingsFake(t, "distill:\n  write: [true]\n")
	h = settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	flat = settingsBox(h.view(), "distill.write")
	assert.NotContains(t, flat, "(●)", "an unknown gate is not off")
	assert.Contains(t, flat, "No choice is marked")
	assert.Empty(t, svc.called())
}

func TestSettingsBlockedKeysAreUnknownNotDefault(t *testing.T) {
	// A file that the settings package cannot parse hides every key.
	svc := settingsFake(t, "agent:\n  cli: codex\nagent:\n  cli: claude\n")
	h := settingsOpen(t, svc, 120, 40)

	flat := settingsBox(h.view(), "agent.cli")
	assert.Contains(t, flat, "Stored unknown")
	assert.NotContains(t, flat, "Stored not set")
	assert.NotContains(t, flat, "no key in the file")
	assert.NotContains(t, flat, "built-in default")

	// A section behind an alias hides its keys from the settings package,
	// but the reader still resolves the value.
	svc = settingsFake(t, "base: &b\n  cli: codex\nagent: *b\n")
	h = settingsOpen(t, svc, 120, 40)

	frame := h.view()
	row := settingsListRow(frame, "agent.cli")
	assert.Contains(t, row, "codex")
	assert.Contains(t, row, "! not editable")
	assert.NotContains(t, row, "default", "the reader value does not come from the default")

	flat = settingsBox(frame, "agent.cli")
	assert.Contains(t, flat, "Effective codex", "the detail still shows the effective value")
	assert.Contains(t, flat, "Stored unknown")
	assert.Contains(t, flat, "Source unknown while the group is not editable")
	assert.NotContains(t, flat, "built-in default")

	// The readers decode the first document of a multi-document file.
	svc = settingsFake(t, "agent:\n  cli: codex\nreviews:\n  window_days: 30\n---\nfoo: 1\n")
	h = settingsOpen(t, svc, 120, 40)

	row = settingsListRow(h.view(), "reviews.window_days")
	assert.Contains(t, row, "30 days")
	assert.Contains(t, row, "! not editable")
	assert.NotContains(t, row, "default")
	assert.Empty(t, svc.called())
}

func TestSettingsBlockedReloadKeepsTheDraft(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: true\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("d")
	require.Equal(t, 1, settingsScr(h).draftTotal())

	// Another editor breaks the file. The blocked snapshot hides the key,
	// so it cannot show that the reset changes nothing.
	settingsWrite(t, svc.root, "distill:\n  write: true\ndistill:\n  write: true\n")
	h.press("r")

	assert.Equal(t, 1, settingsScr(h).draftTotal(), "a blocked reload keeps the draft")
	assert.NotEmpty(t, settingsLine(h.view(), "distill.write", "● draft"))

	// s cannot review the draft, so the tail offers no key (spec R-15).
	flat := settingsBox(h.view(), "Settings")
	assert.Contains(t, flat, "1 unsaved change in Pin writing · fix the file first")
	assert.NotContains(t, flat, "s reviews and saves")
	settingsNoWrites(t, svc)
}

func TestSettingsDraftHintFollowsTheSavableGroups(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
	h.press("enter", "down", "space", "enter")
	require.Equal(t, 1, settingsScr(h).draftTotal())

	// The index reader now rejects the file, so the Indexing group cannot
	// be saved. The cursor goes to another group, whose tail has no note.
	settingsWrite(t, svc.root, "distill:\n  write: false\nreviews:\n  window_days: \"30\"\n")
	h.press("r")
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})

	flat := settingsBox(h.view(), "Settings")
	assert.Contains(t, flat, "1 unsaved change in Indexing · fix the file first")
	assert.NotContains(t, flat, "s reviews")

	// A second draft in a group that can be saved brings the key back.
	h.press("enter", "down", "space", "enter")

	flat = settingsBox(h.view(), "Settings")
	// The hint wraps in the list pane, so the flat text has no separator.
	assert.Contains(t, flat, "2 unsaved changes in 2 groups s reviews the group under the cursor")
	assert.NotContains(t, flat, "fix the file first")

	// A busy slot disables s for every group; its reason is the line above.
	require.True(t, h.m.st.begin("Refreshing evidence"))

	flat = settingsBox(h.view(), "Settings")
	assert.Contains(t, flat, "Saving waits for the running operation: Refreshing evidence. 2 unsaved changes in 2 groups")
	assert.NotContains(t, flat, "s reviews")
	assert.NotContains(t, flat, "fix the file first")
	settingsNoWrites(t, svc)
}

func TestSettingsChoiceRowsKeepTheDefaultTag(t *testing.T) {
	svc := settingsFake(t, "agent:\n  argv: [my-agent, \"-\"]\n")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h := settingsOpen(t, svc, size[0], size[1])

		h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
		h.press("enter")

		skip := settingsCell(h.view(), "(●) skip")
		assert.Contains(t, skip, "default", "size %v: the default tag survives", size)
		assert.NotContains(t, skip, "…", "size %v", size)

		h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
		h.press("enter")

		// The file has no window key, so the default choice is marked.
		def := settingsCell(h.view(), "(●) default")
		require.NotEmpty(t, def, "size %v", size)
		assert.Equal(t, 1, strings.Count(def, "default"), "size %v: the default choice has no second tag", size)

		h.send(navigateMsg{dest: destSettings, intent: "group:inference"})
		h.press("down", "enter")

		remove := settingsCell(h.view(), "( ) remove")
		assert.Contains(t, remove, "agent.cli takes effect", "size %v: the effect of the choice shows", size)
		assert.NotContains(t, remove, "…", "size %v", size)
	}
}

func TestSettingsMinimumTailMarksCutText(t *testing.T) {
	// The blocked file keeps the file location in the one tail row.
	svc := settingsFake(t, "agent:\n  cli: codex\nagent:\n  cli: claude\n")
	h := settingsOpen(t, svc, 60, 20)

	frame := h.view()
	assertFrame(t, frame, 60, 20)
	assert.NotEmpty(t, settingsCell(frame, ".seamark/config.yaml:3"), "the location to open survives")

	// A busy slot cuts its reason with an ellipsis.
	svc = settingsFake(t, "distill:\n  write: false\n")
	h = settingsOpen(t, svc, 60, 20)
	require.True(t, h.m.st.begin("Distilling proposals"))

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	busy := settingsCell(h.view(), "Saving waits")
	require.NotEmpty(t, busy)
	assert.Contains(t, busy, "…", "cut text ends with an ellipsis")

	// The note of a save cuts with an ellipsis too.
	h.send(mutationDoneMsg{})
	h.press("s", "y")
	require.Equal(t, 1, svc.callCount("SaveSettings"))

	saved := settingsCell(h.view(), "Saved Pin writing")
	require.NotEmpty(t, saved)
	assert.Contains(t, saved, "…")
}

func TestSettingsValueLabelsKeepTwoCells(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
	assert.NotEmpty(t, settingsLine(h.view(), "Next refresh  730 days, the saved value"),
		"the widest label keeps a gap of two cells")
}

func TestSettingsBlockedTailNamesTheLocationOnce(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: codex\nagent:\n  cli: claude\n")
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	flat := settingsBox(frame, "Settings")
	assert.Contains(t, flat, `The file cannot be edited here: duplicate key "agent" (first at line 1).`)
	assert.Contains(t, flat, "› .seamark/config.yaml:3", "the location is a file:line route")
	assert.Equal(t, 1, strings.Count(flat, "by hand"), "the manual route shows once")
	assert.NotContains(t, flat, "(line 3)")

	flat = settingsBox(frame, "agent.cli")
	assert.Equal(t, 1, strings.Count(flat, "by hand"), "the editor names the manual route once")
	assert.NotContains(t, flat, "(line 3)")
}

func TestSettingsBlockedEditorOffersNoEditKeys(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: codex\nagent:\n  cli: claude\n")
	h := settingsOpen(t, svc, 80, 24)

	h.press("enter")

	foot := footerLine(h.view())
	assert.NotContains(t, foot, "space select", "a blocked radio editor selects nothing")
	assert.Contains(t, foot, "↑↓ move")

	h.press("esc")
	h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
	h.press("down", "enter")

	foot = footerLine(h.view())
	for _, k := range []string{"a add", "x remove", "K/J reorder", "enter edit"} {
		assert.NotContains(t, foot, k, "a blocked list editor offers no %s", k)
	}

	assert.NotContains(t, settingsBox(h.view(), "index.exclude"), "add a pattern")

	// d stays out of the list footer when the group cannot be edited,
	// even when the file holds the key.
	svc = settingsFake(t, "agent:\n  cli: &c codex\n")
	h = settingsOpen(t, svc, 120, 40)
	assert.NotContains(t, footerLine(h.view()), "d default")
	assert.Empty(t, svc.called())
}
