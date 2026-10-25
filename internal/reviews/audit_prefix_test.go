package reviews

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePrefixLog writes content as the firing log of a new root and
// returns the root.
func writePrefixLog(t *testing.T, content string) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(AuditLogPath(root), []byte(content), 0o644))

	return root
}

// prefixLogLines returns the lines of the mixed log that the bounds test
// reads. The comment after each line gives its line number and its class.
func prefixLogLines() []string {
	overLong := `{"ts":"` + strings.Repeat("x", maxAuditLine+10) + `"}`

	return []string{
		`{"ts":"2026-10-01T00:00:00Z","file":"a.go","fired":[]}`, // 1 legacy
		``, // 2 blank
		`{"ts":"2026-10-02T00:00:00Z","schema_version":2,"event_id":"e1","fired":[]}`,   // 3 v2
		`{"schema_version":3,"event_id":"e2","fired":"changed type in a newer schema"}`, // 4 newer
		`this is not json`, // 5 malformed
		overLong,           // 6 over-long, malformed
		`{"ts":"2026-10-03T00:00:00Z","schema_version":1,"fired":[]}`,                 // 7 v1, not legacy
		`{"ts":"2026-10-04T00:00:00Z","schema_version":-1,"fired":[]}`,                // 8 negative, malformed
		`{"ts":"2026-10-05T00:00:00Z","schema_version":2,"event_id":"e3","fired":[]}`, // 9 v2
	}
}

// TestReadAuditPrefixBoundsAndCounts tests the bounded prefix reader of
// the firing log (contract L3). It covers the absent log, the line
// classes and their counts, the 1-based line numbers, the partial tail,
// and the byte bound. It also covers the rule that an I/O error is never
// an empty log.
func TestReadAuditPrefixBoundsAndCounts(t *testing.T) {
	t.Run("an absent log is an empty prefix and creates nothing", func(t *testing.T) {
		root := t.TempDir()

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.Equal(t, AuditPrefix{Path: AuditLogPath(root)}, got)

		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries, "a read writes no file and no directory")
	})

	t.Run("lines are classified and numbered by physical line", func(t *testing.T) {
		content := strings.Join(prefixLogLines(), "\n") + "\n"
		root := writePrefixLog(t, content)

		got, err := ReadAuditPrefix(root, int64(len(content)))

		require.NoError(t, err)
		assert.Equal(t, AuditLogPath(root), got.Path)
		assert.Equal(t, int64(len(content)), got.Bytes)
		assert.False(t, got.OverLimit)
		assert.False(t, got.PartialTail)
		assert.Equal(t, 1, got.Legacy, "legacy is a subset of the records")
		assert.Equal(t, 3, got.Malformed, "not json, over-long, negative version")
		assert.Equal(t, 1, got.NewerSchema)

		lines := make([]int, 0, len(got.Lines))
		for _, l := range got.Lines {
			lines = append(lines, l.Line)
		}

		assert.Equal(t, []int{1, 3, 7, 9}, lines, "blank lines advance the counter; newer and malformed lines are out")
		assert.Equal(t, "a.go", got.Lines[0].Firing.File)
		assert.Equal(t, "e3", got.Lines[3].Firing.EventID)
	})

	t.Run("a tail without a newline is ignored", func(t *testing.T) {
		complete := `{"ts":"t","schema_version":2,"event_id":"e1","fired":[]}` + "\n"
		root := writePrefixLog(t, complete+`{"ts":"t","sche`)

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.True(t, got.PartialTail)
		require.Len(t, got.Lines, 1)
		assert.Equal(t, 0, got.Malformed, "an incomplete tail is not a malformed line")
		assert.Equal(t, int64(len(complete))+int64(len(`{"ts":"t","sche`)), got.Bytes)
	})

	t.Run("a complete last line needs no partial tail", func(t *testing.T) {
		root := writePrefixLog(t, `{"ts":"t","fired":[]}`+"\n")

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.False(t, got.PartialTail)
		assert.Equal(t, 1, got.Legacy)
	})

	t.Run("a bound below the size cuts the prefix", func(t *testing.T) {
		first := `{"ts":"t","fired":[]}` + "\n"
		second := `{"ts":"t","schema_version":2,"event_id":"e1","fired":[]}` + "\n"
		root := writePrefixLog(t, first+second)

		// The bound ends at a line end, so it splits no line.
		got, err := ReadAuditPrefix(root, int64(len(first)))
		require.NoError(t, err)
		assert.True(t, got.OverLimit)
		assert.False(t, got.PartialTail)
		assert.Equal(t, int64(len(first)), got.Bytes)
		assert.Len(t, got.Lines, 1)

		// The bound ends inside the second line, so that tail is partial.
		cut := int64(len(first)) + 10
		got, err = ReadAuditPrefix(root, cut)
		require.NoError(t, err)
		assert.True(t, got.OverLimit)
		assert.True(t, got.PartialTail)
		assert.Equal(t, cut, got.Bytes)
		assert.Len(t, got.Lines, 1)

		// A bound equal to the size is not over the limit.
		got, err = ReadAuditPrefix(root, int64(len(first+second)))
		require.NoError(t, err)
		assert.False(t, got.OverLimit)
		assert.Len(t, got.Lines, 2)
	})

	t.Run("a zero or negative bound reads nothing", func(t *testing.T) {
		root := writePrefixLog(t, `{"ts":"t","fired":[]}`+"\n")

		for _, bound := range []int64{0, -5} {
			got, err := ReadAuditPrefix(root, bound)

			require.NoError(t, err)
			assert.True(t, got.OverLimit)
			assert.Zero(t, got.Bytes)
			assert.Empty(t, got.Lines)
		}
	})

	t.Run("an empty log is an empty prefix, whatever the bound", func(t *testing.T) {
		root := writePrefixLog(t, "")

		for _, bound := range []int64{1 << 20, 0, -1} {
			got, err := ReadAuditPrefix(root, bound)

			require.NoError(t, err)
			assert.Equal(t, AuditPrefix{Path: AuditLogPath(root)}, got, "bound %d: an empty log is never over the limit", bound)
		}
	})

	t.Run("an unreadable log returns an error, not an empty prefix", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file")
		}

		root := writePrefixLog(t, `{"ts":"t","fired":[]}`+"\n")
		require.NoError(t, os.Chmod(AuditLogPath(root), 0o000))
		t.Cleanup(func() { _ = os.Chmod(AuditLogPath(root), 0o644) })

		got, err := ReadAuditPrefix(root, 1<<20)

		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrPermission)
		assert.Equal(t, AuditPrefix{}, got)
		assert.Contains(t, err.Error(), "firing log: ", "the error names its source")
	})

	t.Run("a path that is not a log returns an error", func(t *testing.T) {
		// .seamark is a file, so the open fails with ENOTDIR. That error
		// is not os.ErrNotExist. A directory at the log path fails the
		// read.
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark"), nil, 0o644))

		_, err := ReadAuditPrefix(root, 1<<20)
		require.Error(t, err)
		assert.NotErrorIs(t, err, os.ErrNotExist)

		root = t.TempDir()
		require.NoError(t, os.MkdirAll(AuditLogPath(root), 0o755))

		_, err = ReadAuditPrefix(root, 1<<20)
		require.Error(t, err)
	})
}
