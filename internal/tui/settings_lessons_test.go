package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// The tests of the lessons groups of Settings (contracts §4.3). They
// drive the console through the harness over the real settings package.
// settingsSvc inspects, plans, and saves the files of a temporary
// workspace. So each test can check the exact bytes of lessons.yaml.

// lessonsWrite writes the lessons.yaml of a workspace.
func lessonsWrite(t testing.TB, root, content string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(pinfile.File))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// lessonsBytes returns the lessons.yaml of svc, or "" when it is absent.
func lessonsBytes(t *testing.T, svc *settingsSvc) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(pinfile.File)))
	if os.IsNotExist(err) {
		return ""
	}

	require.NoError(t, err)

	return string(data)
}

// lessonsFake builds the settings services over a workspace whose
// lessons.yaml holds lessons and whose config.yaml is absent.
func lessonsFake(t *testing.T, lessons string) *settingsSvc {
	t.Helper()

	svc := settingsFake(t, "")
	if lessons != "" {
		lessonsWrite(t, svc.root, lessons)
	}

	return svc
}

// lessonsSnapFake returns plain fake services whose settings snapshot is
// a real inspection of a workspace with lessons.yaml, and whose planner
// is the real one. The receiver table of state_saved_test.go needs a
// *fakeServices, so this helper does not wrap it.
func lessonsSnapFake(t testing.TB, lessons string) *fakeServices {
	t.Helper()

	root := t.TempDir()
	lessonsWrite(t, root, lessons)

	snap, err := settings.Inspect(root, integration.Builtin())
	require.NoError(t, err)

	f := newFake()
	f.settings = snap
	f.planSettings = func(snap *settings.Snapshot, edits []settings.Edit) (*settings.Plan, error) {
		return settings.PlanPatch(snap, integration.Builtin(), edits)
	}

	return f
}

// lessonsOpen opens Settings at size and moves to the group named by
// intent, for example "group:delivery".
func lessonsOpen(t testing.TB, svc services.All, intent string, w, hgt int) *harness {
	t.Helper()

	h := settingsOpen(t, svc, w, hgt)
	h.send(navigateMsg{dest: destSettings, intent: intent})

	return h
}

// muteFile holds one entry of each kind of the mute editor. It has a
// plain entry, a null item, and a flow item that matches nothing. It
// also has a duplicate in another case, a region entry, and a flow entry
// with an unknown key. A pin follows, which no mute edit may change. It
// has no comment: the tests remove its entries, and the planner refuses
// to remove an entry with a comment. TestSettingsMuteRemovalKeepsAComment
// covers that refusal with its own file.
const muteFile = `threshold: 3
mute:
  - rule: F541
  -
  - {}
  - rule: f541
  - region: api/
  - {rule: E1, region: web, why: legacy}
pin:
  - rule: R
    note: keep this pin
`

func TestSettingsFieldTableCoversEveryLessonsField(t *testing.T) {
	// The console lists the fields and groups of the settings package in
	// its order, so both views agree.
	var want []settings.Field
	for _, g := range settingsGroups {
		want = append(want, g.Fields()...)
	}

	assert.Equal(t, want, settingsFields)
	assert.Equal(t, []settings.Group{settings.GroupDelivery, settings.GroupMutes}, settingsGroups[4:])

	titles := map[string]bool{}

	for _, g := range settingsGroups {
		title := settingsGroupTitle(g)
		assert.NotEqual(t, "Unknown group", title, "group %v has its own title", g)
		assert.False(t, titles[title], "the title %q names one group", title)

		titles[title] = true
	}

	assert.Equal(t, "Lesson delivery", settingsGroupTitle(settings.GroupDelivery))
	assert.Equal(t, "Lesson mutes", settingsGroupTitle(settings.GroupMutes))

	// Every field has a description and an editor, and every group has
	// an open intent on Overview and in Settings.
	h := settingsOpen(t, lessonsFake(t, ""), 120, 40)
	scr := settingsScr(h)

	for _, f := range settingsFields {
		assert.NotEmpty(t, settingsDescriptions[f], "field %s", f)

		switch f {
		case settings.IndexExclude, settings.Mutes, settings.AgentArgv:
		default:
			assert.NotEmpty(t, scr.choices(f), "field %s has a radio editor", f)
		}
	}

	for _, g := range settingsGroups {
		intent := ovSettingsIntents[g]
		require.NotEmpty(t, intent, "group %v", g)

		name, _ := strings.CutPrefix(intent, "group:")
		assert.Equal(t, g, settingsIntents[name], "intent %s opens its group", intent)
	}
}

func TestSettingsLessonsGroupsListAfterTheConfigGroups(t *testing.T) {
	svc := lessonsFake(t, muteFile)
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)

	list := settingsBox(frame, "Settings")
	assert.Less(t, strings.Index(list, "INDEXING"), strings.Index(list, "LESSON DELIVERY"))
	assert.Less(t, strings.Index(list, "LESSON DELIVERY"), strings.Index(list, "LESSON MUTES"))
	assert.NotEmpty(t, settingsLine(frame, "threshold", "3", "lessons.yaml"))
	assert.NotEmpty(t, settingsLine(frame, "pin_budget", "3", "default"))
	assert.NotEmpty(t, settingsLine(frame, "mute", "5 entries", "lessons.yaml"))

	// The pane title names the file of the selected group.
	assert.Contains(t, frame, "── .seamark/config.yaml ─╮")

	h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
	frame = h.view()
	assert.Contains(t, frame, "── .seamark/lessons.yaml ─╮")
	assert.Contains(t, settingsBox(frame, "mute"), "Source .seamark/lessons.yaml, line 2")
	assert.Empty(t, svc.called())
}

func TestSettingsDeliveryShowsStoredDefaultAndEffective(t *testing.T) {
	cases := []struct {
		file      string
		field     settings.Field
		stored    string
		effective string
		note      string
		// choice is the marked radio choice.
		choice string
	}{
		{"threshold: 0\n", settings.Threshold, "0", "2", "stored 0 → default 2", "(●) default"},
		{"threshold: -1\n", settings.Threshold, "-1", "2", "stored -1 → default 2", "(●) default"},
		{"threshold: 1.5\n", settings.Threshold, "1.5", "1", "stored 1.5 → 1", "(●) number 1"},
		{"threshold: 0x10\n", settings.Threshold, "0x10", "16", "stored 0x10 → 16", "(●) number 16"},
		{"threshold: 2\n", settings.Threshold, "2", "2", "", "(●) number 2"},
		{"pin_budget: 100\n", settings.PinBudget, "100", "8", "stored 100 → at most 8 pins per edit (the hook total cap)",
			"(●) number 100"},
		{"change_budget: -1\n", settings.ChangeBudget, "-1", "6", "stored -1 → default 6", "(●) default"},
		{"hook_delivery: once-per-context\n", settings.HookDelivery, "once-per-context", "once-per-context", "",
			"(●) once-per-context"},
		{"hook_delivery: ''\n", settings.HookDelivery, "(no value)", "always", "", "(●) default"},
	}

	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			h := lessonsOpen(t, lessonsFake(t, c.file), "group:delivery", 120, 40)

			for settingsScr(h).field() != c.field {
				h.press("down")
			}

			frame := h.view()
			assertFrame(t, frame, 120, 40)

			box := settingsBox(frame, string(c.field))
			assert.Contains(t, box, "Stored "+c.stored+" ")
			assert.Contains(t, box, "Effective "+c.effective+" ")
			assert.Contains(t, box, "Default "+settingsDefaultOf(t, h, c.field)+" ")
			assert.Contains(t, box, c.choice)

			// The box can scroll, so the activation is read from the
			// editor lines.
			lines, _, _ := settingsScr(h).editorLines(c.field, 200, false)
			assert.Contains(t, lesFlat(ansi.Strip(strings.Join(lines, "\n"))), "Affects "+c.field.Activation().When+".",
				"the detail shows the activation")
			assert.NotEmpty(t, settingsLine(frame, string(c.field), c.effective, "lessons.yaml"),
				"the list shows the effective value")

			if c.note != "" {
				assert.Contains(t, box, c.note)
			}
		})
	}
}

