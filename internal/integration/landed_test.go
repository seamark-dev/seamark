package integration

// Tests of Landed and SameGuard, the readback and the staleness check
// that every Phase 4 editor shares.

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLandedReportsTheThreeOutcomes(t *testing.T) {
	root := t.TempDir()
	rel := ".seamark/lessons.yaml"
	path := filepath.Join(root, filepath.FromSlash(rel))

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("pin: []\n"), 0o644))

	written, err := Landed(root, rel, sha256.Sum256([]byte("pin: []\n")))
	require.NoError(t, err)
	assert.True(t, written, "the planned bytes are in the file")

	written, err = Landed(root, rel, sha256.Sum256([]byte("other\n")))
	require.NoError(t, err)
	assert.False(t, written, "the file holds other bytes")

	require.NoError(t, os.Remove(path))

	written, err = Landed(root, rel, sha256.Sum256(nil))
	require.NoError(t, err)
	assert.False(t, written, "an absent file never holds the planned bytes, even empty ones")

	_, err = Landed(root, "../outside", sha256.Sum256(nil))
	assert.Error(t, err, "an unreadable path is an unknown outcome, never written or not written")
}

func TestSameGuardComparesEveryObservedField(t *testing.T) {
	base := FileGuard{Path: "a", Exists: true, SHA256: [32]byte{1}, Mode: 0o644}

	assert.True(t, SameGuard(base, base))

	changes := map[string]func(*FileGuard){
		"existence":    func(g *FileGuard) { g.Exists = false },
		"content":      func(g *FileGuard) { g.SHA256 = [32]byte{2} },
		"mode":         func(g *FileGuard) { g.Mode = 0o600 },
		"link":         func(g *FileGuard) { g.Linked = ".seamark" },
		"write access": func(g *FileGuard) { g.Denied = true },
	}

	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			other := base
			change(&other)
			assert.False(t, SameGuard(base, other))
		})
	}
}
