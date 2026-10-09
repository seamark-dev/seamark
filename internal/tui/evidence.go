package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds the Lessons → Evidence sub-view (design map §2). It
// shows the two evidence sources and the actions that start from them.
// It also shows the states that block these actions. The refresh preview lives in
// evidence_preview.go and the run record in evidence_run.go.
// Browsing here reads the learning snapshot only: it never plans,
// fetches or writes.

// evItem is one row of the source list: a source or an action.
type evItem int

const (
	evFixes evItem = iota
	evReviews
	evRefreshLocal
	evRefreshReviews
	evDistill
	evItemCount
)

// evLabelW is the label column of the facts in the detail pane and the
// preview. It fits "Repository" and two spaces.
const evLabelW = 12

// evidenceView is the Lessons → Evidence sub-view. It shows the stored
// local fix and review findings, and it starts a refresh preview or a
// distillation from its action rows. A refresh starts only with y in
// the preview, never with enter.
type evidenceView struct {
	st *state

	// cursor is the selected row of the list.
	cursor evItem
	// detail is true while the detail pane has the focus. A compact
	// terminal then shows the detail instead of the list.
	detail bool
	// detailScroll is the position of the detail text. It resets when
	// the cursor moves, because each row has its own text.
	detailScroll ui.ScrollState
	// listOff is the first list line shown when the list is taller than
	// its box.
	listOff int

	// prev is the open refresh preview; nil when no preview is open.
	prev *evPreview
	// seq drops the plan of a preview that the user closed.
	seq generation

	// run is the latest evidence run of this session; nil before one.
	run *evRun
	// runs numbers the runs, so a late message of an earlier run never
	// changes the current one.
	runs generation
}

func newEvidenceView(st *state) *evidenceView { return &evidenceView{st: st} }

// snapshot returns the evidence snapshot and whether a ready index
// backs it. The list and the actions need a ready index.
func (v *evidenceView) snapshot() (learning.Snapshot, bool) {
	sec := &v.st.learning
	if !sec.loaded {
		return learning.Snapshot{}, false
	}

	return sec.value, sec.value.Probe.State == workspace.IndexReady
}

// update routes a message. Keys arrive only while the view is visible.
// Plan, progress and result messages arrive always, so a run that the
// user left keeps its record current.
func (v *evidenceView) update(msg tea.Msg) tea.Cmd {
	switch m := msg.(type) {
	case tea.KeyMsg:
		return v.key(m)
	case evPlanMsg:
		return v.planned(m)
	case evWaitMsg:
		return v.waited(m)
	case evEventMsg:
		v.event(m)
	case evDoneMsg:
		return v.done(m)
	case settingsSavedMsg:
		return v.settingsSaved(m)
	case loadedMsg:
		if m.kind == secLearning {
			return v.learningLoaded()
		}
	}

	return nil
}

// key handles a key of the visible view. An open preview takes every
// key, because it captures.
func (v *evidenceView) key(msg tea.KeyMsg) tea.Cmd {
	if v.prev != nil {
		return v.previewKey(msg)
	}

	snap, ready := v.snapshot()
	if !ready {
		if _, cmd, ok := v.blockedLink(snap); ok && key.Matches(msg, ui.Nav.Enter) && v.st.learning.loaded {
			return cmd
		}

		return nil
	}

	switch {
	case key.Matches(msg, ui.Nav.Enter):
		return v.activate()
	case key.Matches(msg, ui.Nav.Next):
		v.detail = !v.detail
	case key.Matches(msg, ui.Nav.Back):
		v.detail = false
	case v.detail:
		v.detailScroll.Key(msg)
	case key.Matches(msg, ui.Nav.Up):
		v.move(-1)
	case key.Matches(msg, ui.Nav.Down):
		v.move(1)
	case key.Matches(msg, ui.Nav.Top):
		v.move(-int(evItemCount))
	case key.Matches(msg, ui.Nav.Bottom):
		v.move(int(evItemCount))
	}

	return nil
}

// move moves the cursor inside the rows and resets the detail text.
func (v *evidenceView) move(delta int) {
	next := evItem(max(0, min(int(evItemCount)-1, int(v.cursor)+delta)))
	if next != v.cursor {
		v.cursor = next
		v.detailScroll = ui.ScrollState{}
	}
}

