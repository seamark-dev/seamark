package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/learning"
	domain "github.com/seamark-dev/seamark/internal/model"
)

// This file renders the plan review of a distillation (design map §5).
// The Plan pane says what WILL and WON'T happen. The Groups pane lists
// the groups in engine order. The file also renders the plan failures
// and "Nothing new to distill" with its separately confirmed cleanup.

// Column widths of the Groups pane of the review.
const (
	distIdxW  = 4
	distFindW = 9
	distTokW  = 15
	// distEvPad indents the evidence rows under their group.
	distEvPad = 6
	// distPlanW is the Plan pane at the 120-column design size. It is
	// wider than the Setup plan, because the notes of a paid run are
	// longer and must show without scrolling where possible.
	distPlanW = 50
	// distIDMinW is the narrowest finding ID column: "#" and five digits
	// with a gap. Longer IDs widen the column.
	distIDMinW = 7
	// distPathMinW is the path room that a long ID leaves at least. The
	// source column gives way first.
	distPathMinW = 12
)

// reviewView renders the planning wait, a plan failure, the empty plan,
// or the plan with its groups.
func (d *distRecord) reviewView(w, h int) string {
	t := d.st.theme
	pl := &d.plan

	switch {
	case d.step == distPlanning:
		o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

		return integBoxed(t, o, ToneNeutral, "Planning the distillation"+t.Glyphs.Ellipsis,
			"Seamark reads the index, the pins and the agent configuration. Nothing is sent or written.",
			t.hints([]key.Binding{withHelp(nkeys.Back, "esc", "back to the scope")}))
	case pl.err != nil:
		return d.planErrBox(w, h)
	case pl.preview.Plan == nil || len(pl.preview.Plan.Groups) == 0:
		return d.nothingBox(w, h)
	}

	if d.wide() {
		leftW := max(34, setupSplit(w, distPlanW))

		return hjoin(1, d.planBox(leftW, h, pl.planFocus), d.groupsBox(w-leftW-1, h, !pl.planFocus))
	}

	if pl.groupsOpen {
		return d.groupsBox(w, h, true)
	}

	return d.planBox(w, h, true)
}

// planErrBox explains a plan that could not be made. Each cause names
// its route: Settings or the file for a configuration error, the index
// build for an index that is not ready. Nothing was sent or written.
func (d *distRecord) planErrBox(w, h int) string {
	t := d.st.theme
	err := d.plan.err
	o := boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Plan", true)}

	title := "Could not plan the distillation"
	body := errText(err) + "\n\nNothing was sent or written."

	// A configuration or an index that blocks the plan is a blocked
	// state, not a failure (design map §9), so it takes the attention
	// color. Other errors keep the failure color.
	tone := ToneFailure

	var cfg *learning.DistillConfigError

	switch {
	case errors.As(err, &cfg):
		// Settings edits config.yaml only; lessons.yaml has no form.
		route := "Edit " + oneLine(cfg.Path)
		if cfg.Path == settingsFile {
			route = "Press o to fix it in Settings, or edit " + oneLine(cfg.Path)
		}

		title, tone = "The configuration blocks the plan", ToneAttention
		body = errText(err) + "\n\nThe console does not fall back to defaults for a paid run. " + route +
			", then plan again. Nothing was sent or written."
	case errors.Is(err, learning.ErrDistillNotReady):
		title, tone = "The index is not ready", ToneAttention
		body = errText(err) + "\n\nPress o to build it from Overview, then plan again. A distillation " +
			"never builds the index by itself. Nothing was sent or written."
	case errors.Is(err, distill.ErrStale):
		title = "The workspace changed while planning"
		body = errText(err) + "\n\nPlan again to read the current state. Nothing was sent or written."
	}

	next := t.hints([]key.Binding{
		withHelp(nkeys.Enter, "enter", "plan again"), d.linkKey(), withHelp(distKeys.Back, "n", "back to the scope"),
	})

	return integBoxed(t, o, tone, title, body, next)
}

