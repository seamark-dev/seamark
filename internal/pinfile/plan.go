package pinfile

// The planner of pin edits. A retarget changes only the region keys of
// one entry, in place. A prune deletes the own lines of one entry and
// its bound provenance lines. The planner splices the reviewed text, so
// the rule spelling, the note, the comments, the unknown keys, and the
// entry position stay as the user wrote them.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// The reasons an edit does not enter a plan. Each EditError wraps one.
var (
	// ErrUnsupported reports a layout that no splice can edit with proof,
	// or an edit request that is not valid. The user edits by hand.
	ErrUnsupported = errors.New("the entry cannot be edited safely")
	// ErrCollision reports a target key that another pin or another
	// proposal already uses. The planner never merges two pins.
	ErrCollision = errors.New("the target key is already in use")
	// ErrNoOp reports a retarget to the current region set.
	ErrNoOp = errors.New("the edit changes nothing")
	// ErrUnverified reports a candidate that fails a check after the
	// splice. The planner then cannot prove that the edit keeps the file.
	ErrUnverified = errors.New("cannot prove the edit preserves the file")
	// ErrStale reports a file or an entry that changed after it was read.
	ErrStale = errors.New("the pin file changed since it was read")
	// ErrNotWritable reports a file that the setup engine does not
	// replace: a path behind a symbolic link, or a file that this user
	// cannot write.
	ErrNotWritable = errors.New("the pin file cannot be replaced")
)

// EntryRef binds an edit to the entry that the user reviewed: its
// position in the pin sequence, its key, and the hash of its own lines.
// A later edit of the entry, or a move, makes the binding stale.
type EntryRef struct {
	Index  int
	Key    reviews.PinKey
	RawSHA [32]byte
}

// Edit is one requested change of one entry.
type Edit struct {
	Ref EntryRef
	// Delete prunes the entry. Regions is then ignored.
	Delete bool
	// Regions is the new region set of a retarget, in the order of the
	// region engine. An empty set means repository-wide.
	Regions []string
	// Provenance holds the IDs of the proposals linked to the entry. A
	// prune deletes only the provenance comments that name one of them.
	Provenance []int64
}

// Change is a planned and verified candidate file.
type Change struct {
	// Before is the reviewed file content; After is the candidate.
	Before, After []byte
	// Edits are the edits in the candidate, in request order.
	Edits []Edit
	// NewKeys holds the key of each edited entry after the change, in the
	// order of Edits. A deleted entry has the zero key.
	NewKeys []reviews.PinKey
	// Diff is the display diff from integration.UnifiedDiff: redacted and
	// sanitized. Added and Removed count the changed lines.
	Diff           string
	Added, Removed int
}

// EditError explains why one edit is not in the plan.
type EditError struct {
	Ref EntryRef
	Err error
}

// Error returns the reason.
func (e *EditError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the reason, which wraps one of the sentinel errors.
func (e *EditError) Unwrap() error {
	return e.Err
}

// EditErrors returns every EditError in an error from Plan.
func EditErrors(err error) []*EditError {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var out []*EditError

		for _, e := range joined.Unwrap() {
			out = append(out, EditErrors(e)...)
		}

		return out
	}

	var ee *EditError
	if errors.As(err, &ee) {
		return []*EditError{ee}
	}

	return nil
}

// splice replaces the 1-based lines [from, to] with lines. A splice with
// to == from-1 inserts lines before line from.
type splice struct {
	from, to int
	lines    []string
}

// planned is the state of one edit inside the planner.
type planned struct {
	edit  Edit
	entry Entry
	item  item
	// target is the key after a retarget; zero for a prune.
	target  reviews.PinKey
	splices []splice
	err     error
}

