package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/settings"
)

// The cross-screen flow tests of Phase 4 (contracts §7, C1 flows a–g).
// Each screen has its own tests: pins_author_test.go, the settings
// tests, sample_test.go, shell_drafts_test.go and state_saved_test.go.
// These tests drive one console across several screens, so they cover
// only the parts that no single screen test reaches.

// p4Fake returns the services of one console with the Phase 4 screens.
// Settings inspects, plans and saves lessons.yaml for real (lessonsFake),
// so a test can check the bytes. Pins lists pinsInspection, and the pin
// plans come from authorFake, so a test sets only the plan fields that
// it reads.
func p4Fake(t *testing.T, lessons string) *settingsSvc {
	t.Helper()

	svc := lessonsFake(t, lessons)
	svc.ledger = lesLedger()
	svc.pins = pinsInspection()
	svc.planAuthor = authorFake(nil).planAuthor

	return svc
}

// p4Visit presses the key of destination dest. Settings asks before the
// user leaves it with a draft (TestSettingsLeaveGuardAsksKeepDiscardOrStay).
// k keeps the draft, so the Phase 4 drafts stay.
func p4Visit(t *testing.T, h *harness, key string, dest destination) {
	t.Helper()

	h.press(key)

	if h.m.active == destSettings && dest != destSettings {
		require.Contains(t, h.view(), "Leave with an unsaved draft?")
		h.press("k")
	}

	require.Equal(t, dest, h.m.active)
}

// Flow (a): a mute save in Settings closes the pin review that waits in
// Lessons › Pins. TestLessonsSavedReceivers sends a made-up message to
// the review; here the message comes from a real save. The review
// captures every key, so the test opens Settings with the navigation
// message of a link. The broadcast does not depend on the screen that
// shows.
func TestP4FlowMuteSaveClosesAnOpenPinReview(t *testing.T) {
	svc := p4Fake(t, muteFile)
	probe := &settingsProbe{}

	h := newHarness(t, svc, 120, 40)
	h.m.screens[destDoctor] = probe
	h.press("3", "m")
	authorFill(h)
	h.press("s")
	require.NotNil(t, authorOf(h).review)

	draft := authorOf(h).newDraft
	want := authorReq(t, draft)

	h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
	require.Equal(t, destSettings, h.m.active)

	h.press("enter", "a")
	h.typeText("W1")
	h.press("enter", "s", "y")
	require.Equal(t, 1, svc.callCount("SaveSettings"))
	require.Contains(t, lessonsBytes(t, svc), "rule: W1")

	require.Len(t, probe.lessons, 1, "one save sends one broadcast")
	assert.Equal(t, "Settings › lesson mutes", probe.lessons[0].by)
	assert.Nil(t, authorOf(h).review, "the review read the old bytes, so it closed")
	assert.Equal(t, "Settings › lesson mutes changed .seamark/lessons.yaml, so the pin review closed. Press s "+
		"in the pin form to review again.", h.m.toast.text, "the toast shows on Settings, so it names the pin form")
	assert.Same(t, draft, authorOf(h).newDraft, "the draft stays")
	assert.Equal(t, want, authorReq(t, draft), "the draft keeps every field")

	// Back in Pins the form waits with the draft, and only a fresh review
	// plans again. The call list holds no CommitAuthor.
	h.press("esc", "3")
	require.Equal(t, destLessons, h.m.active)
	require.NotNil(t, authorOf(h).form)

	h.press("s")
	require.NotNil(t, authorOf(h).review)
	assert.Equal(t, []string{"PlanAuthor:create", "PlanSettings", "SaveSettings", "PlanAuthor:create"}, svc.called())
}

