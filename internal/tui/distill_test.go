package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The tests in this file cover the scope form and the plan review of a
// distillation. They prove that navigation plans nothing. They also
// cover the limit rules, the region, the review, and the no-op states.

// distRequests wraps the plan of f and records every request.
func distRequests(f *fakeServices) *[]learning.DistillRequest {
	var reqs []learning.DistillRequest

	plan := f.planDistill
	f.planDistill = func(req learning.DistillRequest) (*learning.DistillPreview, error) {
		reqs = append(reqs, req)

		return plan(req)
	}

	return &reqs
}

// distNoRun fails the test when the run starts.
func distNoRun(t *testing.T) func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
	return func(context.Context, *learning.DistillPreview, func(distill.Event)) (*distill.Execution, error) {
		t.Fatal("the run must not start")

		return nil, nil
	}
}

// distFullPreview is the review fixture: six groups, a cleanup and 23
// recorded signatures. Every eligible group fits the default limit, as
// the engine plans it; distOverLimit adds the groups over a limit.
func distFullPreview() *learning.DistillPreview {
	return distPreview(distPreviewOpts{groups: 6, cleanup: true, skipped: 23})
}

// distFixedDir makes the process directory of the review /work/repo.
func distFixedDir(t *testing.T, dir string) {
	t.Helper()

	restore := distGetwd
	distGetwd = func() (string, error) { return dir, nil }

	t.Cleanup(func() { distGetwd = restore })
}

func TestDistillNavigationNeverPlansOrRuns(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	les := h.m.screens[destLessons].(*lessons)

	assert.Contains(t, h.view(), "Distillation preparation")
	assert.Contains(t, h.view(), "nothing is planned or sent yet")

	// Sub-views, destinations, a refresh and the form keys read only.
	h.press("p", "s", "e", "u", "1", "3", "u", "r", "down", "down", "down", "up", "space", "/")
	h.typeText("internal/api")
	h.press("esc", "i")

	assert.Empty(t, f.called(), "browsing and editing the draft never plan, run or clean up")

	// esc closes the draft: it goes away, and Evidence shows.
	h.press("esc")
	assert.Equal(t, lesEvidence, les.sub)
	assert.Nil(t, runViewOf(h).cur, "a closed draft is not a session record")
	assert.NotContains(t, lesSubTabs(h), "Run", "without a record the Run tab hides again")
	assert.Empty(t, f.called())
}

func TestDistillScopePlansTheDefaultLimit(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	reqs := distRequests(f)
	h := distOpen(t, f, 120, 40)

	frame := h.view()
	assert.Contains(t, frame, "(●) 10 groups (default)")
	assert.Contains(t, frame, "( ) All eligible groups")
	assert.Contains(t, frame, "everywhere")
	assert.Contains(t, frame, "the whole repository")
	assert.Contains(t, frame, "Planning reads only. The agent runs only after you")
	assert.Contains(t, footerLine(frame), "enter review plan")

	h.press("enter")

	require.Len(t, *reqs, 1)
	assert.Equal(t, learning.DistillRequest{Limit: distill.DefaultGroupLimit}, (*reqs)[0],
		"the default is the CLI default, never all groups")
	assert.Equal(t, []string{"PlanDistill"}, f.called(), "planning reads only; nothing runs")
	assert.Equal(t, stepPreview, distRecOf(t, h).step)
}

func TestDistillScopeLimitRules(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	reqs := distRequests(f)
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	// The custom choice opens a digit input: "-" never enters.
	h.press("down", "down", "space")
	require.True(t, rec.form.editCustom)
	require.True(t, rec.capturing(), "digits must not switch the destination")

	h.typeText("-5")
	assert.Equal(t, "5", rec.form.custom.value(), "a negative limit cannot be typed")
	assert.Equal(t, destLessons, h.m.active)

	// Zero and empty are invalid: the note shows and enter plans nothing.
	for _, typed := range []string{"0", ""} {
		rec.form.custom.set(typed)
		h.press("enter")

		frame := h.view()
		assert.Contains(t, frame, "Enter a whole number above 0.", "value %q", typed)
		assert.NotContains(t, footerLine(frame), "enter review plan", "value %q", typed)

		h.press("enter")
		assert.Empty(t, *reqs, "an invalid limit never plans (value %q)", typed)

		h.press("space")
	}

	h.typeText("1234567")
	assert.Equal(t, "12345", rec.form.custom.value(), "the input keeps five digits")

	rec.form.custom.set("25")
	h.press("enter", "enter")
	require.Len(t, *reqs, 1)
	assert.Equal(t, learning.DistillRequest{Limit: 25}, (*reqs)[0])

	// All eligible groups needs its own explicit choice.
	h.press("n", "down", "space", "enter")
	require.Len(t, *reqs, 2)
	assert.Equal(t, learning.DistillRequest{AllGroups: true}, (*reqs)[1], "all maps to the engine's 0 only when chosen")
	assert.Contains(t, h.view(), "limit all eligible groups")

	// Back to the default choice.
	h.press("n", "up", "up", "space", "enter")
	require.Len(t, *reqs, 3)
	assert.Equal(t, learning.DistillRequest{Limit: distill.DefaultGroupLimit}, (*reqs)[2])
	assert.NotContains(t, f.called(), "RunDistill")
}

