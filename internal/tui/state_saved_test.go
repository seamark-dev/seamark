package tui

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// The tests of state_saved.go. They cover the receivers and the senders
// of lessonsSavedMsg (contracts P4-12). They also cover the check of the
// reload after a write that landed (P4-18, [F02-refresh]).

// pinsSaved is the message of a write of lessons.yaml from Pins.
var pinsSaved = lessonsSavedMsg{at: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), by: "Lessons › Pins"}

// lessonsReceiver is one row of TestLessonsSavedReceivers. open builds a
// console with the preview of one holder. check asserts its state after
// a write of lessons.yaml. yCalls is the service call of a y after the
// write: the commit of a preview that stays live, or "" when y must call
// nothing.
type lessonsReceiver struct {
	name   string
	open   func(t *testing.T) *harness
	check  func(t *testing.T, h *harness)
	yCalls string
}

// lessonsReceivers lists every holder whose preview binds lessons.yaml,
// and the holders that must stay. A new lessonsBound holder joins here.
func lessonsReceivers() []lessonsReceiver {
	return []lessonsReceiver{
		{
			name: "apply preview closes and keeps the selection",
			open: func(t *testing.T) *harness {
				h := lesOpen(t, pendingLedgerFake(), 120, 40)
				h.press("space", "a")
				require.NotNil(t, lesProps(h).dec)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.Nil(t, lesProps(h).dec)
				assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the apply preview closed. Press a in "+
					"Proposals to preview it again.", h.m.toast.text)
				assert.NotEmpty(t, lesProps(h).selected, "the selection is the draft of a decision; it stays")
			},
		},
		{
			name: "dismiss preview stays",
			open: func(t *testing.T) *harness {
				h := lesOpen(t, pendingLedgerFake(), 120, 40)
				h.press("d")
				require.NotNil(t, lesProps(h).dec)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.NotNil(t, lesProps(h).dec, "a dismiss writes only the ledger")
			},
			yCalls: "CommitDecision:dismiss",
		},
		{
			name: "distillation plan drops to its scope",
			open: func(t *testing.T) *harness {
				h := distOpen(t, distFake(distFullPreview(), distNoRun(t)), 120, 40)
				distRecOf(t, h).form.region.Set("internal/api")
				h.press("enter")
				require.True(t, distRecOf(t, h).runnable())

				return h
			},
			check: func(t *testing.T, h *harness) {
				rec := distRecOf(t, h)
				assert.Equal(t, stepScope, rec.step)
				assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the plan was dropped. Press enter "+
					"to plan again.", rec.form.note)
				assert.Equal(t, "internal/api", rec.form.region.Value(), "the scope form keeps its input")
			},
		},
		{
			name: "extraction preview is marked out of date",
			open: func(t *testing.T) *harness {
				p := extPreview(extMixed())

				return extOpen(t, extFake(p, extNoRun(t)), p, 120, 40)
			},
			check: func(t *testing.T, h *harness) {
				rec := extRecOf(t, h)
				assert.False(t, rec.runnable())
				assert.Contains(t, extTallFlat(h), "This preview is out of date: Lessons › Pins changed "+
					".seamark/lessons.yaml. Nothing was sent. Press enter to plan again.")
				assert.NotEmpty(t, rec.req.IDs, "the request stays")
			},
		},
		{
			name: "maintenance preview closes",
			open: func(t *testing.T) *harness {
				fx := maintFixture(t)
				p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
				h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
				require.NotNil(t, maintHostOf(h).modal)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.Nil(t, maintHostOf(h).modal)
				assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the retarget preview closed. Press w "+
					"in Pins to preview it again.", h.m.toast.text)
			},
		},
		{
			name: "maintenance result stays",
			open: func(t *testing.T) *harness {
				fx := maintFixture(t)
				p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
				h := maintOpen(t, maintFakeOf(fx, p, nil), p.Request, 120, 40)
				h.press("y")
				require.Equal(t, flowResult, maintHostOf(h).modal.stage)

				return h
			},
			check: func(t *testing.T, h *harness) {
				require.NotNil(t, maintHostOf(h).modal)
				assert.Equal(t, flowResult, maintHostOf(h).modal.stage, "a result no longer depends on the old bytes")
			},
		},
		{
			name: "pin review closes and keeps the draft",
			open: func(t *testing.T) *harness {
				h := pinsOpen(t, authorFake(nil), 120, 40)
				authorFill(h)
				h.press("s")
				require.NotNil(t, authorOf(h).review)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.Nil(t, authorOf(h).review)
				assert.Equal(t, "Lessons › Pins changed .seamark/lessons.yaml, so the pin review closed. Press s in the "+
					"pin form to review again.", h.m.toast.text)
				assert.NotNil(t, authorOf(h).newDraft, "the draft stays")
			},
		},
		{
			name: "settings lessons review closes and keeps the draft",
			open: func(t *testing.T) *harness {
				h := lessonsOpen(t, lessonsSnapFake(t, "threshold: 4\n"), "group:delivery", 120, 40)
				h.press("d", "s")
				require.True(t, settingsScr(h).flow.showsPreview())

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.False(t, settingsScr(h).flow.active())
				assert.Equal(t, 1, settingsScr(h).draftTotal(), "the draft stays")
				assert.Contains(t, h.m.toast.text, "so the Lesson delivery review closed")
			},
		},
		{
			name: "selection sample of a pin review closes with the review",
			open: func(t *testing.T) *harness {
				return samplePinOpen(t, authorFake(nil), 120, 40)
			},
			check: func(t *testing.T, h *harness) {
				assert.Nil(t, authorOf(h).review, "the sample read the old bytes, as its review did")
				assert.NotNil(t, authorOf(h).newDraft, "the draft stays")
			},
		},
		{
			name: "selection sample of a settings review closes with the review",
			open: func(t *testing.T) *harness {
				h := lessonsOpen(t, lessonsSnapFake(t, "threshold: 4\n"), "group:delivery", 120, 40)
				h.press("d", "s", "s")
				require.NotNil(t, settingsScr(h).flow.sample)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.Nil(t, settingsScr(h).flow.sample, "the sample read the old bytes, as its review did")
				assert.False(t, settingsScr(h).flow.active())
				assert.Equal(t, 1, settingsScr(h).draftTotal(), "the draft stays")
			},
		},
		{
			name: "evidence refresh preview stays",
			open: func(t *testing.T) *harness {
				h, les := evOpen(t, evFake(), 120, 40)
				evPress(h, les, evRefreshReviews)
				require.NotNil(t, les.evid.prev)

				return h
			},
			check: func(t *testing.T, h *harness) {
				assert.NotNil(t, hookLessons(h).evid.prev, "the evidence plan binds config.yaml, not lessons.yaml")
			},
			yCalls: "RunEvidence",
		},
	}
}