// Plan computes the candidate file for the edits on the bytes of d. It
// is pure: it parses d.Bytes again and ignores the other fields, so a
// changed Document cannot redirect an edit. otherApplied holds the
// reviews.ProposalKey of every applied row that is not linked to an
// edited entry; a retarget to one of them is a collision.
//
// Each edit is verified alone, and then all of them together. Plan
// returns the change of the edits that pass, and an error that lists an
// EditError for each other edit. A caller confirms only a plan without an
// error. A link or a file that this user cannot write does not stop
// Plan, so the user can still review the exact change; Prepare refuses
// the write.
func Plan(d *Document, edits []Edit, otherApplied []reviews.PinKey) (*Change, error) {
	if d == nil {
		return nil, errors.New("pinfile: no document")
	}

	if len(edits) == 0 {
		return nil, errors.New("pinfile: no edits")
	}

	a := analyze(d.Bytes)

	ps := make([]*planned, len(edits))
	for i, e := range edits {
		repeated := slices.ContainsFunc(edits[:i], func(o Edit) bool { return o.Ref.Index == e.Ref.Index }) ||
			slices.ContainsFunc(edits[i+1:], func(o Edit) bool { return o.Ref.Index == e.Ref.Index })

		ps[i] = a.bind(e, repeated)
	}

	a.collide(ps, otherApplied)

	// A check per edit names the edit that fails. With one edit, the
	// combined check below is the same check.
	for _, p := range ps {
		if p.err == nil && len(ps) > 1 {
			if err := a.verify(a.candidate([]*planned{p}), []*planned{p}); err != nil {
				p.err = fmt.Errorf("%w: %w", ErrUnverified, err)
			}
		}
	}

	change := a.change(d.Bytes, ps)

	var errs []error

	for _, p := range ps {
		if p.err != nil {
			errs = append(errs, &EditError{Ref: p.edit.Ref, Err: p.err})
		}
	}

	return change, errors.Join(errs...)
}

// change builds the combined candidate of the edits that passed alone.
// A combination that fails the checks blocks every one of them.
func (a *analysis) change(before []byte, ps []*planned) *Change {
	var ready []*planned

	for _, p := range ps {
		if p.err == nil {
			ready = append(ready, p)
		}
	}

	if len(ready) == 0 {
		return nil
	}

	after := a.candidate(ready)

	if err := a.verify(after, ready); err != nil {
		for _, p := range ready {
			p.err = fmt.Errorf("%w: %w", ErrUnverified, err)
		}

		return nil
	}

	c := &Change{Before: before, After: after}

	for _, p := range ready {
		c.Edits = append(c.Edits, p.edit)
		c.NewKeys = append(c.NewKeys, p.target)
	}

	c.Diff, c.Added, c.Removed = integration.UnifiedDiff(File, c.Before, c.After)

	return c
}

// fail returns an error that wraps sentinel with a plain reason.
func fail(sentinel error, reason string) error {
	return fmt.Errorf("%w: %s", sentinel, reason)
}

// bind finds the reviewed entry of e and builds its splices. repeated is
// true when another edit names the same entry.
func (a *analysis) bind(e Edit, repeated bool) *planned {
	p := &planned{edit: e}

	if a.gate != "" {
		p.err = fail(ErrUnsupported, a.gate)

		return p
	}

	k := slices.IndexFunc(a.entries, func(x Entry) bool { return x.Index == e.Ref.Index })
	if k < 0 {
		p.err = fail(ErrStale, "the entry is no longer in the file")

		return p
	}

	p.entry, p.item = a.entries[k], a.items[e.Ref.Index]

	switch {
	case p.entry.Ref() != e.Ref:
		p.err = fail(ErrStale, "the entry changed since it was read")
	case repeated:
		p.err = fail(ErrUnsupported, "the selection names the entry more than once")
	case p.entry.Block != "":
		p.err = fail(ErrUnsupported, p.entry.Block)
	case a.keyCount(p.entry.Key) > 1:
		p.err = fail(ErrCollision, "another pin carries the same key")
	case e.Delete:
		p.splices = a.prune(p)
	default:
		p.target = reviews.NewPinKey(p.entry.Rule, "", e.Regions)
		p.err = validRegions(e.Regions)

		if p.err == nil && p.target == p.entry.Key {
			p.err = fail(ErrNoOp, "the regions are already current")
		}

		if p.err == nil {
			p.splices, p.err = a.retarget(p.item, e.Regions)
		}
	}

	return p
}

// keyCount returns the number of live entries with key.
func (a *analysis) keyCount(key reviews.PinKey) int {
	n := 0

	for _, e := range a.entries {
		if e.Key == key {
			n++
		}
	}

	return n
}

