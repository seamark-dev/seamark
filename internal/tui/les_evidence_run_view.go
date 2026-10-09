package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file renders the evidence run record: the stop question, the
// verdict of a result, its sentences and guidance, and the boxes of
// Lessons › Run. The state and the keys of the record live in
// les_evidence_run.go.

// modal implements runRecord: the stop question, at most 64 cells wide
// like the quit question.
func (r *evRun) modal(w, h int) (string, bool) {
	if !r.confirm {
		return "", false
	}

	text := "Seamark stops the GitHub fetch. Stored reviews stay as they were; a partial fetch is never " +
		"saved. A local fix phase that is running finishes and saves first. Nothing is retried."
	if !r.reviewMode() {
		text = "The local fix mine cannot stop part way. Seamark lets it finish and save, then ends the " +
			"run. Nothing is retried."
	}

	return runStopModal(r.st.theme, "Stop the evidence refresh?", text, withHelp(lesKeys.Confirm, "y", "stop the refresh"),
		w, h), true
}

// evVerdict is the summary of a finished run: the tone, the right label
// of the box and the headline.
type evVerdict struct {
	tone  Tone
	right string
	head  string
}

// verdict summarizes the result. A partial result never reads as a
// success, and failed differs from stopped by its word.
func (r *evRun) verdict() evVerdict {
	sources := r.sources()
	ok := r.succeeded()

	has := func(state index.SourceState) bool {
		for _, o := range sources {
			if o.State == state {
				return true
			}
		}

		return false
	}

	switch {
	case r.conflict() != nil:
		return evVerdict{tone: ToneAttention, right: "out of date", head: "The refresh preview is out of date"}
	case ok == len(sources):
		return evVerdict{tone: ToneSuccess, right: "refreshed", head: "Evidence refreshed"}
	case ok > 0:
		word := "a source that was not attempted"

		switch {
		case has(index.SourceFailed):
			word = "a failed source"
		case has(index.SourceStopped):
			word = "a stopped source"
		case has(index.SourceUnavailable):
			word = "an unavailable source"
		}

		return evVerdict{tone: ToneAttention, right: "partial", head: "Evidence refresh finished with " + word}
	case r.unexplained():
		// Only the error explains this result, for example a panic of the
		// worker. It is a failure, not a refresh that never started.
		return evVerdict{tone: ToneFailure, right: "failed", head: "Evidence refresh failed"}
	case has(index.SourceStopped):
		return evVerdict{tone: ToneAttention, right: "stopped", head: "Evidence refresh stopped"}
	case has(index.SourceFailed):
		return evVerdict{tone: ToneFailure, right: "failed", head: "Evidence refresh failed"}
	case has(index.SourceUnavailable):
		return evVerdict{tone: ToneAttention, right: "unavailable", head: "No evidence source could run"}
	default:
		return evVerdict{tone: ToneAttention, right: "not started", head: "Evidence refresh did not start"}
	}
}

// toast returns the message of the finished run. A partial result uses
// the attention tone, never the success glyph.
func (r *evRun) toast() tea.Cmd {
	v := r.verdict()
	if v.tone != ToneSuccess {
		return toast(v.tone, v.head+". Lessons "+r.st.theme.Glyphs.Arrow+" Run shows the result.")
	}

	parts := []string{evNameFixes + " " + propPlural(r.out.Fixes.Stored, "finding", "findings")}
	if r.reviewMode() {
		parts = append(parts, evNameReviews+" "+propPlural(r.out.Reviews.Stored, "finding", "findings"))
	}

	return toast(ToneSuccess, "Evidence refreshed: "+strings.Join(parts, ", ")+".")
}

// kept returns the stored count phrase of a source that kept its
// corpus, and the verb after it. one is the verb for a count of 1, and
// many is the verb for other counts. The noun and the verb then agree
// with the count. It uses the count after the run when the store read
// it. Else it uses the count of the preview, else no number.
func (r *evRun) kept(source, one, many string) string {
	pick := func(s learning.Snapshot) int {
		if source == index.SourceReviews {
			return s.ReviewFindings
		}

		return s.FixFindings
	}

	noun, nouns := "stored local fix finding", "stored local fix findings"
	if source == index.SourceReviews {
		noun, nouns = "stored review finding", "stored review findings"
	}

	counted := func(n int) string {
		return "the " + propPlural(n, noun+" "+one, nouns+" "+many)
	}

	switch {
	case r.out.After.CountsErr == nil && r.out.After.Probe.State == workspace.IndexReady:
		return counted(pick(r.out.After))
	case r.plan.Current.CountsErr == nil:
		return counted(pick(r.plan.Current))
	default:
		// No count is known, so the plural noun and verb apply.
		return "the " + nouns + " " + many
	}
}

