package maintain

// This file holds the authoring of one check definition. PlanCheckDef
// splices one checks.<id> entry, and lesson_checks_version when the
// file needs it, into the text of lessons.yaml. A verifier that shares
// no line computation with the splice proves that the pin list keeps
// its bytes and that only those two keys change. The candidate must
// then pass the strict check tier. CommitCheckDef writes the candidate
// through pinfile.Prepare, or pinfile.PrepareAppend for an absent file,
// so the writer runs the strict tier once more.
//
// The edit never changes a pin. A pin binds a definition with its own
// edit (lessons --set-mode or the pin form), so no write changes the
// pin list and a top-level key together, which pinfile refuses.

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/yamledit"
)

// The keys of lessons.yaml that a definition edit changes.
const (
	keyChecks        = "checks"
	keyChecksVersion = "lesson_checks_version"
	keyPinList       = "pin"
)

// routeCheckDef is the route of a definition that the strict tier
// refuses while the file itself passes it.
const routeCheckDef = "correct the kind or the pattern of the definition"

// CheckDefRequest is one check definition as the person typed it.
type CheckDefRequest struct {
	// ID is the check ID: the key under checks. It must match the ID
	// rule of the lesson checks, because the splice writes it as a key.
	ID string
	// Kind is the checker kind, for example "no-added-pattern/1".
	// Pattern is its parameter; an empty Pattern writes no pattern key.
	Kind    string
	Pattern string
}

// def returns the definition that the request writes.
func (r CheckDefRequest) def() reviews.CheckDef {
	return reviews.CheckDef{Kind: r.Kind, Pattern: r.Pattern}
}

// CheckDefPlan is a reviewed definition edit. It is complete even when
// it cannot be confirmed, so a review shows the change and every
// blocker. Before, After, Diff, the request texts, and the blocker texts
// hold file or typed data, so a caller sanitizes them before display.
type CheckDefPlan struct {
	Request CheckDefRequest
	// Root is the root of the pin file. Guard is the observation of the
	// file that the plan read, and Before is its content.
	Root   string
	Guard  integration.FileGuard
	Before []byte
	// After is the verified candidate, or nil when no candidate exists.
	After []byte
	// Diff is the redacted display diff; Added and Removed count lines.
	Diff           string
	Added, Removed int
	// Create is true when checks holds no entry with the ID yet.
	Create bool
	// Version is true when the edit also writes lesson_checks_version: 1.
	Version bool
	// Predicate is the lessoncheck.Describe text of the new definition,
	// or "" for a kind without a checker. Describe compiles no pattern,
	// so a pattern that the strict tier refuses still has a text, and a
	// blocker names the refusal.
	Predicate string
	// Impact holds the checker impact block of each check or enforce pin
	// that binds the ID, in file order. An advisory pin gets no block.
	Impact []lessoncheck.Impact
	// Write is distill.write at plan time; GateErr is its read failure.
	Write   bool
	GateErr error
	// NoOp is true when checks already holds the same definition under
	// a supported lesson_checks_version.
	NoOp bool
	// Blockers list every reason that the plan cannot be confirmed. The
	// kinds are those of an authoring plan.
	Blockers []AuthorBlocker
	// Confirmable is true when nothing blocks the write.
	Confirmable bool
}

// checkDefPlanner holds the state of one PlanCheckDef call.
type checkDefPlanner struct {
	deps Deps
	plan *CheckDefPlan
}

// block records one blocker.
func (c *checkDefPlanner) block(kind BlockKind, text, route string) {
	c.plan.Blockers = append(c.plan.Blockers, AuthorBlocker{Kind: kind, Text: text, Route: route})
}