func TestLessonsSavedReceivers(t *testing.T) {
	for _, rc := range lessonsReceivers() {
		t.Run(rc.name, func(t *testing.T) {
			h := rc.open(t)

			h.send(pinsSaved)
			rc.check(t, h)

			// A closed preview never commits on a later y. A preview that
			// stays live still commits.
			f, ok := h.m.st.svc.(*fakeServices)
			require.True(t, ok)

			before := len(f.called())

			h.press("y")

			if rc.yCalls == "" {
				assert.Empty(t, f.called()[before:], "y calls nothing after the write")
			} else {
				assert.Equal(t, []string{rc.yCalls}, f.called()[before:], "the preview stays live")
			}
		})
	}
}

func TestLessonsSavedSenders(t *testing.T) {
	fx := maintFixture(t)

	maintCase := func(t *testing.T, res maintain.Result, err error) []lessonsSavedMsg {
		t.Helper()

		p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
		f := maintFakeOf(fx, p, func(*maintain.Plan) (maintain.Result, error) { return res, err })
		h := maintOpen(t, f, p.Request, 120, 40)
		probe := &settingsProbe{}
		h.m.screens[destDoctor] = probe

		h.press("y")
		require.Equal(t, 1, f.callCount("CommitMaintenance:retarget"))

		return probe.lessons
	}

	t.Run("maintenance written", func(t *testing.T) {
		got := maintCase(t, maintain.Result{Action: maintain.ActionRetarget, FileWritten: true, LedgerDone: true}, nil)
		require.Len(t, got, 1)
		assert.Equal(t, "Lessons › Pins", got[0].by)
		assert.False(t, got[0].at.IsZero())
	})

	t.Run("maintenance unknown", func(t *testing.T) {
		res := maintain.Result{Action: maintain.ActionRetarget, FileWritten: true,
			FileErr: errors.New("rename: disk full; the file could not be read after the failure")}
		assert.Len(t, maintCase(t, res, nil), 1, "an unchecked write may have changed the file")
	})

	t.Run("maintenance restored", func(t *testing.T) {
		res := maintain.Result{Action: maintain.ActionRetarget, FileWritten: true, Restored: true,
			LedgerErr: errors.New("store: database is locked")}
		assert.Empty(t, maintCase(t, res, nil), "the file holds the old bytes again")
	})

	t.Run("maintenance refused", func(t *testing.T) {
		assert.Empty(t, maintCase(t, maintain.Result{}, maintain.ErrStale), "a refusal writes nothing")
	})

	applyCase := func(t *testing.T, action string, res proposals.Result) []lessonsSavedMsg {
		t.Helper()

		f := pendingLedgerFake()
		f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
			res.Action = pv.Action

			return res, nil
		}

		h := lesOpen(t, f, 120, 40)
		probe := &settingsProbe{}
		h.m.screens[destDoctor] = probe

		h.press(action, "y")
		require.Len(t, f.called(), 2, "one preview and one commit")

		return probe.lessons
	}

	t.Run("apply written", func(t *testing.T) {
		got := applyCase(t, "a", proposals.Result{FileWritten: true, Moved: []int64{1}})
		require.Len(t, got, 1)
		assert.Equal(t, "Lessons › Proposals", got[0].by)
	})

	t.Run("apply written then failed", func(t *testing.T) {
		got := applyCase(t, "a", proposals.Result{FileWritten: true, FileErr: errors.New("cannot read again")})
		assert.Len(t, got, 1, "fileChanged counts an unread file as changed, so the outcome is unknown")
	})

	t.Run("apply not written", func(t *testing.T) {
		assert.Empty(t, applyCase(t, "a", proposals.Result{FileErr: errors.New("permission denied")}))
	})

	t.Run("dismiss", func(t *testing.T) {
		assert.Empty(t, applyCase(t, "d", proposals.Result{Moved: []int64{1}}), "a dismiss writes only the ledger")
	})
}