func TestDistillScopeShowsTheServiceLimitError(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	f.planDistill = func(learning.DistillRequest) (*learning.DistillPreview, error) {
		return nil, distill.ErrInvalidLimit
	}

	h := distOpen(t, f, 120, 40)
	h.press("enter")

	rec := distRecOf(t, h)
	assert.Equal(t, stepScope, rec.step, "the form holds the value to fix")
	assert.Contains(t, lesFlat(h.view()), "The group limit is not valid")
}

func TestDistillScopeRegion(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	reqs := distRequests(f)
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	h.press("/")
	h.typeText("./internal/api/")
	h.press("enter")

	assert.Contains(t, h.view(), "scope internal/api", "the normalized scope shows before any plan")
	assert.Equal(t, "Distillation · internal/api", rec.title())

	// esc restores the value from before the edit.
	h.press("/")
	h.typeText("xyz")
	h.press("esc")
	assert.Equal(t, "./internal/api/", rec.form.region.value())

	// A path outside the workspace blocks the plan.
	h.press("/")
	rec.form.region.set("../elsewhere")
	h.press("enter")

	frame := h.view()
	assert.Contains(t, frame, "outside the workspace")
	assert.NotContains(t, footerLine(frame), "enter review plan")

	h.press("enter")
	assert.Empty(t, *reqs, "an escaping region never plans")

	// The service decides again: its refusal returns to the form.
	f.planDistill = func(learning.DistillRequest) (*learning.DistillPreview, error) {
		return nil, distill.ErrRegionEscapes
	}

	h.press("/")
	rec.form.region.set("internal")
	h.press("enter", "enter")

	assert.Equal(t, stepScope, rec.step)
	assert.Equal(t, "The region is outside the workspace. Enter a path inside the repository.", rec.form.note)
	assert.Contains(t, h.view(), "! The region is outside the workspace. Enter a path")
}

func TestDistillScopePrefillsOnlyVisibly(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	f.ledger = lesLedger()

	h := newHarness(t, f, 120, 40)
	h.press("3", "/")
	h.typeText("internal/api")
	h.press("enter")
	require.Equal(t, "internal/api", lesProps(h).region)

	h.send(navigateMsg{dest: destLessons, intent: "distill"})

	rec := distRecOf(t, h)
	assert.Equal(t, "internal/api", rec.form.region.value())
	assert.Contains(t, h.view(), "prefilled from the Proposals region filter", "the prefill names its source")

	// A changed region is no longer the prefill.
	h.press("/")
	rec.form.region.set("internal/cli")
	h.press("enter")
	assert.NotContains(t, h.view(), "prefilled from the Proposals region filter")

	// Without a filter the region is empty.
	h2 := distOpen(t, distFake(distFullPreview(), distNoRun(t)), 120, 40)
	assert.Empty(t, distRecOf(t, h2).form.region.value())
	assert.NotContains(t, h2.view(), "prefilled")
}

func TestDistillScopeBlocksAnInvalidAgentConfiguration(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	f.settings = fakeSettingsSnapshot(t, "agent:\n  cli: nosuch\n")
	reqs := distRequests(f)

	h := distOpen(t, f, 120, 40)

	frame := h.view()
	assert.Contains(t, frame, "The agent configuration is invalid")
	assert.NotContains(t, footerLine(frame), "enter review plan")

	h.press("enter")
	assert.Empty(t, *reqs, "a paid run never falls back to a default agent")
	assert.Contains(t, footerLine(frame), "o open Settings › Inference")

	h.press("o")
	assert.Equal(t, destSettings, h.m.active, "the blocker links to the inference settings")

	// A valid configuration shows the resolved preset.
	f2 := distFake(distFullPreview(), distNoRun(t))
	f2.settings = fakeSettingsSnapshot(t, "agent:\n  cli: codex\n")

	h2 := distOpen(t, f2, 120, 40)
	assert.Contains(t, h2.view(), "preset codex")
}

