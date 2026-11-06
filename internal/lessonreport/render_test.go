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
// The text shows the delivery records and the separate check block, and
// it leaves out each section that needs a capture archive. The renderer
// never changes the JSON of the report, and the same inputs give the
// same text.
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

	// The versions keep their order. Occurrences and session windows need
	// a capture archive, so the text leaves them out.
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
