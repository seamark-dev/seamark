package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	smodel "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovlEvidence returns the evidence snapshot of a ready index with the
// given fix and review finding counts.
func ovlEvidence(fixes, reviews int) learning.Snapshot {
	return learning.Snapshot{
		Probe:       workspace.IndexProbe{State: workspace.IndexReady},
		Findings:    map[string]int{"fix:conventional": fixes, smodel.SourceReview: reviews},
		FixFindings: fixes, ReviewFindings: reviews,
		ObservedAt: time.Now(),
	}
}

// ovlHas reports whether the attention list holds an item with the key.
func ovlHas(o *overview, key string) bool {
	for _, it := range o.attention() {
		if it.key == key {
			return true
		}
	}

	return false
}

// ovlLearnKeys returns the keys of the learning items, in list order.
func ovlLearnKeys(o *overview) []string {
	var keys []string

	for _, it := range o.attention() {
		if strings.HasPrefix(it.key, "learn:") {
			keys = append(keys, it.key)
		}
	}

	return keys
}

// ovlRecorder replaces a destination with a stub that records the open
// intents, so a test sees the exact intent of a link.
func ovlRecorder(h *harness, dest destination) *frIntentScreen {
	stub := &frIntentScreen{}
	h.m.screens[dest] = stub

	return stub
}

// ovlLessons returns the Lessons destination of a console.
func ovlLessons(h *harness) *lessons {
	return h.m.screens[destLessons].(*lessons)
}

func TestOverviewLinksNoEvidenceToEvidence(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(0, 0)

	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	it := ovFind(t, o, "learn:evidence")
	assert.Equal(t, ovOptional, it.group, "collecting evidence is a choice, not a problem")
	assert.Equal(t, ovNav(destLessons, "evidence", "collect evidence"), it.act)

	ovSelect(t, h, "learn:evidence")

	frame := h.view()
	assertFrame(t, frame, 120, 40)
	assert.Regexp(t, `· No evidence collected yet +Lessons ›`, frame)
	assert.Contains(t, frame, "enter collect evidence")
	assert.Contains(t, docFlat(frame), "seamark index --fixes-only mines local fix commits.")

	h.press("enter")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesEvidence, ovlLessons(h).sub)
	assert.Empty(t, f.called(), "the link opens a view; it plans and runs nothing")
}

func TestOverviewLinksFindingsWithoutProposalsToDistill(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.ledger = &proposals.Ledger{}

	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	it := ovFind(t, o, "learn:distill")
	assert.Equal(t, ovOptional, it.group)
	assert.Equal(t, "12 findings, no proposals in the ledger", it.title)
	assert.Equal(t, "no proposals", it.badge)
	assert.Equal(t, ovNav(destLessons, "distill", "prepare a distillation"), it.act)
	assert.Contains(t, it.body[0], "8 from fix commits, 4 from GitHub reviews")
	assert.False(t, ovlHas(o, "learn:evidence"), "stored findings are evidence")

	ovSelect(t, h, "learn:distill")
	assert.Contains(t, docFlat(docRight(h.view())), "nothing is sent before you confirm its preview")

	h.press("enter")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesRun, ovlLessons(h).sub, "the link opens the scope step of a distillation")
	assert.Zero(t, f.callCount("RunDistill"), "the link starts no run")
	assert.Zero(t, f.callCount("RunEvidence"))
}

func TestOverviewLinksNoRepeatedDistillationSuggestion(t *testing.T) {
	cases := map[string]func(f *fakeServices){
		"a proposal was applied before": func(f *fakeServices) {
			f.ledger = &proposals.Ledger{Applied: []smodel.Proposal{{ID: 1, Rule: "a", Status: "applied"}}}
		},
		"a proposal was dismissed before": func(f *fakeServices) {
			f.ledger = &proposals.Ledger{Dismissed: []smodel.Proposal{{ID: 1, Rule: "a", Status: "dismissed"}}}
		},
		"the ledger read failed": func(f *fakeServices) {
			f.ledger, f.ledgerErr = nil, errors.New("database is locked")
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := ovPopulated()
			f.learning = ovlEvidence(8, 4)
			mutate(f)

			h := newHarness(t, f, 120, 40)
			assert.Empty(t, ovlLearnKeys(ovScreen(h)), "only a known, empty ledger suggests a first distillation")
		})
	}
}

