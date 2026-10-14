package pinfile

// The verifier. A splice is text surgery, so the planner proves each
// candidate again from its bytes before it returns it. Checks 1 to 5
// read the candidate bytes alone. Check 7 states the line rules of D-7
// and D-8 again from the reviewed text and the YAML nodes. It does not
// use the span code that built the splices (ItemSpan, the key extents,
// provenanceLines), so a defect there blocks an edit instead of passing
// it. The insert positions are the one exception. They are the places
// after the region keys of a retarget and after the rule key. A region
// key or an absent note goes in there. See retargetLines and
// rewriteLines.

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/yamledit"
)

// verify runs the D-9 checks on the candidate of the edits ps:
//
//  1. the candidate passes the editing gate;
//  2. the lessons reader loads it. The gate holds the reader error, so
//     check 1 also makes this check;
//  3. every top-level key and every value except the pin list is equal
//     and in place;
//  4. the decoded pin items equal the intended change of the reviewed
//     items, in order, with duplicates and unknown keys;
//  5. a retargeted or rewritten entry reads back with its target key and
//     with the rule and the note of the request. It is still editable;
//  6. (in collide) a target key is not in use;
//  7. only the reviewed lines change, and every other line stays
//     byte-identical and in order.
func (a *analysis) verify(cand []byte, ps []*planned) error {
	b := analyze(cand)

	if b.gate != "" {
		return fmt.Errorf("the candidate fails the editing gate: %s", b.gate)
	}

	if !sameTopLevel(a.root, b.root) {
		return errors.New("a setting outside the pin list changes")
	}

	want, err := a.intended(ps)
	if err != nil {
		return err
	}

	got, err := b.itemValues()
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(want, got) {
		return errors.New("the pin list differs from the intended change")
	}

	if err := sameTargets(b, ps); err != nil {
		return err
	}

	return a.sameOutside(b, ps)
}

// sameTopLevel reports whether both top-level mappings hold the same
// keys in the same order with the same values, except the pin value.
func sameTopLevel(x, y *yaml.Node) bool {
	if x == nil || y == nil || len(x.Content) != len(y.Content) {
		return false
	}

	for i := 0; i < len(x.Content); i += 2 {
		if !yamledit.NodeEqual(x.Content[i], y.Content[i]) {
			return false
		}

		if x.Content[i].Value != "pin" && !yamledit.NodeEqual(x.Content[i+1], y.Content[i+1]) {
			return false
		}
	}

	return true
}

// itemValues decodes every pin item into a generic map. The maps keep
// unknown keys, and the slice keeps order and repeated items. A null pin
// value is an empty list, as for the readers.
func (a *analysis) itemValues() ([]map[string]any, error) {
	out := []map[string]any{}

	if a.seq == nil {
		return out, nil
	}

	for _, n := range a.seq.Content {
		var m map[string]any

		if err := n.Decode(&m); err != nil {
			return nil, fmt.Errorf("a pin item does not decode: %w", err)
		}

		out = append(out, m)
	}

	return out, nil
}

// intended returns the reviewed pin items with the edits of ps applied
// in memory. A prune removes an item. A retarget sets the region keys by
// the rules of retarget. A rewrite sets its changed fields.
func (a *analysis) intended(ps []*planned) ([]map[string]any, error) {
	items, err := a.itemValues()
	if err != nil {
		return nil, err
	}

	drop := map[int]bool{}

	for _, p := range ps {
		i := p.entry.Index

		switch {
		case p.edit.Delete:
			drop[i] = true
		case p.change != nil:
			items[i] = a.rewritten(items[i], p)
		default:
			items[i] = withRegions(items[i], p.edit.Regions)
		}
	}

	out := []map[string]any{}

	for i, m := range items {
		if !drop[i] {
			out = append(out, m)
		}
	}

	return out, nil
}