// validRegions refuses a region set that no engine produces: an empty
// region, "*", white space around a region, a control character, or a
// repeated region. A region is a repository path, and the delivery code
// compares it as literal text.
func validRegions(regions []string) error {
	seen := map[string]bool{}

	for _, r := range regions {
		switch {
		case r == "" || r == "*" || strings.TrimSpace(r) != r:
			return fail(ErrUnsupported, `the new region set holds an empty region, "*", or white space around a region`)
		case strings.ContainsFunc(r, unicode.IsControl):
			return fail(ErrUnsupported, "a new region holds a control character")
		case seen[r]:
			return fail(ErrUnsupported, "the new region set repeats a region")
		}

		seen[r] = true
	}

	return nil
}

// collide marks each retarget whose target key is already in use: by
// another live entry, by another edit of the plan, or by an applied row
// outside the plan. The planner never merges two pins into one key.
func (a *analysis) collide(ps []*planned, otherApplied []reviews.PinKey) {
	zero := reviews.PinKey{}

	for i, p := range ps {
		if p.err != nil || p.edit.Delete {
			continue
		}

		switch {
		case slices.ContainsFunc(a.entries, func(x Entry) bool { return x.Index != p.entry.Index && x.Key == p.target }):
			p.err = fail(ErrCollision, "another pin already carries the key "+p.target.String())
		case slices.ContainsFunc(ps, func(q *planned) bool { return q != ps[i] && q.target != zero && q.target == p.target }):
			p.err = fail(ErrCollision, "another selected entry targets the key "+p.target.String())
		case slices.Contains(otherApplied, p.target):
			p.err = fail(ErrCollision, "an applied proposal already records the key "+p.target.String())
		}
	}
}

// candidate applies the splices of ps to the reviewed lines.
func (a *analysis) candidate(ps []*planned) []byte {
	return yamledit.JoinLines(a.spliced(ps), a.finalEOL)
}

// spliced returns the reviewed lines with the splices of ps applied. The
// splices of different entries never overlap, so the bottom-up order
// keeps every line number valid.
//
// Two splices can start on the same line, though. An insertion after
// the last key of one entry starts on the next line, and the deletion of
// the provenance line of the next entry starts there too. The deletion
// must run first: an insertion that ran first would put its lines where
// the deletion then removes them. A deletion covers that line and the
// insertion covers no line, so the order by end line, from the bottom
// up, runs the deletion first.
func (a *analysis) spliced(ps []*planned) []string {
	var all []splice

	for _, p := range ps {
		all = append(all, p.splices...)
	}

	slices.SortFunc(all, func(x, y splice) int {
		if x.from != y.from {
			return y.from - x.from
		}

		return y.to - x.to
	})

	lines := slices.Clone(a.lines)

	for _, s := range all {
		lines = slices.Replace(lines, s.from-1, s.to, s.lines...)
	}

	return lines
}

// prune returns the splices that delete the own lines of the entry and
// the provenance lines above it that name a linked proposal. Blank lines
// and comments after the entry belong to the next item or the list foot,
// so they stay.
func (a *analysis) prune(p *planned) []splice {
	out := []splice{{from: p.item.first, to: p.item.last}}

	for _, n := range a.boundProvenance(p) {
		out = append(out, splice{from: n, to: n})
	}

	return out
}

// boundProvenance returns the provenance lines above the entry of p
// whose proposal ID is linked to the entry.
func (a *analysis) boundProvenance(p *planned) []int {
	lines, ids := a.provenanceLines(p.entry.Index)

	var out []int

	for k, n := range lines {
		if slices.Contains(p.edit.Provenance, ids[k]) {
			out = append(out, n)
		}
	}

	return out
}

