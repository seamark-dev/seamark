package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/proposals"
	"github.com/seamark-dev/seamark/internal/settings"
)

// These tests cover the review fixes of the Overview learning links:
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
