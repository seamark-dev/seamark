package reviews

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestRecordAndReadFirings(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, RecordFiring(root, "scripts/a.py", "Edit", []model.Lesson{
		{Region: "scripts", Symptom: "E702"},
		{Region: "scripts", Symptom: "RUF001"},
	}))
	require.NoError(t, RecordFiring(root, "api/b.py", "Write", []model.Lesson{
		{Region: "api", Symptom: "E501"},
	}))

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 2)

	assert.Equal(t, "scripts/a.py", firings[0].File)
	assert.Equal(t, "Edit", firings[0].Tool)
	require.Len(t, firings[0].Fired, 2)
	assert.Equal(t, "E702", firings[0].Fired[0].Symptom)
	assert.NotEmpty(t, firings[0].TS)
}

func TestRecordFiringNoLessonsIsNoop(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, RecordFiring(root, "x.py", "Edit", nil))

	// No log file created when nothing fired.
	_, err := os.Stat(filepath.Join(root, ".seamark", auditFile))
	assert.True(t, os.IsNotExist(err))
}

func TestRecordHookDeliveryHashesSessionAndMeasuresContext(t *testing.T) {
	root := t.TempDir()

	require.NoError(t, RecordHookDelivery(root, "api/handler.py", "Edit", []model.Lesson{
		{Region: "api", Symptom: "Keep the generated client synchronized."},
	}, HookDelivery{
		Status: DeliveryInjected, SessionID: "provider-session-secret",
		MatchID: "provider-tool-secret", Generation: 2, ContextBytes: 437,
	}))

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1)
	assert.Equal(t, DeliveryInjected, firings[0].Delivery)
	assert.Len(t, firings[0].SessionSHA, 64)
	assert.Len(t, firings[0].MatchSHA, 64)
	assert.Equal(t, uint64(2), firings[0].Generation)
	assert.NotContains(t, firings[0].SessionSHA, "provider-session-secret")
	assert.Equal(t, 437, firings[0].ContextBytes)
	assert.True(t, firings[0].Delivered())
	raw, err := os.ReadFile(filepath.Join(root, ".seamark", auditFile))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "provider-session-secret",
		"the provider session ID must never reach the audit file")
	assert.NotContains(t, string(raw), "provider-tool-secret",
		"the provider tool-use ID must never reach the audit file")

	otherRoot := t.TempDir()
	require.NoError(t, RecordHookDelivery(otherRoot, "api/handler.py", "Edit", []model.Lesson{
		{Region: "api", Symptom: "Keep the generated client synchronized."},
	}, HookDelivery{
		Status: DeliveryInjected, SessionID: "provider-session-secret", ContextBytes: 437,
	}))
	otherFirings, err := ReadFirings(otherRoot)
	require.NoError(t, err)
	require.Len(t, otherFirings, 1)
	assert.NotEqual(t, firings[0].SessionSHA, otherFirings[0].SessionSHA,
		"the same provider session cannot be correlated across repository logs")
}

func TestRecordHookDeliveryFilesKeepsPathsAndOneContextTotal(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "Keep the generated client synchronized."}}

	// One operation on two files is one record with one byte total.
	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go", "db/b.go"}, "apply_patch",
		lessons, HookDelivery{Status: DeliveryInjected, SessionID: "s", ContextBytes: 120}))

	// One file keeps the single-file shape that older readers expect.
	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go"}, "Edit",
		lessons, HookDelivery{Status: DeliveryInjected, SessionID: "s", ContextBytes: 80}))

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 2)

	assert.Empty(t, firings[0].File)
	assert.Equal(t, []string{"api/a.go", "db/b.go"}, firings[0].Files)
	assert.Equal(t, 120, firings[0].ContextBytes)
	assert.Empty(t, firings[0].Surface, "an edit-hook record names no surface")

	assert.Equal(t, "api/a.go", firings[1].File)
	assert.Empty(t, firings[1].Files)

	summary := Summarize(firings, nil)
	assert.Equal(t, 2, summary.Files, "distinct files count once across both records")
}

