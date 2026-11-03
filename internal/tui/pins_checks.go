package tui

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/maintain"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file holds the lesson-check parts of Lessons › Pins (contracts
// K1, K5, K10): the CHECK section of the pin detail and the
// verification view of V. The section joins two reads. The pin
// inspection gives what each pin requests and the predicate of its
// check (maintain.PinCheck). The capability report of the shared state
// gives what the installed hooks make of that request. The view
// computes no badge and runs no checker: the report and the
// verification come from services, as doctor, status, and lessons
// --verify get them.

// checkLabelW is the label column of the CHECK section. "Predicate" and
// a gap of two cells fit, so the label never touches its value.
const checkLabelW = 11

// pinsCheckShown reports whether Pins shows its lesson-check parts: the
// CHECK section and the keys M and V. Doctor and status print their
// lesson-check lines only when lessons.yaml uses a lesson-check key or a
// lesson-check or exploration hook is installed (contract K10). Pins
// follows the same rule, so a repository without lesson checks keeps
// every frame (P4.10 Preserve).
func pinsCheckShown(st *state, in *maintain.Inspection) bool {
	if in != nil && in.Checks.Uses {
		return true
	}

	return st.integrations.loaded &&
		slices.ContainsFunc(st.integrations.value, integration.Inspection.LessonHooksInstalled)
}

// checkShown reports pinsCheckShown for the last pin inspection.
func (p *pinsView) checkShown() bool {
	return pinsCheckShown(p.st, p.inspection())
}

// checkLines renders the CHECK section of one installed pin: the
// requested mode, the bound check and its predicate, the checker scope,
// the listing of an advisory pin, the errors of the entry, and the
// active state of each client. It returns nil while Pins hides its
// lesson-check parts.
func (p *pinsView) checkLines(in *maintain.Inspection, pin *maintain.Pin, w int) []string {
	if !p.checkShown() {
		return nil
	}

	t := p.st.theme
	c := pin.Check
	vw := max(1, w-checkLabelW)

	head := t.Heading.Render("CHECK")
	if c.Known {
		head += "  " + t.Faint.Render("pin-ref "+c.Ref)
	}

	out := []string{head}

	if !c.Known {
		return append(out, propNote(t, ui.ToneUnknown, "Unknown: this entry does not pair with a pin that "+
			"lessons.yaml delivers, so no lesson-check fact is shown.", w)...)
	}

	fact := func(label string, values []string) {
		out = append(out, propIndent(t.Muted.Render(ui.PadRight(label, checkLabelW)), values)...)
	}

	fact("Mode", []string{t.Body.Render(string(c.Mode))})

	if c.Check != nil {
		fact("Check", propLines(t.Body, checkRefText(*c.Check), vw))
		fact("Predicate", checkPredicateValues(t, c, vw))
	}

	fact("Scope", p.regionValues(c.Scope, vw))

	switch c.Listing {
	case lessoncheck.ListingNotCheckable:
		out = append(out, propLines(t.Muted, "not-checkable: an advisory pin without a check. No checker runs it; "+
			"its note is guidance only.", w)...)
	case lessoncheck.ListingAdvisoryWithCheck:
		out = append(out, propLines(t.Muted, "advisory-with-check: the check runs only after a change of the mode "+
			"to check or enforce:", w)...)
		out = append(out, integCommandLines(t, "seamark lessons --set-mode check --pin "+c.Ref, w)...)
	default:
		fact("Active", p.activeValues(in, c, vw))
	}

	for _, e := range c.Errors {
		out = append(out, propNote(t, ui.ToneAttention, ovSentenceEnd(ui.OneLine(e.Message)), w)...)
	}

	return out
}

// checkRefText names a check: "<id> (<kind>, <12 hex of the version>)",
// or "<id> (not defined)" for an ID that names no definition.
func checkRefText(c lessoncheck.CheckRef) string {
	if c.Version == "" {
		return ui.OneLine(c.ID) + " (not defined)"
	}

	return ui.OneLine(c.ID) + " (" + ui.OneLine(string(c.Kind)) + ", " + checkShortHex(c.Version) + ")"
}

