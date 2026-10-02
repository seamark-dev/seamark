package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/learning"
	smodel "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
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
		tone                    Tone
	}{
		{"agent section", "agent:\n  cli: [a, b]\n", "learn:agent", "group:inference", ToneFailure},
		{"distill section", "distill:\n  write: maybe\n", "learn:config:distill", "group:pinwrite", ToneFailure},
		{"exclude pattern", "index:\n  exclude:\n    - \"a/**/b\"\n", "learn:config:index", "group:indexing", ToneFailure},
		{"review window", "reviews:\n  window_days: soon\n", "learn:config:index", "group:evidence", ToneFailure},
		{"malformed file", "agent: [\n", "learn:config", "group:inference", ToneFailure},
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

		if size[0] < wideWidth {
			h.press("enter")
		}

		out := h.m.View()
		assert.Contains(t, h.view(), "Pin-write setting invalid", "size %v", size)
		assert.NotContains(t, out, "\x1b", "size %v: no escape sequence reaches the terminal", size)
		assert.NotContains(t, out, "\x07", "size %v", size)
	}
}

func TestOverviewLinksNarrowOpensTheDetailFirst(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
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
	for _, size := range [][2]int{{120, 40}, {200, 50}, {100, 30}, {80, 24}, {minWidth, minHeight}} {
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
	for _, size := range [][2]int{{80, 24}, {minWidth, minHeight}} {
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