// settingsDefaultOf returns the default text of f in the snapshot.
func settingsDefaultOf(t *testing.T, h *harness, f settings.Field) string {
	t.Helper()

	st, ok := settingsScr(h).fieldState(f)
	require.True(t, ok)

	return st.Default
}

func TestSettingsDeliveryNumberRefusesAnOverflowInline(t *testing.T) {
	svc := lessonsFake(t, "pin_budget: 4\n")
	h := lessonsOpen(t, svc, "group:delivery", 120, 40)
	h.press("down", "enter", "down", "space")
	require.True(t, settingsScr(h).ed.typing)

	// The largest value of the planner plus one is refused on enter, and
	// the typed text stays for a fix.
	h.press("backspace")
	h.typeText("2147483648")
	h.press("enter")

	box := settingsBox(h.view(), "pin_budget")
	assert.Contains(t, box, "pin_budget must be a whole number from 1 to 2147483647.")
	assert.True(t, settingsScr(h).ed.typing)
	assert.Equal(t, "2147483648", settingsScr(h).ed.input.Value())

	// An eleventh digit is refused visibly, never cut.
	h.typeText("9")
	assert.Equal(t, "2147483648", settingsScr(h).ed.input.Value())
	assert.Contains(t, settingsBox(h.view(), "pin_budget"), "This field holds at most 10 characters, so nothing "+
		"was added.")

	// Zero is refused, and a letter is left out with a notice.
	for range 10 {
		h.press("backspace")
	}

	h.typeText("0x")
	h.press("enter")
	box = settingsBox(h.view(), "pin_budget")
	assert.Contains(t, box, "pin_budget must be a whole number from 1 to 2147483647.")
	assert.Zero(t, settingsScr(h).draftTotal(), "a refused number never enters the draft")

	// The largest value is kept and saved.
	h.press("backspace")
	h.typeText("2147483647")
	h.press("enter")
	require.False(t, settingsScr(h).ed.typing)

	h.press("enter", "s", "y")
	assert.Equal(t, "pin_budget: 2147483647\n", lessonsBytes(t, svc))
	assert.Equal(t, "Lesson settings updated: pin_budget.", h.m.toast.text)
}

func TestSettingsDeliverySavesAndResetsKeys(t *testing.T) {
	svc := lessonsFake(t, "# delivery\nthreshold: 4 # team value\npin:\n  - rule: R\n    note: n\n")
	h := lessonsOpen(t, svc, "group:delivery", 120, 40)
	probe := &settingsProbe{}
	h.m.screens[destDoctor] = probe

	// d on threshold removes the key; once-per-context on hook_delivery
	// adds one. One save writes the group.
	h.press("d", "down", "down", "down", "enter", "down", "down", "space", "enter")

	frame := h.view()
	assert.Contains(t, frame, "2 unsaved changes in Lesson delivery", "a lessons draft counts in its group")
	assert.NotEmpty(t, settingsLine(frame, "threshold", "2", "● draft"))
	assert.NotEmpty(t, settingsLine(frame, "hook_delivery", "once-per-context", "● draft"))

	h.press("s")

	save := settingsBox(h.view(), "Save .seamark/lessons.yaml")
	assert.Contains(t, save, "Seamark writes 2 changes to .seamark/lessons.yaml. The pins, the other keys, "+
		"comments and unknown keys stay as they are.")
	assert.Contains(t, save, "threshold 4 › removed")
	assert.Contains(t, save, "hook_delivery not set (default always) › once-per-context")
	assert.Contains(t, save, "− remove threshold; the default 2 applies")
	assert.Contains(t, save, "+ set hook_delivery: once-per-context")
	assert.Contains(t, save, settingsReplaceText)

	h.press("end")
	save = settingsBox(h.view(), "Save .seamark/lessons.yaml")
	assert.Contains(t, save, "threshold affects "+settings.Threshold.Activation().When+".")
	assert.Contains(t, save, "hook_delivery affects "+settings.HookDelivery.Activation().When+".")
	assert.NotContains(t, save, "The index reads as stale", "the index fingerprint does not cover lessons.yaml")

	h.press("y")
	assert.Equal(t, "# delivery\npin:\n  - rule: R\n    note: n\n\nhook_delivery: once-per-context\n", lessonsBytes(t, svc),
		"the key goes, its comment goes with its line, and a new key goes where a new top-level key goes")
	assert.Equal(t, "Lesson settings updated: threshold, hook_delivery.", h.m.toast.text)
	require.Len(t, probe.lessons, 1, "a lessons save tells every screen")
	assert.Equal(t, "Settings › lesson delivery", probe.lessons[0].by)
	assert.Empty(t, probe.saved, "a lessons save sends no settingsSavedMsg")
	assert.Contains(t, settingsBox(h.view(), "Settings"), "Each consumer reads the file at its next run.")
	assert.Equal(t, "", settingsBytes(t, svc), "config.yaml stays absent")
}

func TestSettingsUnknownModeBlocksBothLessonsGroupsOnly(t *testing.T) {
	svc := lessonsFake(t, "hook_delivery: Always\nmute:\n  - rule: F541\n")
	h := settingsOpen(t, svc, 120, 40)

	frame := h.view()
	assert.Contains(t, settingsLine(frame, "LESSON DELIVERY"), "not editable here")
	assert.Contains(t, settingsLine(frame, "LESSON MUTES"), "not editable here")

	for _, g := range []string{"INFERENCE", "PIN WRITING", "EVIDENCE", "INDEXING"} {
		assert.NotContains(t, settingsLine(frame, g), "not editable", "a lessons.yaml fault never blocks %s", g)
	}

	// A config.yaml group still drafts and saves.
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s", "y")
	assert.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	// Each lessons group refuses with the reader error and its line.
	for _, intent := range []string{"group:delivery", "group:mutes"} {
		h.send(navigateMsg{dest: destSettings, intent: intent})
		h.press("d")
		assert.Contains(t, h.m.toast.text, "cannot be edited here: the lessons reader rejects the file")
		assert.Contains(t, h.m.toast.text, "fix .seamark/lessons.yaml by hand.")
		assert.Zero(t, settingsScr(h).draftCount(settings.GroupDelivery))
	}

	h.press("enter", "a")
	assert.Nil(t, settingsScr(h).ed.rec, "a blocked mute group opens no record form")
	assert.Equal(t, "hook_delivery: Always\nmute:\n  - rule: F541\n", lessonsBytes(t, svc))
}

