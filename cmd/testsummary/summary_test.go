package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aggregate decodes a synthetic event stream into result metadata.
func aggregate(t *testing.T, stream string) *aggregator {
	t.Helper()
	agg := newAggregator()
	dec := json.NewDecoder(strings.NewReader(stream))
	for {
		var ev Event
		err := dec.Decode(&ev)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		agg.Add(ev)
	}
	return agg
}

// render builds the failure surface exactly as execute does after a
// failed run, from the same synthetic stream.
func render(t *testing.T, stream string, stderr *excerpt) (summary *Summary, lines []string) {
	t.Helper()
	agg := aggregate(t, stream)
	set, err := collectExcerpts(strings.NewReader(stream), agg)
	require.NoError(t, err)
	s := buildSummary(agg, 3)
	s.Command = []string{"go", "test", "-json", "./..."}
	s.RunDir = "/tmp/run-fixture"
	s.Status = "fail"
	s.ExitCode = 1
	return s, renderFailure(s, set, stderr)
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

// assertBudget enforces AC-3's failure bounds: at most 160 lines, at most
// 32 KiB total, and no logical line above 2 KiB.
func assertBudget(t *testing.T, lines []string) {
	t.Helper()
	require.LessOrEqual(t, len(lines), failureMaxLines)
	total := 0
	for _, line := range lines {
		require.LessOrEqual(t, len(line), lineMaxBytes, "logical line exceeds the cap")
		require.True(t, utf8.ValidString(line), "emitted line must stay valid UTF-8")
		total += len(line) + 1
	}
	require.LessOrEqual(t, total, failureMaxBytes)
}

func TestInterleavedPackagesKeepSeparateResults(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"a"}`,
		`{"Action":"start","Package":"b"}`,
		`{"Action":"run","Package":"a","Test":"TestA"}`,
		`{"Action":"run","Package":"b","Test":"TestB"}`,
		`{"Action":"output","Package":"a","Test":"TestA","Output":"a log\n"}`,
		`{"Action":"output","Package":"b","Test":"TestB","Output":"b log\n"}`,
		`{"Action":"pass","Package":"a","Test":"TestA"}`,
		`{"Action":"fail","Package":"b","Test":"TestB","Elapsed":1.5}`,
		`{"Action":"output","Package":"a","Output":"ok  \ta\t0.2s\n"}`,
		`{"Action":"output","Package":"b","Output":"FAIL\tb\t1.6s\n"}`,
		`{"Action":"pass","Package":"a","Elapsed":0.2}`,
		`{"Action":"fail","Package":"b","Elapsed":1.6}`,
	}, "\n") + "\n"

	s, lines := render(t, stream, nil)
	assert.Equal(t, 1, s.Packages.Passed)
	assert.Equal(t, 1, s.Packages.Failed)
	assert.Equal(t, 1, s.Tests.Top.Pass)
	assert.Equal(t, 1, s.Tests.Top.Fail)
	out := joined(lines)
	assert.Contains(t, out, "a")
	assert.Contains(t, out, "TestB")
	assert.NotContains(t, out, "a log", "passing package output must not be emitted")
	assertBudget(t, lines)
}

func TestNestedFailureAndParentReportedSeparately(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestParent"}`,
		`{"Action":"output","Package":"p","Test":"TestParent","Output":"=== RUN   TestParent\n"}`,
		`{"Action":"run","Package":"p","Test":"TestParent/child"}`,
		`{"Action":"output","Package":"p","Test":"TestParent/child","Output":"    want 1, got 2\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestParent/child"}`,
		`{"Action":"output","Package":"p","Test":"TestParent","Output":"--- FAIL: TestParent (0.00s)\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestParent"}`,
		`{"Action":"fail","Package":"p","Elapsed":0.1}`,
	}, "\n") + "\n"

	s, lines := render(t, stream, nil)
	assert.Equal(t, 1, s.Tests.Top.Fail, "the parent failure is a reported top-level result")
	assert.Equal(t, 1, s.Tests.Sub.Fail, "the subtest failure is counted separately")
	out := joined(lines)
	assert.Contains(t, out, "TestParent")
	assert.Contains(t, out, "TestParent/child")
	assert.Contains(t, out, "want 1, got 2")
	assertBudget(t, lines)
}

