package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/settings"
)

func TestSettingsSaveNeedsYFromThePreview(t *testing.T) {
	const before = "# Seamark workspace configuration\nindex:\n  generated: false\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)
	probe := &settingsProbe{}
	h.m.screens[destDoctor] = probe

	// Draft codex: enter opens the editor, space selects, enter closes.
	h.press("enter", "down", "space", "enter")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "codex", "● draft"))
	assert.Contains(t, frame, "1 unsaved change in Inference · s reviews and saves")
	assert.Equal(t, before, settingsBytes(t, svc), "a draft writes nothing")
	assert.Empty(t, svc.called(), "a draft plans nothing")

	// s plans the patch and opens the diff preview.
	h.press("s")
	require.Equal(t, 1, svc.callCount("PlanSettings"))

	frame = h.view()
	flat := settingsBox(frame, "Save")

	assert.Contains(t, frame, "Save .seamark/config.yaml")
	assert.Contains(t, frame, "modified ─╮", "the border names the file state")
	// A long temporary path wraps inside the modal, so the check ignores
	// the spaces of the wrap.
	assert.Contains(t, strings.ReplaceAll(flat, " ", ""),
		"File"+displayPath(filepath.Join(svc.root, ".seamark", "config.yaml")), "the preview names the absolute file")

	for _, want := range []string{
		"Seamark writes 1 change to .seamark/config.yaml.",
		"Nothing is written before you press y.", "CHANGE", "agent.cli not set (default claude) › codex",
		"EFFECTIVE", "agent.cli claude › codex", "WILL", "+ set agent.cli: codex", "WON'T",
		"run a model, log in, or change the Claude Code or Codex files",
		"start an index build, an evidence refresh or a distillation", "DIFF",
	} {
		assert.Contains(t, flat, want)
	}

	assert.Contains(t, footerLine(frame), "y save")
	assert.Contains(t, footerLine(frame), "esc keep editing")
	assert.Contains(t, footerLine(frame), "ctrl+c quit", "the preview captures the keys")

	// The shell does not act on keys that the preview ignores.
	h.press("enter", "5", "q")
	require.False(t, h.quit)
	assert.Zero(t, svc.callCount("SaveSettings"), "enter never saves")

	// esc keeps the draft and writes nothing.
	h.press("esc")
	assert.NotContains(t, h.view(), "Save .seamark/config.yaml")
	assert.NotEmpty(t, settingsLine(h.view(), "agent.cli", "codex", "● draft"))
	assert.Equal(t, before, settingsBytes(t, svc))
	settingsNoWrites(t, svc)

	// y from a fresh preview saves exactly the planned bytes.
	// The shell renders after every key, and the render records the
	// scroll bounds that end uses.
	h.press("s")
	h.view()
	h.press("end")
	assert.Contains(t, settingsBox(h.view(), "Save"), "The index reads as stale until the next index build",
		"the preview says that every save marks the index stale")

	reads := svc.readCount("Settings")
	h.press("y")

	require.Equal(t, 1, svc.callCount("SaveSettings"))
	assert.Equal(t, before+"\nagent:\n  cli: codex\n", settingsBytes(t, svc), "a new section follows one blank line")
	assert.False(t, h.m.st.busy(), "the save frees the mutation slot")
	assert.Greater(t, svc.readCount("Settings"), reads, "the save reloads the settings")

	require.Len(t, probe.saved, 1, "every screen learns about the save")
	assert.Equal(t, settings.GroupInference, probe.saved[0].group)
	assert.False(t, probe.saved[0].at.IsZero())

	frame = h.view()
	assert.Contains(t, frame, "Configuration updated: agent.cli. It takes effect at the next distillation or trigger extraction.")
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "codex", "config.yaml"), "the saved value has no draft marker")
	assert.Contains(t, settingsBox(frame, "Settings"), "Saved Inference at "+probe.saved[0].at.Format("15:04:05")+
		". The index reads as stale until the next index build.")
}

