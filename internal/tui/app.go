package tui

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/workspace"
)

// destination is one of the five stable top-level views. A new
// destination goes last, so the number keys of the others never change.
type destination int

const (
	destOverview destination = iota
	destIntegrations
	destLessons
	destDoctor
	destSettings
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

// shortTitler is an optional screen method: a short tab label for a
// terminal that is too narrow for the full tab row. The help keeps the
// full title, because it has room for it.
type shortTitler interface {
	shortTitle() string
}

// leaveGuard is an optional screen method. The shell asks it before it
// opens another destination, because a screen with an unsaved draft
// asks the user first. ok=false keeps the current destination; the
// screen then shows its own question and later sends a navigateMsg.
type leaveGuard interface {
	leave(next destination, intent string) (cmd tea.Cmd, ok bool)
}

// intentShower is an optional screen method. It reports whether the
// screen shows the view that intent opens, for example the Run sub-view
// for "run". The notice row then leaves out its hint to that view,
// because the user sees the view already (design map §6).
type intentShower interface {
	showsIntent(intent string) bool
}

// helpKeyer is an optional screen method: the keys that the help lists,
// when they differ from the footer keys. The footer has room for the
// keys that act now; the help has room for every key of the screen,
// also the unavailable entries.
type helpKeyer interface {
	helpKeys() []key.Binding
}

// tabShortOrder is the order in which the tab labels abbreviate. The
// longest label goes first, so the row keeps as many full names as it
// can.
var tabShortOrder = []destination{destIntegrations, destSettings, destOverview}

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

	// exitBy is the deadline of a confirmed stop-and-exit; zero when no
	// such exit waits. exitSeq drops the timer of an exit that the user
	// cancelled.
	exitBy  time.Time
	exitSeq generation

	toast    toastMsg
	toastSeq generation
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

// exitDeadlineMsg ends the wait of a confirmed stop-and-exit. The
// console then exits even when the stopped work has not ended yet.
type exitDeadlineMsg struct{ seq int }

// newModel builds the shell over the services. The root context is
// the background context until Run replaces it with a context that it
// cancels at exit.
func newModel(svc Services, theme *Theme) *model {
	st := &state{svc: svc, theme: theme, ctx: context.Background()}

	m := &model{st: st}
	m.screens[destOverview] = newOverview(st)
	m.screens[destIntegrations] = newIntegrations(st)
	m.screens[destLessons] = newLessons(st)
	m.screens[destDoctor] = newDoctor(st)
	m.screens[destSettings] = newSettings(st)

	return m
}

// Init loads every source and runs the first-run check. Nothing here
// writes: opening the console is inspection only.
func (m *model) Init() tea.Cmd {
	var svc Reader = m.st.svc

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
		seq := m.toastSeq.start()
		m.toast = msg

		ttl := 6 * time.Second
		if msg.tone == ToneFailure || msg.tone == ToneAttention {
			ttl = 15 * time.Second
		}

		return m, after(ttl, func(time.Time) tea.Msg { return clearToastMsg{seq: seq} })

	case clearToastMsg:
		if m.toastSeq.current(msg.seq) {
			m.toast = toastMsg{}
		}

		return m, nil

	case mutationDoneMsg:
		m.st.mutation = ""
		m.st.run = nil

		// The running-work question ends with the work. The dialog
		// closes, so the result of the work becomes visible. A later
		// quit asks again when a Settings draft remains.
		m.quitConfirm = false

		if m.quitAfter {
			m.quitting = true

			return m, tea.Quit
		}

		cmd := m.broadcast(msg)

		return m, cmd

	case exitDeadlineMsg:
		// The stopped work did not end in time. The console exits now;
		// Run spends the rest of the same budget on the wait.
		if m.exitSeq.current(msg.seq) && m.quitAfter {
			m.quitting = true

			return m, tea.Quit
		}

		return m, nil

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

// goTo switches the destination and forwards the intent. A screen with
// a leave guard can keep the user on it; the help closes either way, so
// the question of the guard is visible.
func (m *model) goTo(dest destination, intent string) tea.Cmd {
	m.help = false

	if dest != m.active {
		if g, ok := m.screens[m.active].(leaveGuard); ok {
			if cmd, ok := g.leave(dest, intent); !ok {
				return cmd
			}
		}
	}

	m.active = dest

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
	case key.Matches(msg, gkeys.Settings):
		return m.goTo(destSettings, "")
	case key.Matches(msg, gkeys.Refresh):
		return tea.Batch(m.st.load(allSections...), m.startTick(),
			toast(ToneNeutral, "Reading the current state from disk. Nothing is rebuilt or fetched."))
	}

	return scr.update(msg)
}

// requestQuit quits at once when idle. While a mutation runs it asks
// first: most work cannot stop safely, and stoppable work needs a stop
// before the exit. An unsaved Settings draft also asks first, because
// the quit discards it. The running work takes precedence: its question
// then also names the draft.
func (m *model) requestQuit() tea.Cmd {
	if m.st.busy() || m.settingsDrafts() != "" {
		m.quitConfirm = true

		return nil
	}

	m.quitting = true

	return tea.Quit
}

// quitDialogKey answers the quit question. A yes with no running work
// quits at once: a latched quitAfter would otherwise exit the console
// after a later, unrelated mutation and hide its result. A yes during
// stoppable work also stops it and starts the bounded exit wait.
//
// A second ctrl+c also means yes, because users press it again to
// insist on the exit. The footer names it, so the advertised quit key
// acts. q does nothing here: a shortcut must not act through a
// confirmation, and a repeated q must not stop costly work.
func (m *model) quitDialogKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter, gkeys.Interrupt), msg.String() == "y":
		m.quitConfirm = false

		if !m.st.busy() {
			m.quitting = true

			return tea.Quit
		}

		m.quitAfter = true

		if m.st.requestStop() {
			note := toast(ToneAttention, "Stopping the running work. Seamark exits when it ends.")

			// A repeated yes keeps the first deadline, so the exit wait
			// never grows past exitWait.
			if !m.exitBy.IsZero() {
				return note
			}

			seq := m.exitSeq.start()
			m.exitBy = time.Now().Add(exitWait)

			return tea.Batch(note, after(exitWait, func(time.Time) tea.Msg { return exitDeadlineMsg{seq: seq} }))
		}

		return toast(ToneAttention, "Seamark exits when "+m.st.mutation+" finishes.")
	case key.Matches(msg, nkeys.Back), msg.String() == "n":
		// A requested stop stays in effect, because the work cannot
		// continue safely after a cancel. esc cancels only the exit.
		m.quitConfirm = false
		m.quitAfter = false
		m.exitBy = time.Time{}
		m.exitSeq.cancel()
	}

	return nil
}