func TestRecordHookDeliveryValidatesMetadataWithoutLessons(t *testing.T) {
	root := t.TempDir()
	tests := []HookDelivery{
		{Status: DeliveryInjected, ContextBytes: -1},
		{Status: DeliveryStatus("future-status")},
		{Status: DeliverySuppressedRepeat, ContextBytes: 1},
	}

	for _, delivery := range tests {
		err := RecordHookDelivery(root, "api/handler.py", "Edit", nil, delivery)
		require.Error(t, err, "invalid metadata must not be hidden by an empty lesson set")
	}

	_, err := os.Stat(filepath.Join(root, ".seamark", auditFile))
	assert.True(t, os.IsNotExist(err), "invalid delivery metadata must not create an audit log")
}

func TestReadFiringsMissingAndGarbage(t *testing.T) {
	root := t.TempDir()

	// Missing log is empty history, not an error.
	firings, err := ReadFirings(root)
	require.NoError(t, err)
	assert.Empty(t, firings)

	// A corrupt line is skipped, valid lines survive.
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, auditFile),
		[]byte("not json\n{\"file\":\"a.py\",\"tool\":\"Edit\",\"fired\":[]}\n"), 0o644))

	firings, err = ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "the one valid line survives the garbage one")
	assert.Equal(t, "a.py", firings[0].File)
}

func TestReadFiringsSkipsOverLongLine(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".seamark")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	// A record BEFORE a >maxAuditLine corrupt line, and one AFTER: the
	// giant line must be skipped without an error and without hiding the
	// records on either side of it.
	var buf []byte
	buf = append(buf, []byte(`{"file":"before.py","tool":"Edit","fired":[]}`+"\n")...)
	buf = append(buf, make([]byte, maxAuditLine+16)...) // no newline until…
	buf = append(buf, '\n')
	buf = append(buf, []byte(`{"file":"after.py","tool":"Write","fired":[]}`+"\n")...)

	require.NoError(t, os.WriteFile(filepath.Join(dir, auditFile), buf, 0o644))

	firings, err := ReadFirings(root)
	require.NoError(t, err, "an over-long line must not error the read")
	require.Len(t, firings, 2, "records before AND after the corruption survive")
	assert.Equal(t, "before.py", firings[0].File)
	assert.Equal(t, "after.py", firings[1].File)
}

func TestRecordFiringConcurrentAppendsStayIntact(t *testing.T) {
	root := t.TempDir()

	const n = 40

	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_ = RecordFiring(root, "f.py", "Edit", []model.Lesson{
				{Region: "scripts", Symptom: "E702"},
			})
		}()
	}

	wg.Wait()

	// Every concurrent append must be a whole, parseable line — no
	// interleaved/torn records (O_APPEND + single Write).
	firings, err := ReadFirings(root)
	require.NoError(t, err)
	assert.Len(t, firings, n, "all concurrent appends parsed intact")
}

