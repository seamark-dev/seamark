package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/redact"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the parts that the two agent run records share. An
// agent run record is a paid run of the configured agent in Lessons ›
// Run: a distillation (distill.go) or a trigger extraction
// (extract.go). Both records read a preview, start only on y, run
// under the mutation slot, and can stop. The shared parts are the steps,
// the Preview step state, the run state and the messages. They also
// include the worker, the keys, the verdict shape, the unit counts, the
// activity log and the stop question. Both records render the agent,
// the clock, the token counts, the failures and the stops with the same
// helpers, so one fact reads the same in both.
//
// The records stay two types with their own methods. The methods differ
// in the words of each workflow and in fields of the engine types, and
// Go generics cannot read a field of a type parameter. A shared core
// would need about 25 hooks, and a measure showed that it removes fewer
// than 400 more lines. The control flow core → hook → core is also
// harder to follow than two parallel files.
//
// The evidence refresh record (evidence_run.go) is a different
// machine without a Preview step or units. It shares only the stop
// question, the state label and the activity log.

// runStep is the step of an agent run record. A record moves forward
// only on an explicit key, and the paid start needs y. The steps use the
// preview words of the console: Preparing reads the preview, Preview
// shows it, Running runs it, and Result shows the outcome. Only a
// distillation has the Scope step, because the selection of the user
// is the scope of an extraction.
type runStep int

const (
	// stepScope is the draft form of a distillation: the region and the
	// group limit.
	stepScope runStep = iota
	// stepPreparing waits for the read-only plan. The read writes
	// nothing.
	stepPreparing
	// stepPreview shows the plan, its failure, or why nothing can be
	// sent.
	stepPreview
	// stepRunning runs the reviewed plan.
	stepRunning
	// stepResult shows the outcome of the run.
	stepResult
)

// runTrailSteps names the step trail at the right of the context line.
// The trail shows the Preparing and the Preview steps as one position,
// "Review", because the user reviews the plan there. The words are part
// of every capture, so they stay as they are.
var runTrailSteps = []string{"Scope", "Review", "Run", "Result"}

// runTrail returns the step trail of a record. A record without the
// Scope step leaves out "Scope".
func runTrail(scope bool) []string {
	if scope {
		return runTrailSteps
	}

	return runTrailSteps[1:]
}

// stepper returns the trail position of the step in runTrail(scope).
// Without the Scope step every position moves one to the left.
func (s runStep) stepper(scope bool) int {
	pos := 3

	switch s {
	case stepScope:
		pos = 0
	case stepPreparing, stepPreview:
		pos = 1
	case stepRunning:
		pos = 2
	}

	if scope {
		return pos
	}

	return max(0, pos-1)
}

// agentActivityKeep bounds the recent activity lines of an agent run
// record, as the index build does, so the run detail never grows with
// the run.
const agentActivityKeep = 4

// agentWarnTTL is how long the shell shows an attention message
// (app.go). A clear after that time would remove a newer message of
// another workflow.
const agentWarnTTL = 15 * time.Second

// agentFactsMin is the narrowest context facts next to the compact step
// trail. The facts give way to the trail down to this width, so every
// supported terminal shows the step (design map §4).
const agentFactsMin = 20

// agentPlanW is the width of the Plan pane of a review at the 120-column
// design size. It is wider than the Setup plan, because the notes of a
// paid run are longer and must show without scrolling where possible.
const agentPlanW = 50

// agentGlyphW is the width of the glyph column in the unit rows of a run
// and a result.
const agentGlyphW = 3

// agentKeys are the letter keys that both agent run records bind. The
// records keep the letters of their own workflow in distKeys and
// extKeys. The switch keys of Lessons (p, m, s, e, u) stay free,
// because Lessons switches its sub-views with them.
var agentKeys = struct {
	Start, Stop, Link key.Binding
}{
	// Start starts a ready preview. It also confirms the stop question,
	// because both answers take y.
	Start: key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "start run")),
	Stop:  key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
	// Link opens the place that fixes a blocker, for example Settings ›
	// Inference. It only navigates; the fix stays a separate action.
	Link: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open")),
}

