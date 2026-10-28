package lessonreport

import (
	"bytes"
	"errors"
	"io"
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
// The text shows the delivery records, the observation, and the separate
// check block. The renderer never changes the JSON of the report, and the
// same inputs give the same text.
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
	for _, needle := range []string{
		"delivery records: hook 2; injected 2",
		"delivered sessions: 2",
		"observation (delivered sessions): 0 linked; 0 no linked correction observed (qualified); 2 incomplete",
		"incomplete reasons: session-not-captured 2",
		"Checks / guard (separate from delivery)",
		"guard attempts",
		"pin evaluations",
		"expand: seamark lessons --delivery --pin " + r.Pins[0].PinRef,
	} {
		assert.Contains(t, out, needle)
	}

	assert.Equal(t, wire, encode(t, r), "rendering must never change the JSON")
	assert.Equal(t, out, rendered(t, mustBuild(t, in, "")), "the same inputs give the same text")
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

	// Version grouping, not action order, defines the expansion layout.
	r.Expanded.Versions = []string{"new-version", "old-version"}
	r.Expanded.Occurrences = []OccurrenceDetail{
		{AssociationRef: "as-old", PinVersion: "old-version", EpisodeRef: "old@1"},
		{AssociationRef: "as-new", PinVersion: "new-version", EpisodeRef: "new@1"},
	}
	r.Expanded.Sessions = []SessionWindow{{PinVersion: "old-version", SessionRef: "session-old", Partition: "no-linked-correction"}}
	out = rendered(t, r)
	assert.Less(t, strings.Index(out, "association as-new"), strings.Index(out, "association as-old"))
	assert.Contains(t, out, "session session-old; lane ; no linked correction observed (qualified)")
}

func TestRenderTextClassDescriptions(t *testing.T) {
	for _, tc := range []struct {
		class Class
		want  string
	}{
		{ClassAtAction, "Reminder emitted for an already-constructed action."},
		{ClassEarlierInContext, "Delivered earlier in this context."},
		{ClassOtherLane, "Delivery recorded only in another lane."},
		{ClassRegionNotMatched, "Corrected action outside this pin's regions."},
		{ClassNotEmitted, "Not emitted: over-total"},
		{ClassUnknown, "Unknown: unassessed"},
	} {
		t.Run(string(tc.class), func(t *testing.T) {
			r := mustBuild(t, Inputs{}, "")
			r.Expanded = &Expansion{Versions: []string{"v1"}, Occurrences: []OccurrenceDetail{{
				PinVersion: "v1", Class: PairClass(tc.class), Timing: PairTiming(TimingUnknown),
				Actions: []ActionDetail{{Class: tc.class, Cause: "over-total", Reason: CodeUnassessed,
					Timing: TimingUnknown, TimingReason: CodeUnassessed, Paths: []string{"outside.go"}}},
			}}}
			out := rendered(t, r)
			assert.Contains(t, out, tc.want)
			assert.Contains(t, out, "timing reason unassessed")
			assert.Contains(t, out, "paths [outside.go]")
		})
	}
}

func TestRenderTextSourcesAndSanitization(t *testing.T) {
	cfg := &reviews.Config{Pin: []reviews.PinRule{{Rule: "safe\x1b\nrule", Note: "PRIVATE-pin-note"}}}
	r := mustBuild(t, Inputs{Config: cfg, AuditError: "bad\x1b\naudit", CheckAuditError: "check\x00audit"}, "")
	out := rendered(t, r)
	for _, needle := range []string{
		"capture evidence unavailable", "delivery audit: unreadable", "check audit: unreadable",
		"archive: absent", ArchiveNotReadError, "safe", "badaudit", "prevented: 0",
	} {
		assert.Contains(t, out, needle)
	}

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