func TestSummarize(t *testing.T) {
	firings := []Firing{
		{TS: "2026-07-01T00:00:00Z", File: "scripts/a.py", Fired: []FiredLesson{
			{Region: "scripts", Symptom: "E702"},
		}},
		{TS: "2026-07-02T00:00:00Z", File: "scripts/b.py", Fired: []FiredLesson{
			{Region: "scripts", Symptom: "E702"},
			{Region: "scripts", Symptom: "RUF001"},
		}},
		{TS: "2026-07-03T00:00:00Z", File: "scripts/c.py",
			Delivery: DeliverySuppressedRepeat, Fired: []FiredLesson{
				{Region: "scripts", Symptom: "E702"},
			}},
	}

	surfaced := []model.Lesson{
		{Region: "scripts", Symptom: "E702"},
		{Region: "scripts", Symptom: "RUF001"},
		{Region: "api", Symptom: "E501"}, // never fired
	}

	s := Summarize(firings, surfaced)

	assert.Equal(t, 2, s.Total, "only two records delivered context")
	assert.Equal(t, 2, s.Files, "two distinct files")
	assert.Equal(t, 1, s.SuppressedHookFirings)

	require.NotEmpty(t, s.Ranked)
	assert.Equal(t, "E702", s.Ranked[0].Symptom, "most-matched first")
	assert.Equal(t, 2, s.Ranked[0].Count)
	assert.Equal(t, 3, s.Ranked[0].Matches)
	assert.Equal(t, "2026-07-03T00:00:00Z", s.Ranked[0].LastTS, "latest match wins")

	require.Len(t, s.NeverFired, 1)
	assert.Equal(t, "E501", s.NeverFired[0].Symptom, "surfaced but never fired = decay candidate")
}

func TestSummarizeMeasuresRepeatedHookDeliveryWithinSession(t *testing.T) {
	lessonA := FiredLesson{Region: "api", Symptom: "synchronize generated client"}
	lessonB := FiredLesson{Region: "api", Symptom: "bump cache version"}
	firings := []Firing{
		{File: "api/a.py", Delivery: DeliveryInjected, SessionSHA: "session-a", Generation: 1,
			ContextBytes: 400, Fired: []FiredLesson{lessonA}},
		{File: "api/b.py", Delivery: DeliveryInjected, SessionSHA: "session-a", Generation: 1,
			ContextBytes: 420, Fired: []FiredLesson{lessonA}},
		{File: "api/c.py", Delivery: DeliveryInjected, SessionSHA: "session-a", Generation: 1,
			ContextBytes: 450, Fired: []FiredLesson{lessonA, lessonB}},
		{File: "api/d.py", Delivery: DeliveryInjected, SessionSHA: "session-b", Generation: 1,
			ContextBytes: 410, Fired: []FiredLesson{lessonA}},
		{File: "api/e.py", Delivery: DeliveryInjected, SessionSHA: "session-a", Generation: 2,
			ContextBytes: 430, Fired: []FiredLesson{lessonA}},
	}

	s := Summarize(firings, nil)

	assert.Equal(t, 5, s.InstrumentedHookFirings)
	assert.Equal(t, 1, s.RepeatedHookFirings,
		"only the second delivery contains no lesson new to its session")
	assert.Zero(t, s.SuppressedHookFirings)
	assert.Equal(t, 2110, s.HookContextBytes)
}

func TestRecordHookDeliveryAttributesTheClientAndTheReceiver(t *testing.T) {
	root := t.TempDir()
	lessons := []model.Lesson{{Region: "api", Symptom: "Keep the generated client synchronized."}}
	dc := DeliveryContext{ClientID: "claude", ReceiverID: "provider-receiver-secret"}

	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go"}, "Edit", lessons, HookDelivery{
		Status: DeliveryInjected, SessionID: "provider-session-secret", ContextBytes: 90,
		ClientID: "claude", Mechanism: "pre-tool-use-context", ReceiverID: dc.ReceiverID,
	}))

	// A client that names no receiver gets no digest.
	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go"}, "apply_patch", lessons, HookDelivery{
		Status: DeliveryInjected, SessionID: "provider-session-secret", ContextBytes: 90,
		ClientID: "codex", Mechanism: "pre-tool-use-context",
	}))

	// A receiver without a client is half an identity: no digest either.
	require.NoError(t, RecordHookDeliveryFiles(root, []string{"api/a.go"}, "Edit", lessons, HookDelivery{
		Status: DeliveryInjected, ContextBytes: 90, ReceiverID: "provider-receiver-secret",
	}))

	// No attribution writes the record shape of the earlier versions.
	require.NoError(t, RecordHookDelivery(root, "api/a.go", "Edit", lessons, HookDelivery{
		Status: DeliveryInjected, SessionID: "provider-session-secret", ContextBytes: 90,
	}))

	firings, err := ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 4)
	assert.Empty(t, firings[3].ContextSHA)

	assert.Equal(t, "claude", firings[0].Client)
	assert.Equal(t, "pre-tool-use-context", firings[0].Mechanism)
	assert.Equal(t, contextDigest(root, dc), firings[0].ContextSHA)
	assert.NotEqual(t, firings[0].SessionSHA, firings[0].ContextSHA)

	assert.Equal(t, "codex", firings[1].Client)
	assert.Empty(t, firings[1].ContextSHA, "the log never holds an identity that the client did not report")
	assert.Equal(t, firings[0].SessionSHA, firings[1].SessionSHA,
		"the session digest keeps its meaning: it does not include the client")

	assert.Empty(t, firings[2].Client)
	assert.Empty(t, firings[2].Mechanism)
	assert.Empty(t, firings[2].ContextSHA)

	raw, err := os.ReadFile(filepath.Join(root, ".seamark", auditFile))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "provider-receiver-secret")
	assert.NotContains(t, string(raw), "provider-session-secret")

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	assert.NotContains(t, lines[2], `"client":`, "an unattributed record adds no key")
	assert.NotContains(t, lines[2], `"mechanism":`)
	assert.NotContains(t, lines[2], `"context_sha256":`)
}

