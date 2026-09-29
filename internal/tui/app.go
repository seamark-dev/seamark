package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/workspace"
)

// destination is one of the four stable top-level views.
type destination int

const (
	destOverview destination = iota
	destIntegrations
	destLessons
	destDoctor
	destCount
)

// screen is one destination. The shell owns the chrome, the global keys
// and the overlays; a screen owns its content and its local keys.
type screen interface {
	// title is the destination name in the tab bar.
	title() string
	// open runs when the destination becomes active. intent names a
	// workflow to start, for example "setup:codex"; "" opens normally.
	open(intent string) tea.Cmd
	// update handles a message. Key messages arrive only when the shell
	// did not consume them; every other message reaches every screen.
	update(msg tea.Msg) tea.Cmd
	// view renders the content area of w by h cells.
	view(w, h int) string
	// keys returns the contextual bindings for the footer and help.
	keys() []key.Binding
	// capturing reports a focused text input or an open screen overlay.
	// Global shortcuts then reach the screen instead of the shell.
	capturing() bool
}

// tabCounter is an optional screen method: the count beside its tab,
// for example the attention items or the pending proposals. "" hides it.
type tabCounter interface {
	tabCount() string
}

// modalScreen is an optional screen method. While it returns ok, the
// shell dims the whole frame and places the returned block on top, so a
// preview or a confirmation reads as a separate layer.
type modalScreen interface {
	modal(w, h int) (block string, ok bool)
}

// bannerScreen is an optional screen method: a full-width line for the
// notice row, for example the stale-index banner. Messages and running
// work take the row first.
type bannerScreen interface {
	banner(w int) (line string, ok bool)
}

// chromeHeight is the header, the tab bar, the tab rule, the notice row
// and the footer.
const chromeHeight = 5

// model is the root Bubble Tea model: the shell around the screens.
type model struct {
	st      *state
	screens [destCount]screen
	active  destination

	help bool
	// helpOff is the first visible row of the help body. The help
	// scrolls, because its key list does not fit a small terminal.
	helpOff int
	// welcome is the first-run guide of a fresh workspace; nil hides it.
	// The tabs fade and the banners stay away while it shows.
	welcome     *firstRun
	quitConfirm bool
	quitAfter   bool
	quitting    bool

	toast    toastMsg
	toastSeq int
	ticking  bool
}

// after schedules a timed message. Tests replace it, because a real
// timer would make every test wait.
var after = tea.Tick

// freshMsg carries the first-run check.
type freshMsg struct {
	fresh bool
	err   error
}

// clearToastMsg expires a status line message.
type clearToastMsg struct{ seq int }

// newModel builds the shell over the services.
func newModel(svc Services, theme *Theme) *model {
	st := &state{svc: svc, theme: theme}

	m := &model{st: st}
	m.screens[destOverview] = newOverview(st)
	m.screens[destIntegrations] = newIntegrations(st)
	m.screens[destLessons] = newLessons(st)
	m.screens[destDoctor] = newDoctor(st)

	return m
}

// Init loads every source and runs the first-run check. Nothing here
// writes: opening the console is inspection only.
func (m *model) Init() tea.Cmd {
	svc := m.st.svc

	return tea.Batch(
		m.st.load(allSections...),
		func() tea.Msg {
			fresh, err := svc.Fresh()

			return freshMsg{fresh: fresh, err: err}
		},
		m.screens[m.active].open(""),
		m.startTick(),
	)
}

