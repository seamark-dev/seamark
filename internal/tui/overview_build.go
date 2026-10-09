package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// ovPhase is one step of an index build as the progress panel shows it.
type ovPhase struct {
	// id is the phase name the index engine reports; "prepare" is the
	// console's own step before the first event.
	id    string
	label string
}

// ovPhases lists the build steps in order. The engine sends no event
// while it opens the database and loads the parse cache, so the
// console shows that time as its own "prepare" step.
var ovPhases = []ovPhase{
	{id: "prepare", label: "Open the database"},
	{id: "scan", label: "Scan files"},
	{id: "parse", label: "Parse files"},
	{id: "history", label: "Mine git history"},
	{id: "write", label: "Write the index"},
}

// ovEventBuffer bounds the queue between the build and the event loop.
// The event loop drains it continuously; the bound keeps a stalled
// view from holding an unbounded backlog.
const ovEventBuffer = 128

// ovParseThrottle is the shortest gap between two forwarded parse
// events. The engine reports every file; the view needs far fewer.
const ovParseThrottle = 50 * time.Millisecond

// ovLogKeep is the number of recent warning lines the panel keeps.
const ovLogKeep = 4

// ovBuild is one index build: its choices, its progress and its result.
type ovBuild struct {
	// id tells this build's messages apart from an earlier build's.
	id    int
	force bool
	ch    chan tea.Msg

	started  time.Time
	finished time.Time
	running  bool

	phases     [5]phaseState
	seen       [5]bool
	files      int
	parseDone  int
	parseTotal int
	logs       []string

	summary *index.Summary
	err     error
}

// ovBuildEventMsg carries one progress event to the event loop.
type ovBuildEventMsg struct {
	id int
	ev IndexEvent
}

// ovBuildDoneMsg carries the build result. It is the last message on
// the build channel, so every progress event arrives before it.
type ovBuildDoneMsg struct {
	id      int
	summary *index.Summary
	err     error
}

// ovPhaseIndex returns the position of a phase, or -1.
func ovPhaseIndex(id string) int {
	for i, p := range ovPhases {
		if p.id == id {
			return i
		}
	}

	return -1
}

// newOvBuild starts the progress record with the prepare step active.
func newOvBuild(id int, force bool, now time.Time) *ovBuild {
	b := &ovBuild{id: id, force: force, started: now, running: true, ch: make(chan tea.Msg, ovEventBuffer)}
	b.phases[0], b.seen[0] = phaseActive, true

	return b
}

// apply records one progress event. An event for a later phase closes
// the earlier phases, because the engine sends no closing event for a
// phase it leaves, and on failure it sends none at all.
func (b *ovBuild) apply(ev IndexEvent) {
	if ev.Phase == "" {
		if ev.Log != "" {
			b.logs = append(b.logs, ev.Log)
			if len(b.logs) > ovLogKeep {
				b.logs = b.logs[len(b.logs)-ovLogKeep:]
			}
		}

		return
	}

	idx := ovPhaseIndex(ev.Phase)
	if idx < 0 {
		return
	}

	for i := 0; i < idx; i++ {
		switch {
		case b.seen[i]:
			b.phases[i] = phaseDone
		default:
			b.phases[i] = phaseSkipped
		}
	}

	b.seen[idx] = true
	b.phases[idx] = phaseActive

	switch ev.Phase {
	case "scan":
		b.files = ev.Total
	case "parse":
		b.parseDone, b.parseTotal = ev.Done, ev.Total
	}

	if ev.Total > 0 && ev.Done >= ev.Total {
		b.phases[idx] = phaseDone
	}
}

// finish records the result. On success every seen phase is done and
// every other phase was skipped; on failure the active phase failed and
// later phases stay pending, because they never ran.
func (b *ovBuild) finish(sum *index.Summary, err error, now time.Time) {
	b.running, b.finished, b.summary, b.err = false, now, sum, err

	for i := range b.phases {
		switch {
		case err != nil && b.phases[i] == phaseActive:
			b.phases[i] = phaseFailed
		case err != nil:
		case b.seen[i]:
			b.phases[i] = phaseDone
		default:
			b.phases[i] = phaseSkipped
		}
	}
}

// ovWait returns a command that delivers the next build message. It
// returns nil after the last one, so the chain ends by itself.
func ovWait(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}

		return msg
	}
}

// ovRunBuild runs the build and streams its events, then the result,
// through ch. It runs in a command goroutine, never in Update. A panic
// becomes a failed result, so the mutation slot always frees.
func ovRunBuild(svc Setup, id int, force bool, ch chan<- tea.Msg) {
	defer close(ch)

	var (
		sum *index.Summary
		err error
	)

	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("the index build stopped on an internal error: %v", r)
			}
		}()

		sum, err = svc.BuildIndex(force, ovForward(id, ch))
	}()

	ch <- ovBuildDoneMsg{id: id, summary: sum, err: err}
}