func TestSkippedSubtestIsListedAndCounted(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestParent"}`,
		`{"Action":"run","Package":"p","Test":"TestParent/child"}`,
		`{"Action":"skip","Package":"p","Test":"TestParent/child"}`,
		`{"Action":"pass","Package":"p","Test":"TestParent"}`,
		`{"Action":"pass","Package":"p","Elapsed":0.1}`,
	}, "\n") + "\n"

	agg := aggregate(t, stream)
	s := buildSummary(agg, 3)
	require.Len(t, s.Skips, 1)
	assert.Equal(t, "TestParent/child", s.Skips[0].Test)
	assert.Equal(t, 1, s.Tests.Sub.Skip)
	lines := renderSuccess(s)
	require.LessOrEqual(t, len(lines), successMaxLines)
	total := 0
	for _, line := range lines {
		total += len(line) + 1
	}
	require.LessOrEqual(t, total, successMaxBytes)
	assert.Contains(t, joined(lines), "1 skipped")
}

func TestCachedReplayIsNotReportedAsFreshExecution(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"run","Package":"p","Test":"TestSlow"}`,
		`{"Action":"pass","Package":"p","Test":"TestSlow","Elapsed":30}`,
		`{"Action":"output","Package":"p","Output":"ok  \tp\t(cached)\n"}`,
		`{"Action":"pass","Package":"p","Elapsed":0}`,
	}, "\n") + "\n"

	agg := aggregate(t, stream)
	s := buildSummary(agg, 3)
	assert.Equal(t, 1, s.Packages.Cached)
	assert.Empty(t, s.SlowPackages, "a cached package's elapsed time is not current work")
	assert.Empty(t, s.SlowTests, "cached test durations are historical replay")
	lines := renderSuccess(s)
	assert.Contains(t, joined(lines), "1 cached")
	assert.NotContains(t, joined(lines), "slow:")
}

func TestCachedWordInPackageOutputIsNotACacheHit(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"output","Package":"p","Output":"configuration loaded (cached)\n"}`,
		`{"Action":"run","Package":"p","Test":"TestFast"}`,
		`{"Action":"pass","Package":"p","Test":"TestFast","Elapsed":0.1}`,
		`{"Action":"output","Package":"p","Output":"ok  \tp\t0.1s\n"}`,
		`{"Action":"pass","Package":"p","Elapsed":0.1}`,
	}, "\n") + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	assert.Equal(t, 0, s.Packages.Cached, "only Go's package-result line marks a cache hit")
	assert.Equal(t, 1, s.Packages.Passed)
	assert.Len(t, s.SlowPackages, 1, "a fresh package keeps its timing")
	assert.Contains(t, joined(renderSuccess(s)), "0 cached")
}

func TestNoSelectedTestsIsLabelled(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"output","Package":"p","Output":"ok  \tp\t0.1s\n"}`,
		`{"Action":"pass","Package":"p","Elapsed":0.1}`,
	}, "\n") + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	lines := renderSuccess(s)
	assert.Contains(t, joined(lines), "no tests selected")
}

func TestNoTestFilesIsDistinctFromSkippedTests(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"none"}`,
		`{"Action":"output","Package":"none","Output":"?   \tnone\t[no test files]\n"}`,
		`{"Action":"skip","Package":"none","Elapsed":0}`,
		`{"Action":"run","Package":"skips","Test":"TestS"}`,
		`{"Action":"skip","Package":"skips","Test":"TestS"}`,
		`{"Action":"pass","Package":"skips","Elapsed":0.1}`,
	}, "\n") + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	assert.Equal(t, 1, s.Packages.NoTests)
	assert.Equal(t, 1, s.Packages.Passed, "a package whose tests all skipped still passed")
	assert.Equal(t, 1, s.Tests.Top.Skip)
}

func TestSeventyKiBLineIsReconstructedAndCapped(t *testing.T) {
	line := "HEADMARK" + strings.Repeat("A", 69000) + "TAILMARK"
	var sb strings.Builder
	for start := 0; start < len(line); start += 1024 {
		end := start + 1024
		if end > len(line) {
			end = len(line)
		}
		fragment, err := json.Marshal(Event{Action: "output", Package: "p", Test: "TestBig", Output: line[start:end]})
		require.NoError(t, err)
		sb.Write(fragment)
		sb.WriteByte('\n')
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Test":"TestBig"}` + "\n")
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":0.1}` + "\n")

	s, lines := render(t, sb.String(), nil)
	out := joined(lines)
	assert.Contains(t, out, "HEADMARK")
	assert.Contains(t, out, "TAILMARK", "the bounded tail survives the cap")
	assert.Contains(t, out, "bytes omitted")
	assertBudget(t, lines)
	assert.Equal(t, int64(len(line)), s.OutputBytes, "fragments without a final newline count their bytes exactly")
}

func TestMultilineDiffKeepsHeadAndTail(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"Action":"run","Package":"p","Test":"TestDiff"}` + "\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, `{"Action":"output","Package":"p","Test":"TestDiff","Output":"line %d\n"}`, i)
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Test":"TestDiff"}` + "\n")
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":0.1}` + "\n")

	_, lines := render(t, sb.String(), nil)
	out := joined(lines)
	assert.Contains(t, out, "line 0", "the beginning of the diff is preserved")
	assert.Contains(t, out, "line 99", "the end of the diff is preserved")
	assert.Contains(t, out, "lines omitted")
	assertBudget(t, lines)
}