// pinsDocErr is a Pins inspection that cannot read the pin file.
func pinsDocErr() *maintain.Inspection {
	return &maintain.Inspection{DocErr: errors.New("open .seamark/lessons.yaml: permission denied")}
}

func TestRefreshOutcomeShowsTheRowAndCommitsOnce(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 120, 40)

	// The commit writes the file; the next read of it fails.
	f.pins = pinsDocErr()

	h.press("y")
	require.Equal(t, 1, f.callCount("CommitMaintenance:retarget"))

	want := "Saved: .seamark/lessons.yaml holds the reviewed bytes. Reading it again failed: open " +
		".seamark/lessons.yaml: permission denied. Nothing is retried; r reads again."

	mod := maintHostOf(h).modal
	require.NotNil(t, mod.out.check)
	assert.Equal(t, want, mod.out.check.text())
	assert.Contains(t, maintContent(t, h), want, "the result shows the row")
	assert.Contains(t, maintContent(t, h), "File written · ledger updated", "the outcome stays a success")
	assert.NotEqual(t, want, h.m.toast.text, "the open result shows the failure, so no message repeats it")

	// r reads again; nothing writes again.
	h.press("esc", "r")
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"), "the commit is called once")
	assert.Empty(t, h.m.st.checks, "the check ended with its reload")
	assert.NotEqual(t, want, h.m.toast.text, "a later read does not report the write again")

	// A good reload sets nothing.
	f2 := maintFakeOf(fx, p, nil)
	h2 := maintOpen(t, f2, p.Request, 120, 40)
	h2.press("y")
	assert.Empty(t, maintHostOf(h2).modal.out.check.failed)
	assert.NotContains(t, maintContent(t, h2), "Reading it again failed")
}

// refreshFailedRow is the start of the result row of a failed reload.
const refreshFailedRow = "Reading it again failed"

func TestRefreshOutcomeReadsAgainInTheOpenResult(t *testing.T) {
	fx := maintFixture(t)
	p := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})
	f := maintFakeOf(fx, p, nil)
	h := maintOpen(t, f, p.Request, 120, 40)

	good := f.pins
	f.pins = pinsDocErr()

	h.press("y")
	require.Contains(t, maintContent(t, h), refreshFailedRow)

	// The row says "r reads again", so r acts while the result shows it.
	reads := f.readCount("Pins")

	h.press("r")
	assert.Equal(t, reads+1, f.readCount("Pins"), "r reads the pins again")
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"), "r never runs the commit again")
	require.NotNil(t, maintHostOf(h).modal, "the result stays open")
	assert.Contains(t, maintContent(t, h), refreshFailedRow, "the read failed again, so the row stays")
	assert.NotContains(t, h.m.toast.text, refreshFailedRow, "the open result shows the failure; no message repeats it")

	// A read that succeeds removes the row.
	f.pins = good

	h.press("r")
	assert.NotContains(t, maintContent(t, h), refreshFailedRow)
	assert.Equal(t, 1, f.callCount("CommitMaintenance:retarget"))
}

