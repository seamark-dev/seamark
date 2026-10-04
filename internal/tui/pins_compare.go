package tui

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// This file holds the read-only comparison of Lessons › Pins. The key v
// opens it for a pin with similar pins. It shows both rules, notes and
// region sets side by side. The lessons clustering suggests the pairs,
// and the comparison only explains them: it selects and plans nothing.

// pinCompare is the read-only comparison of the pin left with one of its
// similar pins. It holds entry bindings, so a refresh that changes a pin
// closes it instead of showing another pin.
type pinCompare struct {
	left     pinfile.EntryRef
	partners []pinfile.EntryRef
	at       int
	scroll   scrollState
}

// openCompare opens the comparison of the cursor pin with its similar
// pins. It reads nothing: the readings already hold the clusters. The
// footer lists v as n/a when no comparison exists, so the press then
// says why in one line, as w, - and t do (see refusal).
func (p *pinsView) openCompare(rows []pinRow) tea.Cmd {
	cur, ok := p.current(rows)
	if !ok || cur.pin == nil {
		return toast(ToneNeutral, p.noTarget("comparison"))
	}

	if why := p.compareBlock(cur.pin); why != "" {
		return toast(ToneNeutral, pinRefusal("comparison", why, p.quoteRule(cur.pin.Entry.Rule)))
	}

	p.compare = &pinCompare{left: cur.pin.Entry.Ref(), partners: p.similarRefs(cur.pin)}

	return nil
}

// similarRefs returns the bindings of the similar pins of pin that the
// current inspection holds.
func (p *pinsView) similarRefs(pin *maintain.Pin) []pinfile.EntryRef {
	in := p.inspection()
	if in == nil {
		return nil
	}

	var refs []pinfile.EntryRef

	for _, i := range pin.Readings.Similar {
		if i >= 0 && i < len(in.Pins) {
			refs = append(refs, in.Pins[i].Entry.Ref())
		}
	}

	return refs
}

// compareBlock returns why v opens no comparison for pin, or "" when it
// opens one. The lessons clustering groups only linked pins
// (maintain.Readings.Similar), so a pin without one linked proposal has
// no similar pin.
func (p *pinsView) compareBlock(pin *maintain.Pin) string {
	switch {
	case len(p.similarRefs(pin)) > 0:
		return ""
	case !pinLinksKnown(p.inspection()):
		return "its link is unknown, so no similar pin is known"
	case !pinLinked(pin):
		return "it has " + pinLinkNoun(pin.Link.Link) + ", and only linked pins are compared"
	}

	return "no other linked pin has similar wording"
}

// compareKey handles keys while the comparison is open: tab shows the
// next similar pin, esc closes, the movement keys scroll.
func (p *pinsView) compareKey(msg tea.KeyMsg) tea.Cmd {
	c := p.compare

	switch {
	case key.Matches(msg, nkeys.Back), key.Matches(msg, nkeys.Enter):
		p.compare = nil
	case key.Matches(msg, nkeys.Next):
		if len(c.partners) > 1 {
			c.at = (c.at + 1) % len(c.partners)
			c.scroll = scrollState{}
		}
	default:
		c.scroll.key(msg)
	}

	return nil
}

// rebindCompare binds the comparison to a new inspection. A compared pin
// that moved but kept its content is the same pin (pinMatch), so it
// takes its new binding and the comparison stays open. It reports
// whether both shown pins are still in the file with the same content.
func (p *pinsView) rebindCompare() bool {
	c := p.compare

	for i, ref := range c.partners {
		if pin, _, ok := p.pinByRef(ref); ok {
			c.partners[i] = pin.Entry.Ref()
		}
	}

	left, _, okL := p.pinByRef(c.left)
	if okL {
		c.left = left.Entry.Ref()
	}

	_, _, okR := p.pinByRef(c.partners[c.at])

	return okL && okR
}

// compareKeys returns the keys of the comparison.
func (p *pinsView) compareKeys() []key.Binding {
	c := p.compare

	next := withHelp(nkeys.Next, "tab", "next similar pin")
	next.SetEnabled(len(c.partners) > 1)

	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(c.scroll.scrolls())

	return []key.Binding{next, withHelp(nkeys.Back, "esc", "close"), scroll}
}

// compareModal renders the read-only comparison of two similar pins:
// both rules, notes and region sets side by side, and the coverage
// difference in plain words. It changes nothing.
func (p *pinsView) compareModal(w, h int) string {
	t := p.st.theme
	c := p.compare
	bw := modalBodyWidth(modalMinW, w)

	left, ln, okL := p.pinByRef(c.left)
	right, rn, okR := p.pinByRef(c.partners[c.at])

	o := modalOpts{Actions: p.compareKeys()}

	if len(c.partners) > 1 {
		o.Right = t.Muted.Render(itoa(c.at+1) + " of " + itoa(len(c.partners)))
	}

	if !okL || !okR {
		o.Title = t.TabActive.Render("Compare")
		o.Body = propLines(t.Muted, "A compared pin changed since the comparison opened. Press esc and open it again.", bw)

		return t.modal(o, &c.scroll, w, h)
	}

	o.Title = t.TabActive.Render("Compare") + " " + t.Body.Render("pin "+itoa(ln+1)+" "+t.Glyphs.Sep+" pin "+itoa(rn+1))

	body := propLines(t.Muted, "Similar wording does not mean identical coverage. Read both pins and compare "+
		"their regions before you retire either one. This view is read-only and changes nothing.", bw)

	pw := pairWidth(bw)

	body = append(body, "")
	body = append(body, t.pair(p.compareHead(left, ln, pw), p.compareHead(right, rn, pw),
		p.compareSide(left, pw), p.compareSide(right, pw), bw)...)
	body = append(body, "", t.Heading.Render("COVERAGE"))
	body = append(body, p.coverageLines(left, ln, right, rn, bw)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "The lessons clustering groups pins by wording. It is advice: it selects "+
		"nothing, and a prune preview names a retained pin only when that pin stays linked.", bw)...)

	o.Body = body

	return t.modal(o, &c.scroll, w, h)
}

