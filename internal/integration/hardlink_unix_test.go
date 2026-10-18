//go:build unix

package integration

// Tests of HardLinked, the refusal of a file with a second name.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHardLinkedFindsASecondName(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lessons.yaml")

	linked, err := HardLinked(root, "lessons.yaml")
	require.NoError(t, err)
	assert.False(t, linked, "an absent file has no link")

	require.NoError(t, os.WriteFile(path, []byte("pin: []\n"), 0o644))

	linked, err = HardLinked(root, "lessons.yaml")
	require.NoError(t, err)
	assert.False(t, linked, "one name is the normal case")

	require.NoError(t, os.Link(path, filepath.Join(root, "other.yaml")))

	linked, err = HardLinked(root, "lessons.yaml")
	require.NoError(t, err)
	assert.True(t, linked, "a rename would leave the second name with the old bytes")
}
