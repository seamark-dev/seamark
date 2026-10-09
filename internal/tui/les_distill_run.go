package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/distill"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// This file renders a running distillation and its result (design map
// §6 and §7), and the two confirmations: the stop and the stale-pending
// cleanup. Every count has a known denominator: the groups of the
// frozen plan.

// Column widths of the Groups pane of the run and the result.
const (
	distGlyphW   = 3
	distRunIdxW  = 3
	distRunFindW = 6
	// distOutcomeMin is the narrowest outcome column; the region column
	// gives way first.
	distOutcomeMin = 22
)

// distCount counts group outcomes by status.
func distCount(groups []distill.GroupOutcome) unitCounts {
	var c unitCounts

	for _, g := range groups {
		switch g.Status {
		case distill.GroupCompleted:
			c.completed++
		case distill.GroupFailed:
			c.failed++
		case distill.GroupCancelled:
			c.cancelled++
		case distill.GroupSkipped:
			c.skipped++
		default:
			c.unattempted++
		}
	}

	return c
}

// counts counts the group outcomes of the record. The lost group of an
// internal error has no status, but its call started. So it counts as
// unknown, never as not attempted.
func (d *distRecord) counts() unitCounts {
	c := distCount(d.outcomes())

	if d.lostGroup() >= 0 {
		c.unattempted--
		c.unknown++
	}

	return c
}

// lostGroup returns the group whose result an internal error hid, or -1.
func (d *distRecord) lostGroup() int {
	outs := d.outcomes()
	k := d.run.lost

	if !d.run.internal || k < 0 || k >= len(outs) || outs[k].Status != distill.GroupUnattempted {
		return -1
	}

	return k
}

// verdict classifies the finished run from its typed outcome.
func (d *distRecord) verdict() agentVerdict {
	x := d.run.exec
	c := d.counts()

	fresh := propPlural(len(d.newIDs()), "new proposal", "new proposals")
	if len(d.newIDs()) == 0 {
		fresh = "no new proposals"
	}
	saved := propPlural(c.completed, "group is", "groups are") + " saved with " + fresh + "."

	switch {
	case d.run.internal:
		// Groups can be sent and saved before the error, so the lead
		// never says that nothing was sent.
		lead := "The run stopped on an internal error: " + errText(d.run.err) + ". " + saved
		if c.unknown > 0 {
			lead += " Group " + itoa(d.run.lost+1) + " was sending when the error happened, so its result is not " +
				"known. The Proposals view shows what was saved."
		}

		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Distillation stopped on an internal error",
			lead:  lead + distUnattemptedText(c) + " No pin was applied.",
			toast: "Distillation stopped on an internal error: " + fresh + ".",
		}
	case x.Stop == distill.StopStoreError || d.run.err != nil:
		cause := d.run.err
		if cause == nil {
			cause = x.StopErr
		}

		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Distillation stopped on a store error",
			lead: "A store read or write failed: " + errText(cause) + ". " + saved +
				" The remaining groups did not run. No pin was applied.",
			toast: "Distillation stopped on a store error: " + fresh + ".",
		}
	case x.Stop == distill.StopCancelled:
		return agentVerdict{
			tone: ToneAttention, word: "stopped", head: "Distillation stopped",
			lead:  "You stopped the run. " + saved + distCancelledText(c) + distUnattemptedText(c),
			toast: "Distillation stopped: " + propPlural(c.completed, "group", "groups") + " saved, " + fresh + ".",
		}
	case x.Stop == distill.StopConflict:
		return agentVerdict{
			tone: ToneAttention, word: "stopped", head: "Distillation stopped: an input changed",
			lead: errText(x.StopErr) + ". " + saved + distCancelledText(c) + distUnattemptedText(c) +
				" Prepare the remaining work for a new plan from the current state.",
			toast: "Distillation stopped because an input changed: " + fresh + ".",
		}
	case c.completed == 0 && c.failed > 0:
		return agentVerdict{
			tone: ToneFailure, word: "failed", head: "Distillation failed",
			lead: "Every group failed, so nothing was saved. The failed groups have no signature, so a new plan " +
				"can include them. No pin was applied.",
			toast: "Distillation failed: no group was saved.",
		}
	case c.failed > 0:
		return agentVerdict{
			tone: ToneAttention, word: "partial", head: "Distillation finished with failed groups",
			lead: propPlural(c.failed, "group", "groups") + " failed. " + saved + " The failed groups have no " +
				"signature, so a new plan can include them. No pin was applied.",
			toast: "Distillation finished with " + propPlural(c.failed, "failed group", "failed groups") + ": " +
				fresh + ".",
		}
	}

	lead := "Every planned group was sent and saved: " + fresh + ". No pin was applied."
	if len(d.newIDs()) == 0 {
		lead = "Every planned group was sent and saved, and the agent proposed nothing new. The signatures " +
			"are recorded, so these groups are not sent again."
	}

	if d.run.stopping {
		lead += " The stop came after the last reply, so every group finished."
	}

	return agentVerdict{
		tone: ToneSuccess, word: "completed", head: "Distillation completed", lead: lead,
		toast: "Distillation completed: " + fresh + ".",
	}
}