func TestOverviewLinksUnknownEvidenceSuggestsNothing(t *testing.T) {
	cases := map[string]func(f *fakeServices){
		"the evidence has not loaded": func(f *fakeServices) {},
		"the counts are unknown": func(f *fakeServices) {
			f.learning = ovlEvidence(0, 0)
			f.learning.Findings = nil
			f.learning.CountsErr = errors.New("database is locked")
		},
		"the index is absent": func(f *fakeServices) {
			f.learning = ovlEvidence(0, 0)
			f.learning.Probe.State = workspace.IndexAbsent
		},
		"the index is older": func(f *fakeServices) {
			f.learning = ovlEvidence(0, 0)
			f.learning.Probe.State = workspace.IndexOlder
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := ovPopulated()
			f.ledger = &proposals.Ledger{}
			mutate(f)

			h := newHarness(t, f, 120, 40)
			assert.Empty(t, ovlLearnKeys(ovScreen(h)), "an unknown count is not zero")
		})
	}
}

func TestOverviewLinksStaleEvidenceSuggestsNothing(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(0, 0)

	h := newHarness(t, f, 120, 40)
	require.True(t, ovlHas(ovScreen(h), "learn:evidence"))

	// A failed reload keeps the old counts visible, but a suggestion
	// from them could be wrong.
	h.m.st.learning.set(learning.Snapshot{}, errors.New("database is locked"), time.Now())
	require.True(t, h.m.st.learning.stale())
	assert.False(t, ovlHas(ovScreen(h), "learn:evidence"))
}

func TestOverviewLinksUnresolvedAgentToInferenceSettings(t *testing.T) {
	f := ovPopulated()
	f.settings = fakeSettingsSnapshot(t, "agent:\n  cli: nosuch\n")
	f.doctor = &doctor.Report{Checks: []doctor.Check{
		{Name: "agent", State: doctor.StateWarn, Detail: `unknown agent cli "nosuch"`},
	}, Warns: 1}

	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	it := ovFind(t, o, "learn:agent")
	assert.Equal(t, ovActionable, it.group, "an unresolved agent blocks distillation")
	assert.Equal(t, "Inference agent unresolved", it.title)
	assert.Equal(t, ovNav(destSettings, "group:inference", "open inference settings"), it.act)
	assert.Contains(t, it.body[0], `unknown agent cli "nosuch"`)

	for _, other := range o.attention() {
		assert.NotEqual(t, "doctor:0:agent", other.key, "the doctor check states the same fact; the list shows it once")
	}

	ovSelect(t, h, "learn:agent")
	assert.Regexp(t, `! Inference agent unresolved +Settings ›`, h.view())

	stub := ovlRecorder(h, destSettings)
	h.press("enter")
	assert.Equal(t, destSettings, h.m.active)
	assert.Equal(t, []string{"group:inference"}, stub.intents)
	assert.Empty(t, f.called())
}

func TestOverviewLinksConfigErrorsOpenTheirGroup(t *testing.T) {
	cases := []struct {
		name, yaml, key, intent string
		tone                    ui.Tone
	}{
		{"agent section", "agent:\n  cli: [a, b]\n", "learn:agent", "group:inference", ui.ToneFailure},
		{"distill section", "distill:\n  write: maybe\n", "learn:config:distill", "group:pinwrite", ui.ToneFailure},
		{"exclude pattern", "index:\n  exclude:\n    - \"a/**/b\"\n", "learn:config:index", "group:indexing", ui.ToneFailure},
		{"review window", "reviews:\n  window_days: soon\n", "learn:config:index", "group:evidence", ui.ToneFailure},
		{"malformed file", "agent: [\n", "learn:config", "group:inference", ui.ToneFailure},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ovPopulated()
			f.settings = fakeSettingsSnapshot(t, tc.yaml)

			h := newHarness(t, f, 120, 40)
			o := ovScreen(h)

			it := ovFind(t, o, tc.key)
			assert.Equal(t, ovActionable, it.group)
			assert.Equal(t, tc.tone, it.tone)
			assert.Equal(t, destSettings, it.act.dest)
			assert.Equal(t, tc.intent, it.act.intent)

			var configItems int

			for _, k := range ovlLearnKeys(o) {
				if strings.HasPrefix(k, "learn:config") || k == "learn:agent" {
					configItems++
				}
			}

			assert.Equal(t, 1, configItems, "one settings problem is one item")
		})
	}
}

