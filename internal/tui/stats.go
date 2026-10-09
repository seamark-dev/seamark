package tui

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// statsPane names a box of the Stats sub-view.
type statsPane int

const (
	// statsPaneLessons is the lesson and proposal counts. It is the
	// default focus, as in the 1a design.
	statsPaneLessons statsPane = iota
	// statsPaneDelivery is the delivery chart and the delivery summary.
	statsPaneDelivery
	statsPaneCount
)

// statsPeriods are the day windows of the chart; f cycles through them.
// 14 days come first, as in the 1a design. lessonstats.DeliveryDays
// covers the longest window.
var statsPeriods = []int{14, lessonstats.DeliveryDays, 7}

// Layout of the 1a Stats screen at 120 columns: a Lessons box of 60
// cells, one cell of gap, and a Delivery box of 59 cells.
const (
	statsLeftW   = 60
	statsDesignW = 120

	// statsChartH is the height of the day chart in rows.
	statsChartH = 5
	// statsTopN is the number of lessons under MOST SURFACED.
	statsTopN = 4
	// statsNeverN is the number of never delivered lessons that the
	// box lists before a "+N more" line.
	statsNeverN = 5

	statsGap       = 2
	statsInjectedW = 8  // "Injected"
	statsRepeatedW = 8  // "Repeated"
	statsSuppressW = 10 // "Suppressed"

	// statsRowW caps a count row, so a count stays near its label.
	statsRowW = 44
)

// statsInfo is the info color of the bars. ToneUnknown carries the info
// color; the bars use it for color only, not for an unknown state.
const statsInfo = ToneUnknown

// statsView is the Lessons → Stats sub-view: the lesson counts on the
// left and the delivery of the retained log on the right. It only reads
// the shared snapshots: viewing a lesson here never records a delivery.
type statsView struct {
	st *state

	focus  statsPane
	period int // an index into statsPeriods
	scroll [statsPaneCount]scrollState
}

// newStatsView returns the Stats sub-view over the shared state.
func newStatsView(st *state) *statsView {
	return &statsView{st: st}
}

// update handles the keys. The lessons destination forwards keys only
// while Stats is visible. A new snapshot needs no work here: every
// render clamps the scroll positions.
func (s *statsView) update(msg tea.Msg) tea.Cmd {
	if km, ok := msg.(tea.KeyMsg); ok && s.st.stats.loaded {
		s.handleKey(km)
	}

	return nil
}

// handleKey applies one key: tab changes the box, f changes the chart
// period, and the movement keys scroll the focused box.
func (s *statsView) handleKey(msg tea.KeyMsg) {
	switch {
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		s.focus = (s.focus + 1) % statsPaneCount
	case key.Matches(msg, lesKeys.State):
		s.period = (s.period + 1) % len(statsPeriods)
	default:
		s.scroll[s.focus].key(msg)
	}
}

// days returns the chart window in days.
func (s *statsView) days() int {
	return statsPeriods[s.period]
}

// keys returns the bindings for the footer, in the order of the 1a
// footer. The lessons destination adds "p proposals" in front.
func (s *statsView) keys() []key.Binding {
	if !s.st.stats.loaded {
		return nil
	}

	period := withHelp(lesKeys.State, "f", "period")
	period.SetEnabled(s.chartReady(s.st.stats.value))

	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(s.scroll[s.focus].scrolls())

	return []key.Binding{withHelp(nkeys.Next, "tab", "pane"), period, scroll}
}

// capturing is always false: Stats has no text input and no modal.
func (s *statsView) capturing() bool {
	return false
}

// logReady reports a readable log with records. Only then do the
// delivery counts mean something; the other states explain themselves.
func (s *statsView) logReady(v lessonstats.Snapshot) bool {
	return v.LogErr == nil && v.LogExists && v.Records > 0
}

// chartReady reports a snapshot with day counts to chart.
func (s *statsView) chartReady(v lessonstats.Snapshot) bool {
	return s.logReady(v) && len(v.Days) > 0
}

