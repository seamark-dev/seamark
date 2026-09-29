// Package tui is the experimental `seamark tui` workspace console. It is
// a presentation layer over the shared engines: it reads doctor, status,
// integration, proposal and delivery state through typed services, and
// every write goes through a reviewed preview. It adds no business rule
// and no settings store of its own.
package tui

import (
	"errors"
	"io"
	"net/url"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// Options configures one console session.
type Options struct {
	// Services binds the console to one resolved workspace.
	Services Services
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
func Run(opts Options) error {
	m := newModel(opts.Services, newTheme(opts.ASCII))
	m.st.copyText = func(text string) error { return writeClipboard(opts.Out, text) }
	m.st.openURL = openBrowser

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithInput(opts.In),
		tea.WithOutput(opts.Out),
	)

	_, err := p.Run()

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
