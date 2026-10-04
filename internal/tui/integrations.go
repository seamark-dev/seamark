package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/integration"
)

// integFocus names the pane that receives the movement keys.
type integFocus int

const (
	integFocusList integFocus = iota
	integFocusDetail
)

// integListW is the outer width of the Clients box beside the detail
// box. The 1a design fixes it, so a wider terminal grows only the detail
// box, where the capability table needs the room.
const integListW = 34

// integKeys are the action keys of the Integrations destination.
var integKeys = struct {
	Setup  key.Binding
	Skills key.Binding
}{
	Setup:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "set up")),
	Skills: key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "refresh skills")),
}

// integCapLabels are the human names of the configured capabilities.
// The table uses them because the capability IDs are machine words.
var integCapLabels = map[integration.Capability]string{
	integration.CapabilitySkills:          "Skills",
	integration.CapabilityMCPRegistration: "MCP registration",
	integration.CapabilityToolGrants:      "Tool grants",
	integration.CapabilityEdits:           "Edit lessons hook",
	integration.CapabilityCommands:        "Command gate hook",
	integration.CapabilityResets:          "Context reset hook",
}

// integrations is the Integrations destination: the registered clients
// in the Clients box and the selected client's inspection in the detail
// box. The setup wizard replaces the content while it is open.
type integrations struct {
	st *state

	// clients is the registry, read once. The registry is immutable, so
	// a copy at construction keeps Services calls out of Update and View.
	clients []integration.Client

	list  listState
	focus integFocus
	// detailOpen shows the detail box in the narrow layout.
	detailOpen bool
	detail     integPager

	// pendingClient selects a client when the first inspection arrives,
	// because an intent can arrive before the snapshot.
	pendingClient string

	wizard *setupWizard
}

func newIntegrations(st *state) *integrations {
	return &integrations{st: st, clients: st.svc.Clients()}
}

func (s *integrations) title() string { return "Integrations" }

// shortTitle is the tab label on a narrow terminal.
func (s *integrations) shortTitle() string { return "Integ" }

// capturing is true only while the setup wizard applies a plan, because
// that write cannot stop safely. On the other steps the global keys act,
// and the screen keeps the wizard, so a switch loses no choice.
func (s *integrations) capturing() bool {
	return s.wizard != nil && s.wizard.capturing()
}

// banner shows the wizard title and the step trail in the notice row,
// as in the 1a design. Messages and running work take the row first.
func (s *integrations) banner(w int) (string, bool) {
	if s.wizard == nil {
		return "", false
	}

	return s.wizard.banner(w), true
}

// open accepts "setup", "setup:<id>", "skills:<id>" and "client:<id>".
// Any other intent opens the destination as it was.
func (s *integrations) open(intent string) tea.Cmd {
	verb, id, _ := strings.Cut(intent, ":")

	if s.wizard != nil && s.wizard.running() && verb != "" {
		return toast(ToneAttention, "Setup is applying changes. Wait for the result first.")
	}

	switch verb {
	case "client":
		s.wizard = nil
		s.selectClient(id)

		if !s.wide() {
			s.detailOpen = true
		}

		return nil
	case "setup":
		if id == "" {
			s.wizard = newSetupWizard(s.st, s.clients)

			return nil
		}

		return s.startSetup(id, false)
	case "skills":
		return s.startSetup(id, true)
	}

	return nil
}

// startSetup opens the wizard for one client. refresh=true plans a
// managed skill refresh at once, without the choice step.
func (s *integrations) startSetup(id string, refresh bool) tea.Cmd {
	c, ok := s.client(id)
	if !ok {
		s.wizard = newSetupWizard(s.st, s.clients)

		return toast(ToneAttention, "Unknown client: "+oneLine(id)+". Choose a client to set up.")
	}

	s.selectClient(id)
	s.wizard = newSetupWizard(s.st, s.clients)

	if refresh {
		return s.wizard.startRefresh(c)
	}

	s.wizard.choose(c)

	return nil
}

func (s *integrations) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case setupPlannedMsg:
		if s.wizard != nil {
			s.wizard.planned(msg)
		}

		return nil

	case setupAppliedMsg:
		return s.applied(msg)

	case loadedMsg:
		if msg.kind != secIntegrations {
			return nil
		}

		if s.pendingClient != "" {
			s.selectClient(s.pendingClient)
		}

		if s.wizard != nil {
			s.wizard.inspected(msg.gen)
		}

		return nil

	case tea.KeyMsg:
		if s.wizard != nil {
			cmd := s.wizard.key(msg)

			if s.wizard.closed {
				s.selectClient(s.wizard.client.ID)
				s.wizard = nil
				s.focus = integFocusList
			}

			return cmd
		}

		return s.key(msg)
	}

	return nil
}