// PlanCheckDef plans the definition edit of req from the current pin
// file and write gate. It writes nothing. It returns an error only for
// an empty root; every other reason goes into the plan as a blocker.
//
// An ID that the check ID rule refuses stops before the splice, because
// the splice writes the ID as a key. A kind or a pattern that the
// strict tier refuses still gets a candidate, so the review shows the
// impact on the bound pins next to the refusal.
func PlanCheckDef(d Deps, req CheckDefRequest) (*CheckDefPlan, error) {
	if d.Root == "" {
		return nil, errors.New("maintain: workspace root is empty")
	}

	c := &checkDefPlanner{deps: d, plan: &CheckDefPlan{Request: req, Root: d.Root}}
	c.plan.Write, c.plan.GateErr = readGate(d.Root)

	if doc := c.read(); doc != nil && c.checkID() {
		c.candidate(doc)
	}

	c.finish()

	return c.plan, nil
}

// read reads the pin file and records what blocks its replacement. It
// returns nil when no splice can run on the file.
func (c *checkDefPlanner) read() *pinfile.Document {
	p := c.plan

	doc, err := pinfile.Read(c.deps.Root)
	if err != nil {
		c.block(BlockFile, textFileBlock+err.Error(), routeByHand)

		return nil
	}

	p.Guard, p.Before = doc.Guard, doc.Bytes

	// FileBlock holds the structural gate, CRLF line breaks, a link, and
	// a file that this user cannot write. The definition splice keeps
	// the pin list, but the write replaces the whole file.
	if doc.FileBlock != "" {
		c.block(BlockFile, textFileBlock+doc.FileBlock, routeByHand)

		return nil
	}

	if doc.Unreadable() {
		c.block(BlockFile, textFileBlock+"it does not load", routeByHand)

		return nil
	}

	// The save replaces the file by rename, so every other name of a
	// hard-linked file keeps the old bytes.
	if b, ok := hardLinkBlock(c.deps.Root); ok {
		c.block(b.Kind, b.Text, b.Route)

		return nil
	}

	return doc
}

// checkID applies the check ID rule to the request. It asks Validate,
// so the rule and its text have one owner. It reports whether the ID
// passes.
func (c *checkDefPlanner) checkID() bool {
	id := c.plan.Request.ID
	cfg := &reviews.Config{
		LessonChecksVersion: reviews.MaxLessonChecksVersion,
		Checks:              map[string]reviews.CheckDef{id: c.plan.Request.def()},
	}

	for _, e := range lessoncheck.Validate(cfg) {
		if e.Code == lessoncheck.CodeCheckIDInvalid {
			c.block(BlockCheck, e.Message, routeCheckDef)

			return false
		}
	}

	return true
}

// candidate splices, verifies, and checks the candidate of the request.
func (c *checkDefPlanner) candidate(doc *pinfile.Document) {
	p := c.plan

	before, err := reviews.ParseConfig(p.Before)
	if err != nil {
		c.block(BlockFile, textFileBlock+err.Error(), routeByHand)

		return
	}

	old, exists := before.Checks[p.Request.ID]
	p.Create = !exists

	if exists && sameDef(old, p.Request.def()) && before.LessonChecksVersion == reviews.MaxLessonChecksVersion {
		p.NoOp = true

		return
	}

	after, version, err := spliceCheckDef(p.Before, p.Request, before.LessonChecksVersion)
	if err == nil {
		err = verifyCheckDef(p.Before, after, p.Request)
	}

	if err != nil {
		c.block(BlockFile, err.Error(), routeByHand)

		return
	}

	p.After, p.Version = after, version
	p.Diff, p.Added, p.Removed = integration.UnifiedDiff(pinfile.File, p.Before, after)

	if text, err := lessoncheck.Describe(p.Request.def()); err == nil {
		p.Predicate = text
	}

	c.strict(before, after)
}