// distCancelledText tells that failed and cancelled groups stay
// eligible, because neither has a signature.
func distCancelledText(c unitCounts) string {
	var out string

	if c.failed > 0 {
		out += " " + propPlural(c.failed, "group", "groups") + " failed."
	}

	switch c.cancelled {
	case 0:
	case 1:
		out += " 1 group was cancelled before its result was saved, so it stays eligible."
	default:
		out += " " + itoa(c.cancelled) + " groups were cancelled before their results were saved, so they " +
			"stay eligible."
	}

	return out
}

// distUnattemptedText counts the groups that never started.
func distUnattemptedText(c unitCounts) string {
	if c.unattempted == 0 {
		return ""
	}

	return " " + propPlural(c.unattempted, "group was", "groups were") + " not attempted."
}

// runView renders the run or its result: the summary and the groups side
// by side, or one of them in the compact layout.
func (d *distRecord) runView(w, h int) string {
	if d.st.wide() {
		leftW := setupSplit(w, 60)

		return hjoin(1, d.summaryBox(leftW, h, d.run.sumFocus), d.runGroupsBox(w-leftW-1, h, !d.run.sumFocus))
	}

	if d.run.detail {
		return d.runGroupsBox(w, h, true)
	}

	return d.compactBox(w, h)
}

// stateLabel renders the state at the right of the summary border. The
// result comes first, because a stop request stays set after the end.
func (d *distRecord) stateLabel() string {
	if d.step == stepResult {
		v := d.verdict()

		return d.st.theme.Badge(v.tone, v.word)
	}

	return runStateLabel(d.st.theme, d.st.tick, d.stopping(), d.run.started)
}

// headline renders the first line of the summary.
func (d *distRecord) headline() string {
	t := d.st.theme
	g := t.Glyphs
	spin := t.spinnerFrame(d.st.tick)
	r := &d.run
	n := len(r.units)

	if d.step == stepResult {
		v := d.verdict()

		return t.ToneStyle(v.tone).Render(t.ToneGlyph(v.tone)+" ") + t.Title.Render(v.head)
	}

	group := func(k int) string {
		return "group " + itoa(k+1) + " of " + itoa(n) + " " + g.Sep + " " + distRegionLabel(r.units[k].Plan.Region)
	}

	// A stop from the quit question sets no stop group, so the running
	// group stands in for it.
	stopAt := r.stopAt
	if stopAt < 0 {
		stopAt = r.sending
	}

	switch {
	case d.stopping() && stopAt >= 0:
		return t.Attention.Render(spin+" ") + t.Title.Render("Stopping after "+group(stopAt))
	case d.stopping():
		return t.Attention.Render(spin+" ") + t.Title.Render("Stopping at the next check")
	case r.sending >= 0:
		return t.Accent.Render(spin+" ") + t.Title.Render("Sending "+group(r.sending))
	case !r.checked:
		return t.Accent.Render(spin+" ") + t.Title.Render("Checking the plan against the current state")
	default:
		return t.Accent.Render(spin+" ") + t.Title.Render("Between groups "+g.Sep+" "+itoa(r.finished)+" of "+
			itoa(n)+" done")
	}
}

// summaryBox renders the left pane: the headline, the stop note or the
// result lead, then the phases or what happened, and the next steps.
func (d *distRecord) summaryBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := boxInner(w)

	var body []string

	if d.step == stepResult {
		body = d.resultLines(inner)
	} else {
		body = d.progressLines(inner)
	}

	o := boxOpts{W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Distillation", true), Right: d.stateLabel()}

	return d.st.theme.scrollBox(o, body, nil, &d.run.summary, [2]int{-1, -1}, false)
}

