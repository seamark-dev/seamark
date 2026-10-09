package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovCardLines is the content height of a summary card in the 1a design.
// A card with a stale note or a long reason grows, so no fact is hidden.
const ovCardLines = 3

// ovNoteLines is the most lines that one reason, for example a read
// error, takes in a card. A longer reason ends with an ellipsis, so the
// cards never push the attention list off the screen.
const ovNoteLines = 2

// ovCard is one summary card: an upper-case title, the state at the
// right of the top border, and the styled content lines.
type ovCard struct {
	title string
	right string
	lines []string
}

// ovCardWidths returns the outer card widths for w cells. The first
// three cards share the width evenly and the last one takes the rest,
// which gives 29 + 29 + 29 + 30 cells at 120 columns, as in 1a.
func ovCardWidths(w int) [4]int {
	cw := (w - 3) / 4

	return [4]int{cw, cw, cw, w - 3*cw - 3}
}

// cards returns the four summary cards in display order for w cells.
// Each card picks its wording for its own content width.
func (o *overview) cards(w int) [4]ovCard {
	widths := ovCardWidths(w)

	return [4]ovCard{
		o.indexCard(ui.BoxInner(widths[0])), o.navigateCard(ui.BoxInner(widths[1])),
		o.learnCard(ui.BoxInner(widths[2])), o.guardCard(ui.BoxInner(widths[3])),
	}
}

// cardsView renders the cards side by side over w cells, all with the
// height of the tallest card.
func (o *overview) cardsView(w int) string {
	t := o.st.theme
	widths := ovCardWidths(w)
	cards := o.cards(w)

	lines := ovCardLines
	for _, c := range cards {
		lines = max(lines, len(c.lines))
	}

	blocks := make([]string, 0, len(cards))
	for i, c := range cards {
		blocks = append(blocks, t.Card(widths[i], lines+2, c.title, c.right, c.lines))
	}

	return ui.Hjoin(1, blocks...)
}

// cardsHeight returns the rendered height of the cards at w cells.
func (o *overview) cardsHeight(w int) int {
	lines := ovCardLines
	for _, c := range o.cards(w) {
		lines = max(lines, len(c.lines))
	}

	return lines + 2
}

// ovFit returns the first text that fits w cells, else the last one.
// Card facts have a long and a short wording, because large counts do
// not fit the 1a card width.
func ovFit(w int, texts ...string) string {
	for _, s := range texts {
		if ui.Width(s) <= w {
			return s
		}
	}

	return texts[len(texts)-1]
}

// noteLines wraps a reason onto at most ovNoteLines lines of w cells.
func (o *overview) noteLines(style lipgloss.Style, text string, w int) []string {
	lines := ui.ClipWithEllipsis(strings.Split(ui.Wrap(text, w), "\n"), ovNoteLines, w, o.st.theme.Glyphs.Ellipsis)

	return ovStyled(style.Render, strings.Join(lines, "\n"))
}

// count renders a number in the body style and its unit in the muted
// style, for example "3,821 symbols".
func (o *overview) count(n int, one, many string) string {
	t := o.st.theme

	return t.Body.Render(ovNum(n)) + " " + t.Muted.Render(ovWord(n, one, many))
}

// ovWord returns the singular or plural word for n.
func ovWord(n int, one, many string) string {
	if n == 1 {
		return one
	}

	return many
}

// reading renders the loading state of one summary fact.
func (o *overview) reading(what string) string {
	t := o.st.theme

	return t.Accent.Render(t.SpinnerFrame(o.st.tick)) + " " + t.Muted.Render("reading "+what+t.Glyphs.Ellipsis)
}

// staleNote renders the stale marker of a section that failed to reload.
func (o *overview) staleNote(err error) string {
	return o.st.theme.Attention.Render("stale: " + ui.ErrText(err))
}

// staleLines renders the stale marker of a card, wrapped to w cells.
func (o *overview) staleLines(err error, w int) []string {
	return o.noteLines(o.st.theme.Attention, "stale: "+ui.ErrText(err), w)
}

// indexCard summarizes the index: the freshness at the right, then the
// coverage, the parse errors, the deleted paths when any exist, and the
// age of the last build.
func (o *overview) indexCard(w int) ovCard {
	t := o.st.theme
	sec := &o.st.index
	c := ovCard{title: "INDEX"}

	switch {
	case !sec.loaded && sec.err != nil:
		c.right = t.Badge(ui.ToneFailure, "read failed")
		c.lines = o.noteLines(t.Unknown, "health unknown: "+ui.ErrText(sec.err), w)

		return c
	case !sec.loaded:
		c.lines = []string{o.reading("the index")}

		return c
	}

	v := sec.value

	if v.Probe.State == workspace.IndexReady {
		c.right, c.lines = o.readyIndexCard(v, w)
	} else {
		c.right, c.lines = o.probeCard(v, w)
	}

	if sec.stale() {
		c.lines = append(c.lines, o.staleLines(sec.err, w)...)
	}

	return c
}