// planBox renders the Plan pane: the facts, the effects, the data that
// leaves the machine, and the line that says how to start.
func (d *distRecord) planBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := boxInner(w)
	p := d.plan.preview

	// The notes that stop a start come first, so a short box shows them.
	// The data disclosure comes before WON'T, because it matters more.
	body := d.planNotes(inner)
	body = append(body, d.planFacts(inner)...)
	body = append(body, t.effectLines(d.planWill(), nil, inner)...)
	body = append(body, "", integHeading(t, "Sent data"))
	body = append(body, integLines(t.Body, "Findings hold code and review text. The agent can also load the "+
		"project instructions. Later prompts can grow with labels learned in this run, so usage can exceed the "+
		"estimate. No cost figure is available.", inner)...)
	body = append(body, t.effectLines(nil, d.planWont(), inner)...)

	tail := []string{"", integRule(t, inner)}

	if d.runnable() {
		tail = append(tail, t.Body.Render("Nothing is sent until you press ")+t.Key.Render("y")+t.Body.Render("."))
	} else {
		tail = append(tail, integLines(t.Muted, "Nothing was sent. Press enter to plan again.", inner)...)
	}

	if len(body)+len(tail) > boxRows(h) {
		tail = tail[1:]
	}

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Plan", focused),
		Right: t.Muted.Render(propPlural(len(p.Plan.Groups), "group", "groups")),
	}

	return d.st.theme.scrollBox(o, body, tail, &d.plan.notes, [2]int{-1, -1}, false)
}

// planFacts renders the key facts of the plan. Every count comes from
// the engine plan; the prompt size is the engine's estimate.
func (d *distRecord) planFacts(w int) []string {
	t := d.st.theme
	p := d.plan.preview
	pl := p.Plan

	eligible := len(pl.Groups) + len(pl.Deferred)

	rows := [][2]string{
		{"Groups", t.Body.Render(itoa(len(pl.Groups)) + " of " + itoa(eligible) + " eligible")},
		{"Findings", t.Body.Render(itoa(pl.Preflight.Findings))},
		{"Prompt", t.Body.Render(distTokens(pl.Preflight.PromptChars)+" tokens") + " " + t.Faint.Render("(estimated)")},
		{"Skipped", t.Body.Render(propPlural(len(pl.Skipped), "group", "groups") + " already distilled")},
	}

	if n := len(pl.Cleanup); n > 0 {
		rows = append(rows, [2]string{"Cleanup", t.Body.Render(itoa(n)+" stale pending: ") +
			t.Attention.Render(propIDList(distProposalIDs(pl.Cleanup)))})
	}

	// The value wraps in its column, so the kind needs no cut here.
	agent := distAgentText(t, p.Invocation.Argv, distAgentKind(p.Invocation.Name, !d.plan.preset), 0)

	runsIn := runsInText(t, p.Invocation.Dir, d.plan.cwd, p.RepoRoot)
	rows = append(rows, [2]string{"Agent", agent}, [2]string{"Runs in", runsIn})

	return propKV(t, rows, w)
}

// runsInText names the directory where an agent process starts: dir is
// the directory of the invocation, cwd the directory of the console,
// and root the repository root. A preset without its own directory
// inherits the directory of the console, which can differ from the
// repository root (seamark -C). The distillation and the extraction
// reviews share it, so both disclose the directory the same way.
func runsInText(t *Theme, dir, cwd, root string) string {
	switch {
	case dir != "":
		return t.Body.Render(oneLine(displayPath(dir)))
	case cwd == "":
		return t.Muted.Render("the directory of the console, not known")
	case cwd != root:
		return t.Body.Render(oneLine(displayPath(cwd))) + " " +
			t.Attention.Render("(the console directory, not the repository root)")
	default:
		return t.Body.Render(oneLine(displayPath(cwd)))
	}
}

