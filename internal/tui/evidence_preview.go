package tui

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/tui/services"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds the evidence refresh preview (design map §3). The
// plan only reads: it runs local git and never gh, and it writes
// nothing. y in the preview starts the run; esc closes the preview and
// calls nothing.

// evPreview is one open refresh preview. It lives only while its modal
// is open, so nil closes it. Its flow never commits, because y starts a
// run record: the stage is flowPreparing or flowPreview.
type evPreview struct {
	previewFlow[learning.Plan]

	mode index.SourceMode
}

// evPlanMsg carries a refresh plan back to the event loop. seq drops the
// plan of a preview that the user closed meanwhile.
type evPlanMsg struct {
	seq  int
	plan *learning.Plan
	err  error
}

// plan opens the preparing preview and asks for the plan in the
// background. The plan is read-only, so it needs no mutation slot.
func (v *evidenceView) plan(mode index.SourceMode) tea.Cmd {
	seq := v.seq.start()
	v.prev = &evPreview{mode: mode}
	v.prev.startRead()

	var svc services.Learning = v.st.svc

	return func() tea.Msg {
		p, err := svc.PlanEvidence(mode)

		return evPlanMsg{seq: seq, plan: p, err: err}
	}
}

// planned shows a plan, unless the user closed its preview.
func (v *evidenceView) planned(msg evPlanMsg) tea.Cmd {
	if v.prev != nil {
		v.prev.readDone(v.seq.current(msg.seq), msg.plan, msg.err, "the refresh preview came back empty")
	}

	return nil
}

// closePreview drops the open preview and its pending plan. A non-empty
// reason becomes a message, so a preview never disappears without one.
func (v *evidenceView) closePreview(reason string) tea.Cmd {
	if v.prev == nil {
		return nil
	}

	v.seq.cancel()
	v.prev = nil

	if reason == "" {
		return nil
	}

	return toast(ui.ToneAttention, reason)
}

// settingsSaved drops the open preview after any settings save. The
// plan binds the bytes of the whole config.yaml (decision D-9). Thus a
// save of any group makes the plan stale, not only the evidence group.
func (v *evidenceView) settingsSaved(msg settingsSavedMsg) tea.Cmd {
	return v.closePreview("Settings " + v.st.theme.Glyphs.Arrow + " " + msg.group.String() + " changed " +
		settingsFile + ", so the refresh preview closed. Prepare it again.")
}

// learningLoaded drops a ready preview when a new evidence snapshot
// shows another state than the plan saw, for example after another
// refresh ended. The run would refuse the plan anyway; the early close
// keeps the user from confirming old facts.
func (v *evidenceView) learningLoaded() tea.Cmd {
	p, sec := v.prev, &v.st.learning
	if p == nil || p.stage == flowPreparing || p.preview == nil || sec.err != nil || !sec.loaded {
		return nil
	}

	if evSameObservation(p.preview.Current, sec.value) {
		return nil
	}

	return v.closePreview("The evidence changed after the preview, so the preview closed. Prepare the " +
		"refresh again.")
}

// previewKey handles a key of the open preview. Only y starts, and only
// a ready plan while the mutation slot is free. enter follows the link
// of a blocked plan; it never starts work.
func (v *evidenceView) previewKey(msg tea.KeyMsg) tea.Cmd {
	p := v.prev

	switch {
	case key.Matches(msg, ui.Nav.Back):
		return v.closePreview("")
	case p.stage == flowPreparing:
		return nil
	case key.Matches(msg, lesKeys.Confirm):
		return v.start()
	case key.Matches(msg, ui.Nav.Enter):
		if _, cmd, ok := v.previewLink(); ok {
			v.closePreview("")

			return cmd
		}
	default:
		p.scroll.Key(msg)
	}

	return nil
}

// ready reports a plan without blockers. y can start it once the
// mutation slot is free.
func (v *evidenceView) ready() bool {
	p := v.prev

	return p != nil && p.showsPreview() && p.preview.Ready()
}

// confirmable reports a ready plan while the mutation slot is free. The
// y action of the preview uses it, as the other confirm screens do.
func (v *evidenceView) confirmable() bool {
	return v.ready() && !v.st.busy()
}

// previewLink returns the route that fixes the first blocker of the
// plan. An index blocker goes to the index build. A config.yaml
// blocker goes to the evidence group of Settings.
func (v *evidenceView) previewLink() (key.Binding, tea.Cmd, bool) {
	p := v.prev
	if p == nil || p.stage == flowPreparing || p.preview == nil || p.preview.Ready() {
		return key.Binding{}, nil, false
	}

	switch b := p.preview.Blockers[0]; b.Kind {
	case learning.BlockIndex:
		return v.blockedLink(learning.Snapshot{Probe: p.preview.Probe})
	case learning.BlockConfig:
		return ui.WithHelp(ui.Nav.Enter, "enter", "open Settings "+v.st.theme.Glyphs.Arrow+" evidence"),
			navigate(destSettings, evSettingsIntent), true
	}

	return key.Binding{}, nil, false
}