func TestOverviewLinksPinWriteOffIsOptionalOnly(t *testing.T) {
	f := ovPopulated()
	f.settings = fakeSettingsSnapshot(t, "")

	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	it := ovFind(t, o, "learn:pinwrite")
	assert.Equal(t, ovOptional, it.group, "the closed gate is a choice, never an action")
	assert.Equal(t, ovNav(destSettings, "group:pinwrite", "open pin writing settings"), it.act)
	assert.Contains(t, it.body[0], "3 proposals wait for a decision")
	assert.Contains(t, it.body[1], "Saving applies nothing")

	for _, other := range o.attention() {
		if other.group == ovActionable {
			assert.NotEqual(t, "group:pinwrite", other.act.intent, "no Action item opens the pin-write gate")
		}
	}

	ovSelect(t, h, "learn:pinwrite")

	stub := ovlRecorder(h, destSettings)
	h.press("enter")
	assert.Equal(t, []string{"group:pinwrite"}, stub.intents)
	assert.Empty(t, f.called(), "the link saves nothing")
}

func TestOverviewLinksPinWriteNeedsAKnownClosedGateAndPendingWork(t *testing.T) {
	cases := map[string]func(t *testing.T, f *fakeServices){
		"the gate is on": func(t *testing.T, f *fakeServices) {
			f.settings = fakeSettingsSnapshot(t, "distill:\n  write: true\n")
		},
		"the settings have not loaded": func(t *testing.T, f *fakeServices) {},
		"no proposal is pending": func(t *testing.T, f *fakeServices) {
			f.settings = fakeSettingsSnapshot(t, "")
			f.ledger = &proposals.Ledger{}
		},
		"the pending count is unknown": func(t *testing.T, f *fakeServices) {
			f.settings = fakeSettingsSnapshot(t, "")
			f.ledger, f.ledgerErr = nil, errors.New("database is locked")
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := ovPopulated()
			mutate(t, f)

			h := newHarness(t, f, 120, 40)
			assert.False(t, ovlHas(ovScreen(h), "learn:pinwrite"))
		})
	}
}

func TestOverviewLinksActiveRunRow(t *testing.T) {
	f := ovPopulated()
	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	require.False(t, ovlHas(o, "learn:run"), "no run, no row")

	before, ok := o.attentionCount()
	require.True(t, ok)

	captureRun(h)

	it := ovFind(t, o, "learn:run")
	assert.Equal(t, ovOptional, it.group)
	assert.Equal(t, "Distillation running · 3 of 10 done", it.title)
	assert.Equal(t, ovNav(destLessons, "run", "open the run"), it.act)

	after, _ := o.attentionCount()
	assert.Equal(t, before+1, after)

	ovSelect(t, h, "learn:run")
	assert.Regexp(t, `· Distillation running · 3 of 10 done +Lessons ›`, h.view())

	h.m.st.requestStop()
	assert.Equal(t, "Distillation stopping · 3 of 10 done", ovFind(t, o, "learn:run").title)

	h.press("enter")
	assert.Equal(t, destLessons, h.m.active)
	assert.Equal(t, lesRun, ovlLessons(h).sub)

	h.send(mutationDoneMsg{})
	assert.False(t, ovlHas(o, "learn:run"), "the row leaves with the run")
}

func TestOverviewLinksRunRowWithoutADenominatorShowsNoCount(t *testing.T) {
	h := newHarness(t, ovPopulated(), 120, 40)

	_, cancel := h.m.st.runContext()
	require.True(t, h.m.st.beginRun("Refreshing evidence", runNote{
		verb: "Refreshing evidence", stop: cancel, home: navigateMsg{dest: destLessons, intent: "run"},
	}))
	h.m.st.lastRunKind = runEvidence

	it := ovFind(t, ovScreen(h), "learn:run")
	assert.Equal(t, "Evidence refresh running", it.title, "a fetch has no known total")
}