func TestManyFailuresStayWithinTheAggregateCap(t *testing.T) {
	var sb strings.Builder
	body := strings.Repeat("x", 200) + "\n"
	for i := range 200 {
		name := fmt.Sprintf("TestFail%03d", i)
		fmt.Fprintf(&sb, `{"Action":"run","Package":"p","Test":%q}`+"\n", name)
		for range 50 {
			fmt.Fprintf(&sb, `{"Action":"output","Package":"p","Test":%q,"Output":%q}`+"\n", name, body)
		}
		fmt.Fprintf(&sb, `{"Action":"fail","Package":"p","Test":%q}`+"\n", name)
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":1}` + "\n")

	s, lines := render(t, sb.String(), nil)
	assertBudget(t, lines)
	assert.Equal(t, 200, s.Tests.Top.Fail)
	out := joined(lines)
	assert.Contains(t, out, "TestFail000")
	assert.Contains(t, out, "failing targets not shown")
	assert.Contains(t, out, "/tmp/run-fixture", "the artifact path always survives")
}

func TestPackagePanicSurvivesWithoutAFailingTest(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"output","Package":"p","Output":"panic: test timed out after 10m0s\n"}`,
		`{"Action":"output","Package":"p","Output":"\trunning tests:\n"}`,
		`{"Action":"output","Package":"p","Output":"\t\tTestHang (10m0s)\n"}`,
		`{"Action":"fail","Package":"p","Elapsed":600}`,
	}, "\n") + "\n"

	_, lines := render(t, stream, nil)
	out := joined(lines)
	assert.Contains(t, out, "test timed out after 10m0s")
	assert.Contains(t, out, "TestHang")
	assertBudget(t, lines)
}

func TestRepeatedRunReportsAnUnfinishedTestOnce(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"start","Package":"p"}`,
		`{"Action":"run","Package":"p","Test":"TestRepeat"}`,
		`{"Action":"pass","Package":"p","Test":"TestRepeat","Elapsed":0.1}`,
		`{"Action":"run","Package":"p","Test":"TestRepeat"}`,
		`{"Action":"output","Package":"p","Test":"TestRepeat","Output":"second run did not report\n"}`,
		`{"Action":"fail","Package":"p","Elapsed":10}`,
	}, "\n") + "\n"

	agg := aggregate(t, stream)
	s := buildSummary(agg, 3)
	require.Len(t, s.UnreportedTests, 1, "an earlier pass must not duplicate the identity")
	assert.Equal(t, "TestRepeat", s.UnreportedTests[0].Test)

	set, err := collectExcerpts(strings.NewReader(stream), agg)
	require.NoError(t, err)

	var unreported int
	for _, ex := range set.items {
		if ex.Kind == excerptUnreported && ex.Test == "TestRepeat" {
			unreported++
		}
	}
	assert.Equal(t, 1, unreported, "the diagnostic excerpt is not duplicated")
}

func TestStderrBuildErrorIsReported(t *testing.T) {
	stream := strings.Join([]string{
		`{"ImportPath":"p [p.test]","Action":"build-output","Output":"# p [p.test]\n"}`,
		`{"ImportPath":"p [p.test]","Action":"build-output","Output":"p.go:3:2: undefined: nope\n"}`,
		`{"ImportPath":"p [p.test]","Action":"build-fail"}`,
		`{"Action":"start","Package":"p"}`,
		`{"Action":"output","Package":"p","Output":"FAIL\tp [build failed]\n"}`,
		`{"Action":"fail","Package":"p","FailedBuild":"p [p.test]"}`,
	}, "\n") + "\n"

	var stderrBuf lineBuf
	stderrBuf.add("go: downloading example.com/dep: module lookup disabled\n")
	stderrBuf.flush()
	stderr := &excerpt{Head: stderrBuf.head, Tail: stderrBuf.ring, TotalLines: stderrBuf.lines, TotalBytes: stderrBuf.bytes}

	_, lines := render(t, stream, stderr)
	out := joined(lines)
	assert.Contains(t, out, "undefined: nope")
	assert.Contains(t, out, "module lookup disabled")
	assertBudget(t, lines)
}

