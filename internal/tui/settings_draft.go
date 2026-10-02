package tui

import (
	"slices"
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The draft model of the Settings destination. A draft holds one
// settings.Edit per changed field. The screen drops an edit that changes
// nothing, so the draft marker shows only real changes. A value that a
// draft shows comes from the edit or from the snapshot; the screen never
// computes a default of its own.

// snap returns the current settings snapshot, or nil before the first
// good read.
func (s *settingsScreen) snap() *settings.Snapshot {
	if !s.st.settings.loaded {
		return nil
	}

	return s.st.settings.value
}

// fieldState returns the snapshot state of f.
func (s *settingsScreen) fieldState(f settings.Field) (settings.FieldState, bool) {
	snap := s.snap()
	if snap == nil {
		return settings.FieldState{}, false
	}

	return snap.Field(f)
}

// settingsArgvSet reports a custom command in the snapshot. A custom
// command overrides the agent.cli preset.
func settingsArgvSet(snap *settings.Snapshot) bool {
	return snap != nil && snap.Agent != nil && len(snap.Agent.Agent.Argv) > 0
}

// settingsDecoded reports whether the reader that owns f decoded its
// section. Without the section the saved value is unknown, not the
// default.
func settingsDecoded(snap *settings.Snapshot, f settings.Field) bool {
	switch f.Reader() {
	case settings.ReaderAgent:
		return snap.Agent != nil
	case settings.ReaderDistill:
		return snap.Distill != nil
	default:
		return snap.Index != nil
	}
}

// valueUnknown reports a field whose editor cannot mark a choice: the
// draft does not change the field, and its reader rejects the file.
func (s *settingsScreen) valueUnknown(f settings.Field) bool {
	snap := s.snap()
	if snap == nil {
		return true
	}

	if _, drafted := s.drafts[f]; drafted {
		return false
	}

	return !settingsDecoded(snap, f)
}

// located reports whether the stored state of st says where the key
// is. A key that the file holds is always located. An absent key is
// certain only while the group can be edited, because a file that the
// settings package cannot parse, or a section behind an alias, hides
// its keys.
func (s *settingsScreen) located(st settings.FieldState) bool {
	return st.Stored.Present || s.snap().Editable(st.Field.Group()) == nil
}

// settingsCLI returns the preset that an agent.cli value selects: an
// empty value selects the registry default.
func settingsCLI(v string) string {
	if v == "" {
		return integration.DefaultInvocationID
	}

	return v
}

// settingsArgvStaysText explains a change of agent.cli under a custom
// command that stays. The settings package notes the same fact in the
// activation of the plan.
const settingsArgvStaysText = "agent.argv stays in effect, so the command that runs does not change"

// settingsRedundant reports an edit that changes nothing: a reset of an
// absent key, or a value that the file holds already. A preset under a
// custom command is never redundant, because it comes with the removal
// of that command.
func settingsRedundant(snap *settings.Snapshot, e settings.Edit) bool {
	st, ok := snap.Field(e.Field)
	if !ok {
		return false
	}

	// A group that cannot be edited hides its keys, so the snapshot
	// cannot show that an edit changes nothing. A reload keeps the edit.
	if snap.Editable(e.Field.Group()) != nil {
		return false
	}

	if e.Reset {
		if e.Field == settings.AgentArgv {
			return !settingsArgvSet(snap)
		}

		return !st.Stored.Present
	}

	switch e.Field {
	case settings.AgentCLI:
		return !settingsArgvSet(snap) && snap.Agent != nil && settingsCLI(snap.Agent.Agent.CLI) == e.String
	case settings.DistillWrite:
		return snap.Distill != nil && snap.Distill.Distill.Write == e.Bool
	case settings.ReviewWindow:
		if snap.Index == nil || snap.Index.Reviews.WindowDays == nil {
			return false
		}

		stored := *snap.Index.Reviews.WindowDays
		if e.Unlimited {
			return stored == 0
		}

		return stored == e.Days
	case settings.IndexGenerated:
		return snap.Index != nil && snap.Index.Index.Generated == e.Bool
	case settings.IndexExclude:
		return snap.Index != nil && slices.Equal(snap.Index.Index.Exclude, e.List)
	}

	return false
}

// setEdit records e in the draft, or removes the field from the draft
// when e changes nothing.
func (s *settingsScreen) setEdit(e settings.Edit) {
	snap := s.snap()
	if snap == nil {
		return
	}

	// A new draft makes the note of the last save out of date.
	s.saved = nil

	if settingsRedundant(snap, e) {
		delete(s.drafts, e.Field)

		return
	}

	s.drafts[e.Field] = e
}

// normalize drops the edits that a new snapshot makes redundant, for
// example after an editor outside the console wrote the same value.
func (s *settingsScreen) normalize() {
	snap := s.snap()
	if snap == nil {
		return
	}

	for f, e := range s.drafts {
		if settingsRedundant(snap, e) {
			delete(s.drafts, f)
		}
	}
}

// groupEdits returns the draft of g in field order, the order of the
// plan and of the preview.
func (s *settingsScreen) groupEdits(g settings.Group) []settings.Edit {
	var out []settings.Edit

	for _, f := range g.Fields() {
		if e, ok := s.drafts[f]; ok {
			out = append(out, e)
		}
	}

	return out
}

// draftCount returns the number of changed fields in g.
func (s *settingsScreen) draftCount(g settings.Group) int {
	return len(s.groupEdits(g))
}

// draftTotal returns the number of changed fields in every group.
func (s *settingsScreen) draftTotal() int {
	return len(s.drafts)
}

// draftGroups returns the groups with a draft, in display order.
func (s *settingsScreen) draftGroups() []settings.Group {
	var out []settings.Group

	for _, g := range settingsGroups {
		if s.draftCount(g) > 0 {
			out = append(out, g)
		}
	}

	return out
}

// draftFields names the changed fields of g, for example
// "agent.cli, agent.argv".
func (s *settingsScreen) draftFields(g settings.Group) string {
	var names []string

	for _, e := range s.groupEdits(g) {
		names = append(names, string(e.Field))
	}

	return strings.Join(names, ", ")
}

// dropDraft removes the draft of g. The file does not change.
func (s *settingsScreen) dropDraft(g settings.Group) {
	for _, f := range g.Fields() {
		delete(s.drafts, f)
	}

	s.excludeErrs = nil

	if s.field().Group() == g {
		s.ed = settingsEditor{choice: s.currentChoice(s.field())}
	}
}

// resettable reports whether d changes the draft of f: the file holds
// the key, or the draft changes the field.
func (s *settingsScreen) resettable(f settings.Field) bool {
	snap := s.snap()
	if snap == nil {
		return false
	}

	if _, drafted := s.drafts[f]; drafted {
		return true
	}

	return !settingsRedundant(snap, settings.Edit{Field: f, Reset: true})
}

// argvStays reports that the custom command stays in effect after a
// save of the draft. The snapshot holds agent.argv, and the draft does
// not remove it. An agent.cli edit then does not change the command
// that runs (spec R-12).
func (s *settingsScreen) argvStays() bool {
	if !settingsArgvSet(s.snap()) {
		return false
	}

	e, ok := s.drafts[settings.AgentArgv]

	return !ok || !e.Reset
}

// cliChoice returns the preset that the draft selects, or "" while the
// custom command stays in effect.
func (s *settingsScreen) cliChoice(snap *settings.Snapshot) string {
	if s.argvStays() {
		return ""
	}

	if e, ok := s.drafts[settings.AgentCLI]; ok {
		if e.Reset {
			return integration.DefaultInvocationID
		}

		return e.String
	}

	if snap.Agent == nil {
		return ""
	}

	return settingsCLI(snap.Agent.Agent.CLI)
}

// settingsWindow is the choice of the review window editor.
type settingsWindow int

const (
	settingsWindowDefault settingsWindow = iota
	settingsWindowDays
	settingsWindowUnlimited
)

// windowChoice returns the review window that the draft selects, with
// the day count of a days choice. It follows the reader mapping. No
// value selects the default; zero and negative values select unlimited.
func (s *settingsScreen) windowChoice(snap *settings.Snapshot) (choice settingsWindow, days int) {
	if e, ok := s.drafts[settings.ReviewWindow]; ok {
		switch {
		case e.Reset:
			return settingsWindowDefault, 0
		case e.Unlimited:
			return settingsWindowUnlimited, 0
		default:
			return settingsWindowDays, e.Days
		}
	}

	if snap.Index == nil || snap.Index.Reviews.WindowDays == nil {
		return settingsWindowDefault, 0
	}

	if n := *snap.Index.Reviews.WindowDays; n > 0 {
		return settingsWindowDays, n
	}

	return settingsWindowUnlimited, 0
}

// boolChoice returns the value of distill.write or index.generated that
// the draft selects.
func (s *settingsScreen) boolChoice(snap *settings.Snapshot, f settings.Field) bool {
	if e, ok := s.drafts[f]; ok {
		return !e.Reset && e.Bool
	}

	switch {
	case f == settings.DistillWrite && snap.Distill != nil:
		return snap.Distill.Distill.Write
	case f == settings.IndexGenerated && snap.Index != nil:
		return snap.Index.Index.Generated
	}

	return false
}

// excludeList returns the exclude patterns that the draft selects. The
// patterns come from the file, so every caller sanitizes them for
// display.
func (s *settingsScreen) excludeList(snap *settings.Snapshot) []string {
	if e, ok := s.drafts[settings.IndexExclude]; ok {
		if e.Reset {
			return nil
		}

		return slices.Clone(e.List)
	}

	if snap.Index == nil {
		return nil
	}

	return slices.Clone(snap.Index.Index.Exclude)
}

// setExclude records a new exclude list. An empty list removes the key,
// because the reader treats an absent list and an empty list the same.
func (s *settingsScreen) setExclude(list []string) {
	s.excludeErrs = nil

	if len(list) == 0 {
		s.setEdit(settings.Edit{Field: settings.IndexExclude, Reset: true})

		return
	}

	s.setEdit(settings.Edit{Field: settings.IndexExclude, List: list})
}

// settingsDays renders a day count, for example "180 days".
func settingsDays(n int) string {
	return propPlural(n, "day", "days")
}

// settingsPatterns renders a pattern count, for example "2 patterns".
func settingsPatterns(n int) string {
	if n == 0 {
		return "none"
	}

	return propPlural(n, "pattern", "patterns")
}

// settingsGenerated renders the index.generated choice in short words.
func settingsGenerated(include bool) string {
	if include {
		return "include"
	}

	return "skip"
}

// settingsOnOff renders a gate.
func settingsOnOff(on bool) string {
	if on {
		return "on"
	}

	return "off"
}

// draftValue renders the value that an edit selects, in the short form
// of the list. A reset shows the default that then applies. A reset of
// agent.cli under a custom command that stays shows "not set", because
// agent.argv, not the default, then runs.
func (s *settingsScreen) draftValue(e settings.Edit) string {
	st, _ := s.fieldState(e.Field)

	switch e.Field {
	case settings.AgentCLI:
		switch {
		case e.Reset && s.argvStays():
			return "not set"
		case e.Reset:
			return oneLine(st.Default)
		}

		return oneLine(e.String)
	case settings.AgentArgv:
		return "removed"
	case settings.DistillWrite:
		return settingsOnOff(!e.Reset && e.Bool)
	case settings.ReviewWindow:
		switch {
		case e.Reset:
			return oneLine(st.Default)
		case e.Unlimited:
			return "unlimited"
		default:
			return settingsDays(e.Days)
		}
	case settings.IndexGenerated:
		return settingsGenerated(!e.Reset && e.Bool)
	default:
		if e.Reset {
			return "none"
		}

		return settingsPatterns(len(e.List))
	}
}

// draftDetail renders an edit for the Draft row of the detail. A reset
// names the removal of the key, because that is its exact effect.
func (s *settingsScreen) draftDetail(e settings.Edit) string {
	switch {
	case e.Field == settings.AgentArgv:
		return "remove the custom command; the agent.cli preset takes effect"
	case e.Field == settings.AgentCLI && e.Reset && s.argvStays():
		return "remove the key; " + settingsArgvStaysText
	case e.Reset:
		return "remove the key; the default " + s.draftValue(e) + " applies"
	case e.Field == settings.IndexExclude:
		return oneLine(strings.Join(e.List, ", "))
	case e.Field == settings.ReviewWindow && e.Unlimited:
		return "unlimited (window_days: 0)"
	default:
		return s.draftValue(e)
	}
}

// currentValue renders the saved value of a field in the short form of
// the list, and reports whether the reader could read it.
func (s *settingsScreen) currentValue(f settings.Field) (text string, known bool) {
	snap := s.snap()

	st, ok := snap.Field(f)
	if !ok || st.Err != nil {
		return "unknown", false
	}

	switch f {
	case settings.AgentCLI:
		if st.Source == settings.SourceShadowed {
			// The reader value also covers a key behind an alias, which
			// the stored state does not find. diag removes secret-shaped
			// values, because a wrong value such as a command can hold a
			// token. It also removes the C1 controls and bidi overrides.
			if snap.Agent != nil && snap.Agent.Agent.CLI != "" {
				return diag(snap.Agent.Agent.CLI), true
			}

			return "not set", true
		}
	case settings.AgentArgv:
		if !settingsArgvSet(snap) {
			return "not set", true
		}
	case settings.IndexGenerated:
		if snap.Index != nil {
			return settingsGenerated(snap.Index.Index.Generated), true
		}
	case settings.IndexExclude:
		if snap.Index != nil {
			return settingsPatterns(len(snap.Index.Index.Exclude)), true
		}
	}

	return oneLine(st.Effective), true
}
