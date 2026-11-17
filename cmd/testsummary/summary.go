package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

// Event is the subset of the `go test -json` protocol this runner reads.
// Fields a later toolchain adds are ignored, never required: an unknown
// action is metadata, not a pass, and the child's exit status always
// dominates the reported result.
type Event struct {
	Time        string  `json:"Time"`
	Action      string  `json:"Action"`
	Package     string  `json:"Package"`
	Test        string  `json:"Test"`
	Elapsed     float64 `json:"Elapsed"`
	Output      string  `json:"Output"`
	ImportPath  string  `json:"ImportPath"`
	FailedBuild string  `json:"FailedBuild"`
}

// Output budgets. These are interface constants, not a formatter
// framework: a successful invocation is four short lines, a failed one is
// bounded so a thousand failures still cost one screen.
const (
	successMaxLines = 4
	successMaxBytes = 2 * 1024
	failureMaxLines = 160
	failureMaxBytes = 32 * 1024

	// lineMaxBytes caps one logical line, reconstructed before the cap is
	// applied, so a 70 KiB line split into 1 KiB events cannot bypass it.
	lineMaxBytes = 2 * 1024
	// prefix/tail are the bounded line state kept while reconstructing.
	linePrefixBytes = 1400
	lineTailBytes   = 600

	excerptHeadLines = 6
	excerptTailLines = 16

	// maxFailureTargets bounds excerpt storage globally. Failures beyond
	// it are still counted and reported as omitted.
	maxFailureTargets = 32
	maxBuildTargets   = 8

	trailerReserveLines = 3
	trailerReserveBytes = 1024

	packageTextBytes = 4096
)

type counts struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

type testKey struct {
	Package string
	Test    string
}

type packageState struct {
	Name        string
	Pass        int
	Fail        int
	Skip        int
	Cached      bool
	NoTestFiles bool
	Elapsed     float64
	FailedBuild string
	text        []byte
}

func (p *packageState) addText(out string) {
	p.text = append(p.text, out...)
	if len(p.text) > packageTextBytes {
		p.text = p.text[len(p.text)-packageTextBytes:]
	}
	p.noteText()
}

func (p *packageState) noteText() {
	// Only Go's terminal package-result lines carry these states. Test
	// output may contain the same words without describing the package.
	text := string(p.text)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "ok  \t"+p.Name+"\t(cached)") {
			p.Cached = true
		}
		if strings.HasPrefix(line, "?   \t"+p.Name+"\t[no test files]") {
			p.NoTestFiles = true
		}
	}
}

// status folds the terminal package events into the four states the
// summary distinguishes. A package that skipped every test still passed;
// only a package with no test files is "no tests".
func (p *packageState) status() string {
	switch {
	case p.Fail > 0:
		return "failed"
	case p.Pass > 0:
		return "passed"
	case p.NoTestFiles:
		return "no tests"
	case p.Skip > 0:
		return "skipped"
	default:
		return "unknown"
	}
}

type failure struct {
	Elapsed float64
	Count   int
}

// aggregator records result metadata while the child runs. It keeps no
// test output: excerpts are built by replaying the spooled events after
// the child exits, so passing output never accumulates in memory.
type aggregator struct {
	packages     map[string]*packageState
	packageOrder []string

	top counts
	sub counts

	skips     map[testKey]bool
	skipOrder []testKey

	failedTests     map[testKey]*failure
	failedTestOrder []testKey
	openTests       map[testKey]bool
	openTestOrder   []testKey
	seenTests       map[testKey]bool
	slowTests       map[testKey]float64

	buildImports map[string]bool

	outputLines int64
	outputBytes int64
}

func newAggregator() *aggregator {
	return &aggregator{
		packages:     map[string]*packageState{},
		skips:        map[testKey]bool{},
		failedTests:  map[testKey]*failure{},
		openTests:    map[testKey]bool{},
		seenTests:    map[testKey]bool{},
		slowTests:    map[testKey]float64{},
		buildImports: map[string]bool{},
	}
}