// ovForward adapts engine events to build messages. It forwards at most
// one intermediate parse event per ovParseThrottle and drops it when
// the queue is full, so progress never slows the parse. The first and
// the last parse event and every other event always arrive.
func ovForward(id int, ch chan<- tea.Msg) func(IndexEvent) {
	var last time.Time

	return func(ev IndexEvent) {
		msg := ovBuildEventMsg{id: id, ev: ev}

		if ev.Phase == "parse" && ev.Done > 1 && ev.Done < ev.Total {
			now := time.Now()
			if now.Sub(last) < ovParseThrottle {
				return
			}

			select {
			case ch <- msg:
				last = now
			default:
			}

			return
		}

		ch <- msg
	}
}

// openBuild opens the build confirmation, unless another mutation runs
// or the index is newer than this binary.
func (o *overview) openBuild() tea.Cmd {
	if o.st.busy() {
		return toast(ui.ToneAttention, "Another operation is running: "+o.st.mutation)
	}

	if !o.canBuild() {
		return toast(ui.ToneAttention, "A newer seamark wrote this index. This binary never rebuilds it.")
	}

	o.confirm = true

	return nil
}

// canBuild reports whether a build is offered. A database from a newer
// seamark is never rebuilt, because that would discard its data.
func (o *overview) canBuild() bool {
	return !o.indexNewer()
}

// startBuild claims the mutation slot and starts the build. A second
// start while a mutation runs only shows a message.
func (o *overview) startBuild(force bool) tea.Cmd {
	o.confirm = false

	if !o.st.begin("Building the index") {
		return toast(ui.ToneAttention, "Another operation is running: "+o.st.mutation)
	}

	b := newOvBuild(o.buildSeq.start(), force, time.Now())
	o.build, o.showBuild = b, true
	ch := b.ch

	var svc Setup = o.st.svc

	run := func() tea.Msg {
		ovRunBuild(svc, b.id, force, ch)

		return nil
	}

	return tea.Batch(run, ovWait(ch))
}

// onBuildEvent records a progress event and waits for the next one.
func (o *overview) onBuildEvent(msg ovBuildEventMsg) tea.Cmd {
	b := o.build
	if b == nil || msg.id != b.id {
		return nil
	}

	b.apply(msg.ev)

	return ovWait(b.ch)
}

// onBuildDone records the result, frees the mutation slot and reloads
// the sections the index feeds. It never retries.
func (o *overview) onBuildDone(msg ovBuildDoneMsg) tea.Cmd {
	b := o.build
	if b == nil || msg.id != b.id {
		return nil
	}

	b.finish(msg.summary, msg.err, time.Now())

	note := toast(ui.ToneSuccess, "Index build finished.")
	if msg.err != nil {
		note = toast(ui.ToneFailure, "Index build failed: "+ui.ErrText(msg.err))
	}

	// The evidence view shows the index state and reads its counts
	// from the index, so it reloads too.
	return tea.Batch(mutationDone, refresh(secIndex, secDoctor, secLedger, secStats, secLearning, secPins), note)
}

// ovModalW is the widest build modal. It keeps the lines of the modal
// short enough to read at a glance on a wide terminal.
const ovModalW = 72

// modal returns the build confirmation, progress or result as a modal
// block. The shell dims the frame under it and centers it.
func (o *overview) modal(w, h int) (string, bool) {
	switch {
	case o.confirm:
		return o.confirmModal(w, h), true
	case o.build != nil && o.showBuild:
		return o.buildModal(w, h), true
	}

	return "", false
}

// modalBox renders a build modal: the body lines, then a rule and the
// key hints. The hints always stay visible; a body taller than the area
// gives up its last lines and ends with a note that text is hidden.
func (o *overview) modalBox(title, right string, body []string, hints string, w, h int) string {
	t := o.st.theme
	bw := max(ui.DialogMinBox, min(w-4, ovModalW))
	inner := ui.BoxInner(bw)

	for len(body) > 0 && ui.IsBlank(body[len(body)-1]) {
		body = body[:len(body)-1]
	}

	var tail []string
	if hints != "" {
		tail = []string{"", t.Rule.Render(strings.Repeat(t.Glyphs.RuleLight, inner)), hints}
	}

	if room := max(0, h-2-len(tail)); len(body) > room {
		body = append([]string(nil), body[:room]...)
		if room > 0 {
			body[room-1] = t.Faint.Render(ui.Truncate(dialogMoreNote(t), inner, t.Glyphs.Ellipsis))
		}
	}

	lines := make([]string, 0, len(body)+len(tail))
	lines = append(append(lines, body...), tail...)

	return t.Box(ui.BoxOpts{
		W: bw, H: min(h, len(lines)+2), Title: title, Right: right, Focus: true, Fill: true, Lines: lines,
	})
}

