package mcp

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/reviews"
)

// checkCall is one check request line with the given id and no diff, so
// the check reads the working tree against HEAD.
const checkCall = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"check","arguments":{}}}`

// TestFiringLogLinkKeepsTheAnswers proves that change_set and check
// still answer when .seamark/lessons-audit.jsonl is a symbolic link to a
// file outside the repository, and that no record reaches that file.
// Before the fix, both calls appended a record through the link. A
// control session first shows that both calls write a record to a real
// log, so the empty target cannot pass by writing nothing.
func TestFiringLogLinkKeepsTheAnswers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symbolic links need extra rights on windows")
	}

	root := changeSetPrepFixture(t)

	// The modified pinned file gives check a lesson to print.
	fixtureWrite(t, root, "internal/parse/strict.go",
		"package parse\n\n// Strict rejects unknown fields.\nfunc Strict(fields []string) error { return nil }\n\n// Loose is new.\nfunc Loose() {}\n")

	session := []string{initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"), checkCall}

	control := serve(t, root, session...)
	controlSet := decodeToolParts(t, control["1"])
	controlCheck := decodeToolParts(t, control["2"])

	require.NotEmpty(t, structuredPreparation(t, controlSet).EventID, "the control call records its event")

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	surfaces := map[string]bool{}
	for _, f := range firings {
		surfaces[f.Surface] = true
	}

	require.True(t, surfaces["change_set"] && surfaces["check"],
		"both calls write a firing record to a real log: %v", surfaces)

	sentinel := filepath.Join(t.TempDir(), "outside.jsonl")
	require.NoError(t, os.WriteFile(sentinel, nil, 0o644))
	require.NoError(t, os.Remove(reviews.AuditLogPath(root)))
	require.NoError(t, os.Symlink(sentinel, reviews.AuditLogPath(root)))

	resps := serve(t, root, session...)

	set := decodeToolParts(t, resps["1"])
	assert.False(t, set.IsError, "a refused firing record never fails change_set")
	require.Len(t, set.Content, len(controlSet.Content))
	assert.Equal(t, controlSet.Content[0].Text, set.Content[0].Text, "change_set gives its normal answer")
	assert.Empty(t, structuredPreparation(t, set).EventID, "a refused record leaves no event_id")

	check := decodeToolParts(t, resps["2"])
	assert.False(t, check.IsError, "a refused firing record never fails check")
	require.NotEmpty(t, check.Content)
	assert.Equal(t, controlCheck.Content[0].Text, check.Content[0].Text, "check gives its normal answer")

	data, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Empty(t, string(data), "no record reaches the file outside the repository")

	info, err := os.Lstat(reviews.AuditLogPath(root))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "the link stays as it was")
}
