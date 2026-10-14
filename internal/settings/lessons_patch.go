package settings

// The patch planner of lessons.yaml. It validates one group of edits and
// splices the text of the observed file. A verifier then checks the
// candidate, and it shares no line computation with the splices. The
// guard, the preview, and the save are the shared ones of patch.go and
// save.go.
//
// The delivery keys are top-level scalars. A set replaces the value on
// its key line, and the trailing comment stays. An absent key goes where
// a new top-level key goes (yamledit.SectionStart). A reset removes the
// key line, with the comment on it, and the value lines. A mute
// operation changes one item of the mute list. An add appends one block
// item after the last item. A change replaces the rule and region values
// inside the item. A removal deletes the item lines. Other comments and
// unknown keys stay; a splice that would drop a comment refuses with the
// manual route.

import (
	"bytes"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// noteMuteRemove explains the limit of a mute removal (spec R-7). The
// selection excludes a mined lesson for several reasons, and a removal
// takes away only one of them. Pins are never muted.
const noteMuteRemove = "Removing this mute may not bring a lesson back: " +
	"another mute, the threshold, or an applied pin's coverage can still exclude it."

// validateMute checks one mute operation. The typed values must pass
// CheckMute, the check that the record form shows.
func validateMute(m MuteEdit) error {
	switch m.Op {
	case MuteAdd, MuteChange:
		if _, err := CheckMute(m.Rule, m.Region); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidEdit, err)
		}
	case MuteRemove:
		if m.Rule != "" || m.Region != "" {
			return fmt.Errorf("%w: a mute removal takes no rule and no region", ErrInvalidEdit)
		}
	default:
		return fmt.Errorf("%w: unknown mute operation %d", ErrInvalidEdit, int(m.Op))
	}

	if m.Op == MuteAdd && m.Ref != (MuteRef{}) {
		return fmt.Errorf("%w: a new mute entry takes no entry reference", ErrInvalidEdit)
	}

	return nil
}

// planLessons plans one lessons group. validate has checked the edits.
func planLessons(snap *Snapshot, reg *integration.Registry, group Group, edits []Edit) (*Plan, error) {
	if err := snap.Editable(group); err != nil {
		return nil, err
	}

	lp := snap.Lessons

	// The patch starts from the snapshot bytes. They must still be the
	// file bytes, or the before values in the preview would be false.
	if err := snap.unchanged(pinfile.File); err != nil {
		return nil, err
	}

	// The guard does not record the link count, so the plan asks again.
	if b := hardLinkBlock(snap.Root); b != nil {
		return nil, b
	}

	ordered := orderEdits(edits)

	var (
		after []byte
		notes []string
		err   error
	)

	if group == GroupMutes {
		after, notes, err = spliceMute(lp, *ordered[0].Mute)
	} else {
		after, err = spliceDelivery(lp.data, ordered)
	}

	if err != nil {
		return nil, err
	}

	got, err := verifyLessons(lp, group, ordered, after)
	if err != nil {
		return nil, err
	}

	plan := &Plan{
		Snapshot: snap,
		Group:    group,
		Edits:    slices.Clone(edits),
		File:     pinfile.File,
		Bytes:    PlanBytes{Before: lp.data, After: after},
		Before:   groupFields(snap.Fields, group),
		After:    groupFields(got.fields(), group),
	}

	if bytes.Equal(after, lp.data) {
		plan.NoOp = true
		plan.Notes = []string{noteNoOp}

		return plan, nil
	}

	// A blocked CRLF file never reaches this point, so the planned
	// bytes are the written bytes.
	if err := plan.guard(reg, after); err != nil {
		return nil, err
	}

	notes = append(notes, noteCommit)
	plan.Activation = activation(snap, ordered)
	plan.Notes = notes

	return plan, nil
}

