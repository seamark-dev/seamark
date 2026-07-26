package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