func (a *aggregator) pkgState(name string) *packageState {
	if p, ok := a.packages[name]; ok {
		return p
	}
	p := &packageState{Name: name}
	a.packages[name] = p
	a.packageOrder = append(a.packageOrder, name)
	return p
}

// Add records one event. Repeated `-count` executions are separate
// occurrences: a later pass never erases an earlier failure because
// passes and failures are counted independently.
func (a *aggregator) Add(ev Event) {
	if ev.Output != "" {
		a.outputBytes += int64(len(ev.Output))
		a.outputLines += int64(strings.Count(ev.Output, "\n"))
	}
	switch ev.Action {
	case "build-output":
		// Go also emits build-output for warnings on a successful build,
		// so it is never a failure by itself.
		return
	case "build-fail":
		if ev.ImportPath != "" {
			a.buildImports[ev.ImportPath] = true
		}
		return
	case "start":
		if ev.Package != "" {
			a.pkgState(ev.Package)
		}
		return
	case "pause", "cont", "bench", "":
		return
	case "output":
		if ev.Package == "" {
			return
		}
		if ev.Test == "" {
			a.pkgState(ev.Package).addText(ev.Output)
		}
		return
	case "run":
		if ev.Package == "" {
			return
		}
		a.pkgState(ev.Package)
		if ev.Test != "" {
			key := testKey{ev.Package, ev.Test}
			if !a.seenTests[key] {
				a.seenTests[key] = true
				a.openTestOrder = append(a.openTestOrder, key)
			}
			a.openTests[key] = true
		}
		return
	case "pass", "fail", "skip":
	default:
		return
	}
	if ev.Package == "" {
		return
	}
	p := a.pkgState(ev.Package)
	if ev.Test == "" {
		switch ev.Action {
		case "pass":
			p.Pass++
		case "fail":
			p.Fail++
		case "skip":
			p.Skip++
		}
		if ev.Elapsed > p.Elapsed {
			p.Elapsed = ev.Elapsed
		}
		if ev.FailedBuild != "" {
			p.FailedBuild = ev.FailedBuild
			a.buildImports[ev.FailedBuild] = true
		}
		p.noteText()
		return
	}

	delete(a.openTests, testKey{ev.Package, ev.Test})
	target := countsFor(&a.top, &a.sub, ev.Test)
	switch ev.Action {
	case "pass":
		target.Pass++
	case "skip":
		target.Skip++
		if !a.skips[testKey{ev.Package, ev.Test}] {
			a.skips[testKey{ev.Package, ev.Test}] = true
			a.skipOrder = append(a.skipOrder, testKey{ev.Package, ev.Test})
		}
	case "fail":
		target.Fail++
		key := testKey{ev.Package, ev.Test}
		f := a.failedTests[key]
		if f == nil {
			f = &failure{}
			a.failedTests[key] = f
			a.failedTestOrder = append(a.failedTestOrder, key)
		}
		f.Count++
		if ev.Elapsed > f.Elapsed {
			f.Elapsed = ev.Elapsed
		}
	}
	if !strings.Contains(ev.Test, "/") && ev.Elapsed > a.slowTests[testKey{ev.Package, ev.Test}] {
		a.slowTests[testKey{ev.Package, ev.Test}] = ev.Elapsed
	}
}

func countsFor(top, sub *counts, test string) *counts {
	if strings.Contains(test, "/") {
		return sub
	}
	return top
}

// lineCounter counts a target's diagnostic lines and bytes without
// retaining them, so discarded excerpts still contribute to the trailer.
type lineCounter struct {
	lines   int
	bytes   int64
	pending bool
}

func (c *lineCounter) add(s string) {
	c.bytes += int64(len(s))
	c.lines += strings.Count(s, "\n")
	if s != "" {
		c.pending = !strings.HasSuffix(s, "\n")
	}
}

func (c *lineCounter) flush() {
	if c.pending {
		c.lines++
		c.bytes++
		c.pending = false
	}
}