// applied frees the mutation slot and reloads the sources the setup
// changed. It loads through the state directly, not through refresh,
// so the result can wait for this exact inspection generation. A setup
// can create the starter config.yaml, so the settings and the evidence
// view reload too. It can also create the starter lessons.yaml, so the
// Pins sub-view reloads and stops reporting a missing pin file.
func (s *integrations) applied(msg setupAppliedMsg) tea.Cmd {
	load := s.st.load(secIntegrations, secDoctor, secSettings, secLearning, secPins)
	gen := s.st.gens[secIntegrations]

	if s.wizard != nil {
		s.wizard.finished(msg, gen)
	}

	return tea.Batch(mutationDone, load)
}

func (s *integrations) key(msg tea.KeyMsg) tea.Cmd {
	insps := s.inspections()
	if len(insps) == 0 {
		return nil
	}

	s.list.clamp(len(insps), 0)
	sel := insps[s.list.cursor]

	switch {
	case key.Matches(msg, integKeys.Setup):
		return s.startSetup(sel.ClientID, false)
	case key.Matches(msg, integKeys.Skills):
		if c, ok := s.client(sel.ClientID); ok && c.Supports(integration.CapabilitySkills) {
			return s.startSetup(sel.ClientID, true)
		}

		return toast(ToneNeutral, oneLine(sel.Name)+" reads no managed skills.")
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		if s.wide() {
			s.focus = 1 - s.focus
		}

		return nil
	}

	if s.detailShown() {
		return s.detailKey(msg, sel)
	}

	switch {
	case key.Matches(msg, nkeys.Up):
		s.list.move(-1, len(insps))
	case key.Matches(msg, nkeys.Down):
		s.list.move(1, len(insps))
	case key.Matches(msg, nkeys.Top):
		s.list.cursor = 0
	case key.Matches(msg, nkeys.Bottom):
		s.list.cursor = len(insps) - 1
	case key.Matches(msg, nkeys.PageUp, nkeys.PageDown):
		s.detail.scroll(msg)
	case key.Matches(msg, nkeys.Enter):
		if s.wide() {
			return s.startSetup(sel.ClientID, false)
		}

		s.detailOpen = true
	}

	return nil
}

// detailKey handles the keys while the detail box has focus.
func (s *integrations) detailKey(msg tea.KeyMsg, sel integration.Inspection) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Back):
		s.detailOpen = false
		s.focus = integFocusList
	case key.Matches(msg, nkeys.Enter):
		return s.startSetup(sel.ClientID, false)
	default:
		s.detail.scroll(msg)
	}

	return nil
}

// detailShown reports that the movement keys scroll the detail box.
func (s *integrations) detailShown() bool {
	if s.wide() {
		return s.focus == integFocusDetail
	}

	return s.detailOpen
}

// wide reports the side-by-side layout. Screens get the full terminal
// width, so the threshold applies to the terminal width directly.
func (s *integrations) wide() bool {
	return s.st.width >= wideWidth
}

func (s *integrations) keys() []key.Binding {
	if s.wizard != nil {
		return s.wizard.keys()
	}

	insps := s.inspections()
	if len(insps) == 0 {
		return nil
	}

	skillsKey := integKeys.Skills
	if c, ok := s.client(insps[min(s.list.cursor, len(insps)-1)].ClientID); !ok || !c.Supports(integration.CapabilitySkills) {
		skillsKey.SetEnabled(false)
	}

	switch {
	case s.wide() && s.focus == integFocusDetail:
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "scroll"), withHelp(nkeys.Next, "tab", "clients"),
			withHelp(nkeys.Enter, "enter", "set up"), skillsKey}
	case s.wide():
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "move"), withHelp(nkeys.Next, "tab", "details"),
			withHelp(nkeys.Enter, "enter", "set up"), skillsKey}
	case s.detailOpen:
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "scroll"), withHelp(nkeys.Back, "esc", "clients"),
			integKeys.Setup, skillsKey}
	default:
		return []key.Binding{withHelp(nkeys.Down, "↑↓", "move"), withHelp(nkeys.Enter, "enter", "details"),
			integKeys.Setup, skillsKey}
	}
}

// inspections returns the current snapshot, or nil before the first.
func (s *integrations) inspections() []integration.Inspection {
	if !s.st.integrations.loaded {
		return nil
	}

	return s.st.integrations.value
}