func TestStreamEventsDrainsAfterMalformedJSON(t *testing.T) {
	raw := `{"Action":"start","Package":"p"}` + "\n" +
		`{"Action":"output","Package":"p","Output":"before the failure\n"}` + "\n" +
		`{"Action":` + "\n" + `{"Action":"pass","Package":"p"}` + "\n"
	var events, readable strings.Builder
	err := streamEvents(strings.NewReader(raw), &events, &readable, newAggregator())
	require.Error(t, err, "a malformed stream is a runner error")
	assert.Equal(t, raw, events.String(), "raw evidence must survive the decode failure")
	assert.Contains(t, readable.String(), "before the failure", "decoded output before the failure is retained")
}

func TestSuccessOutputStaysWithinItsBudget(t *testing.T) {
	agg := aggregate(t, `{"Action":"pass","Package":"p","Elapsed":1}`+"\n")
	s := buildSummary(agg, 3)
	s.Command = []string{"go", "test", "-json", strings.Repeat("./some/long/package ", 500)}
	lines := renderSuccess(s)
	require.LessOrEqual(t, len(lines), successMaxLines)
	total := 0
	for _, line := range lines {
		require.LessOrEqual(t, len(line), lineMaxBytes)
		total += len(line) + 1
	}
	require.LessOrEqual(t, total, successMaxBytes)
}

func TestMediumLineKeepsItsTail(t *testing.T) {
	// Longer than the prefix cap (1,400) but shorter than prefix+tail, so
	// no omission marker is due. JSON transport replaces the two bytes
	// split at a fragment boundary with U+FFFD, so the decoded line is
	// about 1,819 bytes: still inside the no-marker range. The last bytes
	// must still be emitted.
	line := strings.Repeat("界", 600) + "ENDMARK"
	var sb strings.Builder
	for start := 0; start < len(line); start += 512 {
		end := start + 512
		if end > len(line) {
			end = len(line)
		}
		fragment, err := json.Marshal(Event{Action: "output", Package: "p", Test: "TestMedium", Output: line[start:end]})
		require.NoError(t, err)
		sb.Write(fragment)
		sb.WriteByte('\n')
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Test":"TestMedium"}` + "\n")
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":0.1}` + "\n")

	_, lines := render(t, sb.String(), nil)
	assert.Contains(t, joined(lines), "ENDMARK", "the tail of a fully retained line survives")
	assert.NotContains(t, joined(lines), "bytes omitted", "no marker is due below the prefix+tail size")
	assertBudget(t, lines)
}

func TestLongMultibyteLineStaysValidUTF8(t *testing.T) {
	line := strings.Repeat("界", 1000) // 3,000 bytes, beyond the prefix cap
	var sb strings.Builder
	for start := 0; start < len(line); start += 512 {
		end := start + 512
		if end > len(line) {
			end = len(line)
		}
		fragment, err := json.Marshal(Event{Action: "output", Package: "p", Test: "TestCJK", Output: line[start:end]})
		require.NoError(t, err)
		sb.Write(fragment)
		sb.WriteByte('\n')
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Test":"TestCJK"}` + "\n")
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":0.1}` + "\n")

	_, lines := render(t, sb.String(), nil)
	assertBudget(t, lines) // also asserts every emitted line is valid UTF-8
	assert.Contains(t, joined(lines), "界")
}

func TestTruncateLineKeepsValidUTF8(t *testing.T) {
	line := strings.Repeat("é", 3000)
	cut := truncateLine(line, lineMaxBytes)
	require.LessOrEqual(t, len(cut), lineMaxBytes)
	assert.True(t, utf8.ValidString(cut))
}

func TestNonzeroChildAfterPassEventsIsNotSuccess(t *testing.T) {
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  t.TempDir(),
		slow:    3,
		grace:   time.Second,
		stdout:  io.Discard,
		command: helperCommand("pass-exit3"),
		env:     helperEnv("pass-exit3"),
	})
	require.NoError(t, err)
	assert.Equal(t, 3, res.ExitCode, "the child status is preserved")
	require.NotNil(t, res.Summary)
	assert.Equal(t, "fail", res.Summary.Status)
}