// progressLines renders the running summary (design map §6).
func (d *distRecord) progressLines(w int) []string {
	t := d.st.theme
	r := &d.run

	lines := []string{d.headline(), ""}

	note := "x stops the run: no new group starts and the running agent call is cancelled. Finished groups " +
		"stay saved. A provider can still bill a cancelled call."
	if d.stopping() {
		note = "No new group starts. Seamark waits for the running work to end; the result shows whether the " +
			"last group was saved before the stop."
		if r.stopAt >= 0 {
			note = "Seamark waits for the agent process of group " + itoa(r.stopAt+1) + " to end. No new group " +
				"starts. The result shows whether group " + itoa(r.stopAt+1) + " was saved before the stop."
		}
	}

	lines = append(lines, integLines(t.Muted, note, w)...)
	lines = append(lines, "", integHeading(t, "Phases"))
	lines = append(lines, t.phaseRows(d.phases(), d.st.tick, w)...)
	lines = append(lines, "")
	lines = append(lines, d.barLines(w)...)
	lines = append(lines, "", integHeading(t, "Results so far"))
	lines = append(lines, propKV(t, d.soFar(), w)...)

	if len(r.activity) > 0 {
		lines = append(lines, "", integHeading(t, "Recent activity"))
		lines = append(lines, t.activityLines(r.activity, agentActivityKeep, w)...)
	}

	return lines
}

// phases renders the steps of the run from the typed events.
func (d *distRecord) phases() []phaseRow {
	r := &d.run
	n := len(r.units)

	check := phaseRow{label: "Check the plan", state: phaseActive}

	switch {
	case r.checked:
		check.state, check.note = phaseDone, "no change since the preview"
	case r.stopped:
		check.state, check.note = phaseStopped, distStopWords(r.stopWhy)
	}

	rows := []phaseRow{check}

	if ids := distProposalIDs(d.prev.preview.Plan.Cleanup); len(ids) > 0 {
		clean := phaseRow{label: "Remove stale pending", note: propIDList(ids) + " before the first group"}
		if d.swept.done {
			clean.state, clean.note = phaseDone, itoa(d.swept.removed)+" removed: "+propIDList(ids)
		}

		rows = append(rows, clean)
	}

	send := phaseRow{label: "Send groups to the agent", note: itoa(r.finished) + " of " + itoa(n) + " done"}
	if r.checked && !r.stopped {
		send.state = phaseActive
	}

	return append(rows, send, phaseRow{label: "Report the result"})
}

// barLines renders the bar of the saved groups of a running record.
// The denominator is the frozen plan, so the bar shows real progress.
// It fills with saved groups only, because the success color must never
// count a failed or cancelled group (design map §1.1). The result has
// no bar: its table and counts tell each outcome apart (design map §7).
func (d *distRecord) barLines(w int) []string {
	t := d.st.theme
	n := len(d.outcomes())
	saved := distCount(d.outcomes()).completed

	count := itoa(saved) + " / " + itoa(n)
	barW := max(4, min(40, w-width(count)-4))

	return []string{
		t.Heading.Render("GROUPS") + t.Faint.Render("  "+itoa(saved)+" of "+itoa(n)+" saved"),
		t.bar(saved, n, barW, ToneSuccess) + "    " + t.Muted.Render(count),
	}
}

// barLine renders the bar of the saved groups in one line, for the
// compact box, where every row counts.
func (d *distRecord) barLine(w int) string {
	t := d.st.theme
	n := len(d.outcomes())
	saved := distCount(d.outcomes()).completed

	count := itoa(saved) + " of " + itoa(n) + " saved"
	barW := max(4, min(40, w-width(count)-4))

	return t.bar(saved, n, barW, ToneSuccess) + "    " + t.Muted.Render(count)
}

// soFar renders the durable results of a running record.
func (d *distRecord) soFar() [][2]string {
	t := d.st.theme

	ids := d.newIDs()

	proposals := t.Muted.Render("none yet")
	if len(ids) > 0 {
		proposals = t.Body.Render(propIDList(ids))
	}

	return [][2]string{
		{"Proposals", proposals},
		{"Duplicates", t.Body.Render(d.duplicatesText())},
		{"Usage", d.usageText()},
	}
}