// Flow (b): one pin create closes every preview that read the old
// lessons.yaml, in every sub-view at once, and the evidence preview
// stays. TestLessonsSavedReceivers proves each holder alone with a
// made-up message. Here one console holds them all, and the message
// comes from the commit of the pin review.
//
// The Run sub-view holds one record, so the distillation plan and the
// extraction review take turns. The apply, evidence and maintenance
// previews capture every key, so the test moves between sub-views with
// the messages of links. The maintenance preview opens under the pin
// review through openMaintMsg; the keys still reach the pin review
// first.
func TestP4FlowPinCreateClosesTheBoundPreviewsAndKeepsTheEvidencePreview(t *testing.T) {
	cases := []struct {
		name string
		// open puts the record in the Run sub-view.
		open func(t *testing.T, h *harness)
		// check asserts the record after the write: it cannot run, and
		// it keeps its input.
		check func(t *testing.T, h *harness)
		calls []string
	}{
		{
			name: "distillation plan",
			open: func(t *testing.T, h *harness) {
				h.send(navigateMsg{dest: destLessons, intent: "distill"})
				distRecOf(t, h).form.region.Set("internal/api")
				h.press("enter")
				require.True(t, distRecOf(t, h).runnable())
			},
			check: func(t *testing.T, h *harness) {
				rec := distRecOf(t, h)
				assert.Equal(t, stepScope, rec.step, "the plan dropped to its scope")
				assert.Equal(t, "internal/api", rec.form.region.Value(), "the scope keeps its input")
			},
			calls: []string{"PlanDistill"},
		},
		{
			name: "extraction review",
			open: func(t *testing.T, h *harness) {
				h.send(openExtractMsg{req: extPreview(extMixed()).Request})
				require.True(t, extRecOf(t, h).runnable())
			},
			check: func(t *testing.T, h *harness) {
				rec := extRecOf(t, h)
				assert.IsType(t, extSavedStale{}, rec.prev.stale, "the write marks the preview out of date")
				assert.False(t, rec.runnable())
				assert.NotEmpty(t, rec.req.IDs, "the request stays")
			},
			calls: []string{"PlanExtract"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := maintFixture(t)
			plan := fx.maintNarrow(t, maintPlanOpts{write: true, cursor: true})

			// One fake serves every screen. Each part comes from the
			// fixture of its own screen.
			f := maintFakeOf(fx, plan, nil)
			f.previewDecision = pendingLedgerFake().previewDecision
			f.planAuthor = authorFake(nil).planAuthor
			f.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
				return maintain.AuthorResult{Outcome: maintain.Written}, nil
			}

			dist := distFake(distFullPreview(), distNoRun(t))
			f.planDistill, f.runDistill = dist.planDistill, dist.runDistill

			ext := extFake(extPreview(extMixed()), extNoRun(t))
			f.planExtract, f.runExtract = ext.planExtract, ext.runExtract

			ev := evFake()
			f.learning, f.planEvidence = ev.learning, ev.planEvidence

			h := newHarness(t, f, 120, 40)
			les := hookLessons(h)

			tc.open(t, h)

			h.send(navigateMsg{dest: destLessons, intent: "evidence"})
			evPress(h, les, evRefreshReviews)
			require.NotNil(t, les.evid.prev)

			h.send(navigateMsg{dest: destLessons, intent: "proposals"})
			h.press("space", "a")
			require.NotNil(t, les.props.dec)

			h.send(navigateMsg{dest: destLessons, intent: "pins"})
			authorFill(h)
			h.press("s")
			require.NotNil(t, authorOf(h).review)

			h.send(openMaintMsg{req: plan.Request})
			require.NotNil(t, maintHostOf(h).modal)

			// y reaches the pin review, which writes the new pin.
			h.press("y")
			require.Equal(t, 1, f.callCount("CommitAuthor:create"))
			assert.Nil(t, authorOf(h).newDraft, "the written draft goes")

			assert.Nil(t, les.props.dec, "the apply preview closed")
			assert.NotEmpty(t, les.props.selected, "the apply selection stays")
			assert.Nil(t, maintHostOf(h).modal, "the maintenance preview closed")
			tc.check(t, h)
			assert.NotNil(t, les.evid.prev, "the evidence plan binds config.yaml, not lessons.yaml")

			// The evidence preview still runs. No closed preview commits.
			h.press("enter", "e", "y")
			assert.Equal(t, 1, f.callCount("RunEvidence"))

			want := append(append([]string(nil), tc.calls...), "PlanEvidence:"+index.SourcesReviewsAndLocal.String(),
				"PreviewDecision:apply", "PlanAuthor:create", "PlanMaintenance:retarget", "CommitAuthor:create",
				"RunEvidence")
			assert.Equal(t, want, f.called())
		})
	}
}

