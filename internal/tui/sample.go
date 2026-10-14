package tui

import (
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the selection sample (contracts §4.4, spec R-10). The
// pin review, the mute review and the delivery review open it with s.
// The sample runs the lesson selection of one surface for explicit
// paths twice: under the saved lessons.yaml text and under the
// candidate of that review. It shows the two selected lists side by
// side, and a marker says what the other side does with each lesson.
// The rendering lives in sample_view.go.
//
// The sample takes the two texts of the review that opens it, and no
// other text. So it never combines the unsaved drafts of two reviews.
// services.Sample reads the index read-only and writes nothing: no
// firing record, no delivery state, and no lock.
//
// Every marker comes from the typed lists of report.Selection on both
// sides. A lesson that no list of the other side accounts for gets no
// marker. change_set keeps one pin per identity, so a later pin of that
// identity is in no list there (A3-step2 R1). Its absence proves
// nothing, so the sample never calls it removed or added.
//
// The sample captures every key while it is open, as its review does.
// esc returns to that review. A save of lessons.yaml closes the review,
// and the sample goes with it, because both read the old bytes (P4-12).

// sampleKeys are the keys of the sample, and the s of the reviews that
// open it. The movement keys come from ui.Nav.
var sampleKeys = struct {
	Open, Add, Remove key.Binding
}{
	Open:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sample")),
	Add:    key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
	Remove: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
}

// sample is one open selection sample. The review that opens it owns
// it and stays as it was under it.
type sample struct {
	st *state

	// req holds the two lessons.yaml texts of the review, the surface and
	// the paths. The texts never change while the sample is open. The
	// surface and the paths follow the rows.
	req services.SampleRequest
	// modeOnly is true when the review changes only hook_delivery. The
	// mode changes repetition, which a sample does not simulate, so the
	// sample says so.
	modeOnly bool

	// row is the cursor row: a path, or the add row after the last path.
	row int
	// editing is true while the path input has the keys. editAt is the
	// path that the input replaces, or -1 for a new path.
	editing bool
	editAt  int
	input   ui.LineInput

	// seq drops the result of a read that a newer read replaced.
	seq generation
	// reading is true while a read runs.
	reading bool
	// view is the last result, and asked holds the request that view
	// answers. view is nil before the first result.
	view  *services.SampleView
	asked services.SampleRequest

	// scroll is the position of the modal body. follow asks the next
	// render to show the cursor row, because only the modal knows the
	// height of its window.
	scroll ui.ScrollState
	follow bool
}

// sampledMsg carries one sample read. owner names the sample that asked:
// a holder gives the result only to its open sample. seq drops the
// result of a read that a newer read replaced.
type sampledMsg struct {
	owner *sample
	seq   int
	req   services.SampleRequest
	view  services.SampleView
}

// newSample opens the sample of one review. before and after are the
// lessons.yaml texts of the review. The sample starts on the hook
// surface with no path, so its first read states why it cannot select
// yet; the engine owns that text.
func newSample(st *state, before, after []byte, modeOnly bool) (*sample, tea.Cmd) {
	s := &sample{st: st, req: services.SampleRequest{Before: before, After: after}, modeOnly: modeOnly, editAt: -1}
	cmd := s.read()

	return s, cmd
}

// read starts a read of the current surface and paths. The read runs in
// a command, because it opens the index. Each read takes a new ticket,
// so the result of an older read never replaces a newer one.
func (s *sample) read() tea.Cmd {
	seq := s.seq.start()
	s.reading = true

	req := s.req
	req.Paths = slices.Clone(s.req.Paths)

	var svc services.Reader = s.st.svc

	return func() tea.Msg {
		return sampledMsg{owner: s, seq: seq, req: req, view: svc.Sample(req)}
	}
}

// done shows the result of a read, unless a newer read replaced it.
func (s *sample) done(msg sampledMsg) {
	if !s.seq.current(msg.seq) {
		return
	}

	s.reading = false
	s.view, s.asked = &msg.view, msg.req
}

// reload reads again after a load of the index, the ledger or the pin
// file (contracts §4.4). Each of these loads can change the evidence or
// the saved side. A failed load keeps the earlier snapshot, so it
// changes nothing here.
func (s *sample) reload(msg loadedMsg) tea.Cmd {
	if msg.err != nil {
		return nil
	}

	switch msg.kind {
	case secIndex, secLedger, secPins:
		return s.read()
	}

	return nil
}

// answers reports whether the shown result answers the current surface
// and paths. A result for other rows must not show under these rows.
func (s *sample) answers() bool {
	return s.view != nil && s.asked.Surface == s.req.Surface && slices.Equal(s.asked.Paths, s.req.Paths)
}

// key handles one key and reports whether the sample closed. Every key
// stays in the sample, so the review under it gets none.
func (s *sample) key(msg tea.KeyMsg) (cmd tea.Cmd, closed bool) {
	if s.editing {
		return s.fieldKey(msg), false
	}

	n := len(s.req.Paths)

	switch {
	case key.Matches(msg, ui.Nav.Back):
		return nil, true
	case key.Matches(msg, ui.Nav.Up):
		s.moveTo(s.row - 1)
	case key.Matches(msg, ui.Nav.Down):
		s.moveTo(s.row + 1)
	case key.Matches(msg, ui.Nav.Enter):
		s.edit(s.row)
	case key.Matches(msg, sampleKeys.Add):
		s.edit(n)
	case key.Matches(msg, sampleKeys.Remove) && s.row < n:
		s.req.Paths = slices.Delete(s.req.Paths, s.row, s.row+1)
		s.moveTo(s.row)

		return s.read(), false
	case key.Matches(msg, ui.Nav.Toggle):
		if s.req.Surface == services.SampleHook {
			s.req.Surface = services.SampleChangeSet
		} else {
			s.req.Surface = services.SampleHook
		}

		return s.read(), false
	default:
		s.scroll.Key(msg)
	}

	return nil, false
}

// fieldKey gives a key to the path input. enter and esc keep the typed
// path with the rule of the region input of the pin form
// (keepRowValue). An empty path removes its row. A changed list reads
// again.
func (s *sample) fieldKey(msg tea.KeyMsg) tea.Cmd {
	if !key.Matches(msg, ui.Nav.Enter, ui.Nav.Back) {
		s.input.Key(msg)

		return nil
	}

	before := slices.Clone(s.req.Paths)
	s.editing = false

	var added int

	s.req.Paths, added = keepRowValue(s.req.Paths, s.editAt, s.input.Value())
	if added >= 0 {
		s.row = added
	}

	s.moveTo(s.row)

	if slices.Equal(before, s.req.Paths) {
		return nil
	}

	return s.read()
}

// edit opens the path input on row i: the path of the row, or a new path
// on the add row.
func (s *sample) edit(i int) {
	s.input = pathInput()
	s.editing, s.editAt = true, -1
	s.moveTo(len(s.req.Paths))

	if i < len(s.req.Paths) {
		s.input.Set(s.req.Paths[i])
		s.editAt = i
		s.moveTo(i)
	}
}

// moveTo puts the cursor on row, inside the rows, and asks the next
// render to show it.
func (s *sample) moveTo(row int) {
	s.row = max(0, min(row, len(s.req.Paths)))
	s.follow = true
}

// keys returns the keys of the path input while it has the keys, else
// the keys of the rows. The footer and the help page show every key.
// modal selects the row of the modal, which leaves out ↑↓, as
// pinReview.previewActions shortens its row. The labels stay short,
// and esc comes before space. So a 60-column modal still shows esc, the
// one key that leaves the sample.
func (s *sample) keys(modal bool) []key.Binding {
	if s.editing {
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "done"), ui.WithHelp(ui.Nav.Back, "esc", "done")}
	}

	enter := ui.WithHelp(ui.Nav.Enter, "enter", "edit")
	if s.row == len(s.req.Paths) {
		enter = ui.WithHelp(ui.Nav.Enter, "enter", "add")
	}

	remove := sampleKeys.Remove
	remove.SetEnabled(s.row < len(s.req.Paths))

	var out []key.Binding
	if !modal {
		out = append(out, ui.WithHelp(ui.Nav.Down, "↑↓", "rows"))
	}

	return append(out, enter, sampleKeys.Add, remove,
		ui.WithHelp(ui.Nav.Back, "esc", "back"), ui.WithHelp(ui.Nav.Toggle, "space", "surface"))
}

