package report

import (
	"fmt"
	"io"

	"github.com/seamark-dev/seamark/internal/mcpusage"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/render"
)

// PrintToolUsage renders the section of MCP tool calls of `lessons
// --stats`. Without a usage log it prints nothing, so the rest of the
// output keeps its bytes. A failed read prints unknown and never a zero
// count, because zero calls and an unreadable file have different
// meanings.
func PrintToolUsage(w io.Writer, usage mcpusage.Report) {
	if !usage.Present {
		return
	}

	if usage.Err != nil {
		fmt.Fprintf(w, "\nmcp tool usage — unknown: %s\n", render.Sanitize(redact.Secrets(usage.Err.Error())))

		return
	}

	fmt.Fprintf(w, "\nmcp tool usage — calls: %d, failed: %d (counts calls, not whether a call helped)\n",
		usage.Calls, usage.Failed)

	for _, row := range usage.Rows {
		fmt.Fprintf(w, "  %-12s %-20s calls: %d, failed: %d; latency median %d ms, largest %d ms; reply median %d bytes\n",
			row.Tool, row.Client, row.Calls, row.Failed, row.MedianMS, row.MaxMS, row.MedianBytes)
	}

	if usage.Skipped > 0 {
		fmt.Fprintf(w, "  lines skipped, because they do not parse: %d\n", usage.Skipped)
	}
}
