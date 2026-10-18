package tui

import (
	"errors"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/ui"
)

// This file handles the keys of a distillation record and names them in
// the footer. The paid start and the cleanup take y from a preview,
// never enter (contracts §3.3).

// distKeys are the letter keys of a distillation record that an
// extraction does not bind; agentKeys holds the shared letters. The
// other keys come from ui.Nav, so every list moves the same way. The
// switch keys of Lessons (p, m, s, e, u) stay free, because Lessons
// switches its sub-views with them.
var distKeys = struct {
	Region, Back, Cleanup, About key.Binding
}{
	Region:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "region")),
	Back:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "back")),
	Cleanup: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "clean up")),
	About:   key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "about")),
}

// key routes a key press by step.
func (d *distRecord) key(msg tea.KeyMsg) tea.Cmd {
	switch d.step {
	case stepScope:
		return d.scopeKey(msg)
	case stepPreparing:
		if key.Matches(msg, ui.Nav.Back, distKeys.Back) {
			d.backToScope(ui.ToneNeutral, "")
		}
	case stepPreview:
		return d.previewKey(msg)
	case stepRunning:
		return d.runKey(msg)
	case stepResult:
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
	if d.st.wide() {
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
		case key.Matches(msg, ui.Nav.Back):
			f.limit = f.limitWas
		case key.Matches(msg, ui.Nav.Enter):
			f.customChecked = true
		}

		return d.editKey(msg, &f.custom, &f.customWas, &f.editCustom)
	case f.about:
		if key.Matches(msg, ui.Nav.Back, distKeys.About) {
			f.about = false
		} else {
			f.aboutSc.Key(msg)
		}

		return nil
	}

	switch {
	case key.Matches(msg, agentKeys.Link):
		return d.follow()
	case key.Matches(msg, ui.Nav.Up):
		f.row = max(0, f.row-1)
	case key.Matches(msg, ui.Nav.Down):
		f.row = min(distRowCount-1, f.row+1)
	case key.Matches(msg, distKeys.Region):
		d.editRegion()
	case key.Matches(msg, ui.Nav.Toggle):
		d.choose()
	case key.Matches(msg, ui.Nav.Enter):
		return d.startPlan()
	case key.Matches(msg, ui.Nav.Back):
		return d.close()
	case key.Matches(msg, distKeys.About):
		f.about = !d.st.wide()
		f.aboutSc = ui.ScrollState{}
	}

	return nil
}

// editKey handles an open input: enter keeps the value, esc restores
// the value from before the edit.
func (d *distRecord) editKey(msg tea.KeyMsg, in *ui.LineInput, was *string, open *bool) tea.Cmd {
	switch {
	case key.Matches(msg, ui.Nav.Enter):
		*open = false
	case key.Matches(msg, ui.Nav.Back):
		in.Set(*was)
		*open = false
	default:
		in.Key(msg)
	}

	return nil
}