func TestSummarizeGroupsHookDeliveryByRecordedClient(t *testing.T) {
	lesson := FiredLesson{Region: "api", Symptom: "synchronize generated client"}
	hook := func(client, receiver string, status DeliveryStatus, contextBytes int) Firing {
		f := Firing{File: "api/a.py", Delivery: status, SessionSHA: "one-session-string", Generation: 1,
			ContextBytes: contextBytes, Fired: []FiredLesson{lesson}, Client: client, ContextSHA: receiver}
		if client != "" {
			f.Mechanism = "pre-tool-use-context"
		}

		return f
	}

	older := func() Firing {
		f := hook("", "", DeliveryInjected, 100)
		f.SessionSHA = "an-older-session"

		return f
	}

	firings := []Firing{
		// Records older than the attribution: one without a delivery
		// status, and two instrumented records of another session.
		{File: "api/a.py", Fired: []FiredLesson{lesson}},
		older(),
		older(),
		// Two clients report the same session string.
		hook("claude", "claude-receiver", DeliveryInjected, 200),
		hook("claude", "claude-receiver", DeliveryInjected, 200),
		hook("claude", "claude-receiver", DeliverySuppressedRepeat, 0),
		hook("codex", "", DeliveryInjected, 300),
		// Another surface never counts as hook delivery.
		{Surface: "check", Files: []string{"api/a.py", "db/b.py"}, Fired: []FiredLesson{lesson}},
	}

	s := Summarize(firings, nil)

	assert.Equal(t, 5, s.InstrumentedHookFirings)
	assert.Equal(t, 2, s.RepeatedHookFirings)
	assert.Equal(t, 1, s.SuppressedHookFirings)
	assert.Equal(t, 900, s.HookContextBytes)

	assert.Equal(t, []HookAttribution{
		{Client: "claude", Mechanism: "pre-tool-use-context", Injected: 2, Repeated: 1, Suppressed: 1, ContextBytes: 400},
		{Client: "codex", Mechanism: "pre-tool-use-context", Injected: 1, ContextBytes: 300},
		{Injected: 2, Repeated: 1, ContextBytes: 200},
	}, s.HookByClient, "named clients first, then the records that name no client")

	var injected, repeated, suppressed, contextBytes int
	for _, tally := range s.HookByClient {
		injected += tally.Injected
		repeated += tally.Repeated
		suppressed += tally.Suppressed
		contextBytes += tally.ContextBytes
	}

	assert.Equal(t, []int{s.InstrumentedHookFirings, s.RepeatedHookFirings, s.SuppressedHookFirings, s.HookContextBytes},
		[]int{injected, repeated, suppressed, contextBytes}, "the split adds up to the totals")

	// The first codex delivery is not a repeat of the claude delivery,
	// although both records carry one session digest.
	assert.Zero(t, s.HookByClient[1].Repeated)

	// A log without any client keeps the summary it always had.
	assert.Empty(t, Summarize(firings[:3], nil).HookByClient)

	// A mechanism without a client attributes nothing: one shared tally.
	stray := hook("", "", DeliveryInjected, 50)
	stray.Mechanism = "pre-tool-use-context"
	assert.Len(t, Summarize(append(firings, stray), nil).HookByClient, 3)
}

