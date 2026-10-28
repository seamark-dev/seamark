package tui

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/lessonreport"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// deliveryLines is a separate reading after Outcome and Exposure. Compact
// owns every word and count; wrapping must retain its whole command tail.
func (p *pinsView) deliveryLines(row pinRow, w int) []string {
	reading, ok := p.deliveryReading(row)
	if !ok {
		return nil
	}

	compact, _ := lessonreport.Compact(&p.st.delivery.value, reading.PinVersion)
	t := p.st.theme
	out := []string{t.Heading.Render("Capture delivery")}

	if p.st.delivery.stale() {
		out = append(out, propNote(t, ui.ToneAttention, "Stale: capture delivery could not be read again. "+
			ui.ErrText(p.st.delivery.err)+". Press r to read again; expansion is unavailable until then.", w)...)
	}

	return append(out, propLines(t.Body, ui.OneLine(compact.Line), w)...)
}

func (v *pinDelivery) modalScroll() *ui.ScrollState {
	return &v.scroll
}

func (v *pinDelivery) keys() []key.Binding {
	back := ui.WithHelp(ui.Nav.Back, "esc", "close")

	var keys []key.Binding

	if v.ready() {
		if _, ok := v.occurrence(); ok {
			keys = append(keys, deliveryKeys.Next, deliveryKeys.Previous)
		}
	}

	return append(keys, deliveryKeys.Reload, back, ui.WithHelp(ui.Nav.Down, "↑↓", "scroll"))
}

func (v *pinDelivery) content(w int) ui.ModalOpts {
	t := v.st.theme
	o := ui.ModalOpts{Title: t.Title.Render("Capture delivery"), Actions: v.keys()}

	switch {
	case v.reading.loading && !v.reading.loaded:
		o = flowWaitModal(v.st, "Reading capture delivery", "Seamark reads the selected pin's versions, "+
			"occurrences and observation windows. Nothing is written.", true, w)
		o.Actions = v.keys()

		return o
	case v.reading.err != nil:
		o.Body = append(o.Body, propNote(t, ui.ToneFailure, "Capture delivery could not be read: "+ui.ErrText(v.reading.err), w)...)
		if !v.reading.loaded {
			return o
		}
	}

	if !v.ready() {
		o.Body = append(o.Body, propNote(t, ui.ToneAttention, "Stale or loading: this is an earlier snapshot of the selected pin.", w)...)
	}

	if d, ok := v.occurrence(); ok {
		o.Body = append(o.Body, propLines(t.Accent, "Selected occurrence: "+ui.OneLine(d.AssociationRef)+
			"; version "+ui.OneLine(d.PinVersion)+"; "+ui.OneLine(d.Review), w)...)
		o.Body = append(o.Body, propLines(t.Muted, "Tab / shift+tab select an occurrence across versions; arrows scroll.", w)...)
		o.Body = append(o.Body, selectedDeliveryLines(t, d, w)...)
	} else {
		o.Body = append(o.Body, propLines(t.Muted, "No occurrence selected.", w)...)
	}

	if v.reading.loaded {
		o.Body = append(o.Body, "")
		o.Body = append(o.Body, deliveryTextLines(t, v.reading.value, w)...)
	}

	return o
}

// selectedDeliveryLines puts the cursor's analyzed metadata before the full
// source report, so selecting a version never requires paging past its units.
func selectedDeliveryLines(t *ui.Theme, d lessonreport.OccurrenceDetail, w int) []string {
	out := propLines(t.Body, fmt.Sprintf("Episode %s; family %s; origin %s; class %s; reason %s; decision timing %s; limitations %v",
		d.EpisodeRef, d.Family, d.Origin, d.Class, d.Reason, d.Timing, d.Limitations), w)

	for _, a := range d.Actions {
		out = append(out, propLines(t.Body, fmt.Sprintf("Action %s; lane %s; class %s; reason %s; decision timing %s; timing reason %s",
			a.ActionRef, a.Lane, a.Class, a.Reason, a.Timing, a.TimingReason), w)...)
	}

	return out
}

// deliveryTextLines shares the read-only CLI renderer, including its analyzed
// next steps, safe inspection commands, source limitations and independent units.
func deliveryTextLines(t *ui.Theme, report lessonreport.Report, w int) []string {
	var b bytes.Buffer

	_ = lessonreport.RenderText(&b, report)

	var out []string

	for line := range strings.SplitSeq(strings.TrimRight(b.String(), "\n"), "\n") {
		out = append(out, hangLines(t.Body, line, w)...)
	}

	return out
}
