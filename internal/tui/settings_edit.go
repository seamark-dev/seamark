package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The field editors. A radio editor changes a scalar field; the list
// editor changes index.exclude. Every change goes into the draft only.

// settingsDaysLimit bounds the typed day count. Six digits allow more
// than two thousand years, so no real window needs more.
const settingsDaysLimit = 6

// settingsEditor is the state of the open editor.
type settingsEditor struct {
	// choice is the radio cursor.
	choice int
	// item is the list editor cursor. The row after the last pattern is
	// the "add a pattern" row.
	item int

	// typing is true while the text input has the keys. target says
	// what the input edits.
	typing bool
	target settingsInputTarget
	input  lineInput
	// editIdx is the pattern that the input replaces; -1 adds one.
	editIdx int
	// inputErr explains why the typed value was not kept.
	inputErr string
}

// settingsInputTarget names the value that the text input edits.
type settingsInputTarget int

const (
	settingsInputDays settingsInputTarget = iota
	settingsInputPattern
)

// settingsChoiceKind names what a radio choice does to the draft.
type settingsChoiceKind int

const (
	settingsChoiceCustom settingsChoiceKind = iota
	settingsChoicePreset
	settingsChoiceKeepArgv
	settingsChoiceRemoveArgv
	settingsChoiceBool
	settingsChoiceWindowDefault
	settingsChoiceWindowDays
	settingsChoiceWindowUnlimited
)

// settingsChoice is one option of a radio editor.
type settingsChoice struct {
	kind  settingsChoiceKind
	label string
	note  string
	// def marks the option that applies without a key in the file.
	def bool
	// preset is the agent.cli value of a preset choice.
	preset string
	// on is the value of a boolean choice.
	on bool
}

// choices returns the radio options of a field. The list editor edits
// index.exclude, so that field has none. Without a custom command,
// agent.argv has none too, because the console does not write one.
func (s *settingsScreen) choices(f settings.Field) []settingsChoice {
	snap := s.snap()
	if snap == nil {
		return nil
	}

	st, _ := snap.Field(f)

	switch f {
	case settings.AgentCLI:
		var out []settingsChoice

		if settingsArgvSet(snap) {
			out = append(out, settingsChoice{kind: settingsChoiceCustom, label: "custom", note: "agent.argv, read-only here"})
		}

		for _, id := range snap.Presets {
			out = append(out, settingsChoice{
				kind: settingsChoicePreset, label: oneLine(id), preset: id,
				note: s.presetCommand(snap.Root, id), def: id == integration.DefaultInvocationID,
			})
		}

		return out
	case settings.AgentArgv:
		if !settingsArgvSet(snap) {
			return nil
		}

		return []settingsChoice{
			{kind: settingsChoiceKeepArgv, label: "keep", note: "the custom command stays in effect"},
			{kind: settingsChoiceRemoveArgv, label: "remove", note: "agent.cli takes effect"},
		}
	case settings.DistillWrite:
		// The notes name no action, because the apply, the retarget and
		// the prune read the same gate (D-14).
		return []settingsChoice{
			{kind: settingsChoiceBool, label: "off", note: "previews show the change only", def: true},
			{kind: settingsChoiceBool, label: "on", note: "y in a preview writes the pins", on: true},
		}
	case settings.ReviewWindow:
		return []settingsChoice{
			{kind: settingsChoiceWindowDefault, label: "default", note: oneLine(st.Default), def: true},
			{kind: settingsChoiceWindowDays, label: "days", note: "a whole number of days above 0"},
			{kind: settingsChoiceWindowUnlimited, label: "unlimited", note: "writes window_days: 0"},
		}
	case settings.IndexGenerated:
		return []settingsChoice{
			// The skip choice needs no note: the description of the field
			// says what the files are, and the default tag must fit.
			{kind: settingsChoiceBool, label: "skip", def: true},
			{kind: settingsChoiceBool, label: "include", note: "index generated files too", on: true},
		}
	}

	return nil
}

// presetCommand renders the command of a preset for display. The client
// builds it from the root without a process or a file read.
func (s *settingsScreen) presetCommand(root, id string) string {
	for _, c := range s.clients {
		if c.ID != id || c.Invocation == nil {
			continue
		}

		spec, err := c.Invocation(root)
		if err != nil {
			return ""
		}

		return settingsCommand(spec.Argv, root)
	}

	return ""
}