// checkShortHex returns the first 12 characters of a digest, the length
// that the CLI prints (lessoncheck shortHex).
func checkShortHex(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}

	return digest
}

// checkPredicateValues renders the predicate of the bound check, or why
// no checker can run it.
func checkPredicateValues(t *ui.Theme, c maintain.PinCheck, w int) []string {
	switch {
	case c.Predicate != "":
		return propLines(t.Body, ui.OneLine(c.Predicate), w)
	case c.Check.Version == "":
		return propLines(t.Attention, "not available: the check is not defined under checks", w)
	}

	return propLines(t.Attention, "not available: no checker reads this kind", w)
}

// activeValues renders the active state of an executable pin: one line
// per client with its badge and reasons, from the capability report.
// The report must hold the bytes that the inspection listed. Else the
// state is unknown until the next report, which a load of the pins
// starts (followChecks).
func (p *pinsView) activeValues(in *maintain.Inspection, c maintain.PinCheck, w int) []string {
	t := p.st.theme
	sec := &p.st.capability

	unknown := func(text string) []string { return propLines(t.Unknown, "unknown: "+text, w) }

	switch {
	case !sec.loaded:
		return unknown("the coverage of the installed hooks is not read yet")
	case sec.value.Config.SHA256 != in.Checks.SHA256:
		return unknown("lessons.yaml changed after the coverage read; it reads again")
	}

	pc, ok := sec.value.Pin(c.Ref)
	switch {
	case !ok:
		return unknown("the coverage report does not list this pin")
	case len(pc.Clients) == 0:
		return propLines(t.Attention, sec.value.ActiveText(c.Ref), w)
	}

	var out []string

	for _, client := range pc.Clients {
		text := ui.OneLine(client.Client) + " " + client.Badge
		if note := client.Note(); note != "" {
			text += " (" + ui.OneLine(note) + ")"
		}

		style := t.Body
		if client.Badge == lessoncheck.BadgeRequestedInactive {
			style = t.Attention
		}

		out = append(out, propLines(style, text, w)...)
	}

	return out
}

// checkRoute gives msg to the open check-definition form or to the open
// verification view. It reports done when the sub-view must not handle
// msg as well: a plan, a commit, a run, or a freshness read of these
// views, and a key while one of them is open. A load of the pins and a
// save of lessons.yaml also reach the rest of the sub-view.
func (p *pinsView) checkRoute(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case checkDefPlannedMsg:
		// A closed form is detached, so its late plan changes nothing.
		if p.checkDef != nil && p.checkDef == m.m {
			m.m.readDone(m.m.seq.current(m.seq), m.plan, m.err, "the review came back empty")
		}

		return nil, true
	case checkDefCommittedMsg:
		return p.checkDefCommitted(m), true
	case pinVerifiedMsg:
		if p.verify != nil && p.verify == m.v {
			m.v.verified(m)
		}

		return nil, true
	case pinFreshMsg:
		if p.verify != nil && p.verify == m.v {
			m.v.freshDone(m)
		}

		return nil, true
	case lessonsSavedMsg:
		if p.checkDef != nil {
			return p.checkDef.lessonsSaved(m), false
		}
	case loadedMsg:
		// Every refresh loads the pins again. The shown result then asks
		// the engine whether its binding still holds (contract K5).
		if p.verify != nil && m.kind == secPins && m.err == nil {
			return p.verify.revalidate(), false
		}
	case tea.KeyMsg:
		return p.checkKey(m)
	}

	return nil, false
}

// checkKey gives a key to the open form or view, and drops the one that
// closes.
func (p *pinsView) checkKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case p.checkDef != nil:
		cmd, closed := p.checkDef.key(msg)
		if closed {
			p.checkDef.seq.cancel()
			p.checkDef = nil
		}

		return cmd, true
	case p.verify != nil:
		cmd, closed := p.verify.key(msg)
		if closed {
			p.verify = nil
		}

		return cmd, true
	}

	return nil, false
}