// duplicatesText counts the groups that returned only known patterns.
func (d *distRecord) duplicatesText() string {
	only, dropped := 0, 0

	for _, o := range d.outcomes() {
		dropped += len(o.Duplicates)

		if o.Status == distill.GroupCompleted && len(o.Proposals) == 0 && len(o.Duplicates) > 0 {
			only++
		}
	}

	if dropped == 0 {
		return "none"
	}

	text := propPlural(dropped, "pattern", "patterns") + " restated known ones"
	if only > 0 {
		text += "; " + propPlural(only, "group", "groups") + " returned only known patterns"
	}

	return text
}

// usageText renders the estimated agent traffic. The engine meters
// characters; the tokens are its estimate. A finished call meters its
// prompt and its reply. The prompt of the group that sends is already
// with the agent, so its planned size counts as sent before the reply.
func (d *distRecord) usageText() string {
	t := d.st.theme

	sent, back := 0, 0

	outs := d.outcomes()
	for _, o := range outs {
		sent += o.PromptChars
		back += o.ReplyChars
	}

	pending, k := 0, d.run.sending
	if d.step == stepRunning && k >= 0 && k < len(outs) {
		pending = outs[k].Plan.PromptChars
	}

	switch {
	case sent == 0 && pending == 0:
		return t.Muted.Render("no agent traffic yet")
	case sent == 0:
		return t.Body.Render(distTokens(pending)+" tokens sent to group "+itoa(k+1)) + " " +
			t.Faint.Render("(estimated)") + t.Muted.Render("; reply pending")
	}

	text := t.Body.Render(distTokens(sent+pending)+" tokens sent "+t.Glyphs.Sep+" "+distTokens(back)+" received") +
		" " + t.Faint.Render("(estimated)")

	if pending > 0 {
		text += t.Muted.Render("; group " + itoa(k+1) + " reply pending")
	}

	if d.step == stepResult {
		text += t.Muted.Render(" " + t.Glyphs.Sep + " " + runElapsed(d.run.ended.Sub(d.run.started)))
	}

	return text
}

// resultLines renders the result summary (design map §7).
func (d *distRecord) resultLines(w int) []string {
	t := d.st.theme
	v := d.verdict()

	lines := []string{d.headline(), ""}
	lines = append(lines, integLines(t.Body, v.lead, w)...)
	lines = append(lines, "", integHeading(t, "What happened"))
	lines = append(lines, propKV(t, d.happened(), w)...)
	lines = append(lines, "", integHeading(t, "Next"))
	lines = append(lines, d.nextLines(w)...)
	lines = append(lines, "")

	return append(lines, integLines(t.Faint, "A new plan starts from the current state. Nothing is retried "+
		"automatically.", w)...)
}

// happened renders the facts of the finished run.
func (d *distRecord) happened() [][2]string {
	t := d.st.theme
	x := d.run.exec
	c := d.counts()
	sep := " " + t.Glyphs.Sep + " "

	ids := d.newIDs()

	proposals := t.Muted.Render("none")
	if len(ids) > 0 {
		proposals = t.Body.Render(itoa(len(ids))+" new, pending: ") + t.Body.Render(propIDList(ids))
	}

	rows := [][2]string{
		{"Groups", t.Body.Render(strings.Join(c.parts("saved"), sep))},
		{"Proposals", proposals},
		{"Duplicates", t.Body.Render(d.duplicatesText())},
	}

	if len(x.CleanupIDs) > 0 {
		rows = append(rows, [2]string{"Cleanup", d.cleanupText(c)})
	}

	signatures := itoa(c.completed) + " recorded"
	if open := c.failed + c.cancelled + c.unknown; open > 0 {
		signatures += "; " + propPlural(open, "group without one stays", "groups without one stay") + " eligible"
	}

	rows = append(rows, [2]string{"Signatures", t.Body.Render(signatures)}, [2]string{"Usage", d.usageText()})

	switch {
	case d.run.internal:
		rows = append(rows, [2]string{"Stop", t.Failure.Render("internal error")})
	case x.Stop != distill.StopNone:
		rows = append(rows, [2]string{"Stop", t.Attention.Render(distStopWords(x.Stop))})
	}

	return rows
}