// help implements the context help of the sample for its holder. The
// path input takes ? as a typed character, so it has no page.
func (s *sample) help() (helpPage, bool) {
	if s.editing {
		return helpPage{}, false
	}

	return helpPage{title: "Selection sample", text: sampleHelp, keys: s.keys(false)}, true
}

// sampleOpenHelp tells what s does in a review that opens a sample. The
// pin review help and the "Save review" help of Settings share it, so
// the two pages say the same.
const sampleOpenHelp = "s opens a selection sample: the lessons that the saved file and this review select for " +
	"paths that you add. It writes nothing."

// sampleHelp holds the paragraphs of the context help.
var sampleHelp = []string{
	"The sample runs the lesson selection of one surface for the paths that you add: once under the saved " +
		"configuration and once under this review. One read of the index serves both sides, and the sample " +
		"writes nothing.",
	"a adds a path, enter edits the path under the cursor, and x removes it. Hook selection is one injection of " +
		"the edit hook. Change-set selection is one change_set answer or one check advisory. space switches the " +
		"surface. pgup and pgdn scroll the result.",
	"A marker compares the two sides: kept, kept with changed wording, added, or removed. A lesson without a " +
		"marker is in no list of the other side, for example a repeated pin of change_set.",
	"A held-back count includes restatements, so it is not a count of lessons over a budget. esc returns to the " +
		"review.",
}