func TestDistillReviewShowsThePlan(t *testing.T) {
	distFixedDir(t, "/work/repo")

	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	h.press("enter")

	frame := h.view()

	for _, want := range []string{
		"scope everywhere", "limit 10 groups", "agent claude", "planned 09:53:12", "engine order",
		"Tokens (est.)", "#88", "PR #12", "internal/cli/lessons.go", "fix:conventional", ".golangci.yml +1",
	} {
		assert.Contains(t, frame, want)
	}

	// PlanDistill needs a ready index, so the header of a plan shows one.
	assert.NotContains(t, frame, "no index")
	assert.NotContains(t, frame, "OVER THE LIMIT", "six eligible groups fit the default limit")

	// Engine order: the groups keep the order of the plan.
	cli, status, gate := strings.Index(frame, "internal/cli "), strings.Index(frame, "internal/status "),
		strings.Index(frame, "internal/gate ")
	assert.Less(t, cli, status)
	assert.Less(t, status, gate)
	assert.Contains(t, footerLine(frame), "y start run")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h.resize(size[0], size[1])
		assertFrame(t, h.view(), size[0], size[1])
	}

	// A tall compact frame shows the whole Plan pane in one box.
	h.resize(80, 80)
	flat := lesBoxFlat(h.view())

	for _, want := range []string{
		"6 of 6 eligible", "~8.5k tokens (estimated)", "23 groups already distilled", "2 stale pending: p4, p7",
		"Runs in /work/repo", "SENT DATA", "Later prompts can grow with labels learned in this run",
		"No cost figure is available", "Nothing is sent until you press y.",
		// The cleanup is the removal of stale pending proposals, not pruning.
		"first remove the stale pending p4, p7", "prune applied pins",
	} {
		assert.Contains(t, flat, want)
	}

	// The compact layout shows the groups on tab, with the cleanup rows
	// and their rules at the end.
	h.press("tab")

	groups := h.view()
	assert.Contains(t, groups, "╭─ Groups ")
	assert.Contains(t, groups, "STALE PENDING  removed before the first group; applied pins stay")
	assert.Contains(t, groups, "wrap-legacy-errors")
	assert.Contains(t, groups, "retire-old-flag")
	assert.Equal(t, []string{"PlanDistill"}, f.called())
}

