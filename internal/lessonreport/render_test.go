package lessonreport

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

func rendered(t *testing.T, report Report) string {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, RenderText(&out, report))

	return out.String()
}

// TestRenderTextOverview prints the overview of two delivered sessions.
// The text shows the delivery records and the separate check block, and
// it leaves out the fixed fields. The renderer never changes the JSON of
// the report, and the same inputs give the same text.
func TestRenderTextOverview(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)
	in := Inputs{Audit: auditLines(
		v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected)),
		v2("e2", "s2", "m2", pin(id, ver, reviews.PinInjected)),
	), Config: cfg, Now: fixedNow}

	r := mustBuild(t, in, "")
	wire := encode(t, r)

	out := rendered(t, r)
	assert.True(t, strings.HasPrefix(out, "Lesson delivery (delivery/1; seamark "), out)

	for _, needle := range []string{
		"delivery records: hook 2; injected 2",
		"delivered sessions: 2",
		"Checks / guard (separate from delivery)",
		"guard attempts",
		"pin evaluations",
		"expand: seamark lessons --delivery --pin " + r.Pins[0].PinRef,
	} {
		assert.Contains(t, out, needle)
	}

	for _, left := range []string{
		"capture:", "archive:", "assessed correction occurrences", "decision timing", "observation",
		"session-not-captured", "Historical versions", "Unlinked feedback items", "Coverage",
	} {
		assert.NotContains(t, out, left)
	}

	assert.Equal(t, 2, r.Pins[0].Observation.Incomplete, "the JSON keeps the observation that the text leaves out")

	assert.Equal(t, wire, encode(t, r), "rendering must never change the JSON")
	assert.Equal(t, out, rendered(t, mustBuild(t, in, "")), "the same inputs give the same text")
}

// unknownEvidenceInputs lists one audit input for each state that gives no
// reading. Each input holds records, so a printed count would be a false zero.
func unknownEvidenceInputs(cfg *reviews.Config) map[string]Inputs {
	overLimit := auditLines(v2("e", "s", "m", heldBackPin(cfg, reviews.CauseRestated)))
	overLimit.OverLimit = true

	return map[string]Inputs{
		StateAbsent:     {},
		StateUnreadable: {AuditError: "denied"},
		StateOverLimit:  {Audit: overLimit},
	}
}

// TestRenderPrintsTheSelectionBudgets proves the budget block of each config
// state. The block comes after the totals and before the units.
func TestRenderPrintsTheSelectionBudgets(t *testing.T) {
	defaults := []string{
		"  pin cap: requested 3 (default), effective 3 pins per injection",
		"  total cap: fixed 8 lessons per injection",
		"  change budget: requested 6, effective 6 lessons per answer",
	}

	tests := []struct {
		name string
		in   Inputs
		want []string
	}{
		{"absent config", Inputs{}, defaults},
		{"zero budget", Inputs{Config: &reviews.Config{}}, defaults},
		{"negative budget", Inputs{Config: &reviews.Config{PinBudget: -2}}, defaults},
		{"stored budget at most the total", Inputs{Config: &reviews.Config{PinBudget: 8, ChangeBudget: 9}}, []string{
			"  pin cap: requested 8, effective 8 pins per injection",
			"  total cap: fixed 8 lessons per injection",
			"  change budget: requested 9, effective 9 lessons per answer",
		}},
		{"stored budget above the total", Inputs{Config: &reviews.Config{PinBudget: 16}}, []string{
			"  pin cap: requested 16, effective 8 pins per injection; pin_budget cannot raise the pin cap above the fixed total",
			"  total cap: fixed 8 lessons per injection",
			"  change budget: requested 6, effective 6 lessons per answer",
		}},
		{"invalid config", Inputs{ConfigRead: ConfigRead{State: ConfigInvalid}}, []string{
			"  pin cap: requested unknown, effective 3 pins per injection",
			"  total cap: fixed 8 lessons per injection",
			"  change budget: requested unknown, effective 6 lessons per answer",
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.Now = fixedNow
			out := rendered(t, mustBuild(t, tc.in, ""))

			block := strings.Join(append([]string{budgetHeading}, tc.want...), "\n") + "\n"
			assert.Contains(t, out, block)
			assert.Less(t, strings.Index(out, "Delivery totals"), strings.Index(out, budgetHeading))
			assert.Less(t, strings.Index(out, budgetHeading), strings.Index(out, "Units:"))
		})
	}
}