// cleanupText reports the stale-pending cleanup of the run. The engine
// removes the rows before the first group, so an attempted group proves
// that the cleanup ran, also when it removed no row.
func (d *distRecord) cleanupText(c unitCounts) string {
	t := d.st.theme
	x := d.run.exec
	n := len(x.CleanupIDs)

	ran := d.swept.done || x.Removed > 0 || c.unattempted < len(x.Groups)

	switch {
	case !ran && d.run.internal:
		// The error can come in the cleanup, after a delete and before
		// its event, so the console does not claim that nothing changed.
		return t.Muted.Render("not known: the run stopped on an internal error before the cleanup reported")
	case !ran:
		return t.Muted.Render("not run: the run ended before the cleanup")
	}

	text := itoa(x.Removed) + " of " + itoa(n) + " stale pending removed: " + propIDList(x.CleanupIDs)
	if x.Removed < n {
		text += "; the others were decided meanwhile"
	}

	return t.Body.Render(text)
}

// nextLines names the keys of the result. enter shows only with new
// proposals, because the review opens exactly the IDs of this run.
func (d *distRecord) nextLines(w int) []string {
	t := d.st.theme

	row := func(k, text string) []string {
		return integHanging(t.Key.Render(padRight(k, 7)), t.Body, text, w)
	}

	var lines []string

	if n := len(d.newIDs()); n > 0 {
		lines = append(lines, row("enter", "review the "+propPlural(n, "new proposal", "new proposals"))...)
	}

	lines = append(lines, row("n", "prepare the remaining work: a new plan, started only with y")...)

	return append(lines, row("esc", "close")...)
}

// runGroupsBox renders the Groups pane: every planned group with its
// state, then the detail of the cursor group.
func (d *distRecord) runGroupsBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := boxInner(w)
	outs := d.outcomes()

	d.run.cursor = max(0, min(d.run.cursor, len(outs)-1))

	body, keep := d.runGroupLines(inner, focused, true)
	body = append(body, "")
	body = append(body, d.groupDetail(inner)...)

	right := itoa(d.run.finished) + " of " + itoa(len(outs)) + " done"
	if d.step == stepResult {
		right = itoa(len(outs)-d.counts().unattempted) + " of " + itoa(len(outs)) + " ran"
	}

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Groups", focused), Right: t.Muted.Render(right),
	}

	return d.st.theme.scrollBox(o, body, nil, &d.run.list, keep, d.run.follow)
}

// runGroupLines renders the group table. find adds the findings column,
// which the compact summary leaves out. It returns the line range of the
// cursor row.
func (d *distRecord) runGroupLines(w int, focused, find bool) (lines []string, keep [2]int) {
	t := d.st.theme
	outs := d.outcomes()

	findW := 0
	if find {
		findW = distRunFindW
	}

	outW := max(distOutcomeMin, (w-2-distGlyphW-distRunIdxW-findW)/2)
	regW := max(6, w-2-distGlyphW-distRunIdxW-findW-outW)

	cells := func(marker, glyph, idx, region, found, outcome string) string {
		cs := []colCell{cell(marker, 2), cell(glyph, distGlyphW), cell(idx, distRunIdxW), cell(region, regW)}
		if find {
			cs = append(cs, cell(found, findW))
		}

		return integRow(t, append(cs, cell(outcome, outW))...)
	}

	lines = []string{t.Faint.Render(cells("", "", padLeft("#", distRunIdxW-1), "Region",
		padLeft("Find", findW-2), "Outcome"))}
	keep = [2]int{-1, -1}

	for i, o := range outs {
		cursor := i == d.run.cursor

		marker, region := "", t.Body
		if cursor {
			marker, region = t.Key.Render(t.Glyphs.Cursor), t.Title
		}

		glyph, outcome := d.outcomeCell(i, o)

		row := cells(marker, glyph, t.Muted.Render(padLeft(itoa(i+1), distRunIdxW-1)),
			region.Render(distRegionLabel(o.Plan.Region)), t.Body.Render(padLeft(itoa(o.Plan.Findings), findW-2)),
			outcome)

		if cursor && focused {
			row = t.highlight(row, w, bgSel)
		}

		if cursor {
			keep = [2]int{len(lines), len(lines)}
		}

		lines = append(lines, row)
	}

	return lines, keep
}

