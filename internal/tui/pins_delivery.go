package tui

import (
	"errors"
	"slices"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// deliveryRefreshKinds adds the independent reading when another refreshed
// source can change its configuration, evidence, links or installed coverage.
// One refresh issues only one overview request, even with several such sources.
func deliveryRefreshKinds(kinds []sectionKind) []sectionKind {
	if slices.Contains(kinds, secDelivery) {
		return kinds
	}

	for _, k := range kinds {
		switch k {
		case secPins, secLedger, secLearning, secIntegrations:
			return append(slices.Clone(kinds), secDelivery)
		}
	}

	return kinds
}

// deliveryReading pairs an installed entry with a unique current identity and
// its exact semantic version. Ledger history has no frozen version to pair.
func (p *pinsView) deliveryReading(row pinRow) (lessonreport.PinReading, bool) {
	sec := &p.st.delivery
	in := p.inspection()

	if row.pin == nil || in == nil || !sec.loaded || in.DocErr != nil || in.Doc.Unreadable() {
		return lessonreport.PinReading{}, false
	}

	c := row.pin.Check
	if !c.Known || c.Ref == "" || c.Version == "" {
		return lessonreport.PinReading{}, false
	}

	identity := reviews.PinIdentityDigest(row.pin.Entry.Ref().Key)
	entries := 0

	for _, pin := range in.Pins {
		if reviews.PinIdentityDigest(pin.Entry.Ref().Key) == identity {
			entries++
		}
	}

	var found lessonreport.PinReading
	readings := 0

	for _, reading := range sec.value.Pins {
		if reading.PinIdentity == identity {
			found = reading
			readings++
		}
	}

	if entries != 1 || readings != 1 || found.PinRef != c.Ref || found.PinVersion != c.Version {
		return lessonreport.PinReading{}, false
	}

	_, ok := lessonreport.Compact(&sec.value, found.PinVersion)

	return found, ok
}

// deliveryAvailable is also the handler gate: a hidden key cannot open an
// expansion using a stale overview or a pin inspection still being replaced.
func (p *pinsView) deliveryAvailable() bool {
	row, ok := p.cursorRow(p.rows())
	if !ok || p.st.delivery.loading || p.st.delivery.err != nil || p.st.pins.loading {
		return false
	}

	_, ok = p.deliveryReading(row)

	return ok
}

// pinDelivery owns one expansion. The binding is the installed entry, never a
// list index. The selected occurrence likewise binds a reference and version.
// Read tickets and source tickets reject results that predate a refresh.
type pinDelivery struct {
	st                     *state
	binding                pinfile.EntryRef
	identity, version, ref string
	seq                    generation
	pinsGen, overviewGen   int
	reading                section[lessonreport.Report]
	selected               deliveryOccurrence
	wanted                 deliveryOccurrence
	scroll                 ui.ScrollState
}

type deliveryOccurrence struct {
	ref, version string
}

type pinDeliveryLoadedMsg struct {
	view   *pinDelivery
	seq    int
	report lessonreport.Report
	err    error
}

// deliveryDisposedMsg detaches the expansion when the console exits. Service
// reads cannot be cancelled; detachment only discards their eventual replies.
type deliveryDisposedMsg struct{}

var deliveryKeys = struct {
	Open, Reload, Next, Previous key.Binding
}{
	Open:     key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "lesson delivery")),
	Reload:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reload")),
	Next:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next occurrence")),
	Previous: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous occurrence")),
}

func (p *pinsView) openDelivery() tea.Cmd {
	if !p.deliveryAvailable() {
		return nil
	}

	row, _ := p.cursorRow(p.rows())
	reading, _ := p.deliveryReading(row)
	v := &pinDelivery{
		st: p.st, binding: row.pin.Entry.Ref(), identity: reading.PinIdentity,
		version: reading.PinVersion, ref: reading.PinRef,
	}
	p.delivery = v

	return v.reload()
}

func (v *pinDelivery) reload() tea.Cmd {
	seq := v.seq.start()
	v.pinsGen, v.overviewGen = v.st.gens[secPins].latest(), v.st.gens[secDelivery].latest()
	v.reading.loading = true
	if v.selected.ref != "" {
		v.wanted = v.selected
	}

	v.selected = deliveryOccurrence{}
	v.scroll = ui.ScrollState{}
	ref, svc := v.ref, v.st.svc

	return func() tea.Msg {
		report, err := svc.Delivery(lessonreport.Request{PinRef: ref})

		return pinDeliveryLoadedMsg{view: v, seq: seq, report: report, err: err}
	}
}

func (v *pinDelivery) sourcesCurrent() bool {
	return v.st.gens[secPins].current(v.pinsGen) && v.st.gens[secDelivery].current(v.overviewGen)
}