// activate runs the enter key on the cursor row. A source row opens its
// detail. A refresh row opens the refresh preview, which only reads. The
// distillation row opens the scope step, which plans nothing yet.
func (v *evidenceView) activate() tea.Cmd {
	switch v.cursor {
	case evRefreshLocal, evRefreshReviews:
		// A running refresh already owns the evidence. Its record shows
		// the progress, so enter opens it instead of a second plan.
		if v.run != nil && v.run.running {
			return navigate(destLessons, "run")
		}

		return v.plan(v.cursorMode())
	case evDistill:
		return navigate(destLessons, "distill")
	default:
		v.detail = true

		return nil
	}
}

// cursorMode returns the refresh mode of the cursor row.
func (v *evidenceView) cursorMode() index.SourceMode {
	if v.cursor == evRefreshReviews {
		return index.SourcesReviewsAndLocal
	}

	return index.SourcesLocal
}

// keys returns the footer keys of the current state. The switch keys
// come last; Lessons adds none for this view.
func (v *evidenceView) keys() []key.Binding {
	if v.prev != nil {
		return v.previewKeys()
	}

	switches := []key.Binding{lesKeys.Proposals, lesKeys.Pins, lesKeys.Stats}

	snap, ready := v.snapshot()
	if !ready {
		link, _, ok := v.blockedLink(snap)
		if !ok || !v.st.learning.loaded {
			return switches
		}

		// A compact footer has room for a short label only; the box
		// names the whole route.
		if !v.st.wide() {
			link = ui.WithHelp(ui.Nav.Enter, "enter", "Overview")
		}

		return append([]key.Binding{link}, switches...)
	}

	if v.detail {
		scroll := ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")
		scroll.SetEnabled(v.detailScroll.Scrolls())

		back := ui.WithHelp(ui.Nav.Next, "tab", "list")
		if !v.st.wide() {
			back = ui.WithHelp(ui.Nav.Back, "esc", "back")
		}

		return append([]key.Binding{scroll, back}, switches...)
	}

	// A compact list opens the detail with enter, so tab leaves the
	// footer there and the switch keys keep their room.
	tab := ui.WithHelp(ui.Nav.Next, "tab", "detail")
	tab.SetEnabled(v.st.wide())

	return append([]key.Binding{ui.WithHelp(ui.Nav.Down, "↑↓", "move"), ui.WithHelp(ui.Nav.Enter, "enter", "open"), tab},
		switches...)
}

// capturing is true while the refresh preview is open. The preview then
// takes every key, so y and esc never reach the shell or Lessons.
func (v *evidenceView) capturing() bool { return v.prev != nil }

// view renders the context line, a blank row and the boxes. The last
// row stays blank above the footer, as in the other sub-views.
func (v *evidenceView) view(w, h int) string {
	return v.contextLine(w) + "\n\n" + v.body(w, max(4, h-3))
}

// body renders the loading, error and blocked states, or the list and
// the detail.
func (v *evidenceView) body(w, h int) string {
	t := v.st.theme
	sec := &v.st.learning

	if !sec.loaded {
		o := ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Evidence sources", true)}

		if sec.err != nil {
			return integBoxed(t, o, ui.ToneFailure, "Could not read the evidence", ui.ErrText(sec.err),
				t.Muted.Render("Press ")+t.Key.Render("r")+t.Muted.Render(" to read it again."))
		}

		return integBoxed(t, o, ui.ToneNeutral, "Reading the evidence"+t.Glyphs.Ellipsis,
			"Seamark reads the stored findings, the review window and the origin remote. Nothing is fetched "+
				"or written.", "")
	}

	snap, ready := v.snapshot()
	if !ready {
		return v.blockedBox(snap, w, h)
	}

	if v.st.wide() {
		listW := min(propListW, w*propListW/propDesignW)

		return ui.Hjoin(1, v.listBox(snap, listW, h, !v.detail), v.detailBox(snap, w-listW-1, h, v.detail))
	}

	if v.detail {
		return v.detailBox(snap, w, h, true)
	}

	return v.listBox(snap, w, h, true)
}