// previewKeys returns the footer keys of the preview. They mirror the
// modal actions with shorter labels.
func (v *evidenceView) previewKeys() []key.Binding {
	p := v.prev
	if p.stage == flowPreparing {
		return []key.Binding{ui.WithHelp(ui.Nav.Back, "esc", "cancel")}
	}

	scroll := ui.WithHelp(ui.Nav.Down, "↑↓", "scroll")
	scroll.SetEnabled(p.scroll.Scrolls())

	out := v.previewActions()

	return append(out, scroll)
}

// previewActions returns the keys under the rule of the preview.
func (v *evidenceView) previewActions() []key.Binding {
	var out []key.Binding

	if v.ready() {
		start := ui.WithHelp(lesKeys.Confirm, "y", "refresh")
		start.SetEnabled(v.confirmable())
		out = append(out, start)
	}

	if link, _, ok := v.previewLink(); ok {
		out = append(out, link)
	}

	back := "close"
	if v.ready() {
		back = "cancel"
	}

	return append(out, ui.WithHelp(ui.Nav.Back, "esc", back))
}

// modal renders the open preview for the shell, which dims the frame
// under it.
func (v *evidenceView) modal(w, h int) (string, bool) {
	if v.prev == nil {
		return "", false
	}

	return v.st.theme.Modal(v.previewContent(ui.ModalBodyWidth(ui.ModalMinW, w)), &v.prev.scroll, w, h), true
}

// previewContent builds the modal of the current preview stage in w
// cells. The right border names the mode, and a ready plan names its
// network use there instead.
func (v *evidenceView) previewContent(w int) ui.ModalOpts {
	t := v.st.theme
	p := v.prev
	mode := t.Muted.Render(evModeTitle(p.mode))

	switch {
	case p.stage == flowPreparing:
		o := flowWaitModal(v.st, "Preparing the refresh preview", "Seamark reads the index, "+settingsFile+
			" and the origin remote. Nothing is fetched or written.", true, w)
		o.Right = mode

		return o
	case p.err != nil:
		o := previewFailedModal(t, "The refresh preview", p.err, "", "Nothing was changed.", v.previewActions(), w)
		o.Right = mode

		return o
	case !p.preview.Ready():
		return ui.ModalOpts{Title: t.Attention.Render("Refresh blocked"), Right: mode,
			Body: v.blockedLines(p.preview, w), Actions: v.previewActions()}
	}

	right := t.Muted.Render("local only")
	if evFetches(p.preview) {
		right = t.Attention.Render("network")
	}

	return ui.ModalOpts{
		Title: t.TabActive.Render("Refresh") + " " + t.Body.Render(evModeTitle(p.mode)),
		Right: right, Body: v.readyLines(p.preview, w), Actions: v.previewActions(),
	}
}

// evFetches reports a plan that can contact GitHub: the review mode
// with a GitHub origin. It ignores gh on purpose. The plan does not bind
// the gh path (decision D-9), and the run looks for gh again when it
// starts. So gh missing now never becomes a promise of no network.
func evFetches(p *learning.Plan) bool {
	return p.Network
}

// evGHMissing reports a plan that can contact GitHub while gh is not on
// PATH now. The fetch then depends on gh at the start, so the preview
// states that condition instead of a result.
func evGHMissing(p *learning.Plan) bool {
	return evFetches(p) && p.GHPath == ""
}

// evNetworkFact is the network row of the preview and of the run
// record. The record repeats the words that the user confirmed.
func evNetworkFact(p *learning.Plan) string {
	switch {
	case !evFetches(p):
		return "no"
	case evGHMissing(p):
		return "yes, if gh is on PATH at the start: the GitHub API through gh"
	default:
		return "yes: the GitHub API through gh"
	}
}