// withRegions returns a copy of the item m with the region keys set to
// regions. It states the retarget rules as values: region holds the
// first region or "*", regions holds a set of more than one region, and
// an entry with only regions keeps that key while the set is not empty.
func withRegions(m map[string]any, regions []string) map[string]any {
	out := maps.Clone(m)
	_, hasRegion := m["region"]
	_, hasSet := m["regions"]

	list := make([]any, 0, len(regions))
	for _, r := range regions {
		list = append(list, r)
	}

	first := "*"
	if len(regions) > 0 {
		first = regions[0]
	}

	switch {
	case hasRegion:
		out["region"] = first

		switch {
		case len(regions) > 1:
			out["regions"] = list
		case hasSet:
			delete(out, "regions")
		}
	case hasSet && len(regions) > 0:
		out["regions"] = list
	case hasSet:
		delete(out, "regions")
		out["region"] = "*"
	case len(regions) > 0:
		out["region"] = first

		if len(regions) > 1 {
			out["regions"] = list
		}
	}

	return out
}

// rewritten returns a copy of the item m with the changed fields of the
// rewrite p. These are the region keys by the rules of retarget, the
// rule, and the note. An empty note removes the note key. A note key on
// the dash line keeps an empty value instead. dashNote finds that place
// from the key node and the text before it.
func (a *analysis) rewritten(m map[string]any, p *planned) map[string]any {
	ch, out := p.change, maps.Clone(m)

	if ch.Regions != nil {
		out = withRegions(out, *ch.Regions)
	}

	if ch.Rule != nil {
		out["rule"] = *ch.Rule
	}

	switch {
	case ch.Note == nil:
	case *ch.Note != "" || a.dashNote(p.item):
		out["note"] = *ch.Note
	default:
		delete(out, "note")
	}

	return out
}

// dashNote reports whether the note key of the item sits on its dash
// line.
func (a *analysis) dashNote(it item) bool {
	k, _, _ := yamledit.Lookup(mapping(it.node), "note")

	return k != nil && a.onDashLine(k)
}

// sameTargets checks each retargeted or rewritten entry of the candidate
// b. The entry must read back with its target key and with the rule and
// the note of the request (requested). It must also stay editable.
func sameTargets(b *analysis, ps []*planned) error {
	for _, p := range ps {
		if p.edit.Delete {
			continue
		}

		noun := "a retargeted entry"
		if p.change != nil {
			noun = "a rewritten entry"
		}

		index := p.entry.Index

		for _, q := range ps {
			if q.edit.Delete && q.entry.Index < p.entry.Index {
				index--
			}
		}

		k := slices.IndexFunc(b.entries, func(e Entry) bool { return e.Index == index })
		if k < 0 {
			return errors.New(noun + " is missing from the candidate")
		}

		rule, note := p.requested()

		e := b.entries[k]
		if e.Key != p.target || e.Rule != rule || e.Note != note || e.Block != "" {
			return errors.New(noun + " does not read back as its target")
		}
	}

	return nil
}

// sameOutside checks the line diff of the candidate b (check 7). Each
// splice must stay inside the lines that its edit may change. The rules
// for those lines come from the text, not from the splice code. Every
// other line must be in the candidate, byte-identical and in order.
func (a *analysis) sameOutside(b *analysis, ps []*planned) error {
	for _, p := range ps {
		check := a.retargetLines

		switch {
		case p.edit.Delete:
			check = a.prunedLines
		case p.change != nil:
			check = a.rewriteLines
		}

		if err := check(p); err != nil {
			return err
		}
	}

	// The rules above prove where the splices go. This comparison proves
	// that the candidate is the result of exactly those splices.
	if !slices.Equal(a.spliced(ps), b.lines) || a.finalEOL != b.finalEOL {
		return errors.New("the candidate lines differ from the reviewed lines outside the edit")
	}

	return nil
}

// outside is the error for a splice that reaches line n, which the edit
// may not change.
func outside(n int) error {
	return fmt.Errorf("line %d is outside the reviewed entry", n)
}

