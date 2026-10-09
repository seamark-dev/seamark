package tui

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// Options configures one console session.
type Options struct {
	// Services binds the console to one resolved workspace.
	Services services.All
	// ASCII selects the ASCII glyph set for limited fonts.
	ASCII bool
	// In and Out are the terminal streams. In can be any io.Reader:
	// Bubble Tea v1 sets raw mode only when In is a terminal file, so a
	// test can script the keys.
	In  io.Reader
	Out io.Writer
}

// Run starts the full-screen console and blocks until it exits. Bubble
// Tea restores the terminal on a normal exit, an error and a panic. The
// lifecycle tests check this through the escape sequences in Out.
//
// Run owns the root context of the session. After the program ends, Run
// cancels that context and waits a bounded time for stoppable work.
// This applies to a quit, an error and a signal. A cancelled agent run
// stops its whole process group, and gh runs as one process, so no
// child outlives the console. A stuck child cannot hold the exit.
//
// Bubble Tea ends the program on SIGINT and SIGTERM. Run also turns
// SIGHUP into a quit, because the agent runs in its own process group:
// a closed terminal does not reach it, and the Go default for SIGHUP
// ends seamark without the cleanup. A SIGKILL to seamark still cannot
// clean up.
func Run(opts Options) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	m := newModel(opts.Services, ui.NewTheme(opts.ASCII))
	m.st.ctx = ctx
	m.st.copyText = func(text string) error { return writeClipboard(opts.Out, text) }
	m.st.openURL = openBrowser

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithInput(opts.In),
		tea.WithOutput(opts.Out),
	)

	// A hangup quits through the program, like SIGTERM. The quit message
	// skips the quit question, so the console always ends, and the
	// cleanup below then stops the work.
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	defer signal.Stop(hangup)

	ended := make(chan struct{})
	defer close(ended)

	go func() {
		select {
		case <-hangup:
			p.Quit()
		case <-ended:
		}
	}()

	_, err := p.Run()

	// The event loop has ended, so reading the model is safe here.
	m.stopWork(cancel, time.Now())

	return err
}

// writeClipboard asks the terminal to put text on the system clipboard
// (OSC 52). The terminal decides whether it honours the request, so the
// console reports the request, not a confirmed copy.
func writeClipboard(out io.Writer, text string) error {
	if out == nil {
		return errNoClipboard
	}

	_, err := io.WriteString(out, ansi.SetSystemClipboard(text))

	return err
}

// errUnsafeURL reports a link that is not a plain web address. Links come
// from repository data, so any other scheme could start a local program.
var errUnsafeURL = errors.New("only http and https links open in the browser")

// openBrowser opens an http or https link with the platform opener. It
// runs only after an explicit key press.
func openBrowser(link string) error {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return errUnsafeURL
	}

	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}

	return exec.Command(opener, u.String()).Start()
}