// Update routes messages: shell overlays first, then global keys, then
// the active screen.
func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.st.width, m.st.height = msg.Width, msg.Height
		cmd := m.broadcast(msg)

		return m, cmd

	case tea.KeyMsg:
		// A key can start a mutation, so the activity ticker may need
		// to start after the key is handled.
		cmd := m.handleKey(msg)

		return m, tea.Batch(cmd, m.startTick())

	case loadedMsg:
		if !m.st.store(msg) {
			return m, nil
		}

		cmd := m.broadcast(msg)

		return m, cmd

	case freshMsg:
		if msg.err != nil {
			return m, toast(ToneAttention, "Could not check for existing setup: "+errText(msg.err))
		}

		if msg.fresh {
			cmd := m.startFirstRun()

			return m, cmd
		}

		return m, nil

	case firstRunMsg:
		cmd := m.firstRunResult(msg)

		return m, tea.Batch(cmd, m.startTick())

	case refreshMsg:
		return m, tea.Batch(m.st.load(msg.kinds...), m.startTick())

	case navigateMsg:
		cmd := m.goTo(msg.dest, msg.intent)

		return m, cmd

	case toastMsg:
		m.toastSeq++
		m.toast = msg
		seq := m.toastSeq

		ttl := 6 * time.Second
		if msg.tone == ToneFailure || msg.tone == ToneAttention {
			ttl = 15 * time.Second
		}

		return m, after(ttl, func(time.Time) tea.Msg { return clearToastMsg{seq: seq} })

	case clearToastMsg:
		if msg.seq == m.toastSeq {
			m.toast = toastMsg{}
		}

		return m, nil

	case mutationDoneMsg:
		m.st.mutation = ""

		// The quit question applies only to running work. The dialog
		// closes, so the result of the work becomes visible.
		m.quitConfirm = false

		if m.quitAfter {
			m.quitting = true

			return m, tea.Quit
		}

		cmd := m.broadcast(msg)

		return m, cmd

	case tickMsg:
		m.st.tick++

		if m.st.loading() || m.st.busy() {
			next := m.tickCmd()

			return m, next
		}

		m.ticking = false
		cmd := m.broadcast(msg)

		return m, cmd
	}

	cmd := m.broadcast(msg)

	return m, cmd
}

// broadcast forwards a message to every screen, so background results
// reach a screen even when it is not active.
func (m *model) broadcast(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.screens)+1)

	for _, s := range m.screens {
		cmds = append(cmds, s.update(msg))
	}

	cmds = append(cmds, m.startTick())

	return tea.Batch(cmds...)
}

// startTick starts the activity ticker when work runs and no ticker
// is active. The ticker stops by itself when the work ends.
func (m *model) startTick() tea.Cmd {
	if m.ticking || (!m.st.loading() && !m.st.busy()) {
		return nil
	}

	m.ticking = true

	return m.tickCmd()
}

func (m *model) tickCmd() tea.Cmd {
	return after(120*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

// goTo switches the destination and forwards the intent.
func (m *model) goTo(dest destination, intent string) tea.Cmd {
	m.active = dest
	m.help = false

	return m.screens[dest].open(intent)
}

// handleKey applies the routing order: the size guard, the quit dialog,
// interrupt, the shell overlays, text input, global shortcuts, then the
// active screen.
func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	// Below the minimum size, the resize message hides every screen. A
	// key must not reach a hidden confirmation, because it could commit
	// a durable change that the user never saw.
	if m.cramped() {
		return m.crampedKey(msg)
	}

	if m.quitConfirm {
		return m.quitDialogKey(msg)
	}

	// Interrupt comes before the overlays, so ctrl+c leaves the console
	// from the help and the welcome overlays too.
	if key.Matches(msg, gkeys.Interrupt) {
		return m.requestQuit()
	}

	switch {
	case m.help:
		return m.helpKey(msg)
	case m.welcome != nil:
		return m.welcomeKey(msg)
	}

	scr := m.screens[m.active]
	if scr.capturing() {
		return scr.update(msg)
	}

	switch {
	case key.Matches(msg, gkeys.Quit):
		return m.requestQuit()
	case key.Matches(msg, gkeys.Help):
		m.openHelp()

		return nil
	case key.Matches(msg, gkeys.Overview):
		return m.goTo(destOverview, "")
	case key.Matches(msg, gkeys.Integrations):
		return m.goTo(destIntegrations, "")
	case key.Matches(msg, gkeys.Lessons):
		return m.goTo(destLessons, "")
	case key.Matches(msg, gkeys.Doctor):
		return m.goTo(destDoctor, "")
	case key.Matches(msg, gkeys.Refresh):
		return tea.Batch(m.st.load(allSections...), m.startTick(),
			toast(ToneNeutral, "Reading the current state from disk. Nothing is rebuilt or fetched."))
	}

	return scr.update(msg)
}

// requestQuit quits at once when idle. While a mutation runs it asks
// first, because the running work cannot stop safely.
func (m *model) requestQuit() tea.Cmd {
	if m.st.busy() {
		m.quitConfirm = true

		return nil
	}

	m.quitting = true

	return tea.Quit
}

// quitDialogKey answers the quit question. A yes with no running work
// quits at once: a latched quitAfter would otherwise exit the console
// after a later, unrelated mutation and hide its result.
func (m *model) quitDialogKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter), msg.String() == "y":
		m.quitConfirm = false

		if !m.st.busy() {
			m.quitting = true

			return tea.Quit
		}

		m.quitAfter = true

		return toast(ToneAttention, "Seamark exits when "+m.st.mutation+" finishes.")
	case key.Matches(msg, nkeys.Back), msg.String() == "n":
		m.quitConfirm = false
		m.quitAfter = false
	}

	return nil
}