// view renders the stale note row, a blank row and the two boxes. A
// failed first read never renders as zero counts.
func (s *statsView) view(w, h int) string {
	t := s.st.theme
	sec := &s.st.stats

	if !sec.loaded {
		if sec.err != nil {
			return t.empty(ToneFailure, "Could not read delivery statistics", errText(sec.err),
				t.Muted.Render("Press r to refresh the view."), w, h)
		}

		return t.empty(ToneNeutral, "Reading delivery statistics"+t.Glyphs.Ellipsis,
			"Stats read the retained delivery log and the lesson configuration. Reading never records a delivery.",
			"", w, h)
	}

	head := ""
	if sec.stale() {
		head = " " + t.Attention.Render(truncate(t.Glyphs.Warn+" stale: "+errText(sec.err)+
			". Showing the snapshot from "+sec.at.Format("15:04:05")+".", w-1, t.Glyphs.Ellipsis))
	}

	v := sec.value
	boxH := max(4, h-3)

	// The keys never read the layout, so the render width decides it.
	// A direct render in a test then needs no terminal size.
	if w < wideWidth {
		if s.focus == statsPaneLessons {
			return head + "\n\n" + s.lessonsBox(v, w, boxH, true)
		}

		return head + "\n\n" + s.deliveryBox(v, w, boxH, true)
	}

	leftW := min(statsLeftW, w*statsLeftW/statsDesignW)

	return head + "\n\n" + hjoin(1,
		s.lessonsBox(v, leftW, boxH, s.focus == statsPaneLessons),
		s.deliveryBox(v, w-leftW-1, boxH, s.focus == statsPaneDelivery))
}

// scrollBox renders a box whose body scrolls with the state of pane.
// The footer shows the visible range, so hidden lines are never a
// surprise. The shared Theme.scrollBox keeps the rules of every box.
func (s *statsView) scrollBox(pane statsPane, o boxOpts, body []string) string {
	return s.st.theme.scrollBox(o, body, nil, &s.scroll[pane], [2]int{-1, -1}, false)
}

// lessonsBox renders "Lessons · N active": the active lessons by source,
// the proposals by state, and the regions with the most lessons.
func (s *statsView) lessonsBox(v lessonstats.Snapshot, w, h int, focused bool) string {
	t := s.st.theme

	right := t.Unknown.Render(t.Glyphs.Unknown + " unknown")
	if v.Lessons != nil {
		right = t.Muted.Render(itoa(v.Lessons.Active()) + " active")
	}

	return s.scrollBox(statsPaneLessons, boxOpts{
		W: w, H: h, Focus: focused, Right: right, Title: boxTitle(t, "Lessons", focused),
	}, s.lessonLines(v, boxInner(w)))
}

// lessonLines renders the sections of the Lessons box.
func (s *statsView) lessonLines(v lessonstats.Snapshot, w int) []string {
	t := s.st.theme
	c := v.Lessons

	out := []string{t.Heading.Render("BY SOURCE")}

	if c == nil {
		out = append(out, s.unknown(v.LessonsErr, "The lesson counts are not available.", w)...)
	} else {
		// The label says "pins", not "manual pins". The count includes
		// the pins applied from proposals, and a pin does not prove that
		// a person wrote it (Pins, D-4).
		bars := s.bars(18, w, max(c.Mined, c.Pins))
		out = append(out, bars.row("review-mined", c.Mined, c.Active(), statsInfo),
			bars.row("pins", c.Pins, c.Active(), statsInfo))

		switch {
		case c.Inactive == 1:
			out = append(out, propLines(t.Faint, "1 more mined lesson is below the threshold or muted, so no "+
				"surface shows it.", w)...)
		case c.Inactive > 1:
			out = append(out, propLines(t.Faint, itoa(c.Inactive)+" more mined lessons are below the threshold "+
				"or muted, so no surface shows them.", w)...)
		}
	}

	out = append(out, "", t.Heading.Render("PROPOSALS"))
	out = append(out, s.proposalLines(w)...)

	out = append(out, "", t.Heading.Render("TOP REGIONS"))

	switch {
	case c == nil:
		out = append(out, s.unknown(v.LessonsErr, "The regions are not available.", w)...)
	case len(c.TopRegions) == 0:
		out = append(out, t.Muted.Render("No active mined lesson."))
	default:
		bars := s.bars(18, w, c.TopRegions[0].Lessons)

		for _, r := range c.TopRegions {
			label := oneLine(r.Region)
			if label == "" {
				label = "repo-wide"
			}

			out = append(out, bars.row(label, r.Lessons, c.Mined, ToneAccent))
		}

		out = append(out, propLines(t.Faint, "Active mined lessons by the region they name.", w)...)
	}

	return out
}