func TestDistillReviewShowsTheGroupsOverTheLimit(t *testing.T) {
	distFixedDir(t, "/work/repo")

	// The engine defers groups only when the planned groups reach the
	// limit, so the fixture refuses its deferred groups at the default.
	f := distFake(distOverLimit(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	h.press("enter")

	assert.Contains(t, lesFlat(h.view()), "fixture: 6 planned and 4 deferred groups need a limit of 6")

	h2 := distOpen(t, distFake(distOverLimit(), distNoRun(t)), 120, 40)
	h2.press(distOverLimitKeys...)

	frame := h2.view()
	assert.Contains(t, frame, "limit 6 groups")
	assert.Contains(t, frame, "Groups    6 of 10 eligible")
	assert.Contains(t, frame, "OVER THE LIMIT  4 groups wait for another run")
	assert.Contains(t, frame, "internal/extra0")

	for _, size := range [][2]int{{120, 40}, {80, 24}, {60, 20}} {
		h2.resize(size[0], size[1])
		assertFrame(t, h2.view(), size[0], size[1])
	}
}

func TestDistillReviewNamesTheWorkingDirectory(t *testing.T) {
	distFixedDir(t, "/elsewhere")

	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 80, 80)
	h.press("enter")

	assert.Contains(t, lesBoxFlat(h.view()), "/elsewhere (the console directory, not the repository root)")

	// A command with its own directory runs there.
	p := distFullPreview()
	p.Invocation = agent.CommandSpec{Name: "codex", Argv: []string{"codex", "exec"}, Dir: "/work/repo"}

	h2 := distOpen(t, distFake(p, distNoRun(t)), 80, 80)
	h2.press("enter")

	flat := lesBoxFlat(h2.view())
	assert.Contains(t, flat, "Runs in /work/repo")
	assert.Contains(t, flat, "codex exec")
	assert.NotContains(t, flat, "/elsewhere")
}

func TestDistillReviewBlockersStopTheStart(t *testing.T) {
	o := distPreviewOpts{groups: 3, blockers: []string{`agent cli "claude" not found on PATH`}}
	f := distFake(distPreview(o), distNoRun(t))
	h := distOpen(t, f, 80, 40)
	h.press("enter", "y")

	frame := h.view()
	assert.Contains(t, lesBoxFlat(frame), `agent cli "claude" not found on PATH`)
	assert.NotContains(t, footerLine(frame), "y start run")
	assert.True(t, strings.HasPrefix(strings.TrimSpace(footerLine(frame)), "enter plan"),
		"the compact footer names the only useful key first: %q", footerLine(frame))
	assert.Equal(t, 1, f.callCount("PlanDistill"))

	h.resize(120, 40)
	assert.Contains(t, footerLine(h.view()), "enter plan again", "a wide footer keeps the whole label")
	h.resize(80, 40)

	h.press("enter")
	assert.Equal(t, 2, f.callCount("PlanDistill"), "enter plans again")
	assert.Zero(t, f.callCount("RunDistill"))

	h.press("o")
	assert.Equal(t, destSettings, h.m.active, "a missing agent links to the inference settings")
}

func TestDistillPlanErrorsNameTheirRoute(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		title string
		route string
		// link is the destination that o opens; destCount means none.
		link destination
	}{
		{"lessons file", &learning.DistillConfigError{Path: ".seamark/lessons.yaml", Err: errors.New("yaml: bad")},
			"The configuration blocks the plan", "Edit .seamark/lessons.yaml", destCount},
		{"config file", &learning.DistillConfigError{Path: settingsFile, Err: errors.New("unknown agent cli")},
			"The configuration blocks the plan", "Press o to fix it in Settings", destSettings},
		{"index", errors.Join(learning.ErrDistillNotReady, errors.New("index database is absent")),
			"The index is not ready", "Press o to build it from Overview", destOverview},
		{"stale", distill.ErrStale, "The workspace changed while planning", "Plan again", destCount},
		{"other", errors.New("disk full"), "Could not plan the distillation", "disk full", destCount},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := distFake(distFullPreview(), distNoRun(t))
			f.planDistill = func(learning.DistillRequest) (*learning.DistillPreview, error) { return nil, c.err }

			h := distOpen(t, f, 120, 40)
			h.press("enter", "y")

			flat := lesBoxFlat(h.view())
			assert.Contains(t, flat, c.title)
			assert.Contains(t, flat, c.route)
			assert.Contains(t, flat, "Nothing was sent or written.")
			assert.Zero(t, f.callCount("RunDistill"))

			h.press("enter")
			assert.Equal(t, 2, f.callCount("PlanDistill"), "enter plans again")

			for _, size := range [][2]int{{80, 24}, {60, 20}} {
				h.resize(size[0], size[1])
				assertFrame(t, h.view(), size[0], size[1])
			}

			// o follows the route, and it only navigates.
			h.press("o")

			want := c.link
			if want == destCount {
				want = destLessons
			}

			assert.Equal(t, want, h.m.active)
			assert.Zero(t, f.callCount("BuildIndex"), "the build keeps its own confirmation")
			assert.Zero(t, f.callCount("SaveSettings"))
		})
	}
}

func TestDistillNothingNewExplainsWhy(t *testing.T) {
	cases := []struct {
		name string
		p    *learning.DistillPreview
		want string
	}{
		{"no findings", distPreview(distPreviewOpts{}), "The index holds no findings in the whole repository."},
		{"all recorded", distPreview(distPreviewOpts{skipped: 14}), "14 groups in the whole repository already " +
			"have recorded signatures, so no agent call is needed."},
		{"no group", func() *learning.DistillPreview {
			p := distPreview(distPreviewOpts{})
			p.Plan.Corpus = 3

			return p
		}(), "The 3 findings in the whole repository form no evidence group"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := distFake(c.p, distNoRun(t))
			h := distOpen(t, f, 120, 40)
			h.press("enter", "y", "c")

			flat := lesBoxFlat(h.view())
			assert.Contains(t, flat, "Nothing new to distill")
			assert.Contains(t, flat, c.want)
			assert.Contains(t, flat, "nothing to do")
			assert.NotContains(t, flat, "stale pending")
			assert.Equal(t, []string{"PlanDistill"}, f.called(), "no agent call and no cleanup")
		})
	}
}