// cramped reports a known terminal size below the supported minimum.
// A zero width means that no size message arrived yet.
func (m *model) cramped() bool {
	w, h := m.st.width, m.st.height

	return w > 0 && (w < minWidth || h < minHeight)
}

// crampedKey handles a key while the terminal is below the minimum size.
// Only quit, interrupt and help act, because tooSmall shows only them;
// esc also closes the compact help. Every other key is dropped, so no
// hidden screen or overlay changes.
func (m *model) crampedKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case m.quitConfirm:
		return m.quitDialogKey(msg)
	case key.Matches(msg, gkeys.Interrupt, gkeys.Quit):
		// A capturing screen does not get q here, because its view is
		// hidden and the user expects the advertised quit.
		return m.requestQuit()
	case key.Matches(msg, gkeys.Help):
		if m.help {
			m.help = false
		} else {
			m.openHelp()
		}
	case m.help && key.Matches(msg, nkeys.Back):
		m.help = false
	}

	return nil
}

// openHelp shows the help overlay from its first row.
func (m *model) openHelp() {
	m.help = true
	m.helpOff = 0
}

// helpKey scrolls or closes the help overlay. The offset is clamped
// before and after the move, so a resize never leaves it out of range.
func (m *model) helpKey(msg tea.KeyMsg) tea.Cmd {
	if key.Matches(msg, nkeys.Back, gkeys.Help, gkeys.Quit, nkeys.Enter) {
		m.help = false

		return nil
	}

	rows, window := m.helpLayout(m.st.width, m.st.height-1)
	last := max(0, len(rows)-window)
	off := max(0, min(m.helpOff, last))

	switch {
	case key.Matches(msg, nkeys.Up):
		off--
	case key.Matches(msg, nkeys.Down):
		off++
	case key.Matches(msg, nkeys.PageUp):
		off -= window
	case key.Matches(msg, nkeys.PageDown):
		off += window
	case key.Matches(msg, nkeys.Top):
		off = 0
	case key.Matches(msg, nkeys.Bottom):
		off = last
	}

	m.helpOff = max(0, min(off, last))

	return nil
}

// View renders the shell. Every region has a fixed height, so content
// never pushes the footer off the screen.
func (m *model) View() string {
	if m.quitting {
		return ""
	}

	w, h := m.st.width, m.st.height
	if w == 0 || h == 0 {
		return ""
	}

	var frame string

	if m.cramped() {
		frame = m.tooSmall(w, h)
	} else {
		frame = m.frame(w, h)
	}

	// The ASCII glyph set must hold for the whole frame. A screen can
	// write a Unicode symbol directly, so the shell replaces it last.
	return m.st.theme.asciiFrame(frame)
}

// frame renders the chrome and the body at a supported size. A modal
// dims everything above the footer, so the footer keys stay readable.
func (m *model) frame(w, h int) string {
	bodyH := h - chromeHeight
	scr := m.screens[m.active]

	// The first-run guide replaces the body. It is not a modal: nothing
	// is set up yet, so there is no content to dim behind it.
	var body string
	if m.welcome != nil {
		body = fit(m.welcome.view(w, bodyH), w, bodyH)
	} else {
		body = fit(scr.view(w, bodyH), w, bodyH)
	}

	top := strings.Join([]string{m.header(w), m.tabs(w), m.tabRule(w), m.notice(w), body}, "\n")

	if block, ok := m.modalBlock(w, h-1); ok {
		top = m.st.theme.overlay(top, block, w, h-1)
	}

	return top + "\n" + m.footer(w)
}