func (v *pinDelivery) loaded(msg pinDeliveryLoadedMsg) {
	if !v.seq.current(msg.seq) || !v.sourcesCurrent() {
		return
	}

	err := msg.err
	if err == nil && (msg.report.Expanded == nil || msg.report.Expanded.PinRef != v.ref) {
		err = errors.New("the lesson delivery read returned no expansion for the selected pin")
	}

	if err == nil && !v.matchesReport(msg.report) {
		err = errors.New("the selected pin changed while lesson delivery was being read; press r to read again")
	}

	v.reading.set(msg.report, err, v.st.delivery.at)
	if err == nil && len(msg.report.Expanded.Occurrences) > 0 {
		if v.wanted.ref != "" {
			v.selected = v.wanted
			if _, ok := v.occurrence(); !ok {
				v.selected = deliveryOccurrence{}
			}
		} else {
			d := msg.report.Expanded.Occurrences[0]
			v.selected = deliveryOccurrence{ref: d.AssociationRef, version: d.PinVersion}
		}
	}
}

// matchesReport rejects an expansion whose independent read saw a changed or
// ambiguous current pin, even when its identity still accepts the same ref.
func (v *pinDelivery) matchesReport(report lessonreport.Report) bool {
	matches, same := 0, false

	for _, pin := range report.Pins {
		if pin.PinIdentity == v.identity {
			matches++
			same = pin.PinRef == v.ref && pin.PinVersion == v.version
		}
	}

	return matches == 1 && same
}

// rebind proves the installed entry still has the same content and semantic
// version, allowing only an unambiguous content-preserving move in the file.
func (p *pinsView) rebindDelivery() bool {
	v := p.delivery
	pin, _, ok := inspectedPin(p.inspection(), v.binding)
	if !ok {
		return false
	}

	reading, ok := p.deliveryReading(pinRow{pin: pin})
	if !ok || reading.PinIdentity != v.identity || reading.PinVersion != v.version || reading.PinRef != v.ref {
		return false
	}

	v.binding = pin.Entry.Ref()

	return true
}

func (v *pinDelivery) occurrence() (lessonreport.OccurrenceDetail, bool) {
	if !v.reading.loaded || v.reading.value.Expanded == nil {
		return lessonreport.OccurrenceDetail{}, false
	}

	var found lessonreport.OccurrenceDetail
	n := 0

	for _, d := range v.reading.value.Expanded.Occurrences {
		if d.AssociationRef == v.selected.ref && d.PinVersion == v.selected.version {
			found = d
			n++
		}
	}

	return found, n == 1 && found.AssociationRef != ""
}

func (v *pinDelivery) ready() bool {
	return v.sourcesCurrent() && !v.st.pins.loading && !v.st.delivery.loading && v.st.delivery.err == nil &&
		!v.reading.loading && v.reading.loaded && v.reading.err == nil &&
		v.reading.value.Capture.State == lessonreport.CaptureAvailable
}

func (v *pinDelivery) moveOccurrence(delta int) {
	if !v.ready() {
		return
	}

	details := v.reading.value.Expanded.Occurrences
	if len(details) == 0 {
		return
	}

	i := slices.IndexFunc(details, func(d lessonreport.OccurrenceDetail) bool {
		return d.AssociationRef == v.selected.ref && d.PinVersion == v.selected.version
	})
	i = (max(0, i) + delta + len(details)) % len(details)
	v.selected = deliveryOccurrence{ref: details[i].AssociationRef, version: details[i].PinVersion}
	v.scroll = ui.ScrollState{}
}

func (p *pinsView) closeDelivery() {
	if p.delivery != nil {
		p.delivery.seq.cancel()
		p.delivery = nil
	}
}

func (p *pinsView) deliveryKey(msg tea.KeyMsg) tea.Cmd {
	v := p.delivery

	switch {
	case key.Matches(msg, ui.Nav.Back):
		p.closeDelivery()
	case key.Matches(msg, deliveryKeys.Reload):
		return refresh(secPins, secDelivery)
	case key.Matches(msg, deliveryKeys.Next):
		v.moveOccurrence(1)
	case key.Matches(msg, deliveryKeys.Previous):
		v.moveOccurrence(-1)
	default:
		v.scroll.Key(msg)
	}

	return nil
}

// deliveryRoute is a narrow modal router. The expansion only reads, so no
// route writes, and a refresh only discards the reading that it replaces.
func (p *pinsView) deliveryRoute(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case pinDeliveryLoadedMsg:
		if p.delivery == m.view {
			m.view.loaded(m)
		}

		return nil, true
	case deliveryDisposedMsg:
		p.closeDelivery()
	case refreshMsg:
		if v := p.delivery; v != nil && slices.Contains(deliveryRefreshKinds(m.kinds), secDelivery) {
			v.seq.cancel()
			v.reading.loading = true
			if v.selected.ref != "" {
				v.wanted = v.selected
			}

			v.selected = deliveryOccurrence{}
		}
	case loadedMsg:
		if m.kind == secDelivery && m.err != nil {
			return toast(ui.ToneAttention, "Lesson delivery could not be read: "+ui.ErrText(m.err)), false
		}

		v := p.delivery
		if v != nil && (m.kind == secDelivery || m.kind == secPins) && !p.st.pins.loading && !p.st.delivery.loading {
			if !p.rebindDelivery() {
				p.closeDelivery()

				return toast(ui.ToneAttention, "Lesson delivery closed: the selected pin is no longer uniquely matched to its version."), false
			}

			return v.reload(), false
		}
	case tea.KeyMsg:
		if p.delivery != nil {
			return p.deliveryKey(m), true
		}
	}

	return nil, false
}