// agentPreview is the state of the Preview step of an agent run record:
// the result of the read-only plan, and the cursor and scroll positions
// of its panes. P is the preview type of the workflow:
// learning.DistillPreview or learning.ExtractPreview. The records keep
// it in the field prev, as Evidence keeps its preview. So "plan" names
// only the read value, never this state.
type agentPreview[P any] struct {
	// seq drops the result of a plan that the record left.
	seq generation

	preview *P
	err     error
	// stale is the refusal of a run that did not start, or a settings
	// save after the preview. The preview is then out of date, and only
	// a new plan helps.
	stale error
	// cwd is the process directory at the time of the plan. An agent
	// command without its own directory runs there. Only a distillation
	// reads it, because an extraction preview names its own directory.
	cwd string
	// preset is true when the command is a registered preset, not a
	// custom agent.argv.
	preset bool

	// cursor is the planned row under the cursor: a group of a
	// distillation, or an item of an extraction in send order.
	cursor int
	follow bool
	list   ui.ScrollState
	notes  ui.ScrollState
	// planFocus gives the keys to the Plan pane in the wide layout;
	// listOpen shows the list pane (Groups or Proposals) in the compact
	// layout.
	planFocus bool
	listOpen  bool
}

// agentProgress is the progress and the outcome of one agent run. X is
// the execution type of the workflow, and U is the outcome type of one
// unit. A unit is one agent call: a distillation group or an extraction
// batch.
type agentProgress[X, U any] struct {
	started time.Time

	// units are parallel to the planned units. Each unit starts as
	// unattempted and changes with its finish event.
	units []U
	// finished counts the units with a finish event.
	finished int
	// sending is the unit whose agent call runs; -1 when none.
	sending int
	// checked reports that the start checks passed: the first event of
	// the cleanup or of a unit proves it.
	checked bool
	// stopped reports a stop event of the engine, and stopWhy is its
	// reason. The event carries no other fact that the record reads.
	stopped bool
	stopWhy distill.StopReason

	activity []string

	stopAsk  bool
	stopping bool
	// stopAt is the unit that ran when the user asked to stop; -1 when
	// no unit ran.
	stopAt int

	exec  *X
	err   error
	ended time.Time
	// internal reports a run that ended on an internal error after its
	// work began. The engine sent no final outcome, so the result comes
	// from the events that arrived.
	internal bool
	// lost is the unit whose agent call ran when the internal error
	// ended the run; -1 when none ran. Its result is not known.
	lost int

	// cursor is the run row under the cursor: a group of a
	// distillation, or an item of an extraction in send order.
	cursor int
	follow bool
	// moved is true after the user moved the cursor. Until then the
	// cursor follows the unit that runs, and the result puts it on the
	// first row that needs attention. The result clears it, so the
	// compact result opens at its top and follows the cursor only after
	// a move.
	moved    bool
	list     ui.ScrollState
	summary  ui.ScrollState
	sumFocus bool
	// detail shows the list pane in the compact layout.
	detail bool
}

// idleAgentProgress returns the progress of a record that has not
// started a run. The unit fields hold -1, because 0 names the first
// unit. Both record constructors and newAgentProgress use it, so a new
// sentinel field needs one change only.
func idleAgentProgress[X, U any]() agentProgress[X, U] {
	return agentProgress[X, U]{sending: -1, stopAt: -1, lost: -1}
}

// newAgentProgress returns the progress of a run that starts now with
// units, every one of them unattempted.
func newAgentProgress[X, U any](units []U) agentProgress[X, U] {
	r := idleAgentProgress[X, U]()
	r.started, r.units, r.follow = time.Now(), units, true

	return r
}

// began reports that the run passed its start checks: an event of the
// cleanup, of a unit, or of a stop arrived. The cleanup event of a
// distillation sets checked, so it needs no term of its own.
func (r *agentProgress[X, U]) began() bool {
	return r.checked || r.finished > 0 || r.stopped
}

// log appends one activity line with its time and keeps the newest
// agentActivityKeep lines.
func (r *agentProgress[X, U]) log(at time.Time, line string) {
	r.activity = appendActivity(r.activity, at, line, agentActivityKeep)
}

// agentPlannedMsg carries a plan result of an agent run record. seq
// drops the result of a plan that the record left.
type agentPlannedMsg[P any] struct {
	rec     runRecord
	seq     int
	preview *P
	err     error
	cwd     string
	preset  bool
}

// agentEventMsg carries one typed progress event E of a run. ch lets
// the receiver wait for the next message.
type agentEventMsg[E any] struct {
	rec runRecord
	ch  <-chan tea.Msg
	ev  E
	at  time.Time
}