func TestOverviewLinksRenderHostileTextInert(t *testing.T) {
	f := ovPopulated()
	f.settings = fakeSettingsSnapshot(t, "distill:\n  write: \"\\e[2Jboom\\a\"\n")

	for _, size := range [][2]int{{120, 40}, {80, 24}} {
		h := newHarness(t, f, size[0], size[1])
		ovSelect(t, h, "learn:config:distill")

		if size[0] < ui.WideWidth {
			h.press("enter")
		}

		out := h.m.View()
		assert.Contains(t, h.view(), "Pin-write setting invalid", "size %v", size)
		assert.NotContains(t, out, "\x1b", "size %v: no escape sequence reaches the terminal", size)
		assert.NotContains(t, out, "\x07", "size %v", size)
	}
}

func TestOverviewLinksNarrowOpensTheDetailFirst(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {ui.MinWidth, ui.MinHeight}} {
		f := ovPopulated()
		f.learning = ovlEvidence(0, 0)

		h := newHarness(t, f, size[0], size[1])
		ovSelect(t, h, "learn:evidence")

		h.press("enter")
		frame := h.view()
		assertFrame(t, frame, size[0], size[1])
		assert.Equal(t, destOverview, h.m.active, "size %v: the first enter shows the detail", size)
		assert.Contains(t, frame, "No evidence collected yet", "size %v", size)

		// The NEXT line ends the detail, which scrolls at the minimum size.
		h.press("end")
		assert.Contains(t, h.view(), "enter collect evidence", "size %v: the NEXT line names the workflow", size)

		h.press("enter")
		assert.Equal(t, destLessons, h.m.active, "size %v", size)
		assert.Equal(t, lesEvidence, ovlLessons(h).sub, "size %v", size)
	}
}

func TestOverviewLinksFramesFitAtEverySize(t *testing.T) {
	for _, size := range [][2]int{{120, 40}, {200, 50}, {100, 30}, {80, 24}, {ui.MinWidth, ui.MinHeight}} {
		w, hgt := size[0], size[1]

		f := ovPopulated()
		f.learning = ovlEvidence(0, 0)
		f.settings = fakeSettingsSnapshot(t, "agent:\n  cli: nosuch\n")

		h := newHarness(t, f, w, hgt)

		for _, key := range []string{"learn:agent", "learn:evidence", "learn:pinwrite"} {
			ovSelect(t, h, key)
			assertFrame(t, h.view(), w, hgt)
		}

		// A run hides the evidence suggestion, so the run row has its
		// own console.
		h = newHarness(t, f, w, hgt)
		captureRun(h)
		ovSelect(t, h, "learn:run")
		assertFrame(t, h.view(), w, hgt)
	}
}

func TestOverviewLinksSummaryShowsEvidenceAndPinWriting(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {ui.MinWidth, ui.MinHeight}} {
		w, hgt := size[0], size[1]

		f := ovPopulated()
		f.learning = ovlEvidence(8, 4)
		f.settings = fakeSettingsSnapshot(t, "")

		h := newHarness(t, f, w, hgt)
		ovOpenSummary(t, h)

		flat := ovFlat(ovReadDetail(t, h, w, hgt))
		assert.Contains(t, flat, "Evidence 8 fix findings · 4 review findings", "size %v", size)
		assert.Contains(t, flat, "Pin writing off · apply shows the YAML block", "size %v", size)
		assert.Empty(t, f.called(), "size %v", size)
	}
}

func TestOverviewLinksSummaryKeepsUnknownFacts(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(0, 0)
	f.learning.CountsErr = errors.New("database is locked")
	f.settings = fakeSettingsSnapshot(t, "distill:\n  write: maybe\n")

	h := newHarness(t, f, 80, 24)
	ovOpenSummary(t, h)

	flat := ovFlat(ovReadDetail(t, h, 80, 24))
	assert.Contains(t, flat, "Evidence unknown: database is locked")
	assert.Contains(t, flat, "Pin writing unknown:")
	assert.NotContains(t, flat, "0 fix findings", "an unknown count is not zero")
}

