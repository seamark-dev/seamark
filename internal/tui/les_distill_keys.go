package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/learning"
)

// This file handles the keys of a distillation record and names them in
// the footer. The paid start and the cleanup take y from a preview,
// never enter (contracts §3.3).

// distKeys are the letter keys of a distillation record. The other keys
// come from nkeys, so every list moves the same way. p, s, e and u stay
// free, because Lessons switches its sub-views with them.
var distKeys = struct {
	Region, Start, Back, Stop, Cleanup, About, Link key.Binding
}{
	Region:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "region")),
	Start:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "start run")),
	Back:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "back")),
	Stop:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
	Cleanup: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clean up")),
	About:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "about")),
	// Link opens the place that fixes a blocker, for example Settings ›
	// Inference. It only navigates; the fix stays a separate action.
	Link: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "open")),
}

// key routes a key press by step.
func (d *distRecord) key(msg tea.KeyMsg) tea.Cmd {
	switch d.step {
	case distScope:
		return d.scopeKey(msg)
	case distPlanning:
		if key.Matches(msg, nkeys.Back, distKeys.Back) {
			d.backToScope(ToneNeutral, "")
		}
	case distReview:
		return d.reviewKey(msg)
	case distRunning:
		return d.runKey(msg)
	case distResult:
		return d.resultKey(msg)
	}

	return nil
}

// scopeKey handles the form. The inputs capture every key while they
// are open, so a typed digit never switches the destination.
func (d *distRecord) scopeKey(msg tea.KeyMsg) tea.Cmd {
	f := &d.form

	// The wide layout shows the explanation beside the form, so the
	// compact explanation mode ends there. Else it takes the form keys
	// while the frame shows the form.
	if d.wide() {
		f.about = false
	}

	switch {
	case f.editRegion:
		return d.editKey(msg, &f.region, &f.regionWas, &f.editRegion)
	case f.editCustom:
		// The custom choice opened this input, so esc also restores the
		// limit choice from before it. A cancelled edit changes nothing.
		// enter ends the edit, so the form now checks the number.
		switch {
		case key.Matches(msg, nkeys.Back):
			f.limit = f.limitWas
		case key.Matches(msg, nkeys.Enter):
			f.customChecked = true
		}

		return d.editKey(msg, &f.custom, &f.customWas, &f.editCustom)
	case f.about:
		if key.Matches(msg, nkeys.Back, distKeys.About) {
			f.about = false
		} else {
			f.aboutSc.key(msg)
		}

		return nil
	}

	switch {
	case key.Matches(msg, distKeys.Link):
		return d.follow()
	case key.Matches(msg, nkeys.Up):
		f.row = max(0, f.row-1)
	case key.Matches(msg, nkeys.Down):
		f.row = min(distRowCount-1, f.row+1)
	case key.Matches(msg, distKeys.Region):
		d.editRegion()
	case key.Matches(msg, nkeys.Toggle):
		d.choose()
	case key.Matches(msg, nkeys.Enter):
		return d.startPlan()
	case key.Matches(msg, nkeys.Back):
		return d.close()
	case key.Matches(msg, distKeys.About):
		f.about = !d.wide()
		f.aboutSc = scrollState{}
	}

	return nil
}

// editKey handles an open input: enter keeps the value, esc restores
// the value from before the edit.
func (d *distRecord) editKey(msg tea.KeyMsg, in *propInput, was *string, open *bool) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter):
		*open = false
	case key.Matches(msg, nkeys.Back):
		in.set(*was)
		*open = false
	default:
		in.key(msg)
	}

	return nil
}

// editRegion opens the region input.
func (d *distRecord) editRegion() {
	f := &d.form
	f.row = distRowRegion
	f.regionWas = f.region.value()
	f.editRegion = true
}

// choose acts on the cursor row: the region row opens its input, and a
// limit row selects its choice. The custom choice opens the digit input,
// because the number is part of the choice. esc in that input restores
// the earlier choice.
func (d *distRecord) choose() {
	f := &d.form

	switch f.row {
	case distRowRegion:
		d.editRegion()
	case distRowDefault:
		f.limit = distLimitDefault
	case distRowCustom:
		f.limitWas = f.limit
		f.limit = distLimitCustom
		f.customWas = f.custom.value()
		f.editCustom = true
		f.customChecked = false
	case distRowAll:
		f.limit = distLimitAll
	}
}

