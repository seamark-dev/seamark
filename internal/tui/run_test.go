package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/settings"
)

// runFake is a scripted run record. It records every message that
// reaches it, so a test can prove the routing of the Run sub-view.
type runFake struct {
	st *state

	name     string
	running  bool
	drafting bool
	capture  bool
	overlay  string
	bindings []key.Binding

	// ids and at are the generated proposals and the run end.
	ids []int64
	at  time.Time

	got []tea.Msg
}

func (r *runFake) active() bool        { return r.running }
func (r *runFake) draft() bool         { return r.drafting }
func (r *runFake) title() string       { return r.name }
func (r *runFake) keys() []key.Binding { return r.bindings }
func (r *runFake) capturing() bool     { return r.capture }

func (r *runFake) update(msg tea.Msg) tea.Cmd {
	r.got = append(r.got, msg)

	return nil
}

func (r *runFake) modal(w, h int) (string, bool) {
	if r.overlay == "" {
		return "", false
	}

	return r.st.theme.modal(modalOpts{Title: r.overlay, Body: []string{"The fake overlay."}}, nil, w, h), true
}

func (r *runFake) view(w, h int) string {
	o := boxOpts{W: w, H: max(4, h-3), Focus: true, Title: r.st.theme.Title.Render("Fake run")}

	return strings.Join([]string{"", "", integBoxed(r.st.theme, o, ToneNeutral, r.name, "Fake record body.", "")}, "\n")
}

func (r *runFake) generated() (ids []int64, label string, at time.Time) {
	return r.ids, r.name, r.at
}

// keysOf returns the messages of r that are key presses, as strings.
func (r *runFake) keysOf() []string {
	var out []string

	for _, m := range r.got {
		if km, ok := m.(tea.KeyMsg); ok {
			out = append(out, km.String())
		}
	}

	return out
}

// runRec returns a finished record named name.
func runRec(h *harness, name string) *runFake {
	return &runFake{st: h.m.st, name: name}
}

// runViewOf returns the Run sub-view of the console.
func runViewOf(h *harness) *runView {
	return h.m.screens[destLessons].(*lessons).runv
}

func TestRunViewEmptyStateNamesWhereARunStarts(t *testing.T) {
	f := newFake()
	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	// A kind without a record shows the tab and the honest empty state.
	h.m.st.lastRunKind = runEvidence
	h.press("u")
	require.Equal(t, lesRun, les.sub)

	frame := h.view()
	assert.Contains(t, frame, "╭─ Run ")
	assert.Contains(t, frame, "No run in this session")
	assert.Contains(t, lesBoxFlat(frame), "Refresh the evidence or prepare a distillation from Evidence, or "+
		"extract trigger information with t in Pins or Proposals.")
	assert.Contains(t, frame, "e open Evidence   m open Pins")
	assert.Contains(t, footerLine(frame), "p proposals   m pins   s stats   e evidence")
	assert.False(t, les.capturing())

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
		assert.Contains(t, h.view(), "No run in this session")
	}

	h.press("e")
	assert.Equal(t, lesEvidence, les.sub, "the named key works")

	h.press("u", "m")
	assert.Equal(t, lesPins, les.sub, "the second named key works")
	assert.Empty(t, f.called(), "the empty state reads and writes nothing")
}