// outcomeCell returns the state glyph and the outcome words of group i
// (design map §9). A waiting group reads "waiting" only while the run
// is active; after it, the same group reads "not attempted".
func (d *distRecord) outcomeCell(i int, o distill.GroupOutcome) (glyph, words string) {
	t := d.st.theme
	g := t.Glyphs

	switch o.Status {
	case distill.GroupCompleted:
		if len(o.Proposals) == 0 && len(o.Duplicates) > 0 {
			return t.Faint.Render(g.Info), t.Faint.Render("duplicates only")
		}

		text := propPlural(len(o.Proposals), "proposal", "proposals")
		if len(o.Proposals) > 0 {
			text += " " + g.Sep + " " + strings.Join(distIDWords(o.Proposals), " ")
		}

		return t.Success.Render(g.OK), t.Body.Render(text)
	case distill.GroupFailed:
		return t.Failure.Render(g.Fail), t.Failure.Render("failed: " + distFailWords(o.Failure))
	case distill.GroupCancelled:
		return t.Attention.Render(g.Warn), t.Attention.Render("cancelled")
	case distill.GroupSkipped:
		return t.Faint.Render(g.Info), t.Faint.Render("skipped: recorded elsewhere")
	}

	switch {
	case i == d.lostGroup():
		return t.Attention.Render(g.Warn), t.Attention.Render("result unknown")
	case d.step == stepRunning && i == d.run.sending:
		return t.Accent.Render(t.spinnerFrame(d.st.tick)), t.Accent.Render("sending" + g.Ellipsis)
	case d.step == stepRunning:
		return t.Faint.Render(g.Hollow), t.Faint.Render("waiting")
	default:
		return t.Attention.Render(g.Warn), t.Attention.Render("not attempted")
	}
}

// distIDWords renders proposal IDs in the ledger's pN form.
func distIDWords(ps []domain.Proposal) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = propID(p.ID)
	}

	return out
}

// groupDetail renders the cursor group: its failure or stop cause, its
// saved proposals, its duplicates, and its evidence. The cause can hold
// agent output, so the view redacts and sanitizes it.
func (d *distRecord) groupDetail(w int) []string {
	t := d.st.theme
	outs := d.outcomes()

	if len(outs) == 0 {
		return nil
	}

	k := d.run.cursor
	o := outs[k]

	lines := []string{t.Heading.Render("GROUP "+itoa(k+1)) + t.Muted.Render(" "+t.Glyphs.Sep+" "+
		distRegionLabel(o.Plan.Region))}

	switch o.Status {
	case distill.GroupFailed:
		lines = append(lines, propNote(t, ToneFailure, "failed: "+distFailWords(o.Failure)+": "+errText(o.Err)+
			". The group has no signature, so a new plan can include it.", w)...)
	case distill.GroupCancelled:
		lines = append(lines, propNote(t, ToneAttention, "cancelled before its result was saved, so it stays "+
			"eligible.", w)...)
	case distill.GroupSkipped:
		lines = append(lines, propNote(t, ToneNeutral, "skipped: "+errText(o.Err), w)...)
	}

	if k == d.lostGroup() {
		lines = append(lines, propNote(t, ToneAttention, "result unknown: the agent call started, then the run "+
			"stopped on an internal error. The Proposals view shows what was saved.", w)...)
	}

	for _, p := range o.Proposals {
		lines = append(lines, integHanging(t.Body.Render(padRight(propID(p.ID), 6)), t.Body, oneLine(p.Rule), w)...)
	}

	for _, dup := range o.Duplicates {
		lines = append(lines, integLines(t.Faint, "dropped "+oneLine(dup.Rule)+": restates "+oneLine(dup.Of), w)...)
	}

	lines = append(lines, "", integHeading(t, "Evidence"))

	return append(lines, distEvidenceLines(t, o.Plan.Evidence, w, 0)...)
}