// planNotes renders what stops the start: a blocker of the preview, or
// the refusal of an earlier start. Each note says what to do.
func (d *distRecord) planNotes(w int) []string {
	t := d.st.theme

	var out []string

	// A blocker stops the start but is not a failure: design map §9
	// gives it the attention color, as the Evidence view does.
	for _, b := range d.plan.preview.Blockers {
		out = append(out, propNote(t, ToneAttention, oneLine(b)+". Install it, or press o to choose another "+
			"agent in Settings. Then press enter to plan again.", w)...)
		out = append(out, "")
	}

	if err := d.plan.stale; err != nil {
		text := "The run did not start: " + errText(err) + ". Nothing was sent. Press enter to plan again."
		if errors.Is(err, distill.ErrStale) {
			text = "The plan is out of date: " + distStaleText(err) + ". Nothing was sent. Press enter to plan again."
		}

		out = append(out, propNote(t, ToneAttention, text, w)...)
		out = append(out, "")
	}

	return out
}

// planWill lists the effects of the run.
func (d *distRecord) planWill() []effect {
	t := d.st.theme
	g := t.Glyphs
	p := d.plan.preview

	// A custom agent.argv has no preset name, so the effect names the
	// setting, as the Agent fact does.
	agent := "the agent"
	if name := oneLine(p.Invocation.Name); name != "" {
		agent = name
	}

	if !d.plan.preset {
		agent = "the custom agent.argv command"
	}

	var will []effect

	if c := p.Plan.Cleanup; len(c) > 0 {
		why := ": their evidence group is gone"
		if len(c) == 1 {
			why = ": its evidence group is gone"
		}

		will = append(will, effect{glyph: g.Del, tone: ToneFailure, text: "first remove the stale pending " +
			propIDList(distProposalIDs(c)) + why})
	}

	return append(will,
		effect{glyph: g.Add, tone: ToneSuccess, text: "send " + propPlural(len(p.Plan.Groups), "group", "groups") +
			" to " + agent + ", one call each, in engine order"},
		effect{glyph: g.Add, tone: ToneSuccess, text: "save the proposals and the signature of each group " +
			"when it finishes"})
}

// planWont lists what the run leaves alone.
func (d *distRecord) planWont() []string {
	wont := []string{
		"apply pins or change lessons.yaml",
		"add groups found after this plan",
		"retry a failed group by itself",
	}

	if len(d.plan.preview.Plan.Cleanup) > 0 {
		wont = append(wont, "prune applied pins: the cleanup removes pending proposals only")
	}

	return wont
}

// groupsBox renders the planned groups in engine order with their
// evidence, then the groups over the limit. The cursor group and its
// evidence stay visible while the cursor moves.
func (d *distRecord) groupsBox(w, h int, focused bool) string {
	t := d.st.theme
	inner := boxInner(w)
	pl := d.plan.preview.Plan

	d.plan.cursor = max(0, min(d.plan.cursor, len(pl.Groups)-1))

	body, keep := d.groupLines(inner, focused)

	o := boxOpts{
		W: w, H: h, Focus: focused, Title: integPaneTitle(t, "Groups", focused),
		Right: t.Faint.Render("engine order"),
	}

	return d.st.theme.scrollBox(o, body, nil, &d.plan.groups, keep, d.plan.follow)
}

