package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
)

// docReport has one check of every doctor state. The fail check has a
// fix; the warn check has a long detail that the list clips.
func docReport() *doctor.Report {
	return &doctor.Report{
		Checks: []doctor.Check{
			{Name: "binary", State: doctor.StateOK, Detail: "seamark v0.6.0 (darwin/arm64)"},
			{Name: "index", State: doctor.StateInfo, Detail: "no index database yet", Fix: "run `seamark index` to build it"},
			{Name: "skills", State: doctor.StateWarn, Detail: "the managed skill review-change in .claude/skills is " +
				"outdated: the installed copy differs from the copy that this seamark binary ships, so agents " +
				"read old instructions until setup refreshes it"},
			{Name: "policy", State: doctor.StateFail, Detail: ".seamark/policy.yaml does not compile: line 3: unknown key",
				Fix: "fix .seamark/policy.yaml, then run `seamark doctor` again"},
		},
		Warns: 1,
		Fails: 1,
	}
}

// docFlat joins the words of a frame, so a check can match text that
// the pane wraps.
func docFlat(frame string) string {
	var words []string

	for _, l := range strings.Split(frame, "\n") {
		words = append(words, strings.Fields(strings.Trim(l, " │"))...)
	}

	return strings.Join(words, " ")
}

// docRight returns the cells of every frame line from column 65 on: the
// detail pane of the wide layout at 120 columns.
func docRight(frame string) string {
	lines := strings.Split(frame, "\n")

	for i, l := range lines {
		r := []rune(l)
		lines[i] = string(r[min(len(r), 65):])
	}

	return strings.Join(lines, "\n")
}

// docOpen starts a console over a doctor report and opens the Doctor.
func docOpen(t *testing.T, r *doctor.Report, w, h int) (*harness, *fakeServices) {
	t.Helper()

	f := newFake()
	f.doctor = r

	hs := newHarness(t, f, w, h)
	hs.press("4")
	require.Equal(t, destDoctor, hs.m.active)

	return hs, f
}

// docView returns the Doctor destination of a console.
func docView(h *harness) *doctorView {
	return h.m.screens[destDoctor].(*doctorView)
}

func TestDoctorListsEveryCheckWithItsStateWord(t *testing.T) {
	h, f := docOpen(t, docReport(), 120, 40)

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Contains(t, frame, "╭─ Doctor checks ")
	assert.Contains(t, frame, " 4 checks ─╮")

	// Every state reads without color: glyph and word.
	assert.Regexp(t, `› ✓ ok +binary +seamark v0\.6\.0`, frame)
	assert.Regexp(t, `  · info +index +no index database yet`, frame)
	assert.Regexp(t, `  ! warn +skills +the managed skill`, frame)
	assert.Regexp(t, `  ! fail +policy +\.seamark/policy\.yaml does not compile`, frame)
	assert.Contains(t, frame, "✓ 1 ok   · 1 info   ! 1 warn   ! 1 fail")

	// A long detail is clipped in the list with one ellipsis and wraps
	// in full in the detail pane.
	assert.Contains(t, frame, "…")

	h.press("down", "down")

	frame = h.view()
	assert.Contains(t, frame, "╭─ skills ")
	assert.Contains(t, docFlat(docRight(frame)), "read old instructions until setup refreshes it")
	assert.Empty(t, f.called(), "browsing the checks plans, previews and mutates nothing")
}

func TestDoctorDetailShowsTheFixAsTextOnly(t *testing.T) {
	h, f := docOpen(t, docReport(), 120, 40)

	h.press("end")

	frame := h.view()
	assert.Contains(t, frame, "╭─ policy ")
	assert.Contains(t, frame, "! fail  seamark cannot do its job")
	assert.Contains(t, frame, "FIX")
	assert.Contains(t, docFlat(docRight(frame)), "fix .seamark/policy.yaml, then run `seamark doctor` again")
	assert.Contains(t, docFlat(docRight(frame)), "The console never runs it.")
	assert.NotContains(t, frame, "enter for detail", "the wide layout shows the detail beside the list")
	assert.Contains(t, frame, "same as seamark doctor")

	// enter moves the focus into the detail; nothing runs.
	h.press("enter")
	assert.True(t, docView(h).focusDetail)
	assert.Empty(t, f.called(), "enter opens the detail; it never runs the fix")

	h.press("home")
	frame = h.view()
	assert.Contains(t, frame, "No action needed.", "an ok check needs no action")
}