// Flow (c): the gate route of a pin review and a real save of Pin
// writing. TestPinReviewRoutesTheGateToSettings covers the o route and
// a made-up save message. Here the plan reads distill.write from the
// file at plan time, as the engine does. Only a fresh review after the
// save reads the new gate, so only that review can write.
func TestP4FlowGateOffRouteSavesPinWritingAndTheNextReviewWrites(t *testing.T) {
	var got *maintain.AuthorPlan

	svc := p4Fake(t, "")
	settingsWrite(t, svc.root, "distill:\n  write: false\n")
	svc.planAuthor = authorFake(func(p *maintain.AuthorPlan) {
		data, err := os.ReadFile(filepath.Join(svc.root, filepath.FromSlash(settings.File)))
		require.NoError(t, err)

		if strings.Contains(string(data), "write: true") {
			return
		}

		p.Write, p.Confirmable = false, false
		p.Blockers = []maintain.AuthorBlocker{{
			Kind: maintain.BlockGate, Text: "distill.write is off: this preview changes nothing",
			Route: "turn on Settings › Pin writing and preview again",
		}}
	}).planAuthor
	svc.commitAuthor = func(p *maintain.AuthorPlan) (maintain.AuthorResult, error) {
		got = p

		return maintain.AuthorResult{Outcome: maintain.Written}, nil
	}

	h := newHarness(t, svc, 120, 40)
	h.press("3", "m")
	authorFill(h)
	h.press("s")

	first := authorOf(h).review
	require.NotNil(t, first)
	assert.Contains(t, authorReviewFlat(t, h), "y is off: distill.write is off")

	h.press("y")
	assert.Zero(t, svc.callCount("CommitAuthor:create"), "y does nothing while the gate is off")

	// o closes the review and opens Settings › Pin writing; the save
	// there is the ordinary Settings save.
	h.press("o")
	require.Equal(t, destSettings, h.m.active)
	assert.Nil(t, authorOf(h).review, "a review of the old gate closes")

	h.press("enter", "down", "space", "enter", "s", "y")
	require.Equal(t, "distill:\n  write: true\n", settingsBytes(t, svc))

	// Back in Pins the form waits with its draft, and no review of the
	// old gate is left.
	h.press("3")
	require.Equal(t, destLessons, h.m.active)
	require.NotNil(t, authorOf(h).form)
	assert.Nil(t, authorOf(h).review)

	h.press("s")

	fresh := authorOf(h).review
	require.NotNil(t, fresh)
	assert.NotSame(t, first, fresh, "s builds a new review")
	require.True(t, fresh.preview.Write, "the fresh plan reads the new gate")

	h.press("y")
	require.NotNil(t, got)
	assert.True(t, got.Write, "the commit gets the fresh plan")
	assert.Equal(t, []string{
		"PlanAuthor:create", "PlanSettings", "SaveSettings", "PlanAuthor:create", "CommitAuthor:create",
	}, svc.called())
}

