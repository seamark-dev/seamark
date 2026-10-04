package integration

// Tests of the exported diff. A planner that cannot write, for example
// because of a link, renders its change through UnifiedDiff. The text
// must be the same that PreviewWrites shows for a write it can make.

import (
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardedCommon is a common document that replaces the file at rel with
// after, as the settings and pin editors do (Route A).
func guardedCommon(rel string, after []byte) Document {
	return Document{
		Path: rel, Detail: "update", KeptDetail: "no change",
		Compose: func([]byte, bool) ([]byte, error) { return after, nil },
	}
}

func TestUnifiedDiffEqualsTheWritePreview(t *testing.T) {
	cases := []struct {
		name, rel, before, after string
	}{
		{"one changed line", ".seamark/lessons.yaml", "pin:\n  - rule: a\n    region: api\n", "pin:\n  - rule: a\n    region: db\n"},
		{"removed entry", ".seamark/lessons.yaml", "pin:\n  - rule: a\n  - rule: b\n", "pin:\n  - rule: b\n"},
		{"no final newline", "doc.txt", "a\nb", "a\nb\n"},
		{"structured file", "a.json", "{\"token\": \"" + liveSecret + "\"}\n", "{\"token\": \"" + liveSecret + "\", \"x\": 1}\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeRel(t, root, tc.rel, tc.before)

			plan := mustPlan(t, Builtin(), SetupRequest{
				Root: root, Common: []Document{guardedCommon(tc.rel, []byte(tc.after))},
			})
			want := previewOf(t, plan, tc.rel)

			diff, added, removed := UnifiedDiff(tc.rel, []byte(tc.before), []byte(tc.after))

			assert.Equal(t, want.Diff, diff)
			assert.Equal(t, want.Added, added)
			assert.Equal(t, want.Removed, removed)
		})
	}
}

func TestUnifiedDiffNeedsNoFileAndNoPlan(t *testing.T) {
	// A linked file cannot be planned for a write, so the plan path has no
	// preview. The exported diff works on bytes alone.
	diff, added, removed := UnifiedDiff(".seamark/lessons.yaml", []byte("pin:\n  - rule: a\n"), []byte("pin:\n"))

	assert.Equal(t, "--- a/.seamark/lessons.yaml\n+++ b/.seamark/lessons.yaml\n@@ -1,2 +1 @@\n pin:\n-  - rule: a\n", diff)
	assert.Zero(t, added)
	assert.Equal(t, 1, removed)
}

func TestUnifiedDiffOfEqualContentIsEmpty(t *testing.T) {
	diff, added, removed := UnifiedDiff("doc.txt", []byte("same\n"), []byte("same\n"))

	assert.Empty(t, diff)
	assert.Zero(t, added)
	assert.Zero(t, removed)
}

func TestUnifiedDiffRedactsAndSanitizes(t *testing.T) {
	// A pin note is repository text: it can hold escape sequences and a
	// secret-shaped value. The display diff hides both, and the bytes to
	// write stay as they are.
	before := []byte("pin:\n  - rule: a\n")
	after := []byte("pin:\n  - rule: a\n    note: \"password: hunter2 \x1b[31mred\x1b]0;t\x07 \u009b2J\u202e\"\n")

	diff, added, removed := UnifiedDiff(".seamark/lessons.yaml", before, after)

	assert.NotContains(t, diff, "hunter2")
	assert.NotContains(t, diff, "\x1b")
	assert.NotContains(t, diff, "\x07")
	assert.NotContains(t, diff, "\u009b")
	assert.NotContains(t, diff, "\u202e")
	assert.Contains(t, diff, "password: [REDACTED]")
	assert.Equal(t, 1, added)
	assert.Zero(t, removed)
	assert.Contains(t, string(after), "hunter2", "the input bytes are not changed")
}

func TestUnifiedDiffMasksStructuredCredentials(t *testing.T) {
	// The structured masking follows the file extension, as in a preview.
	diff, _, _ := UnifiedDiff("a.json", []byte("{}\n"), []byte("{\"token\": \""+liveSecret+"\"}\n"))

	assert.NotContains(t, diff, liveSecret)
	assert.Contains(t, diff, "[REDACTED]")
}

func TestGuardedCommonDocumentWritesTheExactBytes(t *testing.T) {
	// The helper above must behave like a real Route A document, or the
	// equality test would compare against the wrong preview.
	root := t.TempDir()
	writeRel(t, root, "doc.txt", "old\n")

	plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{guardedCommon("doc.txt", []byte("new\n"))}})
	require.Len(t, plan.Reads, 1)
	assert.Equal(t, sha256.Sum256([]byte("old\n")), plan.Reads[0].SHA256)

	_, err := ApplySetup(plan, ApplyOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new\n", readRel(t, root, "doc.txt"))
}
