package settings

// The inspection of the lessons document. .seamark/lessons.yaml holds
// the lesson delivery keys, the mute list, and the pin list. Settings
// edits the first two. The pin editors (internal/pinfile and the pin
// authoring of internal/maintain) own the pin list, so a lessons plan
// never changes a pin byte.
//
// The lessons document has its own guard, block, reader, and structural
// issues. A problem in config.yaml never blocks a lessons group, and a
// problem in lessons.yaml never blocks a config.yaml group.

import (
	"cmp"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// LessonsPart is one read-only observation of lessons.yaml.
type LessonsPart struct {
	// Path is the absolute file under the inspected root. The caller
	// passes the effective root, so this is the file that Pins lists.
	Path string
	// Guard is the observation that every lessons plan binds to.
	Guard integration.FileGuard
	// Exists is true when the file exists.
	Exists bool
	// Blocked holds the reason that stops a save of every lessons group:
	// the file is unreadable, linked, hard-linked, or not writable for
	// this user. Line breaks other than LF (CRLF, bare CR, mixed) and a
	// failed structural gate also block every lessons group. A save of
	// either group replaces the whole file, so a file problem blocks both.
	// BlockedAt is the 1-based line to open, or 0.
	Blocked   error
	BlockedAt int
	// ReaderErr is the failure of reviews.ParseConfig on the file bytes,
	// the reader of every lesson consumer. The editor never repairs a
	// file that the reader rejects, so it blocks both lessons groups.
	ReaderErr error
	// Config is the decoded file; nil when the reader fails.
	Config *reviews.Config
	// Mutes lists every item of the mute list in file order, null items
	// included. An item of a flow list has no line span, so its RawSHA is
	// zero; every mute operation refuses a flow list.
	Mutes []MuteEntry

	// data is the observed file content (LF form); plans start from it.
	data []byte
	// doc is the parsed file; nil when the file cannot be parsed.
	doc *yamledit.Doc
	// issues holds the structural problem of each lessons group.
	issues map[Group]*BlockedError
}

// MuteEntry is one item of the mute list. Rule and Region hold the
// decoded values as the reader decodes them. They are file text: a view
// sanitizes them before display.
type MuteEntry struct {
	// Index is the position of the item in the YAML sequence. A null
	// item counts, so Index can differ from the index in
	// reviews.Config.Mute, which drops null items.
	Index int
	// Line is the 1-based dash line of the item.
	Line int
	// Rule and Region are the decoded values; "" when absent.
	Rule, Region string
	// RawSHA is the SHA-256 of the item lines, from the dash line to the
	// last line of the item. A MuteRef binds an edit to this text.
	RawSHA [32]byte
	// NoOp is true when the entry matches nothing: it has neither a rule
	// nor a region, as a null item or "- {}".
	NoOp bool
	// DuplicateOf is the Index of an earlier entry with the same rule
	// (any case) and exactly the same region, or -1.
	DuplicateOf int
	// Flow is true for an item in flow style, such as "- {rule: a}", and
	// for each item of a flow list. A change refuses a flow item; a
	// removal works in a block list.
	Flow bool
	// Extra lists the keys of the item that the reader does not know, in
	// file order. A change keeps them.
	Extra []string
}

// Ref returns the reference that binds an edit to this entry.
func (e MuteEntry) Ref() MuteRef {
	return MuteRef{Index: e.Index, RawSHA: e.RawSHA}
}

// Lessons document keys that are not fields.
const (
	keyMute = string(Mutes)
	keyPin  = "pin"
)

// lessonsBlock returns a BlockedError that names lessons.yaml.
func lessonsBlock(reason string, line int) *BlockedError {
	return &BlockedError{Reason: reason, Line: line, File: pinfile.File}
}

// inspectLessons reads lessons.yaml under root. It writes nothing. It
// returns the lessons part and the states of the lessons fields.
func inspectLessons(root string) (*LessonsPart, []FieldState) {
	lp := &LessonsPart{
		Path:   filepath.Join(root, filepath.FromSlash(pinfile.File)),
		issues: map[Group]*BlockedError{},
	}

	// ReadInput follows a link for the read, as the reader does, and
	// records it. The link then blocks every write below.
	guard, raw, err := integration.ReadInput(root, pinfile.File)
	if err != nil {
		// The reader fails on an unreadable file, so every value is
		// unknown. The fields still carry their defaults for display.
		lp.ReaderErr = err
		lp.block(lessonsBlock(render.Sanitize(err.Error()), 0))
		fields := lp.fields()

		return lp, fields
	}

	lp.Guard, lp.Exists = guard, guard.Exists

	breaks, crlf, gate := lp.load(raw)
	lp.block(lessonsGate(root, guard, breaks, crlf, gate))
	fields := lp.fields()

	return lp, fields
}

// load reads the bytes raw into the part: the reader values, the parsed
// document, the mute entries, and the structural issues. It returns the
// line-break issue, the CRLF flag, and the gate issue for the block.
// Inspect loads the file; the verifier loads a candidate the same way.
func (lp *LessonsPart) load(raw []byte) (breaks *yamledit.Issue, crlf bool, gate *yamledit.Issue) {
	lp.issues = map[Group]*BlockedError{}

	// The reader decodes the raw bytes, as LoadConfig does. The editor
	// works on the LF form.
	lp.Config, lp.ReaderErr = reviews.ParseConfig(raw)
	lp.data, crlf, breaks = yamledit.NormalizeBreaks(raw)

	if breaks == nil {
		lp.doc, gate = yamledit.Parse(lp.data)
	}

	if lp.doc != nil {
		lp.Mutes = muteEntries(lp.doc)
		lp.analyze()
	}

	return breaks, crlf, gate
}

// lessonsGate returns the reason that blocks every lessons group, or
// nil. The order puts the reason that the user can fix first.
func lessonsGate(root string, guard integration.FileGuard, breaks *yamledit.Issue, crlf bool, gate *yamledit.Issue) *BlockedError {
	// The engine owns the refusal of a link and of a file that this user
	// cannot write. The view shows the same reason before a write.
	if refusal := integration.WriteRefusal(guard); refusal != "" {
		return lessonsBlock(refusal, 0)
	}

	switch {
	case breaks != nil:
		return lessonsBlock(breaks.Reason, breaks.Line)
	case crlf:
		// Every Phase 4 write refuses CRLF (P4-6): the pin editors refuse
		// it, and a mute edit of the same file must agree with them.
		return lessonsBlock("the file uses CRLF line breaks", 0)
	case gate != nil:
		return lessonsBlock(gate.Reason, gate.Line)
	}

	return hardLinkBlock(root)
}

// hardLinkBlock refuses a lessons.yaml with more than one hard link
// (P4-17). The save replaces the file by rename, and every other name of
// the old file would keep the old bytes. It returns nil when the file
// has one link or does not exist.
func hardLinkBlock(root string) *BlockedError {
	linked, err := integration.HardLinked(root, pinfile.File)

	switch {
	case err != nil:
		return lessonsBlock(render.Sanitize(err.Error()), 0)
	case linked:
		return lessonsBlock(integration.HardLinkReason, 0)
	}

	return nil
}

// block records the block of every lessons group. A nil block is no
// block.
func (lp *LessonsPart) block(b *BlockedError) {
	if b == nil {
		return
	}

	lp.Blocked, lp.BlockedAt = b, b.Line
}

// analyze records the structural problems of each lessons group. An
// alias, an anchor, a merge key, or a duplicate key in an edited key
// makes the effect of an edit ambiguous. A flow mute list has no line
// per item, so every mute operation refuses it.
func (lp *LessonsPart) analyze() {
	for _, f := range GroupDelivery.Fields() {
		if issue := keyIssue(lp.doc, string(f)); issue != nil {
			lp.issues[GroupDelivery] = issue

			break
		}
	}

	if issue := keyIssue(lp.doc, keyMute); issue != nil {
		lp.issues[GroupMutes] = issue

		return
	}

	_, v, _ := lp.doc.Section(keyMute)

	switch {
	case v == nil || yamledit.IsNull(v):
	case v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle != 0:
		lp.issues[GroupMutes] = lessonsBlock("the mute list uses flow style [ ]; a mute edit needs one item per line", v.Line)
	case v.Kind != yaml.SequenceNode:
		lp.issues[GroupMutes] = lessonsBlock("the mute key does not hold a list", v.Line)
	}
}

// keyIssue explains why one top-level key of lessons.yaml cannot be
// edited, or returns nil. An absent key has no issue.
func keyIssue(doc *yamledit.Doc, name string) *BlockedError {
	key, value, _ := doc.Section(name)
	if key == nil {
		return nil
	}

	if key.Anchor != "" {
		return lessonsBlock(fmt.Sprintf("the %s key carries an anchor", name), key.Line)
	}

	if issue := yamledit.SubtreeIssue(name, value); issue != nil {
		return lessonsBlock(issue.Reason, issue.Line)
	}

	return nil
}

// editable returns nil when the lessons group g can be planned, or the
// reason it cannot. A nil part means that Inspect did not read the file.
func (lp *LessonsPart) editable(g Group) error {
	if lp == nil {
		return lessonsBlock("the file was not inspected", 0)
	}

	if lp.Blocked != nil {
		return lp.Blocked
	}

	// The editor never repairs a file that the reader rejects, as for
	// config.yaml. A reader error can quote the rejected value, so it
	// gets the redaction of the stored values.
	if lp.ReaderErr != nil {
		return lessonsBlock("the lessons reader rejects the file: "+displayText(lp.ReaderErr.Error()), yamledit.ErrorLine(lp.ReaderErr))
	}

	if issue := lp.issues[g]; issue != nil {
		return issue
	}

	return nil
}

// muteEntries lists the items of the mute list in file order. Decoding
// each item with the reader struct gives the values that the reader
// sees, for example the text "7" for an item "region: 7".
func muteEntries(doc *yamledit.Doc) []MuteEntry {
	span, ok := doc.KeySpan(doc.Root, keyMute, doc.Limit)
	if !ok || span.Value.Kind != yaml.SequenceNode {
		return nil
	}

	seq := span.Value
	flow := seq.Style&yaml.FlowStyle != 0
	out := make([]MuteEntry, 0, len(seq.Content))

	for i, item := range seq.Content {
		e := MuteEntry{Index: i, Line: item.Line, DuplicateOf: -1, Flow: flow}

		if first, last, ok := doc.ItemSpan(seq, i, span.Last+1); ok {
			e.Line = first
			e.RawSHA = itemSHA(doc, first, last)
		}

		// A scalar item fails the reader too, so the values stay empty;
		// Editable shows the reader error.
		var m reviews.MuteRule
		if err := item.Decode(&m); err == nil {
			e.Rule, e.Region = m.Rule, m.Region
		}

		if item.Kind == yaml.MappingNode {
			e.Flow = e.Flow || item.Style&yaml.FlowStyle != 0

			for j := 0; j+1 < len(item.Content); j += 2 {
				if name := item.Content[j].Value; name != "rule" && name != "region" {
					e.Extra = append(e.Extra, name)
				}
			}
		}

		e.NoOp = e.Rule == "" && e.Region == ""
		e.DuplicateOf = duplicateIndex(out, e.Rule, e.Region, -1)

		out = append(out, e)
	}

	return out
}

// itemSHA hashes the lines first through last (1-based, inclusive).
func itemSHA(doc *yamledit.Doc, first, last int) [32]byte {
	return sha256.Sum256([]byte(strings.Join(doc.Lines[first-1:last], "\n")))
}

// duplicateIndex returns the Index of the first entry, other than the
// entry at index skip, that holds the same mute, or -1. Two mutes are
// the same when the rules are equal in any case and the regions are
// equal as stored. The matcher folds the case of a rule. It reads "api/"
// and "api" as different regions. An entry that matches nothing is
// never a duplicate.
func duplicateIndex(entries []MuteEntry, rule, region string, skip int) int {
	if rule == "" && region == "" {
		return -1
	}

	for _, e := range entries {
		if e.Index != skip && !e.NoOp && strings.EqualFold(e.Rule, rule) && e.Region == region {
			return e.Index
		}
	}

	return -1
}

// Duplicate returns the entry, other than the entry at index skip, that
// holds the same mute as rule and region, as the planner refuses it. A
// form calls it to show the refusal before the review. Use -1 as skip
// for a new entry.
func (lp *LessonsPart) Duplicate(rule, region string, skip int) (MuteEntry, bool) {
	if i := duplicateIndex(lp.Mutes, rule, region, skip); i >= 0 {
		return lp.Mutes[i], true
	}

	return MuteEntry{}, false
}

// Overlaps returns the entries, other than the entry at index skip, that
// hide a lesson that a mute of rule and region also hides. For example,
// "api" and "api/" overlap, and so do "W1 in api" and "api/". A
// rule-only mute and a region-only mute are not reported. Almost every
// such pair hides a common lesson, so a note about it would say nothing.
func (lp *LessonsPart) Overlaps(rule, region string, skip int) []MuteEntry {
	mine := reviews.MuteRule{Rule: rule, Region: region}

	var out []MuteEntry

	for _, e := range lp.Mutes {
		if e.Index == skip || e.NoOp || (rule == "" && region == "") {
			continue
		}

		if overlap(mine, reviews.MuteRule{Rule: e.Rule, Region: e.Region}) {
			out = append(out, e)
		}
	}

	return out
}

// anyText is a rule and a path name that no mute names: YAML text
// cannot hold a NUL character.
const anyText = "\x00"

// overlap reports whether the mutes a and b hide a common lesson. The
// caller passes two mutes that are not empty. The check asks the
// production matcher (reviews.Config.MuteMatches) about sample lessons,
// so it cannot disagree with the matcher.
//
// The sample rule is the rule of a, else the rule of b, else a rule that
// no mute names. The sample paths are each region and a path one level
// under it, else a path that no mute names. A region mute hides a path
// and every path under it. So a common lesson exists only when both
// mutes hide one of these samples.
func overlap(a, b reviews.MuteRule) bool {
	// A mute that is not empty and has no region names a rule only. A
	// mute that is not empty and has no rule names a region only.
	ruleOnly := func(m reviews.MuteRule) bool { return m.Region == "" }
	regionOnly := func(m reviews.MuteRule) bool { return m.Rule == "" }

	if ruleOnly(a) && regionOnly(b) || regionOnly(a) && ruleOnly(b) {
		return false
	}

	symptom := cmp.Or(a.Rule, b.Rule, anyText)

	var paths []string

	for _, r := range []string{a.Region, b.Region} {
		if r != "" {
			paths = append(paths, r, strings.TrimSuffix(r, "/")+"/"+anyText)
		}
	}

	if len(paths) == 0 {
		paths = []string{anyText}
	}

	cfg := &reviews.Config{Mute: []reviews.MuteRule{a, b}}

	for _, p := range paths {
		if len(cfg.MuteMatches(model.Lesson{Symptom: symptom, Region: p})) == 2 {
			return true
		}
	}

	return false
}

// ErrEmptyMute reports a mute without a rule and without a region. The
// matcher reads such an entry as matching nothing, so the editor never
// writes one.
var ErrEmptyMute = errors.New("a mute needs a rule, a region, or both")

// MuteInput is a mute that a person typed, after CheckMute. Region is
// the region to store. Notes name each change from the typed region.
type MuteInput struct {
	Rule, Region string
	Notes        []string
}

// CheckMute checks a mute that a person typed. The record form and the
// planner both call it, so the form shows the verdict of the save. The
// rule must pass reviews.CheckAuthoredText; the region, when typed, must
// pass reviews.CheckMuteRegion; one of the two must be set. The errors
// wrap reviews.ErrAuthoredText, reviews.ErrRegionInput, or ErrEmptyMute,
// so a form can show each next to its field.
func CheckMute(rule, region string) (MuteInput, error) {
	if err := reviews.CheckAuthoredText(reviews.TextMuteRule, rule); err != nil {
		return MuteInput{}, err
	}

	in := MuteInput{Rule: rule}

	if region != "" {
		r, err := reviews.CheckMuteRegion(region)
		if err != nil {
			return MuteInput{}, err
		}

		in.Region, in.Notes = r.Value, r.Notes
	}

	if in.Rule == "" && in.Region == "" {
		return MuteInput{}, ErrEmptyMute
	}

	return in, nil
}

// Display text of the lessons fields.
const (
	textAlways   = string(reviews.HookDeliveryAlways)
	textHookCap  = "pins per edit (the hook total cap)"
	textDefaultN = "default"
)

// fields builds the state of every lessons field. The stored text comes
// from the YAML node, because the reader overwrites a nonpositive
// threshold. The effective values come from their owners: the reader,
// the budget accessors, and the hook budget of internal/report.
func (lp *LessonsPart) fields() []FieldState {
	lessons := documentFields(pinfile.File)
	out := make([]FieldState, 0, len(lessons))

	for _, f := range lessons {
		st := FieldState{Field: f, Default: lessonsDefault(f)}

		var explicit bool

		st.Stored, explicit = lp.stored(f)

		if explicit {
			st.Source = SourceFile
		}

		if st.Err = lp.ReaderErr; st.Err != nil {
			st.Effective = textUnknown
		} else {
			st.Effective, st.Notes = lp.effective(f, st.Stored, explicit)
		}

		out = append(out, st)
	}

	return out
}

// lessonsDefault returns the default text of a lessons field, from the
// constants of the reader.
func lessonsDefault(f Field) string {
	switch f {
	case Threshold:
		return strconv.Itoa(reviews.DefaultThreshold)
	case PinBudget:
		return strconv.Itoa(reviews.DefaultPinBudget)
	case ChangeBudget:
		return strconv.Itoa(reviews.DefaultChangeBudget)
	case HookDelivery:
		return textAlways
	case Mutes:
		return textNone
	default:
		return ""
	}
}

// stored returns the raw state of one top-level key, and whether it
// holds a value that is not null.
func (lp *LessonsPart) stored(f Field) (Stored, bool) {
	if lp.doc == nil {
		return Stored{}, false
	}

	k, v, _ := lp.doc.Section(string(f))
	if k == nil {
		return Stored{}, false
	}

	st := Stored{Present: true, Raw: rawText(v), Line: k.Line}

	// A list of mappings has no one-line text: the entries are the
	// display.
	if f == Mutes && v.Kind == yaml.SequenceNode {
		st.Raw = entries(len(v.Content))
	}

	return st, !yamledit.IsNull(v)
}

// entries renders an entry count.
func entries(n int) string {
	if n == 1 {
		return "1 entry"
	}

	return strconv.Itoa(n) + " entries"
}

// effective returns the effective text of one lessons field and the
// notes that explain a stored value that the reader reads differently.
func (lp *LessonsPart) effective(f Field, stored Stored, explicit bool) (text string, notes []string) {
	cfg := lp.Config

	switch f {
	case Threshold:
		return lp.number(f, stored, explicit, cfg.Threshold, "")
	case PinBudget:
		return lp.number(f, stored, explicit, report.HookBudget(cfg).Clamped().Pins, textHookCap)
	case ChangeBudget:
		return lp.number(f, stored, explicit, cfg.ChangeSetBudget(), "")
	case HookDelivery:
		return string(cfg.HookDelivery()), nil
	case Mutes:
		if len(cfg.Mute) == 0 {
			return textNone, nil
		}

		return entries(len(cfg.Mute)), nil
	default:
		return textUnknown, nil
	}
}

// Number returns the stored number of threshold, pin_budget, or
// change_budget that the reader uses. It is false when the key is
// absent or null, or when the node is not a number. It is also false
// when the reader uses its default in place of a stored 0 or a negative
// value.
// The console marks its number choice with it, because Config.Threshold
// already holds the default in place of a nonpositive stored value.
func (lp *LessonsPart) Number(f Field) (int, bool) {
	n, ok := lp.storedNumber(f)
	if !ok || n <= 0 {
		return 0, false
	}

	return n, true
}

// storedNumber decodes the node of a number field as the reader decodes
// it, before the reader replaces a nonpositive value with the default.
func (lp *LessonsPart) storedNumber(f Field) (int, bool) {
	if lp == nil || lp.doc == nil {
		return 0, false
	}

	switch f {
	case Threshold, PinBudget, ChangeBudget:
	default:
		return 0, false
	}

	_, v, _ := lp.doc.Section(string(f))
	if v == nil || yamledit.IsNull(v) {
		return 0, false
	}

	var n int
	if err := v.Decode(&n); err != nil {
		return 0, false
	}

	return n, true
}

// number returns the effective text of a number field and a note when
// the stored text reads as another value. The note keeps the stored text
// as written. Examples are "stored 0 → default 3" and "stored 100 → at
// most 8 pins per edit (the hook total cap)". The parameter capped names
// the unit of a value that a cap lowers.
func (lp *LessonsPart) number(f Field, stored Stored, explicit bool, eff int, capped string) (text string, notes []string) {
	text = strconv.Itoa(eff)

	if !explicit {
		return text, nil
	}

	// The node decodes as the reader decodes it, before the reader
	// replaces a nonpositive value with the default.
	n, ok := lp.storedNumber(f)
	if !ok {
		return text, nil
	}

	var note string

	switch {
	case n <= 0:
		note = textDefaultN + " " + text
	case n > eff && capped != "":
		note = "at most " + text + " " + capped
	case stored.Raw != strconv.Itoa(n):
		note = text
	default:
		return text, nil
	}

	return text, []string{fmt.Sprintf("stored %s → %s", stored.Raw, note)}
}