// proposalLines renders the proposal counts of the ledger snapshot. An
// unread ledger shows why, never zero counts.
func (s *statsView) proposalLines(w int) []string {
	t := s.st.theme
	sec := &s.st.ledger

	switch {
	case !sec.loaded && sec.err != nil:
		return s.unknown(sec.err, "", w)
	case !sec.loaded:
		return []string{t.Faint.Render("Reading the proposal ledger" + t.Glyphs.Ellipsis)}
	case sec.value == nil:
		return s.unknown(nil, "The proposal ledger is not available.", w)
	}

	l := sec.value
	total := len(l.Pending) + len(l.Applied) + len(l.Dismissed)
	bars := s.bars(18, w, max(len(l.Pending), len(l.Applied), len(l.Dismissed)))

	out := []string{
		bars.row("pending", len(l.Pending), total, ToneAttention),
		bars.row("applied", len(l.Applied), total, ToneSuccess),
		bars.row("dismissed", len(l.Dismissed), total, ToneUnsupported),
	}

	if sec.stale() {
		out = append(out, propLines(t.Faint, "From the last good read: "+errText(sec.err), w)...)
	}

	return out
}

// statsBars is the column layout of a group of bar rows.
type statsBars struct {
	t      *Theme
	labelW int
	barW   int
	countW int
}

// bars fits bar rows with a label column of labelW cells into w cells.
// The count column fits the largest count.
func (s *statsView) bars(labelW, w, largest int) statsBars {
	countW := max(4, width(itoa(largest))+1)
	barW := max(4, min(30, w-labelW-2-countW))

	return statsBars{t: s.st.theme, labelW: labelW, barW: barW, countW: countW}
}

// row renders "label  ████░░░░  n". The count carries the value, so the
// bar is a visual aid only.
func (b statsBars) row(label string, n, total int, tone Tone) string {
	t := b.t

	return t.cols(
		cell(t.Muted.Render(truncate(label, b.labelW-1, t.Glyphs.Ellipsis)), b.labelW),
		cell(t.bar(n, total, b.barW, tone), b.barW+2),
		cell(t.Body.Render(padLeft(itoa(n), b.countW-1)), b.countW),
	)
}

// unknown renders an unknown value with its cause. It is never a zero.
func (s *statsView) unknown(err error, fallback string, w int) []string {
	text := fallback
	if err != nil {
		text = "Unknown: " + errText(err)
	}

	return propNote(s.st.theme, ToneUnknown, text, w)
}

// deliveryBox renders "Delivery · last 14 days": the day chart, the most
// delivered lessons and the delivery summary of the retained log.
func (s *statsView) deliveryBox(v lessonstats.Snapshot, w, h int, focused bool) string {
	t := s.st.theme

	title := "Delivery"
	if s.chartReady(v) {
		title += " " + t.Glyphs.Sep + " last " + itoa(s.days()) + " days"
	}

	return s.scrollBox(statsPaneDelivery, boxOpts{
		W: w, H: h, Focus: focused, Title: boxTitle(t, title, focused),
	}, s.deliveryLines(v, boxInner(w)))
}