// sentence explains what one source did to its stored data.
func (r *evRun) sentence(source string, o index.SourceOutcome) string {
	cause := evCauseWord(o.Cause)
	if cause != "" {
		cause = " (" + cause + ")"
	}

	// The kept phrases include the verb, so the verb agrees with a count
	// of 1.
	stay := r.kept(source, "stays", "stay") + "."
	stayAsItWas := r.kept(source, "stays as it was", "stay as they were") + "."

	if source == index.SourceReviews {
		switch o.State {
		case index.SourceRefreshed:
			return "GitHub reviews were refreshed: " + propPlural(o.Stored, "finding", "findings") + " and " +
				propPlural(o.Lessons, "lesson", "lessons") + " are stored."
		case index.SourceEmpty:
			return "GitHub reviews were refreshed with no review lesson, so the stored reviews were replaced."
		case index.SourceUnavailable:
			return "GitHub reviews were unavailable" + cause + ", so " + stay
		case index.SourceFailed:
			return "The GitHub fetch failed" + cause + ", so " + stayAsItWas
		case index.SourceStopped:
			return "The GitHub fetch stopped before the write, so " + stayAsItWas + " A partial fetch is never saved."
		default:
			return "GitHub reviews were not attempted, so " + stay
		}
	}

	switch o.State {
	case index.SourceRefreshed:
		return "Local fixes were refreshed: " + propPlural(o.Stored, "finding is", "findings are") + " stored."
	case index.SourceEmpty:
		return "Local fixes were refreshed, and no fix commit was found, so no local fix finding is stored."
	case index.SourceUnavailable:
		return "Local fixes were not mined" + cause + ", so " + stay
	case index.SourceFailed:
		return "The local fix refresh failed" + cause + ", so " + stay
	case index.SourceStopped:
		return "The stop came before the local fix write, so " + stay
	default:
		return "Local fixes were not attempted, so " + stay
	}
}

// lead explains the result in a few sentences.
func (r *evRun) lead() string {
	c := r.conflict()
	if c == nil {
		return r.sourceSentences()
	}

	// The error starts with the ErrConflict text; the lead says that
	// already, so only the named changes follow.
	reason := strings.TrimPrefix(errText(c), learning.ErrConflict.Error()+": ")
	text := "The workspace changed after the preview: " + reason + "."

	// A conflict before a write can follow work: the fix source can save
	// first, and the GitHub fetch can run before the review write.
	switch {
	case r.succeeded() > 0:
		return text + " " + r.sourceSentences()
	case r.fetchRan():
		return text + " The review comments were fetched but not saved. Nothing was written."
	default:
		return text + " Nothing was fetched or written."
	}
}

// sourceSentences explains what each requested source did to its
// stored data, in the engine order.
func (r *evRun) sourceSentences() string {
	parts := []string{r.sentence(index.SourceFixes, r.out.Fixes)}
	if r.reviewMode() {
		parts = append(parts, r.sentence(index.SourceReviews, r.out.Reviews))
	}

	return strings.Join(parts, " ")
}

// fetchRan reports a GitHub fetch that started in this run. The events
// show it. The outcome shows it too, when the queue dropped the events:
// the engine refuses the review write only after a fetch.
func (r *evRun) fetchRan() bool {
	o := r.out.Reviews
	refusedWrite := r.reviewMode() && o.State == index.SourceFailed && o.Cause == index.CauseRefused

	return r.seen[evPhFetch] || r.fetched || r.out.Comments > 0 || refusedWrite
}

// unexplained reports an error result that no source reason explains,
// for example a panic of the worker or a root that did not resolve. The
// record then shows the error, because nothing else names the cause.
func (r *evRun) unexplained() bool {
	if r.running || r.err == nil || r.conflict() != nil {
		return false
	}

	for _, o := range r.sources() {
		if o.Reason != "" {
			return false
		}
	}

	return true
}

