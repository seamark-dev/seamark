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
	}

	surfaced := []model.Lesson{
		{Region: "scripts", Symptom: "E702"},
		{Region: "scripts", Symptom: "RUF001"},
		{Region: "api", Symptom: "E501"}, // never fired
	}

	s := Summarize(firings, surfaced)

	assert.Equal(t, 2, s.Total, "two firing events")
	assert.Equal(t, 2, s.Files, "two distinct files")

	require.NotEmpty(t, s.Ranked)
	assert.Equal(t, "E702", s.Ranked[0].Symptom, "most-fired first")
	assert.Equal(t, 2, s.Ranked[0].Count)
	assert.Equal(t, "2026-07-02T00:00:00Z", s.Ranked[0].LastTS, "latest firing wins")

	require.Len(t, s.NeverFired, 1)
	assert.Equal(t, "E501", s.NeverFired[0].Symptom, "surfaced but never fired = decay candidate")
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
	})

	// Reordered regions and rule-case tweaks are the same pin: the
	// exposure clock survives the edit.
	edited := PinRule{
		Rule: "pool-reset", Regions: []string{"pkg/a", "pkg/b"},
		Note: "reset in Free and clone",
	}

	exp, ok := exposure[PinIdentity(edited)]
	require.True(t, ok)
	assert.Equal(t, 2, exp.Count)
	assert.True(t, exp.First.Equal(time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)))

	// A reworded note is a new treatment: the clock resets.
	reworded := pin
	reworded.Note = "reset in Free, deep-copy in clone"

	_, ok = exposure[PinIdentity(reworded)]
	assert.False(t, ok)
}