// sampleMark is the marker of a selected lesson: what the other side
// does with it.
type sampleMark int

const (
	// sampleMarkNone means that no typed list of the other side shows
	// what it does with the lesson.
	sampleMarkNone sampleMark = iota
	sampleKept
	// sampleReworded is a pin that the other side keeps at the same key
	// and position, with other words.
	sampleReworded
	sampleAdded
	sampleRemoved
)

// String returns the marker as text, so it reads without color. The
// words are the words of a region delta (widgets.go).
func (m sampleMark) String() string {
	switch m {
	case sampleKept:
		return deltaKept
	case sampleReworded:
		return deltaKept + ", wording changed"
	case sampleAdded:
		return deltaAdded
	case sampleRemoved:
		return deltaRemoved
	}

	return ""
}

// sampleRow is one selected lesson of one side, with its marker.
type sampleRow struct {
	// pin is the pin of a pin row; nil for a mined lesson.
	pin *reviews.PinRule
	// lesson is the lesson of a mined row.
	lesson domain.Lesson
	mark   sampleMark
	// why is the reason that a typed list of the other side gives for a
	// lesson that the other side does not select, or "".
	why string
}

// sampleSlot pairs a pin with a pin of the other side: its key and its
// position among the pins of that key (contracts §4.4). The hook list
// can hold one key twice, so the key alone does not pair.
type sampleSlot struct {
	key reviews.PinKey
	ord int
}

// samplePinKey returns the canonical key of a pin. reviews.NewPinKey is
// the one owner of that key.
func samplePinKey(p reviews.PinRule) reviews.PinKey {
	return reviews.NewPinKey(p.Rule, p.Region, p.Regions)
}

// sampleSlots returns the slot of each pin, in list order.
func sampleSlots(pins []reviews.SurfacedPin) []sampleSlot {
	seen := map[reviews.PinKey]int{}
	out := make([]sampleSlot, len(pins))

	for i, sp := range pins {
		k := samplePinKey(sp.Pin)
		out[i] = sampleSlot{key: k, ord: seen[k]}
		seen[k]++
	}

	return out
}

// sampleSides returns the rows of the saved side and of the review side
// of v. A lesson of the saved side that the review does not select is
// removed. A lesson of the review side that the saved side does not
// select is added.
func sampleSides(v *services.SampleView) (saved, review []sampleRow) {
	saved = sampleSide(v.Current, v.Candidate, v.CandidateThreshold, v.Surface, sampleRemoved)
	review = sampleSide(v.Candidate, v.Current, v.CurrentThreshold, v.Surface, sampleAdded)

	return saved, review
}

// sampleSide returns the rows of the selection own, marked against the
// selection other. gone is the marker of a lesson that other does not
// select. threshold is the threshold of the configuration of other, for
// the words of an exclusion.
func sampleSide(own, other report.Selection, threshold int, surface services.SampleSurface, gone sampleMark) []sampleRow {
	partners := map[sampleSlot]reviews.SurfacedPin{}
	for i, slot := range sampleSlots(other.Pins) {
		partners[slot] = other.Pins[i]
	}

	rows := make([]sampleRow, 0, len(own.Pins)+len(own.Mined))

	for i, slot := range sampleSlots(own.Pins) {
		sp := own.Pins[i]
		row := sampleRow{pin: &own.Pins[i].Pin}

		// The rendered symptom holds the rule and the note, so it is the
		// wording that an agent reads.
		if q, ok := partners[slot]; ok {
			row.mark = sampleKept
			if q.Lesson().Symptom != sp.Lesson().Symptom {
				row.mark = sampleReworded
			}
		} else {
			row.mark, row.why = samplePinFate(sp, other, surface, gone)
		}

		rows = append(rows, row)
	}

	selected := map[string]bool{}
	for _, l := range other.Mined {
		selected[l.ClusterKey] = true
	}

	for _, l := range own.Mined {
		row := sampleRow{lesson: l, mark: sampleKept}
		if !selected[l.ClusterKey] {
			row.mark, row.why = sampleMinedFate(l, other, threshold, surface, gone)
		}

		rows = append(rows, row)
	}

	return rows
}