// contextLine renders the counts row under the sub-view line, for
// example "findings 142     reviews 118     fixes 24". An unknown count
// renders "? unknown", never zero. A failed reload shows the stale note
// instead, as on Stats.
func (v *evidenceView) contextLine(w int) string {
	t := v.st.theme
	sec := &v.st.learning

	if sec.stale() {
		return " " + t.Attention.Render(ui.Truncate(t.Glyphs.Warn+" stale: "+ui.ErrText(sec.err)+
			". Showing the snapshot from "+sec.at.Format("15:04:05")+".", w-1, t.Glyphs.Ellipsis))
	}

	snap, ready := v.snapshot()
	if !ready {
		return ""
	}

	known := snap.CountsErr == nil
	unknown := t.Unknown.Render(t.Glyphs.Unknown + " unknown")

	seg := func(label, value string, ok bool) string {
		if !ok {
			return t.Faint.Render(label) + " " + unknown
		}

		return t.Faint.Render(label) + " " + t.Body.Render(value)
	}

	segs := []string{
		seg("findings", strconv.Itoa(evTotal(snap.Findings)), known),
		seg("reviews", strconv.Itoa(snap.ReviewFindings), known),
		seg("fixes", strconv.Itoa(snap.FixFindings), known),
	}

	if agent := v.agent(); agent != "" {
		segs = append(segs, seg("agent", agent, true))
	}

	if snap.Window.ConfigErr != nil {
		segs = append(segs, t.Faint.Render("review window")+" "+t.Attention.Render(t.Glyphs.Warn+" config.yaml error"))
	} else {
		segs = append(segs, seg("review window", evWindowShort(snap.Window), true))
	}

	// Whole segments drop from the right, so no value is ever cut.
	for len(segs) > 1 && ui.Width(" "+strings.Join(segs, "     ")) > w {
		segs = segs[:len(segs)-1]
	}

	return ui.Truncate(" "+strings.Join(segs, "     "), w, t.Glyphs.Ellipsis)
}

// agent returns the resolved distillation command when the semantic
// health knows it, else "". The command line can quote a configured
// value, so ui.Diag redacts it.
func (v *evidenceView) agent() string {
	sec := &v.st.index
	if !sec.loaded || sec.value.Status == nil {
		return ""
	}

	return ui.Diag(sec.value.Status.DistillAgent)
}

// blockedLink returns the key and the navigation that repair an index
// that is not ready. A build migrates an older index and creates a
// missing one. A newer or an unreadable index goes to Overview. Overview
// explains it and never rebuilds a newer index.
func (v *evidenceView) blockedLink(snap learning.Snapshot) (key.Binding, tea.Cmd, bool) {
	switch snap.Probe.State {
	case workspace.IndexReady:
		return key.Binding{}, nil, false
	case workspace.IndexNewer, workspace.IndexUnreadable:
		return ui.WithHelp(ui.Nav.Enter, "enter", "open Overview"), navigate(destOverview, ""), true
	default:
		return ui.WithHelp(ui.Nav.Enter, "enter", "build in Overview"), navigate(destOverview, ovIntentBuild), true
	}
}

// blockedBox explains why the evidence actions are not offered. A
// refresh needs a usable index and never builds, migrates or rewrites
// one, so the box links to the explicit build instead.
func (v *evidenceView) blockedBox(snap learning.Snapshot, w, h int) string {
	t := v.st.theme
	o := ui.BoxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Evidence sources", true)}

	var title, body string

	cli := t.Faint.Render("or in a shell: ") + commandText(t, "seamark index --fixes-only")

	switch snap.Probe.State {
	case workspace.IndexNewer:
		title = "A newer seamark wrote this index"
		body = "This binary never rewrites a newer index, so it offers no evidence refresh. Use the newer " +
			"seamark, or open Overview for the details."
		cli = ""
	case workspace.IndexUnreadable:
		title = "The index could not be read"
		body = ui.ErrText(snap.Probe.Err) + ". A refresh never repairs the index. Overview and Doctor show the details."
		cli = ""
	case workspace.IndexOlder:
		title = "The index needs a rebuild"
		body = "An older seamark wrote this index. A build in Overview migrates it. An evidence refresh " +
			"never migrates the index. The shell command below builds the index first, then mines the " +
			"local fixes."
	default:
		title = "No index yet"
		body = "Evidence lives in the index database, and this workspace has no usable index. An evidence " +
			"refresh needs one and never builds it. The shell command below builds the index first, then " +
			"mines the local fixes."
	}

	link, _, _ := v.blockedLink(snap)
	next := t.Hints([]key.Binding{link})

	if cli != "" {
		next += "\n" + cli
	}

	return integBoxed(t, o, ui.ToneAttention, title, body, next)
}