// evGuidance returns the next step for a source that did not refresh,
// and a command to run in a shell when one helps. The console never runs
// the command and never logs in.
func evGuidance(o index.SourceOutcome) (text, command string) {
	switch o.Cause {
	case index.CauseAuth:
		return "Run this command in a shell, then refresh again. Seamark never logs in for you.", "gh auth login"
	case index.CauseRateLimited:
		return "GitHub limits the API requests of this login. Wait until the limit resets, then refresh again. " +
			"This command shows the limits and their reset times.", "gh api rate_limit"
	case index.CauseGHMissing:
		return "Install gh and log in with it, then refresh again.", "gh auth login"
	case index.CauseNoRepository:
		return "Review mining needs an origin remote on github.com. Local fixes need no remote.", ""
	case index.CauseFetchFailed:
		return "Check the network and gh in a shell, then refresh again.", "gh auth status"
	case index.CauseUnreadable:
		return "gh returned a reply that is not the GitHub API JSON. Check gh, then refresh again.", "gh --version"
	case index.CauseStore:
		return "The index database did not accept the write. Doctor (press 4) checks it.", ""
	case index.CauseNoHistory, index.CauseMineFailed:
		return "Local fixes need a git repository with commits. Check git in a shell.", "git log -1"
	case index.CauseRefused:
		return "Press n to prepare the refresh again from the current state.", ""
	case index.CauseStopped:
		return "Press n to prepare the refresh again when you want the rest.", ""
	default:
		return "", ""
	}
}

// view implements runRecord: the context line, a blank row and the
// boxes. A wide terminal shows the run and the sources side by side.
func (r *evRun) view(w, h int) string {
	boxH := max(4, h-3)

	if w < wideWidth {
		return r.contextLine(w) + "\n\n" + r.compactBox(w, boxH)
	}

	leftW := min(propListW, w*propListW/propDesignW)

	return r.contextLine(w) + "\n\n" + hjoin(1, r.mainBox(leftW, boxH, !r.right), r.sourcesBox(w-leftW-1, boxH, r.right))
}

// contextLine renders the confirmed scope of the run and its start or
// end time.
func (r *evRun) contextLine(w int) string {
	t := r.st.theme

	seg := func(label, value string) string {
		return t.Faint.Render(label) + " " + t.Body.Render(value)
	}

	segs := []string{seg("sources", evModeTitle(r.plan.Mode))}

	if r.reviewMode() {
		segs = append(segs, seg("repository", evRepoShort(r.plan.Repository, r.plan.RepoState)))

		if text, ok := evWindow(r.plan.Window); ok {
			segs = append(segs, seg("window", text))
		}
	}

	if r.running {
		segs = append(segs, seg("started", r.started.Format("15:04:05")))
	} else {
		segs = append(segs, seg("finished", r.finished.Format("15:04:05")))
	}

	for len(segs) > 1 && width(" "+strings.Join(segs, "     ")) > w {
		segs = segs[:len(segs)-1]
	}

	return truncate(" "+strings.Join(segs, "     "), w, t.Glyphs.Ellipsis)
}

// rightLabel renders the state at the right of the main box. A stop
// request counts only while the worker runs, so the order of the checks
// does not matter here.
func (r *evRun) rightLabel() string {
	if r.running {
		return runStateLabel(r.st.theme, r.st.tick, r.stopping(), r.started)
	}

	v := r.verdict()

	return r.st.theme.Badge(v.tone, v.right)
}

// headline renders the first line of the main box: the current step of
// a running refresh, or the verdict of a finished one.
func (r *evRun) headline() string {
	t := r.st.theme
	spin := t.spinnerFrame(r.st.tick)

	if r.stopping() {
		return t.Attention.Render(spin) + " " + t.Title.Render("Stopping at a safe point")
	}

	if r.running {
		step := "Checking the preview against the workspace"

		for p := evPhCount - 1; p >= 0; p-- {
			if r.phase[p] == phaseActive {
				step = evPhaseLabels[p]
				if p == evPhFetch {
					step += " " + t.Glyphs.Sep + " " + evRepoShort(r.plan.Repository, r.plan.RepoState)
				}

				break
			}
		}

		return t.Accent.Render(spin) + " " + t.Title.Render(step)
	}

	v := r.verdict()

	return t.ToneStyle(v.tone).Render(t.ToneGlyph(v.tone)) + " " + t.Title.Render(v.head)
}