// blockerLink returns the place that fixes the blocker of the current
// step and its name. ok is false without such a place. The index build
// opens its own confirmation, so the link never builds anything.
func (d *distRecord) blockerLink() (to navigateMsg, name string, ok bool) {
	inference := navigateMsg{dest: destSettings, intent: "group:inference"}
	arrow := " " + d.st.theme.Glyphs.Arrow + " "

	switch d.step {
	case distScope:
		if d.agentBlock() != "" {
			return inference, "Settings" + arrow + "Inference", true
		}
	case distReview:
		var cfg *learning.DistillConfigError

		switch err := d.plan.err; {
		case errors.As(err, &cfg) && cfg.Path == settingsFile:
			return inference, "Settings" + arrow + "Inference", true
		case errors.Is(err, learning.ErrDistillNotReady):
			return navigateMsg{dest: destOverview, intent: ovIntentBuild}, "Overview" + arrow + "build", true
		case err == nil && d.plan.preview != nil && len(d.plan.preview.Blockers) > 0:
			return inference, "Settings" + arrow + "Inference", true
		}
	}

	return navigateMsg{}, "", false
}

// follow opens the place that fixes the blocker, when one exists.
func (d *distRecord) follow() tea.Cmd {
	to, _, ok := d.blockerLink()
	if !ok {
		return nil
	}

	return navigate(to.dest, to.intent)
}

// linkKey names the blocker link in the footer; disabled without one.
// A compact footer has room for a short label only, so it names the
// destination alone, as the sub-view keys do ("p proposals").
func (d *distRecord) linkKey() key.Binding {
	to, name, ok := d.blockerLink()

	b := withHelp(distKeys.Link, "o", "open "+name)

	switch {
	case d.wide():
	case to.dest == destOverview:
		b = withHelp(distKeys.Link, "o", "overview")
	default:
		b = withHelp(distKeys.Link, "o", "settings")
	}

	b.SetEnabled(ok)

	return b
}

// close leaves the distillation from a step without work. A draft goes
// away, and the view returns to Evidence, where the workflow starts. A
// record with a result stays in the session list.
func (d *distRecord) close() tea.Cmd {
	return tea.Batch(closeRun(d), navigate(destLessons, "evidence"))
}

// reviewKey handles the plan review. y starts only a ready preview;
// enter plans again only when no preview can start.
func (d *distRecord) reviewKey(msg tea.KeyMsg) tea.Cmd {
	pl := &d.plan

	if pl.cleanAsk {
		switch {
		case key.Matches(msg, distKeys.Start):
			return d.cleanup()
		case key.Matches(msg, nkeys.Back):
			pl.cleanAsk = false
		default:
			pl.cleanSc.key(msg)
		}

		return nil
	}

	if pl.cleaning {
		return nil
	}

	switch {
	case key.Matches(msg, distKeys.Start):
		return d.start()
	case key.Matches(msg, distKeys.Link):
		return d.follow()
	case key.Matches(msg, distKeys.Back):
		d.backToScope(ToneNeutral, "")
	case key.Matches(msg, nkeys.Back):
		return d.close()
	case key.Matches(msg, nkeys.Enter):
		if !d.runnable() {
			return d.startPlan()
		}
	case key.Matches(msg, distKeys.Cleanup):
		if d.cleanable() {
			pl.cleanAsk = true
			pl.cleanSc = scrollState{}
		}
	case key.Matches(msg, nkeys.Next, nkeys.Prev):
		switch {
		case !d.hasGroups():
		case d.wide():
			pl.planFocus = !pl.planFocus
		default:
			pl.groupsOpen = !pl.groupsOpen
		}
	default:
		d.reviewMove(msg)
	}

	return nil
}

// planKeys reports that the movement keys scroll the Plan pane: it has
// the focus in the wide layout, or it shows in the compact layout.
func (d *distRecord) planKeys() bool {
	if !d.hasGroups() {
		return true
	}

	if d.wide() {
		return d.plan.planFocus
	}

	return !d.plan.groupsOpen
}