// pinVerify is the verification view of V. It runs the verification of
// `seamark lessons --verify` on the default selection through services
// (contract K5), and shows the text of the shared renderer. The view
// holds one result. A refresh asks the engine whether the binding of
// that result still holds (lessoncheck.Revalidate), and a changed input
// marks the result stale. Only V runs the checkers again.
type pinVerify struct {
	st *state
	// seq issues the tickets of the runs, and rseq the tickets of the
	// freshness reads. A result of an old ticket never replaces a newer
	// state.
	seq, rseq generation
	// running is true while a verification runs; stop cancels it.
	running bool
	stop    context.CancelFunc
	// res is the shown result, or nil; err is the failure of the run.
	res *lessoncheck.Verification
	err error
	// stale is true after a freshness read found a changed input.
	// freshErr is the failure of the last freshness read.
	stale    bool
	freshErr error

	scroll ui.ScrollState
}

// pinVerifiedMsg carries the result of one verification run.
type pinVerifiedMsg struct {
	v   *pinVerify
	seq int
	res *lessoncheck.Verification
	err error
}

// pinFreshMsg carries the result of one freshness read.
type pinFreshMsg struct {
	v     *pinVerify
	seq   int
	fresh lessoncheck.Freshness
	err   error
}

// verifyKeys are the keys of the verification view.
var verifyKeys = struct {
	Again, Fresh key.Binding
}{
	Again: key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "verify again")),
	Fresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "check freshness")),
}

// newPinVerify opens the view and starts the first run.
func newPinVerify(st *state) (*pinVerify, tea.Cmd) {
	v := &pinVerify{st: st}
	cmd := v.start()

	return v, cmd
}

// start runs the verification as registered work, so an exit waits for
// it. The run appends one record to the lesson-check audit stream, as
// lessons --verify does (contract K9). It takes no mutation slot: the
// engine takes no lock for it, so it can run beside another operation.
func (v *pinVerify) start() tea.Cmd {
	seq := v.seq.start()
	v.rseq.cancel()
	v.running, v.res, v.err, v.stale, v.freshErr = true, nil, nil, false, nil
	v.scroll = ui.ScrollState{}

	ctx, cancel := v.st.runContext()
	v.stop = cancel

	var svc services.Checks = v.st.svc

	return v.st.goWork(func() tea.Msg {
		defer cancel()

		res, err := svc.VerifyLessons(ctx, nil)

		return pinVerifiedMsg{v: v, seq: seq, res: res, err: err}
	})
}

// verified shows the result of the current run.
func (v *pinVerify) verified(msg pinVerifiedMsg) {
	if !v.seq.current(msg.seq) {
		return
	}

	v.running, v.res, v.err = false, msg.res, msg.err
	if v.res == nil && v.err == nil {
		v.err = errVerifyEmpty
	}
}

// errVerifyEmpty is the failure of a run that returned neither a result
// nor an error.
var errVerifyEmpty = errors.New("the verification came back empty")

// revalidate starts a freshness read of the shown result. It reads the
// working tree and lessons.yaml again and runs no checker. A result
// that is already stale needs no read.
func (v *pinVerify) revalidate() tea.Cmd {
	if v.running || v.res == nil || v.stale {
		return nil
	}

	seq := v.rseq.start()
	res := v.res

	var svc services.Checks = v.st.svc

	return func() tea.Msg {
		fresh, err := svc.RevalidateLessons(context.Background(), res)

		return pinFreshMsg{v: v, seq: seq, fresh: fresh, err: err}
	}
}

// freshDone applies the freshness read of the current ticket. A failed
// read proves nothing, so the result keeps its state and the view names
// the failure.
func (v *pinVerify) freshDone(msg pinFreshMsg) {
	if !v.rseq.current(msg.seq) {
		return
	}

	v.freshErr = msg.err
	if msg.err == nil && msg.fresh == lessoncheck.FreshnessStale {
		v.stale = true
	}
}

// close stops a running verification and drops every late result.
func (v *pinVerify) close() {
	if v.stop != nil {
		v.stop()
	}

	v.seq.cancel()
	v.rseq.cancel()
}