// lineBuf reconstructs logical lines with bounded state: a prefix, a
// rolling tail and counters. It never holds an arbitrarily long line.
type lineBuf struct {
	prefix []byte
	tail   []byte
	total  int

	head  []string
	ring  []string
	lines int
	bytes int64
}

func (b *lineBuf) add(s string) {
	for s != "" {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			b.addBytes(s)
			return
		}
		b.addBytes(s[:i])
		b.finishLine()
		s = s[i+1:]
	}
}

func (b *lineBuf) addBytes(s string) {
	if s == "" {
		return
	}
	b.total += len(s)
	if room := linePrefixBytes - len(b.prefix); room > 0 {
		if room > len(s) {
			room = len(s)
		}
		b.prefix = append(b.prefix, s[:room]...)
		s = s[room:]
	}
	if s == "" {
		return
	}
	b.tail = append(b.tail, s...)
	if len(b.tail) > lineTailBytes {
		b.tail = b.tail[len(b.tail)-lineTailBytes:]
	}
}

func (b *lineBuf) finishLine() {
	text := b.render()
	b.lines++
	b.bytes += int64(b.total) + 1
	if len(b.head) < excerptHeadLines {
		b.head = append(b.head, text)
	} else {
		b.ring = append(b.ring, text)
		if len(b.ring) > excerptTailLines {
			b.ring = b.ring[1:]
		}
	}
	b.prefix = b.prefix[:0]
	b.tail = b.tail[:0]
	b.total = 0
}

// flush records a final line that the child never terminated.
func (b *lineBuf) flush() {
	if b.total > 0 {
		b.finishLine()
	}
}

func (b *lineBuf) render() string {
	omitted := b.total - len(b.prefix) - len(b.tail)
	if omitted <= 0 {
		// The prefix and the tail are adjacent pieces of the same line:
		// join them before sanitizing so a rune split at the seam
		// survives, and never drop the tail of a reconstructed line.
		return truncateLine(strings.ToValidUTF8(string(b.prefix)+string(b.tail), ""), lineMaxBytes)
	}
	// The pieces are separated by the omission marker, so sanitize them
	// separately; the emitted line must stay valid UTF-8.
	var sb strings.Builder
	sb.WriteString(strings.ToValidUTF8(string(b.prefix), ""))
	fmt.Fprintf(&sb, " … [%d bytes omitted] … ", omitted)
	sb.WriteString(strings.ToValidUTF8(string(b.tail), ""))
	return truncateLine(sb.String(), lineMaxBytes)
}