// modalInner returns the content width of a build modal in a w-wide
// area, so callers wrap the body to the width the box shows.
func modalInner(w int) int {
	return ui.BoxInner(max(ui.DialogMinBox, min(w-4, ovModalW)))
}

// ovBullet renders a bullet paragraph with a hanging indent.
func (o *overview) ovBullet(text string, w int) []string {
	t := o.st.theme
	lines := ovStyled(t.Body.Render, ui.Wrap(text, max(1, w-2)))

	for i := range lines {
		prefix := "  "
		if i == 0 {
			prefix = t.Muted.Render(t.Glyphs.Bullet) + " "
		}

		lines[i] = prefix + lines[i]
	}

	return lines
}

// confirmModal renders the build confirmation. It states exactly what a
// build writes and what it never does, before anything is written.
func (o *overview) confirmModal(w, h int) string {
	t := o.st.theme
	inner := modalInner(w)

	path := o.st.svc.DBPath()
	if o.st.index.loaded && o.st.index.value.Probe.Path != "" {
		path = o.st.index.value.Probe.Path
	}

	body := ovStyled(t.Body.Render, ui.Wrap("Seamark parses the workspace locally and mines the git history. "+
		"The build writes:", inner))
	body = append(body, "  "+commandText(t, ui.TruncateLeft(ui.OneLine(path), inner-2, t.Glyphs.Ellipsis)), "")
	body = append(body, o.ovBullet("No review fetch, no network call, no inference.", inner)...)
	body = append(body, o.ovBullet("A build cannot be cancelled once it starts. q then offers to exit after it finishes.",
		inner)...)
	body = append(body, "")
	body = append(body, ovStyled(t.Muted.Render, ui.Wrap("Build reuses the parse cache and skips the work when the "+
		"index is current. Rebuild from scratch reparses every file.", inner))...)

	hints := t.Hints([]key.Binding{
		ui.WithHelp(ui.Nav.Enter, "enter", "build"), ovKeys.Force, ui.WithHelp(ui.Nav.Back, "esc", "cancel"),
	})

	return o.modalBox(t.Title.Render("Build the index?"), "", body, hints, w, h)
}

// buildModal renders the progress, the result or the failure of the
// latest build.
func (o *overview) buildModal(w, h int) string {
	t := o.st.theme
	b := o.build
	inner := modalInner(w)

	title := "Index build"
	if b.force {
		title = "Index rebuild from scratch"
	}

	took := b.elapsed(time.Now())

	switch {
	case b.running:
		return o.modalBox(t.Title.Render(title), t.Muted.Render(took+" elapsed"), o.progressBody(inner), "", w, h)
	case b.err != nil:
		return o.modalBox(t.Title.Render(title), t.Muted.Render("after "+took), o.failureBody(inner),
			o.resultHints(), w, h)
	default:
		return o.modalBox(t.Title.Render(title), t.Muted.Render("took "+took), o.resultBody(inner),
			o.resultHints(), w, h)
	}
}

// resultHints are the keys of a finished build.
func (o *overview) resultHints() string {
	return o.st.theme.Hints([]key.Binding{
		ui.WithHelp(ui.Nav.Enter, "enter", "close"), ui.WithHelp(ovKeys.Build, "b", "build again"),
	})
}

// elapsed renders the build duration so far, or in total. A finished
// build prefers the engine's own duration, which excludes the time the
// result spent in the event queue.
func (b *ovBuild) elapsed(now time.Time) string {
	if !b.running && b.summary != nil && b.summary.Duration > 0 {
		return b.summary.Duration.Round(100 * time.Millisecond).String()
	}

	end := now
	if !b.running {
		end = b.finished
	}

	return (end.Sub(b.started) / time.Second * time.Second).String()
}

// progressBody renders the running build: the cancellation limit first,
// because it decides what q does, then the phases and warnings.
func (o *overview) progressBody(w int) []string {
	t := o.st.theme

	body := []string{t.Accent.Render(t.SpinnerFrame(o.st.tick)) + " " + t.Body.Render("Building the index"), ""}
	body = append(body, ovStyled(t.Attention.Render,
		ui.Wrap("This build cannot be cancelled safely. Press q to exit after it finishes.", w))...)
	body = append(body, "")
	body = append(body, o.phaseList(w)...)

	return append(body, o.logLines(w)...)
}