// groupLines renders the group rows, the evidence rows under each group,
// and the deferred groups. It returns the line range of the cursor group.
func (d *distRecord) groupLines(w int, focused bool) (lines []string, keep [2]int) {
	t := d.st.theme
	pl := d.plan.preview.Plan
	regW := max(8, w-2-distIdxW-distFindW-distTokW)

	lines = []string{t.Faint.Render(integRow(t, cell("", 2), cell(padLeft("#", distIdxW-1), distIdxW),
		cell("Region", regW), cell(padLeft("Findings", distFindW-1), distFindW), cell("  Tokens (est.)", distTokW)))}
	keep = [2]int{-1, -1}

	for i, g := range pl.Preflight.Groups {
		cursor := i == d.plan.cursor
		start := len(lines)

		marker, region := "", t.Body
		if cursor {
			marker, region = t.Key.Render(t.Glyphs.Cursor), t.Title
		}

		row := integRow(t, cell(marker, 2), cell(t.Muted.Render(padLeft(itoa(i+1), distIdxW-1)), distIdxW),
			cell(region.Render(distRegionLabel(g.Region)), regW),
			cell(t.Body.Render(padLeft(itoa(g.Findings), distFindW-1)), distFindW),
			cell(t.Body.Render("  "+distTokens(g.PromptChars)), distTokW))

		if cursor && focused {
			row = t.highlight(row, w, bgSel)
		}

		lines = append(lines, row)
		lines = append(lines, distEvidenceLines(t, g.Evidence, w, distEvPad)...)

		if cursor {
			keep = [2]int{start, len(lines) - 1}
		}
	}

	if n := len(pl.Deferred); n > 0 {
		lines = append(lines, "", integHeading(t, "Over the limit")+t.Faint.Render("  "+
			propPlural(n, "group waits", "groups wait")+" for another run"))

		for i, g := range pl.Deferred {
			lines = append(lines, t.Faint.Render(integRow(t, cell("", 2), cell(padLeft(itoa(len(pl.Groups)+i+1),
				distIdxW-1), distIdxW), cell(distRegionLabel(g.Region), regW),
				cell(padLeft(itoa(g.Findings), distFindW-1), distFindW))))
		}
	}

	// The cleanup rows show with their rules, because y deletes them.
	// They are pending proposals, never applied pins.
	if c := pl.Cleanup; len(c) > 0 {
		lines = append(lines, "", integHeading(t, "Stale pending")+t.Faint.Render("  removed before the first "+
			"group; applied pins stay"))

		for _, p := range c {
			lines = append(lines, integRow(t, cell("", 2), cell(t.Attention.Render(propID(p.ID)), distIdxW+2),
				cell(t.Body.Render(oneLine(p.Rule)), max(4, regW-2)), cell(t.Muted.Render(propRegionLabel(p)),
					distFindW+distTokW)))
		}
	}

	return lines, keep
}

// distEvidenceLines renders the evidence of one group without its text.
// Each row shows the finding ID, the source, the pull request and the
// path. pad indents the rows under their group row.
func distEvidenceLines(t *Theme, ev []distill.FindingPlan, w, pad int) []string {
	w -= pad

	// The ID column fits the longest ID of the group. A cut ID cannot be
	// matched with the CLI output, and a fix ID has up to 19 digits.
	idW := distIDMinW
	for _, f := range ev {
		idW = max(idW, len(distFindingID(f))+1)
	}

	// The source column gets a third of the row, between a short
	// source word and "fix:conventional". It gives way to a long ID
	// before the path does.
	prW := 8
	srcW := min(18, max(10, w/3))

	if short := distPathMinW - (w - idW - srcW - prW); short > 0 {
		srcW = max(10, srcW-short)
	}

	pathW := max(4, w-idW-srcW-prW)
	indent := strings.Repeat(" ", pad)

	lines := make([]string, 0, len(ev))

	for _, f := range ev {
		pr := ""
		if f.PR > 0 {
			pr = "PR #" + itoa(f.PR)
		}

		row := integRow(t, cell(t.Faint.Render(distFindingID(f)), idW),
			cell(t.Muted.Render(oneLine(f.Source)), srcW), cell(t.Muted.Render(pr), prW),
			cell(t.Code.Render(truncateLeft(distFindingPath(f), pathW, t.Glyphs.Ellipsis)), pathW))

		lines = append(lines, indent+row)
	}

	return lines
}

// distFindingID renders the full finding ID, as the CLI prints it.
func distFindingID(f distill.FindingPlan) string {
	return "#" + strconv.FormatInt(f.ID, 10)
}

// distFindingPath names the file of a finding. A finding with several
// files shows the first and the count of the others.
func distFindingPath(f distill.FindingPlan) string {
	switch {
	case f.Path != "":
		return oneLine(f.Path)
	case len(f.Paths) == 1:
		return oneLine(f.Paths[0])
	case len(f.Paths) > 1:
		return oneLine(f.Paths[0]) + " +" + itoa(len(f.Paths)-1)
	default:
		return "no path"
	}
}