// agentDoneMsg carries the end of a run. exec is nil when the service
// refused the preview before any work, or when the run panicked.
// panicked tells the two apart, because a panic can come after paid
// agent calls and saved results.
type agentDoneMsg[X any] struct {
	rec      runRecord
	exec     *X
	err      error
	panicked bool
	at       time.Time
}

// agentQueue returns the event queue of a run of n units. The queue
// holds every event that must arrive. These are a start and a finish per
// unit, the cleanup of a distillation, and the stop: 2n+2 events. The
// worker adds one end message. The capacity 2n+4 holds these 2n+3
// messages and one spare slot. So the worker never blocks while nobody
// reads, as in the synchronous test harness. A distillation also sends
// one duplicate event per dropped restatement, and n does not bound
// them. So distForward drops a duplicate event when the queue is full.
func agentQueue(n int) chan tea.Msg {
	return make(chan tea.Msg, 2*n+4)
}

// agentWork runs one paid run in a command goroutine, never in Update.
// run calls the service and sends the progress events through ch with
// the forwarder of its workflow. agentWork then sends the end and
// closes ch. A panic becomes a failed end, so the mutation slot always
// frees. The end marks the panic, because paid work can come before it.
// agentWork reads no field of rec, so a bare record works in a test.
func agentWork[X any](rec runRecord, ch chan<- tea.Msg, run func() (*X, error)) {
	defer close(ch)

	var (
		x        *X
		err      error
		panicked bool
	)

	func() {
		defer func() {
			if v := recover(); v != nil {
				x, panicked = nil, true
				err = fmt.Errorf("panic: %v", v)
			}
		}()

		x, err = run()
	}()

	ch <- agentDoneMsg[X]{rec: rec, exec: x, err: err, panicked: panicked, at: time.Now()}
}

// unitCounts counts the units of a run by state. unknown counts the unit
// whose agent call started before an internal error ended the run. Its
// result is not known, so it never counts as not attempted. The engine
// skips a group that another writer recorded; it never skips a batch.
type unitCounts struct {
	completed, failed, cancelled, skipped, unattempted, unknown int
}

// parts renders the counts for the "What happened" row in a fixed
// order. The completed count always shows, with the word done, for
// example "3 saved". Every other count shows only when it is not 0.
// The caller joins the parts with its own separator.
func (c unitCounts) parts(done string) []string {
	parts := []string{strconv.Itoa(c.completed) + " " + done}

	for _, p := range []struct {
		n    int
		word string
	}{
		{c.failed, " failed"}, {c.cancelled, " cancelled"}, {c.unknown, " result unknown"}, {c.skipped, " skipped"},
		{c.unattempted, " not attempted"},
	} {
		if p.n > 0 {
			parts = append(parts, strconv.Itoa(p.n)+p.word)
		}
	}

	return parts
}

// agentVerdict is the classification of a finished agent run: the tone
// and the word of the box label, the headline, the lead text and the
// toast. A run with a failed, cancelled or unattempted unit never reads
// as a success (spec R-8). Each workflow writes its own words.
type agentVerdict struct {
	tone  ui.Tone
	word  string
	head  string
	lead  string
	toast string
}

// agentWait returns a command that delivers the next run message. It
// returns nil after the last one, so the chain ends by itself.
func agentWait(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}

		return msg
	}
}

// cursorKey moves a cursor over n rows with the arrow keys and reports
// whether the key was one of them.
func cursorKey(msg tea.KeyMsg, cursor *int, n int) bool {
	switch {
	case key.Matches(msg, ui.Nav.Up):
		*cursor = max(0, *cursor-1)
	case key.Matches(msg, ui.Nav.Down):
		*cursor = max(0, min(n-1, *cursor+1))
	default:
		return false
	}

	return true
}

// countSep joins two counts in one line, for example "3 proposals ·
// ~344 tokens". The ASCII set uses a comma, because its separator "/"
// makes two counts read as a fraction.
func countSep(t *ui.Theme) string {
	if t.ASCII {
		return ", "
	}

	return " " + t.Glyphs.Sep + " "
}

// agentTokens renders an estimated token count of chars characters with
// the engine's own estimate, so the console and the CLI agree.
func agentTokens(chars int) string {
	return "~" + distill.Preflight{PromptChars: chars}.Tokens()
}