// spliceDelivery applies the delivery edits one at a time, in the order
// of orderEdits. It parses the text again before each edit, so every
// edit locates its key in the current text. The absent keys go in one
// block at the end, so the plan inserts at one place.
func spliceDelivery(data []byte, edits []Edit) ([]byte, error) {
	cur := data

	var added []string

	for _, e := range edits {
		doc, err := parseLessons(cur)
		if err != nil {
			return nil, err
		}

		key := string(e.Field)
		span, present := doc.KeySpan(doc.Root, key, doc.Limit)

		switch {
		case e.Reset && present:
			cur, err = removeTopKey(doc, span)
		case e.Reset:
		case present:
			cur, err = setTopKey(doc, span, e)
		default:
			var text string

			text, err = deliveryText(e)
			added = append(added, key+": "+text)
		}

		if err != nil {
			return nil, err
		}
	}

	if len(added) == 0 {
		return cur, nil
	}

	doc, err := parseLessons(cur)
	if err != nil {
		return nil, err
	}

	return appendTopLevel(doc, added), nil
}

// parseLessons parses an intermediate text of a lessons splice.
func parseLessons(data []byte) (*yamledit.Doc, error) {
	doc, issue := yamledit.Parse(data)
	if issue != nil {
		return nil, refusal(pinfile.File, "the edited text does not parse: "+issue.Reason)
	}

	return doc, nil
}

// deliveryText encodes the value of a delivery edit. A number goes
// through encodeScalar: yamledit.BlockScalar writes only text that
// reads back as a string, and the reader needs an integer. The mode
// goes through yamledit.BlockScalar.
func deliveryText(e Edit) (string, error) {
	if e.Field != HookDelivery {
		return encodeScalar(e.Int)
	}

	text, err := yamledit.BlockScalar(e.String)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidEdit, err)
	}

	return text, nil
}

// setTopKey replaces the value of a present delivery key on its key
// line. The trailing comment stays.
func setTopKey(doc *yamledit.Doc, span yamledit.Span, e Edit) ([]byte, error) {
	key := string(e.Field)

	switch {
	case span.Value.Kind != yaml.ScalarNode:
		return nil, lessonsBlock(key+" does not hold a single value", span.First)
	case span.Value.Line != span.Key.Line:
		return nil, lessonsBlock("the value of "+key+" starts on its own line", span.First)
	case !span.Inline:
		return nil, lessonsBlock("the value of "+key+" spans several lines", span.First)
	}

	text, err := deliveryText(e)
	if err != nil {
		return nil, err
	}

	line, ok := doc.ReplaceValue(span, text)
	if !ok {
		return nil, lessonsBlock("the value of "+key+" has no safe splice", span.First)
	}

	lines := slices.Clone(doc.Lines)
	lines[span.First-1] = line

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// removeTopKey deletes the key line and the value lines of a delivery
// key. A comment in the value lines is the user's prose, so the reset
// refuses instead of deleting it. That is a comment line or a comment
// after the value text of a line. The comment on the key line goes with
// the key, because it describes the key. Neighbour blank lines stay. A
// key on line 1 gives its byte order mark to the new line 1.
func removeTopKey(doc *yamledit.Doc, span yamledit.Span) ([]byte, error) {
	if doc.CommentIn(span.First+1, span.Last) {
		return nil, lessonsBlock("a comment sits inside the value of "+span.Key.Value, span.First)
	}

	lines := slices.Delete(slices.Clone(doc.Lines), span.First-1, span.Last)

	return yamledit.JoinLines(keepMark(doc, lines), doc.FinalEOL), nil
}

// spliceMute applies one mute operation to the snapshot text. It returns
// the candidate and the notes of the operation.
func spliceMute(lp *LessonsPart, m MuteEdit) (after []byte, notes []string, err error) {
	switch m.Op {
	case MuteAdd:
		return addMute(lp, m)
	case MuteChange:
		return changeMute(lp, m)
	default:
		return removeMute(lp, m)
	}
}

// checkedMute returns the typed values after CheckMute and the overlap
// notes. It refuses a duplicate of another entry. skip is the Index of
// the changed entry, or -1 for a new entry.
func (lp *LessonsPart) checkedMute(m MuteEdit, skip int) (MuteInput, []string, error) {
	in, err := CheckMute(m.Rule, m.Region)
	if err != nil {
		return MuteInput{}, nil, fmt.Errorf("%w: %w", ErrInvalidEdit, err)
	}

	if d, ok := lp.Duplicate(in.Rule, in.Region, skip); ok {
		return MuteInput{}, nil, fmt.Errorf("%w: the same mute is already at line %d", ErrInvalidEdit, d.Line)
	}

	var notes []string

	for _, e := range lp.Overlaps(in.Rule, in.Region, skip) {
		notes = append(notes, OverlapNote(e))
	}

	return in, notes, nil
}

// OverlapNote is the review note about an entry e that hides a lesson
// that the planned mute also hides. A form shows the same note before
// the review, so the form and the review use the same words.
func OverlapNote(e MuteEntry) string {
	return fmt.Sprintf("Another entry also matches some of the same lessons: line %d, %s.", e.Line,
		MuteText(e.Rule, e.Region))
}

// MuteText describes a mute for display, as "rule F541 · region api".
// The values are file text, so the description is sanitized.
func MuteText(rule, region string) string {
	var parts []string

	if rule != "" {
		parts = append(parts, "rule "+displayText(rule))
	}

	if region != "" {
		parts = append(parts, "region "+displayText(region))
	}

	if len(parts) == 0 {
		return "no rule and no region"
	}

	return strings.Join(parts, " · ")
}

// muteItem renders a new block item: the rule on the dash line, the
// region below it. A side without a value has no key.
func muteItem(in MuteInput) ([]string, error) {
	var lines []string

	for _, kv := range [][2]string{{"rule", in.Rule}, {"region", in.Region}} {
		if kv[1] == "" {
			continue
		}

		text, err := yamledit.BlockScalar(kv[1])
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidEdit, err)
		}

		prefix := "  "
		if len(lines) == 0 {
			prefix = "- "
		}

		lines = append(lines, prefix+kv[0]+": "+text)
	}

	return lines, nil
}

