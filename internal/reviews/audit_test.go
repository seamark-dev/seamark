package reviews

import (
	"os"
	"path/filepath"
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