// deliveryLines renders the Delivery box body: the delivery sections of
// a readable log, or the state of the log, then the review mining.
func (s *statsView) deliveryLines(v lessonstats.Snapshot, w int) []string {
	var out []string

	if s.logReady(v) {
		out = append(out, s.chartLines(v, w)...)
		out = append(out, "")
		out = append(out, s.topLines(v.Summary.Ranked, w)...)
		out = append(out, "")
		out = append(out, s.surfaceLines(v.Summary, w)...)
		out = append(out, "")
		out = append(out, s.hookLines(v.Summary, w)...)
		out = append(out, "")
		out = append(out, s.attributionLines(v.Summary, w)...)
		out = append(out, "")
		out = append(out, s.perLessonLines(v.Summary.Ranked, w)...)
		out = append(out, "")
		out = append(out, s.neverLines(v, w)...)
	} else {
		out = append(out, s.logStateLines(v, w)...)
	}

	out = append(out, "")
	out = append(out, s.miningLines(w)...)

	if s.logReady(v) {
		out = append(out, "")
		out = append(out, s.scopeLines(v, w)...)
	}

	return out
}

// logStateLines explains a log without counts: an unreadable log, no
// log, or a log without records. Each state has its own text, because
// they mean different things.
func (s *statsView) logStateLines(v lessonstats.Snapshot, w int) []string {
	t := s.st.theme

	var (
		tone        Tone
		title, body string
		next        string
	)

	switch {
	case v.LogErr != nil:
		tone, title = ToneFailure, "Could not read the delivery log"
		body = "Could not read the delivery log: " + errText(v.LogErr) +
			". No counts are shown, because a failed read is not zero delivery."
		next = "Check access to the file, then press r to refresh."
	case !v.LogExists:
		tone, title = ToneNeutral, "No recorded delivery yet"
		body = "Seamark records a delivery when an agent hook injects a lesson before an edit, and when the " +
			"MCP change_set or check tools surface lessons. The delivery log does not exist yet. " +
			"Viewing lessons here never records a delivery."
		next = "Set up hooks or MCP under Integrations (press 2). After an agent edits files, press r to refresh."
	default:
		tone, title = ToneNeutral, "The log exists but holds no records"
		body = "The delivery log has no readable record yet. Agent hooks and the MCP change_set and check " +
			"tools add a record when they deliver lessons. Viewing lessons here never records a delivery."
		next = "After an agent edits files, press r to refresh."
	}

	out := []string{t.ToneStyle(tone).Render(t.ToneGlyph(tone)) + " " + t.Title.Render(title)}
	out = append(out, propLines(t.Muted, body, w)...)
	out = append(out, "", t.Muted.Render("Delivery log"), t.command(displayPath(v.LogPath)), "")

	return append(out, propLines(t.Muted, next, w)...)
}

// chartLines renders SURFACED PER DAY: the delivered records of each day
// in the chosen window, the first and the last date, and the totals.
func (s *statsView) chartLines(v lessonstats.Snapshot, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("SURFACED PER DAY")}

	if len(v.Days) == 0 {
		return append(out, s.unknown(nil, "This snapshot has no day counts.", w)...)
	}

	days := v.Days[max(0, len(v.Days)-s.days()):]
	vals := make([]int, len(days))

	var delivered, hook int

	for i, d := range days {
		vals[i] = d.Delivered
		delivered += d.Delivered
		hook += d.Hook
	}

	if delivered == 0 {
		out = append(out, propLines(t.Muted, "No record was delivered in the last "+itoa(len(days))+" days.", w)...)
	} else {
		out = append(out, "")
		out = append(out, s.columns(days, vals, w)...)
	}

	sep := " " + t.Glyphs.Sep + " "
	out = append(out, t.Body.Render(itoa(delivered))+t.Muted.Render(" records delivered"+sep)+
		t.Body.Render(itoa(hook))+t.Muted.Render(" surfaced on hook"))

	if v.Undated > 0 {
		out = append(out, propLines(t.Faint, statsCount(v.Undated, "delivered record")+
			" without a readable time are not in the chart.", w)...)
	}

	return out
}

// columns renders the column chart with the first and the last date
// under it. The shared chart gives each day two cells and a gap; a
// window that does not fit in w cells gets one cell per day, and no gap
// when even that does not fit.
func (s *statsView) columns(days []lessonstats.DayCount, vals []int, w int) []string {
	t := s.st.theme
	n := len(vals)

	var (
		rows   []string
		chartW int
	)

	switch {
	case 3*n <= w:
		rows = t.columns(vals, statsChartH, ToneAccent)
		chartW = 3*n - 1
	case 2*n <= w:
		rows = s.narrowColumns(vals, 1)
		chartW = 2*n - 1
	default:
		rows = s.narrowColumns(vals, 0)
		chartW = n
	}

	first := days[0].Day.Format("Jan 2")
	last := days[n-1].Day.Format("Jan 2")

	labels := t.Faint.Render(first)
	if chartW >= width(first)+width(last)+1 {
		labels = spread(t.Faint.Render(first), t.Faint.Render(last), chartW)
	}

	return append(rows, labels)
}

