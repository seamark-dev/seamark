package report

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/seamark-dev/seamark/internal/mcpusage"
)

// toolUsage renders usage to a string.
func toolUsage(usage mcpusage.Report) string {
	var b strings.Builder

	PrintToolUsage(&b, usage)

	return b.String()
}

func TestPrintToolUsageNamesItsUnits(t *testing.T) {
	out := toolUsage(mcpusage.Report{
		Present: true, Calls: 5, Failed: 1, Skipped: 2,
		Rows: []mcpusage.Row{
			{Tool: "change_set", Client: "claude-code", Calls: 4, Failed: 1, MedianMS: 12, MaxMS: 90, MedianBytes: 5120},
			{Tool: "why", Client: "codex", Calls: 1, MedianMS: 3, MaxMS: 3, MedianBytes: 40},
		},
	})

	assert.Equal(t, `
mcp tool usage — calls: 5, failed: 1 (counts calls, not whether a call helped)
  change_set   claude-code          calls: 4, failed: 1; latency median 12 ms, largest 90 ms; reply median 5120 bytes
  why          codex                calls: 1, failed: 0; latency median 3 ms, largest 3 ms; reply median 40 bytes
  lines skipped, because they do not parse: 2
`, out)
}

func TestPrintToolUsageHidesAZeroSkipCount(t *testing.T) {
	assert.NotContains(t, toolUsage(mcpusage.Report{Present: true}), "skipped")
}

func TestPrintToolUsageWithoutALogPrintsNothing(t *testing.T) {
	assert.Empty(t, toolUsage(mcpusage.Report{}))
}

func TestPrintToolUsageReadsAFailureAsUnknown(t *testing.T) {
	out := toolUsage(mcpusage.Report{Present: true, Err: errors.New("read x: denied\x1b[31m")})

	assert.Equal(t, "\nmcp tool usage — unknown: read x: denied[31m\n", out)
	assert.NotContains(t, out, "calls: 0", "an unreadable file is never zero")
}