func truncateLine(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(s) <= limit {
		return s
	}
	const marker = " … [line truncated]"
	if limit <= len(marker) {
		if limit >= len(marker) {
			return marker
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(marker[cut]) {
			cut--
		}
		return marker[:cut]
	}
	cut := limit - len(marker)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + marker
}

type excerptKind int

const (
	excerptBuild excerptKind = iota
	excerptPackage
	excerptTest
	excerptUnreported
)

type excerpt struct {
	Kind       excerptKind
	Package    string
	Test       string
	Elapsed    float64
	Head       []string
	Tail       []string
	TotalLines int
	TotalBytes int64
}

func (e *excerpt) omittedLines() int {
	n := e.TotalLines - len(e.Head) - len(e.Tail)
	if n < 0 {
		return 0
	}
	return n
}

type excerptSet struct {
	items      []*excerpt
	totalLines int
	totalBytes int64
}

// collectExcerpts replays the spooled event stream once and builds
// bounded excerpts for failing packages, failing tests and build
// diagnostics. Storage is bounded globally, not per test.
func collectExcerpts(r io.Reader, a *aggregator) (*excerptSet, error) {
	wanted := map[testKey]bool{}
	trackedTests := a.failedTestOrder
	if len(trackedTests) > maxFailureTargets {
		trackedTests = trackedTests[:maxFailureTargets]
	}
	for _, key := range trackedTests {
		wanted[key] = true
	}
	// A test that never reported a result (a timeout, a hard panic, an
	// interrupted binary) still owns the diagnostics that explain the
	// package failure.
	unreported := make([]testKey, 0, len(a.openTestOrder))
	for _, key := range a.openTestOrder {
		if a.openTests[key] && a.packages[key.Package] != nil && a.packages[key.Package].status() == "failed" {
			unreported = append(unreported, key)
		}
	}
	for _, key := range unreported {
		if len(wanted) >= maxFailureTargets {
			break
		}
		wanted[key] = true
	}
	// Package diagnostics share the same global cap; the rest are counted
	// as omitted rather than retained.
	for _, name := range a.packageOrder {
		if len(wanted) >= maxFailureTargets {
			break
		}
		if a.packages[name].status() == "failed" {
			wanted[testKey{Package: name}] = true
		}
	}

	builds := make([]string, 0, len(a.buildImports))
	for path := range a.buildImports {
		builds = append(builds, path)
	}
	sort.Strings(builds)
	if len(builds) > maxBuildTargets {
		builds = builds[:maxBuildTargets]
	}

	counters := map[testKey]*lineCounter{}
	counterFor := func(key testKey) *lineCounter {
		c := counters[key]
		if c == nil {
			c = &lineCounter{}
			counters[key] = c
		}
		return c
	}
	testCandidate := func(key testKey) bool {
		if _, ok := a.failedTests[key]; ok {
			return true
		}
		return a.openTests[key] && a.packages[key.Package] != nil && a.packages[key.Package].status() == "failed"
	}
	packageCandidate := func(name string) bool {
		p := a.packages[name]
		return p != nil && p.status() == "failed"
	}

	bufs := map[testKey]*lineBuf{}
	bufFor := func(key testKey) *lineBuf {
		if !wanted[key] {
			return nil
		}
		b := bufs[key]
		if b == nil {
			b = &lineBuf{}
			bufs[key] = b
		}
		return b
	}
	buildBufs := map[string]*lineBuf{}
	for _, path := range builds {
		buildBufs[path] = &lineBuf{}
	}

	dec := json.NewDecoder(r)
	for {
		var ev Event
		err := dec.Decode(&ev)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode spooled events: %w", err)
		}
		switch ev.Action {
		case "build-output":
			if a.buildImports[ev.ImportPath] {
				counterFor(testKey{Package: ev.ImportPath}).add(ev.Output)
			}
			if b := buildBufs[ev.ImportPath]; b != nil {
				b.add(ev.Output)
			}
		case "output":
			switch {
			case ev.Test != "":
				key := testKey{Package: ev.Package, Test: ev.Test}
				if testCandidate(key) {
					counterFor(key).add(ev.Output)
				}
				if b := bufFor(key); b != nil {
					b.add(ev.Output)
				}
			case ev.ImportPath != "":
				if a.buildImports[ev.ImportPath] {
					counterFor(testKey{Package: ev.ImportPath}).add(ev.Output)
				}
				if b := buildBufs[ev.ImportPath]; b != nil {
					b.add(ev.Output)
				}
			default:
				key := testKey{Package: ev.Package}
				if packageCandidate(ev.Package) {
					counterFor(key).add(ev.Output)
				}
				if b := bufFor(key); b != nil {
					b.add(ev.Output)
				}
			}
		case "run", "pass", "fail", "skip":
			if ev.Test != "" {
				if b := bufFor(testKey{Package: ev.Package, Test: ev.Test}); b != nil {
					b.flush()
				}
			} else if ev.Package != "" {
				if b := bufFor(testKey{Package: ev.Package}); b != nil {
					b.flush()
				}
			}
		}
	}

	for _, c := range counters {
		c.flush()
	}
	set := &excerptSet{}
	for _, c := range counters {
		set.totalLines += c.lines
		set.totalBytes += c.bytes
	}
	appendExcerpt := func(ex *excerpt, b *lineBuf) {
		if b != nil {
			b.flush()
			ex.Head, ex.Tail = b.head, b.ring
			ex.TotalLines, ex.TotalBytes = b.lines, b.bytes
		}
		set.items = append(set.items, ex)
	}
	for _, path := range builds {
		appendExcerpt(&excerpt{Kind: excerptBuild, Package: path}, buildBufs[path])
	}
	for _, name := range a.packageOrder {
		p := a.packages[name]
		key := testKey{Package: name}
		if p.status() != "failed" || !wanted[key] {
			continue
		}
		appendExcerpt(&excerpt{Kind: excerptPackage, Package: name, Elapsed: p.Elapsed}, bufs[key])
	}
	for _, key := range trackedTests {
		f := a.failedTests[key]
		appendExcerpt(&excerpt{
			Kind:    excerptTest,
			Package: key.Package,
			Test:    key.Test,
			Elapsed: f.Elapsed,
		}, bufs[key])
	}
	for _, key := range unreported {
		if !wanted[key] {
			continue
		}
		appendExcerpt(&excerpt{Kind: excerptUnreported, Package: key.Package, Test: key.Test}, bufs[key])
	}
	return set, nil
}