// narrowColumns renders a column chart with one cell per value and gap
// cells between values. The top cell of a column uses an eighth-block
// glyph, like the shared chart.
func (s *statsView) narrowColumns(vals []int, gap int) []string {
	t := s.st.theme
	spark := t.Glyphs.Spark

	peak := 0
	for _, v := range vals {
		peak = max(peak, v)
	}

	rows := make([]string, 0, statsChartH)

	for r := statsChartH - 1; r >= 0; r-- {
		var b strings.Builder

		for i, v := range vals {
			if i > 0 {
				b.WriteString(strings.Repeat(" ", gap))
			}

			ch := " "

			if peak > 0 {
				eighths := int(math.Round(float64(v)/float64(peak)*float64(statsChartH*8))) - r*8

				switch {
				case eighths >= 8:
					ch = spark[len(spark)-1]
				case eighths > 0:
					ch = spark[eighths-1]
				}
			}

			b.WriteString(ch)
		}

		rows = append(rows, t.Accent.Render(b.String()))
	}

	return rows
}

// topLines renders MOST SURFACED: the lessons with the most delivered
// records in the retained log. The heading names the scope, because
// the box title names the chart window.
func (s *statsView) topLines(ranked []reviews.Fired, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("MOST SURFACED") + "  " + t.Faint.Render("retained log")}

	top := make([]reviews.Fired, 0, len(ranked))

	for _, f := range ranked {
		if f.Count > 0 {
			top = append(top, f)
		}
	}

	// Stable: equal counts keep the snapshot order, which Gather makes
	// total, so a refresh keeps the rows in place.
	sort.SliceStable(top, func(i, j int) bool { return top[i].Count > top[j].Count })

	if len(top) == 0 {
		return append(out, t.Muted.Render("No lesson was delivered."))
	}

	top = top[:min(statsTopN, len(top))]
	bars := s.bars(28, w, top[0].Count)
	bars.barW = min(bars.barW, 16)

	for _, f := range top {
		out = append(out, bars.row(s.orNone(oneLine(f.Symptom)), f.Count, top[0].Count, ToneAccent))
	}

	return out
}

// surfaceLines renders DELIVERY BY SURFACE: the delivered records of
// each surface, the total and the distinct files.
func (s *statsView) surfaceLines(sum reviews.Summary, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("DELIVERY BY SURFACE")}

	surfaces := statsSurfaces(sum.BySurface)

	largest := sum.Total
	bars := s.bars(18, w, largest)

	for _, surface := range surfaces {
		label := oneLine(surface)
		if surface == "hook" {
			label = "hook reminders"
		}

		out = append(out, bars.row(label, sum.BySurface[surface], sum.Total, statsInfo))
	}

	out = append(out, s.countRow("Total", sum.Total, 0, w, true), s.countRow("Distinct files", sum.Files, 0, w, false))

	if sum.Total == 0 && sum.SuppressedHookFirings > 0 {
		out = append(out, propLines(t.Muted, "No record was delivered. Every recorded hook match was suppressed "+
			"as a repeat.", w)...)
	}

	return out
}

// hookLines renders the instrumented hook totals, the suppressed
// repeats, and the legacy hook reminders without a delivery status.
func (s *statsView) hookLines(sum reviews.Summary, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("HOOK DELIVERY")}

	if sum.InstrumentedHookFirings+sum.SuppressedHookFirings == 0 {
		out = append(out, propLines(t.Muted, "No instrumented hook record yet. Only instrumented records carry "+
			"a delivery status, repeats and context bytes.", w)...)
	} else {
		out = append(out,
			s.countRow("Injected", sum.InstrumentedHookFirings, 0, w, false),
			s.countRow("repeated within a context", sum.RepeatedHookFirings, 1, w, false),
			s.countRow("Suppressed repeats", sum.SuppressedHookFirings, 0, w, false),
			s.countRow("Context bytes", sum.HookContextBytes, 0, w, false),
		)
	}

	if legacy := statsLegacy(sum); legacy > 0 {
		out = append(out, s.countRow("Without delivery status", legacy, 0, w, false))
		out = append(out, propIndent("  ", propLines(t.Muted, "recorded before instrumentation; client unknown", w-2))...)
	}

	return out
}