// nothingBox explains a plan without an eligible group and offers the
// cleanup of stale pending proposals as its own confirmed action. No
// agent call happens from here.
func (d *distRecord) nothingBox(w, h int) string {
	t := d.st.theme
	inner := boxInner(w)
	p := d.plan.preview
	g := t.Glyphs

	lines := []string{t.Success.Render(g.OK+" ") + t.Title.Render("Nothing new to distill"), ""}
	lines = append(lines, integLines(t.Body, d.nothingReason(), inner)...)

	if p.Plan != nil && len(p.Plan.Cleanup) > 0 {
		lines = append(lines, "")
		lines = append(lines, d.cleanupLines(inner)...)
	}

	lines = append(lines, "", t.Faint.Render("Nothing was sent to the agent."))

	o := boxOpts{
		W: w, H: h, Focus: true, Title: integPaneTitle(t, "Distillation", true),
		Right: t.Success.Render(g.OK + " nothing to do"),
	}

	return d.st.theme.scrollBox(o, lines, nil, &d.plan.notes, [2]int{-1, -1}, false)
}

// nothingReason tells why no group is eligible: no findings in scope,
// findings without a group, or every group already recorded. The last
// case says that a repeated run would send nothing new.
func (d *distRecord) nothingReason() string {
	p := d.plan.preview
	scope := distScopeIn(p.Region)

	if p.Plan == nil {
		return "The planner found no evidence group " + scope + "."
	}

	pl := p.Plan

	switch {
	case pl.Corpus == 0:
		return "The index holds no findings " + scope + ". Refresh the evidence first: press e for Evidence."
	case pl.Total == 0:
		return "The " + propPlural(pl.Corpus, "finding", "findings") + " " + scope +
			" form no evidence group, so there is nothing to send."
	case len(pl.Skipped) >= pl.Total:
		return propPlural(pl.Total, "group", "groups") + " " + scope + " already have recorded signatures, " +
			"so no agent call is needed. A repeated run would send no new evidence."
	default:
		return "No evidence group " + scope + " is eligible for this run."
	}
}

// cleanupLines offers or reports the stale-pending cleanup. The offer
// names every ID, because the user confirms exactly these rows.
func (d *distRecord) cleanupLines(w int) []string {
	t := d.st.theme
	pl := &d.plan
	ids := distProposalIDs(pl.preview.Plan.Cleanup)
	n := len(ids)

	switch {
	case pl.cleaning:
		return []string{t.Accent.Render(t.spinnerFrame(d.st.tick)) + " " +
			t.Title.Render("Removing "+propPlural(n, "stale pending proposal", "stale pending proposals")+t.Glyphs.Ellipsis)}
	case pl.cleaned && pl.cleanErr != nil:
		out := propNote(t, ToneFailure, "The cleanup removed nothing: "+errText(pl.cleanErr), w)

		if errors.Is(pl.cleanErr, distill.ErrStale) {
			out = append(out, integIndent(integLines(t.Muted, "The ledger changed since the plan. Press enter "+
				"to plan again.", w-2), 2)...)
		}

		return out
	case pl.cleaned:
		text := "Removed " + propPlural(pl.removed, "stale pending proposal", "stale pending proposals") +
			" of " + propIDList(ids) + "."
		if pl.removed < n {
			text += " The other rows were decided meanwhile, so they stay."
		}

		return propNote(t, ToneSuccess, text, w)
	}

	out := propNote(t, ToneAttention, propPlural(n, "stale pending proposal", "stale pending proposals")+": "+
		propIDList(ids), w)

	why := "Their evidence group no longer exists, so a run would remove them first. c removes them"
	if n == 1 {
		why = "Its evidence group no longer exists, so a run would remove it first. c removes it"
	}

	return append(out, integIndent(integLines(t.Muted, why+" after a separate confirmation. No agent call "+
		"happens, and applied pins stay: this is not pin pruning.", w-2), 2)...)
}

// distProposalIDs returns the IDs of proposals in their order.
func distProposalIDs(ps []domain.Proposal) []int64 {
	ids := make([]int64, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
	}

	return ids
}