func TestRunViewOpensARecordFromAnotherDestination(t *testing.T) {
	h := newHarness(t, newFake(), 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	rec := runRec(h, "Evidence refresh")

	h.run(openRun(rec, true))

	assert.Equal(t, destLessons, h.m.active, "show opens Lessons")
	assert.Equal(t, lesRun, les.sub, "show opens the Run sub-view")
	assert.Contains(t, h.view(), "╭─ Fake run ")
	assert.Contains(t, lesSubTabs(h), "Run   p / m / s / e / u", "a record offers the Run tab")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	// Without show the record waits in the sub-view.
	h2 := newHarness(t, newFake(), 120, 40)
	quiet := runRec(h2, "Quiet refresh")

	h2.send(openRunMsg{rec: quiet, show: false})
	assert.Equal(t, destOverview, h2.m.active)
	assert.Same(t, quiet, runViewOf(h2).cur)

	h2.press("3", "u")
	assert.Contains(t, h2.view(), "Quiet refresh")
}

func TestRunViewRefusesToReplaceAnActiveRecord(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	v := runViewOf(h)

	first := runRec(h, "Distillation · internal/api")
	first.running = true
	h.send(openRunMsg{rec: first, show: true})

	second := runRec(h, "Evidence refresh")
	h.send(openRunMsg{rec: second, show: true})

	assert.Same(t, first, v.cur, "an active record stays")
	assert.Empty(t, v.past)
	assert.Contains(t, h.view(), "A run is in progress: Distillation · internal/api.")

	// After the work ends, the next record takes the view.
	first.running = false
	h.send(openRunMsg{rec: second, show: true})

	assert.Same(t, second, v.cur)
	require.Len(t, v.past, 1)
	assert.Same(t, first, v.past[0], "the finished record stays in the session list")
}

func TestRunViewKeepsFinishedRecordsAndDropsDrafts(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	v := runViewOf(h)

	var recs []*runFake

	for i := range runKeep + 2 {
		rec := runRec(h, "Refresh "+itoa(i))
		recs = append(recs, rec)
		h.send(openRunMsg{rec: rec})
	}

	require.Len(t, v.past, runKeep, "the session list is bounded")
	assert.Same(t, recs[len(recs)-2], v.past[0], "newest first")
	assert.Same(t, recs[1], v.past[runKeep-1], "the oldest records leave first")

	// A draft that another record replaces is not a finished record.
	draft := runRec(h, "Distillation draft")
	draft.drafting = true
	h.send(openRunMsg{rec: draft})
	h.send(openRunMsg{rec: runRec(h, "Refresh after the draft")})

	for _, rec := range v.past {
		assert.NotSame(t, draft, rec)
	}

	// A record from the list becomes current and leaves the list.
	earlier := v.past[1]
	h.send(openRunMsg{rec: earlier})
	assert.Same(t, earlier, v.cur)

	for _, rec := range v.past {
		assert.NotSame(t, earlier, rec, "no record shows twice")
	}
}

func TestRunViewRoutesKeysAndMessagesToTheRecord(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	stop := key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop"))
	rec := runRec(h, "Distillation")
	rec.running = true
	rec.bindings = []key.Binding{stop}
	h.send(openRunMsg{rec: rec, show: true})

	assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(h.view())), "x stop   p proposals   m pins   e evidence"),
		footerLine(h.view()))

	h.press("x")
	assert.Equal(t, []string{"x"}, rec.keysOf(), "the visible record gets its keys")

	// A hidden record gets no keys, but every other message.
	h.press("p")
	require.Equal(t, lesProposals, les.sub)

	h.press("x")
	assert.Equal(t, []string{"x"}, rec.keysOf(), "keys reach the visible sub-view only")

	at := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	h.send(settingsSavedMsg{group: settings.GroupInference, at: at})

	var saved []settingsSavedMsg

	for _, m := range rec.got {
		if s, ok := m.(settingsSavedMsg); ok {
			saved = append(saved, s)
		}
	}

	require.Len(t, saved, 1, "a hidden record still receives the save notice")
	assert.Equal(t, settings.GroupInference, saved[0].group)
	assert.Equal(t, at, saved[0].at)
	assert.Empty(t, f.called())
}

func TestRunViewCapturingRecordOwnsTheKeys(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	rec := runRec(h, "Distillation scope")
	rec.drafting = true
	rec.capture = true
	h.send(openRunMsg{rec: rec, show: true})

	h.press("p", "e", "1", "q")

	assert.Equal(t, []string{"p", "e", "1", "q"}, rec.keysOf(), "a capturing record receives every letter")
	assert.Equal(t, lesRun, les.sub)
	assert.Equal(t, destLessons, h.m.active)
	assert.False(t, h.quit)

	footer := footerLine(h.view())
	assert.Contains(t, footer, "ctrl+c quit")
	assert.NotContains(t, footer, "p proposals", "the switch keys give way while the record captures")
}

