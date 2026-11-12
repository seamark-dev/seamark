package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/gate"
)

// The tests of a check whose diff names no changed file. Such a diff
// must never read as a clean allow.

// emptyHeadNote is the note for empty output of git diff HEAD.
const emptyHeadNote = "no changed files in git diff HEAD; untracked files are not part of it"

// noFileFixture builds an indexed repository with a clean working tree.
// The .gitignore keeps the index and the audit log out of git, so the
// tree stays clean after check writes them.
func noFileFixture(t *testing.T) string {
	t.Helper()

	root := writeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".seamark/\n"), 0o644))
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	return root
}

// auditBytes returns the audit log of root, or nil when it does not exist.
func auditBytes(t *testing.T, root string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "audit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return data
}

func TestCheckNotesAnEmptyGitDiff(t *testing.T) {
	root := noFileFixture(t)

	// An empty stdin falls back to git diff HEAD, as `check </dev/null`
	// does. The clean tree gives empty output.
	out, _, err := runIn(t, "", "-C", root, "check")
	require.NoError(t, err)
	assert.Equal(t, "verdict  allow (mode: warn)\n  note: "+emptyHeadNote+"\n", out)

	out, _, err = runIn(t, "", "-C", root, "check", "--json")
	require.NoError(t, err)

	var d gate.Decision
	require.NoError(t, json.Unmarshal([]byte(out), &d))
	assert.Equal(t, []string{emptyHeadNote}, d.Notes)

	// The note is not an error, so enforcement does not block it.
	_, _, err = runIn(t, "", "-C", root, "check", "--enforce")
	require.NoError(t, err)

	// git diff HEAD leaves out an untracked file, so the note is the same.
	require.NoError(t, os.WriteFile(filepath.Join(root, "extra.go"), []byte("package main\n"), 0o644))

	out, _, err = runIn(t, "", "-C", root, "check")
	require.NoError(t, err)
	assert.Equal(t, "verdict  allow (mode: warn)\n  note: "+emptyHeadNote+"\n", out)
}

func TestCheckNotesStdinWithNoFile(t *testing.T) {
	root := noFileFixture(t)

	out, _, err := runIn(t, "not a diff\n", "-C", root, "check")
	require.NoError(t, err)
	assert.Equal(t, "verdict  allow (mode: warn)\n"+
		"  note: stdin names no changed file (no diff --git, ---, +++ or rename header with a path); "+
		"this verdict covers no change\n", out)
}

func TestCheckEnforceBlocksStdinWithNoFile(t *testing.T) {
	root := noFileFixture(t)

	const want = "blocked by policy: stdin names no changed file " +
		"(no diff --git, ---, +++ or rename header with a path); " +
		"enforce mode blocks input that it cannot read as a diff"

	cases := []struct {
		name  string
		stdin string
		args  []string
	}{
		{name: "blank line", stdin: "\n", args: []string{"--enforce"}},
		{name: "text", stdin: "not a diff\n", args: []string{"--enforce"}},
		// The lesson checks run after the verdict, so a block skips them.
		{name: "text with lessons", stdin: "not a diff\n", args: []string{"--enforce", "--lessons"}},
	}

	// A check in warn mode writes the first audit entry. The cases then
	// show that a block adds no entry to a log that exists.
	_, _, err := runIn(t, "not a diff\n", "-C", root, "check")
	require.NoError(t, err)
	require.NotEmpty(t, auditBytes(t, root))

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := auditBytes(t, root)

			out, _, err := runIn(t, c.stdin, append([]string{"-C", root, "check"}, c.args...)...)
			require.ErrorIs(t, err, gate.ErrBlocked)
			assert.Equal(t, want, err.Error())
			assert.Empty(t, out, "a gate failure prints no verdict")
			assert.Equal(t, before, auditBytes(t, root), "a gate failure writes no audit entry")
		})
	}

	// The policy mode enforces as the flag does.
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "policy.yaml"), []byte("mode: enforce\n"), 0o644))

	_, _, err = runIn(t, "not a diff\n", "-C", root, "check")
	require.ErrorIs(t, err, gate.ErrBlocked)
	assert.Equal(t, want, err.Error())
}