func TestSettingsCustomCommandRemovalIsExplicit(t *testing.T) {
	const before = "agent:\n  argv:\n    - my-agent\n    - --json\n    - \"-\"\nreviews:\n  window_days: 30\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)

	// The argv row is read-only here; its only change is the removal.
	h.press("down")
	assert.Contains(t, settingsBox(h.view(), "agent.argv"), "Read-only here; the only edit is its removal.")

	// Choosing a preset drafts the preset and the removal together.
	h.press("up", "enter", "down", "space", "enter")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "claude", "● draft"))
	assert.NotEmpty(t, settingsLine(frame, "agent.argv", "removed", "● draft"), "the removal shows in the list")
	assert.Contains(t, frame, "2 unsaved changes in Inference")

	h.press("s")

	flat := settingsBox(h.view(), "Save")
	for _, want := range []string{
		"Seamark writes 2 changes", "agent.argv [my-agent, --json, -] › removed",
		"The save removes the custom command agent.argv.", "− remove agent.argv, the custom command",
		"+ set agent.cli: claude", "- my-agent",
	} {
		assert.Contains(t, flat, want)
	}

	assert.Contains(t, footerLine(h.view()), "y save and remove agent.argv")

	h.press("y")
	require.Equal(t, 1, svc.callCount("SaveSettings"))
	assert.Equal(t, "agent:\n  cli: claude\nreviews:\n  window_days: 30\n", settingsBytes(t, svc))

	// Choosing custom again drops both edits, so nothing stays drafted.
	svc2 := settingsFake(t, before)
	h2 := settingsOpen(t, svc2, 120, 40)
	h2.press("enter", "down", "space", "up", "space", "enter")
	assert.Zero(t, settingsScr(h2).draftTotal(), "keeping the custom command leaves no draft")
	assert.Empty(t, svc2.called())
}

func TestSettingsReturnToDefaultRemovesTheKey(t *testing.T) {
	const before = "distill:\n  # pins need a review first\n  write: true\nindex:\n  generated: true\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)

	h.press("down", "down", "d")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "distill.write", "off", "● draft"))
	assert.Contains(t, settingsBox(frame, "distill.write"), "Draft remove the key; the default off applies")

	h.press("s")

	flat := settingsBox(h.view(), "Save")
	assert.Contains(t, flat, "distill.write true › removed")
	assert.Contains(t, flat, "− remove distill.write; the default off applies")
	assert.Contains(t, flat, "write a pin: a new apply, retarget or prune preview offers the write")

	h.press("y")
	assert.Equal(t, "distill:\n  # pins need a review first\nindex:\n  generated: true\n", settingsBytes(t, svc),
		"the key goes and its comment stays")

	// d on a field without a key changes nothing and says so.
	h.press("d")
	assert.Contains(t, h.view(), "distill.write has no key in .seamark/config.yaml; the default off applies.")
	assert.Zero(t, settingsScr(h).draftTotal())
}

func TestSettingsReviewWindowAcceptsOnlyPositiveDays(t *testing.T) {
	svc := settingsFake(t, "")
	h := settingsOpen(t, svc, 120, 40)
	scr := settingsScr(h)

	h.send(navigateMsg{dest: destSettings, intent: "group:evidence"})
	h.press("enter", "down", "space")
	require.True(t, scr.capturing(), "the day input captures the keys")
	assert.Contains(t, footerLine(h.view()), "ctrl+c quit")

	h.typeText("q1x")
	assert.Equal(t, "1", scr.ed.input.value(), "the input takes digits only")
	assert.False(t, h.quit, "q is text here, not quit")

	h.press("backspace")
	h.typeText("0")
	h.press("enter")
	assert.Contains(t, settingsBox(h.view(), "reviews.window_days"), "Enter a whole number of days above 0.")
	assert.True(t, scr.ed.typing, "an invalid value keeps the input open")

	h.press("backspace")
	h.typeText("180")
	h.press("enter")
	assert.False(t, scr.ed.typing)
	assert.NotEmpty(t, settingsLine(h.view(), "reviews.window_days", "180 days", "● draft"))
	assert.Contains(t, settingsBox(h.view(), "reviews.window_days"), "Next refresh 730 days, the saved value",
		"the next refresh uses the saved value until a save")

	// esc in the input drops the typed text and keeps the draft.
	h.press("space")
	h.typeText("9")
	h.press("esc")
	assert.Equal(t, settings.Edit{Field: settings.ReviewWindow, Days: 180}, scr.drafts[settings.ReviewWindow])

	h.press("esc", "s", "y")
	assert.Equal(t, "reviews:\n  window_days: 180\n", settingsBytes(t, svc), "a missing file gets only the key")

	// Unlimited writes the reader's 0.
	h.press("enter", "down", "down", "space", "enter", "s")
	assert.Contains(t, settingsBox(h.view(), "Save"), "+ set reviews.window_days: 0 (unlimited)")
	h.press("y")
	assert.Equal(t, "reviews:\n  window_days: 0\n", settingsBytes(t, svc))
}