// Summary is written to summary.json. It never contains the process
// environment.
type Summary struct {
	Status          string      `json:"status"`
	ExitCode        int         `json:"exit_code"`
	Interrupted     bool        `json:"interrupted,omitempty"`
	Signal          string      `json:"signal,omitempty"`
	ProtocolError   string      `json:"protocol_error,omitempty"`
	Error           string      `json:"error,omitempty"`
	Command         []string    `json:"command"`
	WallSeconds     float64     `json:"wall_seconds"`
	RunDir          string      `json:"run_dir"`
	Packages        PkgCounts   `json:"packages"`
	Tests           TestCounts  `json:"tests"`
	Skips           []TestRef   `json:"skips,omitempty"`
	SlowPackages    []SlowRef   `json:"slow_packages,omitempty"`
	SlowTests       []SlowRef   `json:"slow_tests,omitempty"`
	FailedPackages  []FailedRef `json:"failed_packages,omitempty"`
	FailedTests     []FailedRef `json:"failed_tests,omitempty"`
	UnreportedTests []TestRef   `json:"unreported_tests,omitempty"`
	BuildFailures   []string    `json:"build_failures,omitempty"`
	OutputLines     int64       `json:"output_lines"`
	OutputBytes     int64       `json:"output_bytes"`
	StderrBytes     int64       `json:"stderr_bytes"`
}

type PkgCounts struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Cached  int `json:"cached"`
	NoTests int `json:"no_tests"`
	Skipped int `json:"skipped"`
	Unknown int `json:"unknown"`
}

type TestCounts struct {
	Top counts `json:"top_level"`
	Sub counts `json:"subtests"`
}

type TestRef struct {
	Package string `json:"package"`
	Test    string `json:"test"`
}

type SlowRef struct {
	Package string  `json:"package"`
	Test    string  `json:"test,omitempty"`
	Elapsed float64 `json:"elapsed_seconds"`
	Cached  bool    `json:"cached,omitempty"`
}

type FailedRef struct {
	Package string  `json:"package"`
	Test    string  `json:"test,omitempty"`
	Elapsed float64 `json:"elapsed_seconds,omitempty"`
	Count   int     `json:"occurrences,omitempty"`
}

