package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/settings"
)

// settingsFile is the workspace configuration file, relative to the
// workspace root. The agent, index, review and pin-write readers share
// this one document.
const settingsFile = settings.File

// settingsFields lists the fields of the Settings list in display order.
// It is the order of the settings package, so both views agree.
var settingsFields = []settings.Field{
	settings.AgentCLI, settings.AgentArgv, settings.DistillWrite,
	settings.ReviewWindow, settings.IndexGenerated, settings.IndexExclude,
}

// settingsGroups lists the save units in display order. One save
// writes the draft of one group, as the settings package requires.
var settingsGroups = []settings.Group{
	settings.GroupInference, settings.GroupPinWrite, settings.GroupEvidence, settings.GroupIndexing,
}

// settingsIntents maps the group names of an open intent to the groups.
// The section names also work, because a caller can know a YAML section
// but not the name of its group.
var settingsIntents = map[string]settings.Group{
	"inference": settings.GroupInference, "agent": settings.GroupInference,
	"pinwrite": settings.GroupPinWrite, "distill": settings.GroupPinWrite,
	"evidence": settings.GroupEvidence, "reviews": settings.GroupEvidence,
	"indexing": settings.GroupIndexing, "index": settings.GroupIndexing,
}

// settingsGroupTitle names a group in prose and in the list headings.
func settingsGroupTitle(g settings.Group) string {
	switch g {
	case settings.GroupInference:
		return "Inference"
	case settings.GroupPinWrite:
		return "Pin writing"
	case settings.GroupEvidence:
		return "Evidence"
	default:
		return "Indexing"
	}
}

// settingsKeys are the action keys of the Settings destination. The
// values stay enabled, because key.Matches ignores a disabled binding;
// keys() returns disabled copies for the footer.
var settingsKeys = struct {
	Default    key.Binding
	Save       key.Binding
	Discard    key.Binding
	Add        key.Binding
	Remove     key.Binding
	MoveUp     key.Binding
	MoveDown   key.Binding
	Confirm    key.Binding
	Keep       key.Binding
	DropDrafts key.Binding
}{
	Default:    key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "default")),
	Save:       key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "review save")),
	Discard:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "discard draft")),
	Add:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
	Remove:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "remove")),
	MoveUp:     key.NewBinding(key.WithKeys("K"), key.WithHelp("K/J", "reorder")),
	MoveDown:   key.NewBinding(key.WithKeys("J"), key.WithHelp("J", "move down")),
	Confirm:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "save")),
	Keep:       key.NewBinding(key.WithKeys("k", "enter"), key.WithHelp("k", "keep draft")),
	DropDrafts: key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "discard")),
}

// settingsScreen is the Settings destination: the focused settings of
// the learning workflow and the index. The list shows every field with
// its value and its source. The editor changes a draft of one group.
// Only y in the diff preview writes the file.
type settingsScreen struct {
	st *state

	// clients is the registry, read once. The preset commands of the
	// editor come from it; the registry is immutable.
	clients []integration.Client

	// cursor is the index of the selected field in settingsFields.
	cursor int
	// editing is true while the editor has focus: the right box in the
	// wide layout, the whole area in the compact layout.
	editing bool
	// ed is the state of the open editor.
	ed settingsEditor
	// detail is the scroll position of the editor box. follow asks the
	// next render to scroll the editor cursor into view.
	detail scrollState
	follow bool
	// table keeps the list cursor visible when the list does not fit.
	table listState

	// drafts holds the unsaved edits by field. A draft lives in this
	// screen only: nothing saves it on blur, on a tab change or at exit.
	drafts map[settings.Field]settings.Edit
	// excludeErrs holds the plan errors of exclude patterns, by pattern.
	// The list editor shows each one under its pattern.
	excludeErrs map[string]string

	// flow is the preview and save of one group.
	flow settingsFlow
	// prompt is the open question about a draft, or nil.
	prompt *settingsPrompt
	// passLeave lets the next departure through the leave guard, after
	// the user answered the draft question.
	passLeave bool

	// saved is the last successful save of this session, or nil. The
	// list keeps its activation note after the toast expires.
	saved *settingsSaveNote
}