// statsLegacy returns the hook records without a delivery status. They
// predate the instrumentation: Summarize counts them as delivered but
// not as instrumented, so the difference is the legacy count.
func statsLegacy(sum reviews.Summary) int {
	return max(0, sum.BySurface["hook"]-sum.InstrumentedHookFirings)
}

// attributionLines renders the instrumented hook records by recorded
// client. A record without a client stays unattributed: the view never
// assigns a client that the record does not name.
func (s *statsView) attributionLines(sum reviews.Summary, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("CLIENT ATTRIBUTION")}

	switch {
	case sum.HookByClient != nil:
		out = append(out, s.attributionTable(sum.HookByClient, w)...)
	case sum.InstrumentedHookFirings+sum.SuppressedHookFirings > 0:
		out = append(out, propNote(t, ToneUnknown, "No record names a client, so every instrumented hook "+
			"record lacks client attribution. Seamark never assigns a client to a record.", w)...)
	default:
		out = append(out, propLines(t.Muted, "No instrumented hook record, so no client attribution exists.", w)...)
	}

	if legacy := statsLegacy(sum); legacy > 0 {
		out = append(out, propNote(t, ToneUnknown, "Not in this table: "+statsCount(legacy, "legacy hook reminder")+
			" with no delivery status and no recorded client.", w)...)
	}

	return out
}

// attributionTable renders one row per recorded client and mechanism.
// A narrow box moves the mechanism to a line under the client, so the
// counts keep their columns.
func (s *statsView) attributionTable(rows []reviews.HookAttribution, w int) []string {
	t := s.st.theme
	nums := statsGap + statsInjectedW + statsGap + statsRepeatedW + statsGap + statsSuppressW

	clientW, mechW := width("Client"), width("Mechanism")

	for _, r := range rows {
		clientW = max(clientW, width(statsClient(r)))
		mechW = max(mechW, width(s.orNone(oneLine(r.Mechanism))))
	}

	fiveColumns := clientW+statsGap+mechW+nums <= w
	if !fiveColumns {
		clientW = max(6, min(clientW, w-nums))
	}

	gap := strings.Repeat(" ", statsGap)

	counts := func(inj, rep, sup string) string {
		return gap + padLeft(inj, statsInjectedW) + gap + padLeft(rep, statsRepeatedW) + gap + padLeft(sup, statsSuppressW)
	}

	header := padRight("Client", clientW)
	if fiveColumns {
		header += gap + padRight("Mechanism", mechW)
	}

	lines := []string{t.Faint.Render(header + counts("Injected", "Repeated", "Suppressed"))}

	for _, r := range rows {
		client := padRight(truncate(statsClient(r), clientW, t.Glyphs.Ellipsis), clientW)

		style := t.Body
		if r.Client == "" {
			style = t.Unknown
		}

		line := style.Render(client)
		if fiveColumns {
			line += gap + t.Muted.Render(padRight(s.orNone(oneLine(r.Mechanism)), mechW))
		}

		lines = append(lines, line+t.Body.Render(counts(itoa(r.Injected), itoa(r.Repeated), itoa(r.Suppressed))))

		if !fiveColumns && r.Mechanism != "" {
			lines = append(lines, t.Muted.Render(truncate("  via "+oneLine(r.Mechanism), w, t.Glyphs.Ellipsis)))
		}
	}

	return lines
}