// settingsResetInspections are the inspections of three clients for the
// hook_delivery help: a verified reset hook with a finding, a client
// without an inspection of the reset hook, and an unsupported one.
func settingsResetInspections() []integration.Inspection {
	resets := integration.CapabilityInspection{
		Capability: integration.CapabilityResets, Supported: true, State: integration.StateCurrent,
		Verification: integration.VerificationEvidence{
			Level: integration.VerificationVerified, ClientVersion: "2.1", Surface: "SessionStart compact",
		},
		Detail: "SessionStart hook with the compact matcher",
	}

	return []integration.Inspection{
		{
			ClientID: "claude", Name: "Claude Code", Capabilities: []integration.CapabilityInspection{resets},
			Findings: []integration.Finding{{Level: integration.FindingWarning, Reason: "trust cannot be read"}},
		},
		{ClientID: "codex", Name: "Codex"},
		{ClientID: "agent", Name: "Agent", Capabilities: []integration.CapabilityInspection{
			{Capability: integration.CapabilityResets, State: integration.StateAbsent},
		}},
	}
}

func TestSettingsModeHelpQuotesTheInspectionsOnly(t *testing.T) {
	render := func(t *testing.T, onPath []string, declared bool) string {
		t.Helper()

		svc := lessonsFake(t, "")
		svc.integrations = settingsResetInspections()
		svc.agentsOnPath = onPath

		if declared {
			for i := range svc.integrations {
				svc.integrations[i].Declared = []integration.Capability{integration.CapabilityResets}
			}
		}

		h := lessonsOpen(t, svc, "group:delivery", 200, 50)
		h.press("down", "down", "down")
		require.Equal(t, settings.HookDelivery, settingsScr(h).field())

		return settingsBox(h.view(), "hook_delivery")
	}

	box := render(t, nil, false)

	// Part one: the engine rule. Part two: the facts as they are. Part
	// three: the route.
	assert.Contains(t, box, "REPETITION "+settingsRepetitionRule)
	assert.Contains(t, box, "Claude Code reset hook current verification verified detail SessionStart hook with the "+
		"compact matcher ! warning trust cannot be read")
	assert.Contains(t, box, "Codex reset hook no inspection of the reset hook ✓ No findings.")
	assert.Contains(t, box, "Agent reset hook not supported verification unknown",
		"an unsupported capability has no state of its own")
	assert.Contains(t, box, settingsRepetitionRoute)

	for _, claim := range []string{"supports", "Codex supports", "suppression works"} {
		assert.NotContains(t, box, claim)
	}

	// A declared capability and an executable on PATH change nothing:
	// neither proves that a reset reaches the hook.
	assert.Equal(t, box, render(t, []string{"claude", "codex"}, true))
}

// muteEditorOpen opens the mute editor over muteFile.
func muteEditorOpen(t *testing.T, w, hgt int) (*harness, *settingsSvc) {
	t.Helper()

	svc := lessonsFake(t, muteFile)
	h := lessonsOpen(t, svc, "group:mutes", w, hgt)
	h.press("enter")
	require.True(t, settingsScr(h).muteEditing())

	return h, svc
}

func TestSettingsMuteRowsShowEveryEntryAsItIs(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)

	box := settingsBox(h.view(), "mute")
	for _, want := range []string{
		"MUTES 6",
		"1 rule F541 · line 3",
		"2 no rule and no region · line 4 · matches nothing",
		"3 no rule and no region · line 5 · matches nothing · flow entry: remove only",
		"4 rule f541 · line 6 · duplicate of line 3",
		"5 region api/ · line 7",
		"6 rule E1 · region web · line 8 · flow entry: remove only · other keys stay: why",
		"+ add mute",
	} {
		assert.Contains(t, box, want)
	}

	// A flow entry and an entry without keys only go: enter names the
	// refusal of the planner and the route, and no form opens.
	h.press("down", "down", "enter")
	assert.Nil(t, settingsScr(h).ed.rec)
	assert.Equal(t, "The entry at line 5 cannot change: the mute entry uses flow style { }; remove the entry and add "+
		"a new one. x removes it, and a adds a new one.", h.m.toast.text)

	h.press("up", "enter")
	assert.Nil(t, settingsScr(h).ed.rec)
	assert.Equal(t, "The entry at line 4 cannot change: the mute entry holds no keys; remove the entry and add a "+
		"new one. x removes it, and a adds a new one.", h.m.toast.text)

	// x on the null item drafts its removal; the review shows the entry
	// and the planner note, and y removes only its line.
	h.press("x")
	assert.Contains(t, settingsBox(h.view(), "mute"), "draft: remove this entry")

	h.press("s")
	save := settingsBox(h.view(), "Save .seamark/lessons.yaml")
	assert.Contains(t, save, "remove line 4: no rule and no region")
	assert.Contains(t, save, "− remove the mute at line 4: no rule and no region")

	h.press("end")
	assert.Contains(t, settingsBox(h.view(), "Save .seamark/lessons.yaml"), "Removing this mute may not bring a "+
		"lesson back")

	h.press("y")
	assert.Equal(t, strings.Replace(muteFile, "  -\n", "", 1), lessonsBytes(t, svc))
	assert.Equal(t, "Lesson settings updated: mute.", h.m.toast.text)
}

func TestSettingsMuteCursorRowShowsItsNotesAtTheWindowEdge(t *testing.T) {
	// At 60x20 the editor window ends near row 3. Each down then puts the
	// cursor row on the bottom edge, and the notes under the row hold its
	// verdict, so they show with it.
	h, _ := muteEditorOpen(t, 60, 20)

	h.press("down", "down", "down")
	assert.Contains(t, settingsBox(h.view(), "mute"), "4 rule f541 · line 6 · duplicate of line 3")

	// Row 6 has a note of two lines at this width.
	h.press("down", "down")
	assert.Contains(t, settingsBox(h.view(), "mute"), "6 rule E1 · region web · line 8 · flow entry: remove only · "+
		"other keys stay: why")
}

func TestSettingsMuteDetailNamesEnterOnlyWhileTheEditorIsClosed(t *testing.T) {
	svc := lessonsFake(t, muteFile)
	h := lessonsOpen(t, svc, "group:mutes", 120, 40)

	closed := settingsScr(h).explanation(settings.Mutes)
	assert.True(t, strings.HasSuffix(closed, " "+muteOpenHelp), closed)
	assert.Contains(t, closed, `A region with a trailing "/" matches only the regions under it.`)

	// The open editor names its own keys, so the detail drops the key
	// that opens it and keeps the rule of a match.
	h.press("enter")
	require.True(t, settingsScr(h).muteEditing())

	open := settingsScr(h).explanation(settings.Mutes)
	assert.NotContains(t, open, muteOpenHelp)
	assert.Equal(t, strings.TrimSuffix(closed, " "+muteOpenHelp), open)
	assert.NotContains(t, lesBoxFlat(h.view()), "enter opens the mute editor")
}