// modalBlock returns the modal that covers the frame, if any. Shell
// modals come first, because they answer global keys.
func (m *model) modalBlock(w, h int) (string, bool) {
	switch {
	case m.quitConfirm:
		return m.quitView(w, h), true
	case m.help:
		return m.helpView(w, h), true
	case m.welcome != nil:
		// The guide is in the body; no screen modal may cover it.
		return "", false
	}

	if ms, ok := m.screens[m.active].(modalScreen); ok {
		return ms.modal(w, h)
	}

	return "", false
}

// header renders " ▶┊◀ seamark  ~/path          ● index current · 6h ago   v0.6.0 ".
// A narrow terminal drops the version first, then shortens the path from
// the left, so the repository name stays visible.
func (m *model) header(w int) string {
	t := m.st.theme

	right := m.indexBadge()
	if w >= wideWidth {
		right += "   " + t.Faint.Render(oneLine(m.st.svc.Version()))
	}

	right += " "

	brand := " " + t.brand() + "  "
	room := w - width(brand) - width(right) - 2
	path := t.Workspace.Render(truncateLeft(oneLine(displayPath(m.st.svc.Root())), max(8, room), t.Glyphs.Ellipsis))

	return spread(brand+path, right, w)
}

// indexBadge summarizes the index for the header: a colored dot and a
// short state, never a percentage or an overall score.
func (m *model) indexBadge() string {
	t := m.st.theme
	g := t.Glyphs
	sec := &m.st.index

	dot := func(tone Tone, text string) string {
		return t.ToneStyle(tone).Render(g.Dot) + " " + t.Muted.Render(text)
	}

	if !sec.loaded {
		if sec.err != nil {
			return dot(ToneFailure, "index unreadable")
		}

		return t.Faint.Render(g.Hollow + " index " + g.Ellipsis)
	}

	v := sec.value
	age := ""

	if v.Probe.IndexedAt > 0 {
		age = " " + g.Sep + " " + ageSince(v.Probe.IndexedAt)
	}

	switch v.Probe.State {
	case workspace.IndexAbsent:
		return t.Faint.Render(g.Hollow + " no index")
	case workspace.IndexReady:
		if v.Status == nil {
			return dot(ToneUnknown, "index health unknown")
		}

		switch v.Status.Freshness {
		case "current":
			return dot(ToneSuccess, "index current"+age)
		case "stale":
			return t.Attention.Render(g.Dot + " index stale" + age)
		default:
			return dot(ToneUnknown, "index freshness unknown"+age)
		}
	case workspace.IndexNotIndexed:
		return t.Attention.Render(g.Dot + " not indexed")
	case workspace.IndexOlder:
		return t.Attention.Render(g.Dot + " index needs upgrade")
	default:
		return t.Failure.Render(g.Dot + " index " + v.Probe.State.String())
	}
}

// ageSince renders the age of a Unix time as "6h ago".
func ageSince(unix int64) string {
	d := time.Since(time.Unix(unix, 0))

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	default:
		return itoa(int(d.Hours()/24)) + "d ago"
	}
}

// tabSegment returns the tab label of destination i without styles, and
// its styled form. The two have the same width.
func (m *model) tabSegment(i int) (plain, styled string) {
	t := m.st.theme
	name := m.screens[i].title()

	// The first-run guide disables the tabs, so they show no count, as
	// in the 1a design.
	count := ""
	if tc, ok := m.screens[i].(tabCounter); ok && m.welcome == nil {
		count = tc.tabCount()
	}

	plain = " " + itoa(i+1) + " " + name
	if count != "" {
		plain += " " + count
	}

	plain += " "

	switch {
	case m.welcome != nil:
		styled = " " + t.Faint.Render(itoa(i+1)+" "+name)
		if count != "" {
			styled += " " + t.Faint.Render(count)
		}
	case destination(i) == m.active:
		styled = " " + t.TabActive.Render(itoa(i+1)+" "+name)
		if count != "" {
			styled += " " + t.Accent.Render(count)
		}
	default:
		styled = " " + t.TabNum.Render(itoa(i+1)) + " " + t.TabIdle.Render(name)
		if count != "" {
			styled += " " + t.Faint.Render(count)
		}
	}

	return plain, styled + " "
}

