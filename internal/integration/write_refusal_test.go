package integration

// Tests of the exported write refusal. The pin and settings editors show
// it before they plan a write, so it must be the refusal text of the
// engine itself.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteRefusalNamesTheLinkAndTheReadOnlyFile(t *testing.T) {
	cases := []struct {
		name  string
		guard FileGuard
		want  string
	}{
		{"a regular file", FileGuard{Exists: true, Mode: 0o644}, ""},
		{"a new file", FileGuard{}, ""},
		{"a link", FileGuard{Exists: true, Mode: 0o644, Linked: ".seamark"}, "symlink at .seamark; seamark writes only real paths inside the repository"},
		{"a read-only file", FileGuard{Exists: true, Mode: 0o444}, "the file is read-only (-r--r--r--); seamark does not replace it"},
		{"a read-only link", FileGuard{Exists: true, Mode: 0o444, Linked: "a.txt"}, "symlink at a.txt; seamark writes only real paths inside the repository"},
		{"a file this user cannot write", FileGuard{Exists: true, Mode: 0o644, Denied: true}, "this user cannot write the file; seamark does not replace it"},
		{"a read-only file this user cannot write", FileGuard{Exists: true, Mode: 0o444, Denied: true}, "the file is read-only (-r--r--r--); seamark does not replace it"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, WriteRefusal(tc.guard))
		})
	}
}

func TestWriteRefusalIsTheEngineRefusal(t *testing.T) {
	t.Run("a link", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, "real.txt", "old\n")
		require.NoError(t, os.Symlink("real.txt", filepath.Join(root, "doc.txt")))

		guard, _, err := ReadInput(root, "doc.txt")
		require.NoError(t, err)
		require.NotEmpty(t, WriteRefusal(guard))

		_, err = PlanSetup(Builtin(), SetupRequest{Root: root, Common: []Document{guardedCommon("doc.txt", []byte("new\n"))}})
		require.EqualError(t, err, "doc.txt: "+WriteRefusal(guard))
	})

	t.Run("a read-only file", func(t *testing.T) {
		root := t.TempDir()
		writeRel(t, root, "doc.txt", "old\n")
		require.NoError(t, os.Chmod(filepath.Join(root, "doc.txt"), 0o444))

		plan := mustPlan(t, Builtin(), SetupRequest{Root: root, Common: []Document{guardedCommon("doc.txt", []byte("new\n"))}})
		require.NotEmpty(t, WriteRefusal(plan.Reads[0]))

		_, err := ApplySetup(plan, ApplyOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), WriteRefusal(plan.Reads[0]))
		assert.Equal(t, "old\n", readRel(t, root, "doc.txt"))
	})
}