// readyIndexCard renders a ready index. A failed health read says so
// with its reason; it never shows zero files.
func (o *overview) readyIndexCard(v IndexView, w int) (right string, lines []string) {
	t := o.st.theme
	s := v.Status

	if s == nil {
		why := "health unknown"
		if v.StatusErr != nil {
			why += ": " + ui.ErrText(v.StatusErr)
		}

		return t.Badge(ui.ToneUnknown, "unknown"), append(o.noteLines(t.Unknown, why, w), o.ageText(v, "indexed "))
	}

	if s.Coverage.Known {
		parsed, seen := t.Body.Render(ovNum(s.Coverage.FilesParsed)), ovNum(s.Coverage.FilesSeen)

		errs := t.Muted.Render("no parse errors")
		if s.Coverage.ParseErrors > 0 {
			errs = t.Attention.Render(ovPlural(s.Coverage.ParseErrors, "parse error", "parse errors"))
		}

		lines = append(lines, ovFit(w,
			parsed+" "+t.Muted.Render("of "+seen+" files parsed"),
			parsed+t.Muted.Render("/"+seen+" files parsed"),
			parsed+t.Muted.Render("/"+seen+" parsed")), errs)

		// A deleted path is a state of the working tree, not an error,
		// so the count keeps the muted style of "no parse errors".
		if n := s.Coverage.FilesDeleted; n > 0 {
			lines = append(lines, t.Muted.Render(ovFit(w,
				ovNum(n)+" deleted, not staged",
				ovNum(n)+" deleted")))
		}
	} else {
		lines = append(lines, t.Unknown.Render("coverage unknown"), t.Unknown.Render("parse errors unknown"))
	}

	return o.freshnessBadge(s.Freshness), append(lines, o.ageText(v, "indexed "))
}

// probeCard renders an index state other than ready: a short reason and
// the next step. The attention item of the state holds the full text.
func (o *overview) probeCard(v IndexView, w int) (right string, lines []string) {
	t := o.st.theme
	g := t.Glyphs
	p := v.Probe
	schema := t.Muted.Render("schema v" + strconv.Itoa(p.Schema) + " " + g.Sep + " reads v" + strconv.Itoa(p.Supported))

	switch p.State {
	case workspace.IndexAbsent:
		// No database exists, so it has no age to report.
		return t.Faint.Render(g.Hollow + " no index"), []string{
			t.Muted.Render("no index database yet"),
			t.Key.Render("b") + " " + t.Muted.Render("build the index"),
		}
	case workspace.IndexUnreadable:
		right = t.Badge(ui.ToneFailure, "unreadable")
		lines = append([]string{t.Muted.Render("database unreadable")}, o.noteLines(t.Muted, ui.ErrText(p.Err), w)...)
	case workspace.IndexNewer:
		right, lines = t.Badge(ui.ToneFailure, "newer schema"), []string{t.Muted.Render("from a newer seamark"), schema}
	case workspace.IndexOlder:
		right, lines = t.Badge(ui.ToneAttention, "needs upgrade"), []string{schema, t.Muted.Render("a build migrates it")}
	default:
		right, lines = t.Badge(ui.ToneAttention, "not indexed"),
			[]string{t.Muted.Render("decisions only"), t.Muted.Render("no index run yet")}
	}

	if p.IndexedAt > 0 {
		lines = append(lines, o.ageText(v, "indexed "))
	}

	return right, lines
}

// ovIndexedAt returns the last index time. The status time wins over the
// probe time, because status reads it from the same snapshot as the
// other health facts. It returns 0 when neither is known.
func ovIndexedAt(v IndexView) int64 {
	if v.Status != nil && v.Status.IndexedAt > 0 {
		return v.Status.IndexedAt
	}

	return v.Probe.IndexedAt
}

// ageText renders the last index time as an age after prefix, or says
// that the time is unknown.
func (o *overview) ageText(v IndexView, prefix string) string {
	t := o.st.theme

	at := ovIndexedAt(v)
	if at <= 0 {
		return t.Unknown.Render("index time unknown")
	}

	return t.Muted.Render(prefix + ovAge(at, time.Now()))
}