func TestDistillCleanupOnlyNeedsItsOwnConfirmation(t *testing.T) {
	p := distPreview(distPreviewOpts{skipped: 14, cleanup: true})
	f := distFake(p, distNoRun(t))

	var cleaned []*learning.DistillPreview

	f.cleanupPending = func(pv *learning.DistillPreview) (int, error) {
		cleaned = append(cleaned, pv)

		return 2, nil
	}

	h := distOpen(t, f, 120, 40)
	h.press("enter")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "2 stale pending proposals: p4, p7")
	assert.Contains(t, flat, "this is not pin pruning")
	assert.Contains(t, footerLine(h.view()), "c clean up")

	// y and enter never clean up; c opens the confirmation.
	h.press("y")
	assert.Empty(t, cleaned)

	h.press("c")

	rec := distRecOf(t, h)
	require.True(t, rec.capturing())

	modal, ok := rec.modal(120, 39)
	require.True(t, ok)
	assert.Contains(t, modal, "p4")
	assert.Contains(t, modal, "wrap-legacy-errors")
	assert.Contains(t, modal, "retire-old-flag")
	assert.Contains(t, modal, "this is not pin pruning")

	h.press("esc")
	assert.Empty(t, cleaned, "esc keeps the rows")

	// A busy slot refuses the cleanup with a reason.
	require.True(t, h.m.st.begin("Building the index"))
	h.press("c", "y")
	assert.Empty(t, cleaned)
	assert.Contains(t, h.view(), "Another operation is running: Building the index")
	h.send(mutationDoneMsg{})

	h.press("c", "y")
	require.Len(t, cleaned, 1, "one confirmation, one cleanup")
	assert.Equal(t, p.Plan.Cleanup, cleaned[0].Plan.Cleanup, "the cleanup binds the planned rows")
	assert.Contains(t, lesBoxFlat(h.view()), "Removed 2 stale pending proposals of p4, p7.")
	assert.False(t, h.m.st.busy(), "the slot frees")
	assert.False(t, rec.draft(), "the record did durable work")
	assert.Zero(t, f.callCount("RunDistill"))

	h.press("c", "y")
	assert.Len(t, cleaned, 1, "a done cleanup does not repeat")
}

func TestDistillCleanupShowsAStaleLedger(t *testing.T) {
	f := distFake(distPreview(distPreviewOpts{skipped: 1, cleanup: true}), distNoRun(t))
	f.cleanupPending = func(*learning.DistillPreview) (int, error) {
		return 0, errors.Join(distill.ErrStale, errors.New("ledger changed"))
	}

	h := distOpen(t, f, 120, 40)
	h.press("enter", "c", "y")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "The cleanup removed nothing")
	assert.Contains(t, flat, "The ledger changed since the plan. Press enter to plan again.")

	h.press("enter")
	assert.Equal(t, 2, f.callCount("PlanDistill"))
}

func TestDistillNotReadyIndexNeverPlansAHiddenBuild(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	f.index = IndexView{Probe: workspace.IndexProbe{State: workspace.IndexAbsent}}
	f.planDistill = func(learning.DistillRequest) (*learning.DistillPreview, error) {
		return nil, learning.ErrDistillNotReady
	}

	h := distOpen(t, f, 120, 40)
	h.press("enter", "y", "enter")

	assert.Zero(t, f.callCount("BuildIndex"), "a distillation never builds the index")
	assert.Zero(t, f.callCount("RunDistill"))
}

func TestDistillAboutModeEndsInTheWideLayout(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	reqs := distRequests(f)
	h := distOpen(t, f, 80, 24)
	rec := distRecOf(t, h)

	h.press("i")
	require.True(t, rec.form.about)
	assert.Contains(t, footerLine(h.view()), "i back to the form")

	// The wide frame shows the form beside the explanation, so the form
	// keys work there, and the footer names them.
	h.resize(120, 40)
	assert.NotContains(t, footerLine(h.view()), "i back to the form")
	assert.True(t, strings.HasPrefix(distFooterHead(h), "enter review plan"), distFooterHead(h))

	h.press("down", "space")
	assert.False(t, rec.form.about)
	assert.Equal(t, distRowDefault, rec.form.row, "the arrow moves the visible cursor")

	h.press("enter")
	require.Len(t, *reqs, 1, "enter plans from the visible form")
	assert.Equal(t, stepPreview, rec.step)
}