func TestSettingsMuteRecordFormCapturesEveryKey(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)
	h.press("a")
	require.NotNil(t, settingsScr(h).ed.rec)
	assert.True(t, settingsScr(h).capturing(), "the record form takes every key")

	// Digits, q, ?, r, and the action letters type into the field.
	h.typeText("q15?rsxy")
	h.press("tab")
	h.typeText("3/")
	h.paste("4\n")

	rec := settingsScr(h).ed.rec
	assert.Equal(t, "q15?rsxy", rec.rule.Value())
	assert.Equal(t, "3/", rec.region.Value(), "a paste with a line break is refused whole")
	assert.Contains(t, settingsBox(h.view(), "mute"), "This field takes one line")
	assert.Equal(t, destSettings, h.m.active)
	assert.False(t, h.quit)
	assert.False(t, h.m.help)
	assert.Empty(t, svc.called())
	assert.Contains(t, footerLine(h.view()), "ctrl+c quit")

	// esc closes the form and keeps the draft as it was.
	h.press("esc")
	assert.Nil(t, settingsScr(h).ed.rec)
	assert.Zero(t, settingsScr(h).draftTotal())
}

func TestSettingsMuteRecordShowsLiveVerdicts(t *testing.T) {
	h, _ := muteEditorOpen(t, 200, 50)
	h.press("a")

	box := func() string { return settingsBox(h.view(), "mute") }

	assert.Contains(t, box(), "Type a rule, a region, or both.")

	// The verdicts of settings.CheckMute show at their fields.
	h.typeText(" F541")
	assert.Contains(t, box(), "the rule starts or ends with a space")

	h.press("home", "delete", "tab")
	// The empty region field shows its hint. The hint has no trailing
	// "/", because a "/" also changes what the mute hides (trial F1).
	assert.Contains(t, box(), "Region _ region, for example api")
	assert.NotContains(t, box(), "for example api/")
	h.typeText("../x")
	assert.Contains(t, box(), `the region contains ".."`)

	// A duplicate refuses with the line of the first entry.
	for range 4 {
		h.press("backspace")
	}

	assert.Contains(t, box(), "Duplicate of line 3 (rule F541): a save would change nothing that it hides.")

	h.press("enter")
	assert.NotNil(t, settingsScr(h).ed.rec, "enter keeps nothing while a verdict refuses")
	assert.Contains(t, box(), "Nothing was kept: fix the fields above first.")

	// A valid record names what it hides, its normalization, and the
	// entries that overlap it.
	h.typeText("./api/")
	flat := box()
	assert.Contains(t, flat, `leading "./" removed`)
	assert.Contains(t, flat, "Hides mined lessons with rule F541 (any case) under api/, not api itself; pins are never muted: d "+
		"Remove or - Prune in Lessons › Pins.")
	// The form shows the overlap notes of the planner, one per entry,
	// so the form and the review use the same words.
	for _, note := range []string{
		"Another entry also matches some of the same lessons: line 3, rule F541.",
		"Another entry also matches some of the same lessons: line 6, rule f541.",
		"Another entry also matches some of the same lessons: line 7, region api/.",
	} {
		assert.Contains(t, flat, note)
	}

	h.press("enter")
	assert.Nil(t, settingsScr(h).ed.rec)
	assert.Contains(t, box(), "rule F541 · region api/ ! draft: a new entry after the last one")

	// The other two match descriptions.
	h.press("enter")
	require.NotNil(t, settingsScr(h).ed.rec, "enter on the drafted entry edits it")
	assert.Equal(t, "./api/", settingsScr(h).ed.rec.region.Value())

	// The rule field has the keys first: a rule without a region hides
	// the rule in every region.
	h.press("home")

	for range 4 {
		h.press("delete")
	}

	h.typeText("W7")
	h.press("tab")

	for range 6 {
		h.press("backspace")
	}

	assert.Contains(t, box(), "Hides mined lessons with rule W7 (any case) in every region;")

	// A region without a rule hides every mined lesson under it.
	h.press("tab", "backspace", "backspace", "tab")
	h.typeText("db")
	assert.Contains(t, box(), "Hides every mined lesson under db;")
}

func TestSettingsMuteDraftHoldsOneOperation(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)

	// An add goes into the draft.
	h.press("a")
	h.typeText("W1")
	h.press("enter")
	require.Equal(t, settings.MuteAdd, settingsScr(h).muteDraft().Op)

	// x on an entry is a second operation: it asks first.
	h.press("home", "x")
	require.NotNil(t, settingsScr(h).prompt)

	assert.Contains(t, settingsBox(h.view(), "Save or discard the mute draft?"), "The draft already holds one mute "+
		"operation: add rule W1. One save changes one entry. Press s to review and save it, or d to discard it and "+
		"remove the entry at line 3.")

	// esc keeps the add and drops the removal.
	h.press("esc")
	assert.Nil(t, settingsScr(h).prompt)
	assert.Equal(t, settings.MuteAdd, settingsScr(h).muteDraft().Op)

	// d discards the add and runs the removal.
	h.press("x", "d")
	d := settingsScr(h).muteDraft()
	require.NotNil(t, d)
	assert.Equal(t, settings.MuteRemove, d.Op)
	assert.Equal(t, 0, d.Ref.Index)

	// x on the same entry is the same operation, so it asks nothing.
	h.press("x")
	assert.Nil(t, settingsScr(h).prompt)

	// a asks again; s reviews the removal.
	h.press("a", "s")
	assert.Contains(t, settingsBox(h.view(), "Save .seamark/lessons.yaml"), "remove line 3: rule F541")
	assert.Equal(t, []string{"PlanSettings"}, svc.called())
}

// The planner refuses to remove a mute entry with a comment after its
// text, because the comment would go with the line. The review then
// shows the reason and the manual route. The draft stays, and the file
// does not change.
func TestSettingsMuteRemovalKeepsAComment(t *testing.T) {
	const before = "mute:\n  - rule: F541   # noisy\n  - rule: E1\n"

	svc := lessonsFake(t, before)
	h := lessonsOpen(t, svc, "group:mutes", 120, 40)

	h.press("enter", "x", "s")

	box := settingsBox(h.view(), "Settings cannot be saved here")
	assert.Contains(t, box, "The file cannot be edited safely: a comment sits inside the mute entry.")
	assert.Contains(t, box, "› .seamark/lessons.yaml:2")
	assert.Contains(t, box, "The draft stays. Nothing was written.")

	assert.Equal(t, settings.MuteRemove, settingsScr(h).muteDraft().Op, "the draft stays")
	assert.Equal(t, []string{"PlanSettings"}, svc.called())
	assert.Equal(t, before, lessonsBytes(t, svc))
}