// cleanable reports a cleanup-only plan whose cleanup did not run yet.
func (d *distRecord) cleanable() bool {
	p := d.plan.preview

	return d.plan.err == nil && p != nil && p.CleanupOnly() && !d.plan.cleaned && !d.plan.cleaning
}

// hasGroups reports a preview with groups to review.
func (d *distRecord) hasGroups() bool {
	p := d.plan.preview

	return d.plan.err == nil && p != nil && p.Plan != nil && len(p.Plan.Groups) > 0
}

// reviewMove moves the group cursor or scrolls the focused pane.
func (d *distRecord) reviewMove(msg tea.KeyMsg) {
	pl := &d.plan

	if d.planKeys() {
		pl.notes.key(msg)

		return
	}

	if distCursorKey(msg, &pl.cursor, len(pl.preview.Plan.Groups)) {
		pl.follow = true

		return
	}

	if pl.groups.key(msg) {
		pl.follow = false
	}
}

// runKey handles the running record: x asks before it stops.
func (d *distRecord) runKey(msg tea.KeyMsg) tea.Cmd {
	r := &d.run

	if r.stopAsk {
		switch {
		case key.Matches(msg, distKeys.Start):
			return d.stop()
		case key.Matches(msg, nkeys.Back):
			r.stopAsk = false
		}

		return nil
	}

	switch {
	case key.Matches(msg, distKeys.Stop):
		if !d.stopping() {
			r.stopAsk = true
		}
	default:
		d.runMove(msg)
	}

	return nil
}

// resultKey handles the result. enter opens the proposals of this run,
// and n plans the remaining work again from the current state. Nothing
// repeats the run.
func (d *distRecord) resultKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, nkeys.Enter):
		if len(d.newIDs()) > 0 {
			return navigate(destLessons, "proposals:run")
		}
	case key.Matches(msg, distKeys.Back):
		return openRun(newDistillFrom(d.st, d.plan.req), true)
	case key.Matches(msg, nkeys.Back):
		return navigate(destLessons, "evidence")
	default:
		d.runMove(msg)
	}

	return nil
}

// runMove moves the group cursor, scrolls the focused pane, or changes
// the pane with tab.
func (d *distRecord) runMove(msg tea.KeyMsg) {
	r := &d.run

	if key.Matches(msg, nkeys.Next, nkeys.Prev) {
		if d.wide() {
			r.sumFocus = !r.sumFocus
		} else {
			r.detail = !r.detail
		}

		return
	}

	if d.wide() && r.sumFocus {
		r.summary.key(msg)

		return
	}

	if distCursorKey(msg, &r.cursor, len(d.outcomes())) {
		r.follow, r.moved = true, true

		return
	}

	if r.groups.key(msg) {
		r.follow = false
	}
}

// distCursorKey moves a cursor over n rows with the arrow keys and
// reports whether the key was one of them.
func distCursorKey(msg tea.KeyMsg, cursor *int, n int) bool {
	switch {
	case key.Matches(msg, nkeys.Up):
		*cursor = max(0, *cursor-1)
	case key.Matches(msg, nkeys.Down):
		*cursor = max(0, min(n-1, *cursor+1))
	default:
		return false
	}

	return true
}

// wide reports the two-pane layout. The terminal width decides it, as
// the other Lessons sub-views do.
func (d *distRecord) wide() bool {
	return d.st.width >= wideWidth
}

// keys returns the footer keys of the current step. The Run sub-view
// adds the sub-view switch keys while the record does not capture.
func (d *distRecord) keys() []key.Binding {
	switch d.step {
	case distScope:
		return d.scopeKeys()
	case distPlanning:
		return []key.Binding{withHelp(nkeys.Back, "esc", "cancel")}
	case distReview:
		return d.reviewKeys()
	case distRunning:
		return d.runKeys()
	default:
		return d.resultKeys()
	}
}

