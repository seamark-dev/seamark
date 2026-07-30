package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/seamark-dev/seamark/internal/htmlreport"
)

// stdoutTarget is the conventional "write to stdout instead of a file"
// path, for piping the page somewhere else.
const stdoutTarget = "-"

func newReportCmd(opts *options) *cobra.Command {
	var (
		out       string
		inBrowser bool
	)

	cmd := &cobra.Command{
		Use:   "report",
		Short: "Write a self-contained HTML report of what the index knows",
		Long: `Renders the learning layer as one HTML file: the proposals awaiting a
decision, the applied pins that restate each other, where fixes concentrate
in the tree, and every mined lesson behind those.

Agents read the compact text reports over MCP; this is the surface for the
human who has to decide what the repo should actually remember. The page is
self-contained — no server, no assets, nothing fetched when it is opened —
so it can be attached to a pull request or read offline.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, root, err := openIndex(opts)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }() // read-only surface, nothing to lose

			staleNote(cmd.ErrOrStderr(), st, root)

			if out == stdoutTarget {
				if inBrowser {
					fmt.Fprintln(cmd.ErrOrStderr(),
						"note: --open needs a file; ignoring it while writing to stdout")
				}

				return htmlreport.Generate(cmd.OutOrStdout(), st, root, time.Now())
			}

			path := out
			if path == "" {
				path = filepath.Join(root, ".seamark", "report.html")
			}

			// Rendered whole before anything is written: a report that
			// failed halfway is a file someone opens and believes.
			var page bytes.Buffer
			if err := htmlreport.Generate(&page, st, root, time.Now()); err != nil {
				return err
			}

			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}

			if err := writeAtomic(path, page.Bytes()); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s (%d KB)\n", path, page.Len()/1024)

			if inBrowser {
				if err := openInBrowser(path); err != nil {
					// The report exists; failing to launch a viewer is a
					// note, not a failed command.
					fmt.Fprintf(cmd.ErrOrStderr(), "note: could not open a browser: %v\n", err)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&out, "out", "o", "",
		"output file (default <workspace>/.seamark/report.html; - for stdout)")
	cmd.Flags().BoolVar(&inBrowser, "open", false,
		"open the written report in the default browser")

	return cmd
}

// writeAtomic writes data to path via a temporary file in the same
// directory, then renames it into place. Buffering the render is only
// half the guarantee: a plain write truncates the target first, so a
// full disk would leave a half-written page where a perfectly good
// report from yesterday used to be. The rename either happens or it
// does not, and until it does the old report is exactly as it was.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}

	// Remove the temporary file on every failure path; harmless once the
	// rename below has moved it away.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return err
	}

	// Close before renaming, and report a close error: it is where a
	// delayed write failure surfaces.
	if err := tmp.Close(); err != nil {
		return err
	}

	// CreateTemp makes the file 0600; a report is not a secret, and the
	// non-atomic path wrote 0644.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}

// openInBrowser hands the file to the desktop's default handler. Best
// effort by design: headless machines and unknown platforms report a
// plain error rather than guessing at a browser binary.
func openInBrowser(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}

	var opener string

	switch runtime.GOOS {
	case "darwin":
		opener = "open"
	case "linux":
		opener = "xdg-open"
	case "windows":
		// Not `cmd /c start`: cmd re-parses its arguments, so a path
		// containing & or ^ would be read as shell syntax. rundll32
		// takes the path as one opaque argument.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", abs).Start()
	default:
		return fmt.Errorf("no known way to open a browser on %s", runtime.GOOS)
	}

	return exec.Command(opener, abs).Start()
}