func TestSettingsExcludeListEditor(t *testing.T) {
	const before = "index:\n  exclude:\n    - \"*.pb.go\" # protobuf output\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)
	scr := settingsScr(h)

	h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
	h.press("down", "enter")
	assert.Contains(t, h.view(), "index.exclude · editing")
	assert.Contains(t, settingsBox(h.view(), "index.exclude"), "+ add a pattern")
	assert.Contains(t, settingsBox(h.view(), "index.exclude"), "FORMS")

	// An invalid pattern stays in the input with the reader's reason.
	h.press("a")
	h.typeText("a/**/b")
	h.press("enter")
	assert.Contains(t, settingsBox(h.view(), "index.exclude"), "a/**/b is not valid")
	assert.True(t, scr.ed.typing)

	h.press("esc")
	assert.False(t, scr.ed.typing)
	assert.Zero(t, scr.draftTotal(), "a cancelled pattern leaves no draft")

	h.press("a")
	h.typeText("internal/gen/")
	h.press("enter", "a")
	h.typeText("**/testdata")
	h.press("enter")
	assert.NotEmpty(t, settingsLine(h.view(), "index.exclude", "3 patterns", "● draft"))

	// The editor refuses a duplicate.
	h.press("a")
	h.typeText("*.pb.go")
	h.press("enter")
	assert.Contains(t, settingsBox(h.view(), "index.exclude"), "*.pb.go is already in the list.")
	h.press("esc")

	// K moves the last pattern up; x removes the first one.
	h.press("up", "K", "home", "x")
	assert.Equal(t, []string{"**/testdata", "internal/gen/"}, scr.drafts[settings.IndexExclude].List)

	h.press("esc", "s")
	assert.Contains(t, settingsBox(h.view(), "Save"), "+ set index.exclude: 2 patterns, in this order")

	h.press("y")
	assert.Equal(t, "index:\n  exclude:\n    - '**/testdata'\n    - internal/gen/\n", settingsBytes(t, svc))
}

func TestSettingsPlanErrorsMarkTheirPattern(t *testing.T) {
	svc := settingsFake(t, "index:\n  exclude:\n    - \"ok/\"\n")
	svc.planSettings = func(*settings.Snapshot, []settings.Edit) (*settings.Plan, error) {
		return nil, fmt.Errorf("%w: exclude %q: bad pattern", settings.ErrInvalidEdit, "ok/")
	}

	h := settingsOpen(t, svc, 120, 40)
	h.send(navigateMsg{dest: destSettings, intent: "group:indexing"})
	h.press("down", "enter", "a")
	h.typeText("more/")
	h.press("enter", "esc", "s")

	assert.Contains(t, h.view(), "The draft is not valid")

	h.press("esc")
	assert.Contains(t, settingsBox(h.view(), "index.exclude"), `exclude "ok/": bad pattern`,
		"the error shows under its pattern")
	settingsNoWrites(t, svc)
}