// addMute appends one block item after the last item of the mute list.
// Under a null "mute:" the item goes after its comment children, such as
// the commented examples of the starter file. An absent mute key goes
// where a new top-level key goes.
func addMute(lp *LessonsPart, m MuteEdit) (after []byte, notes []string, err error) {
	in, notes, err := lp.checkedMute(m, -1)
	if err != nil {
		return nil, nil, err
	}

	item, err := muteItem(in)
	if err != nil {
		return nil, nil, err
	}

	doc := lp.doc

	span, present := doc.KeySpan(doc.Root, keyMute, doc.Limit)
	if !present {
		return appendTopLevel(doc, append([]string{keyMute + ":"}, indentAll(item, 2)...)), notes, nil
	}

	var at, col int

	switch v := span.Value; {
	case yamledit.IsNull(v):
		// A null token ("~", "null", or a tag) would need a change of the
		// key line too. The editor leaves that line to the user.
		if !span.Inline || span.End > span.Start {
			return nil, nil, lessonsBlock(`the mute key holds an explicit null value; write "mute:" without a value`, span.First)
		}

		at, col = span.Last, span.Key.Column-1+2
	case v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0:
		first, _, okFirst := doc.ItemSpan(v, 0, span.Last+1)
		_, last, okLast := doc.ItemSpan(v, len(v.Content)-1, span.Last+1)

		if !okFirst || !okLast {
			return nil, nil, lessonsBlock("the mute list has no safe splice", span.First)
		}

		at, col = last, yamledit.LeadingSpaces(doc.Lines[first-1])
	default:
		return nil, nil, lessonsBlock("the mute key does not hold a block list", span.First)
	}

	lines := slices.Insert(slices.Clone(doc.Lines), at, indentAll(item, col)...)

	return yamledit.JoinLines(lines, doc.FinalEOL), notes, nil
}

// Target returns the entry that ref names, or the refusal of the
// planner. The plan bytes are the snapshot bytes, so a hash that
// differs means that the draft started on another version of the file.
// A form asks it too, so it names a stale draft before the review.
func (lp *LessonsPart) Target(ref MuteRef) (MuteEntry, error) {
	if lp == nil || ref.Index < 0 || ref.Index >= len(lp.Mutes) || lp.Mutes[ref.Index].RawSHA != ref.RawSHA {
		return MuteEntry{}, fmt.Errorf("%w: the mute entry changed since the draft started; choose it again", ErrInvalidEdit)
	}

	return lp.Mutes[ref.Index], nil
}

// Changeable returns nil when the planner can change the entry of ref,
// or its refusal. A form asks it before it opens. So a form never offers
// a change that the planner refuses.
func (lp *LessonsPart) Changeable(ref MuteRef) error {
	_, err := lp.changeTarget(ref)

	return err
}

