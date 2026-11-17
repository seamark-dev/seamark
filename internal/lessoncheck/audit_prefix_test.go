package lessoncheck

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests cover the bounded audit reader that the delivery report uses.
// Every root is a temporary directory.

// prefixRecord returns one valid record line with its newline. The
// discard cause is the marker that tells records apart in a test.
func prefixRecord(t *testing.T, marker string) string {
	t.Helper()

	rec := AuditRecord{Schema: AuditSchema, TS: "2026-10-26T00:00:00Z", Kind: AuditBaselineDiscarded, Caller: CallerHook, Discarded: marker}
	line, err := json.Marshal(rec)
	require.NoError(t, err)

	return string(line) + "\n"
}

// writeGeneration writes content to the live log (rotated false) or the
// rotated generation (rotated true) of root.
func writeGeneration(t *testing.T, root string, rotated bool, content string) {
	t.Helper()

	path := auditPath(root)
	if rotated {
		path += ".1"
	}

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// markers returns the discard markers of the records, in order.
func markers(p AuditPrefix) []string {
	out := make([]string, 0, len(p.Records))
	for _, r := range p.Records {
		out = append(out, r.Discarded)
	}

	return out
}

// TestLessonCheckReadAuditPrefixGenerations tests the bounded reader. It
// covers the order of the generations, the single bound, the counts, the
// partial tail, and the read-only guarantee. It also covers the one
// reopen after a rotation and the rule that an I/O error is never an
// empty stream.
func TestLessonCheckReadAuditPrefixGenerations(t *testing.T) {
	t.Run("a missing stream is an empty prefix and creates nothing", func(t *testing.T) {
		root := t.TempDir()

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.Equal(t, AuditPrefix{Path: auditPath(root)}, got)
		assert.Zero(t, got.Generations)

		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		assert.Empty(t, entries, "a read creates no .seamark directory")
	})

	t.Run("the live file alone is one generation", func(t *testing.T) {
		root := t.TempDir()
		content := prefixRecord(t, "live-1") + prefixRecord(t, "live-2")
		writeGeneration(t, root, false, content)

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.Equal(t, 1, got.Generations)
		assert.Equal(t, int64(len(content)), got.Bytes)
		assert.Equal(t, []string{"live-1", "live-2"}, markers(got))
		assert.False(t, got.OverLimit)
	})

	t.Run("the rotated generation comes before the live file", func(t *testing.T) {
		root := t.TempDir()
		rotated := prefixRecord(t, "old-1") + prefixRecord(t, "old-2")
		live := prefixRecord(t, "new-1")
		writeGeneration(t, root, true, rotated)
		writeGeneration(t, root, false, live)

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.Equal(t, 2, got.Generations)
		assert.Equal(t, int64(len(rotated)+len(live)), got.Bytes)
		assert.Equal(t, []string{"old-1", "old-2", "new-1"}, markers(got))
	})

	t.Run("a rotated generation without a live file is one generation", func(t *testing.T) {
		root := t.TempDir()
		writeGeneration(t, root, true, prefixRecord(t, "old-1"))

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.Equal(t, 1, got.Generations)
		assert.Equal(t, []string{"old-1"}, markers(got))
	})

	t.Run("one bound covers both generations", func(t *testing.T) {
		root := t.TempDir()
		rotated := prefixRecord(t, "old-1") + prefixRecord(t, "old-2")
		live := prefixRecord(t, "new-1") + prefixRecord(t, "new-2")
		writeGeneration(t, root, true, rotated)
		writeGeneration(t, root, false, live)

		// The bound ends after the first live record.
		first := int64(len(prefixRecord(t, "new-1")))
		bound := int64(len(rotated)) + first

		got, err := ReadAuditPrefix(root, bound)
		require.NoError(t, err)
		assert.True(t, got.OverLimit)
		assert.Equal(t, bound, got.Bytes)
		assert.False(t, got.PartialTail)
		assert.Equal(t, []string{"old-1", "old-2", "new-1"}, markers(got))

		// A bound inside the rotated generation leaves the live file out.
		got, err = ReadAuditPrefix(root, int64(len(prefixRecord(t, "old-1"))))
		require.NoError(t, err)
		assert.True(t, got.OverLimit)
		assert.Equal(t, []string{"old-1"}, markers(got))
		assert.Equal(t, 2, got.Generations, "both files exist; the bound left the live bytes unread")

		// A bound equal to both sizes is not over the limit.
		got, err = ReadAuditPrefix(root, int64(len(rotated)+len(live)))
		require.NoError(t, err)
		assert.False(t, got.OverLimit)
		assert.Len(t, got.Records, 4)

		// A zero bound reads nothing.
		got, err = ReadAuditPrefix(root, 0)
		require.NoError(t, err)
		assert.True(t, got.OverLimit)
		assert.Empty(t, got.Records)
		assert.Zero(t, got.Bytes)
	})

	t.Run("other schemas, malformed, over-long, and blank lines are counted", func(t *testing.T) {
		root := t.TempDir()
		overLong := `{"schema":"` + AuditSchema + `","error":"` + strings.Repeat("x", 1<<20+10) + `"}` + "\n"
		content := prefixRecord(t, "keep") +
			"\n" +
			`{"schema":"lesson-check-audit/2","kind":"verify","caller":{"changed":"shape"}}` + "\n" +
			`{}` + "\n" +
			`not json` + "\n" +
			`{"schema":"` + AuditSchema + `","caller":7}` + "\n" +
			overLong
		writeGeneration(t, root, false, content)

		got, err := ReadAuditPrefix(root, int64(len(content)))

		require.NoError(t, err)
		assert.Equal(t, []string{"keep"}, markers(got))
		assert.Equal(t, 1, got.NewerSchema, "another schema value")
		assert.Equal(t, 4, got.Malformed, "no schema, not json, a bad field type, an over-long line")
		assert.False(t, got.PartialTail)
	})

	t.Run("a newer record that also decodes as the current shape counts as newer", func(t *testing.T) {
		root := t.TempDir()
		content := `{"schema":"lesson-check-audit/2","kind":"verify","caller":"cli"}` + "\n" + prefixRecord(t, "keep")
		writeGeneration(t, root, false, content)

		got, err := ReadAuditPrefix(root, int64(len(content)))

		require.NoError(t, err)
		assert.Equal(t, []string{"keep"}, markers(got))
		assert.Equal(t, 1, got.NewerSchema, "the schema decides, also when every field fits AuditRecord")
		assert.Zero(t, got.Malformed)
	})

	t.Run("a tail without a newline is ignored", func(t *testing.T) {
		root := t.TempDir()
		complete := prefixRecord(t, "keep")
		writeGeneration(t, root, false, complete+`{"schema":"lesson-che`)

		got, err := ReadAuditPrefix(root, 1<<20)

		require.NoError(t, err)
		assert.True(t, got.PartialTail)
		assert.Equal(t, []string{"keep"}, markers(got))
		assert.Zero(t, got.Malformed)
	})

	t.Run("a read writes nothing and never touches the lock", func(t *testing.T) {
		root := t.TempDir()
		writeGeneration(t, root, true, prefixRecord(t, "old-1"))
		writeGeneration(t, root, false, prefixRecord(t, "new-1"))

		before := listTree(t, root)

		_, err := ReadAuditPrefix(root, 1<<20)
		require.NoError(t, err)

		assert.Equal(t, before, listTree(t, root), "no file, directory, or mode change")
		assert.NoFileExists(t, auditPath(root)+".lock")
	})

	t.Run("a rotation between the opens costs one reopen", func(t *testing.T) {
		root := t.TempDir()
		writeGeneration(t, root, false, prefixRecord(t, "before"))

		var attempts []int

		// The function runs after the live file opens. It rotates the log
		// as the writer does: the live file moves to .1 and a new live
		// file starts. The second open then returns the file that the
		// first open holds.
		between := func(attempt int) {
			attempts = append(attempts, attempt)

			if attempt == 0 {
				require.NoError(t, os.Rename(auditPath(root), auditPath(root)+".1"))
				writeGeneration(t, root, false, prefixRecord(t, "after"))
			}
		}

		got, err := readAuditPrefixAt(auditPath(root), 1<<20, between)

		require.NoError(t, err)
		assert.Equal(t, []int{0, 1}, attempts, "one reopen")
		assert.Equal(t, 2, got.Generations)
		assert.Equal(t, []string{"before", "after"}, markers(got), "no record is lost or read twice")
	})

	t.Run("a second rotation returns rotated during read", func(t *testing.T) {
		root := t.TempDir()
		writeGeneration(t, root, false, prefixRecord(t, "before"))

		calls := 0
		between := func(int) {
			calls++

			require.NoError(t, os.Rename(auditPath(root), auditPath(root)+".1"))
			writeGeneration(t, root, false, prefixRecord(t, "after"))
		}

		got, err := readAuditPrefixAt(auditPath(root), 1<<20, between)

		require.Error(t, err)
		assert.ErrorIs(t, err, errRotatedDuringRead)
		assert.Contains(t, err.Error(), "rotated during read")
		assert.Equal(t, 2, calls, "one reopen, no third try")
		assert.Equal(t, AuditPrefix{}, got)
	})

	t.Run("an unreadable file returns an error, not an empty prefix", func(t *testing.T) {
		if os.Geteuid() == 0 || !posixModes {
			t.Skip("modes do not deny this reader")
		}

		for _, rotated := range []bool{false, true} {
			root := t.TempDir()
			writeGeneration(t, root, false, prefixRecord(t, "new-1"))
			writeGeneration(t, root, true, prefixRecord(t, "old-1"))

			path := auditPath(root)
			if rotated {
				path += ".1"
			}

			require.NoError(t, os.Chmod(path, 0o000))

			got, err := ReadAuditPrefix(root, 1<<20)

			require.Error(t, err, "rotated=%v", rotated)
			assert.ErrorIs(t, err, os.ErrPermission)
			assert.Equal(t, AuditPrefix{}, got)
			assert.Contains(t, err.Error(), "lesson-check audit: ", "the error names its source")

			require.NoError(t, os.Chmod(path, 0o600))
		}
	})

	t.Run("a symbolic link is refused, as the writer refuses it", func(t *testing.T) {
		if !posixModes {
			t.Skip("symbolic links need POSIX")
		}

		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "elsewhere.jsonl")
		require.NoError(t, os.WriteFile(target, []byte(prefixRecord(t, "outside")), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Dir(auditPath(root)), 0o700))
		require.NoError(t, os.Symlink(target, auditPath(root)))

		got, err := ReadAuditPrefix(root, 1<<20)

		require.Error(t, err)
		assert.Equal(t, AuditPrefix{}, got)
	})

	t.Run("a path that is not a stream returns an error", func(t *testing.T) {
		// .seamark is a file, so the open fails with ENOTDIR. That error
		// is not os.ErrNotExist.
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark"), nil, 0o600))

		_, err := ReadAuditPrefix(root, 1<<20)

		require.Error(t, err)
		assert.NotErrorIs(t, err, os.ErrNotExist)
	})
}