func TestSettingsMuteChangeKeepsTheOtherLines(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)

	// Change the region entry to a combined one.
	h.press("down", "down", "down", "down", "enter")
	require.NotNil(t, settingsScr(h).ed.rec)
	assert.Equal(t, "api/", settingsScr(h).ed.rec.region.Value(), "the form starts from the entry")

	// The rule field has the keys first.
	h.typeText("E9")
	h.press("tab", "end")
	h.typeText("v2")
	h.press("enter")
	assert.Contains(t, settingsBox(h.view(), "mute"), "draft: change to rule E9 · region api/v2")

	h.press("s")
	assert.Contains(t, settingsBox(h.view(), "Save .seamark/lessons.yaml"), "line 7 region api/ › rule E9 · region "+
		"api/v2")

	h.press("y")
	assert.Equal(t, strings.Replace(muteFile, "  - region: api/\n", "  - region: api/v2\n    rule: E9\n", 1),
		lessonsBytes(t, svc))

	// A change that keeps the values of the entry is no change. The
	// cursor stays on the entry after the save.
	h.press("enter")
	require.NotNil(t, settingsScr(h).ed.rec)
	assert.Equal(t, "E9", settingsScr(h).ed.rec.rule.Value())
	h.press("enter")
	assert.Zero(t, settingsScr(h).draftTotal())
	assert.Contains(t, h.m.toast.text, "keeps its values, so the draft holds no change of it")
}

func TestSettingsMuteHostileTextIsInert(t *testing.T) {
	svc := lessonsFake(t, "mute:\n  - rule: \"\\e[31mred\\e]0;owned\\a\"\n    region: \"\\e[2Jwipe/\\u202e\"\n")
	h := lessonsOpen(t, svc, "group:mutes", 120, 40)

	for _, keys := range [][]string{nil, {"enter"}, {"x", "s"}, {"end"}} {
		h.press(keys...)

		raw := h.m.View()
		assert.NotContains(t, raw, "\x1b]0;", "no title sequence from the file")
		assert.NotContains(t, raw, "\x1b[2J", "no clear-screen sequence from the file")
		assert.NotContains(t, raw, "\a")
		assert.NotContains(t, raw, "\u202e", "no direction override from the file")
	}

	assert.Contains(t, lesFlat(h.view()), "wipe/", "the entry text still shows")
}

func TestSettingsLessonsDraftFramesNameNoConfigFile(t *testing.T) {
	svc := lessonsFake(t, "threshold: 4\n")
	h := lessonsOpen(t, svc, "group:delivery", 120, 40)
	scr := settingsScr(h)

	var frames []string

	shot := func(what string) {
		frame := h.view()
		assertFrame(t, frame, 120, 40)
		assert.NotContains(t, frame, "config.yaml", "the %s of a lessons draft names config.yaml", what)
		frames = append(frames, frame)
	}

	h.press("d")
	require.Equal(t, 1, scr.draftCount(settings.GroupDelivery))
	shot("list")

	// The quit question names the file whose draft goes.
	h.press("q")
	shot("quit question")
	assert.Contains(t, settingsBox(h.view(), "Discard the unsaved drafts and quit?"),
		"Quitting discards every unsaved draft of .seamark/lessons.yaml.")
	h.press("esc")

	// The wait of the review, the review, and the wait of the save.
	read := scr.review(settings.GroupDelivery)
	shot("preparing modal")
	assert.Contains(t, settingsModalText(scr), "Seamark reads .seamark/lessons.yaml, applies the draft")
	h.run(read)
	shot("review")

	svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
		return settings.Result{Path: p.Path()}, errors.New("disk full")
	}

	save := scr.confirm()
	shot("saving modal")
	assert.Contains(t, settingsModalText(scr), "Seamark writes .seamark/lessons.yaml. The result appears here.")
	h.run(save)
	shot("failed result")
	assert.Contains(t, settingsBox(h.view(), "Save failed"), "Seamark did not replace the file, so it is unchanged.")

	// An unknown outcome names the file to check.
	h.press("enter")
	svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
		return settings.Result{Path: p.Path(), Outcome: settings.Unknown}, errors.New("rename: input/output error")
	}

	h.press("s", "y")
	shot("unknown result")
	assert.Contains(t, settingsBox(h.view(), "Result unknown"), "Result unknown: check .seamark/lessons.yaml.")

	// The discard question and its toast.
	h.press("enter", "x")
	shot("discard question")
	h.press("d")
	shot("discard toast")
	assert.Equal(t, "Discarded the Lesson delivery draft. .seamark/lessons.yaml is unchanged.", h.m.toast.text)

	// A save that works and its toast.
	svc.saveSettings = settings.Save
	h.press("d", "s", "y")
	shot("saved toast")
	assert.Equal(t, "Lesson settings updated: threshold.", h.m.toast.text)
	assert.Empty(t, lessonsBytes(t, svc), "the reset removed the only key")
	assert.NotEmpty(t, frames)
}

// settingsModalText returns the body of the open Settings modal as one
// line without styles.
func settingsModalText(scr *settingsScreen) string {
	return lesFlat(ansi.Strip(strings.Join(scr.flowContent(200).Body, " ")))
}

func TestSettingsLessonsSavedClosesOnlyALessonsReview(t *testing.T) {
	f := lessonsSnapFake(t, "threshold: 4\n")
	h := lessonsOpen(t, f, "group:delivery", 120, 40)
	scr := settingsScr(h)

	// A config.yaml review binds another file, so it stays.
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "enter", "s")
	require.True(t, scr.flow.showsPreview())

	reads := f.readCount("Settings")
	h.send(pinsSaved)
	assert.True(t, scr.flow.showsPreview(), "a config.yaml review stays")
	assert.Greater(t, f.readCount("Settings"), reads, "the snapshot reads lessons.yaml again")

	// A lessons review closes; its draft stays.
	h.press("esc")
	h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
	h.press("d", "s")
	require.True(t, scr.flow.showsPreview())

	h.send(pinsSaved)
	assert.False(t, scr.flow.active())
	assert.Equal(t, 1, scr.draftCount(settings.GroupDelivery))
	assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the Lesson delivery review closed. The draft "+
		"stays; press s to review it again.", h.m.toast.text)
}