func TestOverviewLinksIndexReaderGroup(t *testing.T) {
	window := fakeSettingsSnapshot(t, "index:\n  generated: true\nreviews:\n  window_days: soon\n")
	assert.Equal(t, settings.GroupEvidence, ovIndexReaderGroup(window), "the error line is the window line")

	exclude := fakeSettingsSnapshot(t, "reviews:\n  window_days: 30\nindex:\n  exclude: [\"a/**/b\"]\n")
	assert.Equal(t, settings.GroupIndexing, ovIndexReaderGroup(exclude), "an error without a line opens indexing")

	generated := fakeSettingsSnapshot(t, "reviews:\n  window_days: 30\nindex:\n  generated: often\n")
	require.Error(t, generated.ReaderErrs[settings.ReaderIndex])
	assert.Equal(t, settings.GroupIndexing, ovIndexReaderGroup(generated), "another line opens indexing")
}

// The tests below cover the review fixes of the Overview learning links:
// neutral ledger wording, no suggestion during or after a run, the tab
// count that waits for every source, and a blocked settings file.

// ovlSettingsFile inspects a settings file with content and mode. A
// read-only file blocks every save, so the tests can separate the block
// from the reader errors.
func ovlSettingsFile(t *testing.T, content string, mode os.FileMode) *settings.Snapshot {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(settings.File))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	require.NoError(t, os.Chmod(path, mode))

	snap, err := settings.Inspect(root, integration.Builtin())
	require.NoError(t, err)

	return snap
}

// ovlBeginEvidenceRun starts a held evidence refresh in the session
// state, as the Evidence view does. The kind is not a distillation, so
// a test sees the effect of the run alone.
func ovlBeginEvidenceRun(t *testing.T, h *harness) {
	t.Helper()

	_, cancel := h.m.st.runContext()
	require.True(t, h.m.st.beginRun("Refreshing evidence", runNote{
		verb: "Refreshing evidence", stop: cancel, home: navigateMsg{dest: destLessons, intent: "run"},
	}))
	h.m.st.lastRunKind = runEvidence
}

func TestOverviewLinksDistillItemStatesOnlyTheLedgerFact(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.ledger = &proposals.Ledger{}

	h := newHarness(t, f, 120, 40)

	it := ovFind(t, ovScreen(h), "learn:distill")
	assert.NotEqual(t, "not distilled", it.badge, "the badge states the ledger fact")
	assert.NotContains(t, it.title, "yet")
	assert.Equal(t, "12 findings, no proposals in the ledger", it.title)

	body := strings.Join(it.body, " ")
	assert.Contains(t, body, "The decision ledger holds no proposal.")
	assert.Contains(t, body, "No evidence group has a distillation record yet.",
		"the snapshot counts the recorded groups, so the item can say so")
}

// Review fix: a distillation of an earlier session that saved no
// proposal left an empty ledger. The item then offered the work again
// as if nothing was distilled.
func TestOverviewLinksDistillItemNamesAnEarlierDistillation(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.learning.DistilledGroups = 3
	f.ledger = &proposals.Ledger{}

	h := newHarness(t, f, 120, 40)

	it := ovFind(t, ovScreen(h), "learn:distill")
	assert.Equal(t, ovOptional, it.group)
	assert.Equal(t, "12 findings, 3 distilled groups, no proposals", it.title)
	assert.Equal(t, ovNav(destLessons, "distill", "check for new groups"), it.act)

	body := strings.Join(it.body, " ")
	assert.Contains(t, body, "An earlier distillation recorded 3 evidence groups, and the ledger holds no proposal.")
	assert.Contains(t, body, "A new plan skips every recorded group.")
	assert.Contains(t, body, "New evidence can form new groups, and only a plan finds them.")
	assert.NotContains(t, body, "No evidence group has a distillation record")

	ovSelect(t, h, "learn:distill")
	assert.Contains(t, h.view(), "enter check for new groups")

	// One recorded group reads in the singular.
	f.learning.DistilledGroups = 1
	h.run(h.m.st.load(secLearning))

	it = ovFind(t, ovScreen(h), "learn:distill")
	assert.Equal(t, "12 findings, 1 distilled group, no proposals", it.title)
	assert.Contains(t, strings.Join(it.body, " "), "recorded 1 evidence group,")

	// The link opens the scope step, which only plans.
	h.press("enter")
	assert.Equal(t, lesRun, ovlLessons(h).sub)
	assert.Zero(t, f.callCount("RunDistill"))
	assert.Zero(t, f.callCount("PlanDistill"), "opening the scope step plans nothing")
}