// blockedLines explains each blocker of a plan and its route. The plan
// discloses; it never runs, so nothing changed.
func (v *evidenceView) blockedLines(p *learning.Plan, w int) []string {
	t := v.st.theme

	out := propLines(t.Body, "This refresh cannot start. Nothing was fetched or written.", w)

	for _, b := range p.Blockers {
		out = append(out, "")
		out = append(out, propNote(t, ui.ToneAttention, ui.Diag(b.Reason), w)...)

		if b.Path != "" {
			out = append(out, integHanging(t.Muted.Render("file  "), t.Code, ui.OneLine(ui.DisplayPath(b.Path)), w)...)
		}

		switch b.Kind {
		case learning.BlockIndex:
			route := "Build or repair the index from Overview. A refresh never builds, migrates or rewrites " +
				"the index."
			if p.Probe.State == workspace.IndexNewer {
				route = "A newer seamark wrote this index. This binary never rewrites it."
			}

			out = append(out, propLines(t.Muted, route, w)...)
		case learning.BlockConfig:
			out = append(out, propLines(t.Muted, "Fix the file in Settings "+t.Glyphs.Arrow+" evidence or in an "+
				"editor. The console never falls back to a default for a network run.", w)...)
		}
	}

	return out
}

// readyLines renders a ready plan: the lead, the facts, what changes,
// what stays, what can follow later, and how to stop.
func (v *evidenceView) readyLines(p *learning.Plan, w int) []string {
	t := v.st.theme

	out := propLines(t.Body, evPreviewLead(p), w)
	out = append(out, "")
	out = append(out, v.previewFacts(p, w)...)

	will, wont, later := evEffects(t, p)
	out = append(out, effectLines(t, will, wont, w)...)

	if len(later) > 0 {
		out = append(out, "", t.Heading.Render("LATER"))

		for _, text := range later {
			out = append(out, propIndent(t.Faint.Render(t.Glyphs.Sep)+" ", propLines(t.Muted, text, w-2))...)
		}
	}

	if v.st.busy() {
		out = append(out, "")
		out = append(out, propNote(t, ui.ToneAttention, ui.OneLine(v.st.mutation)+" is running. The refresh can "+
			"start after it ends; y is off until then.", w)...)
	}

	tail := "The local fix mine cannot stop part way. x in Lessons " + t.Glyphs.Arrow + " Run stops the " +
		"refresh after it."
	if evFetches(p) {
		tail = "Counts from GitHub are known only after the fetch. x in Lessons " + t.Glyphs.Arrow + " Run " +
			"stops the fetch; a partial fetch is never saved."
	}

	out = append(out, "")

	return append(out, propLines(t.Faint, tail, w)...)
}

// evPreviewLead is the first paragraph of a ready preview. A ready plan
// reads HEAD, so an empty Head means that no commit exists. The fix
// mine then has nothing to read, and the lead must not promise it.
func evPreviewLead(p *learning.Plan) string {
	if p.Head == "" {
		return evNoCommitLead(p)
	}

	switch {
	case p.Mode != index.SourcesReviewsAndLocal:
		return "Seamark mines the fix commits that are reachable from HEAD. This refresh makes no network " +
			"call and keeps the stored review findings. Nothing runs before you press y."
	case !p.Network:
		return "The origin remote names no GitHub repository, so the review fetch cannot run. Seamark mines " +
			"the local fix commits only, and the review source reads unavailable. Nothing runs before you " +
			"press y."
	case p.GHPath == "":
		return "Seamark mines the local fix commits first. gh is not on PATH now. If gh is on PATH when the " +
			"refresh starts, Seamark then fetches the review comments through it; else the review source reads " +
			"unavailable. Nothing runs before you press y."
	default:
		return "Seamark mines the local fix commits first, then fetches the review comments through gh. The " +
			"two sources succeed or fail on their own. Nothing runs before you press y."
	}
}

// evNoCommitLead is the lead of a plan without a commit. The local fix
// source reads unavailable, so only a review fetch can change evidence.
func evNoCommitLead(p *learning.Plan) string {
	const noCommit = "No commit is reachable from HEAD, so the local fix source reads unavailable and keeps " +
		"the stored fix findings. "

	switch {
	case p.Mode != index.SourcesReviewsAndLocal:
		return noCommit + "This refresh makes no network call and keeps the stored review findings. Nothing " +
			"runs before you press y."
	case !p.Network:
		return noCommit + "The origin remote names no GitHub repository, so the review fetch cannot run " +
			"either. Nothing runs before you press y."
	case p.GHPath == "":
		return noCommit + "gh is not on PATH now. If gh is on PATH when the refresh starts, Seamark fetches " +
			"the review comments through it; else the review source reads unavailable. Nothing runs before " +
			"you press y."
	default:
		return noCommit + "Seamark fetches the review comments through gh. Nothing runs before you press y."
	}
}