// statusLines renders the text under the headline: the stop note while
// running, the stop wait while stopping, and the lead of a result. short
// selects the brief notes of the compact box, which keep the steps in
// view on a small terminal.
func (r *evRun) statusLines(w int, short bool) []string {
	t := r.st.theme

	var note string

	switch {
	case r.stopping() && short:
		note = "Seamark waits for the running step to end. A partial fetch is never saved."
	case r.stopping():
		note = "Seamark waits for the running step to end. A running local fix mine finishes and saves " +
			"first. A stopped GitHub fetch saves nothing, so the stored reviews stay."
	case r.running && short && r.reviewMode():
		note = "x stops at a safe point. A partial fetch is never saved."
	case r.running && r.reviewMode():
		note = "x stops the refresh at a safe point. A running local fix mine finishes and saves first. A " +
			"stopped GitHub fetch saves nothing, so the stored reviews stay."
	case r.running:
		note = "The local fix mine cannot stop part way. x stops the run after it has saved."
	}

	if note != "" {
		return propLines(t.Muted, note, w)
	}

	// A source reason already names the cause, for example the busy
	// slot. The error then repeats it, so only an unexplained error shows.
	out := propLines(t.Body, r.lead(), w)
	if r.unexplained() {
		out = append(out, propLines(t.Muted, "Details: "+errText(r.err), w)...)
	}

	return out
}

// phaseLines renders the steps of the requested sources.
func (r *evRun) phaseLines(w int) []string {
	last := evPhSaveFixes
	if r.reviewMode() {
		last = evPhSaveReviews
	}

	rows := make([]phaseRow, 0, last+1)
	for p := evPhMine; p <= last; p++ {
		rows = append(rows, phaseRow{label: evPhaseLabels[p], note: oneLine(r.note[p]), state: r.phase[p]})
	}

	return r.st.theme.phaseRows(rows, r.st.tick, w)
}

// nextLines renders the NEXT block of a result: the keys that start the
// next step and the guidance for a source that did not refresh.
func (r *evRun) nextLines(w int) []string {
	t := r.st.theme

	var out []string

	keyRow := func(k, text string) {
		out = append(out, truncate(t.Key.Render(padRight(k, 7))+t.Body.Render(text), w, t.Glyphs.Ellipsis))
	}

	if r.succeeded() > 0 && r.conflict() == nil {
		keyRow("enter", "prepare a distillation from the evidence")
	}

	keyRow("n", "prepare this refresh again")

	for _, o := range r.sources() {
		if evSucceeded(o) {
			continue
		}

		if text, command := evGuidance(o); text != "" {
			out = append(out, "")
			out = append(out, propLines(t.Body, text, w)...)

			if command != "" {
				out = append(out, integCommandLines(t, command, w)...)
			}

			break
		}
	}

	return out
}

// sourceLines renders one source: its state and the facts about its
// stored data. A running record shows the state that the source
// reported, so the pane, the steps and the activity use one word.
func (r *evRun) sourceLines(source string, w int) []string {
	t := r.st.theme
	g := t.Glyphs
	name := padRight(evSourceName(source), 16)

	row := func(glyph, word string, facts ...string) []string {
		out := []string{truncate(glyph+" "+t.Body.Render(name)+" "+word, w, g.Ellipsis)}

		for _, f := range facts {
			if f != "" {
				out = append(out, propIndent("  ", propLines(t.Muted, f, w-2))...)
			}
		}

		return out
	}

	if source == index.SourceReviews && !r.reviewMode() {
		return row(t.Faint.Render(g.Info), t.Faint.Render("not part of this refresh"), r.kept(source, "stays", "stay"))
	}

	o := r.out.Fixes
	work, save := evPhMine, evPhSaveFixes

	if source == index.SourceReviews {
		o, work, save = r.out.Reviews, evPhFetch, evPhSaveReviews
	}

	if r.running {
		state, reported := r.reported[source]

		switch {
		case reported:
			o = index.SourceOutcome{State: state}
		case r.seen[work] || r.seen[save]:
			return row(t.Accent.Render(t.spinnerFrame(r.st.tick)), t.Accent.Render("running"), r.note[work])
		default:
			return row(t.Faint.Render(g.Hollow), t.Faint.Render("waiting"))
		}
	}

	st := evOutcomeState(o)
	glyph := t.ToneStyle(st.tone).Render(t.ToneGlyph(st.tone))
	word := t.ToneStyle(st.tone).Render(st.word)

	switch o.State {
	case index.SourceRefreshed:
		// The event of a running source carries no count. The result
		// adds the stored counts, so the pane never shows a guess.
		if r.running {
			return row(glyph, word)
		}

		fact := propPlural(o.Stored, "finding", "findings") + " stored"
		if source == index.SourceReviews {
			fact = propPlural(o.Stored, "finding", "findings") + " " + g.Sep + " " +
				propPlural(o.Lessons, "lesson", "lessons") + " stored"
		}

		return row(glyph, word, fact)
	case index.SourceEmpty:
		if source == index.SourceReviews {
			return row(glyph, word, "no review lessons "+g.Sep+" stored reviews replaced")
		}

		return row(glyph, word, "no fix commits "+g.Sep+" stored fix findings replaced")
	default:
		// A source that did not succeed never wrote, so its stored data
		// stays, also while the run goes on.
		return row(glyph, word, r.kept(source, "kept", "kept"), diag(o.Reason))
	}
}