// TestRenderPrintsTheFixedSentence proves that the limit of the counts prints
// once, whether or not the audit reads.
func TestRenderPrintsTheFixedSentence(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	audit := auditLines(v2("e", "s", "m", heldBackPin(cfg, reviews.CauseRestated)))

	read := rendered(t, mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, ""))
	assert.Equal(t, 1, strings.Count(read, EmissionSentence))

	for state, in := range unknownEvidenceInputs(cfg) {
		t.Run(state, func(t *testing.T) {
			in.Config, in.Now = cfg, fixedNow
			assert.Equal(t, 1, strings.Count(rendered(t, mustBuild(t, in, "")), EmissionSentence))
		})
	}
}

// TestRenderPrintsTheCauseSplit proves that a pin prints its split beside its
// counts, and that a pin with no held-back record prints no split.
func TestRenderPrintsTheCauseSplit(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	audit := auditLines(
		v2("e1", "s", "m1", heldBackPin(cfg, reviews.CauseOverPinCap)),
		v2("e2", "s", "m2", heldBackPin(cfg, reviews.CauseRestated)),
	)
	r := mustBuild(t, Inputs{Audit: audit, Config: cfg, Now: fixedNow}, "")

	assert.Equal(t, 1, strings.Count(rendered(t, r), "    held back by cause (records): hook/over-pin-cap 1, hook/restated 1\n"))
	assert.Contains(t, rendered(t, r), "held-back 2;")

	plain := mustBuild(t, Inputs{Audit: auditLines(), Config: cfg, Now: fixedNow}, "")
	assert.NotContains(t, rendered(t, plain), "held back by cause")
}

// TestRenderPrintsNoDeliveryCountForUnknownEvidence covers the delivery lines
// only, because the pin version, the budgets, and the check counts still
// print digits. The words of the units block keep their text, so the check
// for delivered sessions names the indent of the pin line.
func TestRenderPrintsNoDeliveryCountForUnknownEvidence(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")

	for state, in := range unknownEvidenceInputs(cfg) {
		t.Run(state, func(t *testing.T) {
			in.Config, in.Now = cfg, fixedNow
			out := rendered(t, mustBuild(t, in, ""))
			unknown := "unknown (audit " + state + ")"

			assert.Contains(t, out, "Delivery totals (records, not corrections): "+unknown+"\n")
			assert.Contains(t, out, "  "+unknown+"\n", "the evidence reading names the state")
			assert.Equal(t, len(cfg.Pin), strings.Count(out, "    delivery records: "+unknown+"\n"))

			for _, word := range []string{"suppressed hook records", "    delivered sessions:", "legacy identity records", "held back by cause"} {
				assert.NotContains(t, out, word)
			}
		})
	}
}

// TestSelectionLineHoldsTheBudgetsAndTheCauses owns the content of the line.
// The console test only compares its lines with this engine string.
func TestSelectionLineHoldsTheBudgetsAndTheCauses(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	cfg.PinBudget = 16
	_, ver := pinOf(cfg, 0)

	causes := []string{reviews.CauseRestated, reviews.CauseOverPinCap, reviews.CauseOverTotal, "invented"}
	records := make([]reviews.Firing, 0, len(causes))

	for _, cause := range causes {
		records = append(records, v2("e-"+cause, "s", "m-"+cause, heldBackPin(cfg, cause)))
	}

	r := mustBuild(t, Inputs{Audit: auditLines(records...), Config: cfg, Now: fixedNow}, "")
	line, ok := SelectionLine(&r, ver)
	require.True(t, ok)

	for _, want := range []string{
		"requested 16, effective 8",
		"total cap: fixed 8 lessons per injection",
		"change budget: requested 6, effective 6 lessons per answer",
		"evidence read",
		"held-back 4;",
		"hook/over-pin-cap 1", "hook/over-total 1", "hook/restated 1", "hook/unknown 1",
	} {
		assert.Contains(t, line, want)
	}

	for state, in := range unknownEvidenceInputs(cfg) {
		t.Run(state, func(t *testing.T) {
			in.Config, in.Now = cfg, fixedNow
			unread := mustBuild(t, in, "")

			line, ok := SelectionLine(&unread, ver)
			require.True(t, ok)

			lines := strings.Split(line, "\n")
			require.Len(t, lines, 5)
			assert.Contains(t, lines[1], "requested 16, effective 8")
			assert.Equal(t, "unknown (audit "+state+")", lines[4])
			assert.NotRegexp(t, "[0-9]", lines[4])
		})
	}
}