// perLessonLines renders PER LESSON: the delivered and matched counts
// and the last recorded match of every lesson in the counted records.
func (s *statsView) perLessonLines(ranked []reviews.Fired, w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("PER LESSON") + "  " + t.Faint.Render(itoa(len(ranked)))}

	if len(ranked) == 0 {
		return append(out, t.Muted.Render("No lesson appears in the counted records."))
	}

	out = append(out, propLines(t.Faint, "Delivered counts the records that emitted a lesson to an agent, on any "+
		"surface. Matched adds the hook matches that were not emitted again, so matched includes suppressed "+
		"repeats. A record shows that a reminder was emitted, not that an agent followed it.", w)...)

	sep := " " + t.Glyphs.Sep + " "
	ell := t.Glyphs.Ellipsis

	for _, f := range ranked {
		meta := s.orNone(oneLine(f.Region)) + sep + itoa(f.Count) + " delivered" + sep + itoa(f.Matches) +
			" matched" + sep + "last " + statsWhen(f.LastTS, false, s.none())

		out = append(out, t.Body.Render(truncate(s.orNone(oneLine(f.Symptom)), w, ell)),
			"  "+t.Muted.Render(truncate(meta, w-2, ell)))
	}

	return out
}

// neverLines renders the eligible lessons that no record shows as
// delivered. Without the index or the lessons config the list is
// incomplete, and the view says so instead of showing an empty list.
func (s *statsView) neverLines(v lessonstats.Snapshot, w int) []string {
	t := s.st.theme

	if !v.Eligible {
		reason := errText(v.EligibleErr)
		if reason == "" {
			reason = "the set of eligible lessons is unavailable"
		}

		out := []string{t.Heading.Render("NEVER DELIVERED")}
		out = append(out, propNote(t, ToneUnknown, "Incomplete: "+reason, w)...)

		return append(out, propIndent("  ", propLines(t.Muted, "The delivery counts above do not depend on this "+
			"list.", w-2))...)
	}

	never := v.Summary.NeverFired
	out := []string{t.Heading.Render("NEVER DELIVERED") + "  " + t.Faint.Render(itoa(len(never)))}
	intro := "Eligible lessons never recorded as delivered on any surface. Pinned lessons are not listed."

	if len(never) == 0 {
		return append(out, propLines(t.Muted, intro+" Every eligible lesson has a delivered record.", w)...)
	}

	out = append(out, propLines(t.Faint, intro, w)...)

	sep := " " + t.Glyphs.Sep + " "
	ell := t.Glyphs.Ellipsis

	for _, l := range never[:min(statsNeverN, len(never))] {
		out = append(out, t.Body.Render(truncate(s.orNone(oneLine(l.Symptom)), w, ell)),
			"  "+t.Muted.Render(truncate(s.orNone(oneLine(l.Region))+sep+statsCount(l.Occurrences, "occurrence"), w-2, ell)))
	}

	if rest := len(never) - statsNeverN; rest > 0 {
		out = append(out, t.Faint.Render("+"+itoa(rest)+" more"))
	}

	return out
}

// miningLines renders REVIEW MINING from the index health: the last
// review mining run and the findings by source. An unread health is
// unknown, never "never".
func (s *statsView) miningLines(w int) []string {
	t := s.st.theme
	out := []string{t.Heading.Render("REVIEW MINING")}
	sec := &s.st.index

	switch {
	case !sec.loaded && sec.err != nil:
		return append(out, s.unknown(sec.err, "", w)...)
	case !sec.loaded:
		return append(out, t.Faint.Render("Reading the index health"+t.Glyphs.Ellipsis))
	case sec.value.Status == nil:
		return append(out, s.unknown(sec.value.StatusErr, "The index health is not available.", w)...)
	}

	st := sec.value.Status

	last := "never recorded"
	if st.ReviewsMinedAt > 0 {
		last = ageSince(st.ReviewsMinedAt) + " (" + time.Unix(st.ReviewsMinedAt, 0).Format(time.DateOnly) + ")"
	}

	out = append(out, propKV(t, [][2]string{{"Last run", last}, {"Findings", statsFindings(st.Findings, t.Glyphs.Sep)}}, w)...)

	if st.ReviewsMinedAt == 0 {
		out = append(out, t.Muted.Render("Mine review findings with ")+t.command("seamark index --reviews"))
	}

	return out
}