// Flow (d): an evidence refresh runs while a pin draft, a mute draft and
// a delivery draft wait. The user moves through every destination and
// back, and each draft keeps every field. No draft saves while the run
// holds the mutation slot, and the pin review commits after the run.
// TestSettingsSaveWaitsForARunningOperation covers a config.yaml draft;
// TestConsoleFlowExtractionRunKeepsReadsAndRefusesOtherWrites covers
// the Phase 3 writes during a run.
func TestP4FlowDraftsSurviveNavigationWhileAnEvidenceRefreshRuns(t *testing.T) {
	const lessons = "threshold: 4\nmute:\n  - rule: F541\n"

	svc := p4Fake(t, lessons)
	ev := evFake()
	svc.learning, svc.planEvidence = ev.learning, ev.planEvidence
	svc.commitAuthor = func(*maintain.AuthorPlan) (maintain.AuthorResult, error) {
		return maintain.AuthorResult{Outcome: maintain.Written}, nil
	}

	h := newHarness(t, svc, 120, 40)
	les := hookLessons(h)
	scr := settingsScr(h)

	// The pin draft; esc closes its form and keeps it.
	h.press("3", "m")
	authorFill(h)
	h.press("esc")
	require.False(t, authorOf(h).open())

	pinWant := authorReq(t, authorOf(h).newDraft)

	// The delivery draft resets threshold; the mute draft adds W1.
	h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
	h.press("d")
	h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
	h.press("enter", "a")
	h.typeText("W1")
	h.press("enter", "esc")
	require.Equal(t, 2, scr.draftTotal())

	// The want states what the user typed. A clone of scr.edits shares
	// the MuteEdit pointer. A change of the mute draft in place then
	// changes the want too. No check can see that change.
	edits := map[settings.Field]settings.Edit{
		settings.Threshold: {Field: settings.Threshold, Reset: true},
		settings.Mutes:     {Field: settings.Mutes, Mute: &settings.MuteEdit{Op: settings.MuteAdd, Rule: "W1"}},
	}
	require.Equal(t, edits, scr.edits)

	// intact asserts that each draft holds what the user typed.
	intact := func(t *testing.T, where string) {
		t.Helper()

		require.NotNil(t, authorOf(h).newDraft, "%s: the pin draft stays", where)
		assert.Equal(t, pinWant, authorReq(t, authorOf(h).newDraft), "%s: the pin draft keeps every field", where)
		assert.Equal(t, edits, scr.edits, "%s: the Settings drafts keep every field", where)
	}

	// visit opens a destination; the run notice shows on each one.
	visit := func(t *testing.T, key string, dest destination) {
		t.Helper()

		p4Visit(t, h, key, dest)
		assert.Contains(t, noticeRow(h), "Refreshing evidence", "the run shows on %v", dest)
	}

	// evStarted opens the run record and holds its worker, so the run
	// holds the slot while the test moves around (evidence_run_test.go).
	h.press("3", "k", "e")
	evStarted(t, h, les, index.SourcesReviewsAndLocal)
	require.Equal(t, "Refreshing evidence", h.m.st.mutation)

	for _, step := range []struct {
		key  string
		dest destination
	}{
		{"1", destOverview}, {"2", destIntegrations}, {"3", destLessons}, {"4", destDoctor},
		{"5", destSettings}, {"1", destOverview}, {"5", destSettings}, {"3", destLessons},
	} {
		visit(t, step.key, step.dest)
		intact(t, "after "+step.key)
	}

	// Every Lessons sub-view and back to Pins.
	h.press("p", "s", "e", "u", "m")
	require.Equal(t, lesPins, les.sub)
	intact(t, "after the sub-views")

	// The Settings drafts wait: s plans nothing during the run.
	visit(t, "5", destSettings)

	for _, intent := range []string{"group:delivery", "group:mutes"} {
		h.send(navigateMsg{dest: destSettings, intent: intent})
		h.press("s")
		assert.Equal(t, "Saving waits for the running operation: Refreshing evidence.", h.m.toast.text, intent)
	}

	// The pin review reads its plan, but y waits.
	visit(t, "3", destLessons)
	h.press("n", "s")
	require.NotNil(t, authorOf(h).review)
	assert.Contains(t, authorReviewFlat(t, h), "y waits: another operation is running")

	h.press("y")
	assert.Zero(t, svc.callCount("CommitAuthor:create"), "y commits nothing during the run")
	assert.NotContains(t, h.m.toast.text, "Another operation is running",
		"the review says that y waits, so y starts no commit that the slot then refuses")
	assert.Zero(t, svc.callCount("PlanSettings"), "no Settings review starts during the run")
	assert.True(t, les.evid.run.active(), "moving around never stops the run")
	intact(t, "at the end of the run")

	// The run ends. The same review commits once the slot is free.
	h.send(evDoneMsg{id: 1, out: learning.Outcome{After: evSnap(), FinishedAt: time.Now()}})
	require.False(t, h.m.st.busy())
	require.NotNil(t, authorOf(h).review)

	h.press("y")
	assert.Equal(t, 1, svc.callCount("CommitAuthor:create"))
	assert.Equal(t, edits, scr.edits, "the Settings drafts outlive the run and the pin write")
	assert.Equal(t, lessons, lessonsBytes(t, svc), "no Settings draft saved by itself")
}

