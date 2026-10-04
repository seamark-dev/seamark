//go:build unix

package approve

// The tests in this file need unix: they create FIFOs.

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTheRecordsRefuseADocumentThatIsNotARegularFile(t *testing.T) {
	// A read of a FIFO blocks until a writer opens it. Every reader opens
	// the path without blocking and refuses a file that is not regular,
	// so doctor and status never hang where init stops.
	for _, tc := range []struct {
		rel     string
		inspect func(root string) string
	}{
		{MCPConfig, func(root string) string { return InspectClaude(root).Err }},
		{ClaudeSettings, func(root string) string { return InspectClaude(root).Err }},
		{CodexConfig, func(root string) string { return InspectCodex(root).Err }},
	} {
		t.Run(tc.rel, func(t *testing.T) {
			root := t.TempDir()
			fifo := filepath.Join(root, filepath.FromSlash(tc.rel))
			require.NoError(t, os.MkdirAll(filepath.Dir(fifo), 0o755))
			require.NoError(t, syscall.Mkfifo(fifo, 0o600))

			// A writer that opens and closes the FIFO releases a reader that
			// waits on it. The release is best effort: it only limits what a
			// failed run leaves behind.
			t.Cleanup(func() {
				if f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
					_ = f.Close()
				}
			})

			done := make(chan string, 1)

			go func() { done <- tc.inspect(root) }()

			select {
			case reason := <-done:
				assert.Equal(t, tc.rel+": not a regular file", reason)
			case <-time.After(5 * time.Second):
				require.FailNow(t, "the record opened a FIFO", tc.rel)
			}
		})
	}
}