// B1 deviation 13: the reload after a save fails on the read error of
// the written document. The tests reach it through the real signal. The
// real save writes the file, and then a directory takes the place of the
// file. So the real settings.Inspect of the reload cannot read it.
func TestSettingsSaveRefreshFailureReadsTheWrittenDocument(t *testing.T) {
	breakAfterSave := func(t *testing.T, svc *settingsSvc, rel string) {
		t.Helper()

		svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
			res, err := settings.Save(p)

			path := filepath.Join(svc.root, filepath.FromSlash(rel))
			require.NoError(t, os.Remove(path))
			require.NoError(t, os.Mkdir(path, 0o755))

			return res, err
		}
	}

	t.Run("config.yaml", func(t *testing.T) {
		svc := settingsFake(t, "distill:\n  write: false\n")
		breakAfterSave(t, svc, settings.File)

		h := lessonsOpen(t, svc, "group:pinwrite", 120, 40)
		h.press("enter", "down", "space", "enter", "s", "y")

		assert.True(t, strings.HasPrefix(h.m.toast.text, "Saved: .seamark/config.yaml holds the reviewed bytes. "+
			"Reading it again failed: "), h.m.toast.text)
		assert.Contains(t, h.m.toast.text, "Reading it again failed: .seamark/config.yaml: not a regular file.")
		assert.Equal(t, 1, svc.callCount("SaveSettings"), "a failed reload never saves again")
	})

	t.Run("lessons.yaml", func(t *testing.T) {
		svc := lessonsFake(t, "threshold: 4\npin_budget: 2\n")
		breakAfterSave(t, svc, pinfile.File)

		h := lessonsOpen(t, svc, "group:delivery", 120, 40)
		h.press("d", "s", "y")

		assert.True(t, strings.HasPrefix(h.m.toast.text, "Saved: .seamark/lessons.yaml holds the reviewed bytes. "+
			"Reading it again failed: "), h.m.toast.text)
		assert.Contains(t, h.m.toast.text, "Reading it again failed: .seamark/lessons.yaml: not a regular file.")
		assert.Zero(t, settingsScr(h).draftTotal(), "the save wrote the file, so the draft is gone")
		assert.Equal(t, 1, svc.callCount("SaveSettings"))
	})

	t.Run("a config.yaml save beside a broken lessons.yaml", func(t *testing.T) {
		// The reader rejects lessons.yaml, which the save did not touch.
		// That says nothing about the reload of config.yaml.
		svc := settingsFake(t, "distill:\n  write: false\n")
		lessonsWrite(t, svc.root, "hook_delivery: Always\n")

		h := lessonsOpen(t, svc, "group:pinwrite", 120, 40)
		h.press("enter", "down", "space", "enter", "s", "y")

		assert.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))
		assert.True(t, strings.HasPrefix(h.m.toast.text, "Configuration updated: distill.write."), h.m.toast.text)
		assert.Empty(t, h.m.st.checks, "the check ended without a failure")
	})
}

func TestSettingsForeignRootRoutesToTheIndexBuild(t *testing.T) {
	f := lessonsSnapFake(t, "threshold: 4\n")
	foreign := fmt.Errorf("%w (/other/repo), not this workspace (/work/repo)", services.ErrForeignRoot)
	f.settings.Lessons.Blocked = foreign

	h := lessonsOpen(t, f, "group:delivery", 120, 40)

	// The block covers the whole document, so the tail explains it
	// once, with the route of the index build.
	frame := h.view()
	list := settingsBox(frame, "Settings")
	assert.Contains(t, list, "the index records another repository root (/other/repo), not this workspace "+
		"(/work/repo). "+settingsForeignRootRoute)
	assert.NotContains(t, list, "by hand", "a fix by hand would edit the file of another repository")

	editor := settingsBox(frame, "threshold")
	assert.Contains(t, editor, settingsForeignRootRoute)
	assert.NotContains(t, editor, "Fix the file by hand")
	assert.Contains(t, footerLine(frame), "o index build")

	// d names the route in its refusal, and o opens the index build.
	h.press("d")
	assert.Contains(t, h.m.toast.text, settingsForeignRootRoute)
	assert.Zero(t, settingsScr(h).draftTotal())

	h.press("o")
	assert.Equal(t, destOverview, h.m.active)

	// A config.yaml group keeps its keys: no o there.
	h.press("5")
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	assert.NotContains(t, footerLine(h.view()), "o index build")
}

func TestSettingsContextHelpOfTheMuteEditorAndTheReview(t *testing.T) {
	h, _ := muteEditorOpen(t, 120, 40)

	// The mute editor captures, so ? opens its own page.
	h.press("?")
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Lesson mutes", h.m.page.title)
	assert.Contains(t, lesFlat(h.view()), "One save changes one entry")
	assert.Contains(t, strings.Join(h.m.page.text, " "), `A region with a trailing "/" matches only the regions under it.`,
		"the page names the region that a trailing slash leaves out")

	h.press("esc")
	assert.True(t, settingsScr(h).muteEditing(), "the editor gets its keys back")

	// The record form always has a focused field, so ? types.
	h.press("a", "?")
	assert.Nil(t, h.m.page)
	assert.Equal(t, "?", settingsScr(h).ed.rec.rule.Value())

	// A review has a page too.
	h.press("backspace")
	h.typeText("W1")
	h.press("enter", "s", "?")
	require.NotNil(t, h.m.page)
	assert.Equal(t, "Save review", h.m.page.title)
}

func TestSettingsIntentsOpenTheLessonsGroups(t *testing.T) {
	h := settingsOpen(t, lessonsFake(t, ""), 120, 40)

	for intent, f := range map[string]settings.Field{
		"group:delivery": settings.Threshold, "group:mutes": settings.Mutes, "group:mute": settings.Mutes,
	} {
		h.send(navigateMsg{dest: destSettings, intent: intent})
		assert.Equal(t, f, settingsScr(h).field(), "intent %s", intent)
	}

	// The Overview link of a lessons group names it.
	assert.Equal(t, "open lesson mutes settings", ovSettingsLink(settings.GroupMutes).label)
}

func TestStatsHiddenLessonsLinkToTheMutes(t *testing.T) {
	snap := statsSnapshot(statsMixedFirings(), statsSurfaced())
	snap.Lessons = &lessonstats.LessonCounts{Mined: 4, Inactive: 3, Pins: 1}

	f := lessonsFake(t, "")
	f.stats = snap

	h := newHarness(t, f, 120, 40)
	h.press("3", "s")

	assert.Contains(t, statsFlat(statsLessons(hookLessons(h).stats, snap, 56)), "3 more mined lessons are below the "+
		"threshold or muted, so no surface shows them. o opens Settings › Lesson mutes.")
	assert.Contains(t, footerLine(h.view()), "o lesson mutes")

	h.press("o")
	assert.Equal(t, destSettings, h.m.active)
	assert.Equal(t, settings.Mutes, settingsScr(h).field())

	// Without hidden lessons, o is no key of Stats.
	snap.Lessons.Inactive = 0
	f.stats = snap
	h.press("3", "r")
	assert.NotContains(t, footerLine(h.view()), "o lesson mutes")

	h.press("o")
	assert.Equal(t, destLessons, h.m.active)
}