// evLine is one line of the source list. item is the row that owns the
// line, or -1 for a heading or a rule. drop orders the optional lines:
// a list that does not fit removes the highest drop value first; 0
// never drops.
type evLine struct {
	text string
	item evItem
	drop int
}

// listBox renders the "Evidence sources" box: the two sources, the
// actions, and the rule that nothing runs before y.
func (v *evidenceView) listBox(snap learning.Snapshot, w, h int, focused bool) string {
	t := v.st.theme
	inner, rows := ui.BoxInner(w), ui.BoxRows(h)

	shown, foot := v.fitList(v.listLines(snap, inner), rows, inner, focused)
	if foot == "" && !v.st.wide() {
		foot = "enter for detail"
	}

	if foot != "" {
		foot = t.Faint.Render(foot)
	}

	return t.Box(ui.BoxOpts{
		W: w, H: h, Focus: focused, Foot: foot, Lines: shown,
		Title: integPaneTitle(t, "Evidence sources", focused), Right: t.Muted.Render("2"),
	})
}

// listLines builds every line of the list in w cells.
func (v *evidenceView) listLines(snap learning.Snapshot, w int) []evLine {
	t := v.st.theme

	var out []evLine

	add := func(item evItem, drop int, lines ...string) {
		for _, l := range lines {
			out = append(out, evLine{text: l, item: item, drop: drop})
		}
	}

	add(evFixes, 0, v.sourceItem(snap, evFixes, w)...)
	add(-1, 1, "")
	add(evReviews, 0, v.sourceItem(snap, evReviews, w)...)
	add(-1, 2, "")

	if v.st.wide() {
		add(-1, 3, integRule(t, w))
	}

	add(-1, 0, t.Heading.Render("ACTIONS"))

	for _, it := range []evItem{evRefreshLocal, evRefreshReviews, evDistill} {
		// The cursor row is bold, as a cursor source is.
		style := t.Body
		if it == v.cursor {
			style = t.Title
		}

		add(it, 0, ui.Spread(style.Render(evItemName(it)), v.actionHint(snap, it)+" ", w-2))
	}

	if v.st.wide() {
		add(-1, 4, "", integRule(t, w))
		add(-1, 4, propLines(t.Faint, "Every action shows a preview first. Nothing runs, fetches or writes "+
			"until you press y.", w)...)
	}

	return out
}

// sourceItem renders one source as a list item: the name with its
// freshness badge, then its summary.
func (v *evidenceView) sourceItem(snap learning.Snapshot, it evItem, w int) []string {
	t := v.st.theme
	known := snap.CountsErr == nil

	item := integItem{name: evItemName(it)}

	if it == evFixes {
		item.badge = v.sourceBadge(evFreshness(snap.FixesMinedAt, snap.FixFindings, known), false)
		item.summary = []string{"local git history", "no network", evFindings(snap.FixFindings, known)}
	} else {
		item.badge = v.sourceBadge(evFreshness(snap.ReviewsMinedAt, snap.ReviewFindings, known), true)
		// gh on PATH is no login, so the card says that the fetch alone
		// proves access.
		access := "gh found, login not checked"
		if snap.GHPath == "" {
			access = "gh not on PATH"
		}

		// Without a GitHub origin no fetch can run, so the card does not
		// name a network reach.
		item.summary = []string{"gh", "network"}
		if snap.RepoState != reviews.RepositoryGitHub {
			item.summary = []string{"no fetch"}
		}

		item.summary = append(item.summary, evFindings(snap.ReviewFindings, known),
			"window "+evWindowShort(snap.Window), evRepoShort(snap.Repository, snap.RepoState), access)
	}

	return item.lines(t, w-2, it == v.cursor)
}