// changeTarget returns the entry of ref when a change of it can be
// planned. A flow item has no line per key. A null item has no mapping
// that can take a key. So both refuse, and the route removes the entry.
func (lp *LessonsPart) changeTarget(ref MuteRef) (MuteEntry, error) {
	entry, err := lp.Target(ref)
	if err != nil {
		return MuteEntry{}, err
	}

	if entry.Flow {
		return MuteEntry{}, lessonsBlock("the mute entry uses flow style { }; remove the entry and add a new one", entry.Line)
	}

	item, _, _, err := muteItemAt(lp.doc, entry.Index)
	if err != nil {
		return MuteEntry{}, err
	}

	if item.Kind != yaml.MappingNode {
		return MuteEntry{}, lessonsBlock("the mute entry holds no keys; remove the entry and add a new one", entry.Line)
	}

	return entry, nil
}

// muteItemAt returns the item node at index of the mute list in doc and
// its first and last line.
func muteItemAt(doc *yamledit.Doc, index int) (item *yaml.Node, first, last int, err error) {
	span, ok := doc.KeySpan(doc.Root, keyMute, doc.Limit)
	if !ok || span.Value.Kind != yaml.SequenceNode || index < 0 || index >= len(span.Value.Content) {
		return nil, 0, 0, refusal(pinfile.File, "the mute entry is not in the edited text")
	}

	first, last, ok = doc.ItemSpan(span.Value, index, span.Last+1)
	if !ok {
		return nil, 0, 0, lessonsBlock("the mute entry has no safe splice", span.Value.Content[index].Line)
	}

	return span.Value.Content[index], first, last, nil
}

// changeMute replaces the rule and the region of one entry. A side whose
// decoded value stays is not touched, so its text stays as written. The
// sets come first and the removals last, so the entry never has no key
// in between. A flow item and a null item refuse (changeTarget).
func changeMute(lp *LessonsPart, m MuteEdit) (after []byte, notes []string, err error) {
	entry, err := lp.changeTarget(m.Ref)
	if err != nil {
		return nil, nil, err
	}

	in, notes, err := lp.checkedMute(m, entry.Index)
	if err != nil {
		return nil, nil, err
	}

	sides := [][3]string{{"rule", entry.Rule, in.Rule}, {"region", entry.Region, in.Region}}
	cur := lp.data

	for _, s := range sides {
		if s[2] != "" && s[2] != s[1] {
			if cur, err = setMuteKey(cur, entry.Index, s[0], s[2]); err != nil {
				return nil, nil, err
			}
		}
	}

	for _, s := range sides {
		if s[2] == "" && s[1] != "" {
			if cur, err = removeMuteKey(cur, entry.Index, s[0]); err != nil {
				return nil, nil, err
			}
		}
	}

	return cur, notes, nil
}