// strict adds the impact on the bound pins and refuses a candidate that
// fails the strict check tier. The impact reads the lenient decodes, so
// it shows also next to a refusal.
func (c *checkDefPlanner) strict(before *reviews.Config, after []byte) {
	p := c.plan

	next, err := reviews.ParseConfig(after)
	if err != nil {
		c.block(BlockFile, textFileBlock+"the candidate does not load: "+err.Error(), routeByHand)

		return
	}

	// The edit keeps the pin list, so each pin keeps its position.
	var bound []int

	for i, pin := range next.Pin {
		if pin.Check == p.Request.ID {
			bound = append(bound, i+1)
		}
	}

	impact, err := lessoncheck.ImpactOf(before, next, bound)
	if err != nil {
		c.block(BlockFile, err.Error(), routeByHand)

		return
	}

	p.Impact = impact

	reason := pinfile.CandidateRefusal(next)
	if reason == "" {
		return
	}

	// An error that the file already holds needs a manual fix first.
	route := routeCheckDef
	if len(lessoncheck.Validate(before)) > 0 {
		route = routeByHand
	}

	c.block(BlockCheck, reason, route)
}

// finish adds the blockers of the root and the gate and decides whether
// the plan can be confirmed. The gate comes last, because a separate
// settings save fixes it.
func (c *checkDefPlanner) finish() {
	p := c.plan

	if c.deps.ForeignRoot != nil {
		c.block(BlockForeignRoot, c.deps.ForeignRoot.Error(), routeOwnIndex)
	}

	if b := gateBlocker(p.Write, p.GateErr); b != nil {
		p.Blockers = append(p.Blockers, *b)
	}

	p.Confirmable = len(p.Blockers) == 0 && p.After != nil && !p.NoOp
}

// sameDef reports whether two definitions are equal in every field that
// a reader decodes, unknown keys included.
func sameDef(a, b reviews.CheckDef) bool {
	return a.Kind == b.Kind && a.Pattern == b.Pattern && len(a.Unknown) == 0 && len(b.Unknown) == 0
}

// CommitCheckDef writes a confirmed definition plan. The write replaces
// the file only while it still holds the reviewed bytes, mode, and link
// state: the guarded write compares them with the plan guard and checks
// them again right before the rename. The write gate decides at the
// commit too. The hard-link refusal of the plan repeats at the commit,
// because the guard holds no link count and a rename splits a file that
// gained a link after the preview. A non-nil error means that nothing
// was written. After the write starts, the result holds the outcome, as
// for an authoring commit.
func CommitCheckDef(d Deps, p *CheckDefPlan) (AuthorResult, error) {
	refuse := func(err error) (AuthorResult, error) {
		return AuthorResult{Outcome: NotWritten, Err: err}, err
	}

	switch {
	case p == nil:
		return refuse(errors.New("maintain: no plan"))
	case d.ForeignRoot != nil:
		return refuse(d.ForeignRoot)
	}

	if err := confirmRefusal(p.Confirmable, p.Write, p.GateErr); err != nil {
		return refuse(err)
	}

	write, gateErr := readGate(d.Root)
	if err := confirmRefusal(write && gateErr == nil, write, gateErr); err != nil {
		return refuse(err)
	}

	// A link that appears after the preview makes the review stale, as
	// in the authoring commit.
	if b, ok := hardLinkBlock(d.Root); ok {
		err := &StaleError{Differences: []string{b.String()}}

		return refuse(err)
	}

	return writeAuthored(d, p.Guard, p.After, !p.Guard.Exists)
}

// hardLinkBlock returns the blocker of a pin file that a rename would
// split, and whether the file blocks. It uses the check of the plan,
// integration.HardLinked, so the plan and the commit refuse alike. A
// failed check also blocks, because the commit cannot prove the file safe.
func hardLinkBlock(root string) (AuthorBlocker, bool) {
	linked, err := integration.HardLinked(root, pinfile.File)

	switch {
	case err != nil:
		return AuthorBlocker{Kind: BlockFile, Text: err.Error(), Route: routeByHand}, true
	case linked:
		return AuthorBlocker{Kind: BlockFile, Text: integration.HardLinkReason, Route: routeByHand}, true
	}

	return AuthorBlocker{}, false
}