// statsFindings renders the findings by source, most first, for
// example "review 120 · fix:conventional 8".
func statsFindings(by map[string]int, sep string) string {
	type source struct {
		name string
		n    int
	}

	var sources []source

	for name, n := range by {
		sources = append(sources, source{oneLine(name), n})
	}

	if len(sources) == 0 {
		return "none recorded"
	}

	sort.Slice(sources, func(i, j int) bool {
		if sources[i].n != sources[j].n {
			return sources[i].n > sources[j].n
		}

		return sources[i].name < sources[j].name
	})

	parts := make([]string, len(sources))
	for i, src := range sources {
		parts[i] = src.name + " " + itoa(src.n)
	}

	return strings.Join(parts, " "+sep+" ")
}

// scopeLines says what the counts cover. The log is retained history,
// not a fixed period, and only the chart and its totals use the window.
func (s *statsView) scopeLines(v lessonstats.Snapshot, w int) []string {
	t := s.st.theme

	text := "Counts cover the retained log " + oneLine(displayPath(v.LogPath)) + " (" + statsCount(v.Records, "record") +
		"); the chart and its totals cover the last " + itoa(s.days()) + " local days. A refresh reads a new " +
		"snapshot; nothing streams."

	if v.Skipped > 0 {
		text += " " + statsCount(v.Skipped, "unreadable line") + " skipped."
	}

	return strings.Split(t.Faint.Render(statsWrapWords(text, w)), "\n")
}

// countRow renders a label and a right-aligned count. level indents a
// sub-row under its parent row.
func (s *statsView) countRow(label string, n, level, w int, strong bool) string {
	t := s.st.theme
	rowW := min(w, statsRowW)
	value := itoa(n)

	label = truncate(strings.Repeat("  ", level)+label, max(1, rowW-width(value)-1), t.Glyphs.Ellipsis)
	gap := max(1, rowW-width(label)-width(value))

	labelStyle := t.Body
	if level > 0 {
		labelStyle = t.Muted
	}

	valueStyle := t.Body
	if strong {
		valueStyle = t.Title
	}

	return labelStyle.Render(label) + strings.Repeat(" ", gap) + valueStyle.Render(value)
}

// none is the placeholder for an absent value.
func (s *statsView) none() string {
	if s.st.theme.ASCII {
		return "-"
	}

	return "—"
}

// orNone returns v, or the absent-value placeholder for "".
func (s *statsView) orNone(v string) string {
	if v == "" {
		return s.none()
	}

	return v
}

// statsClient labels one attribution row. An empty client is the
// shared bucket of records that name no client.
func statsClient(r reviews.HookAttribution) string {
	if r.Client == "" {
		return "no client recorded"
	}

	return oneLine(r.Client)
}

// statsSurfaces returns the surface keys in display order: the three
// known surfaces first, then any other recorded surface by name.
func statsSurfaces(by map[string]int) []string {
	known := []string{"hook", "change_set", "check"}

	var extra []string

	for surface := range by {
		if surface != "hook" && surface != "change_set" && surface != "check" {
			extra = append(extra, surface)
		}
	}

	sort.Strings(extra)

	return append(known, extra...)
}

// statsWhen formats a record timestamp in local time. full adds the
// clock time. A timestamp that does not parse shows as recorded.
func statsWhen(ts string, full bool, none string) string {
	if ts == "" {
		return none
	}

	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return oneLine(ts)
	}

	if full {
		return at.Local().Format("2006-01-02 15:04")
	}

	return at.Local().Format(time.DateOnly)
}

// statsCount formats a count with a singular or plural noun.
func statsCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return itoa(n) + " " + noun + "s"
}

// statsWrapWords wraps text at spaces only. A path then stays on one
// line when it fits, because wrap also breaks at "-" and "/". A word
// wider than w still breaks, so no line overflows.
func statsWrapWords(text string, w int) string {
	var lines []string

	line := ""

	for _, word := range strings.Fields(text) {
		switch {
		case line == "":
			line = word
		case width(line)+1+width(word) <= w:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}

	return wrap(strings.Join(append(lines, line), "\n"), w)
}