// client returns the registered descriptor of a client ID.
func (s *integrations) client(id string) (integration.Client, bool) {
	for _, c := range s.clients {
		if c.ID == id {
			return c, true
		}
	}

	return integration.Client{}, false
}

// selectClient moves the cursor to a client. Before the first snapshot
// it remembers the ID and applies it when the snapshot arrives.
func (s *integrations) selectClient(id string) {
	insps := s.inspections()
	if insps == nil {
		s.pendingClient = id

		return
	}

	s.pendingClient = ""

	for i, insp := range insps {
		if insp.ClientID == id {
			s.list.cursor = i

			return
		}
	}
}

// view renders the Clients box and the detail box side by side in a
// wide terminal, and one box at a time in a narrow one. The last row
// stays blank above the footer, as in the 1a design.
func (s *integrations) view(w, h int) string {
	if s.wizard != nil {
		return s.wizard.view(w, h)
	}

	t := s.st.theme
	sec := &s.st.integrations
	boxH := max(3, h-1)

	if !sec.loaded {
		o := boxOpts{W: w, H: boxH, Focus: true, Title: integPaneTitle(t, "Clients", true)}

		if sec.err != nil {
			return integBoxed(t, o, ToneFailure, "Could not inspect the integrations", errText(sec.err),
				t.Muted.Render("Press ")+t.Key.Render("r")+t.Muted.Render(" to read the files again."))
		}

		return integBoxed(t, o, ToneNeutral, "Reading integrations"+t.Glyphs.Ellipsis,
			"Seamark reads the native configuration files of every registered client. Nothing is written.", "")
	}

	insps := sec.value
	if len(insps) == 0 {
		return integBoxed(t, boxOpts{W: w, H: boxH, Focus: true, Title: integPaneTitle(t, "Clients", true)},
			ToneNeutral, "No clients registered",
			"This build registers no coding agent clients, so there is nothing to set up.", "")
	}

	s.list.clamp(len(insps), 0)
	sel := insps[s.list.cursor]

	if w >= wideWidth {
		left := s.listBox(insps, integListW, boxH, s.focus == integFocusList)
		right := s.detailBox(sel, w-integListW-1, boxH, s.focus == integFocusDetail)

		return hjoin(1, left, right)
	}

	if s.detailOpen {
		return s.detailBox(sel, w, boxH, true)
	}

	return s.listBox(insps, w, boxH, true)
}

// listBox renders the Clients box: one item per client with its
// configuration count and a short summary.
func (s *integrations) listBox(insps []integration.Inspection, w, h int, focused bool) string {
	t := s.st.theme
	items := make([]integItem, 0, len(insps))

	for _, insp := range insps {
		items = append(items, integClientItem(t, insp))
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused,
		Title: integPaneTitle(t, "Clients", focused), Right: t.Muted.Render(itoa(len(insps))),
		Lines: integRenderClients(t, items, &s.list, boxInner(w), boxRows(h), focused),
	})
}

// detailBox renders the selected client's inspection in a scrolling
// box, with the scroll position in the bottom border.
func (s *integrations) detailBox(insp integration.Inspection, w, h int, focused bool) string {
	t := s.st.theme
	inner, rows := boxInner(w), boxRows(h)

	content := s.detail.render(insp.ClientID, strings.Join(s.detailLines(insp, inner), "\n"), inner, rows)

	right := ""
	if s.st.integrations.stale() {
		right = t.Attention.Render(t.Glyphs.Warn + " stale")
	}

	return t.box(boxOpts{
		W: w, H: h, Focus: focused,
		Title: integPaneTitle(t, integName(s.clientOf(insp), insp), focused), Right: right,
		Foot: s.detail.foot(t), Lines: strings.Split(content, "\n"),
	})
}

// clientOf returns the descriptor of an inspected client, or the zero
// value when the registry does not know it.
func (s *integrations) clientOf(insp integration.Inspection) integration.Client {
	c, _ := s.client(insp.ClientID)

	return c
}

// integItem is one client in a client list: its name, a styled badge
// at the right of the name, and the summary parts under it.
type integItem struct {
	name    string
	badge   string
	summary []string
}

// lines renders the item in w cells: the name with the badge at the
// right, then the summary packed into as many lines as it needs, so a
// client with many states is never cut. current marks the cursor item,
// whose name is bold.
func (it integItem) lines(t *Theme, w int, current bool) []string {
	style := t.Body
	if current {
		style = t.Title
	}

	badge := it.badge + " "
	name := style.Render(truncate(it.name, max(1, w-width(badge)-1), t.Glyphs.Ellipsis))
	out := []string{spread(name, badge, w)}

	sep := " " + t.Glyphs.Sep + " "
	line := ""

	for _, part := range it.summary {
		switch {
		case line == "":
			line = part
		case width(line+sep+part) <= w:
			line += sep + part
		default:
			out = append(out, t.Muted.Render(truncate(line, w, t.Glyphs.Ellipsis)))
			line = part
		}
	}

	if line != "" {
		out = append(out, t.Muted.Render(truncate(line, w, t.Glyphs.Ellipsis)))
	}

	return out
}