// freshnessBadge renders the index freshness. Unknown is never green.
func (o *overview) freshnessBadge(f string) string {
	t := o.st.theme
	tone := ovFreshnessTone(f)

	word := "unknown"
	if tone != ui.ToneUnknown {
		word = f
	}

	return t.Badge(tone, word)
}

// ovFreshnessTone maps a freshness word to a tone. Unknown is never
// green.
func ovFreshnessTone(f string) ui.Tone {
	switch f {
	case status.FreshCurrent:
		return ui.ToneSuccess
	case status.FreshStale:
		return ui.ToneAttention
	default:
		return ui.ToneUnknown
	}
}

// healthGap returns the semantic health, or the reason it is missing as
// plain text. unknown marks a reason that hides a real value, so it
// renders in the unknown color. An empty reason means loading. A first
// read that failed has no snapshot, so it reports unknown, not loading.
func (o *overview) healthGap() (s *status.Status, why string, unknown bool) {
	sec := &o.st.index

	switch {
	case !sec.loaded && sec.err != nil:
		return nil, "health unknown: " + ui.ErrText(sec.err), true
	case !sec.loaded:
		return nil, "", false
	case sec.value.Status != nil:
		return sec.value.Status, "", false
	case sec.value.Probe.State == workspace.IndexReady && sec.value.StatusErr != nil:
		return nil, "health unknown: " + ui.ErrText(sec.value.StatusErr), true
	case sec.value.Probe.State == workspace.IndexReady:
		return nil, "health unknown", true
	default:
		return nil, "available after an index build", false
	}
}

// health returns the semantic health, or the styled reason it is
// missing, for the rows of the summary detail.
func (o *overview) health() (s *status.Status, why string) {
	t := o.st.theme

	s, text, unknown := o.healthGap()

	switch {
	case s != nil:
		return s, ""
	case text == "":
		return nil, o.reading("semantic health")
	case unknown:
		return nil, t.Unknown.Render(text)
	default:
		return nil, t.Muted.Render(text)
	}
}

// healthLines renders the reason of a missing semantic health for a
// card, wrapped to w cells.
func (o *overview) healthLines(w int) []string {
	t := o.st.theme

	switch _, text, unknown := o.healthGap(); {
	case text == "":
		return []string{o.reading("semantic health")}
	case unknown:
		return o.noteLines(t.Unknown, text, w)
	default:
		return o.noteLines(t.Muted, text, w)
	}
}

// navigateCard summarizes what the navigation tools answer from: the
// code graph and the decisions mined from history.
func (o *overview) navigateCard(w int) ovCard {
	t := o.st.theme
	c := ovCard{title: "NAVIGATE"}

	s, _, _ := o.healthGap()
	if s == nil {
		c.lines = o.healthLines(w)

		return c
	}

	decisions := t.Muted.Render("no decisions from history")
	if n := s.History.Decisions; n > 0 {
		decisions = ovFit(w,
			t.Body.Render(ovNum(n))+" "+t.Muted.Render(ovWord(n, "decision", "decisions")+" from history"),
			t.Body.Render(ovNum(n))+" "+t.Muted.Render("history "+ovWord(n, "decision", "decisions")),
			o.count(n, "decision", "decisions"))
	}

	c.lines = []string{o.count(s.Symbols, "symbol", "symbols"), o.count(s.Edges, "edge", "edges"), decisions}

	return c
}

// learnCard summarizes the lesson loop: the pending proposals at the
// right, then the lessons, the review mining and the distill agent.
func (o *overview) learnCard(w int) ovCard {
	t := o.st.theme
	sec := &o.st.ledger
	c := ovCard{title: "LEARN", right: o.pendingBadge()}

	if s, _, _ := o.healthGap(); s != nil {
		reviews := o.reviewsText(s, "reviews ")
		if ui.Width(reviews) > w {
			reviews = t.Unknown.Render("reviews mined: unknown")
		}

		c.lines = []string{o.count(s.Lessons, "lesson", "lessons"), reviews, o.distillText(s)}
	} else {
		c.lines = o.healthLines(w)
	}

	// Without a readable index no attention item explains the unknown
	// count, so the card states the reason itself.
	if !sec.loaded && sec.err != nil && !o.indexReadable() {
		reason := "no readable index"
		if !ledgerUnavailable(sec.err) {
			reason = ui.ErrText(sec.err)
		}

		c.lines = append(c.lines, o.noteLines(t.Unknown, "proposals unknown: "+reason, w)...)
	}

	if sec.stale() {
		c.lines = append(c.lines, o.staleLines(sec.err, w)...)
	}

	return c
}