func TestDoctorTabCountShowsProblemsOnly(t *testing.T) {
	h, _ := docOpen(t, docReport(), 120, 40)
	assert.Equal(t, "2", docView(h).tabCount())
	assert.Contains(t, h.view(), "4 Doctor 2")

	healthy := &doctor.Report{Checks: []doctor.Check{{Name: "binary", State: doctor.StateOK, Detail: "ok"}}}
	h2, _ := docOpen(t, healthy, 120, 40)
	assert.Empty(t, docView(h2).tabCount())
	assert.NotContains(t, h2.view(), "4 Doctor 0", "a healthy report shows no count")

	// Before the report arrives the count is unknown, not zero.
	st := &state{svc: newFake(), theme: newTheme(false)}
	assert.Empty(t, newDoctor(st).tabCount())
}

func TestDoctorTabMovesFocusBetweenPanes(t *testing.T) {
	h, _ := docOpen(t, docReport(), 120, 40)
	d := docView(h)

	assert.Contains(t, shellAFooter(h.view()), "enter open")

	h.press("tab")
	assert.True(t, d.focusDetail)
	assert.Contains(t, shellAFooter(h.view()), "scroll")

	// With the detail focused, the movement keys scroll it and the
	// selection stays.
	h.press("down")
	assert.Equal(t, 0, d.list.cursor)

	h.press("tab")
	assert.False(t, d.focusDetail)

	h.press("down")
	assert.Equal(t, 1, d.list.cursor)

	// The policy check has no in-app workflow, so enter focuses its detail.
	h.press("end", "enter")
	require.True(t, d.focusDetail)

	h.press("esc")
	assert.False(t, d.focusDetail, "esc gives the focus back to the list")
}

func TestDoctorNarrowOpensTheDetailFullScreen(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
		w, hgt := size[0], size[1]
		h, _ := docOpen(t, docReport(), w, hgt)

		frame := h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "Doctor checks", "size %v", size)
		assert.Contains(t, frame, "enter for detail", "size %v", size)
		assert.NotContains(t, frame, "FIX", "size %v: one pane at a time", size)

		h.press("end", "enter")

		frame = h.view()
		assertFrame(t, frame, w, hgt)
		assert.Contains(t, frame, "╭─ policy ", "size %v", size)
		assert.Contains(t, frame, "FIX", "size %v", size)
		assert.NotContains(t, frame, "Doctor checks", "size %v", size)
		assert.Contains(t, shellAFooter(frame), "esc back", "size %v", size)

		h.press("esc")
		assert.Contains(t, h.view(), "Doctor checks", "size %v: esc goes back to the list", size)
		assert.Equal(t, 3, docView(h).list.cursor, "size %v: the selection stays", size)
	}
}

func TestDoctorScrollsALongList(t *testing.T) {
	r := &doctor.Report{}
	for i := range 30 {
		r.Checks = append(r.Checks, doctor.Check{Name: "check" + itoa(i+1), State: doctor.StateOK, Detail: "fine"})
	}

	h, _ := docOpen(t, r, 80, 24)

	frame := h.view()
	assertFrame(t, frame, 80, 24)
	assert.Regexp(t, `1–\d+ of 30`, frame, "the list names the visible range")

	h.press("end")

	frame = h.view()
	assert.Contains(t, frame, "check30")
	assert.NotContains(t, frame, "check1 ", "the first rows scroll away")
	assert.Regexp(t, `\d+–30 of 30`, frame)
}

func TestDoctorLongDetailScrolls(t *testing.T) {
	long := strings.Repeat("The check reads a file and reports what it found. ", 20)
	r := &doctor.Report{Checks: []doctor.Check{{Name: "gitignore", State: doctor.StateWarn, Detail: long, Fix: "add the carve-outs"}}}

	h, _ := docOpen(t, r, minWidth, minHeight)
	h.press("enter")

	frame := h.view()
	assertFrame(t, frame, minWidth, minHeight)
	assert.Contains(t, frame, "↓ more · pgdn")

	h.press("pgdown", "pgdown", "pgdown", "pgdown", "pgdown")

	frame = h.view()
	assert.Contains(t, frame, "same as seamark doctor", "the end of the detail is reachable")
	assert.Contains(t, frame, "↑ pgup")
}