// prunedLines checks the lines that the prune p deletes against D-8. The
// own lines are contiguous. They start at a dash line and end on the dash
// line or on a line deeper than the dash, and no deeper line follows
// them. A deleted line above the dash is a provenance comment with a
// linked ID, and only provenance comments stand between it and the dash.
// The check reads the IDs with provenanceID, as the listing does, because
// D-8 defines one ID format; TestProvenanceIDNeedsTheCanonicalDigitRun
// covers that reader.
func (a *analysis) prunedLines(p *planned) error {
	var deleted []int

	for _, s := range p.splices {
		if len(s.lines) > 0 || s.to < s.from {
			return errors.New("a prune would add text")
		}

		for n := s.from; n <= s.to; n++ {
			deleted = append(deleted, n)
		}
	}

	slices.Sort(deleted)
	deleted = slices.Compact(deleted)

	// The comments above the dash come first, so the first other line is
	// the dash line.
	k := slices.IndexFunc(deleted, func(n int) bool { return !a.comment(n) })
	if k < 0 {
		return errors.New("a prune deletes no entry")
	}

	dash := deleted[k]
	col := yamledit.LeadingSpaces(a.lines[dash-1])

	if !yamledit.DashAt(a.lines[dash-1], col) {
		return outside(dash)
	}

	if err := a.ownRun(deleted[k:], col); err != nil {
		return err
	}

	for _, n := range deleted[:k] {
		if err := a.boundLine(p, n, dash, col); err != nil {
			return err
		}
	}

	return nil
}

// ownRun checks the deleted lines from the dash line on. col is the dash
// column. A blank line or a comment at the dash column after the last
// deeper line belongs to the next item, so the run must not end on one.
func (a *analysis) ownRun(own []int, col int) error {
	dash, last := own[0], own[len(own)-1]

	for i, n := range own {
		if n != dash+i {
			return outside(n)
		}
	}

	if last != dash && !deeper(a.lines[last-1], col) {
		return outside(last)
	}

	for n := last + 1; n <= len(a.lines); n++ {
		switch l := a.lines[n-1]; {
		case yamledit.IsBlank(l):
			continue
		case deeper(l, col):
			return fmt.Errorf("line %d of the reviewed entry stays", n)
		}

		break
	}

	return nil
}

// boundLine checks one deleted line n above the dash line. A comment
// deeper than the dash after an earlier item is an own line of that
// item, so only the first item can have a deeper provenance line.
func (a *analysis) boundLine(p *planned, n, dash, col int) error {
	id, ok := provenanceID(a.lines[n-1])

	switch {
	case !ok || !slices.Contains(p.edit.Provenance, id):
		return outside(n)
	case deeper(a.lines[n-1], col) && p.entry.Index > 0:
		return outside(n)
	}

	for m := n + 1; m < dash; m++ {
		if _, ok := provenanceID(a.lines[m-1]); !ok || !a.comment(m) {
			return outside(n)
		}
	}

	return nil
}

// deeper reports whether line l holds text indented deeper than col.
func deeper(l string, col int) bool {
	return !yamledit.IsBlank(l) && yamledit.IndentOf(l) > col
}

// retargetLines checks the splices of the retarget p against D-7.
func (a *analysis) retargetLines(p *planned) error {
	lines, inserts := a.regionLines(p)

	return within(p, lines, inserts)
}

// regionLines returns the lines that a change of the region keys of p
// may change or remove. These are the line of a region key, the item
// lines of a block regions list, and the blank lines between them. These
// lines come from the YAML nodes.
//
// inserts holds the lines before which an insertion may go. These are
// the lines right after the lines of a region key, and the line after
// the rule key. These places come from the key extents, as in the
// planner. An insertion removes no line, and checks 3 to 5 prove its
// value. A wrong place can therefore only move a key line.
func (a *analysis) regionLines(p *planned) (lines, inserts map[int]bool) {
	lines, inserts = map[int]bool{}, map[int]bool{}
	stop := p.item.last + 1

	for _, name := range []string{"region", "regions"} {
		k, v, _ := yamledit.Lookup(mapping(p.item.node), name)
		if k == nil {
			continue
		}

		lines[k.Line] = true

		if v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 {
			for n := k.Line; n <= yamledit.LastLine(v); n++ {
				lines[n] = lines[n] || yamledit.IsBlank(a.lines[n-1])
			}

			for _, c := range v.Content {
				lines[c.Line] = true
			}
		}

		if s, ok := a.doc.KeySpan(p.item.node, name, stop); ok {
			for n := s.First; n <= s.Last; n++ {
				inserts[n+1] = true
			}
		}
	}

	if s, ok := a.doc.KeySpan(p.item.node, "rule", stop); ok {
		inserts[s.Last+1] = true
	}

	return lines, inserts
}