// retarget returns the splices that set the region keys of the entry to
// regions. The cases follow the keys that the entry has; the target
// shape is the one distill.RenderPins writes: region holds the first
// region or "*", and regions holds the set when it has more than one.
func (a *analysis) retarget(it item, regions []string) ([]splice, error) {
	n, stop := it.node, it.last+1
	region, hasRegion := a.doc.KeySpan(n, "region", stop)
	set, hasSet := a.doc.KeySpan(n, "regions", stop)
	indent := strings.Repeat(" ", a.doc.BlockIndent(n))

	first := "*"
	if len(regions) > 0 {
		first = regions[0]
	}

	switch {
	case hasRegion && hasSet && len(regions) <= 1 && a.onDashLine(set):
		return a.liftRegion(region, set, first)
	case hasRegion:
		out, err := a.setScalar(region, first)
		if err != nil {
			return nil, err
		}

		var more []splice

		switch {
		case hasSet && len(regions) > 1:
			more, err = a.setList(set, regions)
		case hasSet:
			more, err = a.removeKey(set, nil)
		case len(regions) > 1:
			more, err = a.insertAfter(region.Last, indent, nil, regions)
		}

		return append(out, more...), err
	case hasSet && len(regions) > 0:
		return a.setList(set, regions)
	case hasSet:
		text, err := encode(yamledit.BlockScalar("*"))
		if err != nil {
			return nil, err
		}

		return a.removeKey(set, []string{indent + "region: " + text})
	}

	// Case D. The set is never empty here: without region keys the entry
	// has the repository-wide key, so bind refuses an empty set as ErrNoOp.
	rule, ok := a.doc.KeySpan(n, "rule", stop)
	if !ok {
		return nil, fail(ErrUnsupported, "the entry has no rule key to place the region after")
	}

	return a.insertAfter(rule.Last, indent, &first, regions)
}

// encode turns an encoder failure into an unsupported edit.
func encode(text string, err error) (string, error) {
	if err != nil {
		return "", fail(ErrUnsupported, "a region has no safe single-line YAML text")
	}

	return text, nil
}

// setScalar replaces the inline value of s with value.
func (a *analysis) setScalar(s yamledit.Span, value string) ([]splice, error) {
	text, err := encode(yamledit.BlockScalar(value))
	if err != nil {
		return nil, err
	}

	return a.replaceInline(s, text)
}

// replaceInline replaces the inline value of s with text. The key, the
// spacing, and a trailing comment stay.
func (a *analysis) replaceInline(s yamledit.Span, text string) ([]splice, error) {
	line, ok := a.doc.ReplaceValue(s, text)
	if !ok {
		return nil, fail(ErrUnsupported, "the value is not on its key line")
	}

	if line == a.lines[s.First-1] {
		return nil, nil
	}

	return []splice{{from: s.First, to: s.First, lines: []string{line}}}, nil
}

// setList sets the regions list of s. A flow list stays one flow line.
// A block list stays a block list with its item indentation.
func (a *analysis) setList(s yamledit.Span, regions []string) ([]splice, error) {
	if s.Inline {
		text, err := encode(yamledit.FlowList(regions))
		if err != nil {
			return nil, err
		}

		return a.replaceInline(s, text)
	}

	return a.setBlockList(s, regions)
}

// setBlockList replaces the items of a block list by position. An item
// keeps its dash line and its trailing comment; new items follow the
// last item with the prefix of the first item. A removed item must hold
// no comment, because the comment would go with it.
func (a *analysis) setBlockList(s yamledit.Span, regions []string) ([]splice, error) {
	v := s.Value

	var (
		out    []splice
		prefix string
		last   = s.First
	)

	for j, n := range v.Content {
		first, end, ok := a.doc.ItemSpan(v, j, s.Last+1)
		if !ok {
			return nil, fail(ErrUnsupported, "the editor cannot find the lines of a regions item")
		}

		last = end

		if j >= len(regions) {
			if a.commentIn(first, end) {
				return nil, fail(ErrUnsupported, "removing a regions item would drop a comment")
			}

			out = append(out, splice{from: first, to: end})

			continue
		}

		start, stop, ok := a.token(n)
		if !ok {
			return nil, fail(ErrUnsupported, "a regions item is not one text value on its own line")
		}

		text, err := encode(yamledit.BlockScalar(regions[j]))
		if err != nil {
			return nil, err
		}

		line := a.lines[first-1]
		if j == 0 {
			prefix = line[:start]
		}

		if next := line[:start] + text + line[stop:]; next != line {
			out = append(out, splice{from: first, to: first, lines: []string{next}})
		}
	}

	var add []string

	for _, r := range regions[min(len(v.Content), len(regions)):] {
		text, err := encode(yamledit.BlockScalar(r))
		if err != nil {
			return nil, err
		}

		add = append(add, prefix+text)
	}

	if len(add) > 0 {
		out = append(out, splice{from: last + 1, to: last, lines: add})
	}

	return out, nil
}