func TestRawEventStreamRoundTrips(t *testing.T) {
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  t.TempDir(),
		slow:    3,
		grace:   time.Second,
		stdout:  io.Discard,
		command: helperCommand("pass"),
		env:     helperEnv("pass"),
	})
	require.NoError(t, err)
	assert.Equal(t, 0, res.ExitCode)
	data, err := os.ReadFile(filepath.Join(res.RunDir, "events.jsonl"))
	require.NoError(t, err)
	require.NotEmpty(t, data)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 4, "every child stdout line is spooled exactly once")
	for _, line := range lines {
		assert.True(t, json.Valid([]byte(line)), "spooled bytes stay exact: %s", line)
	}
}

func TestMalformedChildStreamFailsTheRun(t *testing.T) {
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  t.TempDir(),
		slow:    3,
		grace:   time.Second,
		stdout:  io.Discard,
		command: helperCommand("malformed"),
		env:     helperEnv("malformed"),
	})
	require.NoError(t, err)
	assert.NotZero(t, res.ExitCode)
	require.NotNil(t, res.Summary)
	assert.Equal(t, "error", res.Summary.Status)
	assert.NotEmpty(t, res.Summary.ProtocolError)

	events, err := os.ReadFile(filepath.Join(res.RunDir, "events.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(events), `{"Action":`, "raw evidence is retained")
}

func TestStderrOnlyFailureIsReported(t *testing.T) {
	var stdout strings.Builder
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  t.TempDir(),
		slow:    3,
		grace:   time.Second,
		stdout:  &stdout,
		command: helperCommand("stderr-exit2"),
		env:     helperEnv("stderr-exit2"),
	})
	require.NoError(t, err)
	assert.Equal(t, 2, res.ExitCode)
	require.NotNil(t, res.Summary)
	assert.Equal(t, "fail", res.Summary.Status)
	assert.Contains(t, stdout.String(), "go: unknown flag -bad", "stderr diagnostics survive without any test event")
	assert.Contains(t, stdout.String(), res.RunDir, "the artifact path survives")
}

func TestLogDirectoryFailureIsReported(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-a-dir")
	require.NoError(t, os.WriteFile(parent, []byte("x"), 0o600))
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  parent,
		slow:    3,
		grace:   time.Second,
		stdout:  io.Discard,
		command: helperCommand("pass"),
		env:     helperEnv("pass"),
	})
	require.Error(t, err)
	assert.NotZero(t, res.ExitCode, "an artifact failure never becomes success")
}

func TestConcurrentInvocationsGetDistinctArtifacts(t *testing.T) {
	parent := t.TempDir()
	var wg sync.WaitGroup
	results := make([]outcome, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = execute(invocation{
				args:    []string{"./..."},
				logDir:  parent,
				slow:    3,
				grace:   time.Second,
				stdout:  io.Discard,
				command: helperCommand("pass"),
				env:     helperEnv("pass"),
			})
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.NotEqual(t, results[0].RunDir, results[1].RunDir)
	for _, res := range results {
		for _, name := range []string{"events.jsonl", "stderr.log", "output.log", "summary.json"} {
			assert.FileExists(t, filepath.Join(res.RunDir, name))
		}
	}
}

func helperCommand(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestHelperProcess$"}
}

// helperEnv marks the child as the synthetic event producer. It is passed
// explicitly so the test process never carries the gate itself.
func helperEnv(mode string) []string {
	return append(os.Environ(), "GO_WANT_TESTSUMMARY_HELPER=1", "TESTSUMMARY_HELPER_MODE="+mode)
}

// TestHelperProcess is the child process for the execution-level tests.
// It exits before the testing framework prints its own summary, so the
// runner sees only the synthetic event stream.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_TESTSUMMARY_HELPER") != "1" {
		return
	}
	mode := os.Getenv("TESTSUMMARY_HELPER_MODE")
	switch mode {
	case "pass-exit3":
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"run","Package":"p","Test":"TestOK"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"pass","Package":"p","Test":"TestOK"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"pass","Package":"p"}`)
		os.Exit(3)
	case "malformed":
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		fmt.Fprintln(os.Stdout, `{"Action":`)
		os.Exit(0)
	case "stderr-exit2":
		fmt.Fprintln(os.Stderr, "go: unknown flag -bad")
		os.Exit(2)
	case "cooperative":
		// The shell handles SIGTERM, finishes a 0.3 s cleanup, and only
		// then records success.
		child := exec.Command("sh", "-c",
			`trap 'sleep 0.3; echo clean > "$TESTSUMMARY_CLEAN_FILE"; exit 0' TERM; echo $$ > "$TESTSUMMARY_DESCENDANT_FILE"; while :; do sleep 0.05; done`)
		require.NoError(t, child.Start())
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		time.Sleep(time.Hour)
	case "stubborn":
		// The shell ignores SIGTERM and records its pid only after the
		// trap is installed, so the test knows the descendant is ready.
		child := exec.Command("sh", "-c",
			`trap "" TERM; echo $$ > "$TESTSUMMARY_DESCENDANT_FILE"; while :; do sleep 1; done`)
		require.NoError(t, child.Start())
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		time.Sleep(time.Hour)
	case "interrupt":
		child := exec.Command("sleep", "30")
		require.NoError(t, child.Start())
		require.NoError(t, os.WriteFile(os.Getenv("TESTSUMMARY_DESCENDANT_FILE"),
			[]byte(strconv.Itoa(child.Process.Pid)), 0o600))
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		time.Sleep(time.Hour)
	case "pass":
		fmt.Fprintln(os.Stdout, `{"Action":"start","Package":"p"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"run","Package":"p","Test":"TestOK"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"pass","Package":"p","Test":"TestOK"}`)
		fmt.Fprintln(os.Stdout, `{"Action":"pass","Package":"p"}`)
		os.Exit(0)
	}
	os.Exit(0)
}

// failingWriter stands in for an artifact whose writes fail.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("artifact write failed")
}

func TestFailedArtifactDrainsAVerboseChild(t *testing.T) {
	var payload strings.Builder
	for range 2000 {
		payload.WriteString(`{"Action":"output","Package":"p","Test":"TestX","Output":"` +
			strings.Repeat("x", 200) + `"}` + "\n")
	}
	reader := strings.NewReader(payload.String())
	var readable strings.Builder
	err := streamEvents(reader, failingWriter{}, &readable, newAggregator())
	require.Error(t, err)
	assert.Zero(t, reader.Len(), "the child stream is drained after an artifact write failure")
}

