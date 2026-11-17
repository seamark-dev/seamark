// Command testsummary runs `go test -json` and prints a bounded summary.
//
// Go decides what runs and whether it passed. This command only invokes,
// captures and summarizes: it never rewrites selections, never retries and
// never reports success for a failed, interrupted or unparseable run.
//
//	testsummary [-log-dir dir] [-slow n] -- go test arguments...
//
// Every invocation creates a private run directory holding the exact event
// stream, the exact stderr, a readable decoded log and summary.json. The
// summary printed on stdout is bounded: four lines on success, at most 160
// lines and 32 KiB on failure, with a 2 KiB cap on any one line.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// defaultShutdownGrace bounds how long an interrupted child and its
// process group get to exit before the group is killed.
const defaultShutdownGrace = 5 * time.Second

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("testsummary", flag.ContinueOnError)
	fs.SetOutput(stderr)
	logDir := fs.String("log-dir", os.TempDir(), "parent directory for the unique per-invocation run directory")
	slow := fs.Int("slow", 3, "number of uncached slow packages to name")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprintln(stderr, "testsummary: usage: testsummary [-log-dir dir] [-slow n] -- go test arguments...")
		return 2
	}
	res, err := execute(invocation{
		args:    args,
		logDir:  *logDir,
		slow:    *slow,
		grace:   defaultShutdownGrace,
		stdout:  stdout,
		signals: nil,
	})
	if err != nil {
		fmt.Fprintf(stderr, "testsummary: %v\n", err)
	}
	return res.ExitCode
}

type invocation struct {
	args    []string // go test arguments, after --
	logDir  string
	slow    int
	grace   time.Duration
	stdout  io.Writer
	signals <-chan os.Signal // test seam; nil means the process signals
	command []string         // test seam; nil means `go test -json <args>`
	env     []string         // test seam; nil inherits the runner environment
}

type outcome struct {
	RunDir   string
	ExitCode int
	Summary  *Summary
}

