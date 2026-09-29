package tui

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessonstats"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// TestMain fixes the color profile and disables timers, so views are
// plain text and no test waits for a real tick.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	lipgloss.SetHasDarkBackground(true)

	after = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }

	os.Exit(m.Run())
}

// fakeServices is a scripted Services for screen tests. Read methods
// return the fields; mutation methods call the function fields and
// record the call, so a test can prove that browsing never mutates.
type fakeServices struct {
	mu sync.Mutex

	root, dbPath, version string

	fresh    bool
	freshErr error

	doctor       *doctor.Report
	index        IndexView
	integrations []integration.Inspection
	clients      []integration.Client
	ledger       *proposals.Ledger
	ledgerErr    error
	stats        lessonstats.Snapshot
	agentsOnPath []string

	planSetup       func([]integration.ClientSetup) (*SetupPreview, error)
	applySetup      func(*SetupPreview) (integration.SetupResult, error)
	buildIndex      func(bool, func(IndexEvent)) (*index.Summary, error)
	previewDecision func(proposals.Action, []int64) (*proposals.Preview, error)
	commitDecision  func(*proposals.Preview) (proposals.Result, error)

	// calls records every plan, preview and mutation call by name.
	calls []string
}

func newFake() *fakeServices {
	return &fakeServices{
		root:    "/work/repo",
		dbPath:  "/work/repo/.seamark/index.db",
		version: "test",
		doctor:  &doctor.Report{},
		clients: integration.Builtin().Clients(),
	}
}

func (f *fakeServices) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, name)
}

// called returns the recorded calls.
func (f *fakeServices) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

func (f *fakeServices) Root() string                           { return f.root }
func (f *fakeServices) DBPath() string                         { return f.dbPath }
func (f *fakeServices) Version() string                        { return f.version }
func (f *fakeServices) Fresh() (bool, error)                   { return f.fresh, f.freshErr }
func (f *fakeServices) Doctor() *doctor.Report                 { return f.doctor }
func (f *fakeServices) Index() IndexView                       { return f.index }
func (f *fakeServices) Integrations() []integration.Inspection { return f.integrations }
func (f *fakeServices) Clients() []integration.Client          { return f.clients }
func (f *fakeServices) Ledger() (*proposals.Ledger, error)     { return f.ledger, f.ledgerErr }
func (f *fakeServices) Stats() lessonstats.Snapshot            { return f.stats }
func (f *fakeServices) AgentsOnPath() []string                 { return f.agentsOnPath }

func (f *fakeServices) PlanSetup(setups []integration.ClientSetup) (*SetupPreview, error) {
	f.record("PlanSetup")

	if f.planSetup == nil {
		return &SetupPreview{Setups: setups, Plan: &integration.SetupPlan{}}, nil
	}

	return f.planSetup(setups)
}

func (f *fakeServices) ApplySetup(p *SetupPreview) (integration.SetupResult, error) {
	f.record("ApplySetup")

	if f.applySetup == nil {
		return integration.SetupResult{}, nil
	}

	return f.applySetup(p)
}

func (f *fakeServices) BuildIndex(force bool, progress func(IndexEvent)) (*index.Summary, error) {
	f.record("BuildIndex")

	if f.buildIndex == nil {
		return &index.Summary{}, nil
	}

	return f.buildIndex(force, progress)
}

func (f *fakeServices) PreviewDecision(a proposals.Action, ids []int64) (*proposals.Preview, error) {
	f.record("PreviewDecision:" + a.String())

	if f.previewDecision == nil {
		return &proposals.Preview{Action: a}, nil
	}

	return f.previewDecision(a, ids)
}

func (f *fakeServices) CommitDecision(p *proposals.Preview) (proposals.Result, error) {
	f.record("CommitDecision:" + p.Action.String())

	if f.commitDecision == nil {
		return proposals.Result{Action: p.Action}, nil
	}

	return f.commitDecision(p)
}

// harness drives the root model synchronously: it runs every command
// and feeds the resulting messages back, like the Bubble Tea loop.
type harness struct {
	t    *testing.T
	m    *model
	quit bool
}

// newHarness starts a console over svc at w by h cells.
func newHarness(t *testing.T, svc Services, w, h int) *harness {
	t.Helper()

	hs := &harness{t: t, m: newModel(svc, newTheme(false))}
	hs.run(hs.m.Init())
	hs.send(tea.WindowSizeMsg{Width: w, Height: h})

	return hs
}