func TestFailedStderrArtifactDrainsTheChild(t *testing.T) {
	reader := strings.NewReader(strings.Repeat("e", 1<<20))
	_, err := copyStderr(failingWriter{}, reader)
	require.Error(t, err)
	assert.Zero(t, reader.Len(), "the stderr stream is drained after an artifact write failure")
}

func TestReportingFailureKeepsChildExitStatus(t *testing.T) {
	res, err := execute(invocation{
		args:    []string{"./..."},
		logDir:  t.TempDir(),
		slow:    3,
		grace:   time.Second,
		stdout:  failingWriter{},
		command: helperCommand("pass-exit3"),
		env:     helperEnv("pass-exit3"),
	})
	require.Error(t, err, "a reporting failure is still reported")
	assert.Equal(t, 3, res.ExitCode, "the child's status is not replaced by the reporting error")
}

func TestSuccessKeepsCountsAndArtifactPathWithLongCommand(t *testing.T) {
	s := &Summary{
		Command:     []string{"go", "test", "-json", strings.Repeat("./some/long/package ", 500)},
		RunDir:      "/tmp/run-fixture",
		WallSeconds: 1.25,
	}
	lines := renderSuccess(s)
	out := joined(lines)
	assert.Contains(t, out, "no tests selected", "the zero-test disclosure survives a long command")
	assert.Contains(t, out, "/tmp/run-fixture", "the artifact path survives a long command")
	assertSuccessBudget(t, lines)
}

func assertSuccessBudget(t *testing.T, lines []string) {
	t.Helper()
	require.LessOrEqual(t, len(lines), successMaxLines)
	total := 0
	for _, line := range lines {
		require.LessOrEqual(t, len(line), lineMaxBytes)
		total += len(line) + 1
	}
	require.LessOrEqual(t, total, successMaxBytes)
}

func TestSuccessKeepsArtifactPathAtTheByteBoundary(t *testing.T) {
	s := &Summary{
		Command:     []string{"go", "test", "-json", strings.Repeat("./x/", 5000)},
		RunDir:      "/tmp/" + strings.Repeat("d", 1795),
		WallSeconds: 1,
	}
	lines := renderSuccess(s)
	out := joined(lines)
	assert.Contains(t, out, s.RunDir, "the artifact path survives the exact byte boundary")
	assert.Contains(t, out, "no tests selected")
	assertSuccessBudget(t, lines)
}

func TestSuccessKeepsStatusAndTimeAtExtremePath(t *testing.T) {
	s := &Summary{
		Command:     []string{"go", "test", "-json", "./..."},
		RunDir:      "/tmp/" + strings.Repeat("d", 1975),
		WallSeconds: 1,
	}
	lines := renderSuccess(s)
	out := joined(lines)
	assert.Contains(t, out, "PASS", "the verdict survives an extreme artifact path")
	assert.Contains(t, out, "1.00s", "the elapsed time survives an extreme artifact path")
	assert.Contains(t, out, "no tests selected", "the coverage disclosure survives an extreme path")
	assert.Contains(t, out, s.RunDir, "the artifact path survives")
	assertSuccessBudget(t, lines)
}