// settingsDrafts names the unsaved drafts of the Settings groups, for
// example "Inference has 1 unsaved change: agent.cli.", or returns ""
// when no group holds a draft.
func (m *model) settingsDrafts() string {
	s, ok := m.screens[destSettings].(*settingsScreen)
	if !ok || s.draftTotal() == 0 {
		return ""
	}

	return s.leaveText()
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

	// The tab row and the tab rule share one abbreviation choice, or the
	// heavy rule would no longer sit under the active label.
	short := m.tabShort(w)
	top := strings.Join([]string{m.header(w), m.tabs(w, short), m.tabRule(w, short), m.notice(w), body}, "\n")

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
// its styled form. The two have the same width. short selects the short
// label of a screen that has one.
func (m *model) tabSegment(i int, short bool) (plain, styled string) {
	t := m.st.theme
	name := m.screens[i].title()

	if st, ok := m.screens[i].(shortTitler); ok && short {
		name = st.shortTitle()
	}

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

// tabShort returns the tabs that show their short label at width w. It
// abbreviates in tabShortOrder until the row fits, and it keeps every
// full label when the row fits already. tabs clips a row that still
// does not fit.
func (m *model) tabShort(w int) [destCount]bool {
	var (
		short      [destCount]bool
		full, abbr [destCount]int
	)

	// Each segment takes one leading space, as tabs writes it.
	total := 0

	for i := range m.screens {
		plain, _ := m.tabSegment(i, false)
		full[i] = 1 + width(plain)
		abbr[i] = full[i]

		if _, ok := m.screens[i].(shortTitler); ok {
			plain, _ = m.tabSegment(i, true)
			abbr[i] = 1 + width(plain)
		}

		total += full[i]
	}

	for _, d := range tabShortOrder {
		if total <= w {
			break
		}

		if abbr[d] < full[d] {
			short[d] = true
			total += abbr[d] - full[d]
		}
	}

	return short
}

// tabs renders the tab row with the labels that short selects, for
// example "  1 Overview 5   2 Integ   3 Lessons 6".
func (m *model) tabs(w int, short [destCount]bool) string {
	var b strings.Builder

	for i := range m.screens {
		_, styled := m.tabSegment(i, short[i])
		b.WriteString(" " + styled)
	}

	return truncate(b.String(), w, "")
}

// tabRule underlines the active tab with the heavy rule glyph, so the
// active destination is visible without color. It takes the same short
// labels as tabs, so the segment widths match.
func (m *model) tabRule(w int, short [destCount]bool) string {
	t := m.st.theme
	g := t.Glyphs

	var b strings.Builder

	used := 0

	for i := range m.screens {
		plain, _ := m.tabSegment(i, short[i])
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

// notice renders the row under the tabs. It shows the first item that
// applies, in this order:
//   - a stoppable run;
//   - the latest message;
//   - the running work;
//   - a screen banner, for example the stale index;
//   - a blank spacer line.
func (m *model) notice(w int) string {
	t := m.st.theme

	if m.st.run != nil {
		return m.runNotice(m.st.run, w)
	}

	var line string

	switch {
	case m.toast.text != "":
		line = m.toastLine()
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

// toastLine renders the current message with its tone glyph.
func (m *model) toastLine() string {
	t := m.st.theme

	return " " + t.ToneStyle(m.toast.tone).Render(t.ToneGlyph(m.toast.tone)) + " " + t.Body.Render(oneLine(m.toast.text))
}

// latestSnapshot returns the newest load time over all sources.
func (m *model) latestSnapshot() time.Time {
	var at time.Time

	for _, t := range []time.Time{
		m.st.doctor.at, m.st.index.at, m.st.integrations.at, m.st.ledger.at, m.st.stats.at,
		m.st.learning.at, m.st.settings.at, m.st.pins.at,
	} {
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
		_, _, local = m.quitCopy(false)
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
	// so their hints would be wrong there. A way to quit stays visible.
	// A capturing screen takes q as text or ignores it. So its footer
	// names ctrl+c, which always reaches the shell. The quit question
	// also ignores q and takes ctrl+c as its yes, see quitDialogKey.
	var global []key.Binding

	switch {
	case m.quitConfirm:
		global = []key.Binding{gkeys.Interrupt}
	case m.help || m.welcome != nil:
		global = []key.Binding{gkeys.Quit}
	case m.screens[m.active].capturing():
		global = []key.Binding{gkeys.Interrupt}
	default:
		global = []key.Binding{gkeys.Refresh, gkeys.Help, gkeys.Quit}
	}

	right := t.hints(global)

	clock := ""
	if at := m.latestSnapshot(); !at.IsZero() {
		clock = "   " + t.Faint.Render(at.Format("15:04:05"))
	}

	// The global hints always stay: "? help" leads to every key, so the
	// local hints give way first. The room keeps one cell at each edge
	// and three cells between the two sides, as between two hints.
	left, kept := m.footerHints(local, w-width(right+clock)-5)

	// The n/a entries must stay in view (user decision of 2026-10-04:
	// "Always list, mark unavailable"). So the clock gives way when the
	// room cuts an n/a entry and the line without the clock keeps them
	// all. Other footers have no n/a entry and always keep the clock.
	if !kept && clock != "" {
		if line, ok := m.footerHints(local, w-width(right)-5); ok {
			left, clock = line, ""
		}
	}

	return spread(" "+left, right+clock+" ", w)
}

// footerHints renders the leading hints that fit in room cells. It
// drops whole hints from the end and marks the cut with an ellipsis, so
// no hint breaks in the middle of a word. It also reports whether the
// line keeps every n/a entry of bindings.
//
// The n/a groups keep their actions only when every hint fits. A cut
// line shows them by their keys ("n/a: w t v"). The unavailable keys
// then take fewer cells, so more keys that work stay in view.
func (m *model) footerHints(bindings []key.Binding, room int) (line string, kept bool) {
	t := m.st.theme

	var shown []key.Binding

	// An unavailable entry is a hint too, so a cut can end inside its
	// n/a group.
	for _, b := range bindings {
		if listed(b) {
			shown = append(shown, b)
		}
	}

	if line := t.hints(shown); width(line) <= room {
		return line, true
	}

	for n := len(shown); n > 0; n-- {
		line := t.shortHints(shown[:n])
		if n < len(shown) {
			line += t.Faint.Render("  " + t.Glyphs.Ellipsis)
		}

		if width(line) <= room {
			return line, !slices.ContainsFunc(shown[n:], isUnavailable)
		}
	}

	kept = !slices.ContainsFunc(shown, isUnavailable)

	// No hint fits: the ellipsis still says that keys exist, and "? help"
	// at the right lists them.
	if len(shown) > 0 && room >= width(t.Glyphs.Ellipsis) {
		return t.Faint.Render(t.Glyphs.Ellipsis), kept
	}

	return "", kept
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
		title, body, acts := m.quitCopy(true)

		rows := make([][2]string, 0, len(acts))
		for _, a := range acts {
			rows = append(rows, [2]string{a.Help().Key, a.Help().Desc})
		}

		msg = t.Title.Render(wrap(title, textW)) + "\n\n" + t.Body.Render(wrap(body, textW)) + "\n\n" + keyRows(rows...)
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

// quitView renders the quit question as a modal of at most 64 cells.
func (m *model) quitView(w, h int) string {
	t := m.st.theme
	bw := min(w-4, 64)
	inner := boxInner(bw)
	title, body, acts := m.quitCopy(false)

	lines := strings.Split(t.Body.Render(wrap(body, inner)), "\n")
	lines = append(lines, "", t.hints(acts))

	return t.box(boxOpts{
		W: bw, H: min(h, len(lines)+2), Focus: true, Fill: true, Lines: lines,
		Title: t.Title.Render(title),
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
		foot = propRange(t, off, min(len(rows), off+window), len(rows))
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

	// keyCell pads a key label to the key column. A label can be wider
	// than the column, so one space always stays before the action.
	keyCell := func(k string) string {
		label := t.keyLabel(k)

		return padRight(label, max(helpKeyW, width(label)+1))
	}

	// entries renders the listed bindings of a group, one per row. The
	// keys that work come first. The unavailable entries follow under
	// one faint label row that explains the word n/a. Each unavailable
	// row also has the word after its key ("w n/a"). NO_COLOR removes the
	// faint style, and the help scrolls past the label row, so only the
	// word on the row itself tells that the key does not act now.
	entries := func(bindings []key.Binding) []string {
		var on, off []string

		for _, b := range bindings {
			if !listed(b) {
				continue
			}

			hp := b.Help()

			if isUnavailable(b) {
				off = append(off, t.Faint.Render(keyCell(hp.Key+" "+unavailableWord)+hp.Desc))

				continue
			}

			on = append(on, t.Key.Render(keyCell(hp.Key))+t.Muted.Render(hp.Desc))
		}

		if len(off) == 0 {
			return on
		}

		return append(append(on, t.Faint.Render(unavailableWord+": not available now")), off...)
	}

	scr := m.screens[m.active]

	screenKeys := scr.keys()
	if hk, ok := scr.(helpKeyer); ok {
		screenKeys = hk.helpKeys()
	}

	// The ASCII words of "↑↓" are wider than the key column. So the ASCII
	// help uses a short form, and every action starts in the same column.
	moveKeys := "↑↓  j k"
	if t.ASCII {
		moveKeys = "up/dn j k"
	}

	groups := []struct {
		title string
		rows  []string
	}{
		{"GLOBAL", entries([]key.Binding{
			withHelp(gkeys.Overview, "1–5", "jump to tab"), withHelp(nkeys.Next, "tab", "switch pane"),
			gkeys.Refresh, withHelp(gkeys.Help, "?", "this help"), gkeys.Quit,
		})},
		{"MOVE", entries([]key.Binding{
			withHelp(nkeys.Down, moveKeys, "move"), withHelp(nkeys.Top, "g  G", "top / bottom"),
			withHelp(nkeys.Enter, "enter", "open"), withHelp(nkeys.Back, "esc", "back / close"),
			withHelp(nkeys.PageDown, "pgup pgdn", "scroll detail"),
		})},
		{strings.ToUpper(scr.title()), entries(screenKeys)},
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
			depth = max(depth, len(g.rows))
		}

		for r := 0; r < depth; r++ {
			line := ""

			for _, g := range groups {
				c := ""
				if r < len(g.rows) {
					c = g.rows[r]
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
			rows = append(rows, g.rows...)
		}
	}

	rows = append(rows, "", t.Heading.Render("PLAIN COMMANDS"))
	rows = append(rows, helpCommandRows(t, inner)...)

	// The settings live in one file. The help names it, because an
	// editor is the route for every key that Settings does not show.
	rows = append(rows, t.command(settingsFile)+"  "+t.Faint.Render("settings file"),
		"", t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner)))
	rows = append(rows, strings.Split(t.Faint.Render(wrap("Every key that writes a file asks first. "+
		"Nothing changes before you confirm with y.", inner)), "\n")...)

	window = max(1, boxRows(bh))

	return rows, min(window, len(rows))
}

// helpCommands are the plain routes of the console actions, in reading
// order. Related routes stand in pairs, because the wide help shows two
// routes on one row. The CLI forms of retarget, prune and trigger
// extraction act differently from the console; docs/tui.md names the
// differences.
var helpCommands = []string{
	"seamark doctor", "seamark status",
	"seamark index", "seamark index --fixes-only",
	"seamark index --reviews", "seamark init --client <claude|codex>",
	"seamark lessons --proposals", "seamark lessons --stats",
	"seamark lessons --distill --dry-run", "seamark lessons --extract-triggers --dry-run",
	"seamark lessons --retarget <ids>", "seamark lessons --prune <ids>",
}

// helpCommandGap is the space between the two route columns.
const helpCommandGap = 3

// helpCommandRows renders the plain routes in inner cells. Two routes
// share a row when both columns fit, so the wide help stays short. At
// the reference size, the whole help of a short screen then fits without
// a scroll. A narrower help shows one route on each row.
func helpCommandRows(t *Theme, inner int) []string {
	longest := 0

	for _, c := range helpCommands {
		longest = max(longest, width(c))
	}

	colW := longest + helpCommandGap

	var rows []string

	if colW+longest > inner {
		for _, c := range helpCommands {
			rows = append(rows, t.command(c))
		}

		return rows
	}

	for i := 0; i < len(helpCommands); i += 2 {
		row := t.command(helpCommands[i])

		if i+1 < len(helpCommands) {
			row = padRight(row, colW) + t.command(helpCommands[i+1])
		}

		rows = append(rows, row)
	}

	return rows
}