// tabs renders "  1 Overview 5   2 Integrations   3 Lessons 6   4 Doctor".
func (m *model) tabs(w int) string {
	var b strings.Builder

	for i := range m.screens {
		_, styled := m.tabSegment(i)
		b.WriteString(" " + styled)
	}

	return truncate(b.String(), w, "")
}

// tabRule underlines the active tab with the heavy rule glyph, so the
// active destination is visible without color.
func (m *model) tabRule(w int) string {
	t := m.st.theme
	g := t.Glyphs

	var b strings.Builder

	used := 0

	for i := range m.screens {
		plain, _ := m.tabSegment(i)
		segW := width(plain)

		b.WriteString(t.Rule.Render(g.RuleLight))

		if destination(i) == m.active && m.welcome == nil {
			b.WriteString(t.RuleOn.Render(strings.Repeat(g.RuleHeavy, segW)))
		} else {
			b.WriteString(t.Rule.Render(strings.Repeat(g.RuleLight, segW)))
		}

		used += segW + 1
	}

	if rest := w - used; rest > 0 {
		b.WriteString(t.Rule.Render(strings.Repeat(g.RuleLight, rest)))
	}

	return truncate(b.String(), w, "")
}

// notice renders the row under the tabs: the latest message, else the
// running work, else a screen banner such as the stale index, else a
// blank spacer line.
func (m *model) notice(w int) string {
	t := m.st.theme

	var line string

	switch {
	case m.toast.text != "":
		line = " " + t.ToneStyle(m.toast.tone).Render(t.ToneGlyph(m.toast.tone)) + " " + t.Body.Render(oneLine(m.toast.text))
	case m.st.busy():
		line = " " + t.Accent.Render(t.spinnerFrame(m.st.tick)) + " " + t.Body.Render(m.st.mutation)
	case m.st.loading():
		line = " " + t.Accent.Render(t.spinnerFrame(m.st.tick)) + " " + t.Muted.Render("Reading the current state"+t.Glyphs.Ellipsis)
	default:
		if bs, ok := m.screens[m.active].(bannerScreen); ok && m.welcome == nil {
			if banner, ok := bs.banner(w); ok {
				return fit(banner, w, 1)
			}
		}
	}

	return fit(truncate(line, w, t.Glyphs.Ellipsis), w, 1)
}

// latestSnapshot returns the newest load time over all sources.
func (m *model) latestSnapshot() time.Time {
	var at time.Time

	for _, t := range []time.Time{m.st.doctor.at, m.st.index.at, m.st.integrations.at, m.st.ledger.at, m.st.stats.at} {
		if t.After(at) {
			at = t
		}
	}

	return at
}

// footer renders the contextual keys at the left and the global keys
// with the snapshot clock at the right.
func (m *model) footer(w int) string {
	t := m.st.theme

	var local []key.Binding

	switch {
	case m.quitConfirm:
		local = []key.Binding{withHelp(nkeys.Enter, "enter", "exit after completion"), withHelp(nkeys.Back, "esc", "keep working")}
	case m.help:
		local = []key.Binding{withHelp(nkeys.Back, "esc", "close")}

		if rows, window := m.helpLayout(w, m.st.height-1); len(rows) > window {
			local = append([]key.Binding{withHelp(nkeys.Down, "↑↓", "scroll")}, local...)
		}
	case m.welcome != nil:
		local = m.welcome.keys()
	default:
		local = m.screens[m.active].keys()
	}

	// An overlay or a capturing screen receives the global keys itself,
	// so their hints would be wrong there. The quit key stays visible.
	global := []key.Binding{gkeys.Quit}
	if !m.help && !m.quitConfirm && m.welcome == nil && !m.screens[m.active].capturing() {
		global = []key.Binding{gkeys.Refresh, gkeys.Help, gkeys.Quit}
	}

	right := t.hints(global)
	if at := m.latestSnapshot(); !at.IsZero() {
		right += "   " + t.Faint.Render(at.Format("15:04:05"))
	}

	right += " "

	// The global hints always stay: "? help" leads to every key, so the
	// local hints give way first.
	left := " " + m.footerHints(local, w-width(right)-4)

	return spread(left, right, w)
}

