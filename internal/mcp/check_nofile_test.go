package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tests of a check whose diff names no changed file. Such a diff
// must never read as a clean allow, and the server must give the CLI
// texts with the source of the diff in them.

// auditLog returns the audit log of root, or nil when it does not exist.
func auditLog(t *testing.T, root string) []byte {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(root, ".seamark", "audit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}

	require.NoError(t, err)

	return data
}

func TestCheckMCPNotesADiffWithNoFile(t *testing.T) {
	root, _ := lessonChecksFixture(t, "warn")

	// No diff argument: the server checks git diff HEAD of the clean tree.
	text, isErr := callCheck(t, root, `{}`)
	assert.False(t, isErr, text)
	assert.Equal(t, "verdict  allow (mode: warn)\n"+
		"  note: no changed files in git diff HEAD; untracked files are not part of it\n", text)

	text, isErr = callCheck(t, root, `{"diff":"not a diff"}`)
	assert.False(t, isErr, text)
	assert.Equal(t, "verdict  allow (mode: warn)\n"+
		"  note: the diff argument names no changed file (no diff --git, ---, +++ or rename header with a path); "+
		"this verdict covers no change\n", text)
}

func TestCheckMCPEnforceBlocksADiffWithNoFile(t *testing.T) {
	root, _ := lessonChecksFixture(t, "enforce")

	// The clean tree gives a first audit entry, so the block below
	// shows that it adds no entry to a log that exists.
	text, isErr := callCheck(t, root, `{}`)
	assert.False(t, isErr, text)

	before := auditLog(t, root)
	require.NotEmpty(t, before)

	// Whitespace is input with bytes in it, not an absent diff.
	for _, args := range []string{`{"diff":"not a diff"}`, `{"diff":" "}`} {
		text, isErr := callCheck(t, root, args)
		assert.True(t, isErr, "%s: a gate failure is a tool error, not a verdict", args)
		assert.Equal(t, "blocked by policy: the diff argument names no changed file "+
			"(no diff --git, ---, +++ or rename header with a path); "+
			"enforce mode blocks input that it cannot read as a diff", text, args)
		assert.Equal(t, before, auditLog(t, root), "%s: a gate failure writes no audit entry", args)
	}
}
