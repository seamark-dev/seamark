package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// ui renders live phase progress on a terminal: an animated spinner
// line per phase, rewritten in place, resolving to a ◆ done line. It is
// strictly a presentation layer for humans — when the writer is not a
// terminal (piped output, CI, agents) every method is a no-op and the
// caller's plain log lines remain the only output, so not one ANSI byte
// can reach an agent's context or a captured log.
type ui struct {
	mu      sync.Mutex
	w       io.Writer
	tty     bool
	name    string
	detail  string
	started time.Time
	frame   int
	done    chan struct{}
	wg      sync.WaitGroup
}

// newUI detects whether w is an interactive terminal. NO_COLOR, CI, and
// TERM=dumb all force plain mode — the animation is a courtesy, never a
// dependency.
func newUI(w io.Writer) *ui {
	tty := false

	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil {
			tty = info.Mode()&os.ModeCharDevice != 0
		}
	}

	if os.Getenv("NO_COLOR") != "" || os.Getenv("CI") != "" || os.Getenv("TERM") == "dumb" {
		tty = false
	}

	return &ui{w: w, tty: tty}
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// phase begins a live line. An unfinished previous phase is closed
// first with its last detail as the summary, so callers can't leak a
// spinner.
func (u *ui) phase(name, detail string) {
	if !u.tty {
		return
	}

	u.finish("")

	u.mu.Lock()
	u.name, u.detail, u.started, u.frame = name, detail, time.Now(), 0
	u.mu.Unlock()

	u.done = make(chan struct{})
	u.wg.Add(1)

	go u.spin(u.done)
}

// update swaps the live line's detail text (progress bars, counters).
func (u *ui) update(detail string) {
	if !u.tty {
		return
	}

	u.mu.Lock()
	u.detail = detail
	u.mu.Unlock()
}

// finish resolves the live line into a green done line. An empty
// summary keeps the last detail.
func (u *ui) finish(summary string) {
	if !u.tty || u.done == nil {
		return
	}

	close(u.done)
	u.wg.Wait()
	u.done = nil

	u.mu.Lock()
	defer u.mu.Unlock()

	if summary == "" {
		summary = u.detail
	}

	fmt.Fprintf(u.w, "\r\x1b[2K  \x1b[32m◆\x1b[0m %-9s %s \x1b[2m(%s)\x1b[0m\n",
		u.name, summary, time.Since(u.started).Round(100*time.Millisecond))
}

func (u *ui) spin(done chan struct{}) {
	defer u.wg.Done()

	t := time.NewTicker(120 * time.Millisecond)
	defer t.Stop()

	u.redraw()

	for {
		select {
		case <-done:
			return
		case <-t.C:
			u.redraw()
		}
	}
}

func (u *ui) redraw() {
	u.mu.Lock()
	defer u.mu.Unlock()

	frame := spinnerFrames[u.frame%len(spinnerFrames)]
	u.frame++

	fmt.Fprintf(u.w, "\r\x1b[2K  \x1b[33m%s\x1b[0m %-9s %s \x1b[2m%s\x1b[0m",
		frame, u.name, u.detail, time.Since(u.started).Round(time.Second))
}

// bar renders a ten-cell progress bar with a percentage.
func bar(done, total int) string {
	if total <= 0 {
		return ""
	}

	filled := done * 10 / total

	return fmt.Sprintf("▐%s%s▏%3d%%",
		strings.Repeat("▉", filled), strings.Repeat("▁", 10-filled), done*100/total)
}