// footerHints renders the leading hints that fit in room cells. It
// drops whole hints from the end and marks the cut with an ellipsis, so
// no hint breaks in the middle of a word.
func (m *model) footerHints(bindings []key.Binding, room int) string {
	t := m.st.theme

	var shown []key.Binding

	for _, b := range bindings {
		if b.Enabled() && b.Help().Key != "" {
			shown = append(shown, b)
		}
	}

	for n := len(shown); n > 0; n-- {
		line := t.hints(shown[:n])
		if n < len(shown) {
			line += t.Faint.Render("  " + t.Glyphs.Ellipsis)
		}

		if width(line) <= room {
			return line
		}
	}

	// No hint fits: the ellipsis still says that keys exist, and "? help"
	// at the right lists them.
	if len(shown) > 0 && room >= width(t.Glyphs.Ellipsis) {
		return t.Faint.Render(t.Glyphs.Ellipsis)
	}

	return ""
}

// tooSmall replaces the console below the minimum size. It shows only
// what crampedKey accepts: quit, help, and an open quit question,
// because that question still waits for an answer.
func (m *model) tooSmall(w, h int) string {
	t := m.st.theme
	textW := max(10, w-4)

	// keyRows lists one key per line, because a narrow terminal cannot
	// hold the hints side by side.
	keyRows := func(rows ...[2]string) string {
		lines := make([]string, 0, len(rows))

		for _, r := range rows {
			lines = append(lines, t.Key.Render(padRight(r[0], 7))+" "+t.Muted.Render(r[1]))
		}

		return strings.Join(lines, "\n")
	}

	var msg string

	switch {
	case m.quitConfirm:
		msg = t.Title.Render(wrap("Exit after the running operation?", textW)) + "\n\n" +
			t.Body.Render(wrap(m.st.mutation+" is running and cannot stop safely. "+
				"Completed changes remain either way.", textW)) + "\n\n" +
			keyRows([2]string{"enter", "exit after completion"}, [2]string{"esc", "keep working"})
	case m.help:
		msg = t.Title.Render("Keys at this size") + "\n\n" +
			keyRows([2]string{"q", "quit"}, [2]string{"ctrl+c", "quit"}, [2]string{"?", "close help"}) + "\n\n" +
			t.Muted.Render(wrap("Other keys work again at "+itoa(minWidth)+"×"+itoa(minHeight)+
				" cells or more.", textW))
	default:
		msg = t.Title.Render("More space needed") + "\n\n" +
			t.Muted.Render(wrap("Seamark needs at least "+itoa(minWidth)+"×"+itoa(minHeight)+
				" cells; this terminal is "+itoa(w)+"×"+itoa(h)+". Enlarge the window.", textW)) +
			"\n\n" + t.hints([]key.Binding{gkeys.Quit, gkeys.Help})
	}

	// Place does not shorten a block taller than h, so fit clips it to
	// the terminal.
	return fit(lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, msg), w, h)
}

func (m *model) quitView(w, h int) string {
	t := m.st.theme
	bw := min(w-4, 64)
	inner := boxInner(bw)

	lines := strings.Split(t.Body.Render(wrap(m.st.mutation+" is running. It cannot stop safely at this point, "+
		"so Seamark can exit when it finishes. Already completed changes remain either way.", inner)), "\n")

	lines = append(lines, "", t.hints([]key.Binding{
		withHelp(nkeys.Enter, "enter", "exit after completion"), withHelp(nkeys.Back, "esc", "keep working"),
	}))

	return t.box(boxOpts{
		W: bw, H: min(h, len(lines)+2), Focus: true, Fill: true, Lines: lines,
		Title: t.Title.Render("Exit after the running operation?"),
	})
}

// helpKeyW is the width of the key column in the help rows.
const helpKeyW = 11

// helpColW is the width of one help column in the wide layout.
const helpColW = 30