func TestOverviewLinksNoDistillSuggestionAfterADistillationOfThisSession(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.ledger = &proposals.Ledger{}

	h := newHarness(t, f, 120, 40)
	require.True(t, ovlHas(ovScreen(h), "learn:distill"))

	// The distillation ends, and every group saved no proposal: the
	// ledger stays empty after the reload.
	captureRun(h)
	h.send(mutationDoneMsg{})
	require.Nil(t, h.m.st.run)
	require.Empty(t, f.ledger.Pending)

	o := ovScreen(h)
	assert.False(t, ovlHas(o, "learn:distill"), "the list does not offer the same paid work again")
	assert.NotContains(t, h.view(), "prepare a distillation")

	// A later evidence refresh can store new findings, which can form
	// new groups. The suggestion comes back.
	ovlBeginEvidenceRun(t, h)
	h.send(mutationDoneMsg{})
	assert.True(t, ovlHas(o, "learn:distill"))
}

func TestOverviewLinksNoLearningSuggestionDuringARun(t *testing.T) {
	cases := map[string]struct {
		fixes, reviews int
		key            string
	}{
		"findings without proposals": {8, 4, "learn:distill"},
		"no evidence yet":            {0, 0, "learn:evidence"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := ovPopulated()
			f.learning = ovlEvidence(tc.fixes, tc.reviews)
			f.ledger = &proposals.Ledger{}

			h := newHarness(t, f, 120, 40)
			o := ovScreen(h)
			require.True(t, ovlHas(o, tc.key))

			ovlBeginEvidenceRun(t, h)
			assert.Equal(t, []string{"learn:run"}, ovlLearnKeys(o),
				"the snapshots wait for the run to end, so the run row is the only learning item")

			h.send(mutationDoneMsg{})
			assert.True(t, ovlHas(o, tc.key), "the suggestion returns after the run")
		})
	}
}

func TestOverviewTabCountWaitsForTheLearningSources(t *testing.T) {
	cases := map[string]struct {
		reset func(st *state)
		note  string
	}{
		"the ledger": {
			reset: func(st *state) { st.ledger = section[*proposals.Ledger]{loading: true} },
			note:  "reading the decision ledger",
		},
		"the evidence": {
			reset: func(st *state) { st.learning = section[learning.Snapshot]{loading: true} },
			note:  "reading the evidence store",
		},
		"the settings": {
			reset: func(st *state) { st.settings = section[*settings.Snapshot]{loading: true} },
			note:  "reading the settings",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := ovPopulated()
			f.learning = ovlEvidence(0, 0)
			f.settings = fakeSettingsSnapshot(t, "")

			h := newHarness(t, f, 80, 24)
			o := ovScreen(h)
			require.NotEmpty(t, o.tabCount(), "every source has loaded")

			tc.reset(h.m.st)

			_, ok := o.attentionCount()
			assert.False(t, ok, "a partial count would read as a result")
			assert.Empty(t, o.tabCount())
			assert.Contains(t, o.pendingSources(), tc.note)
			assert.Contains(t, h.view(), tc.note)
			assert.False(t, ovlHas(o, ovSummaryKey), "the summary item waits for every source")
		})
	}
}

func TestOverviewTabCountSettlesOnAFailedLearningSource(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(0, 0)
	f.settings = fakeSettingsSnapshot(t, "")

	h := newHarness(t, f, 120, 40)
	o := ovScreen(h)

	// A failed first read is a result: it settles the source, and the
	// item that reads it shows the failure or nothing.
	h.m.st.settings = section[*settings.Snapshot]{err: os.ErrPermission}

	_, ok := o.attentionCount()
	assert.True(t, ok)
	assert.NotContains(t, o.pendingSources(), "reading the settings")
}