// newSettings builds the Settings destination without drafts. It reads
// the client registry once, like the Integrations destination.
func newSettings(st *state) *settingsScreen {
	return &settingsScreen{
		st:      st,
		clients: st.svc.Clients(),
		drafts:  map[settings.Field]settings.Edit{},
	}
}

// title is the destination name in the tab bar and in the help.
func (s *settingsScreen) title() string { return "Settings" }

// shortTitle is the tab label on a narrow terminal.
func (s *settingsScreen) shortTitle() string { return "Set" }

// open accepts "group:<name>" with the names of settingsIntents. It
// selects the first field of the group and closes the editor. Any other
// intent opens the destination as it was, so a return keeps the drafts,
// the cursor, and the result of a save that ended elsewhere.
func (s *settingsScreen) open(intent string) tea.Cmd {
	verb, name, _ := strings.Cut(intent, ":")
	if verb != "group" {
		return nil
	}

	g, ok := settingsIntents[name]
	if !ok {
		return nil
	}

	s.moveTo(g.Fields()[0])
	s.closeEditor()

	return nil
}

// moveTo selects a field in the list.
func (s *settingsScreen) moveTo(f settings.Field) {
	for i, sf := range settingsFields {
		if sf == f {
			s.cursor = i
		}
	}

	s.detail = scrollState{}
}

// field returns the selected field.
func (s *settingsScreen) field() settings.Field {
	return settingsFields[max(0, min(s.cursor, len(settingsFields)-1))]
}

// capturing is true while text input, a modal or a question has the
// keys. The radio editor does not capture, so the number keys still
// switch the destination through the leave guard.
func (s *settingsScreen) capturing() bool {
	return s.prompt != nil || s.flow.stage != settingsIdle || s.ed.typing
}

// update routes the service results, the settings reloads and the keys.
func (s *settingsScreen) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case settingsPlannedMsg:
		return s.planned(msg)
	case settingsSaveDoneMsg:
		return s.saveDone(msg)
	case loadedMsg:
		if msg.kind == secSettings {
			s.normalize()
		}
	case tea.KeyMsg:
		return s.key(msg)
	}

	return nil
}

// key applies a key press to the innermost open layer: the question,
// the preview, the text input, the editor, then the list. Without a
// snapshot only the question and the preview act, because every other
// layer reads the snapshot.
func (s *settingsScreen) key(msg tea.KeyMsg) tea.Cmd {
	switch {
	case s.prompt != nil:
		return s.promptKey(msg)
	case s.flow.stage != settingsIdle:
		return s.flowKey(msg)
	case s.snap() == nil:
		return nil
	case s.ed.typing:
		return s.typingKey(msg)
	case s.editing:
		return s.editorKey(msg)
	default:
		return s.listKey(msg)
	}
}

// listKey handles the keys of the field list.
func (s *settingsScreen) listKey(msg tea.KeyMsg) tea.Cmd {
	n := len(settingsFields)
	before := s.cursor

	switch {
	case key.Matches(msg, nkeys.Up):
		s.cursor = max(0, s.cursor-1)
	case key.Matches(msg, nkeys.Down):
		s.cursor = min(n-1, s.cursor+1)
	case key.Matches(msg, nkeys.Top):
		s.cursor = 0
	case key.Matches(msg, nkeys.Bottom):
		s.cursor = n - 1
	case key.Matches(msg, nkeys.PageUp, nkeys.PageDown):
		// The list has six fields, so the page keys scroll the detail.
		s.detail.key(msg)
	case key.Matches(msg, nkeys.Enter, nkeys.Next):
		return s.openEditor()
	case key.Matches(msg, settingsKeys.Default):
		return s.resetField(s.field())
	case key.Matches(msg, settingsKeys.Save):
		return s.review(s.field().Group())
	case key.Matches(msg, settingsKeys.Discard):
		return s.askDiscard(s.field().Group())
	}

	if s.cursor != before {
		s.detail = scrollState{}
	}

	return nil
}

// openEditor gives the editor the keys and puts its cursor on the
// current choice.
func (s *settingsScreen) openEditor() tea.Cmd {
	if s.snap() == nil {
		return nil
	}

	s.editing = true
	s.ed = settingsEditor{}
	s.ed.choice = s.currentChoice(s.field())
	s.follow = true

	return nil
}