func TestSettingsBlockedFileOffersTheManualRoute(t *testing.T) {
	const before = "agent:\n  cli: codex\nagent:\n  cli: claude\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	flat := settingsBox(frame, "Settings")
	assert.Contains(t, flat, "The file cannot be edited here:")
	assert.Contains(t, flat, "› .seamark/config.yaml:3")
	assert.Contains(t, flat, "Fix the file by hand; the other views stay usable.")
	assert.NotContains(t, flat, "not editable here", "the tail explains a file-wide block once")
	assert.NotContains(t, footerLine(frame), "s review save")

	h.press("enter", "down", "space")
	assert.Contains(t, h.view(), "Inference cannot be edited here")
	assert.Zero(t, settingsScr(h).draftTotal(), "a blocked group takes no draft")

	h.press("esc", "d", "s")
	assert.Empty(t, svc.called())
	assert.Equal(t, before, settingsBytes(t, svc))
}

func TestSettingsBlockedSectionLeavesOtherGroupsEditable(t *testing.T) {
	const before = "base: &b\n  cli: codex\nagent: *b\ndistill:\n  write: false\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)

	assert.Contains(t, settingsBox(h.view(), "Settings"), "Inference cannot be edited here")
	assert.NotEmpty(t, settingsLine(h.view(), "INFERENCE", "! not editable here"))
	assert.Empty(t, settingsLine(h.view(), "PIN WRITING", "not editable"))

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, 1, svc.callCount("SaveSettings"))
	assert.Equal(t, "base: &b\n  cli: codex\nagent: *b\ndistill:\n  write: true\n", settingsBytes(t, svc))
}

func TestSettingsPlanOfAStaleFileReadsItAgain(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	// Another editor changes the file after the inspection.
	settingsWrite(t, svc.root, "distill:\n  write: false\n# changed elsewhere\n")

	reads := svc.readCount("Settings")
	h.press("s")

	frame := h.view()
	assert.Contains(t, frame, "The file changed")
	assert.Contains(t, settingsBox(frame, "The file changed"), "The draft keeps only the edits that still change "+
		"the file; press s to review them against the file as it is now.")
	assert.Equal(t, 1, settingsScr(h).draftTotal(), "the file does not hold the drafted value, so the draft stays")
	assert.Greater(t, svc.readCount("Settings"), reads, "a stale plan reads the file again")
	assert.NotContains(t, footerLine(frame), "y save")

	h.press("y")
	settingsNoWrites(t, svc)

	// A new preview against the new snapshot saves.
	h.press("esc", "s", "y")
	assert.Equal(t, "distill:\n  write: true\n# changed elsewhere\n", settingsBytes(t, svc))
}

func TestSettingsSaveOfAStaleFileWritesNothing(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s")
	require.Contains(t, h.view(), "Save .seamark/config.yaml")

	const external = "distill:\n  write: false # someone else\n"
	settingsWrite(t, svc.root, external)

	reads := svc.readCount("Settings")
	h.press("y")

	require.Equal(t, 1, svc.callCount("SaveSettings"))
	assert.Equal(t, external, settingsBytes(t, svc), "the stale save keeps the other writer's bytes")

	frame := h.view()
	assert.Contains(t, frame, "Nothing was saved")
	assert.Contains(t, settingsBox(frame, "Nothing was saved"), "The file changed after the preview, so Seamark did not write it.")
	assert.Contains(t, settingsBox(frame, "Nothing was saved"), "press s to build a new preview from the file as it is now.")
	assert.Greater(t, svc.readCount("Settings"), reads, "a stale save reads the file again")
	assert.False(t, h.m.st.busy())
	assert.NotEmpty(t, settingsLine(frame, "distill.write", "on", "● draft"), "the draft stays")

	h.press("enter")
	assert.NotContains(t, h.view(), "Nothing was saved")
}

func TestSettingsSaveFailureKeepsTheFile(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
		err := errors.New("rename .seamark/config.yaml: permission denied")

		return settings.Result{Path: p.Snapshot.Path, Err: err}, err
	}

	h := settingsOpen(t, svc, 120, 40)
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s", "y")

	flat := settingsBox(h.view(), "Save failed")
	assert.Contains(t, h.view(), "Save failed")
	assert.Contains(t, flat, "Seamark did not replace the file, so it is unchanged.")
	assert.Contains(t, flat, "permission denied")
	assert.Contains(t, flat, "Nothing is retried automatically.")
	assert.False(t, h.m.st.busy())
	assert.Equal(t, 1, settingsScr(h).draftTotal(), "the draft stays for a new attempt")
}