func TestSummarizeKeepsReceiverBoundariesAcrossTheAttributionUpgrade(t *testing.T) {
	// A record that names no client says nothing about the receiver. An
	// attributed record must never count as a repeat because of it.
	lesson := FiredLesson{Region: "api", Symptom: "synchronize generated client"}
	record := func(client, contextSHA string, generation uint64) Firing {
		return Firing{File: "api/a.py", Delivery: DeliveryInjected, SessionSHA: "one-session", Generation: generation,
			ContextBytes: 100, Fired: []FiredLesson{lesson}, Client: client, ContextSHA: contextSHA}
	}

	legacy := record("", "", 1)

	cases := []struct {
		name    string
		firings []Firing
		want    int
	}{
		{"a subagent of the session is another receiver",
			[]Firing{legacy, record("claude", "subagent-context", 1)}, 0},
		{"another client reports the same session string",
			[]Firing{legacy, record("codex", "", 1)}, 0},
		{"version 2 restarts the generation, so generation 1 is a later window",
			[]Firing{legacy, record("", "", 2), record("claude", "session-context", 1)}, 0},
		{"the same receiver after the upgrade does not join the older record",
			[]Firing{legacy, record("claude", "session-context", 1)}, 0},
		{"records of one receiver still join each other",
			[]Firing{legacy, legacy, record("claude", "session-context", 1), record("claude", "session-context", 1)}, 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, Summarize(tc.firings, nil).RepeatedHookFirings)
		})
	}
}

func TestExposureSurvivesCosmeticPinEdits(t *testing.T) {
	record := func(p PinRule) FiredLesson {
		// Exactly what RecordFiring persists: the raw rendered pair.
		l := SurfacedPin{Pin: p}.Lesson()

		return FiredLesson{Region: l.Region, Symptom: l.Symptom}
	}

	pin := PinRule{
		Rule: "Pool-Reset", Regions: []string{"pkg/b", "pkg/a"},
		Note: "reset in Free and clone",
	}

	exposure := FirstFirings([]Firing{
		{TS: "2026-08-01T10:00:00Z", Fired: []FiredLesson{record(pin)}},
		{TS: "2026-08-02T10:00:00Z", Fired: []FiredLesson{record(pin)}},
		{TS: "2026-08-03T10:00:00Z", Delivery: DeliverySuppressedRepeat,
			Fired: []FiredLesson{record(pin)}},
	})

	// Reordered regions and rule-case tweaks are the same pin: the
	// exposure clock survives the edit.
	edited := PinRule{
		Rule: "pool-reset", Regions: []string{"pkg/a", "pkg/b"},
		Note: "reset in Free and clone",
	}

	exp, ok := exposure[PinIdentity(edited)]
	require.True(t, ok)
	assert.Equal(t, 2, exp.Count, "a suppressed repeat is not an exposure")
	assert.Equal(t, 3, exp.Matches, "suppressed repeats remain visible as matching opportunities")
	assert.True(t, exp.First.Equal(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)))

	// A reworded note is a new treatment: the clock resets.
	reworded := pin
	reworded.Note = "reset in Free, deep-copy in clone"

	_, ok = exposure[PinIdentity(reworded)]
	assert.False(t, ok)
}