// compactBox renders the run in one box for a narrow terminal. The
// headline stays above the lines that scroll, so the state of a paid
// run never scrolls away. While the run works, the short stop note
// stays under it, and the bar, the group table and the latest activity
// scroll. A result shows its lead, what happened, the group table and
// the next steps (design map §7). It opens at its top and follows the
// cursor only after a move. tab shows the Groups pane.
func (d *distRecord) compactBox(w, h int) string {
	t := d.st.theme
	inner := boxInner(w)
	r := &d.run

	head := []string{d.headline()}
	body := []string{""}
	follow := r.follow

	if d.step == stepResult {
		body = append(body, integLines(t.Body, d.verdict().lead, inner)...)
		body = append(body, "", integHeading(t, "What happened"))
		body = append(body, propKV(t, d.happened(), inner)...)
		body = append(body, "")
		follow = r.follow && r.moved
	} else {
		head = append(head, integLines(t.Muted, d.shortStopNote(), inner)...)
		body = append(body, d.barLine(inner), "")
	}

	table, keep := d.runGroupLines(inner, true, false)
	keep = [2]int{keep[0] + len(body), keep[1] + len(body)}
	body = append(body, table...)

	switch {
	case d.step == stepResult:
		body = append(body, "", integHeading(t, "Next"))
		body = append(body, d.nextLines(inner)...)
		body = append(body, "")
		body = append(body, integLines(t.Faint, "A new plan starts from the current state. Nothing is retried "+
			"automatically.", inner)...)
	case len(r.activity) > 0:
		body = append(body, "")
		body = append(body, t.activityLines(r.activity, 1, inner)...)
	}

	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Distillation", true), Right: d.stateLabel()}

	return d.st.theme.pinnedBox(o, head, body, nil, &r.list, keep, follow)
}

// shortStopNote is the one-line stop note of the compact box. The stop
// question gives the full text, with the billing note.
func (d *distRecord) shortStopNote() string {
	switch {
	case d.stopping() && d.run.stopAt >= 0:
		return "Seamark waits for the agent call of group " + itoa(d.run.stopAt+1) + " to end."
	case d.stopping():
		return "Seamark waits for the running work to end."
	default:
		return "x stops the run. Finished groups stay saved."
	}
}

// modal returns the open confirmation: the stop of a running record, or
// the cleanup of a cleanup-only plan.
func (d *distRecord) modal(w, h int) (string, bool) {
	switch {
	case d.step == stepRunning && d.run.stopAsk:
		return d.stopModal(w, h), true
	case d.step == stepPreview && d.clean.ask:
		return d.cleanModal(w, h), true
	}

	return "", false
}

// stopModal asks before a stop. It says what stops, what stays, and
// that a provider can still bill the cancelled call. It is at most 64
// cells wide, like the quit question and the stop question of an
// evidence refresh (design map §6).
func (d *distRecord) stopModal(w, h int) string {
	text := "Seamark starts no new group and ends the run at the next check. Groups saved so far stay " +
		"saved. Nothing is retried."
	if k := d.run.sending; k >= 0 {
		text = "Seamark cancels the agent call of group " + itoa(k+1) + " and starts no new group. Groups saved " +
			"so far stay saved. A provider can still bill the cancelled call. Nothing is retried."
	}

	return runStopModal(d.st.theme, "Stop the distillation?", text, withHelp(agentKeys.Start, "y", "stop the run"), w, h)
}

// cleanModal asks before the stale-pending cleanup. It lists every row,
// because y removes exactly these rows and nothing else.
func (d *distRecord) cleanModal(w, h int) string {
	t := d.st.theme
	inner := modalBodyWidth(modalMinW, w)
	rows := d.prev.preview.Plan.Cleanup

	body := integLines(t.Body, "Seamark deletes these pending proposals. Their evidence group no longer "+
		"exists, so a distillation would delete them first:", inner)
	body = append(body, "")

	for _, p := range rows {
		body = append(body, integHanging(t.Attention.Render(padRight(propID(p.ID), 7)), t.Body,
			oneLine(p.Rule)+" "+t.Glyphs.Sep+" "+propRegionLabel(p), inner)...)
	}

	body = append(body, t.effectLines(
		[]effect{{glyph: t.Glyphs.Del, tone: ToneFailure, text: "delete the listed rows that are still pending"}},
		[]string{"call the agent", "change applied pins or lessons.yaml: this is not pin pruning",
			"delete a row that another writer decided meanwhile"}, inner)...)

	n := len(rows)

	return t.modal(modalOpts{
		Title: t.TabActive.Render("Remove") + t.Body.Render(" "+propPlural(n, "stale pending proposal",
			"stale pending proposals")+"?"),
		Body: body,
		Actions: []key.Binding{
			withHelp(agentKeys.Start, "y", "remove "+propPlural(n, "proposal", "proposals")),
			withHelp(nkeys.Back, "esc", "keep them"),
		},
	}, &d.clean.scroll, w, h)
}