func TestSettingsMuteDraftOfAChangedEntryIsFlagged(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)

	// The removal binds the text of the first entry.
	h.press("x")
	require.Equal(t, settings.MuteRemove, settingsScr(h).muteDraft().Op)

	// Another editor changes that entry; the reload keeps the draft.
	lessonsWrite(t, svc.root, strings.Replace(muteFile, "rule: F541\n", "rule: F542\n", 1))
	h.press("esc", "r")

	assert.Equal(t, 1, settingsScr(h).draftTotal(), "a reload never drops a mute draft")
	assert.Contains(t, settingsBox(h.view(), "mute"), "The entry of the draft changed since the draft started, so "+
		"a save refuses it.")

	// The review shows the refusal of the planner and writes nothing.
	h.press("s")
	assert.Contains(t, settingsBox(h.view(), "The draft is not valid"), "choose it again")
	assert.Equal(t, []string{"PlanSettings"}, svc.called())
}

// A delivery draft is redundant only when the file holds the same text.
// The reader reads a stored 0 as the default 2. So a typed 2 is a real
// change of the file, and a typed 2 over a stored 2 is none.
func TestSettingsDeliveryRedundancyReadsTheStoredText(t *testing.T) {
	type2 := func(t *testing.T, file string) (*harness, *settingsSvc) {
		t.Helper()

		svc := lessonsFake(t, file)
		h := lessonsOpen(t, svc, "group:delivery", 120, 40)
		h.press("enter", "down", "space")

		for range 10 {
			h.press("backspace")
		}

		h.typeText("2")
		h.press("enter")
		require.False(t, settingsScr(h).ed.typing)

		return h, svc
	}

	h, svc := type2(t, "threshold: 0\n")
	require.Equal(t, 1, settingsScr(h).draftTotal(), "stored 0 reads as 2, but the file holds 0")

	h.press("enter", "s")
	assert.Contains(t, settingsBox(h.view(), "Save .seamark/lessons.yaml"), "threshold 0 › 2")

	h.press("y")
	assert.Equal(t, "threshold: 2\n", lessonsBytes(t, svc))

	h, _ = type2(t, "threshold: 2\n")
	assert.Zero(t, settingsScr(h).draftTotal(), "the file holds the typed value")
}

// R-14: every new layer fits the compact and the minimum size, and the
// ASCII theme draws it with ASCII only. Each state is the most crowded
// one of its layer: notes, a notice, two clients with a finding.
func TestSettingsLessonsLayersFitEverySizeInASCII(t *testing.T) {
	inspections := []integration.Inspection{
		{
			ClientID: "claude", Name: "Claude Code",
			Capabilities: []integration.CapabilityInspection{{
				Capability: integration.CapabilityResets, Supported: true, State: integration.StatePartial,
				Detail: "a SessionStart hook without the compact matcher",
			}},
			Findings: []integration.Finding{{
				Level: integration.FindingWarning, Path: ".claude/settings.json", Reason: "trust cannot be read",
				Action: "open Claude Code and review the hooks",
			}},
		},
		{ClientID: "codex", Name: "Codex"},
	}

	states := []struct {
		name string
		keys []string
		// text is typed after the keys.
		text string
		more []string
		// want is a phrase that the frame must show: the minimum size
		// keeps the focused field with its verdict.
		want string
	}{
		{
			name: "record form", keys: []string{"group:mutes", "enter", "a"}, text: "W1",
			more: []string{"tab", "text:./api/"}, want: "Hides mined lessons with rule W1",
		},
		{
			name: "number notice", keys: []string{"group:delivery", "down", "enter", "down", "space"},
			text: "99999999999", want: "This field holds at most 10 characters",
		},
		{name: "repetition help", keys: []string{"group:delivery", "down", "down", "down", "enter"}},
		{name: "mute review", keys: []string{"group:mutes", "enter", "x", "s"}},
		{name: "one-operation question", keys: []string{"group:mutes", "enter", "x", "down", "x"}},
	}

	for _, size := range [][2]int{{60, 20}, {80, 24}} {
		for _, st := range states {
			t.Run(fmt.Sprintf("%s %dx%d", st.name, size[0], size[1]), func(t *testing.T) {
				svc := lessonsFake(t, muteFile)
				svc.integrations = inspections

				h := &harness{t: t, m: newModel(svc, ui.NewTheme(true))}
				h.run(h.m.Init())
				h.resize(size[0], size[1])
				h.press("5")

				for _, k := range append(append(append([]string{}, st.keys...), "text:"+st.text), st.more...) {
					switch {
					case strings.HasPrefix(k, "group:"):
						h.send(navigateMsg{dest: destSettings, intent: k})
					case strings.HasPrefix(k, "text:"):
						h.typeText(strings.TrimPrefix(k, "text:"))
					default:
						h.press(k)
					}
				}

				frame := h.view()
				assertFrame(t, frame, size[0], size[1])

				if st.want != "" {
					assert.Contains(t, lesFlat(strings.ReplaceAll(frame, "|", " ")), st.want)
				}

				for i, r := range frame {
					if r > 127 {
						require.Failf(t, "non-ASCII glyph", "%q at %d in\n%s", r, i, frame)
					}
				}
			})
		}
	}
}

// The tests below are the regression tests of the B3 review fixes.

// Review fix B3-BEH-1: the mute editor stays open after a save and
// captures the keys. The message of a failed reload says "r reads
// again", so r in the editor reads again, and the footer names it.
func TestSettingsMuteEditorReadsAgainAfterAFailedReload(t *testing.T) {
	svc := lessonsFake(t, muteFile)
	path := filepath.Join(svc.root, filepath.FromSlash(pinfile.File))

	svc.saveSettings = func(p *settings.Plan) (settings.Result, error) {
		res, err := settings.Save(p)

		require.NoError(t, os.Remove(path))
		require.NoError(t, os.Mkdir(path, 0o755))

		return res, err
	}

	h := lessonsOpen(t, svc, "group:mutes", 120, 40)
	h.press("enter", "x", "s", "y")

	require.True(t, settingsScr(h).muteEditing(), "the editor stays open after the save")
	require.True(t, strings.HasSuffix(h.m.toast.text, "Nothing is retried; r reads again."), h.m.toast.text)
	assert.Contains(t, footerLine(h.view()), "r read again")

	// A new draft of another group drops the saved note, and the check
	// stays. r then reads again, and the new read fails again: the
	// message repeats.
	h.press("esc")
	h.send(navigateMsg{dest: destSettings, intent: "group:pinwrite"})
	h.press("enter", "down", "space", "esc")
	require.Nil(t, settingsScr(h).saved, "a new draft drops the saved note")

	h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
	h.press("enter")
	assert.Contains(t, footerLine(h.view()), "r read again")

	reads := svc.readCount("Settings")
	h.m.toast = toastMsg{}
	h.press("r")

	assert.Greater(t, svc.readCount("Settings"), reads, "r in the mute editor reads the settings again")
	assert.Contains(t, h.m.toast.text, "Reading it again failed")
	assert.True(t, settingsScr(h).muteEditing())
	assert.Equal(t, 1, svc.callCount("SaveSettings"), "r never saves again")

	// After the fix of the file, r reads it, and the key goes away.
	require.NoError(t, os.Remove(path))
	lessonsWrite(t, svc.root, "mute:\n  - rule: F541\n")
	h.press("r")

	assert.NotContains(t, footerLine(h.view()), "r read again")
	assert.Contains(t, settingsBox(h.view(), "mute"), "1 rule F541")
}

