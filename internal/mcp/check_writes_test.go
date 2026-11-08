package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// checkDescription returns the description of the check tool.
func checkDescription(t *testing.T) string {
	t.Helper()

	for _, def := range toolDefs {
		if def["name"] == "check" {
			text, ok := def["description"].(string)
			require.True(t, ok)

			return text
		}
	}

	require.Fail(t, "no check tool")

	return ""
}

// TestCheckDescriptionNamesItsWrites keeps every write of a normal check
// run in the description. An agent reads only the description before it
// calls check as a probe.
func TestCheckDescriptionNamesItsWrites(t *testing.T) {
	text := checkDescription(t)

	for _, want := range []string{
		"refreshes the index", "audit.jsonl", "lessons-audit.jsonl", "lesson_checks", "lesson-check-audit.jsonl",
	} {
		assert.Contains(t, text, want)
	}
}

// TestDataFlowNamesCheckWrites keeps the four data-flow rows that declare
// the writes of check in agreement. The rows drifted apart before.
func TestDataFlowNamesCheckWrites(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "data-flow.md"))
	require.NoError(t, err)

	rows := map[string]string{}

	for line := range strings.SplitSeq(string(data), "\n") {
		for _, prefix := range []string{
			"| Diff check (`seamark check`) |", "| MCP server (`seamark mcp`) |", "| `check` |", "| `mcp` |",
		} {
			if strings.HasPrefix(line, prefix) {
				rows[prefix] = line
			}
		}
	}

	require.Len(t, rows, 4, "every row exists")

	for prefix, row := range rows {
		for _, file := range []string{"audit.jsonl", "lessons-audit.jsonl", "lesson-check-audit.jsonl"} {
			assert.Contains(t, row, "`"+file+"`", "%s names %s", prefix, file)
		}
	}
}