// sourceBadge returns the freshness badge, or the activity of a running
// refresh that includes the source.
func (v *evidenceView) sourceBadge(fresh evFresh, reviewSource bool) string {
	t := v.st.theme

	if r := v.run; r != nil && r.running && (!reviewSource || r.plan.Mode == index.SourcesReviewsAndLocal) {
		return t.Accent.Render(t.SpinnerFrame(v.st.tick)) + " " + t.Muted.Render("refreshing"+t.Glyphs.Ellipsis)
	}

	return fresh.badge(t)
}

// actionHint is the faint right label of an action row. It names the
// reach of the action; a known blocker or a running refresh replaces
// it, so the row never promises work that cannot start.
func (v *evidenceView) actionHint(snap learning.Snapshot, it evItem) string {
	t := v.st.theme
	arrow := " " + t.Glyphs.Sep + " preview " + t.Glyphs.Arrow

	if it == evDistill {
		return t.Faint.Render("agent" + arrow)
	}

	if v.run != nil && v.run.running {
		return t.Faint.Render("running " + t.Glyphs.Sep + " Lessons " + t.Glyphs.Arrow + " Run")
	}

	if snap.Window.ConfigErr != nil {
		return t.Attention.Render(t.Glyphs.Warn+" blocked") + t.Faint.Render(arrow)
	}

	if it == evRefreshLocal {
		return t.Faint.Render("local" + arrow)
	}

	// The origin is bound in the plan, so without a GitHub origin no fetch
	// can run. gh is not bound: the run looks for it again, so a missing
	// gh is a warning and not a promise of no network.
	switch {
	case snap.RepoState != reviews.RepositoryGitHub:
		return t.Faint.Render("local only" + arrow)
	case snap.GHPath == "":
		return t.Attention.Render(t.Glyphs.Warn+" no gh") + t.Faint.Render(arrow)
	}

	return t.Faint.Render("GitHub" + arrow)
}

// fitList fits the list lines into rows. It first removes optional
// lines, then scrolls so that every line of the cursor row stays
// visible. It returns the rendered lines and a foot that names hidden
// lines.
func (v *evidenceView) fitList(lines []evLine, rows, w int, focused bool) (shown []string, foot string) {
	t := v.st.theme

	for level := 4; level > 0 && len(lines) > rows; level-- {
		kept := lines[:0:0]

		for _, l := range lines {
			if l.drop != level {
				kept = append(kept, l)
			}
		}

		lines = kept
	}

	first, last := -1, -1

	for i, l := range lines {
		if l.item == v.cursor {
			if first < 0 {
				first = i
			}

			last = i
		}
	}

	off := max(0, min(v.listOff, len(lines)-rows))
	if first >= 0 {
		off = min(off, first)
		off = max(off, last-rows+1)
	}

	off = max(0, off)
	v.listOff = off

	end := min(len(lines), off+max(0, rows))

	for i := off; i < end; i++ {
		l := lines[i]
		prefix := "  "

		if l.item < 0 {
			prefix = ""
		} else if i == first {
			prefix = t.Key.Render(t.Glyphs.Cursor) + " "
		}

		text := prefix + l.text
		if l.item == v.cursor && focused {
			text = t.Highlight(text, w, ui.BgSel)
		}

		shown = append(shown, text)
	}

	switch {
	case off > 0 && end < len(lines):
		foot = "↑↓ more"
	case off > 0:
		foot = "↑ more"
	case end < len(lines):
		foot = "↓ more"
	}

	return shown, foot
}

// evItemName names a row of the list.
func evItemName(it evItem) string {
	switch it {
	case evFixes:
		return evNameFixes
	case evReviews:
		return evNameReviews
	case evRefreshLocal:
		return evModeAction(index.SourcesLocal)
	case evRefreshReviews:
		return evModeAction(index.SourcesReviewsAndLocal)
	default:
		return "Prepare distillation"
	}
}