// settingsCommand renders a command for display. The workspace root
// reads as displayPath shows it, so the command fits a narrow pane
// better. diag redacts and sanitizes the text, because a custom command
// comes from the repository.
func settingsCommand(argv []string, root string) string {
	out := slices.Clone(argv)

	for i, a := range out {
		if a == root {
			out[i] = displayPath(root)
		}
	}

	return diag(strings.Join(out, " "))
}

// selectedChoice returns the index of the option that the draft
// selects, or -1 when no option matches, for example an unknown preset.
// A value that the reader cannot read selects nothing, because the
// default choice would claim a value that the file may not hold.
func (s *settingsScreen) selectedChoice(f settings.Field) int {
	snap := s.snap()
	if snap == nil || s.valueUnknown(f) {
		return -1
	}

	cs := s.choices(f)

	for i, c := range cs {
		switch c.kind {
		case settingsChoiceCustom:
			if s.cliChoice(snap) == "" {
				return i
			}
		case settingsChoicePreset:
			if s.cliChoice(snap) == c.preset {
				return i
			}
		case settingsChoiceKeepArgv:
			if e, ok := s.drafts[settings.AgentArgv]; !ok || !e.Reset {
				return i
			}
		case settingsChoiceRemoveArgv:
			return i
		case settingsChoiceBool:
			if s.boolChoice(snap, f) == c.on {
				return i
			}
		case settingsChoiceWindowDefault, settingsChoiceWindowDays, settingsChoiceWindowUnlimited:
			if w, _ := s.windowChoice(snap); w == settingsWindowOf(c.kind) {
				return i
			}
		}
	}

	return -1
}

// settingsWindowOf returns the review window that a radio choice
// selects.
func settingsWindowOf(k settingsChoiceKind) settingsWindow {
	switch k {
	case settingsChoiceWindowDays:
		return settingsWindowDays
	case settingsChoiceWindowUnlimited:
		return settingsWindowUnlimited
	default:
		return settingsWindowDefault
	}
}

// currentChoice returns the radio cursor for a newly opened editor: the
// selected option, else the first one.
func (s *settingsScreen) currentChoice(f settings.Field) int {
	return max(0, s.selectedChoice(f))
}

// editable returns a refusal when the settings package cannot edit the
// group of f, for example because the file holds an alias there. A
// draft that no save can write would only mislead.
func (s *settingsScreen) editable(f settings.Field) tea.Cmd {
	snap := s.snap()
	if snap == nil {
		return nil
	}

	if err := snap.Editable(f.Group()); err != nil {
		return toast(ToneAttention, settingsBlockSentence(settingsGroupTitle(f.Group())+" cannot be edited here", err))
	}

	return nil
}

// blocked reports a selected field whose group the settings package
// cannot edit. The editor then shows the values but offers no edit key.
func (s *settingsScreen) blocked() bool {
	snap := s.snap()

	return snap != nil && snap.Editable(s.field().Group()) != nil
}

// editorKey handles the keys of the open editor.
func (s *settingsScreen) editorKey(msg tea.KeyMsg) tea.Cmd {
	f := s.field()

	switch {
	case key.Matches(msg, nkeys.PageUp, nkeys.PageDown):
		s.detail.key(msg)

		return nil
	case key.Matches(msg, nkeys.Back, nkeys.Next):
		s.closeEditor()

		return nil
	}

	if f == settings.IndexExclude {
		return s.listEditorKey(msg)
	}

	cs := s.choices(f)

	switch {
	case key.Matches(msg, nkeys.Enter):
		s.closeEditor()
	case len(cs) == 0:
	case key.Matches(msg, nkeys.Up):
		s.ed.choice = max(0, s.ed.choice-1)
		s.follow = true
	case key.Matches(msg, nkeys.Down):
		s.ed.choice = min(len(cs)-1, s.ed.choice+1)
		s.follow = true
	case key.Matches(msg, nkeys.Top):
		s.ed.choice = 0
		s.follow = true
	case key.Matches(msg, nkeys.Bottom):
		s.ed.choice = len(cs) - 1
		s.follow = true
	case key.Matches(msg, nkeys.Toggle):
		return s.pick(f, cs[max(0, min(s.ed.choice, len(cs)-1))])
	}

	return nil
}