// compareHead renders the head of one side: the pin number, its line,
// and its linked proposal.
func (p *pinsView) compareHead(pin *maintain.Pin, n, w int) string {
	t := p.st.theme
	sep := " " + t.Glyphs.Sep + " "

	meta := "line " + itoa(pin.Entry.Line)
	if pinLinked(pin) {
		meta += sep + propID(pin.Link.Applied[0].ID)
	}

	return truncate(t.Heading.Render("PIN "+itoa(n+1))+"  "+t.Faint.Render(meta), w, t.Glyphs.Ellipsis)
}

// compareSide renders one side of the comparison: the live rule, the
// live note and the regions, each wrapped to the column width.
func (p *pinsView) compareSide(pin *maintain.Pin, w int) []string {
	t := p.st.theme
	e := pin.Entry

	rule := oneLine(e.Rule)
	if strings.TrimSpace(rule) == "" {
		rule = "(no rule)"
	}

	out := propLines(t.Title, rule, w)
	out = append(out, "")

	if strings.TrimSpace(e.Note) == "" {
		out = append(out, t.Muted.Render("No note is set."))
	} else {
		out = append(out, propLines(t.Body, pinNoteText(e.Note), w)...)
	}

	out = append(out, "", t.Muted.Render("Regions"))

	return append(out, p.regionValues(pinEntryRegions(e), w)...)
}

// coverageLines states how the two region sets differ in the paths
// they reach. A region inside a region of the other pin is reached by
// both, so the rows compare coverage, not the spelling of the sets. A
// repository-wide pin reaches every path, so it is named as such.
func (p *pinsView) coverageLines(left *maintain.Pin, ln int, right *maintain.Pin, rn int, w int) []string {
	t := p.st.theme
	a, b := pinEntryRegions(left.Entry), pinEntryRegions(right.Entry)
	la, lb := "pin "+itoa(ln+1), "pin "+itoa(rn+1)

	switch {
	case len(a) == 0 && len(b) == 0:
		return propLines(t.Body, "Both pins are repository-wide: each one reaches every path.", w)
	case len(a) == 0:
		return propLines(t.Body, "Pin "+itoa(ln+1)+" is repository-wide and reaches every path; "+
			lb+" reaches only "+pinJoin(b)+".", w)
	case len(b) == 0:
		return propLines(t.Body, "Pin "+itoa(rn+1)+" is repository-wide and reaches every path; "+
			la+" reaches only "+pinJoin(a)+".", w)
	}

	onlyA, bothA := pinCoverage(a, b)
	onlyB, bothB := pinCoverage(b, a)

	if len(onlyA) == 0 && len(onlyB) == 0 {
		return propLines(t.Body, "Both pins reach the same paths: "+pinJoin(a)+".", w)
	}

	var rows [][2]string

	if len(onlyA) > 0 {
		rows = append(rows, [2]string{"Only " + la + " reaches", strings.Join(onlyA, ", ")})
	}

	if len(onlyB) > 0 {
		rows = append(rows, [2]string{"Only " + lb + " reaches", strings.Join(onlyB, ", ")})
	}

	if both := pinUnion(bothA, bothB); len(both) > 0 {
		rows = append(rows, [2]string{"Both reach", strings.Join(both, ", ")})
	}

	return propKV(t, rows, w)
}

// pinCoverage splits the regions of set by the regions of other. A
// region inside a region of other is reached by both. Any other region
// is reached only by set; when regions of other lie inside it, the text
// names them as the part that both reach. Every region is sanitized.
func pinCoverage(set, other []string) (only, both []string) {
	for _, r := range set {
		if slices.ContainsFunc(other, func(o string) bool { return reviews.RegionMatches(o, r) }) {
			both = append(both, oneLine(r))

			continue
		}

		var inside []string

		for _, o := range other {
			if reviews.RegionMatches(r, o) {
				inside = append(inside, oneLine(o))
			}
		}

		if len(inside) == 0 {
			only = append(only, oneLine(r))
		} else {
			only = append(only, oneLine(r)+" outside "+strings.Join(inside, ", "))
		}
	}

	return only, both
}

// pinUnion returns the regions of a and then the new regions of b.
func pinUnion(a, b []string) []string {
	out := slices.Clone(a)

	for _, r := range b {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}

	return out
}

// pinJoin joins a region set for a sentence, each region sanitized.
func pinJoin(set []string) string {
	out := make([]string, len(set))
	for i, r := range set {
		out[i] = oneLine(r)
	}

	return strings.Join(out, ", ")
}