func TestDistillCustomLimitEscRestoresTheChoice(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	reqs := distRequests(f)
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	h.press("down", "down", "space")
	require.True(t, rec.form.editCustom)
	require.Equal(t, distLimitCustom, rec.form.limit)

	h.typeText("7")
	h.press("esc")

	assert.Equal(t, distLimitDefault, rec.form.limit, "esc restores the choice from before the edit")
	assert.Empty(t, rec.form.custom.value())

	frame := h.view()
	assert.Contains(t, frame, "(●) 10 groups (default)")
	assert.NotContains(t, frame, "Enter a whole number above 0.", "a cancelled edit leaves a valid form")
	assert.True(t, strings.HasPrefix(distFooterHead(h), "enter review plan"), distFooterHead(h))

	// A kept custom number never becomes the choice through a cancelled
	// edit either.
	h.press("space")
	h.typeText("25")
	h.press("enter", "up", "space")
	require.Equal(t, distLimitDefault, rec.form.limit)

	h.press("down", "space", "esc")
	assert.Equal(t, distLimitDefault, rec.form.limit)
	assert.Equal(t, "25", rec.form.custom.value(), "esc keeps the typed number from before the edit")

	h.press("enter")
	require.Len(t, *reqs, 1)
	assert.Equal(t, learning.DistillRequest{Limit: distill.DefaultGroupLimit}, (*reqs)[0])
}

func TestDistillContextLineKeepsTheStepAtEverySize(t *testing.T) {
	var calls []distRunCall

	o := distPreviewOpts{groups: 6, cleanup: true, region: "internal/a/really/long/region/name/that/fills/the/line"}
	f := distFake(distPreview(o), nil)
	f.runDistill = distScripted(&calls, distAllDone, nil)

	h := distOpen(t, f, 120, 40)

	steps := []struct {
		keys          []string
		wide, compact string
	}{
		{nil, "● Scope", "● Scope 1/4"},
		{[]string{"enter"}, "● Review", "● Review 2/4"},
		{[]string{"y"}, "● Result", "● Result 4/4"},
	}

	for _, s := range steps {
		h.resize(120, 40)
		h.press(s.keys...)
		assert.Contains(t, h.view(), s.wide, "the long scope keeps a trail at 120 columns too")

		for _, size := range [][2]int{{80, 24}, {60, 20}} {
			h.resize(size[0], size[1])
			assert.Contains(t, h.view(), s.compact, "size %v: the facts give way to the step", size)
		}
	}
}

func TestDistillAgentReadsTheSameOnScopeAndReview(t *testing.T) {
	distFixedDir(t, "/work/repo")

	f := distFake(distFullPreview(), distNoRun(t))
	f.settings = fakeSettingsSnapshot(t, "agent:\n  cli: claude\n")

	h := distOpen(t, f, 120, 40)
	assert.Contains(t, h.view(), "claude -p · preset claude", "the scope names the preset command")

	h.press("enter")
	assert.Contains(t, h.view(), "claude -p · preset claude", "the review names it the same way")

	// A custom command reads the same way on both steps.
	p := distFullPreview()
	p.Invocation = agent.CommandSpec{Name: "custom", Argv: []string{"mytool", "--flag"}}

	f2 := distFake(p, distNoRun(t))
	f2.settings = fakeSettingsSnapshot(t, "agent:\n  argv: [mytool, --flag]\n")

	h2 := distOpen(t, f2, 120, 40)
	assert.Contains(t, h2.view(), "mytool --flag · custom agent.argv")

	h2.press("enter")
	assert.Contains(t, h2.view(), "mytool --flag · custom agent.argv")

	// A long command line gives way, so the kind stays visible.
	f3 := distFake(p, distNoRun(t))
	f3.settings = fakeSettingsSnapshot(t,
		"agent:\n  argv: [mytool, --model, a-very-long-model-name, --system-prompt-file, prompts/distill.md]\n")

	h3 := distOpen(t, f3, 80, 24)
	assert.Contains(t, h3.view(), "· custom agent.argv")
}

// distWithColors renders the test with 256 colors, so a test can tell
// the attention color from the failure color. It restores the plain
// profile of TestMain.
func distWithColors(t *testing.T) {
	t.Helper()

	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
}