// helpView renders the help modal. Wide terminals show three columns;
// narrow ones show one column that scrolls. The title and the border
// always show.
func (m *model) helpView(w, h int) string {
	t := m.st.theme
	bw, bh := m.helpBox(w, h)

	rows, window := m.helpLayout(w, h)
	off := max(0, min(m.helpOff, len(rows)-window))

	foot := ""
	if len(rows) > window {
		foot = t.Faint.Render(itoa(off+1) + "–" + itoa(min(len(rows), off+window)) + " of " + itoa(len(rows)))
	}

	// The modal fits its content, so a short key list leaves no blank
	// band inside the border.
	return t.box(boxOpts{
		W: bw, H: min(bh, window+2), Focus: true, Fill: true, Lines: rows[off:min(len(rows), off+window)],
		Title: t.TabActive.Render("Keys"), Right: t.Muted.Render("? or esc to close"), Foot: foot,
	})
}

// helpBox returns the outer size of the help modal for a w by h area.
func (m *model) helpBox(w, h int) (bw, bh int) {
	return min(w-4, 96), min(h-2, 24)
}

// helpLayout returns the help body as display lines and the number of
// lines the modal shows at once.
func (m *model) helpLayout(w, h int) (rows []string, window int) {
	t := m.st.theme
	bw, bh := m.helpBox(w, h)
	inner := boxInner(bw)

	entry := func(b key.Binding) string {
		hp := b.Help()

		return t.Key.Render(padRight(t.keyLabel(hp.Key), helpKeyW)) + t.Muted.Render(hp.Desc)
	}

	enabled := func(bindings []key.Binding) []key.Binding {
		var out []key.Binding

		for _, b := range bindings {
			if b.Enabled() && b.Help().Key != "" {
				out = append(out, b)
			}
		}

		return out
	}

	groups := []struct {
		title    string
		bindings []key.Binding
	}{
		{"GLOBAL", []key.Binding{
			withHelp(gkeys.Overview, "1–4", "jump to tab"), withHelp(nkeys.Next, "tab", "switch pane"),
			gkeys.Refresh, withHelp(gkeys.Help, "?", "this help"), gkeys.Quit,
		}},
		{"MOVE", []key.Binding{
			withHelp(nkeys.Down, "↑↓  j k", "move"), withHelp(nkeys.Top, "g  G", "top / bottom"),
			withHelp(nkeys.Enter, "enter", "open"), withHelp(nkeys.Back, "esc", "back / close"),
			withHelp(nkeys.PageDown, "pgup pgdn", "scroll detail"),
		}},
		{strings.ToUpper(m.screens[m.active].title()), enabled(m.screens[m.active].keys())},
	}

	if inner >= 3*helpColW {
		// Three columns, as in the 1a design.
		header := ""
		for _, g := range groups {
			header += padRight(t.Heading.Render(g.title), helpColW)
		}

		rows = append(rows, header, "")

		depth := 0
		for _, g := range groups {
			depth = max(depth, len(g.bindings))
		}

		for r := 0; r < depth; r++ {
			line := ""

			for _, g := range groups {
				c := ""
				if r < len(g.bindings) {
					c = entry(g.bindings[r])
				}

				line += padRight(truncate(c, helpColW-1, t.Glyphs.Ellipsis), helpColW)
			}

			rows = append(rows, line)
		}
	} else {
		for i, g := range groups {
			if i > 0 {
				rows = append(rows, "")
			}

			rows = append(rows, t.Heading.Render(g.title))

			for _, b := range g.bindings {
				rows = append(rows, entry(b))
			}
		}
	}

	rows = append(rows, "", t.Heading.Render("PLAIN COMMANDS"))

	for _, c := range []string{"seamark doctor", "seamark status", "seamark index",
		"seamark init --client <claude|codex>", "seamark lessons --proposals", "seamark lessons --stats"} {
		rows = append(rows, t.command(c))
	}

	rows = append(rows, "", t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner)))
	rows = append(rows, strings.Split(t.Faint.Render(wrap("Every key that writes a file asks first. "+
		"Nothing changes before you confirm with y.", inner)), "\n")...)

	window = max(1, boxRows(bh))

	return rows, min(window, len(rows))
}