func buildSummary(a *aggregator, slow int) *Summary {
	s := &Summary{
		Tests:       TestCounts{Top: a.top, Sub: a.sub},
		OutputLines: a.outputLines,
		OutputBytes: a.outputBytes,
	}
	for _, name := range a.packageOrder {
		p := a.packages[name]
		switch p.status() {
		case "passed":
			s.Packages.Passed++
			if p.Cached {
				s.Packages.Cached++
			}
		case "failed":
			s.Packages.Failed++
		case "no tests":
			s.Packages.NoTests++
		case "skipped":
			s.Packages.Skipped++
		default:
			s.Packages.Unknown++
		}
	}
	for _, key := range a.skipOrder {
		s.Skips = append(s.Skips, TestRef(key))
	}
	for _, key := range a.failedTestOrder {
		f := a.failedTests[key]
		s.FailedTests = append(s.FailedTests, FailedRef{
			Package: key.Package,
			Test:    key.Test,
			Elapsed: f.Elapsed,
			Count:   f.Count,
		})
	}
	for _, name := range a.packageOrder {
		p := a.packages[name]
		if p.status() != "failed" {
			continue
		}
		s.FailedPackages = append(s.FailedPackages, FailedRef{
			Package: name,
			Elapsed: p.Elapsed,
			Count:   p.Fail,
		})
	}
	builds := make([]string, 0, len(a.buildImports))
	for path := range a.buildImports {
		builds = append(builds, path)
	}
	sort.Strings(builds)
	s.BuildFailures = builds
	for _, key := range a.openTestOrder {
		if !a.openTests[key] {
			continue
		}
		if p := a.packages[key.Package]; p != nil && p.status() == "failed" {
			s.UnreportedTests = append(s.UnreportedTests, TestRef(key))
		}
	}

	// Cached test events are a historical replay, not fresh execution;
	// their old durations never appear as current slow items.
	for _, name := range a.packageOrder {
		p := a.packages[name]
		if p.Cached || p.Elapsed <= 0 {
			continue
		}
		s.SlowPackages = append(s.SlowPackages, SlowRef{Package: name, Elapsed: p.Elapsed})
	}
	sort.Slice(s.SlowPackages, func(i, j int) bool {
		return s.SlowPackages[i].Elapsed > s.SlowPackages[j].Elapsed
	})
	if len(s.SlowPackages) > slow {
		s.SlowPackages = s.SlowPackages[:slow]
	}
	slowTests := make([]SlowRef, 0, len(a.slowTests))
	for key, elapsed := range a.slowTests {
		p := a.packages[key.Package]
		if p == nil || p.Cached || elapsed <= 0 {
			continue
		}
		slowTests = append(slowTests, SlowRef{Package: key.Package, Test: key.Test, Elapsed: elapsed})
	}
	sort.Slice(slowTests, func(i, j int) bool {
		if slowTests[i].Elapsed != slowTests[j].Elapsed {
			return slowTests[i].Elapsed > slowTests[j].Elapsed
		}
		return slowTests[i].Test < slowTests[j].Test
	})
	if len(slowTests) > slow {
		slowTests = slowTests[:slow]
	}
	s.SlowTests = slowTests
	return s
}

// lineBudget enforces the total emitted-line and byte caps.
type lineBudget struct {
	lines    []string
	used     int
	maxLines int
	maxBytes int
}

func (b *lineBudget) add(line string) bool {
	line = truncateLine(line, lineMaxBytes)
	if len(b.lines) >= b.maxLines {
		return false
	}
	if b.used+len(line)+1 > b.maxBytes {
		return false
	}
	b.lines = append(b.lines, line)
	b.used += len(line) + 1
	return true
}

func (b *lineBudget) full() bool {
	return len(b.lines) >= b.maxLines || b.used >= b.maxBytes
}

func displayCommand(argv []string) string {
	if len(argv) >= 3 && argv[0] == "go" && argv[1] == "test" && argv[2] == "-json" {
		argv = argv[3:]
	}
	if len(argv) == 0 {
		return "(no packages)"
	}
	return strings.Join(argv, " ")
}

// successCommandBytes caps the echoed command so a long package list
// cannot crowd out the counts, the no-tests disclosure or the log path.
const successCommandBytes = 200