// samplePinFate returns the marker of a pin that the other side does not
// select, from the typed lists of the other side. A held-back pin of the
// other side gives the reason (sampleHeldWhy). The identity holds the
// note, so it finds the entry itself first. The pin key then finds a
// reworded entry. The hook puts every applicable pin in one list. So a
// pin in no list of the hook is not in the other configuration for
// these paths. change_set drops a later pin of an identity that it
// lists. So a pin whose identity the other side lists can be such a
// repeat, and it gets no marker.
func samplePinFate(sp reviews.SurfacedPin, other report.Selection, surface services.SampleSurface,
	gone sampleMark,
) (mark sampleMark, why string) {
	id := reviews.PinIdentity(sp.Pin)
	sameID := func(p reviews.SurfacedPin) bool { return reviews.PinIdentity(p.Pin) == id }

	pk := samplePinKey(sp.Pin)
	sameKey := func(p reviews.SurfacedPin) bool { return samplePinKey(p.Pin) == pk }

	for _, match := range []func(reviews.SurfacedPin) bool{sameID, sameKey} {
		if why, held := sampleHeldWhy(other, surface, match); held {
			return gone, why
		}
	}

	if surface == services.SampleHook {
		return gone, ""
	}

	for _, list := range [][]reviews.SurfacedPin{other.Pins, other.Restated, other.OverTotal} {
		if slices.ContainsFunc(list, sameID) {
			return sampleMarkNone, ""
		}
	}

	return gone, ""
}

// sampleHeldWhy returns the reason of the one held-back pin of other
// that match accepts. held is true when any held-back pin matches. The
// hook can hold one key, or one repeated entry, in two lists. The lists
// then do not say which pin is this entry, so why stays "".
func sampleHeldWhy(other report.Selection, surface services.SampleSurface,
	match func(reviews.SurfacedPin) bool,
) (why string, held bool) {
	lists := []struct {
		pins []reviews.SurfacedPin
		why  string
	}{
		{other.Restated, "restates another pin"},
		{other.OverPinCap, "above the pin cap"},
		{other.OverTotal, sampleOverWords(surface)},
	}

	n := 0

	for _, l := range lists {
		for _, p := range l.pins {
			if match(p) {
				n++
				why = l.why
			}
		}
	}

	if n != 1 {
		why = ""
	}

	return why, n > 0
}

// sampleMinedFate returns the marker of a mined lesson that the other
// side does not select: above the total, or excluded with every reason.
// Both sides fold one evidence read, so a list of the other side holds
// the lesson. A lesson in none of them gets no marker, because no typed
// data names its fate.
func sampleMinedFate(l domain.Lesson, other report.Selection, threshold int, surface services.SampleSurface,
	gone sampleMark,
) (mark sampleMark, why string) {
	same := func(m domain.Lesson) bool { return m.ClusterKey == l.ClusterKey }

	if slices.ContainsFunc(other.MinedOverTotal, same) {
		return gone, sampleOverWords(surface)
	}

	if i := slices.IndexFunc(other.Excluded, func(ex report.Exclusion) bool { return same(ex.Lesson) }); i >= 0 {
		return gone, sampleExclusionWords(other.Excluded[i], threshold)
	}

	return sampleMarkNone, ""
}

// sampleOverWords names the cap of a lesson above the total of the
// surface: the total cap of the hook, or the change_set budget.
func sampleOverWords(surface services.SampleSurface) string {
	if surface == services.SampleChangeSet {
		return "above the change_set budget"
	}

	return "above the total cap"
}

// sampleExclusionWords names every reason of an exclusion from its typed
// fields, in the order of contracts §4.4. threshold is the threshold of
// the configuration that excludes the lesson. settings.MuteText owns the
// words of a mute entry, and it sanitizes the file text.
func sampleExclusionWords(ex report.Exclusion, threshold int) string {
	var why []string

	for _, m := range ex.Mutes {
		why = append(why, "muted by "+settings.MuteText(m.Rule, m.Region))
	}

	if ex.BelowThreshold {
		why = append(why, "below threshold "+strconv.Itoa(threshold))
	}

	if ex.Covered {
		why = append(why, "covered by an applied pin")
	}

	return strings.Join(why, "; ")
}

// sampleHeldText labels the held-back count of one side. The count
// includes restatements, so the words never say "over budget"
// (contracts §4.4). The hook counts pins only; change_set counts pins
// and mined lessons (report.Selection.HeldBack).
func sampleHeldText(surface services.SampleSurface, n int) string {
	if surface == services.SampleChangeSet {
		return "lessons trimmed (restatements and the change_set budget): " + strconv.Itoa(n)
	}

	return "pins held back (restatements and caps): " + strconv.Itoa(n)
}

// settingsModeOnly reports a settings plan whose every edit changes
// hook_delivery. The plan names its edits, so the sample needs no
// comparison of two configurations.
func settingsModeOnly(p *settings.Plan) bool {
	return len(p.Edits) > 0 && !slices.ContainsFunc(p.Edits, func(e settings.Edit) bool {
		return e.Field != settings.HookDelivery
	})
}