// removeKey deletes the lines of the key s and puts replacement in their
// place. A comment in those lines blocks the edit: the user wrote it, and
// a removal would lose it.
//
// A key on the dash line shares that line with the dash of the entry.
// The first replacement line then takes the dash in place of its
// indentation, so the entry keeps its dash. Case A never removes such a
// key without a replacement; liftRegion handles it.
func (a *analysis) removeKey(s yamledit.Span, replacement []string) ([]splice, error) {
	if a.commentIn(s.First, s.Last) {
		return nil, fail(ErrUnsupported, "removing the regions key would drop a comment")
	}

	lines := slices.Clone(replacement)
	if len(lines) > 0 && a.onDashLine(s) {
		lines[0] = a.keyPrefix(s) + strings.TrimLeft(lines[0], " ")
	}

	return []splice{{from: s.First, to: s.Last, lines: lines}}, nil
}

// liftRegion removes the regions key from the dash line in case A. The
// region key, with its new value and its trailing comment, moves onto
// the dash line in its place. The entry keeps its dash, and only the
// lines of the two region keys change. When another key stands between
// them, the region key moves up; the verifier proves that no value
// changes.
func (a *analysis) liftRegion(region, set yamledit.Span, value string) ([]splice, error) {
	if a.commentIn(set.First, set.Last) {
		return nil, fail(ErrUnsupported, "removing the regions key would drop a comment")
	}

	// A comment line after the region key belongs to that key. It would
	// stay behind, under another key, so the move is refused.
	if region.Last != region.First {
		return nil, fail(ErrUnsupported, "moving the region key onto the dash line would separate a comment from it")
	}

	text, err := encode(yamledit.BlockScalar(value))
	if err != nil {
		return nil, err
	}

	line, ok := a.doc.ReplaceValue(region, text)
	prefix := a.keyPrefix(set)

	if !ok || yamledit.LeadingSpaces(line) != len(prefix) {
		return nil, fail(ErrUnsupported, "the region key is not on its own line at the key column")
	}

	return []splice{
		{from: set.First, to: set.Last, lines: []string{prefix + line[len(prefix):]}},
		{from: region.First, to: region.Last},
	}, nil
}

// keyPrefix returns the text before the key of s on its line: the
// indentation, and the dash when the key is the first key of the entry.
// The gate refuses tags and anchors in the pin section, so the key node
// column is the key text.
func (a *analysis) keyPrefix(s yamledit.Span) string {
	return a.lines[s.First-1][:a.doc.Offset(s.Key.Line, s.Key.Column)]
}

// onDashLine reports whether the key of s sits on the dash line.
func (a *analysis) onDashLine(s yamledit.Span) bool {
	return !yamledit.IsBlank(a.keyPrefix(s))
}

// insertAfter inserts new key lines after line at the key indentation:
// a region line when first is set, and a flow regions line when the set
// has more than one region.
func (a *analysis) insertAfter(line int, indent string, first *string, regions []string) ([]splice, error) {
	var add []string

	if first != nil {
		text, err := encode(yamledit.BlockScalar(*first))
		if err != nil {
			return nil, err
		}

		add = append(add, indent+"region: "+text)
	}

	if len(regions) > 1 {
		text, err := encode(yamledit.FlowList(regions))
		if err != nil {
			return nil, err
		}

		add = append(add, indent+"regions: "+text)
	}

	return []splice{{from: line + 1, to: line, lines: add}}, nil
}

// commentIn reports whether any line in [from, to] holds a comment, as a
// whole line or after a value.
func (a *analysis) commentIn(from, to int) bool {
	for n := from; n <= to; n++ {
		line := a.lines[n-1]

		if a.doc.Comment(n) || yamledit.CommentStart(line, 0) < len(line) {
			return true
		}
	}

	return false
}