// spliceCheckDef returns the text of data with the definition of req
// under checks and with lesson_checks_version 1. version is the decoded
// version of data, and wrote reports whether the splice writes the
// version key. The splice refuses a layout that it cannot change without
// a loss, for example a comment inside the replaced entry, and names the
// reason. An existing version other than 0 stays: a newer version keeps
// its keys, and the strict tier refuses it.
func spliceCheckDef(data []byte, req CheckDefRequest, version int) (after []byte, wrote bool, err error) {
	entry, err := checkDefLines(req)
	if err != nil {
		return nil, false, err
	}

	cur := data

	if version == 0 {
		cur, err = spliceVersion(cur)
		if err != nil {
			return nil, false, err
		}

		wrote = true
	}

	doc, err := parseCheckDef(cur)
	if err != nil {
		return nil, false, err
	}

	sk, sv, idx := doc.Section(keyChecks)

	switch {
	case sk == nil:
		return doc.AppendSection(keyChecks, entry), wrote, nil
	case yamledit.IsNull(sv):
		return doc.InsertChild(keyChecks, entry), wrote, nil
	case sv.Kind != yaml.MappingNode || sv.Style&yaml.FlowStyle != 0:
		return nil, false, checkDefBlock("checks is not a block mapping", sk.Line)
	}

	if issue := yamledit.SubtreeIssue(keyChecks, sv); issue != nil {
		return nil, false, checkDefBlock(issue.Reason, issue.Line)
	}

	span, present := doc.KeySpan(sv, req.ID, doc.SectionEnd(idx)+1)
	if !present {
		return doc.InsertChild(keyChecks, entry), wrote, nil
	}

	// The comments are the user's prose, so a replace never drops one.
	if doc.CommentIn(span.First, span.Last) {
		return nil, false, checkDefBlock("a comment sits inside checks."+req.ID, span.First)
	}

	lines := slices.Replace(slices.Clone(doc.Lines), span.First-1, span.Last,
		yamledit.IndentLines(entry, span.Key.Column-1)...)

	return yamledit.JoinLines(lines, doc.FinalEOL), wrote, nil
}

// spliceVersion sets lesson_checks_version to 1. An absent key goes
// where a new top-level key goes. An explicit 0 means absent, so its
// value changes in place, and the comment on its line stays.
func spliceVersion(data []byte) ([]byte, error) {
	doc, err := parseCheckDef(data)
	if err != nil {
		return nil, err
	}

	span, present := doc.KeySpan(doc.Root, keyChecksVersion, doc.Limit)
	if !present {
		return doc.AppendTopLevel([]string{fmt.Sprintf("%s: %d", keyChecksVersion, reviews.MaxLessonChecksVersion)}), nil
	}

	line, ok := doc.ReplaceValue(span, fmt.Sprint(reviews.MaxLessonChecksVersion))
	if !ok {
		return nil, checkDefBlock("the value of "+keyChecksVersion+" has no safe splice", span.First)
	}

	lines := slices.Clone(doc.Lines)
	lines[span.First-1] = line

	return yamledit.JoinLines(lines, doc.FinalEOL), nil
}

// checkDefLines returns the entry of req without indentation: the ID
// key, then kind and pattern one level deeper. The encoder quotes a
// value that a reader would read as another type, such as an ID "1".
func checkDefLines(req CheckDefRequest) ([]string, error) {
	id, err := yamledit.BlockScalar(req.ID)
	if err != nil {
		return nil, fmt.Errorf("the check ID has no safe YAML text: %w", err)
	}

	kind, err := yamledit.BlockScalar(req.Kind)
	if err != nil {
		return nil, fmt.Errorf("the kind has no safe YAML text: %w", err)
	}

	lines := []string{id + ":", "  kind: " + kind}

	if req.Pattern != "" {
		pattern, err := yamledit.BlockScalar(req.Pattern)
		if err != nil {
			return nil, fmt.Errorf("the pattern has no safe YAML text: %w", err)
		}

		lines = append(lines, "  pattern: "+pattern)
	}

	return lines, nil
}

// parseCheckDef parses an intermediate text of the definition splice.
func parseCheckDef(data []byte) (*yamledit.Doc, error) {
	doc, issue := yamledit.Parse(data)
	if issue != nil {
		return nil, checkDefBlock(issue.Reason, issue.Line)
	}

	return doc, nil
}