// Flow (e): a save of Lesson delivery keeps the mute draft of the same
// file. The next review of that draft plans against the new bytes.
// The new delivery key goes to the document end, right after the drafted
// mute entry. The draft binds the text of its entry, so a new line next
// to the entry must not break that binding.
// TestSettingsMuteDraftOfAChangedEntryIsFlagged covers a real change of
// the entry.
func TestP4FlowDeliverySaveKeepsTheMuteDraftAndItsReviewPlansAgain(t *testing.T) {
	const before = "mute:\n  - rule: F541\n  - region: api/\n"

	svc := p4Fake(t, before)
	h := lessonsOpen(t, svc, "group:mutes", 120, 40)
	scr := settingsScr(h)

	// The mute draft removes the last entry. Its first review reads the
	// file as it is now.
	h.press("enter", "down", "x", "s")
	require.True(t, scr.flow.showsPreview())
	require.Equal(t, before, string(scr.flow.preview.Bytes.Before))

	h.press("esc", "esc")
	require.Equal(t, settings.MuteRemove, scr.muteDraft().Op)

	// A delivery save writes the same file.
	h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
	h.press("enter", "down", "space")
	h.send(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.typeText("5")
	h.press("enter", "enter", "s", "y")

	saved := lessonsBytes(t, svc)
	require.Equal(t, before+"\nthreshold: 5\n", saved, "the new key goes to the document end")

	// The mute draft stays, and its review plans again from the new bytes.
	assert.Equal(t, 1, scr.draftCount(settings.GroupMutes), "the delivery save keeps the mute draft")
	assert.Zero(t, scr.draftCount(settings.GroupDelivery))

	h.send(navigateMsg{dest: destSettings, intent: "group:mutes"})
	h.press("enter", "s")
	require.True(t, scr.flow.showsPreview(), "the draft still binds its entry")
	assert.Equal(t, saved, string(scr.flow.preview.Bytes.Before), "the review reads the file after the save")

	// The save holds both changes: the entry goes and the key stays.
	h.press("y")
	assert.Equal(t, "mute:\n  - rule: F541\n\nthreshold: 5\n", lessonsBytes(t, svc))
	assert.Zero(t, scr.draftTotal())
	assert.Equal(t, []string{"PlanSettings", "PlanSettings", "SaveSettings", "PlanSettings", "SaveSettings"}, svc.called())
}

// Flow (f): a quit with a pin draft and a Settings draft of the same
// file. TestPinDraftsNameTheFileAtQuit and
// TestSettingsLessonsDraftFramesNameNoConfigFile cover each real holder
// alone; TestQuitNamesEachFileWhoseDraftIsLost covers the wording of
// every holder set with stub holders. Here two real holders name one
// file, so the question names it once.
func TestP4FlowQuitNamesTheLessonsFileOnceForTwoHolders(t *testing.T) {
	const lessons = "threshold: 4\n"

	svc := p4Fake(t, lessons)
	h := lessonsOpen(t, svc, "group:delivery", 120, 40)

	// A delivery draft, then a pin draft. k keeps the Settings draft at
	// the leave question.
	h.press("d", "3", "k", "m", "n", "enter")
	h.typeText("x")
	h.press("esc", "esc")
	require.Equal(t, []string{pinfile.File}, settingsScr(h).drafts())
	require.Equal(t, []string{pinfile.File}, pinsOf(h).drafts())

	h.press("q")
	require.True(t, h.m.quitConfirm)

	title, body, _ := h.m.quitCopy(false)
	assert.Equal(t, "Discard the unsaved drafts and quit?", title)
	assert.Equal(t, "Quitting discards every unsaved draft of .seamark/lessons.yaml. The file does not change.", body)

	// esc keeps both drafts; enter drops both and quits.
	h.press("esc")
	assert.NotNil(t, authorOf(h).newDraft)
	assert.Equal(t, 1, settingsScr(h).draftTotal())

	h.press("q", "enter")
	assert.True(t, h.quit)
	assert.Nil(t, authorOf(h).newDraft)
	assert.Zero(t, settingsScr(h).draftTotal())
	assert.Empty(t, svc.called(), "drafting and quitting plan and write nothing")
	assert.Equal(t, lessons, lessonsBytes(t, svc))
}

// p4KeyRow is one earlier shortcut of TestP4FlowEarlierKeysKeepTheirMeaning.
// The row starts on Overview. intent, when set, opens Settings at a
// group, as an Overview link does. keys follow; the last key is the
// shortcut. call is the service call that the shortcut starts, or ""
// for none. check asserts the state that it leaves, or is nil.
type p4KeyRow struct {
	name   string
	intent string
	keys   []string
	call   string
	check  func(t *testing.T, h *harness)
}

// Flow (g), AC-1: the earlier keys keep their actions while Phase 4
// drafts wait. The rows cover destinations, Lessons sub-views, Run
// history, decisions, list marks, maintenance, extraction, the index
// build and settings groups. The screen tests prove each key alone. This
// walk runs them in one console that holds a pin draft and a lessons
// Settings draft.
// TestListPaneKeysStayApart proves that the new Pins keys take no pane
// key. TestPinsAndProposalsContextSurvivesARoundTrip proves the list
// context across a round trip.
func TestP4FlowEarlierKeysKeepTheirMeaning(t *testing.T) {
	svc := p4Fake(t, "threshold: 4\n")

	// p3 matches pin 6, so c can record it, as in
	// TestConsoleFlowPendingMatchRouteThroughProposalsLinksThePin.
	p3 := svc.pins.Pins[5].Link.Pending[0]
	svc.ledger.Pending[2] = p3
	svc.pins.Links.ByProposal[p3.ID] = pinfile.LinkPendingMatch

	h := newHarness(t, svc, 120, 40)
	les := hookLessons(h)
	scr := settingsScr(h)

	// The Phase 4 drafts: a new pin and a delivery reset.
	h.press("3", "m")
	authorFill(h)
	h.press("esc")
	h.send(navigateMsg{dest: destSettings, intent: "group:delivery"})
	h.press("d")

	pinWant := authorReq(t, authorOf(h).newDraft)
	delivery := scr.edits[settings.Threshold]

	// Two finished runs give the Run history.
	older, newer := runRec(h, "Distillation 13:00"), runRec(h, "Distillation 14:05")
	h.send(openRunMsg{rec: older})
	h.send(openRunMsg{rec: newer})

	dest := func(want destination) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) {
			t.Helper()
			assert.Equal(t, want, h.m.active)
		}
	}

	sub := func(want lesSub) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) {
			t.Helper()
			assert.Equal(t, want, les.sub)
		}
	}

	shown := func(want runRecord) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) {
			t.Helper()
			assert.Same(t, want, runViewOf(h).shown())
		}
	}

	group := func(g settings.Group) func(t *testing.T, h *harness) {
		return func(t *testing.T, h *harness) {
			t.Helper()
			assert.Equal(t, g, scr.field().Group())
			assert.True(t, scr.editing, "enter edits the first field of the group")
		}
	}

	// The rows start the Lessons switch keys in Pins, where n, i and d
	// are new. Pin 2 (p9) offers every maintenance action; p1 and p3 are
	// pending proposals.
	pin2 := []string{"3", "m", "home", "down"}
	p1 := []string{"3", "p", "home"}
	at := func(path []string, key ...string) []string { return append(slices.Clone(path), key...) }

	rows := []p4KeyRow{
		{name: "1 Overview", keys: []string{"1"}, check: dest(destOverview)},
		{name: "2 Integrations", keys: []string{"2"}, check: dest(destIntegrations)},
		{name: "3 Lessons", keys: []string{"3"}, check: dest(destLessons)},
		{name: "4 Doctor", keys: []string{"4"}, check: dest(destDoctor)},
		{name: "5 Settings", keys: []string{"5"}, check: dest(destSettings)},
		{name: "m pins", keys: []string{"3", "p", "m"}, check: sub(lesPins)},
		{name: "p proposals", keys: []string{"3", "m", "p"}, check: sub(lesProposals)},
		{name: "s stats", keys: []string{"3", "m", "s"}, check: sub(lesStats)},
		{name: "e evidence", keys: []string{"3", "m", "e"}, check: sub(lesEvidence)},
		{name: "u run", keys: []string{"3", "m", "u"}, check: sub(lesRun)},
		{name: "[ earlier run", keys: []string{"3", "u", "["}, check: shown(older)},
		{name: "] later run", keys: []string{"3", "u", "[", "]"}, check: shown(newer)},
		{name: "a apply", keys: at(p1, "a"), call: "PreviewDecision:apply"},
		{name: "d dismiss", keys: at(p1, "d"), call: "PreviewDecision:dismiss"},
		{name: "c record as applied", keys: at(p1, "down", "down", "c"), call: "PreviewDecision:reconcile"},
		{name: "x clears the selection", keys: at(p1, "space", "x"), check: func(t *testing.T, h *harness) {
			assert.Empty(t, les.props.selected)
		}},
		// In the pin form x discards the draft, so the list x shares a key
		// with a Phase 4 action. The toast proves that space marked a pin.
		{name: "x clears the marks in Pins", keys: at(pin2, "space", "x"), check: func(t *testing.T, h *harness) {
			assert.Empty(t, pinsOf(h).selected)
			assert.Equal(t, "Selection cleared.", h.m.toast.text, "x cleared a mark, not the pin draft")
		}},
		{name: "w retarget", keys: at(pin2, "w"), call: "PlanMaintenance:retarget"},
		{name: "- prune", keys: at(pin2, "-"), call: "PlanMaintenance:prune"},
		{name: "t extract in Pins", keys: at(pin2, "t"), call: "PlanExtract"},
		{name: "t extract in Proposals", keys: at(p1, "t"), call: "PlanExtract"},
		{name: "b build index", keys: []string{"b", "enter"}, call: "BuildIndex"},
		{name: "group inference", intent: "group:inference", keys: []string{"enter"}, check: group(settings.GroupInference)},
		{name: "group pin writing", intent: "group:pinwrite", keys: []string{"enter"}, check: group(settings.GroupPinWrite)},
		{name: "group evidence", intent: "group:evidence", keys: []string{"enter"}, check: group(settings.GroupEvidence)},
		{name: "group indexing", intent: "group:indexing", keys: []string{"enter"}, check: group(settings.GroupIndexing)},
		{name: "s reviews a config draft", intent: "group:inference", keys: []string{"enter", "down", "space", "enter", "s"},
			call: "PlanSettings", check: func(t *testing.T, h *harness) {
				assert.Contains(t, h.view(), "Save .seamark/config.yaml")
			}},
		{name: "x discards the config draft", intent: "group:inference",
			keys: []string{"enter", "down", "space", "enter", "x", "d"}, check: func(t *testing.T, h *harness) {
				assert.Zero(t, scr.draftCount(settings.GroupInference), "the row drafts agent.cli, and x d discards it")
			}},
	}

	// destOf holds the destination keys of the shell, which the walk
	// presses through p4Visit to answer the leave question of Settings.
	destOf := map[string]destination{
		"1": destOverview, "2": destIntegrations, "3": destLessons, "4": destDoctor, "5": destSettings,
	}

	for _, row := range rows {
		p4Visit(t, h, "1", destOverview)

		before := len(svc.called())

		if row.intent != "" {
			h.send(navigateMsg{dest: destSettings, intent: row.intent})
		}

		for _, k := range row.keys {
			if d, ok := destOf[k]; ok {
				p4Visit(t, h, k, d)
			} else {
				h.press(k)
			}
		}

		if row.call == "" {
			assert.Empty(t, svc.called()[before:], "%s: the key starts no service call", row.name)
		} else {
			assert.Equal(t, []string{row.call}, svc.called()[before:], "%s: the key starts its earlier action", row.name)
		}

		if row.check != nil {
			row.check(t, h)
		}

		// Close what the key opened; the next row starts from Overview.
		for range 3 {
			if !h.m.screens[h.m.active].capturing() && !scr.editing {
				break
			}

			h.press("esc")
		}

		assert.Equal(t, pinWant, authorReq(t, authorOf(h).newDraft), "%s: the pin draft stays", row.name)
		assert.Equal(t, delivery, scr.edits[settings.Threshold], "%s: the delivery draft stays", row.name)
	}

	assert.Zero(t, svc.callCount("PlanAuthor:create"), "no earlier key reaches the pin authoring")
	assert.Zero(t, svc.callCount("SaveSettings"), "no earlier key saves a draft")
}