func TestRefreshOutcomeOfAnApply(t *testing.T) {
	f := pendingLedgerFake()
	f.commitDecision = func(pv *proposals.Preview) (proposals.Result, error) {
		return proposals.Result{Action: pv.Action, FileWritten: true, Moved: []int64{1}}, nil
	}

	h := lesOpen(t, f, 120, 40)
	good := f.pins

	h.press("space", "a")
	f.pins = pinsDocErr()
	h.press("y")

	want := "Saved: .seamark/lessons.yaml holds the reviewed bytes. Reading it again failed: open " +
		".seamark/lessons.yaml: permission denied. Nothing is retried; r reads again."
	assert.Contains(t, lesModalText(t, h), want, "the apply result shows the row")
	assert.NotEqual(t, want, h.m.toast.text, "the open result shows the failure; no message repeats it")

	f.pins = good

	h.press("r")
	assert.NotContains(t, lesModalText(t, h), refreshFailedRow, "r reads again, and the read succeeds")
	assert.Equal(t, 1, f.callCount("CommitDecision:apply"), "r never applies again")
}

func TestRefreshOutcomeIgnoresAnOlderTicket(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 40)

	// A load starts before the write lands and holds the old state.
	early := h.m.st.load(secPins)

	c, _ := h.m.st.watchRefresh(pinfile.File, nil, secPins)
	f.pins = pinsDocErr()

	h.run(early)
	assert.Empty(t, c.failed, "a load of an older ticket never sets the failure")
	assert.Len(t, h.m.st.checks, 1, "the check still waits for the reload")
	assert.Empty(t, h.m.toast.text)

	// The reload after the write fails, and no result shows, so a
	// message says so.
	h.run(h.m.st.load(secPins))
	assert.Equal(t, "open .seamark/lessons.yaml: permission denied", c.failed)
	assert.Equal(t, c.text(), h.m.toast.text)
	assert.Empty(t, h.m.st.checks)
}

func TestRefreshOutcomeWatchesOnlyTheSectionsOfTheFile(t *testing.T) {
	f := newFake()
	h := newHarness(t, f, 120, 40)

	// A config.yaml save also reloads Pins, but a broken pin file says
	// nothing about config.yaml.
	cfg, _ := h.m.st.watchRefresh(settingsFile, nil, secSettings, secPins, secIndex)
	assert.Equal(t, []sectionKind{secSettings}, mapKeys(cfg.after))

	lessons, _ := h.m.st.watchRefresh(pinfile.File, nil, secSettings, secPins, secIndex)
	assert.ElementsMatch(t, []sectionKind{secSettings, secPins}, mapKeys(lessons.after))

	none, _ := h.m.st.watchRefresh(pinfile.File, nil, secLedger, secStats)
	assert.Empty(t, none.after)
	assert.Len(t, h.m.st.checks, 2, "a check with no section is not kept")

	// The settings rule: an error, or no reading of lessons.yaml.
	f.settings, f.settingsErr = nil, errors.New("settings: workspace root is empty")
	f.pins = pinsDocErr()
	h.run(h.m.st.load(secSettings, secPins))

	assert.Equal(t, "settings: workspace root is empty", cfg.failed)
	assert.Equal(t, "settings: workspace root is empty", lessons.failed, "the first failure wins")
	assert.Contains(t, h.m.toast.text, "Reading it again failed")

	f.settingsErr = nil
	assert.Equal(t, "the settings read returned no snapshot", reloadFailure(loadedMsg{kind: secSettings}, pinfile.File))
	assert.Empty(t, reloadFailure(loadedMsg{kind: secPins, value: fakePins()}, pinfile.File))
	assert.Equal(t, ".seamark/lessons.yaml could not be read: bad yaml",
		reloadFailure(loadedMsg{kind: secPins, value: &maintain.Inspection{Doc: &pinfile.Document{
			DecodeErr: errors.New("bad yaml"),
		}}}, pinfile.File))
}

// mapKeys returns the keys of m in no order.
func mapKeys(m map[sectionKind]int) []sectionKind {
	out := make([]sectionKind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}

	return out
}