// key handles a key of the view. It reports whether the view closes.
func (v *pinVerify) key(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch {
	case key.Matches(msg, ui.Nav.Back):
		v.close()

		return nil, true
	case v.running:
		// The run reads the working tree; only esc leaves it.
	case key.Matches(msg, verifyKeys.Again):
		return v.start(), false
	case key.Matches(msg, verifyKeys.Fresh):
		return v.revalidate(), false
	default:
		v.scroll.Key(msg)
	}

	return nil, false
}

// keys returns the footer keys of the view.
func (v *pinVerify) keys() []key.Binding {
	back := ui.WithHelp(ui.Nav.Back, "esc", "close")
	if v.running {
		return []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	}

	fresh := verifyKeys.Fresh
	fresh.SetEnabled(v.res != nil && !v.stale)

	return []key.Binding{verifyKeys.Again, fresh, back, ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")}
}

// content builds the modal of the view in w cells.
func (v *pinVerify) content(w int) ui.ModalOpts {
	t := v.st.theme

	if v.running {
		return flowWaitModal(v.st, "Verifying the lesson checks", "Seamark reads the working tree and runs the "+
			"checker of every check and enforce pin, as seamark lessons --verify does. It appends one record to "+
			"the lesson-check audit stream and changes no other file.", true, w)
	}

	if v.err != nil {
		body := propNote(t, ui.ToneFailure, "The verification could not run: "+ui.ErrText(v.err), w)
		body = append(body, "")
		body = append(body, propLines(t.Body, "Fix the cause, then press V to run it again.", w)...)

		return ui.ModalOpts{Title: t.Failure.Render("Verification failed"), Body: body, Actions: v.keys()}
	}

	var body []string

	switch {
	case v.stale:
		body = append(body, propNote(t, ui.ToneAttention, "Stale: the working tree, lessons.yaml, or a check "+
			"changed after this verification. Its results no longer hold; press V to run it again.", w)...)
		body = append(body, "")
	case v.freshErr != nil:
		body = append(body, propNote(t, ui.ToneUnknown, "The freshness of this result is unknown: "+
			ui.ErrText(v.freshErr), w)...)
		body = append(body, "")
	}

	body = append(body, verifyTextLines(t, v.res, w)...)

	return ui.ModalOpts{
		Title: t.Title.Render("Lesson checks"), Right: verifyBadge(t, v.res, v.stale), Body: body,
		Actions: v.keys(),
	}
}

// verifyTextLines renders a result with the renderer of lessons --verify
// (lessoncheck.RenderText). One renderer gives the CLI, MCP, and the
// console one text (contract K5). The renderer sanitizes every text from
// the repository; the console sanitizes each line again, because a box
// must never draw a control byte.
func verifyTextLines(t *ui.Theme, res *lessoncheck.Verification, w int) []string {
	var b bytes.Buffer

	lessoncheck.RenderText(&b, res)

	var out []string

	for line := range strings.SplitSeq(strings.TrimRight(b.String(), "\n"), "\n") {
		out = append(out, hangLines(t.Body, line, w)...)
	}

	return out
}

// hangLines wraps one line of an engine text, such as an impact block or
// the verification report. The line keeps its leading spaces, and each
// wrapped part goes two cells deeper, so it never reads as a line of
// its own. The text is sanitized, because it holds file data.
func hangLines(style lipgloss.Style, line string, w int) []string {
	text := strings.TrimLeft(line, " ")
	indent := strings.Repeat(" ", len(line)-len(text))
	lines := propLines(style, ui.OneLine(text), max(1, w-len(indent)-2))

	for i := range lines {
		pad := indent
		if i > 0 {
			pad += "  "
		}

		lines[i] = pad + lines[i]
	}

	return lines
}

// verifyBadge labels the summary state of a result in the modal border.
// A stale result never shows the tone of its old state.
func verifyBadge(t *ui.Theme, res *lessoncheck.Verification, stale bool) string {
	state := string(res.Summary.State)

	switch {
	case stale:
		return t.Badge(ui.ToneAttention, state+", stale")
	case res.Summary.State == lessoncheck.SummaryPassed:
		return t.Badge(ui.ToneSuccess, state)
	case res.Summary.State == lessoncheck.SummaryViolation:
		return t.Badge(ui.ToneFailure, state)
	}

	return t.Badge(ui.ToneAttention, state)
}