// execute runs the child once and writes the run's artifacts. It returns
// a nonzero ExitCode for every failure class, including runner errors.
func execute(inv invocation) (outcome, error) {
	var out outcome
	if inv.stdout == nil {
		inv.stdout = io.Discard
	}
	if inv.slow < 0 {
		inv.slow = 0
	}

	runDir, err := createRunDir(inv.logDir)
	if err != nil {
		out.ExitCode = 1
		return out, err
	}
	out.RunDir = runDir

	eventsFile, err := createArtifact(runDir, "events.jsonl")
	if err != nil {
		out.ExitCode = 1
		return out, err
	}
	stderrFile, err := createArtifact(runDir, "stderr.log")
	if err != nil {
		_ = eventsFile.Close()
		out.ExitCode = 1
		return out, err
	}
	outputFile, err := createArtifact(runDir, "output.log")
	if err != nil {
		_ = eventsFile.Close()
		_ = stderrFile.Close()
		out.ExitCode = 1
		return out, err
	}
	if _, err := outputFile.WriteString("# decoded `go test -json` output (packages interleave)\n"); err != nil {
		_ = eventsFile.Close()
		_ = stderrFile.Close()
		_ = outputFile.Close()
		out.ExitCode = 1
		return out, err
	}

	argv := inv.command
	if argv == nil {
		argv = append([]string{"go", "test", "-json"}, inv.args...)
	}

	agg := newAggregator()
	started := time.Now()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = inv.env
	configureProcessGroup(cmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = closeArtifacts(eventsFile, stderrFile, outputFile)
		out.ExitCode = 1
		return out, fmt.Errorf("open child stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = closeArtifacts(eventsFile, stderrFile, outputFile)
		out.ExitCode = 1
		return out, fmt.Errorf("open child stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = closeArtifacts(eventsFile, stderrFile, outputFile)
		out.ExitCode = 1
		return out, fmt.Errorf("start %s: %w", argv[0], err)
	}

	waitDone := make(chan struct{})
	cleanupDone := make(chan struct{})
	var interrupted atomic.Int32
	signals := inv.signals
	if signals == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, terminationSignals()...)
		defer signal.Stop(ch)
		signals = ch
	}
	go func() {
		defer close(cleanupDone)
		select {
		case <-waitDone:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			interrupted.Store(int32(signalNumber(sig)))
			signalGroup(cmd, sig)
			// The grace belongs to the whole group, not only to the
			// leader: a cooperative descendant keeps the time it has
			// left, and escalation happens at the same deadline.
			deadline := time.NewTimer(inv.grace)
			defer deadline.Stop()
			select {
			case <-waitDone:
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for groupAlive(cmd) {
					select {
					case <-deadline.C:
						killGroup(cmd)
						return
					case <-ticker.C:
					}
				}
			case <-deadline.C:
				killGroup(cmd)
			}
		}
	}()

	var (
		wg          sync.WaitGroup
		capture     errSink
		stderrBytes int64
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		capture.set(streamEvents(stdoutPipe, eventsFile, outputFile, agg))
	}()
	go func() {
		defer wg.Done()
		n, err := copyStderr(stderrFile, stderrPipe)
		stderrBytes = n
		capture.set(err)
	}()
	wg.Wait()
	waitErr := cmd.Wait()
	close(waitDone)
	if interrupted.Load() != 0 {
		// Do not return while a surviving group member is being killed.
		<-cleanupDone
	}
	wall := time.Since(started).Seconds()

	closeErr := closeArtifacts(eventsFile, stderrFile, outputFile)

	s := buildSummary(agg, inv.slow)
	s.Command = append([]string(nil), argv...)
	s.WallSeconds = wall
	s.RunDir = runDir
	s.StderrBytes = stderrBytes
	s.ExitCode = childExitCode(waitErr)
	if captureErr := capture.get(); captureErr != nil {
		s.ProtocolError = captureErr.Error()
	}
	// An artifact that could not be completed makes the run a runner
	// error even when the child passed: the reported verdict must match
	// the evidence that survives.
	if closeErr != nil {
		s.Error = closeErr.Error()
	}
	if sig := interrupted.Load(); sig != 0 {
		s.Interrupted = true
		s.Signal = signalName(int(sig))
		s.ExitCode = 128 + int(sig)
	}

	switch {
	case s.Interrupted:
		s.Status = "interrupted"
	case s.ProtocolError != "":
		s.Status = "error"
	case s.Error != "":
		s.Status = "error"
	case waitErr != nil || agg.top.Fail > 0 || agg.sub.Fail > 0 || s.Packages.Failed > 0:
		s.Status = "fail"
	default:
		s.Status = "pass"
	}
	if s.Status == "pass" && s.ExitCode != 0 {
		s.Status = "fail"
	}
	if s.Status != "pass" && s.ExitCode == 0 {
		s.ExitCode = 1
	}

	var lines []string
	if s.Status == "pass" {
		lines = renderSuccess(s)
	} else {
		set, replayErr := collectExcerptsFromFile(filepath.Join(runDir, "events.jsonl"), agg)
		noteReplayError(s, replayErr)
		if set == nil {
			set = &excerptSet{}
		}
		stderrExcerpt := readStderrExcerpt(filepath.Join(runDir, "stderr.log"))
		lines = renderFailure(s, set, stderrExcerpt)
	}

	if err := writeSummary(runDir, s); err != nil {
		// The recorded verdict is missing or partial. Never print a
		// success line for that run.
		s.Error = err.Error()
		if s.Status == "pass" {
			s.Status = "error"
			lines = fitLines([]string{
				fmt.Sprintf("ERROR  %s  %.2fs  %v", displayCommand(s.Command), s.WallSeconds, err),
				"logs: " + runDir,
			}, failureMaxLines, failureMaxBytes)
		} else {
			lines = withRunnerError(lines, "testsummary: "+err.Error())
		}
		if s.ExitCode == 0 {
			s.ExitCode = 1
		}
		out.Summary = s
		out.ExitCode = s.ExitCode
		_ = printLines(inv.stdout, lines)
		return out, err
	}
	if err := printLines(inv.stdout, lines); err != nil {
		// Keep the child's status: a broken output pipe is a reporting
		// failure, not a different test result.
		out.ExitCode = s.ExitCode
		if out.ExitCode == 0 {
			out.ExitCode = 1
		}
		return out, fmt.Errorf("write summary output: %w", err)
	}
	out.Summary = s
	out.ExitCode = s.ExitCode
	if closeErr != nil {
		return out, closeErr
	}
	return out, nil
}

// withRunnerError inserts a runner error before the trailer's artifact
// path, then re-applies the failure budget.
func withRunnerError(lines []string, message string) []string {
	if len(lines) == 0 {
		return fitLines([]string{message}, failureMaxLines, failureMaxBytes)
	}
	// The last line is the artifact path. Keep it: drop excerpt lines
	// from the top until the message and the path both fit.
	trailer := lines[len(lines)-1]
	body := append([]string(nil), lines[:len(lines)-1]...)
	for {
		out := append(append([]string(nil), body...), message, trailer)
		fitted := fitLines(out, failureMaxLines, failureMaxBytes)
		if len(fitted) >= 2 && fitted[len(fitted)-1] == trailer {
			return fitted
		}
		if len(body) <= 1 {
			// Keep the status headline and drop the message's tail
			// instead; the exit status is still reported.
			return fitLines([]string{body[0], truncateLine(message, lineMaxBytes), trailer},
				failureMaxLines, failureMaxBytes)
		}
		// Drop the last excerpt line, never the status headline.
		body = body[:len(body)-1]
	}
}