// scopeKeys names the form keys. enter vanishes while the form cannot
// plan; the form says why. The step action comes first, because a
// narrow footer drops hints from the end. At 60 columns only one short
// hint fits.
func (d *distRecord) scopeKeys() []key.Binding {
	f := &d.form

	if f.editRegion || f.editCustom {
		return []key.Binding{withHelp(nkeys.Enter, "enter", "done"), withHelp(nkeys.Back, "esc", "cancel")}
	}

	if f.about && !d.wide() {
		return []key.Binding{withHelp(distKeys.About, "i", "back to the form")}
	}

	plan := withHelp(nkeys.Enter, "enter", "review plan")
	if !d.wide() {
		plan = withHelp(nkeys.Enter, "enter", "plan")
	}

	_, reason := d.request()
	plan.SetEnabled(reason == "")

	about := withHelp(distKeys.About, "i", "about")
	about.SetEnabled(!d.wide())

	return []key.Binding{
		plan, d.linkKey(), withHelp(nkeys.Down, "↑↓", "move"), withHelp(nkeys.Toggle, "space", "choose"),
		withHelp(distKeys.Region, "/", "region"), about, withHelp(nkeys.Back, "esc", "close"),
	}
}

// reviewKeys names the keys of the plan review for its state.
func (d *distRecord) reviewKeys() []key.Binding {
	pl := &d.plan

	switch {
	case pl.cleanAsk:
		return []key.Binding{withHelp(distKeys.Start, "y", "remove"), withHelp(nkeys.Back, "esc", "keep")}
	case pl.cleaning:
		return nil
	case d.runnable():
		move, pane := withHelp(nkeys.Down, "↑↓", "group"), withHelp(nkeys.Next, "tab", "plan")
		if d.planKeys() {
			move, pane = withHelp(nkeys.Down, "↑↓", "scroll"), withHelp(nkeys.Next, "tab", "groups")
		}

		// y comes first, so it survives when a narrow footer drops
		// hints from the end.
		return []key.Binding{
			withHelp(distKeys.Start, "y", "start run"), withHelp(distKeys.Back, "n", "back"), pane, move,
			withHelp(nkeys.PageDown, "pgup/pgdn", "scroll"), withHelp(nkeys.Back, "esc", "cancel"),
		}
	case d.cleanable():
		return []key.Binding{
			withHelp(distKeys.Cleanup, "c", "clean up"), withHelp(distKeys.Back, "n", "back"),
			withHelp(nkeys.Back, "esc", "close"),
		}
	}

	scroll := withHelp(nkeys.Down, "↑↓", "scroll")
	scroll.SetEnabled(pl.notes.scrolls())

	again := withHelp(nkeys.Enter, "enter", "plan again")
	if !d.wide() {
		again = withHelp(nkeys.Enter, "enter", "plan")
	}

	return []key.Binding{
		again, d.linkKey(), withHelp(distKeys.Back, "n", "back"), withHelp(nkeys.Back, "esc", "close"), scroll,
	}
}

// runKeys names the keys of the running record. x comes first, so the
// stop key of a paid run survives a narrow footer: at 60 columns only
// one short hint fits (design map §6).
func (d *distRecord) runKeys() []key.Binding {
	if d.run.stopAsk {
		return []key.Binding{withHelp(distKeys.Start, "y", "stop the run"), withHelp(nkeys.Back, "esc", "keep running")}
	}

	stop := withHelp(distKeys.Stop, "x", "stop")
	stop.SetEnabled(!d.stopping())

	return []key.Binding{stop, withHelp(nkeys.Down, "↑↓", "group"), d.paneKey()}
}

// resultKeys names the keys of the result. enter shows only with new
// proposals to review. A compact footer has room for a short label only;
// the NEXT block in the box keeps the whole sentence.
func (d *distRecord) resultKeys() []key.Binding {
	review := withHelp(nkeys.Enter, "enter", "review proposals")
	if !d.wide() {
		review = withHelp(nkeys.Enter, "enter", "review")
	}

	review.SetEnabled(len(d.newIDs()) > 0)

	return []key.Binding{
		review, withHelp(distKeys.Back, "n", "plan again"), withHelp(nkeys.Down, "↑↓", "group"), d.paneKey(),
		withHelp(nkeys.Back, "esc", "close"),
	}
}

// paneKey names what tab shows next in the run and result views.
func (d *distRecord) paneKey() key.Binding {
	switch {
	case d.wide():
		return withHelp(nkeys.Next, "tab", "pane")
	case d.run.detail:
		return withHelp(nkeys.Next, "tab", "summary")
	default:
		return withHelp(nkeys.Next, "tab", "groups")
	}
}