// integRenderClients renders list items of several lines each, with a
// blank line between items. The cursor item always stays visible and
// gets the selection background when the list has focus. A list that
// does not fit gives its last line to a position note, so a hidden
// client is never a surprise.
func integRenderClients(t *Theme, items []integItem, l *listState, w, h int, focused bool) []string {
	l.clamp(len(items), 0)

	rendered := make([][]string, len(items))
	for i, it := range items {
		rendered[i] = it.lines(t, w-2, i == l.cursor)
	}

	height := func(from, to int) int {
		n := 0
		for i := from; i <= to; i++ {
			n += len(rendered[i]) + 1
		}

		return n - 1
	}

	room := h
	if len(items) > 0 && h >= 2 && height(0, len(items)-1) > h {
		room = h - 1
	}

	l.offset = min(l.offset, l.cursor)
	for l.offset < l.cursor && height(l.offset, l.cursor) > room {
		l.offset++
	}

	var out []string

	shown := 0

	for i := l.offset; i < len(items); i++ {
		block := rendered[i]

		// The first item always renders, so the cursor item never
		// disappears; the box clips a block taller than the window.
		if i > l.offset {
			if len(out)+1+len(block) > room {
				break
			}

			out = append(out, "")
		}

		for j, line := range block {
			prefix := "  "
			if i == l.cursor && j == 0 {
				prefix = t.Key.Render(t.Glyphs.Cursor) + " "
			}

			line = prefix + line

			if i == l.cursor && focused {
				line = t.highlight(line, w, bgSel)
			}

			out = append(out, line)
		}

		shown++
	}

	if room < h {
		out = append(out, t.scrollNote(l.offset, shown, len(items), w))
	}

	return out
}

// integName returns the display name of a client, from the descriptor
// when known, else from the inspection.
func integName(c integration.Client, insp integration.Inspection) string {
	if c.Name != "" {
		return c.Name
	}

	return insp.Name
}

// integPager scrolls one long text box over bubbles/viewport. It keeps
// the offset while the content key stays the same, so a refresh keeps
// the position and a new selection starts at the top.
type integPager struct {
	vp  viewport.Model
	key string
}

// render sizes the viewport, sets the content and returns the visible
// window.
func (p *integPager) render(id, content string, w, h int) string {
	p.vp.Width, p.vp.Height = max(1, w), max(1, h)
	p.vp.SetContent(content)

	if id != p.key {
		p.key = id
		p.vp.GotoTop()
	}

	return p.vp.View()
}

// scroll applies a movement key. It reports whether the key moved.
func (p *integPager) scroll(msg tea.KeyMsg) bool {
	switch {
	case key.Matches(msg, nkeys.Up):
		p.vp.ScrollUp(1)
	case key.Matches(msg, nkeys.Down):
		p.vp.ScrollDown(1)
	case key.Matches(msg, nkeys.PageUp):
		p.vp.PageUp()
	case key.Matches(msg, nkeys.PageDown):
		p.vp.PageDown()
	case key.Matches(msg, nkeys.Top):
		p.vp.GotoTop()
	case key.Matches(msg, nkeys.Bottom):
		p.vp.GotoBottom()
	default:
		return false
	}

	return true
}

// position returns "a–b of n" while the content scrolls, else "".
func (p *integPager) position(t *Theme) string {
	if p.vp.TotalLineCount() <= p.vp.Height {
		return ""
	}

	return p.span(t)
}

// span returns "a–b of n" for the visible lines, or "" for no content.
// The diff box shows it even when every line fits, as in the design.
func (p *integPager) span(t *Theme) string {
	total := p.vp.TotalLineCount()
	if total == 0 {
		return ""
	}

	dash := "–"
	if t.ASCII {
		dash = "-"
	}

	first := p.vp.YOffset + 1
	last := min(total, p.vp.YOffset+p.vp.Height)

	return itoa(first) + dash + itoa(last) + " of " + itoa(total)
}

// foot renders the scroll position for a box border, or "" when all
// content fits.
func (p *integPager) foot(t *Theme) string {
	pos := p.position(t)
	if pos == "" {
		return ""
	}

	return t.Faint.Render(pos)
}