// distColorOf returns the escape sequence that starts text in style s.
func distColorOf(s lipgloss.Style) string {
	seq, _, _ := strings.Cut(s.Render("X"), "X")

	return seq
}

func TestDistillBlockedStatesUseTheAttentionColor(t *testing.T) {
	distWithColors(t)

	theme := newTheme(false)
	attention, failure := distColorOf(theme.Attention), distColorOf(theme.Failure)
	require.NotEqual(t, attention, failure)

	// An agent that is not on PATH blocks the start.
	o := distPreviewOpts{groups: 3, blockers: []string{`agent cli "claude" not found on PATH`}}
	f := distFake(distPreview(o), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	h.press("enter")

	rec := distRecOf(t, h)
	notes := strings.Join(rec.planNotes(60), "\n")
	assert.Contains(t, notes, attention)
	assert.NotContains(t, notes, failure)

	// A configuration or an index that blocks the plan is a blocked
	// state; any other error stays a failure.
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"config", &learning.DistillConfigError{Path: settingsFile, Err: errors.New("unknown agent cli")}, attention},
		{"lessons", &learning.DistillConfigError{Path: ".seamark/lessons.yaml", Err: errors.New("bad")}, attention},
		{"index", errors.Join(learning.ErrDistillNotReady, errors.New("index database is absent")), attention},
		{"other", errors.New("disk full"), failure},
	}

	for _, c := range cases {
		rec.prev.err = c.err

		box := rec.planErrBox(60, 20)
		assert.Contains(t, box, c.want, c.name)

		if c.want == attention {
			assert.NotContains(t, box, failure, c.name)
		}
	}

	// An invalid agent configuration blocks the scope in the same color.
	f2 := distFake(distFullPreview(), distNoRun(t))
	f2.settings = fakeSettingsSnapshot(t, "agent:\n  cli: nosuch\n")

	h2 := distOpen(t, f2, 120, 40)

	row, _ := distRecOf(t, h2).agentRows(58)
	assert.Contains(t, row, attention)
	assert.NotContains(t, row, failure)
}

// The tests below hold the review findings of the distillation record:
// the custom limit input, the refusal messages, the full finding IDs,
// and the plan wording.

func TestDistillScopeCustomLimitWaitsForInput(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)
	rec := distRecOf(t, h)

	// A just opened input shows its placeholder and no error.
	h.press("down", "down", "space")
	require.True(t, rec.form.editCustom)

	frame := h.view()
	assert.Contains(t, frame, "custom: _ a whole number above 0")
	assert.NotContains(t, frame, "Enter a whole number above 0.", "nothing is typed yet")

	// An empty input that closes shows the rule.
	h.press("enter")
	assert.Contains(t, h.view(), "Enter a whole number above 0.")

	// A new edit starts without the error again.
	h.press("space")
	assert.NotContains(t, h.view(), "Enter a whole number above 0.")

	// One group reads in the singular, as everywhere else.
	h.typeText("1")
	h.press("enter")

	frame = h.view()
	assert.Contains(t, frame, "custom: 1 group")
	assert.NotContains(t, frame, "1 groups")
	assert.NotContains(t, frame, "Enter a whole number above 0.")

	rec.form.custom.set("12")
	assert.Contains(t, h.view(), "custom: 12 groups")

	// A plan request with an empty value shows the rule, too.
	h.press("space", "backspace", "backspace", "esc")
	rec.form.limit = distLimitCustom
	rec.form.custom.set("")
	rec.form.customChecked = false
	h.press("enter")
	assert.Contains(t, h.view(), "Enter a whole number above 0.")
	assert.Empty(t, f.called(), "an invalid limit never plans")
}

func TestDistillRefusedPlanMessageClearsAfterAPlan(t *testing.T) {
	f := distFake(distFullPreview(), distNoRun(t))
	h := distOpen(t, f, 120, 40)

	h.press("/")
	h.typeText("../tmp")
	h.press("enter", "enter")
	require.Equal(t, "The region is outside the workspace.", h.m.toast.text)

	// A valid region plans, and the refusal no longer shows beside it.
	h.press("/")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.press("enter", "enter")
	require.Equal(t, stepPreview, distRecOf(t, h).step)
	assert.Empty(t, h.m.toast.text, "the stale refusal is gone")
	assert.NotContains(t, h.view(), "outside the workspace")
}