// pendingBadge renders the pending proposal count for the LEARN card. A
// failed ledger read is unknown, never zero.
func (o *overview) pendingBadge() string {
	t := o.st.theme

	switch n := o.st.pendingCount(); {
	case n > 0:
		return t.Attention.Render(ovNum(n) + " pending")
	case n == 0:
		return t.Muted.Render("none pending")
	case o.st.ledger.err != nil:
		return t.Badge(ui.ToneUnknown, "unknown")
	default:
		return ""
	}
}

// reviewsText says when review mining last ran, after prefix. Lessons
// or review findings without a mining time mean an unknown time, not
// "never".
func (o *overview) reviewsText(s *status.Status, prefix string) string {
	t := o.st.theme

	switch {
	case s.ReviewsMinedAt > 0:
		return t.Muted.Render(prefix + "mined " + ovAge(s.ReviewsMinedAt, time.Now()))
	case s.Findings["review"] > 0 || s.Lessons > 0:
		return t.Unknown.Render(prefix + "last mined: unknown")
	default:
		return t.Muted.Render(prefix + "never mined")
	}
}

// distillText names the client that the distill agent resolves to.
func (o *overview) distillText(s *status.Status) string {
	t := o.st.theme

	if s.DistillClient == "" {
		return t.Unknown.Render("distill agent unresolved")
	}

	return t.Muted.Render("distill agent") + " " + t.Body.Render(ui.OneLine(s.DistillClient))
}

// proposalsText renders the pending proposal count as a phrase. A failed
// ledger read renders as unknown with its reason, never as zero.
func (o *overview) proposalsText() string {
	t := o.st.theme
	sec := &o.st.ledger

	if sec.loaded && sec.value != nil {
		n := len(sec.value.Pending)

		text := t.Muted.Render("no pending proposals")
		if n > 0 {
			text = t.Body.Render(ovPlural(n, "proposal", "proposals") + " pending")
		}

		if sec.stale() {
			text += " " + o.staleNote(sec.err)
		}

		return text
	}

	switch {
	case sec.err != nil && ledgerUnavailable(sec.err):
		return t.Unknown.Render("proposals unknown: no readable index")
	case sec.err != nil:
		return t.Unknown.Render("proposals unknown: " + ui.ErrText(sec.err))
	default:
		return o.reading("proposals")
	}
}

// guardCard summarizes the gate: the policy mode at the right, then the
// clients with a gate hook, the rule counts and the policy load result.
// The policy file does not need the index, so the card reads it in every
// index state.
func (o *overview) guardCard(w int) ovCard {
	t := o.st.theme
	sec := &o.st.index
	c := ovCard{title: "GUARD"}

	gate := o.gateClientsText(w)

	switch {
	case !sec.loaded && sec.err != nil:
		c.lines = append([]string{gate}, o.noteLines(t.Unknown, "policy unknown: "+ui.ErrText(sec.err), w)...)

		return c
	case !sec.loaded:
		c.lines = []string{gate, o.reading("the policy")}

		return c
	}

	p := sec.value.Policy

	switch {
	case p.Err != nil:
		c.right = t.Badge(ui.ToneFailure, "policy error")
		c.lines = append([]string{gate, t.Unknown.Render("rule counts unknown")},
			o.noteLines(t.Failure, ui.ErrText(p.Err), w)...)

		return c
	case p.Mode == "":
		// A policy that loaded always has a mode, so an empty one means
		// no policy summary arrived. Its counts are unknown, not zero.
		c.right = t.Badge(ui.ToneUnknown, "unknown")
		c.lines = []string{gate, t.Unknown.Render("policy unknown")}

		return c
	}

	deny, approval := t.Body.Render(ovNum(p.Deny)), t.Body.Render(ovNum(p.Approval))
	sep := t.Muted.Render(" " + t.Glyphs.Sep + " ")

	c.right = t.Muted.Render("mode") + " " + t.Title.Render(ui.OneLine(p.Mode))
	c.lines = []string{
		gate,
		ovFit(w,
			deny+" "+t.Muted.Render("deny")+sep+approval+" "+t.Muted.Render(ovWord(p.Approval, "approval rule", "approval rules")),
			deny+" "+t.Muted.Render("deny")+sep+approval+" "+t.Muted.Render("approval")),
		t.Muted.Render("policy compiles"),
	}

	return c
}