// setMuteKey sets one key of the item at index. A present key gets the
// new value on its line, and the trailing comment stays. An absent key
// gets one line at the key indent of the item, after the last content
// line of the item's last key.
func setMuteKey(data []byte, index int, key, value string) ([]byte, error) {
	doc, err := parseLessons(data)
	if err != nil {
		return nil, err
	}

	item, first, last, err := muteItemAt(doc, index)
	if err != nil {
		return nil, err
	}

	text, err := yamledit.BlockScalar(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEdit, err)
	}

	lines := slices.Clone(doc.Lines)

	if ks, ok := doc.KeySpan(item, key, last+1); ok {
		line, ok := doc.ReplaceValue(ks, text)
		if !ok || ks.Value.Kind != yaml.ScalarNode {
			return nil, lessonsBlock("the "+key+" of the mute entry spans several lines; remove the entry and add a new one", first)
		}

		lines[ks.First-1] = line

		return yamledit.JoinLines(lines, doc.FinalEOL), nil
	}

	tail, ok := doc.KeySpan(item, item.Content[len(item.Content)-2].Value, last+1)
	if !ok {
		return nil, lessonsBlock("the mute entry has no safe splice", first)
	}

	content := doc.ContentLines(tail.First, tail.Last)
	at := content[len(content)-1]
	lines = slices.Insert(lines, at, spaces(item.Content[0].Column-1)+key+": "+text)

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// removeMuteKey removes one key of the item at index with its value
// lines. A comment in the removed lines refuses. When the key is on the
// dash line, the dash moves to the next key line of the item, so the
// item keeps its other keys. A comment line before that key line stays
// below a bare dash instead. A dash below the comment moves the comment
// into the item above.
func removeMuteKey(data []byte, index int, key string) ([]byte, error) {
	doc, err := parseLessons(data)
	if err != nil {
		return nil, err
	}

	item, first, last, err := muteItemAt(doc, index)
	if err != nil {
		return nil, err
	}

	ks, ok := doc.KeySpan(item, key, last+1)
	if !ok {
		return nil, refusal(pinfile.File, "the "+key+" of the mute entry is not in the edited text")
	}

	// CommentIn scans each line after its value text. The old scan of the
	// key line started at the key. It read the quote of plain text such
	// as rock 'n roll as an opening quote, so it missed the comment.
	if doc.CommentIn(ks.First, ks.Last) {
		return nil, lessonsBlock("a comment sits in the lines that the change removes; remove the entry and add a new one", ks.First)
	}

	lines := slices.Clone(doc.Lines)

	if ks.First == first {
		if ks.Index+2 >= len(item.Content) {
			return nil, refusal(pinfile.File, "the change would leave the mute entry without a key")
		}

		next := item.Content[ks.Index+2].Line
		dash := yamledit.LeadingSpaces(doc.Lines[first-1])
		line := lines[next-1]

		if yamledit.LeadingSpaces(line) <= dash {
			return nil, lessonsBlock("the mute entry has no safe splice", first)
		}

		// These lines hold no value text, so a comment there is a comment
		// line. It decides where the dash goes; no line is deleted.
		if doc.HasComment(ks.Last+1, next-1) {
			lines[first-1] = spaces(dash) + "-"
			lines = slices.Delete(lines, ks.First, ks.Last)

			return yamledit.JoinLines(lines, doc.FinalEOL), nil
		}

		lines[next-1] = line[:dash] + "-" + line[dash+1:]
	}

	lines = slices.Delete(lines, ks.First-1, ks.Last)

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// removeMute deletes the lines of one item. A comment inside them is the
// user's prose, so the removal refuses with the manual route. That is a
// comment line or a comment after the value text of a line, also on the
// dash line.
func removeMute(lp *LessonsPart, m MuteEdit) (after []byte, notes []string, err error) {
	entry, err := lp.Target(m.Ref)
	if err != nil {
		return nil, nil, err
	}

	_, first, last, err := muteItemAt(lp.doc, entry.Index)
	if err != nil {
		return nil, nil, err
	}

	if lp.doc.CommentIn(first, last) {
		return nil, nil, lessonsBlock("a comment sits inside the mute entry", first)
	}

	lines := slices.Delete(slices.Clone(lp.doc.Lines), first-1, last)

	return yamledit.JoinLines(lines, lp.doc.FinalEOL), []string{noteMuteRemove}, nil
}

// verifyLessons is the safety net under the lessons splices. It proves
// that only the intended keys and lines changed and that the reader
// reads exactly the intended values. It returns the candidate as a
// lessons part, for the after states of the plan. The line rule takes
// its allowed lines from the nodes of the reviewed text. It never uses
// KeySpan, Extent, or ItemSpan, so a defect of the splice helpers
// cannot hide from it. The line checks read the texts without the byte
// order mark (lineDocs).
func verifyLessons(lp *LessonsPart, group Group, edits []Edit, after []byte) (*LessonsPart, error) {
	got := &LessonsPart{}

	breaks, crlf, gate := got.load(after)

	switch {
	case breaks != nil, crlf, gate != nil, got.doc == nil:
		return nil, refusal(pinfile.File, "the edited text does not parse")
	case got.ReaderErr != nil:
		return nil, refusal(pinfile.File, fmt.Sprintf("the lessons reader would reject the result: %v", got.ReaderErr))
	}

	was, now, err := lineDocs(pinfile.File, lp.doc, lp.data, got.doc, after)
	if err != nil {
		return nil, err
	}

	keys := editedKeys(edits)

	if err := sameOtherKeys(was, now, keys); err != nil {
		return nil, err
	}

	// The pin list belongs to the pin editors. Its text, comments
	// included, stays byte for byte.
	if pinText(was) != pinText(now) {
		return nil, refusal(pinfile.File, "the pin list changed")
	}

	if err := intendedValues(lp, got, group, edits); err != nil {
		return nil, err
	}

	regions, inserts, err := allowedLines(was, group, edits)
	if err != nil {
		return nil, err
	}

	if err := lineRule(was, now, regions, inserts); err != nil {
		return nil, err
	}

	// The line rule lets an allowed region take any lines. Every splice
	// keeps each comment or refuses, so the comments must stay. Only the
	// comment on the key line of a reset key goes, by design.
	if err := sameComments(pinfile.File, was, now, resetKeyLines(was, edits)); err != nil {
		return nil, err
	}

	return got, nil
}

// editedKeys lists the top-level keys that the edits change.
func editedKeys(edits []Edit) []string {
	out := make([]string, 0, len(edits))

	for _, e := range edits {
		out = append(out, string(e.Field))
	}

	return out
}

// sameOtherKeys requires every other top-level pair to be equal, in
// order, and each edited key to keep its place among them.
func sameOtherKeys(before, after *yamledit.Doc, keys []string) error {
	was, _ := yamledit.PairsExcept(before.Root, keys...)
	now, _ := yamledit.PairsExcept(after.Root, keys...)

	if key, same := yamledit.SamePairs(was, now); !same {
		if key == "" {
			return refusal(pinfile.File, "the other top-level keys changed")
		}

		return refusal(pinfile.File, fmt.Sprintf("the %s key changed", key))
	}

	for _, k := range keys {
		wasAt, inBefore := keyPlace(before.Root, k, keys)
		nowAt, inAfter := keyPlace(after.Root, k, keys)

		if inBefore && inAfter && wasAt != nowAt {
			return refusal(pinfile.File, fmt.Sprintf("the %s key moved", k))
		}
	}

	return nil
}

// keyPlace returns the number of pairs before key in the mapping m whose
// key is not edited, and whether key is in m.
func keyPlace(m *yaml.Node, key string, edited []string) (int, bool) {
	if m == nil {
		return 0, false
	}

	n := 0

	for i := 0; i+1 < len(m.Content); i += 2 {
		name := m.Content[i].Value

		if name == key {
			return n, true
		}

		if !slices.Contains(edited, name) {
			n++
		}
	}

	return 0, false
}

// pinText returns the text of the pin key: its key line through the
// last line of the pin node, deeper comments included. A top-level
// comment after the pin node is not pin text. A reset of the next key
// can move such a comment up, and the pin list stays the same. The line
// rule and the comment check protect these top-level lines.
func pinText(doc *yamledit.Doc) string {
	k, v, idx := yamledit.Lookup(doc.Root, keyPin)
	if k == nil {
		return ""
	}

	end := nodeEnd(doc, k.Line, v, k.Column-1, nextKeyLine(doc, idx), true)

	return strings.Join(doc.Lines[k.Line-1:end], "\n")
}

// nextKeyLine returns the line of the top-level key after the pair at
// index idx, or the end of the document content.
func nextKeyLine(doc *yamledit.Doc, idx int) int {
	if doc.Root != nil && idx >= 0 && idx+2 < len(doc.Root.Content) {
		return doc.Root.Content[idx+2].Line
	}

	return doc.Limit
}

// intendedValues requires the reader values of the candidate to equal
// the reviewed values with the edits applied in memory. For a mute
// operation, the decoded mute list must equal the reviewed list with the
// one operation applied, null items and unknown keys included.
func intendedValues(lp, got *LessonsPart, group Group, edits []Edit) error {
	want := *lp.Config

	switch group {
	case GroupMutes:
		if err := intendedMutes(lp, got, *edits[0].Mute); err != nil {
			return err
		}

		want.Mute = got.Config.Mute
	case GroupDelivery:
		for _, e := range edits {
			// The reader reads an absent key and a stored default alike,
			// so the presence of the key proves a reset or a set.
			k, _, _ := yamledit.Lookup(got.doc.Root, string(e.Field))
			if (k == nil) != e.Reset {
				return refusal(pinfile.File, fmt.Sprintf("the %s key is not in the intended state", e.Field))
			}

			intendDelivery(&want, e)
		}
	}

	if !reflect.DeepEqual(&want, got.Config) {
		return refusal(pinfile.File, fmt.Sprintf("the lessons reader does not read exactly the intended %s change", group))
	}

	return nil
}

// intendDelivery applies one delivery edit to a decoded config. A reset
// gives the value of an absent key: the reader replaces a missing
// threshold with the default and leaves the other keys empty.
func intendDelivery(cfg *reviews.Config, e Edit) {
	switch e.Field {
	case Threshold:
		cfg.Threshold = e.Int
		if e.Reset {
			cfg.Threshold = reviews.DefaultThreshold
		}
	case PinBudget:
		cfg.PinBudget = e.Int
	case ChangeBudget:
		cfg.ChangeBudget = e.Int
	case HookDelivery:
		cfg.Delivery = reviews.HookDeliveryMode(e.String)
	}
}

// intendedMutes compares the decoded mute list of the candidate with the
// reviewed list after the one operation.
func intendedMutes(lp, got *LessonsPart, m MuteEdit) error {
	want, err := muteList(lp.doc)
	if err != nil {
		return err
	}

	now, err := muteList(got.doc)
	if err != nil {
		return err
	}

	in, _ := CheckMute(m.Rule, m.Region)

	switch m.Op {
	case MuteAdd:
		item := map[string]any{}
		setSide(item, "rule", "", in.Rule)
		setSide(item, "region", "", in.Region)
		want = append(want, item)
	case MuteChange:
		if m.Ref.Index >= len(want) {
			return refusal(pinfile.File, "the changed mute entry is not in the reviewed list")
		}

		entry := lp.Mutes[m.Ref.Index]
		item := maps.Clone(want[m.Ref.Index])
		setSide(item, "rule", entry.Rule, in.Rule)
		setSide(item, "region", entry.Region, in.Region)
		want[m.Ref.Index] = item
	case MuteRemove:
		if m.Ref.Index >= len(want) {
			return refusal(pinfile.File, "the removed mute entry is not in the reviewed list")
		}

		want = slices.Delete(want, m.Ref.Index, m.Ref.Index+1)
	}

	if len(want) == 0 {
		want = nil
	}

	if len(now) == 0 {
		now = nil
	}

	if !reflect.DeepEqual(want, now) {
		return refusal(pinfile.File, "the mute list does not hold exactly the intended change")
	}

	return nil
}

// setSide applies the intended value of one side of a mute. A new value
// replaces the old one. An emptied side has no key. A side whose value
// stays keeps its decoded form, such as the integer of "region: 7".
func setSide(item map[string]any, key, old, value string) {
	switch value {
	case old:
	case "":
		delete(item, key)
	default:
		item[key] = value
	}
}

// muteList decodes the mute list: one map per item, nil for a null item.
func muteList(doc *yamledit.Doc) ([]map[string]any, error) {
	_, v, _ := yamledit.Lookup(doc.Root, keyMute)
	if v == nil || yamledit.IsNull(v) {
		return nil, nil
	}

	var out []map[string]any
	if err := v.Decode(&out); err != nil {
		return nil, refusal(pinfile.File, fmt.Sprintf("the mute list does not decode: %v", err))
	}

	return out, nil
}

// allowedLines returns the lines of the reviewed text that a plan may
// change, as 1-based inclusive ranges in order. It also returns the
// number of places where the plan may insert lines. A delivery key may
// change its key line and value lines; an absent key is inserted at one
// place. A mute change or removal may change the item lines; an add
// inserts at one place.
func allowedLines(doc *yamledit.Doc, group Group, edits []Edit) (regions [][2]int, inserts int, err error) {
	if group == GroupMutes {
		m := *edits[0].Mute
		if m.Op == MuteAdd {
			return nil, 1, nil
		}

		_, seq, idx := yamledit.Lookup(doc.Root, keyMute)

		first, last, ok := itemLines(doc, seq, m.Ref.Index, nextKeyLine(doc, idx))
		if !ok {
			return nil, 0, refusal(pinfile.File, "the lines of the mute entry cannot be verified")
		}

		return [][2]int{{first, last}}, 0, nil
	}

	for _, e := range edits {
		k, v, idx := yamledit.Lookup(doc.Root, string(e.Field))
		if k == nil {
			if !e.Reset {
				inserts = 1
			}

			continue
		}

		regions = append(regions, [2]int{k.Line, nodeEnd(doc, k.Line, v, k.Column-1, nextKeyLine(doc, idx), false)})
	}

	slices.SortFunc(regions, func(a, b [2]int) int { return a[0] - b[0] })

	return regions, inserts, nil
}

// itemLines returns the dash line and the last line of item i of the
// block sequence seq. bound is the first line after the list. The scan
// reads the dashes in the text: a dash at the column of the sequence
// opens each item.
func itemLines(doc *yamledit.Doc, seq *yaml.Node, i, bound int) (first, last int, ok bool) {
	if seq == nil || seq.Kind != yaml.SequenceNode || seq.Style&(yaml.FlowStyle|yaml.TaggedStyle) != 0 ||
		i < 0 || i >= len(seq.Content) {
		return 0, 0, false
	}

	col := seq.Column - 1

	if first, ok = dashAbove(doc, seq.Content[i], col); !ok {
		return 0, 0, false
	}

	end := bound

	if i+1 < len(seq.Content) {
		if end, ok = dashAbove(doc, seq.Content[i+1], col); !ok {
			return 0, 0, false
		}
	}

	return first, nodeEnd(doc, first, seq.Content[i], col, end, false), true
}

// dashAbove returns the line of the dash at column col that opens item:
// the item line, or an earlier line with only blank lines and comments
// in between.
func dashAbove(doc *yamledit.Doc, item *yaml.Node, col int) (int, bool) {
	for n := item.Line; n >= 1; n-- {
		l := doc.Lines[n-1]

		if yamledit.LeadingSpaces(l) == col && yamledit.DashAt(l, col) {
			return n, true
		}

		if n != item.Line && !yamledit.IsBlank(l) && !doc.Comment(n) {
			return 0, false
		}
	}

	return 0, false
}

// nodeEnd returns the last line of the node n that starts on line from.
// The scan starts at the last start line in the subtree of n. It then
// takes each later line before bound that is deeper than col, such as a
// line of a multi-line value. A blank line counts only between two
// taken lines. When comments is false, a comment line ends the scan,
// because a comment after the last node is not part of the node.
func nodeEnd(doc *yamledit.Doc, from int, n *yaml.Node, col, bound int, comments bool) int {
	last := max(from, yamledit.LastLine(n))

	for i := last + 1; i < bound; i++ {
		l := doc.Lines[i-1]

		switch {
		case yamledit.IsBlank(l):
		case yamledit.IndentOf(l) > col && (comments || !doc.Comment(i)):
			last = i
		default:
			return last
		}
	}

	return last
}

// lineRule requires every line of the reviewed text outside regions to
// be in the candidate, byte for byte and in order. A region may be
// replaced by any lines in its place. At most inserts other places may
// gain new lines. A final line break stays, unless the candidate has no
// line at all. A reset of the only key leaves an empty file, which the
// reader reads as the defaults. The match is greedy: it can
// refuse a valid candidate in a contrived text, but it never accepts a
// candidate that breaks the rule.
func lineRule(before, after *yamledit.Doc, regions [][2]int, inserts int) error {
	b, c := before.Lines, after.Lines

	ends := map[int]int{}
	for _, r := range regions {
		ends[r[0]-1] = r[1]
	}

	changed := func(n int) error {
		return refusal(pinfile.File, fmt.Sprintf("line %d changed outside the edited lines", n))
	}

	i, j := 0, 0

	for i < len(b) {
		if _, ok := ends[i]; ok {
			// Skip the region, and each region that follows it directly.
			for end, ok := ends[i]; ok; end, ok = ends[i] {
				i = end
			}

			if i == len(b) {
				j = len(c)

				break
			}

			k := slices.Index(c[j:], b[i])
			if k < 0 {
				return changed(i + 1)
			}

			j += k

			continue
		}

		if j < len(c) && c[j] == b[i] {
			i, j = i+1, j+1

			continue
		}

		k := slices.Index(c[j:], b[i])
		if inserts == 0 || k < 0 {
			return changed(i + 1)
		}

		inserts--
		j += k
	}

	if j < len(c) && inserts == 0 {
		return refusal(pinfile.File, "lines were added outside the edited lines")
	}

	if before.FinalEOL && !after.FinalEOL && len(c) > 0 {
		return refusal(pinfile.File, "the final line break is gone")
	}

	return nil
}