// rewriteLines checks the splices of the rewrite p. Each changed field
// adds its lines: the rule key line, the region lines of regionLines,
// and the note lines of noteLines. An absent note may go in only after
// the rule key, the place that the planner takes from the key extent.
func (a *analysis) rewriteLines(p *planned) error {
	ch, m := p.change, mapping(p.item.node)
	lines, inserts := map[int]bool{}, map[int]bool{}

	if ch.Rule != nil {
		if k, _, _ := yamledit.Lookup(m, "rule"); k != nil {
			lines[k.Line] = true
		}
	}

	if ch.Regions != nil {
		l, i := a.regionLines(p)
		maps.Copy(lines, l)
		maps.Copy(inserts, i)
	}

	if ch.Note != nil {
		l, err := a.noteLines(p)
		if err != nil {
			return err
		}

		maps.Copy(lines, l)

		k, _, _ := yamledit.Lookup(m, "note")
		if s, ok := a.doc.KeySpan(p.item.node, "rule", p.item.last+1); k == nil && ok {
			inserts[s.Last+1] = true
		}
	}

	return within(p, lines, inserts)
}

// noteLines returns the lines that a change of the note of p may replace
// or remove. They come from the nodes and the text. They are the line of
// the note key and each later line that is blank or deeper than the key
// column. They stop at the next key of the entry or at the next dash at
// the item column. Blank lines at the end belong to what follows, so
// they are left out.
//
// A comment below the key line refuses the change, because the planner
// carries over only the trailing comment of the key line. That is a
// comment line or a comment after the text of a note line. The check
// uses Doc.CommentIn, which reads a "#" inside a quoted or a block note
// as note text.
func (a *analysis) noteLines(p *planned) (map[int]bool, error) {
	m := mapping(p.item.node)

	k, _, _ := yamledit.Lookup(m, "note")
	if k == nil {
		return nil, nil
	}

	next := len(a.lines) + 1

	for i := 0; i < len(m.Content); i += 2 {
		if l := m.Content[i].Line; l > k.Line && l < next {
			next = l
		}
	}

	col, dash := k.Column-1, a.seq.Column-1
	last := k.Line

	for n := k.Line + 1; n < next; n++ {
		l := a.lines[n-1]

		if yamledit.DashAt(l, dash) && yamledit.LeadingSpaces(l) == dash {
			break
		}

		if yamledit.IsBlank(l) {
			continue
		}

		if !deeper(l, col) {
			break
		}

		last = n
	}

	if a.doc.CommentIn(k.Line+1, last) {
		return nil, fmt.Errorf("the note at line %d holds a comment, which the edit would drop", k.Line)
	}

	lines := map[int]bool{}
	for n := k.Line; n <= last; n++ {
		lines[n] = true
	}

	return lines, nil
}

// within checks that each splice of p changes only lines in lines, and
// that each insertion goes before a line in inserts.
func within(p *planned, lines, inserts map[int]bool) error {
	for _, s := range p.splices {
		if s.to < s.from {
			if !inserts[s.from] {
				return fmt.Errorf("an inserted line at line %d is outside the reviewed entry", s.from)
			}

			continue
		}

		for n := s.from; n <= s.to; n++ {
			if !lines[n] {
				return outside(n)
			}
		}
	}

	return nil
}