// send delivers one message and runs the commands it returns.
func (h *harness) send(msg tea.Msg) {
	h.t.Helper()

	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

// run executes a command tree until it settles. Commands must not use
// tea.Sequence; the harness cannot see inside it.
func (h *harness) run(cmd tea.Cmd) {
	h.t.Helper()

	queue := []tea.Cmd{cmd}

	for steps := 0; len(queue) > 0; steps++ {
		require.Less(h.t, steps, 5000, "command loop does not settle")

		c := queue[0]
		queue = queue[1:]

		if c == nil {
			continue
		}

		switch msg := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case tea.QuitMsg:
			h.quit = true
		default:
			_, next := h.m.Update(msg)
			queue = append(queue, next)
		}
	}
}

// press sends key presses by name: "enter", "esc", "tab", "shift+tab",
// "up", "down", "left", "right", "space", "pgdown", "pgup", "ctrl+c",
// or literal runes such as "j" or "?".
func (h *harness) press(keys ...string) {
	h.t.Helper()

	for _, k := range keys {
		h.send(keyMsg(k))
	}
}

// typeText sends each rune of s as a key press.
func (h *harness) typeText(s string) {
	h.t.Helper()

	for _, r := range s {
		h.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// view returns the current frame without escape sequences.
func (h *harness) view() string {
	return ansi.Strip(h.m.View())
}

// resize sends a new terminal size.
func (h *harness) resize(w, hgt int) {
	h.t.Helper()
	h.send(tea.WindowSizeMsg{Width: w, Height: hgt})
}

func keyMsg(k string) tea.KeyMsg {
	named := map[string]tea.KeyType{
		"enter": tea.KeyEnter, "esc": tea.KeyEsc, "tab": tea.KeyTab, "shift+tab": tea.KeyShiftTab,
		"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft, "right": tea.KeyRight,
		"pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp, "home": tea.KeyHome, "end": tea.KeyEnd,
		"ctrl+c": tea.KeyCtrlC, "backspace": tea.KeyBackspace,
	}

	if t, ok := named[k]; ok {
		return tea.KeyMsg{Type: t}
	}

	if k == "space" {
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}

	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
}

// assertFrame checks that every line of a frame fits the terminal, so
// no layout overflows or wraps.
func assertFrame(t *testing.T, frame string, w, h int) {
	t.Helper()

	lines := strings.Split(frame, "\n")
	require.LessOrEqual(t, len(lines), h, "frame is taller than the terminal")

	for i, l := range lines {
		require.LessOrEqual(t, lipgloss.Width(l), w, "line %d is wider than the terminal: %q", i, l)
	}
}

func TestShellRendersChromeAndNavigates(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 32)

	frame := h.view()
	assertFrame(t, frame, 120, 32)
	require.Contains(t, frame, "▶┊◀ seamark")
	require.Contains(t, frame, "1 Overview")
	require.Contains(t, frame, "2 Integrations")
	require.Contains(t, frame, "3 Lessons")
	require.Contains(t, frame, "4 Doctor")

	h.press("2")
	require.Equal(t, destIntegrations, h.m.active)

	h.press("3")
	require.Equal(t, destLessons, h.m.active)

	h.press("4")
	require.Equal(t, destDoctor, h.m.active)

	h.press("?")
	require.True(t, h.m.help)
	require.Contains(t, h.view(), "? or esc to close")

	h.press("esc")
	require.False(t, h.m.help)
	require.NotContains(t, h.view(), "? or esc to close")

	h.press("q")
	require.True(t, h.quit)
	require.Empty(t, f.called(), "browsing must not plan, preview, or mutate")
}

func TestShellAsksForSpaceWhenTooSmall(t *testing.T) {
	h := newHarness(t, newFake(), 40, 10)

	frame := h.view()
	require.Contains(t, frame, "More space needed")
	require.Contains(t, frame, "q")

	h.resize(120, 32)
	require.Contains(t, h.view(), "1 Overview")
}

func TestShellOffersFirstRunOnlyForFreshWorkspace(t *testing.T) {
	f := newFake()
	f.fresh = true

	h := newHarness(t, f, 120, 32)
	require.Contains(t, h.view(), "Set up seamark in this repository")

	h.press("v")
	require.NotContains(t, h.view(), "Set up seamark in this repository")
	require.Equal(t, destOverview, h.m.active)
	require.Empty(t, f.called(), "the read-only view writes no marker and plans nothing")

	f2 := newFake()
	h2 := newHarness(t, f2, 120, 32)
	require.NotContains(t, h2.view(), "Set up seamark in this repository")
}

func TestShellDefersQuitWhileMutationRuns(t *testing.T) {
	h := newHarness(t, newFake(), 120, 32)

	require.True(t, h.m.st.begin("Building the index"))
	h.press("q")
	require.False(t, h.quit)
	require.Contains(t, h.view(), "Exit after the running operation?")

	h.press("enter")
	require.False(t, h.quit)

	h.send(mutationDoneMsg{})
	require.True(t, h.quit)
}
