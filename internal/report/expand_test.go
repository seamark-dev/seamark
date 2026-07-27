package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestExpandLessonsRef(t *testing.T) {
	st, root := seedStore(t)

	var b strings.Builder
	require.NoError(t, Expand(&b, st, root, "lessons:scripts"))

	out := b.String()
	assert.Contains(t, out, "review lessons for scripts")
	assert.Contains(t, out, "RUF001")
	assert.Contains(t, out, "solitary finding",
		"below-threshold one-offs are the point of the raw view")
	assert.Contains(t, out, "propose a pin", "the promotion nudge rides along")

	// A region with nothing says so instead of dumping the whole repo.
	b.Reset()
	require.NoError(t, Expand(&b, st, root, "lessons:elsewhere"))
	assert.Contains(t, b.String(), "no review lessons under elsewhere")
}

func TestExpandLessonsCapped(t *testing.T) {
	st, root := seedStore(t)

	many := make([]model.Lesson, 0, expandLessonCap+30)
	for i := 0; i < expandLessonCap+30; i++ {
		many = append(many, model.Lesson{
			ClusterKey:  fmt.Sprintf("pkg/f%d.py\x00finding %d", i, i),
			Region:      fmt.Sprintf("pkg/f%d.py", i),
			Symptom:     fmt.Sprintf("finding number %d", i),
			Occurrences: 1, LastTS: int64(i),
		})
	}
	require.NoError(t, st.ReplaceLessons(many, nil))

	var b strings.Builder
	require.NoError(t, Expand(&b, st, root, "lessons:pkg"))
	assert.Contains(t, b.String(), "… 30 more",
		"an oversized ledger is capped with a narrowing hint, not dumped")
}

func TestParseSpanRef(t *testing.T) {
	cases := []struct {
		ref   string
		file  string
		start uint32
		end   uint32
		ok    bool
	}{
		{"a/b.go:12", "a/b.go", 12, 12, true},
		{"a/b.go:12-40", "a/b.go", 12, 40, true},
		{"Store.Rebuild", "", 0, 0, false},
		{"internal/store.Store.Rebuild", "", 0, 0, false},
		{"b.go:0", "", 0, 0, false},
		{"b.go:12abc", "", 0, 0, false},
		{"b.go:40-12", "", 0, 0, false /* reversed range */},
		{":12", "", 0, 0, false},
	}

	for _, tc := range cases {
		file, start, end, ok := parseSpanRef(tc.ref)

		assert.Equal(t, tc.ok, ok, "ref %q", tc.ref)

		if tc.ok {
			assert.Equal(t, tc.file, file, "ref %q", tc.ref)
			assert.Equal(t, tc.start, start, "ref %q", tc.ref)
			assert.Equal(t, tc.end, end, "ref %q", tc.ref)
		}
	}
}

func TestWriteRegionStaysInWorkspace(t *testing.T) {
	root := t.TempDir()

	var b strings.Builder

	// Refs are model-supplied input: an escape attempt must fail before
	// any file IO, not leak content from outside the workspace.
	err := writeRegion(&b, root, "../outside.txt", 1, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inside the workspace")

	err = writeRegion(&b, root, "/etc/hosts", 1, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inside the workspace")

	err = writeRegion(&b, root, "a/../../outside.txt", 1, 5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inside the workspace")
}

func TestWriteRegionRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	secret := filepath.Join(outside, "secret.txt")
	require.NoError(t, os.WriteFile(secret, []byte("PRIVATE KEY MATERIAL\n"), 0o600))

	// A symlink INSIDE the workspace pointing OUT of it: the ref string
	// stays clean ("link/secret.txt"), but following it must not leak the
	// target. Skip where symlinks are unavailable (non-root Windows).
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	var b strings.Builder

	err := writeRegion(&b, root, "link/secret.txt", 1, 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outside the workspace")
	assert.NotContains(t, b.String(), "PRIVATE KEY MATERIAL")
}

func TestWriteRegionAllowsRealFile(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "real.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644))

	var b strings.Builder

	// The guard must not block legitimate reads — including when root
	// itself sits under a symlinked prefix (t.TempDir on macOS is under
	// /var -> /private/var).
	require.NoError(t, writeRegion(&b, root, "real.go", 1, 3))
	assert.Contains(t, b.String(), "func main()")
}
