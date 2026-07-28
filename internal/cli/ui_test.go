package cli

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncBuffer makes bytes.Buffer safe for the spinner goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func TestUILifecycle(t *testing.T) {
	var buf syncBuffer

	u := &ui{w: &buf, tty: true}

	u.phase("parse", bar(0, 10))
	u.update(bar(7, 10))
	u.finish("")

	// Starting a new phase auto-closes a dangling one.
	u.phase("history", "")
	u.phase("write", "")
	u.finish("done")

	out := buf.String()
	assert.Contains(t, out, "\r", "live lines rewrite in place")
	assert.Contains(t, out, "◆", "phases resolve to done markers")
	assert.Contains(t, out, "parse")
	assert.Contains(t, out, "70%", "the last detail becomes the summary")
	assert.Contains(t, out, "history")
	assert.Contains(t, out, "write")
	assert.Contains(t, out, "done")

	// finish with nothing active is a no-op, not a panic.
	u.finish("again")
	require.Equal(t, out, buf.String())
}

func TestUISilentWhenNotATerminal(t *testing.T) {
	var buf syncBuffer

	u := &ui{w: &buf, tty: false}

	u.phase("parse", "x")
	u.update("y")
	u.finish("z")

	assert.Empty(t, buf.String(), "not one byte may reach piped or agent output")
}

func TestBar(t *testing.T) {
	assert.Equal(t, "", bar(1, 0), "indeterminate totals render nothing")
	assert.True(t, strings.HasSuffix(bar(5, 10), " 50%"))
	assert.True(t, strings.Contains(bar(10, 10), "100%"))
	assert.NotContains(t, bar(0, 10), "▉")
}