func TestSettingsSaveWaitsForARunningOperation(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)

	require.True(t, h.m.st.begin("Distilling proposals"))

	// Drafting works while the slot is busy.
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "distill.write", "on", "● draft"))
	assert.Contains(t, settingsBox(frame, "Settings"), "Saving waits for the running operation: Distilling proposals.")
	assert.NotContains(t, footerLine(frame), "s review save", "s vanishes from the footer")
	assert.Contains(t, settingsBox(frame, "Settings"), "1 unsaved change in Pin writing")
	assert.NotContains(t, settingsBox(frame, "Settings"), "s reviews and saves", "the tail offers no disabled key")

	h.press("s")
	assert.Zero(t, svc.callCount("PlanSettings"), "s does not start while the slot is busy")
	assert.Contains(t, h.view(), "Saving waits for the running operation: Distilling proposals.")

	// A preview that opened before the operation cannot save during it.
	h.send(mutationDoneMsg{})
	assert.Contains(t, settingsBox(h.view(), "Settings"), "1 unsaved change in Pin writing · s reviews and saves",
		"the hint returns with the free slot")

	h.press("s")
	require.Contains(t, h.view(), "Save .seamark/config.yaml")
	require.True(t, h.m.st.begin("Refreshing evidence"))

	frame = h.view()
	assert.NotContains(t, footerLine(frame), "y save")
	assert.Contains(t, settingsBox(frame, "Save"), "Saving waits for the running operation: Refreshing evidence.")

	h.press("y")
	settingsNoWrites(t, svc)
	assert.Equal(t, "distill:\n  write: false\n", settingsBytes(t, svc))

	h.send(mutationDoneMsg{})
	h.press("y")
	assert.Equal(t, 1, svc.callCount("SaveSettings"))
	assert.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))
}

func TestSettingsEscWhilePreparingDropsThePlan(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)
	scr := settingsScr(h)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	// The plan result arrives after esc: the screen drops it.
	cmd := scr.review(settings.GroupPinWrite)
	require.Equal(t, flowPreparing, scr.flow.stage)
	assert.Contains(t, h.view(), "Preparing the save preview")

	h.press("esc")
	h.send(cmd())
	assert.Equal(t, flowClosed, scr.flow.stage, "a late plan opens no preview")
	settingsNoWrites(t, svc)
}

// Each review is a new flow value, and the generation lives on the
// screen. So the tickets keep growing, and a late plan of a closed
// review cannot open the preview of the next review.
func TestSettingsLatePlanOfAnEarlierReviewIsDropped(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)
	scr := settingsScr(h)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")

	first := scr.review(settings.GroupPinWrite)
	h.press("esc")
	require.Equal(t, flowClosed, scr.flow.stage)

	second := scr.review(settings.GroupPinWrite)
	h.send(first())
	assert.Equal(t, flowPreparing, scr.flow.stage, "the plan of the closed review opens no preview")

	h.send(second())
	assert.Equal(t, flowPreview, scr.flow.stage, "the plan of the open review opens the preview")
	settingsNoWrites(t, svc)
}

func TestSettingsNoOpPlanDropsTheDraft(t *testing.T) {
	svc := settingsFake(t, "distill:\n  write: false\n")
	svc.planSettings = func(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
		return &settings.Plan{Snapshot: snap, Group: settings.GroupPinWrite, Edits: edits, NoOp: true}, nil
	}

	h := settingsOpen(t, svc, 120, 40)
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s")

	assert.Contains(t, h.view(), "Nothing to save")
	assert.Zero(t, settingsScr(h).draftTotal())

	h.press("y")
	settingsNoWrites(t, svc)

	h.press("enter")
	assert.NotContains(t, h.view(), "Nothing to save")
}