// editRegion opens the region input.
func (d *distRecord) editRegion() {
	f := &d.form
	f.row = distRowRegion
	f.regionWas = f.region.Value()
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
		f.customWas = f.custom.Value()
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
	case stepScope:
		if d.agentBlock() != "" {
			return inference, "Settings" + arrow + "Inference", true
		}
	case stepPreview:
		var cfg *learning.DistillConfigError

		switch err := d.prev.err; {
		case errors.As(err, &cfg) && cfg.Path == settingsFile:
			return inference, "Settings" + arrow + "Inference", true
		case errors.Is(err, learning.ErrDistillNotReady):
			return navigateMsg{dest: destOverview, intent: ovIntentBuild}, "Overview" + arrow + "build", true
		case err == nil && d.prev.preview != nil && len(d.prev.preview.Blockers) > 0:
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

	b := ui.WithHelp(agentKeys.Link, "o", "open "+name)

	switch {
	case d.st.wide():
	case to.dest == destOverview:
		b = ui.WithHelp(agentKeys.Link, "o", "overview")
	default:
		b = ui.WithHelp(agentKeys.Link, "o", "settings")
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

// previewKey handles the Preview step. y starts only a ready preview;
// enter plans again only when no preview can start.
func (d *distRecord) previewKey(msg tea.KeyMsg) tea.Cmd {
	pl := &d.prev

	if d.clean.ask {
		switch {
		case key.Matches(msg, agentKeys.Start):
			return d.cleanup()
		case key.Matches(msg, ui.Nav.Back):
			d.clean.ask = false
		default:
			d.clean.scroll.Key(msg)
		}

		return nil
	}

	if d.clean.running {
		return nil
	}

	switch {
	case key.Matches(msg, agentKeys.Start):
		return d.start()
	case key.Matches(msg, agentKeys.Link):
		return d.follow()
	case key.Matches(msg, distKeys.Back):
		d.backToScope(ui.ToneNeutral, "")
	case key.Matches(msg, ui.Nav.Back):
		return d.close()
	case key.Matches(msg, ui.Nav.Enter):
		if !d.runnable() {
			return d.startPlan()
		}
	case key.Matches(msg, distKeys.Cleanup):
		if d.cleanable() {
			d.clean.ask = true
			d.clean.scroll = ui.ScrollState{}
		}
	case key.Matches(msg, ui.Nav.Next, ui.Nav.Prev):
		switch {
		case !d.hasGroups():
		case d.st.wide():
			pl.planFocus = !pl.planFocus
		default:
			pl.listOpen = !pl.listOpen
		}
	default:
		d.previewMove(msg)
	}

	return nil
}

// planKeys reports that the movement keys scroll the Plan pane: it has
// the focus in the wide layout, or it shows in the compact layout.
func (d *distRecord) planKeys() bool {
	if !d.hasGroups() {
		return true
	}

	if d.st.wide() {
		return d.prev.planFocus
	}

	return !d.prev.listOpen
}

// cleanable reports a cleanup-only plan whose cleanup did not run yet.
func (d *distRecord) cleanable() bool {
	p := d.prev.preview

	return d.prev.err == nil && p != nil && p.CleanupOnly() && !d.clean.done && !d.clean.running
}

// hasGroups reports a preview with groups to review.
func (d *distRecord) hasGroups() bool {
	p := d.prev.preview

	return d.prev.err == nil && p != nil && p.Plan != nil && len(p.Plan.Groups) > 0
}

// previewMove moves the group cursor or scrolls the focused pane.
func (d *distRecord) previewMove(msg tea.KeyMsg) {
	pl := &d.prev

	if d.planKeys() {
		pl.notes.Key(msg)

		return
	}

	if cursorKey(msg, &pl.cursor, len(pl.preview.Plan.Groups)) {
		pl.follow = true

		return
	}

	if pl.list.Key(msg) {
		pl.follow = false
	}
}

// runKey handles the running record: x asks before it stops.
func (d *distRecord) runKey(msg tea.KeyMsg) tea.Cmd {
	r := &d.run

	if r.stopAsk {
		switch {
		case key.Matches(msg, agentKeys.Start):
			return d.stop()
		case key.Matches(msg, ui.Nav.Back):
			r.stopAsk = false
		}

		return nil
	}

	switch {
	case key.Matches(msg, agentKeys.Stop):
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
	case key.Matches(msg, ui.Nav.Enter):
		if len(d.newIDs()) > 0 {
			return navigate(destLessons, "proposals:run")
		}
	case key.Matches(msg, distKeys.Back):
		return openRun(newDistillFrom(d.st, d.req), true)
	case key.Matches(msg, ui.Nav.Back):
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

	if key.Matches(msg, ui.Nav.Next, ui.Nav.Prev) {
		if d.st.wide() {
			r.sumFocus = !r.sumFocus
		} else {
			r.detail = !r.detail
		}

		return
	}

	if d.st.wide() && r.sumFocus {
		r.summary.Key(msg)

		return
	}

	if cursorKey(msg, &r.cursor, len(d.outcomes())) {
		r.follow, r.moved = true, true

		return
	}

	if r.list.Key(msg) {
		r.follow = false
	}
}

// keys returns the footer keys of the current step. The Run sub-view
// adds the sub-view switch keys while the record does not capture.
func (d *distRecord) keys() []key.Binding {
	switch d.step {
	case stepScope:
		return d.scopeKeys()
	case stepPreparing:
		return []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	case stepPreview:
		return d.previewKeys()
	case stepRunning:
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
		return []key.Binding{ui.WithHelp(ui.Nav.Enter, "enter", "done"), ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	}

	if f.about && !d.st.wide() {
		return []key.Binding{ui.WithHelp(distKeys.About, "i", "back to the form")}
	}

	plan := ui.WithHelp(ui.Nav.Enter, "enter", "review plan")
	if !d.st.wide() {
		plan = ui.WithHelp(ui.Nav.Enter, "enter", "plan")
	}

	_, reason := d.request()
	plan.SetEnabled(reason == "")

	about := ui.WithHelp(distKeys.About, "i", "about")
	about.SetEnabled(!d.st.wide())

	return []key.Binding{
		plan, d.linkKey(), ui.WithHelp(ui.Nav.Down, "↑↓", "move"), ui.WithHelp(ui.Nav.Toggle, "space", "choose"),
		ui.WithHelp(distKeys.Region, "/", "region"), about, ui.WithHelp(ui.Nav.Back, "esc", "close"),
	}
}

// previewKeys names the keys of the Preview step for its state.
func (d *distRecord) previewKeys() []key.Binding {
	pl := &d.prev

	switch {
	case d.clean.ask:
		return []key.Binding{ui.WithHelp(agentKeys.Start, "y", "remove"), ui.WithHelp(ui.Nav.Back, "esc", "keep")}
	case d.clean.running:
		return nil
	case d.runnable():
		move, pane := ui.WithHelp(ui.Nav.Down, "↑↓", "group"), ui.WithHelp(ui.Nav.Next, "tab", "plan")
		if d.planKeys() {
			move, pane = ui.WithHelp(ui.Nav.Down, "↑↓", "scroll"), ui.WithHelp(ui.Nav.Next, "tab", "groups")
		}

		// y comes first, so it survives when a narrow footer drops
		// hints from the end.
		return []key.Binding{
			ui.WithHelp(agentKeys.Start, "y", "start run"), ui.WithHelp(distKeys.Back, "n", "back"), pane, move,
			ui.WithHelp(ui.Nav.PageDown, "pgup/pgdn", "scroll"), ui.WithHelp(ui.Nav.Back, "esc", "cancel"),
		}
	case d.cleanable():
		return []key.Binding{
			ui.WithHelp(distKeys.Cleanup, "c", "clean up"), ui.WithHelp(distKeys.Back, "n", "back"),
			ui.WithHelp(ui.Nav.Back, "esc", "close"),
		}
	}

	scroll := ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")
	scroll.SetEnabled(pl.notes.Scrolls())

	again := ui.WithHelp(ui.Nav.Enter, "enter", "plan again")
	if !d.st.wide() {
		again = ui.WithHelp(ui.Nav.Enter, "enter", "plan")
	}

	return []key.Binding{
		again, d.linkKey(), ui.WithHelp(distKeys.Back, "n", "back"), ui.WithHelp(ui.Nav.Back, "esc", "close"), scroll,
	}
}

// runKeys names the keys of the running record. x comes first, so the
// stop key of a paid run survives a narrow footer: at 60 columns only
// one short hint fits (design map §6).
func (d *distRecord) runKeys() []key.Binding {
	if d.run.stopAsk {
		return []key.Binding{ui.WithHelp(agentKeys.Start, "y", "stop the run"), ui.WithHelp(ui.Nav.Back, "esc", "keep running")}
	}

	stop := ui.WithHelp(agentKeys.Stop, "x", "stop")
	stop.SetEnabled(!d.stopping())

	return []key.Binding{stop, ui.WithHelp(ui.Nav.Down, "↑↓", "group"), d.paneKey()}
}

// resultKeys names the keys of the result. enter shows only with new
// proposals to review. A compact footer has room for a short label only;
// the NEXT block in the box keeps the whole sentence.
func (d *distRecord) resultKeys() []key.Binding {
	review := ui.WithHelp(ui.Nav.Enter, "enter", "review proposals")
	if !d.st.wide() {
		review = ui.WithHelp(ui.Nav.Enter, "enter", "review")
	}

	review.SetEnabled(len(d.newIDs()) > 0)

	return []key.Binding{
		review, ui.WithHelp(distKeys.Back, "n", "plan again"), ui.WithHelp(ui.Nav.Down, "↑↓", "group"), d.paneKey(),
		ui.WithHelp(ui.Nav.Back, "esc", "close"),
	}
}

// paneKey names what tab shows next in the run and result views.
func (d *distRecord) paneKey() key.Binding {
	switch {
	case d.st.wide():
		return ui.WithHelp(ui.Nav.Next, "tab", "pane")
	case d.run.detail:
		return ui.WithHelp(ui.Nav.Next, "tab", "summary")
	default:
		return ui.WithHelp(ui.Nav.Next, "tab", "groups")
	}
}