func TestSuccessStaysValidUTF8AtTinyCommandBudgets(t *testing.T) {
	s := &Summary{
		Command:     []string{"go", "test", "-json", "./..."},
		RunDir:      "/tmp/" + strings.Repeat("d", 2020),
		WallSeconds: 1.25,
	}
	lines := renderSuccess(s)
	out := joined(lines)
	assert.True(t, utf8.ValidString(out), "a tiny truncation budget must not split the ellipsis")
	assert.Contains(t, out, "PASS")
	assert.Contains(t, out, "1.25s")
	assert.Contains(t, out, "no tests selected")
	assertSuccessBudget(t, lines)
}

func TestTruncateLineTinyBudgetsStayValidUTF8(t *testing.T) {
	const marker = " … [line truncated]"
	for limit := range len(marker) + 3 {
		got := truncateLine("abcdef", limit)
		assert.True(t, utf8.ValidString(got), "limit %d produced invalid UTF-8", limit)
		assert.LessOrEqual(t, len(got), limit, "limit %d exceeded", limit)
	}
}

func TestExcerptStorageIsGloballyBounded(t *testing.T) {
	var sb strings.Builder
	for i := range 64 {
		name := fmt.Sprintf("pkg%02d", i)
		fmt.Fprintf(&sb, `{"Action":"start","Package":%q}`+"\n", name)
		fmt.Fprintf(&sb, `{"Action":"output","Package":%q,"Output":"boom\n"}`+"\n", name)
		fmt.Fprintf(&sb, `{"Action":"fail","Package":%q,"Elapsed":0.1}`+"\n", name)
	}
	stream := sb.String()
	agg := aggregate(t, stream)
	set, err := collectExcerpts(strings.NewReader(stream), agg)
	require.NoError(t, err)
	packages := 0
	for _, ex := range set.items {
		if ex.Kind == excerptPackage {
			packages++
		}
	}
	assert.LessOrEqual(t, packages, maxFailureTargets, "package excerpts share the global cap")

	s := buildSummary(agg, 3)
	s.Command = []string{"go", "test", "-json", "./..."}
	s.RunDir = "/tmp/run-fixture"
	lines := renderFailure(s, set, nil)
	assert.Regexp(t, `[1-9][0-9]* failing targets not shown`, joined(lines))
}

func TestBuildWarningsAreNotFailures(t *testing.T) {
	stream := strings.Join([]string{
		`{"ImportPath":"p [p.test]","Action":"build-output","Output":"warning: something\n"}`,
		`{"Action":"start","Package":"p"}`,
		`{"Action":"output","Package":"p","Output":"ok  \tp\t0.1s\n"}`,
		`{"Action":"pass","Package":"p","Elapsed":0.1}`,
	}, "\n") + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	assert.Empty(t, s.BuildFailures, "a build warning is not a failure")
	assert.NotContains(t, joined(renderSuccess(s)), "BUILD FAIL")
}

func TestOmittedBuildDiagnosticsAreCounted(t *testing.T) {
	var sb strings.Builder
	for i := range 20 {
		path := fmt.Sprintf("pkg%02d [pkg%02d.test]", i, i)
		fmt.Fprintf(&sb, `{"ImportPath":%q,"Action":"build-output","Output":"# %s\n"}`+"\n", path, path)
		fmt.Fprintf(&sb, `{"ImportPath":%q,"Action":"build-output","Output":"pkg%02d.go:1: undefined: nope\n"}`+"\n", path, i)
		fmt.Fprintf(&sb, `{"ImportPath":%q,"Action":"build-fail"}`+"\n", path)
	}
	s, lines := render(t, sb.String(), nil)
	require.Len(t, s.BuildFailures, 20)
	assert.Regexp(t, `[1-9][0-9]* failing targets not shown`, joined(lines),
		"diagnostics beyond the build cap are counted as omitted")
	assertBudget(t, lines)
}