// agentFailWords names a failure kind for a reader. A failed group and a
// failed batch carry the same kinds.
func agentFailWords(k distill.FailureKind) string {
	switch k {
	case distill.FailAgent:
		return "agent error"
	case distill.FailTimeout:
		return "timeout"
	case distill.FailInvalidReply:
		return "invalid reply"
	case distill.FailStore:
		return "store error"
	default:
		return k.String()
	}
}

// agentStopWords names why a run ended before its last unit.
func agentStopWords(r distill.StopReason) string {
	switch r {
	case distill.StopCancelled:
		return "stopped by you"
	case distill.StopConflict:
		return "an input changed"
	case distill.StopStoreError:
		return "store error"
	default:
		return r.String()
	}
}

// agentClock renders a time of day for the facts and the activity lines.
func agentClock(at time.Time) string {
	return at.Format("15:04:05")
}

// agentKind names where the agent command comes from: a registered
// preset by its name, or the custom agent.argv of the configuration.
func agentKind(name string, custom bool) string {
	if custom {
		return "custom agent.argv"
	}

	return "preset " + ui.OneLine(name)
}

// agentText renders the agent as "<argv> · <kind>" (design map §4 and
// §5). Every step that names the agent uses it, so one agent reads the
// same in both records. room > 0 cuts a long command line before the
// kind, so the kind always shows; room <= 0 leaves the cut to the caller.
func agentText(t *ui.Theme, argv []string, kind string, room int) string {
	tail := " " + t.Glyphs.Sep + " " + kind
	cmd := agentArgv(argv)

	if room > 0 && ui.Width(cmd)+ui.Width(tail) > room {
		cmd = ui.Truncate(cmd, max(1, room-ui.Width(tail)), t.Glyphs.Ellipsis)
	}

	return t.Body.Render(cmd) + t.Muted.Render(tail)
}

// agentArgv renders an agent command line for display. Arguments can
// hold tokens, so the helper redacts them before it sanitizes the text.
// A secret-shaped value that holds shell syntax stays visible. It is
// code that runs after y, and "[REDACTED]" would hide it behind a word
// that reads as a safety measure.
func agentArgv(argv []string) string {
	return ui.OneLine(redact.SecretsExcept(strings.Join(argv, " "), shellActive))
}

// shellActive reports text that a shell runs or redirects: a command
// substitution, a backquote, a command separator, a pipe, a redirection
// or a line break. A password that holds such a character then shows
// too; the trade-off favors the code that runs.
func shellActive(s string) bool {
	return strings.Contains(s, "$(") || strings.ContainsAny(s, "`;|&<>\n\r")
}

// isPreset reports a registered preset command, not a custom
// agent.argv. It reads the preset list, so only a plan command calls
// it, never View or Update.
func isPreset(name string) bool {
	return slices.Contains(integration.Builtin().InvocationIDs(), name)
}

// appendActivity appends one activity line with its time and keeps the
// last keep lines. The lines hold typed facts only, never agent output,
// so a raw reply never reaches the screen.
func appendActivity(lines []string, at time.Time, text string, keep int) []string {
	lines = append(lines, agentClock(at)+"  "+text)

	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}

	return lines
}

// runStateLabel renders the state of a running record at the right of
// its box border: "stopping…" after a stop request, else the spinner
// and the elapsed time. The caller shows the verdict badge of a result
// itself, because only it knows when its result exists.
func runStateLabel(t *ui.Theme, tick int, stopping bool, started time.Time) string {
	spin := t.SpinnerFrame(tick)

	if stopping {
		return t.Attention.Render(spin + " stopping" + t.Glyphs.Ellipsis)
	}

	return t.Accent.Render(spin) + t.Muted.Render(" running "+t.Glyphs.Sep+" "+runElapsed(time.Since(started)))
}

// runStopModal renders the stop question of a running record: title,
// the sentences of text, and the two answers. stop is the y answer
// with its words, for example "stop the run". It is at most 64 cells
// wide, like the quit question (design map §6).
func runStopModal(t *ui.Theme, title, text string, stop key.Binding, w, h int) string {
	area := min(w, 68)

	return t.Modal(ui.ModalOpts{
		Title:   t.Title.Render(title),
		Body:    integLines(t.Body, text, ui.ModalBodyWidth(64, area)),
		Actions: []key.Binding{stop, ui.WithHelp(ui.Nav.Back, "esc", "keep running")},
		MinW:    64,
	}, nil, area, h)
}