// gateClientsText names the clients with a gate hook, for the GUARD
// card: "gate on Claude Code, Codex", or a count when the names do not
// fit w cells.
func (o *overview) gateClientsText(w int) string {
	t := o.st.theme
	sec := &o.st.integrations

	if !sec.loaded {
		return o.reading("the gate hooks")
	}

	names := ovGateHookClients(sec.value)
	if len(names) == 0 {
		return t.Muted.Render("no gate hook installed")
	}

	return ovFit(w,
		t.Muted.Render("gate on")+" "+t.Body.Render(ui.OneLine(strings.Join(names, ", "))),
		t.Muted.Render("gate on")+" "+t.Body.Render(ovPlural(len(names), "client", "clients")))
}

// ovGateHookClients returns the names of the clients whose gate hook is
// installed, current or partial. The names are raw; the caller sanitizes
// them.
func ovGateHookClients(insps []integration.Inspection) []string {
	var names []string

	for _, insp := range insps {
		e, ok := insp.Entry(integration.CapabilityCommands)
		if ok && e.Supported && (e.State == integration.StateCurrent || e.State == integration.StatePartial) {
			names = append(names, ovClientName(insp))
		}
	}

	return names
}

// summaryLine renders the cards as one line for the collapsed layout:
// "INDEX ✓ 244/336   NAV 3,821 sym   LEARN 6 pending   GUARD warn". A
// source that failed to reload adds a "stale" word after its value.
func (o *overview) summaryLine(w int) string {
	t := o.st.theme

	parts := []string{
		t.Heading.Render("INDEX") + " " + o.indexBrief(),
		t.Heading.Render("NAV") + " " + o.navBrief(),
		t.Heading.Render("LEARN") + " " + o.learnBrief(),
		t.Heading.Render("GUARD") + " " + o.guardBrief(),
	}

	return ui.Truncate(" "+strings.Join(parts, "   "), w, t.Glyphs.Ellipsis)
}

// indexBrief is the INDEX value of the summary line: the freshness glyph
// and the parsed and seen files, or the index state.
func (o *overview) indexBrief() string {
	t := o.st.theme
	sec := &o.st.index

	switch {
	case !sec.loaded && sec.err != nil:
		return t.Badge(ui.ToneFailure, "read failed")
	case !sec.loaded:
		return t.Accent.Render(t.SpinnerFrame(o.st.tick))
	}

	v := sec.value

	var brief string

	switch s := v.Status; {
	case v.Probe.State != workspace.IndexReady:
		brief, _ = o.probeCard(v, 0)
	case s == nil:
		brief = t.Badge(ui.ToneUnknown, "health unknown")
	case s.Coverage.Known:
		tone := ovFreshnessTone(s.Freshness)
		brief = t.ToneStyle(tone).Render(t.ToneGlyph(tone)) + " " +
			t.Body.Render(ovNum(s.Coverage.FilesParsed)+"/"+ovNum(s.Coverage.FilesSeen))
	default:
		brief = o.freshnessBadge(s.Freshness)
	}

	if sec.stale() {
		brief += " " + t.Attention.Render("stale")
	}

	return brief
}

// navBrief is the NAV value of the summary line: the symbol count.
func (o *overview) navBrief() string {
	t := o.st.theme
	sec := &o.st.index

	switch {
	case !sec.loaded && sec.err == nil:
		return t.Accent.Render(t.SpinnerFrame(o.st.tick))
	case sec.loaded && sec.value.Status != nil:
		return t.Body.Render(ovNum(sec.value.Status.Symbols)) + " " + t.Muted.Render("sym")
	case sec.loaded && sec.value.Probe.State != workspace.IndexReady:
		return t.Faint.Render("no index")
	default:
		return t.Badge(ui.ToneUnknown, "unknown")
	}
}

// learnBrief is the LEARN value of the summary line: the pending count.
func (o *overview) learnBrief() string {
	t := o.st.theme

	brief := o.pendingBadge()
	if brief == "" {
		brief = t.Accent.Render(t.SpinnerFrame(o.st.tick))
	}

	if o.st.ledger.stale() {
		brief += " " + t.Attention.Render("stale")
	}

	return brief
}

// guardBrief is the GUARD value of the summary line: the policy mode.
func (o *overview) guardBrief() string {
	t := o.st.theme
	sec := &o.st.index

	switch {
	case !sec.loaded && sec.err != nil:
		return t.Badge(ui.ToneUnknown, "unknown")
	case !sec.loaded:
		return t.Accent.Render(t.SpinnerFrame(o.st.tick))
	case sec.value.Policy.Err != nil:
		return t.Badge(ui.ToneFailure, "policy error")
	case sec.value.Policy.Mode == "":
		return t.Badge(ui.ToneUnknown, "unknown")
	default:
		return t.Body.Render(ui.OneLine(sec.value.Policy.Mode))
	}
}