// detailBox renders the detail of the cursor row in a box that scrolls.
func (v *evidenceView) detailBox(snap learning.Snapshot, w, h int, focused bool) string {
	t := v.st.theme
	inner, rows := ui.BoxInner(w), ui.BoxRows(h)

	body := v.detailLines(snap, inner)

	sc := &v.detailScroll
	from, to := sc.Fit(len(body), rows)

	var foot string
	if sc.Scrolls() {
		foot = t.RangeNote(from, to, len(body))
	}

	return t.Box(ui.BoxOpts{
		W: w, H: h, Focus: focused, Foot: foot, Lines: body[from:to],
		Title: integPaneTitle(t, evItemName(v.cursor), focused),
	})
}

// detailLines renders the detail text of the cursor row in w cells.
func (v *evidenceView) detailLines(snap learning.Snapshot, w int) []string {
	switch v.cursor {
	case evFixes:
		return v.fixesDetail(snap, w)
	case evReviews:
		return v.reviewsDetail(snap, w)
	case evDistill:
		return v.distillDetail(snap, w)
	default:
		return v.refreshDetail(snap, w)
	}
}

// evHeading renders an upper-case section label with an optional faint
// count, after a blank line.
func evHeading(t *ui.Theme, label, count string) []string {
	head := t.Heading.Render(label)
	if count != "" {
		head += "  " + t.Faint.Render(count)
	}

	return []string{"", head}
}

// evCountRows renders label and count pairs with the counts right
// aligned in one column.
func evCountRows(t *ui.Theme, rows [][2]string, w int) []string {
	labelW, countW := 0, 0

	for _, r := range rows {
		labelW = max(labelW, ui.Width(r[0]))
		countW = max(countW, ui.Width(r[1]))
	}

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, ui.Truncate(t.Muted.Render(ui.PadRight(r[0], labelW))+"  "+t.Body.Render(ui.PadLeft(r[1], countW)),
			w, t.Glyphs.Ellipsis))
	}

	return out
}

// unknownCounts explains counts that the store could not read.
func (v *evidenceView) unknownCounts(snap learning.Snapshot, w int) []string {
	t := v.st.theme

	return integHanging(t.Unknown.Render(t.Glyphs.Unknown+" "), t.Muted,
		"Unknown: the stored findings could not be read. "+ui.ErrText(snap.CountsErr), w)
}

// fixesDetail renders the local fix source.
func (v *evidenceView) fixesDetail(snap learning.Snapshot, w int) []string {
	t := v.st.theme
	known := snap.CountsErr == nil
	fresh := evFreshness(snap.FixesMinedAt, snap.FixFindings, known)

	out := []string{t.Title.Render(evNameFixes), fresh.badge(t) + "   " + t.Muted.Render("local git history"), ""}
	out = append(out, propLines(t.Body, "Seamark mines the fix commits that are reachable from HEAD. This "+
		"refresh makes no network call and keeps the stored review findings.", w)...)

	count := ""
	if known {
		count = strconv.Itoa(snap.FixFindings)
	}

	out = append(out, evHeading(t, "FINDINGS", count)...)

	switch rows := evFixRows(snap.Findings); {
	case !known:
		out = append(out, v.unknownCounts(snap, w)...)
	case len(rows) == 0:
		out = append(out, t.Muted.Render("No local fix finding is stored."))
	default:
		out = append(out, evCountRows(t, rows, w)...)
	}

	out = append(out, evHeading(t, "LAST REFRESH", "")...)
	out = append(out, propLines(t.Body, evLastRefresh(snap.FixesMinedAt, snap.FixFindings, known), w)...)
	out = append(out, v.cliRoute("seamark index --fixes-only", w)...)
	out = append(out, evHeading(t, "NEXT", "")...)

	return append(out, propLines(t.Body, "Select "+evModeAction(index.SourcesLocal)+" and press enter to see "+
		"its preview.", w)...)
}