// phaseList renders each phase with its state glyph. Only the scan and
// parse phases have real counts; the others show a state, never a
// percentage.
func (o *overview) phaseList(w int) []string {
	b := o.build
	rows := make([]phaseRow, len(ovPhases))

	for i, p := range ovPhases {
		note := ""

		switch {
		case p.id == "scan" && b.seen[i]:
			note = ovPlural(b.files, "file", "files")
		case p.id == "parse" && b.parseTotal > 0:
			note = ovNum(b.parseDone) + " / " + ovNum(b.parseTotal)
		}

		rows[i] = phaseRow{label: p.label, note: note, state: b.phases[i]}
	}

	return phaseRows(o.st.theme, rows, o.st.tick, w)
}

// logLines renders the recent warnings of the engine log under their
// heading, after a blank line. It returns nothing without warnings.
func (o *overview) logLines(w int) []string {
	t := o.st.theme
	b := o.build

	if len(b.logs) == 0 {
		return nil
	}

	lines := []string{"", t.Heading.Render("RECENT WARNINGS")}

	return append(lines, activityLines(t, b.logs, ovLogKeep, w)...)
}

// resultBody renders a finished build from the fields of its summary. A
// skipped build reports file counts of zero, so it shows only what the
// summary really knows.
func (o *overview) resultBody(w int) []string {
	t := o.st.theme
	b := o.build
	s := b.summary

	if s == nil {
		s = &index.Summary{}
	}

	after := ovStyled(t.Muted.Render, ui.Wrap("The view reloads the index, doctor, proposal and stats sections.", w))

	if s.Skipped {
		body := []string{t.Badge(ui.ToneSuccess, "Index already current"), ""}
		body = append(body, ovStyled(t.Body.Render, ui.Wrap("Nothing was rebuilt: the files match the last index run. "+
			"Press b, then f, to rebuild from scratch.", w))...)
		body = append(body, "")
		body = append(body, strings.Split(kv(t, [][2]string{
			{"Symbols", ovNum(s.Stats.Symbols)},
			{"Edges", ovNum(s.Stats.Edges)},
			{"Decisions", ovNum(s.Stats.Decisions)},
		}, w), "\n")...)

		return append(append(body, ""), after...)
	}

	history := "mined"
	if !s.HistoryMined {
		history = "not mined"
		if s.HistorySkipNote != "" {
			history += ": " + ui.OneLine(s.HistorySkipNote)
		}
	}

	parseErrs := "none"
	if s.ParseErrors > 0 {
		parseErrs = t.Attention.Render(ovNum(s.ParseErrors))
	}

	db := s.DBPath
	if db == "" {
		db = o.st.svc.DBPath()
	}

	sep := " " + t.Glyphs.Sep + " "

	facts := [][2]string{
		{"Files", ovNum(s.FilesSeen) + " seen" + sep + ovNum(s.FilesParsed) + " parsed" + sep +
			ovNum(s.FilesReparsed) + " reparsed" + sep + ovNum(s.FilesSkipped) + " skipped"},
		{"Parse errors", parseErrs},
	}

	// Deleted paths are not in the seen count and are not parse errors,
	// so the row names them apart, and only when they exist.
	if s.FilesDeleted > 0 {
		facts = append(facts, [2]string{"Deleted", ovPlural(s.FilesDeleted, "tracked file", "tracked files") + ", not staged"})
	}

	facts = append(facts, [][2]string{
		{"Symbols", ovNum(s.Stats.Symbols)},
		{"Edges", ovNum(s.Stats.Edges)},
		{"History", history},
		{"Decisions", ovNum(s.Stats.Decisions)},
		{"Database", ui.OneLine(db)},
	}...)

	body := []string{t.Badge(ui.ToneSuccess, "Index built"), ""}
	body = append(body, strings.Split(kv(t, facts, w), "\n")...)
	body = append(body, o.logLines(w)...)

	return append(append(body, ""), after...)
}

// failureBody renders a failed build with the full error. Nothing
// retries automatically.
func (o *overview) failureBody(w int) []string {
	t := o.st.theme

	body := []string{t.Badge(ui.ToneFailure, "Index build failed"), "", t.Heading.Render("ERROR")}
	body = append(body, ovStyled(t.Body.Render, ui.Wrap(ui.ErrText(o.build.err), w))...)
	body = append(body, "")
	body = append(body, o.phaseList(w)...)
	body = append(body, o.logLines(w)...)
	body = append(body, "")

	return append(body, ovStyled(t.Muted.Render, ui.Wrap("Refresh view (r) shows what changed; "+
		"nothing retries automatically.", w))...)
}