func TestRepeatedCountKeepsEarlierFailure(t *testing.T) {
	stream := strings.Join([]string{
		`{"Action":"run","Package":"p","Test":"TestX"}`,
		`{"Action":"fail","Package":"p","Test":"TestX"}`,
		`{"Action":"run","Package":"p","Test":"TestX"}`,
		`{"Action":"pass","Package":"p","Test":"TestX"}`,
		`{"Action":"fail","Package":"p","Elapsed":1}`,
		`{"Action":"pass","Package":"p","Elapsed":1}`,
	}, "\n") + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	assert.Equal(t, 1, s.Tests.Top.Fail, "a later pass must not erase the earlier failure")
	assert.Equal(t, 1, s.Tests.Top.Pass, "each -count execution is its own occurrence")
	assert.Equal(t, 1, s.Packages.Failed, "a package with any failing occurrence stays failed")
	require.Len(t, s.FailedTests, 1)
	assert.Equal(t, 1, s.FailedTests[0].Count)
}

func TestArtifactErrorIsReportedAsError(t *testing.T) {
	s := &Summary{
		Status:      "error",
		ExitCode:    1,
		Error:       "close events.jsonl: input/output error",
		Command:     []string{"go", "test", "-json", "./..."},
		RunDir:      "/tmp/run-fixture",
		WallSeconds: 1.5,
	}
	lines := renderFailure(s, &excerptSet{}, nil)
	out := joined(lines)
	assert.Contains(t, out, "ERROR")
	assert.Contains(t, out, "input/output error")
	assertBudget(t, lines)

	// A runner error inserted before the trailer never hides the path.
	withError := withRunnerError([]string{"FAIL  x", "logs: /tmp/run-fixture"}, "testsummary: boom")
	require.NotEmpty(t, withError)
	assert.Equal(t, "logs: /tmp/run-fixture", withError[len(withError)-1])
	assert.Contains(t, joined(withError), "testsummary: boom")
}

func TestRunnerErrorKeepsArtifactPathAtTheCap(t *testing.T) {
	lines := []string{"FAIL  x"}
	for range 16 {
		lines = append(lines, strings.Repeat("x", 2000))
	}
	lines = append(lines, "logs: /tmp/run-fixture")
	lines = fitLines(lines, failureMaxLines, failureMaxBytes)
	require.Equal(t, "logs: /tmp/run-fixture", lines[len(lines)-1])

	got := withRunnerError(lines, "testsummary: "+strings.Repeat("e", 1000))
	require.NotEmpty(t, got)
	assert.Equal(t, "FAIL  x", got[0], "the status headline survives at the cap")
	assert.Equal(t, "logs: /tmp/run-fixture", got[len(got)-1], "the artifact path survives at the cap")
	assert.LessOrEqual(t, len(got), failureMaxLines)
	total := 0
	for _, line := range got {
		total += len(line) + 1
	}
	assert.LessOrEqual(t, total, failureMaxBytes)
}

func TestReplayErrorPreservesChildExitStatus(t *testing.T) {
	s := &Summary{ExitCode: 3}
	noteReplayError(s, errors.New("events.jsonl is gone"))
	assert.Equal(t, 3, s.ExitCode, "a replay error must not replace a nonzero child status")
	assert.Equal(t, "error", s.Status)
	assert.NotEmpty(t, s.ProtocolError)

	zero := &Summary{}
	noteReplayError(zero, errors.New("events.jsonl is gone"))
	assert.Equal(t, 1, zero.ExitCode, "a zero child status still becomes a runner error")
}

func TestDiscardedDiagnosticLinesAreCounted(t *testing.T) {
	var sb strings.Builder
	for i := range 40 {
		name := fmt.Sprintf("TestFail%02d", i)
		fmt.Fprintf(&sb, `{"Action":"run","Package":"p","Test":%q}`+"\n", name)
		fmt.Fprintf(&sb, `{"Action":"output","Package":"p","Test":%q,"Output":"boom\n"}`+"\n", name)
		fmt.Fprintf(&sb, `{"Action":"fail","Package":"p","Test":%q}`+"\n", name)
	}
	sb.WriteString(`{"Action":"fail","Package":"p","Elapsed":1}` + "\n")

	_, lines := render(t, sb.String(), nil)
	re := regexp.MustCompile(`[1-9]\d* excerpt lines \([1-9]\d* bytes\) omitted`)
	assert.Regexp(t, re, joined(lines),
		"diagnostics discarded by the target cap are counted as omitted")
	assertBudget(t, lines)
}

func TestUnknownActionIsNotAPass(t *testing.T) {
	stream := `{"Action":"start","Package":"p"}` + "\n" +
		`{"Action":"something-new","Package":"p","Test":"TestX"}` + "\n"

	s := buildSummary(aggregate(t, stream), 3)
	assert.Zero(t, s.Tests.Top.Pass)
	assert.Zero(t, s.Tests.Sub.Pass)
	assert.Equal(t, "unknown", func() string {
		agg := aggregate(t, stream)
		return agg.packages["p"].status()
	}())
}