// confirmedLines renders what the user confirmed in the preview, so the
// record keeps the scope of the run in view.
func (r *evRun) confirmedLines(w int) []string {
	t := r.st.theme
	p := r.plan

	rows := [][2]string{{"Workspace", oneLine(displayPath(p.Root))}}

	if r.reviewMode() {
		repo, _ := evRepo(p.Repository, p.RepoState)
		rows = append(rows, [2]string{"Repository", repo})

		if text, ok := evWindow(p.Window); ok {
			rows = append(rows, [2]string{"Window", text})
		}
	}

	rows = append(rows, [2]string{"Network", evNetworkFact(p)})

	var out []string
	for _, row := range rows {
		out = append(out, integLabeled(t, row[0], evLabelW, row[1], t.Body, w)...)
	}

	return out
}

// mainBox renders the run pane: the headline, the note or the lead,
// the steps, the next keys and the recent activity.
func (r *evRun) mainBox(w, h int, focused bool) string {
	t := r.st.theme
	inner := boxInner(w)

	body := append([]string{r.headline(), ""}, r.statusLines(inner, false)...)
	body = append(body, "", t.Heading.Render("PHASES"))
	body = append(body, r.phaseLines(inner)...)

	if !r.running {
		body = append(body, "", t.Heading.Render("NEXT"))
		body = append(body, r.nextLines(inner)...)
		body = append(body, "")
		body = append(body, propLines(t.Faint, "Nothing is retried automatically.", inner)...)
	}

	body = append(body, "", t.Heading.Render("RECENT ACTIVITY"))
	body = append(body, t.activityLines(r.activity, evActivityShow, inner)...)

	return r.scrollBox(0, boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Evidence refresh", focused), Right: r.rightLabel(),
	}, body)
}

// sourcesBox renders the sources pane: each source with its state and
// its stored data, then the confirmed scope.
func (r *evRun) sourcesBox(w, h int, focused bool) string {
	t := r.st.theme
	inner := boxInner(w)

	body := []string{t.Heading.Render("SOURCES")}
	body = append(body, r.sourceLines(index.SourceFixes, inner)...)
	body = append(body, r.sourceLines(index.SourceReviews, inner)...)
	body = append(body, "", t.Heading.Render("CONFIRMED IN THE PREVIEW"))
	body = append(body, r.confirmedLines(inner)...)

	return r.scrollBox(1, boxOpts{W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Sources", focused)}, body)
}

// compactBox renders the whole record in one box that scrolls, for a
// terminal below the wide width.
func (r *evRun) compactBox(w, h int) string {
	t := r.st.theme
	inner := boxInner(w)

	body := append([]string{r.headline(), ""}, r.statusLines(inner, true)...)
	body = append(body, "", t.Heading.Render("PHASES"))
	body = append(body, r.phaseLines(inner)...)
	body = append(body, "", t.Heading.Render("SOURCES"))
	body = append(body, r.sourceLines(index.SourceFixes, inner)...)
	body = append(body, r.sourceLines(index.SourceReviews, inner)...)

	if !r.running {
		body = append(body, "", t.Heading.Render("NEXT"))
		body = append(body, r.nextLines(inner)...)
	}

	body = append(body, "", t.Heading.Render("RECENT ACTIVITY"))
	body = append(body, t.activityLines(r.activity, 2, inner)...)

	return r.scrollBox(0, boxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Evidence refresh", true), Right: r.rightLabel(),
	}, body)
}

// scrollBox renders body in a box whose text scrolls with the state of
// pane i. The foot shows the visible range, so hidden lines are never a
// surprise. The shared Theme.scrollBox keeps the rules of every box.
func (r *evRun) scrollBox(i int, o boxOpts, body []string) string {
	return r.st.theme.scrollBox(o, body, nil, &r.scroll[i], [2]int{-1, -1}, false)
}