// editorKeys returns the footer keys of the open editor. A group that
// cannot be edited keeps only the keys that move and close, because
// every edit key would only repeat the refusal.
func (s *settingsScreen) editorKeys() []key.Binding {
	move := withHelp(nkeys.Down, "↑↓", "move")

	if s.field() == settings.IndexExclude {
		if s.blocked() {
			return []key.Binding{move, withHelp(nkeys.Back, "esc", "done")}
		}

		return []key.Binding{
			move, withHelp(nkeys.Enter, "enter", "edit"), settingsKeys.Add,
			settingsKeys.Remove, settingsKeys.MoveUp, withHelp(nkeys.Back, "esc", "done"),
		}
	}

	if len(s.choices(s.field())) == 0 {
		return []key.Binding{withHelp(nkeys.Back, "esc", "done")}
	}

	if s.blocked() {
		return []key.Binding{move, withHelp(nkeys.Enter, "enter", "done")}
	}

	return []key.Binding{
		withHelp(nkeys.Down, "↑↓", "choose"), withHelp(nkeys.Toggle, "space", "select"),
		withHelp(nkeys.Enter, "enter", "done"),
	}
}

// pick applies a radio choice to the draft.
func (s *settingsScreen) pick(f settings.Field, c settingsChoice) tea.Cmd {
	if refusal := s.editable(f); refusal != nil {
		return refusal
	}

	snap := s.snap()

	switch c.kind {
	case settingsChoiceCustom:
		// The custom command stays, so a preset in the draft would stay
		// without effect.
		s.saved = nil
		delete(s.drafts, settings.AgentCLI)
		delete(s.drafts, settings.AgentArgv)
	case settingsChoicePreset:
		if settingsArgvSet(snap) {
			// The preset takes effect only without the custom command, so
			// the draft removes it, and the preview says so (spec R-12).
			s.saved = nil
			s.drafts[settings.AgentCLI] = settings.Edit{Field: settings.AgentCLI, String: c.preset}
			s.drafts[settings.AgentArgv] = settings.Edit{Field: settings.AgentArgv, Reset: true}

			return nil
		}

		s.setEdit(settings.Edit{Field: settings.AgentCLI, String: c.preset})
	case settingsChoiceKeepArgv:
		s.saved = nil
		delete(s.drafts, settings.AgentArgv)

		if e, ok := s.drafts[settings.AgentCLI]; ok && !e.Reset {
			delete(s.drafts, settings.AgentCLI)
		}
	case settingsChoiceRemoveArgv:
		s.setEdit(settings.Edit{Field: settings.AgentArgv, Reset: true})
	case settingsChoiceBool:
		s.setEdit(settings.Edit{Field: f, Bool: c.on})
	case settingsChoiceWindowDefault:
		s.setEdit(settings.Edit{Field: settings.ReviewWindow, Reset: true})
	case settingsChoiceWindowDays:
		_, days := s.windowChoice(snap)
		s.startTyping(settingsInputDays, -1, "")

		if days > 0 {
			s.ed.input.set(strconv.Itoa(days))
		}
	case settingsChoiceWindowUnlimited:
		s.setEdit(settings.Edit{Field: settings.ReviewWindow, Unlimited: true})
	}

	return nil
}

// startTyping opens the text input for a day count or a pattern.
func (s *settingsScreen) startTyping(target settingsInputTarget, idx int, value string) {
	s.ed.typing = true
	s.ed.target = target
	s.ed.editIdx = idx
	s.ed.inputErr = ""
	s.follow = true

	switch target {
	case settingsInputDays:
		s.ed.input = lineInput{
			placeholder: "days, for example 180", limit: settingsDaysLimit,
			accept: func(r rune) bool { return r >= '0' && r <= '9' },
		}
	default:
		// The input drops control characters by itself, and the reader
		// rejects them too. A space can be part of a real file name.
		s.ed.input = lineInput{placeholder: "pattern, for example *.pb.go"}
	}

	s.ed.input.set(value)
}

// typingKey handles the keys of the text input. enter keeps a valid
// value; esc drops the typed text and keeps the draft as it was.
func (s *settingsScreen) typingKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Back):
		s.ed.typing = false
		s.ed.inputErr = ""

		return nil
	case key.Matches(msg, nkeys.Enter):
		if s.ed.target == settingsInputDays {
			return s.keepDays()
		}

		return s.keepPattern()
	}

	s.ed.input.key(msg)
	s.ed.inputErr = ""

	return nil
}

// keepDays records a typed day count. The reader reads 0 as unlimited,
// so a days choice must be above 0; unlimited is its own choice.
func (s *settingsScreen) keepDays() tea.Cmd {
	n, err := strconv.Atoi(s.ed.input.value())
	if err != nil || n <= 0 {
		s.ed.inputErr = "Enter a whole number of days above 0."

		return nil
	}

	s.ed.typing = false
	s.setEdit(settings.Edit{Field: settings.ReviewWindow, Days: n})

	return nil
}