// Review fix B3-BEH-2: the text under the table explains the block of
// the file of the cursor group only. A block of the other file marks
// the headings of its groups, in both directions.
func TestSettingsBlockOfTheOtherFileMarksItsHeadings(t *testing.T) {
	configGroups := []string{"INFERENCE", "PIN WRITING", "EVIDENCE", "INDEXING"}
	lessonsGroups := []string{"LESSON DELIVERY", "LESSON MUTES"}

	marked := func(t *testing.T, frame string, groups []string, want bool) {
		t.Helper()

		for _, g := range groups {
			assert.Equal(t, want, strings.Contains(settingsLine(frame, g), "not editable here"), "heading %s", g)
		}
	}

	t.Run("lessons.yaml with CRLF line breaks", func(t *testing.T) {
		h := settingsOpen(t, lessonsFake(t, "threshold: 4\r\nmute:\r\n  - rule: F541\r\n"), 120, 40)

		// The cursor is on agent.cli: the lessons headings say it.
		frame := h.view()
		marked(t, frame, lessonsGroups, true)
		marked(t, frame, configGroups, false)

		// On a lessons field the tail explains the block once.
		h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})

		frame = h.view()
		marked(t, frame, lessonsGroups, false)
		assert.Contains(t, settingsBox(frame, "Settings"), "The file cannot be edited here")
	})

	t.Run("a read-only config.yaml", func(t *testing.T) {
		svc := settingsFake(t, "distill:\n  write: false\n")
		require.NoError(t, os.Chmod(filepath.Join(svc.root, filepath.FromSlash(settings.File)), 0o444))
		lessonsWrite(t, svc.root, "threshold: 4\n")

		frame := lessonsOpen(t, svc, "group:delivery", 120, 40).view()
		marked(t, frame, configGroups, true)
		marked(t, frame, lessonsGroups, false)
		assert.NotContains(t, settingsBox(frame, "Settings"), "The file cannot be edited here",
			"the tail explains the file of the cursor group")
	})
}

// Review fix B3-BEH-3: a stale draft binds the text of its entry, so an
// entry that replaced it at its index is another entry. No text names
// the new entry, and x on it asks about the draft first.
func TestSettingsStaleMuteDraftNamesNoOtherEntry(t *testing.T) {
	h, svc := muteEditorOpen(t, 120, 40)
	h.press("x")
	require.Equal(t, settings.MuteRemove, settingsScr(h).muteDraft().Op)

	lessonsWrite(t, svc.root, strings.Replace(muteFile, "  - rule: F541\n", "  - rule: NEW\n", 1))
	h.send(pinsSaved)
	require.Contains(t, settingsBox(h.view(), "mute"), "1 rule NEW", "the reload shows the new entry")

	// The Draft row and the list cell never name the new entry.
	draft := settingsLine(h.view(), "Draft ")
	assert.Contains(t, draft, "remove a changed entry")
	assert.NotContains(t, draft, "NEW")

	h.press("esc")
	assert.Contains(t, settingsLine(h.view(), "› mute"), "remove a changed")

	// x on the new entry is a second operation, so the question comes.
	h.press("enter", "x")
	require.NotNil(t, settingsScr(h).prompt, "x on the new entry is not the drafted removal")

	question := settingsBox(h.view(), "Save or discard the mute draft?")
	assert.Contains(t, question, "The draft already holds one mute operation: remove a changed entry.")
	assert.NotContains(t, question, "NEW")
}

// Review fix B3-BEH-4 and B3-CR-1: the keys of a row follow the rule of
// the planner. A flow list has no "remove only" label, because its block
// refuses x too. enter changes an entry only where the planner can.
func TestSettingsMuteRowKeysFollowThePlanner(t *testing.T) {
	t.Run("a flow list", func(t *testing.T) {
		h := lessonsOpen(t, lessonsFake(t, "mute: [{rule: a}, {region: api/}]\n"), "group:mutes", 120, 40)
		h.press("enter")

		box := settingsBox(h.view(), "mute")
		assert.Contains(t, box, "the mute list uses flow style [ ]")
		assert.NotContains(t, box, "remove only", "x is refused here too")
	})

	t.Run("a block list", func(t *testing.T) {
		svc := lessonsFake(t, "mute:\n  - {rule: a}\n  -\n  - rule: ''\n    why: legacy\n")
		h := lessonsOpen(t, svc, "group:mutes", 120, 40)
		h.press("enter")

		footer := func() string { return footerLine(h.view()) }

		assert.Contains(t, settingsBox(h.view(), "mute"), "flow entry: remove only")
		assert.NotContains(t, footer(), "enter", "a flow item cannot change")
		assert.Contains(t, footer(), "x remove")

		h.press("down")
		assert.NotContains(t, footer(), "enter", "a null item cannot change")

		// An empty rule in a block mapping matches nothing, but the
		// planner can change it.
		h.press("down")
		assert.Contains(t, footer(), "enter change")

		h.press("down")
		assert.Contains(t, footer(), "enter add")
		assert.NotContains(t, footer(), "x remove")

		h.press("a")
		h.typeText("W2")
		h.press("enter")
		assert.Contains(t, footer(), "enter change")
		assert.Contains(t, footer(), "x discard draft")
	})

	t.Run("an empty rule changes and keeps its other keys", func(t *testing.T) {
		svc := lessonsFake(t, "mute:\n  - rule: ''\n    why: legacy\n")
		h := lessonsOpen(t, svc, "group:mutes", 120, 40)
		h.press("enter", "enter")
		require.NotNil(t, settingsScr(h).ed.rec, "the planner can change the entry, so the form opens")

		h.typeText("W1")
		h.press("enter", "s", "y")
		assert.Equal(t, "mute:\n  - rule: W1\n    why: legacy\n", lessonsBytes(t, svc))
	})
}

// Review fix B3-BEH-5: a stored number past the bound of the planner
// does not fit the input. The input then starts empty, so the user can
// type a valid number.
func TestSettingsNumberChoiceStartsEmptyPastTheBound(t *testing.T) {
	svc := lessonsFake(t, "threshold: 99999999999\n")
	h := lessonsOpen(t, svc, "group:delivery", 120, 40)

	assert.Contains(t, settingsBox(h.view(), "threshold"), "number 99999999999", "the radio row shows the stored value")

	h.press("enter", "down", "space")
	scr := settingsScr(h)
	require.True(t, scr.ed.typing)
	assert.Empty(t, scr.ed.input.Value())
	assert.False(t, scr.ed.input.ReadOnly())

	h.typeText("7")
	h.press("enter")
	assert.Equal(t, settings.Edit{Field: settings.Threshold, Int: 7}, scr.edits[settings.Threshold])

	// A stored value inside the bound still starts the input.
	h = lessonsOpen(t, lessonsFake(t, "threshold: 2147483647\n"), "group:delivery", 120, 40)
	h.press("enter", "down", "space")
	assert.Equal(t, "2147483647", settingsScr(h).ed.input.Value())
}