// noteReplayError records a failed excerpt replay without replacing a
// child status that is already nonzero.
func noteReplayError(s *Summary, err error) {
	if err == nil || s.ProtocolError != "" {
		return
	}
	s.ProtocolError = err.Error()
	s.Status = "error"
	if s.ExitCode == 0 {
		s.ExitCode = 1
	}
}

func closeArtifacts(files ...*os.File) error {
	var first error
	for _, f := range files {
		if err := f.Close(); err != nil && first == nil {
			first = fmt.Errorf("close %s: %w", f.Name(), err)
		}
	}
	return first
}

func createRunDir(parent string) (string, error) {
	if parent == "" {
		parent = os.TempDir()
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create log directory %s: %w", parent, err)
	}
	dir, err := os.MkdirTemp(parent, "run-"+time.Now().Format("20060102-150405")+"-")
	if err != nil {
		return "", fmt.Errorf("create run directory under %s: %w", parent, err)
	}
	return dir, nil
}

func createArtifact(runDir, name string) (*os.File, error) {
	path := filepath.Join(runDir, name)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create artifact %s: %w", path, err)
	}
	return f, nil
}

func writeSummary(runDir string, s *Summary) error {
	f, err := createArtifact(runDir, "summary.json")
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err == nil {
		data = append(data, '\n')
		_, err = f.Write(data)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func printLines(w io.Writer, lines []string) error {
	buf := bufio.NewWriter(w)
	for _, line := range lines {
		if _, err := buf.WriteString(line + "\n"); err != nil {
			return err
		}
	}
	return buf.Flush()
}

func collectExcerptsFromFile(path string, a *aggregator) (*excerptSet, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open spooled events: %w", err)
	}
	defer func() { _ = f.Close() }()
	return collectExcerpts(f, a)
}

func readStderrExcerpt(path string) *excerpt {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	b := &lineBuf{}
	if _, err := io.Copy(b, f); err != nil {
		return nil
	}
	b.flush()
	return &excerpt{Head: b.head, Tail: b.ring, TotalLines: b.lines, TotalBytes: b.bytes}
}

// streamEvents spools the exact child stdout while decoding it. Output is
// a stream of fragments, not lines: the readable log concatenates the
// fragments and the excerpt builder reconstructs lines later.
func streamEvents(r io.Reader, events, readable io.Writer, agg *aggregator) error {
	tee := io.TeeReader(r, events)
	dec := json.NewDecoder(tee)
	var firstErr error
	for {
		var ev Event
		err := dec.Decode(&ev)
		if err == io.EOF {
			break
		}
		if err != nil {
			firstErr = fmt.Errorf("decode go test event stream: %w", err)
			break
		}
		agg.Add(ev)
		if ev.Output == "" {
			continue
		}
		if _, err := io.WriteString(readable, ev.Output); err != nil {
			firstErr = fmt.Errorf("write decoded output: %w", err)
			break
		}
	}
	if firstErr != nil {
		// Keep draining so a verbose child never blocks on a full pipe.
		// Spool what the broken artifact still accepts, then discard the
		// rest: a failed write must not stop the drain.
		if _, err := io.Copy(events, r); err != nil {
			if _, derr := io.Copy(io.Discard, r); derr != nil && !errors.Is(derr, os.ErrClosed) {
				firstErr = errors.Join(firstErr, fmt.Errorf("drain child stdout: %w", derr))
			}
		}
	}
	return firstErr
}

// copyStderr copies the child's stderr into its artifact. A failed write
// must not leave a verbose child blocked on a full stderr pipe, so the
// remainder is drained after an error.
func copyStderr(dst io.Writer, r io.Reader) (int64, error) {
	n, err := io.Copy(dst, r)
	if err != nil {
		if _, derr := io.Copy(io.Discard, r); derr != nil && !errors.Is(derr, os.ErrClosed) {
			err = errors.Join(err, fmt.Errorf("drain child stderr: %w", derr))
		}
	}
	return n, err
}

type errSink struct {
	mu  sync.Mutex
	err error
}

func (s *errSink) set(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *errSink) get() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// childExitCode preserves the child's status: an ordinary nonzero code
// unchanged, a signal death as 128+signal.
func childExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if code := ee.ExitCode(); code >= 0 {
			return code
		}
		if code, ok := signaledExitCode(ee); ok {
			return code
		}
	}
	return 1
}

// lineBuf.Write lets a bounded line buffer consume an arbitrary stream,
// such as the child's stderr artifact.
func (b *lineBuf) Write(p []byte) (int, error) {
	b.add(string(p))
	return len(p), nil
}