func TestDoctorStatesAreNeverHealthyWithoutAReport(t *testing.T) {
	st := &state{svc: newFake(), theme: newTheme(false), width: 120, height: 40}
	d := newDoctor(st)

	frame := ansi.Strip(d.view(120, 35))
	assert.Contains(t, frame, "Running the doctor checks")
	assert.Contains(t, frame, "They change nothing.")
	assert.NotContains(t, frame, "✓")

	st.doctor.set(nil, errors.New("doctor failed"), time.Now())
	frame = ansi.Strip(d.view(120, 35))
	assert.Contains(t, frame, "Could not run the doctor checks")
	assert.Contains(t, frame, "doctor failed")
	assert.Contains(t, frame, "seamark doctor")

	st.doctor = section[*doctor.Report]{}
	st.doctor.set(nil, nil, time.Now())
	frame = ansi.Strip(d.view(120, 35))
	assert.Contains(t, frame, "No doctor report")
	assert.Contains(t, frame, "unknown")

	st.doctor.set(&doctor.Report{}, nil, time.Now())
	frame = ansi.Strip(d.view(120, 35))
	assert.Contains(t, frame, "No doctor checks")
	assert.Contains(t, frame, "unknown, not healthy")
	assert.Empty(t, d.keys(), "no movement key without a check")
}

func TestDoctorStaleReportKeepsTheChecks(t *testing.T) {
	st := &state{svc: newFake(), theme: newTheme(false), width: 120, height: 40}
	d := newDoctor(st)

	st.doctor.set(docReport(), nil, time.Now())
	st.doctor.set(nil, errors.New("permission denied"), time.Now())
	require.True(t, st.doctor.stale())

	frame := ansi.Strip(d.view(120, 35))
	assert.Contains(t, frame, "stale: permission denied")
	assert.Contains(t, frame, "binary", "the previous checks stay visible")
	assert.Equal(t, "2", d.tabCount())
}

func TestDoctorRendersControlSequencesInert(t *testing.T) {
	r := &doctor.Report{Checks: []doctor.Check{{
		Name: "hooks\x1b[31m", State: doctor.StateWarn,
		Detail: "hook reads \x1b[2Jcleared\x1b]0;title\x07", Fix: "run \x1b[31mseamark init\x1b[0m",
	}}, Warns: 1}

	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		h, _ := docOpen(t, r, size[0], size[1])
		h.press("enter")

		out := h.m.View()
		assert.NotContains(t, out, "\x1b", "size %v: no escape sequence reaches the terminal", size)
		assert.NotContains(t, out, "\x07", "size %v", size)
	}
}

func TestDoctorFramesFitAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {200, 50}, {100, 30}, {80, 24}, {minWidth, minHeight}} {
		w, hgt := size[0], size[1]
		h, _ := docOpen(t, docReport(), w, hgt)

		assertFrame(t, h.view(), w, hgt)

		// The policy check has no in-app workflow, so enter stays in the
		// Doctor and shows or focuses the detail.
		h.press("end", "enter")
		require.Equal(t, destDoctor, h.m.active)
		assertFrame(t, h.view(), w, hgt)
	}
}

func TestDoctorPaneWidthsFollowTheDesign(t *testing.T) {
	assert.Equal(t, 64, docListWidth(120), "the 1a list pane is 64 cells at 120 columns")
	assert.Equal(t, 64, docListWidth(200), "a wider terminal grows the detail pane")
	assert.Equal(t, 53, docListWidth(100))

	h, _ := docOpen(t, docReport(), 120, 40)
	lines := strings.Split(h.view(), "\n")

	top := []rune(lines[shellBLineIndex(lines, "Doctor checks")])
	assert.Equal(t, 63, slices.Index(top, '╮'), "the list box ends at column 64")
	assert.Equal(t, 65, slices.Index(top[1:], '╭')+1, "the detail box starts after one gap cell")
	assert.Equal(t, '╮', top[len(top)-1], "the detail box ends at the last column")
}

func TestDoctorOpenIntentSelectsACheck(t *testing.T) {
	f := newFake()
	f.doctor = docReport()

	h := newHarness(t, f, 120, 40)
	h.send(navigateMsg{dest: destDoctor, intent: "check:policy"})

	assert.Equal(t, 3, docView(h).list.cursor)
	assert.Contains(t, h.view(), "╭─ policy ")

	h.send(navigateMsg{dest: destDoctor, intent: "check:unknown-name"})
	assert.Equal(t, 3, docView(h).list.cursor, "an unknown name keeps the selection")
}