// checkDefBlock names a splice refusal and the line to open.
func checkDefBlock(reason string, line int) error {
	if line > 0 {
		return fmt.Errorf("%s cannot be changed here: %s (line %d)", pinfile.File, reason, line)
	}

	return fmt.Errorf("%s cannot be changed here: %s", pinfile.File, reason)
}

// verifyCheckDef is the safety net under the definition splice. It
// proves that the pin list keeps its bytes, that every comment stays in
// order, that no other top-level key and no other check changes, and
// that the reader reads exactly the requested definition. It reads both
// texts again and uses no line number of the splice, so a defect of the
// splice cannot hide from it.
func verifyCheckDef(before, after []byte, req CheckDefRequest) error {
	was, err := parseCheckDef(before)
	if err != nil {
		return err
	}

	now, err := parseCheckDef(after)
	if err != nil {
		return checkDefBlock("the edited text does not parse", 0)
	}

	if pinListText(was) != pinListText(now) {
		return checkDefBlock("the pin list changed", 0)
	}

	if !slices.Equal(comments(was), comments(now)) {
		return checkDefBlock("a comment changed", 0)
	}

	pairs := func(d *yamledit.Doc) [][2]*yaml.Node {
		p, _ := yamledit.PairsExcept(d.Root, keyChecks, keyChecksVersion)

		return p
	}

	if _, same := yamledit.SamePairs(pairs(was), pairs(now)); !same {
		return checkDefBlock("another top-level key changed", 0)
	}

	checks := func(d *yamledit.Doc) [][2]*yaml.Node {
		_, v, _ := d.Section(keyChecks)
		p, _ := yamledit.PairsExcept(v, req.ID)

		return p
	}

	if _, same := yamledit.SamePairs(checks(was), checks(now)); !same {
		return checkDefBlock("another check definition changed", 0)
	}

	return intendedCheckDef(before, after, req)
}

// intendedCheckDef proves that the reader reads the requested definition
// and version and every other decoded value as before.
func intendedCheckDef(before, after []byte, req CheckDefRequest) error {
	b, err := reviews.ParseConfig(before)
	if err != nil {
		return checkDefBlock("the file does not load: "+err.Error(), 0)
	}

	a, err := reviews.ParseConfig(after)
	if err != nil {
		return checkDefBlock("the lessons reader would reject the result: "+err.Error(), 0)
	}

	got, ok := a.Checks[req.ID]
	if !ok || !sameDef(got, req.def()) {
		return checkDefBlock("the reader does not read the requested definition", 0)
	}

	wantVersion := b.LessonChecksVersion
	if wantVersion == 0 {
		wantVersion = reviews.MaxLessonChecksVersion
	}

	if a.LessonChecksVersion != wantVersion {
		return checkDefBlock("the reader does not read the requested lesson_checks_version", 0)
	}

	// The rest of the decoded file must stay equal. A Go map and a nil
	// map read the same, so an empty checks map compares as nil.
	rest := func(c *reviews.Config) *reviews.Config {
		out := *c
		out.LessonChecksVersion = 0
		out.Checks = maps.Clone(c.Checks)
		delete(out.Checks, req.ID)

		if len(out.Checks) == 0 {
			out.Checks = nil
		}

		return &out
	}

	if !reflect.DeepEqual(rest(b), rest(a)) {
		return checkDefBlock("the reader reads another change", 0)
	}

	return nil
}

// pinListText returns the lines of the pin list pair, or "" without one.
func pinListText(d *yamledit.Doc) string {
	span, ok := d.KeySpan(d.Root, keyPinList, d.Limit)
	if !ok {
		return ""
	}

	return strings.Join(d.Lines[span.First-1:span.Last], "\n")
}

// comments returns every comment text of d in line order: comment lines
// and the comments after the value text of a line.
func comments(d *yamledit.Doc) []string {
	var out []string

	for n := 1; n <= len(d.Lines); n++ {
		if c := strings.TrimSpace(d.LineComment(n)); c != "" {
			out = append(out, c)
		}
	}

	return out
}