func renderSuccess(s *Summary) []string {
	elapsed := fmt.Sprintf("%.2fs", s.WallSeconds)
	noTestsSelected := s.Tests.Top.Pass+s.Tests.Top.Fail+s.Tests.Top.Skip+
		s.Tests.Sub.Pass+s.Tests.Sub.Fail+s.Tests.Sub.Skip == 0
	verdict := "PASS"
	if noTestsSelected {
		// The disclosure lives in the mandatory verdict line, so a long
		// artifact path can never hide a run that selected no tests.
		verdict = "PASS  no tests selected"
	}
	status := verdict + "  " + elapsed
	countsLine := fmt.Sprintf(
		"packages: %d passed, %d cached, %d no tests; tests: %d passed, %d skipped; subtests: %d passed, %d skipped",
		s.Packages.Passed, s.Packages.Cached, s.Packages.NoTests,
		s.Tests.Top.Pass, s.Tests.Top.Skip, s.Tests.Sub.Pass, s.Tests.Sub.Skip)
	if noTestsSelected {
		countsLine = fmt.Sprintf(
			"packages: %d passed, %d cached, %d no tests; no tests selected",
			s.Packages.Passed, s.Packages.Cached, s.Packages.NoTests)
	}
	logsLine := truncateLine("logs: "+s.RunDir, lineMaxBytes)

	// The status with its elapsed time and the artifact path are
	// mandatory. Truncate the path only if the two cannot fit together.
	if len(status)+1+len(logsLine)+1 > successMaxBytes {
		logsLine = truncateLine(logsLine, successMaxBytes-len(status)-2)
	}
	remaining := successMaxBytes - len(status) - 1 - len(logsLine) - 1
	lines := []string{status}

	// The counts come next; the command and the slow list are optional.
	if len(countsLine)+1 <= remaining {
		lines = append(lines, countsLine)
		remaining -= len(countsLine) + 1
	}
	if room := remaining - 2; room > 0 {
		limit := successCommandBytes
		if limit > room {
			limit = room
		}
		if command := strings.TrimSpace(truncateLine(displayCommand(s.Command), limit)); command != "" {
			lines[0] = verdict + "  " + command + "  " + elapsed
			remaining -= len(command) + 2
		}
	}
	if slow := slowLine(s); slow != "" && remaining > 0 {
		lines = append(lines, truncateLine(slow, remaining-1))
	}
	lines = append(lines, logsLine)
	return fitLines(lines, successMaxLines, successMaxBytes)
}

func slowLine(s *Summary) string {
	var items []string
	for _, p := range s.SlowPackages {
		items = append(items, fmt.Sprintf("%s %.1fs", p.Package, p.Elapsed))
	}
	if len(items) == 0 {
		for _, t := range s.SlowTests {
			items = append(items, fmt.Sprintf("%s %s %.1fs", t.Package, t.Test, t.Elapsed))
		}
	}
	if len(items) == 0 {
		return ""
	}
	return "slow: " + strings.Join(items, ", ")
}

func fitLines(lines []string, maxLines, maxBytes int) []string {
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	out := make([]string, 0, len(lines))
	used := 0
	for _, line := range lines {
		line = truncateLine(line, lineMaxBytes)
		remaining := maxBytes - used - 1
		if remaining <= 0 {
			break
		}
		if len(line) > remaining {
			line = truncateLine(line, remaining)
		}
		out = append(out, line)
		used += len(line) + 1
	}
	return out
}