func TestRunViewShowsTheOverlayOfTheRecord(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)

	rec := runRec(h, "Distillation")
	rec.running = true
	rec.capture = true
	rec.overlay = "Stop the distillation?"
	h.send(openRunMsg{rec: rec, show: true})

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Contains(t, frame, "Stop the distillation?")
	}

	// A hidden sub-view shows no overlay.
	rec.capture = false
	h.press("p")
	assert.NotContains(t, h.view(), "Stop the distillation?")
}

func TestRunViewDistillIntentKeepsADraft(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	v := runViewOf(h)

	h.send(navigateMsg{dest: destLessons, intent: "distill"})
	require.Equal(t, lesRun, les.sub)
	require.NotNil(t, v.cur)
	assert.IsType(t, &distRecord{}, v.cur)
	assert.True(t, isDraft(v.cur))

	draft := v.cur

	h.press("p")
	h.send(navigateMsg{dest: destLessons, intent: "distill"})
	assert.Same(t, draft, v.cur, "a repeated intent keeps the draft")
	assert.Equal(t, lesRun, les.sub)

	// A finished distillation gives way to a new draft and stays listed.
	done := runRec(h, "Distillation 14:05")
	h.send(openRunMsg{rec: done})
	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	assert.NotSame(t, done, v.cur)
	assert.True(t, isDraft(v.cur))
	require.NotEmpty(t, v.past)
	assert.Same(t, done, v.past[0])

	// An active record stays; the intent only shows it.
	running := runRec(h, "Evidence refresh")
	running.running = true
	h.send(openRunMsg{rec: running})
	h.press("p")
	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	assert.Same(t, running, v.cur)
	assert.Equal(t, lesRun, les.sub)
	assert.Contains(t, h.view(), "A run is in progress: Evidence refresh.")
}

func TestRunViewReviewsTheNewestFinishedDistillation(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	v := runViewOf(h)

	ids, _, at := v.generated()
	assert.Empty(t, ids, "no run, no IDs")
	assert.True(t, at.IsZero())

	end := time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)

	older := runRec(h, "Distillation 13:00")
	older.ids, older.at = []int64{9}, end.Add(-time.Hour)
	h.send(openRunMsg{rec: older})

	done := runRec(h, "Distillation 14:05")
	done.ids, done.at = []int64{2, 3}, end
	h.send(openRunMsg{rec: done})

	h.send(openRunMsg{rec: runRec(h, "Evidence refresh")})
	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	ids, label, at := v.generated()
	assert.Equal(t, []int64{2, 3}, ids, "the draft and the evidence run have no result to review")
	assert.Equal(t, "Distillation 14:05", label)
	assert.Equal(t, end, at)

	h.send(navigateMsg{dest: destLessons, intent: "proposals:run"})
	assert.Equal(t, lesProposals, les.sub)
	require.NotNil(t, les.props.run)
	assert.Equal(t, map[int64]bool{2: true, 3: true}, les.props.run.ids)
}