// reviewsDetail renders the GitHub review source: the repository, gh,
// the window and the freshness.
func (v *evidenceView) reviewsDetail(snap learning.Snapshot, w int) []string {
	t := v.st.theme
	known := snap.CountsErr == nil
	fresh := evFreshness(snap.ReviewsMinedAt, snap.ReviewFindings, known)
	sep := " " + t.Glyphs.Sep + " "

	out := []string{t.Title.Render(evNameReviews), fresh.badge(t) + "   " + t.Muted.Render("gh"+sep+"network"), ""}
	out = append(out, propLines(t.Body, "Seamark fetches the pull request review comments of the origin "+
		"repository through gh and groups them into review lessons. Only github.com origins resolve.", w)...)

	count := ""
	if known {
		count = strconv.Itoa(snap.ReviewFindings)
	}

	out = append(out, evHeading(t, "FINDINGS", count)...)

	if known {
		out = append(out, evCountRows(t, [][2]string{
			{"review findings", strconv.Itoa(snap.ReviewFindings)}, {"review lessons", strconv.Itoa(snap.Lessons)},
		}, w)...)
	} else {
		out = append(out, v.unknownCounts(snap, w)...)
	}

	out = append(out, evHeading(t, "REPOSITORY", "")...)

	if repo, ok := evRepo(snap.Repository, snap.RepoState); ok {
		out = append(out, propLines(t.Code, repo, w)...)
	} else {
		out = append(out, propNote(t, ui.ToneAttention, repo+". The review source reads unavailable until "+
			"origin names a github.com repository.", w)...)
	}

	out = append(out, evHeading(t, "ACCESS", "")...)

	if snap.GHPath == "" {
		out = append(out, propNote(t, ui.ToneAttention, "gh not found on PATH. The review source reads unavailable "+
			"until gh is installed and logged in.", w)...)
	} else {
		out = append(out, propLines(t.Body, evGH(snap.GHPath), w)...)
		out = append(out, integHanging(t.Unknown.Render(t.Glyphs.Unknown+" "), t.Unknown,
			"login not checked: only the fetch proves access", w)...)
	}

	out = append(out, evHeading(t, "WINDOW", "")...)
	out = append(out, v.windowLines(snap.Window, w)...)
	out = append(out, evHeading(t, "LAST REFRESH", "")...)
	out = append(out, propLines(t.Body, evLastRefresh(snap.ReviewsMinedAt, snap.ReviewFindings, known), w)...)
	out = append(out, v.cliRoute("seamark index --reviews", w)...)
	out = append(out, evHeading(t, "NEXT", "")...)

	return append(out, propLines(t.Body, "Select "+evModeAction(index.SourcesReviewsAndLocal)+" and press "+
		"enter to see its preview.", w)...)
}

// windowLines renders the review window and its floor, or the reader
// error with the routes that fix it.
func (v *evidenceView) windowLines(win learning.Window, w int) []string {
	t := v.st.theme

	text, ok := evWindow(win)
	if !ok {
		out := propNote(t, ui.ToneAttention, ".seamark/config.yaml could not be read: "+ui.ErrText(win.ConfigErr), w)

		return append(out, propLines(t.Muted, "A refresh stays blocked until the file reads again. Fix "+
			"reviews.window_days in Settings (press 5) or in the file "+settingsFile+".", w)...)
	}

	out := propLines(t.Body, text, w)
	if note := evFloorNote(win); note != "" {
		out = append(out, propLines(t.Faint, note+", so the window is not a strict cutoff.", w)...)
	}

	return out
}

// cliRoute renders the shell command of a source and the caveat that
// the command also updates the index first.
func (v *evidenceView) cliRoute(command string, w int) []string {
	t := v.st.theme

	out := evHeading(t, "CLI ROUTE", "")
	out = append(out, commandText(t, command))

	return append(out, propLines(t.Faint, "The CLI route also updates the index first. The console refresh "+
		"needs a usable index and does not build it.", w)...)
}