// previewFacts renders the labelled facts that the confirmation binds:
// the workspace, the database, the GitHub target, gh, the window, the
// history and the network use.
func (v *evidenceView) previewFacts(p *learning.Plan, w int) []string {
	t := v.st.theme

	row := func(label, value string, style lipgloss.Style) []string {
		return integHanging(t.Muted.Render(ui.PadRight(label, evLabelW)), style, value, w)
	}

	db := p.Probe.Path
	if db == "" {
		db = p.Workspace.DBPath
	}

	out := row("Workspace", ui.OneLine(ui.DisplayPath(p.Root)), t.Code)
	out = append(out, row("Database", ui.OneLine(ui.DisplayPath(db)), t.Code)...)

	if p.Mode == index.SourcesReviewsAndLocal {
		if repo, ok := evRepo(p.Repository, p.RepoState); ok {
			out = append(out, row("Repository", repo, t.Code)...)
		} else {
			out = append(out, row("Repository", repo, t.Attention)...)
		}

		// The run looks for gh again at the start, so the row names the
		// condition, not a result.
		if p.GHPath == "" {
			out = append(out, row("Access", "gh not found on PATH now. The refresh looks for gh again when it "+
				"starts: with gh it fetches, without gh the review source reads unavailable.", t.Attention)...)
		} else {
			out = append(out, row("Access", evGH(p.GHPath), t.Body)...)
			out = append(out, row("", t.Glyphs.Unknown+" login not checked: only the fetch proves access",
				t.Unknown)...)
		}

		if text, ok := evWindow(p.Window); ok {
			if note := evFloorNote(p.Window); note != "" {
				text += " " + t.Glyphs.Sep + " " + note
			}

			out = append(out, row("Window", text, t.Body)...)
		}
	}

	history := "fix commits of the last " + propPlural(p.FixWindowDays, "day", "days") + " reachable from HEAD"
	if head := ui.OneLine(p.Head); head != "" {
		history += " " + head[:min(len(head), 7)]
	} else {
		history = "no commit yet: the local fix source reads unavailable"
	}

	out = append(out, row("History", history, t.Body)...)

	return append(out, row("Network", evNetworkFact(p), t.Body)...)
}

// evEffects turns the typed effects of a plan into the WILL, WON'T and
// LATER lines. The known source and kind pairs get their own words with
// the current counts. Any other pair keeps the plan text, so a new
// backend effect never goes missing.
func evEffects(t *ui.Theme, p *learning.Plan) (will []effect, wont, later []string) {
	cur := p.Current
	known := cur.CountsErr == nil
	fetches := evFetches(p)

	stored := func(n int, what string) string {
		if !known {
			return "the stored " + what
		}

		return "the " + strconv.Itoa(n) + " stored " + what
	}

	if fetches {
		wont = append(wont, "contact GitHub before you press y")
	} else {
		wont = append(wont, "contact GitHub")
	}

	for _, e := range p.Affects {
		switch {
		case e.Kind == learning.EffectLater:
			later = append(later, ui.OneLine(e.Text))
		case e.Source == index.SourceFixes && e.Kind == learning.EffectReplace:
			will = append(will, effect{glyph: "~", tone: ui.ToneAttention,
				text: "replace " + stored(cur.FixFindings, "local fix findings") + " with a new mine of the " +
					"history reachable from HEAD"})
		case e.Source == index.SourceReviews && e.Kind == learning.EffectReplace && fetches:
			// The fetch runs after the fix phase, so its line follows the
			// fix line, in the engine order.
			fetch := "fetch the review comments of " + evRepoShort(p.Repository, p.RepoState) + " through gh"
			if evGHMissing(p) {
				fetch += ", if gh is on PATH at the start"
			}

			will = append(will,
				effect{glyph: t.Glyphs.Add, tone: ui.ToneSuccess, text: fetch},
				effect{glyph: "~", tone: ui.ToneAttention,
					text: "replace the stored review findings and lessons, only after a complete fetch"})
			wont = append(wont, "keep a partial fetch: a failed, unavailable or stopped fetch keeps "+
				stored(cur.ReviewFindings, "review findings"))
		case e.Source == index.SourceFixes && e.Kind == learning.EffectKeep:
			// Without a commit the fix mine reads unavailable, so the
			// stored fix findings stay.
			wont = append(wont, "change "+stored(cur.FixFindings, "local fix findings")+
				": no commit is reachable from HEAD")
		case e.Source == index.SourceReviews:
			wont = append(wont, "change "+stored(cur.ReviewFindings, "review findings")+" or the review lessons")
		case e.Source == "" && e.Kind == learning.EffectKeep:
			wont = append(wont, "change proposals, pins or the record of distilled evidence groups")
		case e.Kind == learning.EffectReplace:
			will = append(will, effect{glyph: "~", tone: ui.ToneAttention, text: ui.OneLine(e.Text)})
		default:
			wont = append(wont, ui.OneLine(e.Text))
		}
	}

	wont = append(wont, "run the agent or create proposals")

	return will, wont, later
}