// listEditorKey handles the keys of the exclude list editor.
func (s *settingsScreen) listEditorKey(msg tea.KeyMsg) tea.Cmd {
	snap := s.snap()
	list := s.excludeList(snap)
	n := len(list)

	// The row after the last pattern adds one. A blocked list has no add
	// row, so the cursor stops at the last pattern.
	last := n
	if s.blocked() {
		last = max(0, n-1)
	}

	s.ed.item = max(0, min(s.ed.item, last))

	switch {
	case key.Matches(msg, nkeys.Up):
		s.ed.item = max(0, s.ed.item-1)
		s.follow = true

		return nil
	case key.Matches(msg, nkeys.Down):
		s.ed.item = min(last, s.ed.item+1)
		s.follow = true

		return nil
	case key.Matches(msg, nkeys.Top):
		s.ed.item = 0
		s.follow = true

		return nil
	case key.Matches(msg, nkeys.Bottom):
		s.ed.item = last
		s.follow = true

		return nil
	}

	if refusal := s.editable(settings.IndexExclude); refusal != nil {
		if key.Matches(msg, nkeys.Enter, settingsKeys.Add, settingsKeys.Remove, settingsKeys.MoveUp, settingsKeys.MoveDown) {
			return refusal
		}

		return nil
	}

	switch {
	case key.Matches(msg, settingsKeys.Add), key.Matches(msg, nkeys.Enter) && s.ed.item == n:
		s.ed.item = n
		s.startTyping(settingsInputPattern, -1, "")
	case key.Matches(msg, nkeys.Enter):
		s.startTyping(settingsInputPattern, s.ed.item, list[s.ed.item])
	case key.Matches(msg, settingsKeys.Remove) && s.ed.item < n:
		s.setExclude(slices.Delete(list, s.ed.item, s.ed.item+1))
		s.ed.item = min(s.ed.item, max(0, n-1))
	case key.Matches(msg, settingsKeys.MoveUp) && s.ed.item > 0 && s.ed.item < n:
		list[s.ed.item-1], list[s.ed.item] = list[s.ed.item], list[s.ed.item-1]
		s.setExclude(list)
		s.ed.item--
		s.follow = true
	case key.Matches(msg, settingsKeys.MoveDown) && s.ed.item < n-1:
		list[s.ed.item+1], list[s.ed.item] = list[s.ed.item], list[s.ed.item+1]
		s.setExclude(list)
		s.ed.item++
		s.follow = true
	}

	return nil
}

// keepPattern records a typed pattern. It checks the pattern with the
// rule of the index reader, so the list never holds a pattern that the
// next build would reject. An empty value keeps the list as it was.
func (s *settingsScreen) keepPattern() tea.Cmd {
	p := strings.TrimSpace(s.ed.input.value())
	if p == "" {
		s.ed.typing = false

		return nil
	}

	if err := index.CheckExclude(p); err != nil {
		s.ed.inputErr = oneLine(p) + " is not valid: " + errText(err)

		return nil
	}

	list := s.excludeList(s.snap())

	for i, q := range list {
		if q == p && i != s.ed.editIdx {
			s.ed.inputErr = oneLine(p) + " is already in the list."

			return nil
		}
	}

	if s.ed.editIdx >= 0 && s.ed.editIdx < len(list) {
		list[s.ed.editIdx] = p
		s.ed.item = s.ed.editIdx
	} else {
		list = append(list, p)
		s.ed.item = len(list) - 1
	}

	s.ed.typing = false
	s.setExclude(list)

	return nil
}

// resetField drafts the return of f to its default: the removal of its
// key. For agent.argv that is the removal of the custom command. When
// the file has no key, the field only leaves the draft, because setEdit
// drops a reset of an absent key.
func (s *settingsScreen) resetField(f settings.Field) tea.Cmd {
	if refusal := s.editable(f); refusal != nil {
		return refusal
	}

	st, ok := s.fieldState(f)
	if !ok {
		return nil
	}

	if !s.resettable(f) {
		// A custom command overrides the agent.cli default, so the toast
		// names the command that stays (spec R-12).
		then := "the default " + oneLine(st.Default) + " applies"
		if f == settings.AgentCLI && s.argvStays() {
			then = "agent.argv stays in effect"
		}

		return toast(ToneNeutral, string(f)+" has no key in "+settingsFile+"; "+then+".")
	}

	s.setEdit(settings.Edit{Field: f, Reset: true})

	return nil
}