// refreshDetail renders a refresh action: what it reads and changes,
// and how it starts and stops.
func (v *evidenceView) refreshDetail(snap learning.Snapshot, w int) []string {
	t := v.st.theme
	mode := v.cursorMode()
	sep := " " + t.Glyphs.Sep + " "

	reach := "local" + sep + "no network"
	lead := "Seamark mines the fix commits that are reachable from HEAD and replaces the stored local fix " +
		"findings. It keeps the stored review findings."
	stop := "The local fix mine cannot stop part way. x in Lessons " + t.Glyphs.Arrow + " Run stops the " +
		"refresh after it."

	// Without a GitHub origin the review mode mines the local fixes only,
	// so it keeps the words of the local refresh.
	github := mode == index.SourcesReviewsAndLocal && snap.RepoState == reviews.RepositoryGitHub

	switch {
	case github && snap.GHPath == "":
		reach = "GitHub" + sep + "network if gh is on PATH at the start"
	case github:
		reach = "GitHub" + sep + "network"
	case mode == index.SourcesReviewsAndLocal:
		reach = "local only" + sep + "no network"
	}

	if github {
		lead = "Seamark mines the local fix commits first, then fetches the review comments of " +
			evRepoShort(snap.Repository, snap.RepoState) + " through gh. The two sources succeed or fail on " +
			"their own, and a failed or stopped fetch keeps the stored reviews."
		stop = "x in Lessons " + t.Glyphs.Arrow + " Run stops the GitHub fetch. A partial fetch is never saved."
	}

	out := []string{t.Title.Render(evModeAction(mode)), t.Muted.Render(reach), ""}
	out = append(out, propLines(t.Body, lead, w)...)

	if note := evReachNote(mode, github, snap.GHPath); note != "" {
		out = append(out, "")
		out = append(out, propNote(t, ui.ToneAttention, note, w)...)
	}

	if snap.Window.ConfigErr != nil {
		out = append(out, evHeading(t, "BLOCKED", "")...)
		out = append(out, propNote(t, ui.ToneAttention, ".seamark/config.yaml could not be read: "+
			ui.ErrText(snap.Window.ConfigErr), w)...)
		out = append(out, propLines(t.Muted, "The preview links to Settings "+t.Glyphs.Arrow+" evidence. An "+
			"editor on "+settingsFile+" fixes it too.", w)...)
	}

	out = append(out, evHeading(t, "BEFORE IT STARTS", "")...)
	out = append(out, propLines(t.Body, "enter opens a preview of the workspace, the sources and the stored "+
		"data that change. Nothing runs, fetches or writes until you press y there.", w)...)
	out = append(out, evHeading(t, "DURING THE RUN", "")...)

	return append(out, propLines(t.Body, stop, w)...)
}

// evReachNote warns when the review mode cannot fetch, or can fetch only
// if gh is on PATH at the start. It returns "" for every other case.
func evReachNote(mode index.SourceMode, github bool, ghPath string) string {
	switch {
	case mode != index.SourcesReviewsAndLocal:
		return ""
	case !github:
		return "origin names no GitHub repository, so this refresh can only mine the local fixes. The review " +
			"source reads unavailable."
	case ghPath == "":
		return "gh is not on PATH now. The refresh looks for gh again when it starts: with gh it fetches, " +
			"without gh the review source reads unavailable."
	default:
		return ""
	}
}

// distillDetail renders the distillation action. It links to the scope
// step and says that this view plans and sends nothing.
func (v *evidenceView) distillDetail(snap learning.Snapshot, w int) []string {
	t := v.st.theme
	sep := " " + t.Glyphs.Sep + " "

	out := []string{t.Title.Render(evItemName(evDistill)), t.Muted.Render("agent" + sep + "preview"), ""}
	out = append(out, propLines(t.Body, "Opens the scope step of a distillation in Lessons "+t.Glyphs.Arrow+
		" Run. You choose the region and the group limit, then review the plan. The agent runs only after y "+
		"on that plan.", w)...)

	if snap.CountsErr == nil {
		out = append(out, evHeading(t, "EVIDENCE NOW", "")...)
		out = append(out, evCountRows(t, [][2]string{
			{"findings", strconv.Itoa(evTotal(snap.Findings))},
			{"review findings", strconv.Itoa(snap.ReviewFindings)},
			{"local fix findings", strconv.Itoa(snap.FixFindings)},
		}, w)...)
	}

	out = append(out, evHeading(t, "NEXT", "")...)

	return append(out, propLines(t.Body, "enter opens the scope step. Nothing is planned or sent before you "+
		"ask for the plan.", w)...)
}