func TestSettingsInferenceSaveLeavesIntegrationFiles(t *testing.T) {
	svc := settingsFake(t, "")

	claude := filepath.Join(svc.root, ".claude", "settings.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(claude), 0o755))
	require.NoError(t, os.WriteFile(claude, []byte(`{"hooks":{}}`), 0o644))

	h := settingsOpen(t, svc, 120, 40)
	h.press("enter", "down", "space", "enter", "s", "y")

	assert.Equal(t, "agent:\n  cli: codex\n", settingsBytes(t, svc))

	data, err := os.ReadFile(claude)
	require.NoError(t, err)
	assert.JSONEq(t, `{"hooks":{}}`, string(data), "an inference save leaves the client files alone")
}

func TestSettingsCLIResetUnderCustomCommandSaysArgvStays(t *testing.T) {
	const before = "agent:\n  cli: codex\n  argv: [my-agent, --json]\n"

	svc := settingsFake(t, before)
	h := settingsOpen(t, svc, 120, 40)

	// d removes the agent.cli key, but agent.argv still runs. No view may
	// say that the default claude takes over (spec R-12).
	h.press("d")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "not set", "● draft"), "the list shows the removed key")
	assert.Empty(t, settingsLine(frame, "agent.cli", "claude", "● draft"), "the default does not take over")

	flat := settingsBox(frame, "agent.cli")
	assert.Contains(t, flat, "Draft remove the key; agent.argv stays in effect, so the command that runs does not change")
	assert.Contains(t, flat, "(●) custom", "the custom command stays the selected choice")
	assert.NotContains(t, flat, "the default claude applies")

	h.press("s")
	h.view()
	h.press("end")

	flat = settingsBox(h.view(), "Save")
	for _, want := range []string{
		"agent.cli codex › removed", "− remove agent.cli; agent.argv stays in effect",
		"agent.argv overrides agent.cli, so this save does not change the command that runs.",
		"agent.argv stays in effect, so the command that runs does not change.",
	} {
		assert.Contains(t, flat, want)
	}

	assert.NotContains(t, flat, "the default claude applies")
	assert.NotContains(t, flat, "EFFECTIVE", "the effective command does not change")
	assert.Contains(t, footerLine(h.view()), "y save")
	assert.NotContains(t, footerLine(h.view()), "remove agent.argv", "the save keeps agent.argv")

	h.press("y")
	assert.Equal(t, "agent:\n  argv: [my-agent, --json]\n", settingsBytes(t, svc))

	frame = h.view()
	assert.Contains(t, frame, "Configuration updated: agent.cli. agent.argv stays in effect, so the command "+
		"that runs does not change.")
	assert.NotContains(t, frame, "It takes effect")
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "not set", "agent.argv"), "the saved row matches the draft")

	// d without a key names the custom command, not the default.
	h.press("d")
	assert.Contains(t, h.view(), "agent.cli has no key in .seamark/config.yaml; agent.argv stays in effect.")
	assert.Zero(t, settingsScr(h).draftTotal())
}

func TestSettingsCLIResetWithTheArgvRemovalAppliesTheDefault(t *testing.T) {
	svc := settingsFake(t, "agent:\n  cli: codex\n  argv: [my-agent, --json]\n")
	h := settingsOpen(t, svc, 120, 40)

	// The argv editor drafts the removal; then d resets agent.cli.
	h.press("down", "enter", "down", "space", "enter", "up", "d")

	frame := h.view()
	assert.NotEmpty(t, settingsLine(frame, "agent.cli", "claude", "● draft"), "without agent.argv the default runs")
	assert.Contains(t, settingsBox(frame, "agent.cli"), "Draft remove the key; the default claude applies")

	h.press("s")

	flat := settingsBox(h.view(), "Save")
	assert.Contains(t, flat, "− remove agent.cli; the default claude applies")
	assert.Contains(t, flat, "The save removes the custom command agent.argv.")
	assert.NotContains(t, flat, "agent.argv stays in effect")

	h.press("y")
	assert.Equal(t, "", strings.TrimSpace(settingsBytes(t, svc)), "both keys go")
	assert.Contains(t, h.view(), "Configuration updated: agent.cli, agent.argv. They take effect at the next "+
		"distillation or trigger extraction.")
}