func TestOverviewLinksConfigFileItemKeepsReaderErrorsUnderABlock(t *testing.T) {
	cases := []struct {
		name, yaml string
		mode       os.FileMode
		want, not  []string
	}{
		{
			name: "read-only file with two reader errors",
			yaml: "distill:\n  write: maybe\nindex:\n  exclude: [\"**/a/b\"]\n",
			mode: 0o444,
			want: []string{
				"The distill reader: distill config:",
				"The index reader: index config: exclude",
				"The file cannot be edited here: the file is read-only",
			},
			not: []string{"Reason: the file is read-only"},
		},
		{
			name: "read-only malformed file",
			yaml: "agent: [\n",
			mode: 0o444,
			want: []string{
				"The agent reader: agent config: yaml:",
				"The file cannot be edited here: the file is read-only",
			},
			not: []string{"Reason:"},
		},
		{
			name: "writable malformed file",
			yaml: "agent: [\n",
			mode: 0o644,
			want: []string{"Reason: the file is not valid YAML: yaml:"},
			not:  []string{"reader:", "cannot be edited here"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ovPopulated()
			f.settings = ovlSettingsFile(t, tc.yaml, tc.mode)

			h := newHarness(t, f, 120, 40)
			it := ovFind(t, ovScreen(h), "learn:config")
			body := strings.Join(it.body, "\n")

			for _, s := range tc.want {
				assert.Contains(t, body, s)
			}

			for _, s := range tc.not {
				assert.NotContains(t, body, s)
			}

			assert.Empty(t, it.fix, "the block names the manual route")
		})
	}
}

func TestOverviewLinksIndexReaderGroupIgnoresTheFileBlock(t *testing.T) {
	content := "index:\n  generated: true\nreviews:\n  window_days: soon\n"

	for _, mode := range []os.FileMode{0o644, 0o444} {
		snap := ovlSettingsFile(t, content, mode)
		assert.Equal(t, settings.GroupEvidence, ovIndexReaderGroup(snap), "mode %v: the error line is the window line", mode)

		f := ovPopulated()
		f.settings = snap

		h := newHarness(t, f, 120, 40)
		assert.Equal(t, "group:evidence", ovFind(t, ovScreen(h), "learn:config:index").act.intent, "mode %v", mode)
	}

	readOnly := ovlSettingsFile(t, "reviews:\n  window_days: 30\nindex:\n  exclude: [\"a/**/b\"]\n", 0o444)
	assert.Equal(t, settings.GroupIndexing, ovIndexReaderGroup(readOnly), "an error without a line opens indexing")
}

func TestOverviewLinksPinWriteNamesABlockedFile(t *testing.T) {
	cases := []struct {
		name, yaml, reason string
		mode               os.FileMode
	}{
		{"read-only file", "distill:\n  write: false\n", "the file is read-only", 0o444},
		{"mixed line breaks", "distill:\r\n  write: false\n", "the file mixes CRLF and LF line breaks", 0o644},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ovPopulated()
			f.settings = ovlSettingsFile(t, tc.yaml, tc.mode)
			require.Empty(t, f.settings.ReaderErrs, "the reader loads the gate")
			require.Error(t, f.settings.Editable(settings.GroupPinWrite))

			h := newHarness(t, f, 120, 40)
			it := ovFind(t, ovScreen(h), "learn:pinwrite")
			assert.Equal(t, ovOptional, it.group)
			assert.Equal(t, "group:pinwrite", it.act.intent, "Settings shows the block in full")
			assert.NotContains(t, it.body[1], "Settings can turn pin writing on")
			assert.Contains(t, it.body[1], "Pin writing cannot be edited here: "+tc.reason)
			assert.Contains(t, it.body[1], "by hand", "the item names the manual route")
		})
	}
}

// The tests below hold review fixes. Each test drives the real model
// through the harness, so it proves the fix where the user sees it.

// Review fix: a ledger that holds only superseded rows is not empty, so
// Overview never says that it holds no proposal.
func TestOverviewNamesNoEmptyLedgerForSupersededHistory(t *testing.T) {
	f := ovPopulated()
	f.learning = ovlEvidence(8, 4)
	f.learning.DistilledGroups = 2
	f.ledger = &proposals.Ledger{Superseded: []smodel.Proposal{{ID: 1, Rule: "a"}, {ID: 2, Rule: "b"}}}

	h := newHarness(t, f, 120, 40)

	assert.False(t, ovlHas(ovScreen(h), "learn:distill"), "Proposals › f shows the superseded rows")
	assert.NotContains(t, lesFlat(h.view()), "holds no proposal")

	// The same evidence with a truly empty ledger still suggests the
	// work, so only the superseded rows changed the answer.
	f.ledger = &proposals.Ledger{}
	h.run(h.m.st.load(secLedger))
	assert.True(t, ovlHas(ovScreen(h), "learn:distill"))
}