// closeEditor returns the keys to the list. An unfinished text input
// goes away; the draft keeps only the values that the user confirmed.
func (s *settingsScreen) closeEditor() {
	s.editing = false
	s.ed = settingsEditor{}
}

// keys returns the bindings of the innermost open layer for the footer.
func (s *settingsScreen) keys() []key.Binding {
	switch {
	case s.prompt != nil:
		return s.promptKeys()
	case s.flow.stage != settingsIdle:
		return s.flowActions()
	case s.ed.typing:
		return []key.Binding{withHelp(nkeys.Enter, "enter", "keep"), withHelp(nkeys.Back, "esc", "cancel")}
	case s.editing:
		return s.editorKeys()
	}

	if s.snap() == nil {
		return nil
	}

	g := s.field().Group()

	// d on a group that cannot be edited only repeats the refusal, so it
	// leaves the footer like s and x.
	def := settingsKeys.Default
	def.SetEnabled(s.resettable(s.field()) && !s.blocked())

	save := settingsKeys.Save
	save.SetEnabled(s.saveBlocked(g) == "")

	discard := settingsKeys.Discard
	discard.SetEnabled(s.draftCount(g) > 0)

	out := []key.Binding{withHelp(nkeys.Down, "↑↓", "move"), withHelp(nkeys.Enter, "enter", "edit"), def, save, discard}

	if s.st.width >= wideWidth {
		out = append(out, withHelp(nkeys.Next, "tab", "detail"))
	}

	return out
}

// leave is the leave guard of the destination. With a draft it keeps the
// user here and asks: keep the draft, discard it, or stay. A running
// save lets the user go, because its result arrives on every screen.
func (s *settingsScreen) leave(next destination, intent string) (tea.Cmd, bool) {
	if s.passLeave {
		s.passLeave = false

		return nil, true
	}

	if s.flow.stage == settingsSaving {
		return nil, true
	}

	// An open preview binds to this visit. Nothing was written, so it
	// can go.
	s.flow = settingsFlow{seq: s.flow.seq + 1}
	s.ed.typing = false

	if s.draftTotal() == 0 {
		return nil, true
	}

	s.prompt = &settingsPrompt{kind: settingsPromptLeave, next: next, intent: intent}

	return nil, false
}

// modal returns the open question or the open preview as a block for
// the shell to place on the dimmed frame.
func (s *settingsScreen) modal(w, h int) (string, bool) {
	switch {
	case s.prompt != nil:
		return s.promptView(w, h), true
	case s.flow.stage != settingsIdle:
		return s.flowView(w, h), true
	}

	return "", false
}

// view renders the list box beside the editor box in a wide terminal.
// A narrow terminal shows one box at a time. The last row stays blank
// above the footer, as in the other destinations.
func (s *settingsScreen) view(w, h int) string {
	boxH := max(3, h-1)

	if s.snap() == nil {
		return s.unreadBox(w, boxH)
	}

	if w >= wideWidth {
		listW := setupSplit(w, settingsListDesignW)

		return hjoin(1, s.listBox(listW, boxH, !s.editing), s.editorBox(w-listW-1, boxH, s.editing, false))
	}

	if s.editing {
		return s.editorBox(w, boxH, true, true)
	}

	return s.listBox(w, boxH, true)
}

// unreadBox renders the state before the first readable snapshot: the
// read in progress, or its failure with the manual route.
func (s *settingsScreen) unreadBox(w, h int) string {
	t := s.st.theme
	sec := &s.st.settings
	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Settings", true), Right: t.Code.Render(settingsFile)}

	if sec.err != nil || (sec.loaded && sec.value == nil) {
		body := "Seamark could not read the settings."
		if sec.err != nil {
			body = "Seamark could not read the settings: " + errText(sec.err)
		}

		return integBoxed(t, o, ToneFailure, "Settings are not readable", body,
			t.Muted.Render("Press ")+t.Key.Render("r")+t.Muted.Render(" to read them again, or edit ")+
				t.Code.Render(settingsFile)+t.Muted.Render(" by hand."))
	}

	return integBoxed(t, o, ToneNeutral, "Reading settings"+t.Glyphs.Ellipsis,
		"Seamark reads "+settingsFile+" with the shared readers. Nothing is written.", "")
}