func TestSettingsSaveReloadsDiagnosticsAndStartsNoWork(t *testing.T) {
	for _, tc := range []struct {
		name, content, intent string
		// keys draft one change of the group from its first field.
		keys []string
		// when is the TAKES EFFECT line of the preview.
		when string
		// ledger reports the reload of the proposal ledger, which
		// carries the pin-write gate.
		ledger bool
	}{
		{
			name: "inference", intent: "group:inference", keys: []string{"enter", "down", "space", "enter"},
			when: "agent.cli: at the next distillation or trigger extraction.",
		},
		{
			name: "pin writing", content: "distill:\n  write: false\n", intent: "group:pinwrite",
			keys: []string{"enter", "down", "space", "enter"},
			when: "distill.write: at the next apply, retarget or prune preview.", ledger: true,
		},
		{
			name: "evidence", intent: "group:evidence", keys: []string{"enter", "down", "down", "space", "enter"},
			when: "reviews.window_days: at the next confirmed evidence refresh.",
		},
		{
			name: "indexing", intent: "group:indexing", keys: []string{"enter", "down", "space", "enter"},
			when: "index.generated: at the next explicit index build.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := settingsFake(t, tc.content)
			h := settingsOpen(t, svc, 200, 50)

			h.send(navigateMsg{dest: destSettings, intent: tc.intent})
			h.press(tc.keys...)
			require.Equal(t, 1, settingsScr(h).draftTotal())

			h.press("s")
			h.view()
			h.press("end")

			flat := settingsBox(h.view(), "Save")
			assert.Contains(t, flat, "TAKES EFFECT "+tc.when)
			assert.Contains(t, flat, "start an index build, an evidence refresh or a distillation")

			gens := h.m.st.gens
			h.press("y")
			after := h.m.st.gens

			// A save only plans and writes; it starts no build, fetch or
			// agent run (spec R-13, AC-10).
			assert.Equal(t, []string{"PlanSettings", "SaveSettings"}, svc.called())

			// Every save changes the index fingerprint, so Doctor, the
			// index and the evidence view read again.
			for _, k := range []sectionKind{secSettings, secDoctor, secIndex, secLearning} {
				assert.Greater(t, after[k].latest(), gens[k].latest(), "section %d reloads", k)
			}

			if tc.ledger {
				assert.Greater(t, after[secLedger].latest(), gens[secLedger].latest(), "the ledger reloads for its write gate")
			} else {
				assert.Equal(t, gens[secLedger], after[secLedger], "only pin writing reloads the ledger")
			}

			assert.Equal(t, gens[secIntegrations], after[secIntegrations], "the client files do not change")
			assert.Equal(t, gens[secStats], after[secStats])
		})
	}
}

func TestSettingsStaleModalsFollowTheReloadedDraft(t *testing.T) {
	// Another editor writes the drafted value before the plan. The
	// reload drops the draft, so no modal may promise that it stays.
	svc := settingsFake(t, "distill:\n  write: false\n")
	h := settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter")
	settingsWrite(t, svc.root, "distill:\n  write: true\n")
	h.press("s")

	flat := settingsBox(h.view(), "The file changed")
	assert.Contains(t, flat, "The file now holds every value of the draft, so the draft is dropped and nothing "+
		"is left to save.")
	assert.NotContains(t, flat, "The draft stays")
	assert.NotContains(t, flat, "press s")
	assert.Zero(t, settingsScr(h).draftTotal())

	h.press("esc", "s")
	assert.Contains(t, h.view(), "Pin writing has no unsaved change.")
	settingsNoWrites(t, svc)

	// The same holds when the file changes between the preview and y.
	svc = settingsFake(t, "distill:\n  write: false\n")
	h = settingsOpen(t, svc, 120, 40)

	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s")
	settingsWrite(t, svc.root, "distill:\n  write: true # someone else\n")
	h.press("y")

	flat = settingsBox(h.view(), "Nothing was saved")
	assert.Contains(t, flat, "nothing is left to save")
	assert.NotContains(t, flat, "The draft stays")
	assert.Equal(t, "distill:\n  write: true # someone else\n", settingsBytes(t, svc))
}