func TestDistillRefusedStartNamesTheCauseOnce(t *testing.T) {
	var calls []distRunCall

	stale := fmt.Errorf("%w: %s changed since the preview", distill.ErrStale, ".seamark/config.yaml")

	f := distFake(distFullPreview(), nil)
	f.runDistill = distScripted(&calls, func(*learning.DistillPreview) *distill.Execution { return nil }, stale)

	h := distOpen(t, f, 80, 60)
	h.press("enter", "y")

	assert.Equal(t, "The distillation did not start: .seamark/config.yaml changed since the preview.", h.m.toast.text)

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "The plan is out of date: .seamark/config.yaml changed since the preview. Nothing was sent.")
	assert.NotContains(t, flat, "changed since the plan:", "the cause is not said twice")

	// A new plan clears the refusal, so it never shows beside the run.
	h.press("enter")
	assert.Empty(t, h.m.toast.text)

	rec := distRecOf(t, h)
	rec.warned = time.Now()
	require.NotNil(t, rec.start())
	assert.True(t, rec.warned.IsZero(), "a start clears a recent refusal")
}

func TestDistillSettleClearsOnlyARecentRefusal(t *testing.T) {
	h := distOpen(t, distFake(distFullPreview(), nil), 120, 40)
	rec := distRecOf(t, h)

	assert.Nil(t, rec.settle(), "no refusal, nothing to clear")

	rec.warned = time.Now().Add(-agentWarnTTL - time.Second)
	assert.Nil(t, rec.settle(), "an expired refusal is gone, and a clear would remove a newer message")

	rec.warned = time.Now()
	cleared := rec.settle()
	require.NotNil(t, cleared)
	assert.Equal(t, toastMsg{tone: ToneNeutral}, cleared())
	assert.Nil(t, rec.settle(), "one clear per refusal")
}

func TestDistillEvidenceShowsTheFullFindingIDs(t *testing.T) {
	distFixedDir(t, "/work/repo")

	// Real fix IDs have 19 digits, and review IDs share their first digits.
	p := distFullPreview()
	ev := p.Plan.Preflight.Groups[0].Evidence
	ev[0].ID, ev[1].ID, ev[2].ID = 3997123456, 3997123999, 1714358230123456789

	want := []string{"#3997123456", "#3997123999", "#1714358230123456789"}

	for _, c := range []struct {
		w, h int
		keys []string
	}{
		{120, 40, nil}, {80, 40, []string{"tab"}}, {60, 20, []string{"tab"}},
	} {
		h := distOpen(t, distFake(p, distNoRun(t)), c.w, c.h)
		h.press("enter")
		h.press(c.keys...)

		frame := h.view()
		assertFrame(t, frame, c.w, c.h)

		for _, id := range want {
			assert.Contains(t, frame, id+" ", "size %dx%d shows the full ID", c.w, c.h)
		}
	}

	// The rows keep the file name of the path and fit the evidence width
	// of the 60, 120 and 80 column layouts.
	for _, w := range []int{50, 61, 70} {
		for _, line := range distEvidenceLines(newTheme(false), ev, w+6, 6) {
			assert.LessOrEqual(t, width(line), w+6)
			assert.True(t, strings.HasSuffix(strings.TrimRight(line, " "), ".go"), line)
		}
	}
}

func TestDistillPlanWordsACustomAgentAndOneCleanup(t *testing.T) {
	distFixedDir(t, "/work/repo")

	p := distFullPreview()
	p.Invocation = agent.CommandSpec{Name: "custom", Argv: []string{"my-agent", "--json"}}
	p.Plan.Cleanup = p.Plan.Cleanup[:1]

	h := distOpen(t, distFake(p, distNoRun(t)), 80, 80)
	h.press("enter")

	flat := lesBoxFlat(h.view())
	assert.Contains(t, flat, "send 6 groups to the custom agent.argv command, one call each")
	assert.Contains(t, flat, "first remove the stale pending p4: its evidence group is gone")

	// A cleanup-only plan offers one row in the singular.
	one := distPreview(distPreviewOpts{skipped: 14, cleanup: true})
	one.Plan.Cleanup = one.Plan.Cleanup[:1]

	h2 := distOpen(t, distFake(one, distNoRun(t)), 80, 60)
	h2.press("enter")

	flat = lesBoxFlat(h2.view())
	assert.Contains(t, flat, "Its evidence group no longer exists, so a run would remove it first. c removes it after")
	assert.False(t, strings.Contains(flat, "Their evidence group"), flat)
}