func renderFailure(s *Summary, set *excerptSet, stderr *excerpt) []string {
	if set == nil {
		set = &excerptSet{}
	}
	// Reserve room for the trailer and the artifact path itself: a long
	// log directory must not make the path disappear from the output.
	logsLine := "logs: " + s.RunDir
	reserve := trailerReserveBytes
	if need := len(logsLine) + 1 + 256; need > reserve {
		reserve = need
	}
	if reserve > failureMaxBytes/2 {
		reserve = failureMaxBytes / 2
	}
	budget := &lineBudget{
		maxLines: failureMaxLines - trailerReserveLines,
		maxBytes: failureMaxBytes - reserve,
	}

	headline := fmt.Sprintf("FAIL  %s  %.2fs  exit %d", displayCommand(s.Command), s.WallSeconds, s.ExitCode)
	switch {
	case s.Interrupted:
		headline = fmt.Sprintf("INTERRUPTED  %s  %.2fs  %s", displayCommand(s.Command), s.WallSeconds, s.Signal)
	case s.ProtocolError != "":
		headline = fmt.Sprintf("ERROR  %s  %.2fs  %s", displayCommand(s.Command), s.WallSeconds, s.ProtocolError)
	case s.Error != "":
		headline = fmt.Sprintf("ERROR  %s  %.2fs  %s", displayCommand(s.Command), s.WallSeconds, s.Error)
	}
	budget.add(headline)

	countsLine := fmt.Sprintf(
		"packages: %d failed, %d passed, %d cached, %d no tests; tests: %d failed (%d top-level, %d subtest), %d passed, %d skipped; subtests: %d failed, %d passed, %d skipped",
		s.Packages.Failed, s.Packages.Passed, s.Packages.Cached, s.Packages.NoTests,
		s.Tests.Top.Fail+s.Tests.Sub.Fail, s.Tests.Top.Fail, s.Tests.Sub.Fail,
		s.Tests.Top.Pass, s.Tests.Top.Skip,
		s.Tests.Sub.Fail, s.Tests.Sub.Pass, s.Tests.Sub.Skip)
	budget.add(countsLine)

	emittedLines := 0
	emittedBytes := int64(0)
	shownFailures := 0
	for _, ex := range set.items {
		if budget.full() {
			break
		}
		lines, bytes, shown := appendExcerpt(budget, ex)
		if shown {
			shownFailures++
		}
		emittedLines += lines
		emittedBytes += bytes
	}
	if stderr != nil && stderr.TotalLines > 0 && !budget.full() {
		if budget.add("--- stderr") {
			_, _ = appendExcerptBody(budget, stderr)
		}
	}

	// Reserve space for the trailer: it names what was omitted and where
	// the complete evidence lives, so truncation cannot hide either.
	totalFailures := len(s.FailedTests) + len(s.FailedPackages) + len(s.BuildFailures) + len(s.UnreportedTests)
	notShown := totalFailures - shownFailures
	if notShown < 0 {
		notShown = 0
	}
	omittedLines := set.totalLines - emittedLines
	omittedBytes := set.totalBytes - emittedBytes
	if omittedLines < 0 {
		omittedLines = 0
	}
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	// The trailer gets its own reserved budget, so truncating the
	// diagnostics can never drop the omitted counts or the artifact path.
	trailerBudget := &lineBudget{maxLines: trailerReserveLines, maxBytes: reserve}
	trailerBudget.add(fmt.Sprintf(
		"%d failing targets not shown; %d excerpt lines (%d bytes) omitted of %d output lines (%d bytes)",
		notShown, omittedLines, omittedBytes, s.OutputLines, s.OutputBytes))
	trailerBudget.add(logsLine)
	return fitLines(append(budget.lines, trailerBudget.lines...), failureMaxLines, failureMaxBytes)
}

func appendExcerpt(budget *lineBudget, ex *excerpt) (lines int, bytes int64, shown bool) {
	var header string
	switch ex.Kind {
	case excerptBuild:
		header = "--- BUILD FAIL " + ex.Package
	case excerptPackage:
		header = fmt.Sprintf("--- FAIL %s (%.2fs)", ex.Package, ex.Elapsed)
	case excerptUnreported:
		header = fmt.Sprintf("--- FAIL %s %s (no result reported: panic or timeout)", ex.Package, ex.Test)
	default:
		header = fmt.Sprintf("--- FAIL %s %s (%.2fs)", ex.Package, ex.Test, ex.Elapsed)
	}
	if !budget.add(header) {
		return 0, 0, false
	}
	lines, bytes = appendExcerptBody(budget, ex)
	return lines, bytes, true
}

func appendExcerptBody(budget *lineBudget, ex *excerpt) (lines int, bytes int64) {
	emit := func(text string) bool {
		if !budget.add("  " + text) {
			return false
		}
		lines++
		bytes += int64(len(text)) + 1
		return true
	}
	for _, line := range ex.Head {
		if !emit(line) {
			return lines, bytes
		}
	}
	if omitted := ex.omittedLines(); omitted > 0 {
		if !budget.add(fmt.Sprintf("  ... [%d lines omitted] ...", omitted)) {
			return lines, bytes
		}
	}
	for _, line := range ex.Tail {
		if !emit(line) {
			return lines, bytes
		}
	}
	return lines, bytes
}