func TestRenderTextExpansion(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)
	in := Inputs{Audit: auditLines(v2("e1", "s1", "m1", pin(id, ver, reviews.PinInjected))), Config: cfg, Now: fixedNow}

	r := mustBuild(t, in, id[:12])
	require.NotNil(t, r.Expanded)
	out := rendered(t, r)
	assert.Contains(t, out, "Expansion: pin "+r.Expanded.PinRef+" (versions are never pooled)")
	assert.Contains(t, out, "  version "+ver)
	assert.Equal(t, 1, strings.Count(out, "; mode "), "the expansion prints the selected pin only")

	// The versions keep their order. The text leaves out the occurrences
	// and the session windows.
	r.Expanded.Versions = []string{"new-version", "old-version"}
	r.Expanded.Occurrences = []OccurrenceDetail{
		{AssociationRef: "as-old", PinVersion: "old-version", EpisodeRef: "old@1"},
		{AssociationRef: "as-new", PinVersion: "new-version", EpisodeRef: "new@1"},
	}
	r.Expanded.Sessions = []SessionWindow{{PinVersion: "old-version", SessionRef: "session-old", Partition: "no-linked-correction"}}
	out = rendered(t, r)
	assert.Less(t, strings.Index(out, "  version new-version"), strings.Index(out, "  version old-version"))
	assert.NotContains(t, out, "as-old")
	assert.NotContains(t, out, "session-old")
}

func TestRenderTextSourcesAndSanitization(t *testing.T) {
	cfg := &reviews.Config{Pin: []reviews.PinRule{{Rule: "safe\x1b\nrule", Note: "PRIVATE-pin-note"}}}
	r := mustBuild(t, Inputs{Config: cfg, AuditError: "bad\x1b\naudit", CheckAuditError: "check\x00audit"}, "")
	out := rendered(t, r)
	for _, needle := range []string{"delivery audit: unreadable", "check audit: unreadable", "safe", "badaudit", "prevented: 0"} {
		assert.Contains(t, out, needle)
	}

	assert.NotContains(t, out, CaptureUnavailableMessage)
	assert.NotContains(t, out, ArchiveNotReadError)

	assert.NotContains(t, out, "\x1b")
	assert.NotContains(t, out, "\x00")
	assert.NotContains(t, out, "PRIVATE-pin-note")
	assert.NotContains(t, out, "enforced")
}

func TestRenderTextWriterErrors(t *testing.T) {
	r := mustBuild(t, Inputs{}, "")
	failure := errors.New("writer failed")
	require.ErrorIs(t, RenderText(renderFailWriter{failure}, r), failure)
	require.ErrorIs(t, RenderText(renderShortWriter{}, r), io.ErrShortWrite)
}

type renderFailWriter struct{ err error }

func (w renderFailWriter) Write([]byte) (int, error) { return 0, w.err }

type renderShortWriter struct{}

func (renderShortWriter) Write(b []byte) (int, error) { return len(b) - 1, nil }

// TestRenderPrintsNoSplitWithoutAHeldBackRecord proves the condition of the
// split on a read audit. The empty split prints no line, so the sessions
// line follows the counts directly.
func TestRenderPrintsNoSplitWithoutAHeldBackRecord(t *testing.T) {
	cfg := testConfig("Reject unknown fields.")
	id, ver := pinOf(cfg, 0)

	r := mustBuild(t, Inputs{
		Audit: auditLines(v2("e", "s", "m", pin(id, ver, reviews.PinInjected))), Config: cfg, Now: fixedNow,
	}, "")

	out := rendered(t, r)
	assert.NotContains(t, out, "held back by cause")

	lines := strings.Split(out, "\n")
	at := slices.IndexFunc(lines, func(line string) bool {
		return strings.HasPrefix(line, "    delivery records: hook 1; injected 1;")
	})

	require.GreaterOrEqual(t, at, 0, out)
	require.Less(t, at+1, len(lines))
	assert.True(t, strings.HasPrefix(lines[at+1], "    delivered sessions: "), "next line: %q", lines[at+1])
}