func TestRunViewStepsThroughEarlierRecordsReadOnly(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)
	v := runViewOf(h)

	end := time.Date(2026, 9, 29, 14, 5, 0, 0, time.UTC)

	older := runRec(h, "Distillation 13:00")
	older.ids, older.at = []int64{2}, end.Add(-time.Hour)
	older.bindings = []key.Binding{
		withHelp(nkeys.Enter, "enter", "review proposals"), withHelp(distKeys.Back, "n", "plan again"),
		withHelp(nkeys.Down, "↑↓", "group"),
	}
	h.send(openRunMsg{rec: older})

	cur := runRec(h, "Distillation 14:05")
	cur.ids, cur.at = []int64{3}, end
	h.send(openRunMsg{rec: cur, show: true})
	cur.running = true

	require.Equal(t, lesRun, les.sub)
	assert.Contains(t, footerLine(h.view()), "[ earlier run", "the footer offers the earlier record")

	h.press("[")
	require.Same(t, older, v.shown())

	frame := h.view()
	assert.Contains(t, frame, "Earlier run 1 of 1 · read-only · ] later")
	assert.Contains(t, frame, "Distillation 13:00")

	footer := footerLine(frame)
	assert.Contains(t, footer, "enter review proposals")
	assert.Contains(t, footer, "↑↓ group")
	assert.Contains(t, footer, "] later run")
	assert.NotContains(t, footer, "n plan again", "an earlier record starts no work")
	assert.NotContains(t, footer, "[ earlier run", "no record is older")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	// Integration check fix: at 60 columns the long enter label left no
	// room, so the footer showed no key. A compact footer uses the short
	// label, as the result of the current record does.
	for _, size := range [][2]int{{80, 24}, {60, 20}} {
		h.resize(size[0], size[1])

		footer := footerLine(h.view())
		assert.Contains(t, footer, "enter review", "size %v: the step action leads", size)
		assert.NotContains(t, footer, "review proposals", "size %v: the short label", size)
	}

	h.resize(120, 40)

	// Only the movement keys reach the earlier record, and the current
	// record keeps every other message.
	h.press("n", "x", "down", "[")
	assert.Equal(t, []string{"down"}, older.keysOf())
	assert.Empty(t, cur.keysOf())

	h.send(settingsSavedMsg{group: settings.GroupInference})
	assert.IsType(t, settingsSavedMsg{}, cur.got[len(cur.got)-1], "the running record still receives its messages")
	assert.Len(t, older.got, 1, "the earlier record receives the movement key only")

	// enter reviews the proposals of the earlier run, and u returns to it.
	h.press("enter")
	require.Equal(t, lesProposals, les.sub)
	require.NotNil(t, les.props.run)
	assert.Equal(t, map[int64]bool{2: true}, les.props.run.ids)

	h.press("u")
	assert.Same(t, older, v.shown(), "u keeps the record on view")

	// The run intent and ] show the current record.
	h.send(navigateMsg{dest: destLessons, intent: "run"})
	assert.Same(t, cur, v.shown())

	h.press("[", "]")
	assert.Same(t, cur, v.shown())
	assert.False(t, v.browsing())

	// A capturing current record receives the brackets as text.
	cur.capture = true
	h.press("[")
	assert.Equal(t, []string{"["}, cur.keysOf())
	assert.False(t, v.browsing())
	assert.Empty(t, f.called())
}

func TestRunViewNewRecordEndsTheBrowse(t *testing.T) {
	h := lesOpen(t, newFake(), 120, 40)
	v := runViewOf(h)

	first := runRec(h, "Refresh 1")
	h.send(openRunMsg{rec: first, show: true})
	h.send(openRunMsg{rec: runRec(h, "Refresh 2")})

	h.press("[")
	require.Same(t, first, v.shown())

	third := runRec(h, "Refresh 3")
	h.send(openRunMsg{rec: third})
	assert.Same(t, third, v.shown(), "a new record shows at once")
	assert.Contains(t, h.view(), "Refresh 3")
}

// The tests below hold review fixes. Each test drives the real model
// through the harness, so it proves the fix where the user sees it.

// The shell guard: any dropped draft that leaves no record moves the
// view off the Run sub-view, so no visible sub-view lacks its tab.
func TestDroppedDraftNeverLeavesAnUnlistedRunView(t *testing.T) {
	f := newFake()
	f.ledger = lesLedger()

	h := lesOpen(t, f, 120, 40)
	les := hookLessons(h)

	d := newDistillDraft(h.m.st, "")
	h.send(openRunMsg{rec: d, show: true})
	require.Equal(t, lesRun, les.sub)

	// Only the close message, without the navigation that a closed
	// scope step sends with it.
	h.send(closeRunMsg{rec: d})
	assert.False(t, les.runShown())
	assert.Equal(t, lesProposals, les.sub)
}
