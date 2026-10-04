package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"

	"github.com/seamark-dev/seamark/internal/distill"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/proposals"
)

// This file renders the decision modals of Lessons → Proposals: the
// YAML or apply preview, the dismiss and record-as-applied previews, the
// waits, and the commit results. Every preview states what WILL and
// what WON'T change, and nothing is written before y.

// lesModal is the content of one modal: the border labels, the body
// that scrolls, and the actions that the bottom row names.
type lesModal struct {
	title   string
	right   string
	body    []string
	actions []key.Binding
}

// modal renders the open decision as a box for the shell to place on
// the dimmed frame. The body wraps to the modal width, and the modal
// keeps the scroll position of the decision.
func (p *propView) modal(w, h int) (string, bool) {
	d := p.dec
	if d == nil {
		return "", false
	}

	m := p.modalContent(d, modalBodyWidth(modalMinW, w))

	return p.st.theme.modal(modalOpts{Title: m.title, Right: m.right, Body: m.body, Actions: m.actions},
		&d.scroll, w, h), true
}

// modalContent builds the modal of the current decision stage.
func (p *propView) modalContent(d *propDecision, w int) lesModal {
	t := p.st.theme
	spin := t.Accent.Render(t.spinnerFrame(p.st.tick)) + " "

	switch d.stage {
	case propStagePreparing:
		return lesModal{
			title: spin + t.Title.Render("Preparing the "+propVerb(d.action)+" preview"+t.Glyphs.Ellipsis),
			body: propLines(t.Muted, "Seamark reads the ledger, the write gate and the pin file. "+
				"Nothing is written.", w),
			actions: []key.Binding{withHelp(nkeys.Back, "esc", "cancel")},
		}
	case propStageCommitting:
		note := "Seamark records the ledger change. The result appears here."
		if d.action == proposals.ActionApply {
			note = "Seamark writes the pins to " + proposals.LessonsFile + ", then records the ledger. " +
				"The operation cannot stop safely; the result appears here."
		}

		return lesModal{title: spin + t.Title.Render(p.st.mutation+t.Glyphs.Ellipsis), body: propLines(t.Muted, note, w)}
	case propStageResult:
		return p.resultModal(d, w)
	}

	if d.err != nil {
		return p.previewErrModal(d, w)
	}

	switch d.action {
	case proposals.ActionDismiss:
		return p.dismissModal(d, w)
	case proposals.ActionReconcile:
		return p.reconcileModal(d, w)
	default:
		return p.applyModal(d, w)
	}
}

// propVerb names an action in prose.
func propVerb(a proposals.Action) string {
	switch a {
	case proposals.ActionDismiss:
		return "dismiss"
	case proposals.ActionReconcile:
		return "record-as-applied"
	default:
		return "apply"
	}
}

// propTarget returns the pin file that a preview names.
func propTarget(pv *proposals.Preview) string {
	if pv.Target == "" {
		return proposals.LessonsFile
	}

	return pv.Target
}

// propSubject names the previewed proposals for a modal title: the ID
// and the rule of one proposal, or the IDs and the count of several.
func propSubject(t *Theme, d *propDecision) string {
	sep := " " + t.Glyphs.Sep + " "
	ps := d.pv.Proposals

	switch len(ps) {
	case 0:
		return propIDList(d.ids)
	case 1:
		return propID(ps[0].ID) + sep + oneLine(ps[0].Rule)
	default:
		return propIDList(propProposalIDs(ps)) + sep + propPlural(len(ps), "proposal", "proposals")
	}
}

// previewActions returns the modal actions of a preview. Only a
// confirmable preview offers y; c records existing pins before it
// offers a copy, because a copy would paste them again.
func (p *propView) previewActions() []key.Binding {
	var out []key.Binding

	if p.confirmable() {
		out = append(out, withHelp(lesKeys.Confirm, "y", p.confirmLabel()))
	}

	switch {
	case len(p.reconcileOffer()) > 0:
		out = append(out, withHelp(lesKeys.Reconcile, "c", "record as applied (ledger only)"))
	case p.copyable():
		out = append(out, withHelp(lesKeys.Reconcile, "c", "copy to clipboard"))
	}

	back := "close"
	if p.confirmable() {
		back = "cancel"
	}

	return append(out, withHelp(nkeys.Back, "esc", back))
}

// commonLines returns the parts that every preview shares after its
// lead: the implicit-target note, the selected rows and the left-out
// IDs.
func (p *propView) commonLines(d *propDecision, why string, w int) []string {
	t := p.st.theme
	pv := d.pv

	var out []string

	if d.implicit && len(pv.Proposals) > 0 {
		out = append(out, "")
		out = append(out, propLines(t.Muted, "Nothing was selected, so this covers the proposal under the cursor: "+
			propIDList(d.ids)+".", w)...)
	}

	if len(pv.Proposals) > 1 {
		out = append(out, "", t.Heading.Render("SELECTED")+"  "+t.Faint.Render(itoa(len(pv.Proposals))))
		out = append(out, p.previewRows(pv, w)...)
	}

	if len(pv.Ineligible) > 0 {
		out = append(out, "", t.Heading.Render("LEFT OUT"))
		out = append(out, propLines(t.Muted, propIDList(pv.Ineligible)+": "+why+".", w)...)
	}

	return out
}

// previewRows lists the previewed proposals with their rules.
func (p *propView) previewRows(pv *proposals.Preview, w int) []string {
	t := p.st.theme

	idW := 0
	for _, pr := range pv.Proposals {
		idW = max(idW, width(propID(pr.ID)))
	}

	lines := make([]string, len(pv.Proposals))
	for i, pr := range pv.Proposals {
		lines[i] = t.Muted.Render(padRight(propID(pr.ID), idW)) + "  " +
			t.Body.Render(truncate(oneLine(pr.Rule), max(4, w-idW-2), t.Glyphs.Ellipsis))
	}

	return lines
}

// applyModal renders the apply preview. With the gate off it is the
// YAML modal: the block to copy and no write. With existing pins it
// offers only the ledger-only record.
func (p *propView) applyModal(d *propDecision, w int) lesModal {
	t := p.st.theme
	pv := d.pv
	target := propTarget(pv)
	n := len(pv.Proposals)

	m := lesModal{actions: p.previewActions()}

	switch {
	case n == 0:
		m.title = t.Attention.Render("Nothing to apply")
	case len(pv.AlreadyPinned) > 0:
		m.title = t.Attention.Render("Pins already exist") + " " + t.Body.Render(propIDList(pv.AlreadyPinned))
	case p.confirmable():
		m.title = t.TabActive.Render("Apply") + " " + t.Body.Render(propSubject(t, d))
	default:
		m.title = t.TabActive.Render("YAML") + " " + t.Body.Render(propSubject(t, d))
	}

	m.body = p.previewLead(d, target, w)
	m.body = append(m.body, p.commonLines(d, "no longer pending", w)...)

	if n == 0 {
		return m
	}

	arrow := " " + t.Glyphs.Arrow + " "
	ids := propIDList(propProposalIDs(pv.Proposals))

	switch {
	case len(pv.AlreadyPinned) > 0:
		pinned := propIDList(pv.AlreadyPinned)
		m.body = append(m.body, p.st.theme.effectLines(nil, []string{
			"write " + target + ": pins already exist for " + pinned,
			"change the ledger until you press c to record " + pinned + " as applied",
		}, w)...)
	case pv.Write:
		file := target
		if !pv.Lessons.Exists {
			file += " (the file is created)"
		}

		m.body = append(m.body, p.st.theme.effectLines([]effect{
			{glyph: t.Glyphs.Add, tone: ToneSuccess, text: "add " + propPlural(n, "pin", "pins") + " under pin: in " + file},
			{glyph: t.Glyphs.Add, tone: ToneSuccess, text: "record " + ids + " as applied in the ledger (pending" +
				arrow + "applied)"},
		}, []string{"change other pins or other proposals", "write anything before you press y"}, w)...)
	}

	block, rest, err := p.shownBlock(pv)
	if err != nil {
		m.body = append(m.body, "")
		m.body = append(m.body, propNote(t, ToneAttention, "The pin block for "+propIDList(rest)+
			" could not be rendered: "+errText(err), w)...)

		return m
	}

	if block == "" {
		return m
	}

	yamlLines, numbered, pins := yamlBlockLines(t, block, w)
	m.right = t.Muted.Render(propPlural(numbered, "line", "lines"))
	m.body = append(m.body, "")
	m.body = append(m.body, yamlLines...)

	if pins.folded {
		m.body = append(m.body, "")
		m.body = append(m.body, propLines(t.Faint, "Long notes continue on indented lines. YAML reads them as one "+
			"line, so the pasted note is the same text.", w)...)
	}

	if pins.overflow {
		m.body = append(m.body, "")
		m.body = append(m.body, propNote(t, ToneAttention, "Some lines cannot fold without a change to the YAML. "+
			"A line without a number continues the line above on the screen only; c copies the exact block.", w)...)
	}

	if !pv.Write {
		m.body = append(m.body, "")
		m.body = append(m.body, propLines(t.Muted, "This command prints the same block in a shell. It changes "+
			"nothing while distill.write is off:", w)...)
		m.body = append(m.body, t.command("seamark lessons --apply "+propIDArg(rest)))
	}

	return m
}

// shownBlock returns the pin block that the apply modal shows and the
// IDs it holds, or the render failure. With existing pins the modal never shows a block that
// holds them, because a paste or a write would duplicate them. With the
// gate off it shows a display-only block for the proposals without a
// pin. With the gate on it shows none, because c must come first.
func (p *propView) shownBlock(pv *proposals.Preview) (block string, ids []int64, err error) {
	if len(pv.AlreadyPinned) == 0 {
		return pv.YAML, propProposalIDs(pv.Proposals), nil
	}

	rest := propUnpinned(pv)
	if pv.Write || len(rest) == 0 {
		return "", nil, nil
	}

	// This block is for display only. Commit compares Preview.YAML,
	// which the gate-off preview never commits.
	block, err = distill.RenderPins(rest)

	return block, propProposalIDs(rest), err
}

// propYAMLKeyEnd returns the end of a "key:" prefix of a YAML line, or
// -1. A key is a plain identifier followed by a colon and a space or
// the line end.
func propYAMLKeyEnd(s string) int {
	for i, r := range s {
		switch {
		case r == ':':
			if i > 0 && (i+1 == len(s) || s[i+1] == ' ') {
				return i + 1
			}

			return -1
		case r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' && i > 0:
		default:
			return -1
		}
	}

	return -1
}

// yamlBlockLines numbers a pin block for a modal: line numbers faint,
// comments faint, keys in the info color, values in the body color.
// The block loses its common indent, as in the 1a design, so a comment
// line fits the 80-cell modal. Long notes fold where YAML allows it. A
// line that cannot fold, for example a long comment, wraps on the
// screen only: its continuation has no number, and a copy takes the
// block unchanged. It returns the lines, the count of numbered lines,
// and the fold result. The apply preview and the Phase 3 pin previews
// share it, so every pin block reads the same way.
func yamlBlockLines(t *Theme, block string, w int) (out []string, numbered int, pins propPinBlock) {
	src := propDedent(multiLine(block))

	numW := 2
	pins = propFoldPins(src, max(1, w-numW-2))

	if n := len(itoa(len(pins.lines))); n > numW {
		numW = n
		pins = propFoldPins(src, max(1, w-numW-2))
	}

	avail := max(1, w-numW-2)
	gutter := strings.Repeat(" ", numW+2)

	for i, line := range pins.lines {
		body := strings.TrimLeft(line, " ")
		indent := line[:len(line)-len(body)]

		// A continuation segment starts two cells deeper, so it reads as
		// part of the line above.
		segs := strings.Split(wrap(body, max(1, avail-len(indent)-2)), "\n")
		if width(line) <= avail {
			segs = []string{body}
		}

		out = append(out, t.Faint.Render(padLeft(itoa(i+1), numW))+"  "+indent+propYAMLStyle(t, segs[0], pins.cont[i]))

		for _, seg := range segs[1:] {
			style := t.Body
			if strings.HasPrefix(body, "#") {
				style = t.Faint
			}

			out = append(out, gutter+indent+"  "+style.Render(seg))
		}
	}

	return out, len(pins.lines), pins
}

// propYAMLStyle colors one YAML line without its indent. A folded note
// continuation carries no key, so it keeps the body color.
func propYAMLStyle(t *Theme, body string, cont bool) string {
	switch {
	case cont:
		return t.Body.Render(body)
	case strings.HasPrefix(body, "#"):
		return t.Faint.Render(body)
	}

	var out string

	if rest, ok := strings.CutPrefix(body, "- "); ok {
		out = t.Muted.Render("-") + " "
		body = rest
	}

	if end := propYAMLKeyEnd(body); end > 0 {
		return out + t.Info.Render(body[:end]) + t.Body.Render(body[end:])
	}

	return out + t.Body.Render(body)
}

// propDedent removes the indent that every non-blank line shares. A
// sequence at the key column under "pin:" is valid YAML, so the block
// keeps its value.
func propDedent(block string) string {
	lines := strings.Split(block, "\n")
	common := -1

	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}

		n := len(l) - len(strings.TrimLeft(l, " "))
		if common < 0 || n < common {
			common = n
		}
	}

	if common <= 0 {
		return block
	}

	for i, l := range lines {
		if len(l) >= common {
			lines[i] = l[common:]
		} else {
			lines[i] = strings.TrimLeft(l, " ")
		}
	}

	return strings.Join(lines, "\n")
}

// previewLead is the first paragraph of a preview: what the decision
// does, or why it cannot do it.
func (p *propView) previewLead(d *propDecision, target string, w int) []string {
	t := p.st.theme
	pv := d.pv

	switch {
	case len(pv.Proposals) == 0:
		return propNote(t, ToneAttention, "None of the requested proposals is still pending, so there is "+
			"nothing to "+propVerb(d.action)+". Nothing was changed.", w)
	case d.action == proposals.ActionDismiss:
		return propLines(t.Body, "Dismissed proposals are remembered: the same evidence will not come back. "+
			"Pins stay unchanged. The console has no undo.", w)
	case d.action == proposals.ActionReconcile:
		return propLines(t.Body, "The pins of these proposals already exist in "+target+". This records them "+
			"as applied in the ledger only; the pin file stays unchanged.", w)
	case len(pv.AlreadyPinned) > 0:
		return p.pinnedLead(pv, target, w)
	case !pv.Write && pv.LessonsErr != nil:
		// The gate-off preview still shows the block, but apply can never
		// write through a linked or unsafe pin file, so enabling the gate
		// would not help.
		return append(propNote(t, ToneAttention, "The console cannot write "+target+": "+errText(pv.LessonsErr), w),
			propLines(t.Body, "Copy this block under pin: in the pin file by hand. Enabling distill.write does "+
				"not change this. The proposals stay pending. Nothing was changed.", w)...)
	case !pv.Write:
		// Every a reads the gate again, so a new preview is enough after
		// a change; a refresh with r is not needed.
		return propLines(t.Muted, "distill.write is off in .seamark/config.yaml, so nothing is written. Copy this "+
			"block under pin: in "+target+" to apply it by hand, or turn on distill.write in Settings "+
			t.Glyphs.Arrow+" Pin writing (5), then press a again for a new preview. The proposals stay pending. "+
			"Nothing was changed.", w)
	default:
		return propLines(t.Body, "Seamark adds the pin block below under pin: in "+target+", then records the "+
			"proposals as applied. distill.write is on. Nothing changes before you press y.", w)
	}
}

// pinnedLead explains an apply preview that found existing pins. It
// comes before the gate check: with the gate off, a pasted block would
// duplicate the pins just like a written one. The ledger-only record
// ignores the gate, so c works in both cases.
func (p *propView) pinnedLead(pv *proposals.Preview, target string, w int) []string {
	t := p.st.theme
	ids := propIDList(pv.AlreadyPinned)
	rest := propUnpinned(pv)

	out := propNote(t, ToneAttention, "Pins already exist for "+ids+".", w)

	if pv.Write {
		text := target + " already holds these pins, so a blind apply would duplicate them. Press c to record " +
			ids + " as applied (ledger only); the pin file stays unchanged."
		if len(rest) > 0 {
			text += " Then apply " + propIDList(propProposalIDs(rest)) + " again."
		}

		return append(out, propLines(t.Body, text, w)...)
	}

	out = append(out, propLines(t.Body, target+" already holds these pins, so pasting them again would duplicate "+
		"them. Press c to record "+ids+" as applied (ledger only). The record works with distill.write off, and "+
		"the pin file stays unchanged.", w)...)

	block := "Every selected proposal has its pin, so there is no block to copy. Nothing was changed."
	if len(rest) > 0 {
		block = "distill.write is off, so the console does not write pins. The block below holds only " +
			propIDList(propProposalIDs(rest)) + ", without a pin yet: copy it under pin: in " + target +
			". The proposals stay pending. Nothing was changed."
	}

	return append(append(out, ""), propLines(t.Body, block, w)...)
}

// dismissModal renders the dismiss preview.
func (p *propView) dismissModal(d *propDecision, w int) lesModal {
	t := p.st.theme
	pv := d.pv
	n := len(pv.Proposals)

	m := lesModal{actions: p.previewActions()}

	if n == 0 {
		m.title = t.Attention.Render("Nothing to dismiss")
	} else {
		m.title = t.Failure.Bold(true).Render("Dismiss") + " " + t.Body.Render(propSubject(t, d))
	}

	m.body = p.previewLead(d, propTarget(pv), w)
	m.body = append(m.body, p.commonLines(d, "no longer pending", w)...)

	if n == 0 {
		return m
	}

	arrow := " " + t.Glyphs.Arrow + " "
	m.body = append(m.body, p.st.theme.effectLines([]effect{
		{glyph: t.Glyphs.Del, tone: ToneFailure, text: "move " + propIDList(propProposalIDs(pv.Proposals)) +
			" to dismissed in the ledger (pending" + arrow + "dismissed)"},
		{glyph: t.Glyphs.Del, tone: ToneFailure, text: "remember the decision for the same evidence"},
	}, []string{"change " + proposals.LessonsFile + ": pins stay unchanged", "write anything before you press y"}, w)...)

	return m
}

// reconcileModal renders the record-as-applied preview. It records
// existing pins in the ledger and never writes the pin file.
func (p *propView) reconcileModal(d *propDecision, w int) lesModal {
	t := p.st.theme
	pv := d.pv
	target := propTarget(pv)
	n := len(pv.Proposals)

	m := lesModal{actions: p.previewActions(), right: t.Muted.Render("ledger only")}

	if n == 0 {
		m.title = t.Attention.Render("Nothing to record")
	} else {
		m.title = t.TabActive.Render("Record") + " " + t.Body.Render(propSubject(t, d)) + t.Muted.Render(" as applied")
	}

	m.body = p.previewLead(d, target, w)
	m.body = append(m.body, p.commonLines(d, "no longer pending, or without a pin", w)...)

	if n == 0 {
		return m
	}

	arrow := " " + t.Glyphs.Arrow + " "
	m.body = append(m.body, p.st.theme.effectLines([]effect{
		{glyph: t.Glyphs.Add, tone: ToneSuccess, text: "record " + propIDList(propProposalIDs(pv.Proposals)) +
			" as applied in the ledger (pending" + arrow + "applied)"},
	}, []string{"change " + target + ": the pins already exist", "write anything before you press y"}, w)...)

	return m
}

// previewErrModal explains a failed preview. Nothing changed, because a
// preview only reads.
func (p *propView) previewErrModal(d *propDecision, w int) lesModal {
	t := p.st.theme

	var hint string

	switch {
	case errors.Is(d.err, proposals.ErrNotPinned):
		hint = "Record as applied only records pins that already exist in " + proposals.LessonsFile +
			". Use a to apply proposals whose pins are missing."
	case errors.Is(d.err, proposals.ErrNothingSelected):
		hint = "Select pending proposals with space first."
	case errors.Is(d.err, ErrNoLedger):
		hint = "Proposals live in the index database. Build it from Overview (press 1, then b)."
	default:
		hint = "Fix the cause named above, for example a malformed file, then refresh with r and try again."
	}

	body := propNote(t, ToneFailure, "The "+propVerb(d.action)+" preview could not be built: "+errText(d.err), w)
	body = append(body, "")
	body = append(body, propLines(t.Body, hint, w)...)
	body = append(body, "")
	body = append(body, propLines(t.Muted, "Nothing was changed.", w)...)

	return lesModal{
		title: t.Failure.Render("Preview failed"), body: body,
		actions: []key.Binding{withHelp(nkeys.Back, "esc", "back to the list")},
	}
}

// resultModal reports what a commit changed. File and ledger outcomes
// stay separate, and the title never claims an apply that did not
// happen. Nothing retries by itself.
func (p *propView) resultModal(d *propDecision, w int) lesModal {
	t := p.st.theme

	actions := []key.Binding{withHelp(nkeys.Enter, "enter", "close")}
	if d.reviewAgain() {
		actions = []key.Binding{withHelp(nkeys.Enter, "enter", "preview again"), withHelp(nkeys.Back, "esc", "close")}
	}

	if d.err != nil {
		body := propNote(t, ToneAttention, propRefusal(d.err, t), w)
		body = append(body, "")
		body = append(body, propLines(t.Muted, "Details: "+errText(d.err), w)...)

		if d.reviewAgain() {
			body = append(body, "")
			body = append(body, propLines(t.Body, "Press enter to build a new preview from the current state.", w)...)
		}

		return lesModal{title: t.Attention.Render("Nothing changed"), body: body, actions: actions}
	}

	var (
		title string
		tone  Tone
		lead  string
	)

	if d.action == proposals.ActionApply {
		title, tone, lead = p.applyOutcome(d.res)
	} else {
		title, tone, lead = p.ledgerOutcome(d.action, d.res)
	}

	body := propNote(t, tone, lead, w)
	body = append(body, "", t.Heading.Render("WHAT HAPPENED"))
	body = append(body, propKV(t, p.resultFacts(d.action, d.res), w)...)
	body = append(body, "")
	body = append(body, propLines(t.Faint, "Nothing is retried automatically.", w)...)

	return lesModal{title: t.ToneStyle(tone).Render(title), body: body, actions: actions}
}

// applyOutcome titles an apply result and states the next safe step.
func (p *propView) applyOutcome(res proposals.Result) (title string, tone Tone, lead string) {
	file := proposals.LessonsFile

	switch {
	case res.FileErr != nil && res.FileWritten:
		return "Pin file write failed", ToneFailure, "The write to " + file + " failed after it changed the file. " +
			"Check the file with git diff before anything else. The ledger was not changed. Do not apply again: " +
			"that could duplicate pins."
	case res.FileErr != nil:
		return "Apply failed", ToneFailure, "The pin file was not changed, and the ledger was not changed."
	case res.Partial():
		why := "not recorded as applied"
		if res.LedgerErr != nil {
			why = "still pending"
		}

		return "Pins written, ledger incomplete", ToneFailure, "Pins were written to " + file + " but the ledger " +
			"was not fully updated (" + propIDList(res.NotMoved) + " " + why + "). Do not apply again: that would " +
			"duplicate pins. Keep them selected and press c to record them as applied (ledger only)."
	case res.FileWritten:
		return "Applied " + propPlural(len(res.Moved), "pin", "pins"), ToneSuccess, "Wrote the pins to " + file +
			" and recorded " + propIDList(res.Moved) + " as applied. Review the diff and commit it."
	default:
		return "Nothing applied", ToneAttention, "No pin was written and no proposal changed."
	}
}

// ledgerOutcome titles a dismiss or record-as-applied result.
func (p *propView) ledgerOutcome(a proposals.Action, res proposals.Result) (title string, tone Tone, lead string) {
	done, verb := "dismissed", "Dismiss"
	if a == proposals.ActionReconcile {
		done, verb = "recorded as applied", "Recording"
	}

	total := len(res.Moved) + len(res.NotMoved)

	switch {
	case len(res.Moved) == 0 && res.LedgerErr != nil:
		return verb + " failed", ToneFailure, "No proposal changed: the ledger write failed."
	case len(res.Moved) == 0:
		return "Nothing " + done, ToneAttention, "No proposal changed: none was still pending."
	case len(res.NotMoved) > 0 || res.LedgerErr != nil:
		return itoa(len(res.Moved)) + " of " + itoa(total) + " " + done, ToneAttention,
			propIDList(res.Moved) + " " + done + "; " + propIDList(res.NotMoved) + " did not change. Pins stay unchanged."
	default:
		return strings.ToUpper(done[:1]) + done[1:] + " " + propPlural(len(res.Moved), "proposal", "proposals"),
			ToneSuccess, propIDList(res.Moved) + " " + done + ". Pins stay unchanged."
	}
}

// resultFacts lists the file and ledger outcomes separately.
func (p *propView) resultFacts(a proposals.Action, res proposals.Result) [][2]string {
	arrow := " " + p.st.theme.Glyphs.Arrow + " "

	to := "applied"
	if a == proposals.ActionDismiss {
		to = "dismissed"
	}

	var rows [][2]string

	switch {
	case a != proposals.ActionApply:
		rows = append(rows, [2]string{"Pin file", "unchanged"})
	case res.FileWritten && res.FileErr != nil:
		rows = append(rows, [2]string{"Pin file", "changed, then the write failed"})
	case res.FileWritten:
		rows = append(rows, [2]string{"Pin file", "written: " + proposals.LessonsFile})
	default:
		rows = append(rows, [2]string{"Pin file", "not changed"})
	}

	if res.FileErr != nil {
		rows = append(rows, [2]string{"File error", errText(res.FileErr)})
	}

	if len(res.Moved) > 0 {
		rows = append(rows, [2]string{"Ledger", propIDList(res.Moved) + arrow + to})
	} else {
		rows = append(rows, [2]string{"Ledger", "no change"})
	}

	if len(res.NotMoved) > 0 {
		rows = append(rows, [2]string{"Not moved", propIDList(res.NotMoved)})
	}

	if res.LedgerErr != nil {
		rows = append(rows, [2]string{"Ledger error", errText(res.LedgerErr)})
	}

	return rows
}

// propRefusal maps a commit refusal to a clear message. Every refusal
// means that nothing changed.
func propRefusal(err error, t *Theme) string {
	switch {
	case errors.Is(err, proposals.ErrStale):
		return "Something changed since the preview" + propDash(t) + "preview again."
	case errors.Is(err, proposals.ErrWriteDisabled):
		return "distill.write is off, so nothing was written. Preview again to get the YAML block."
	case errors.Is(err, proposals.ErrNeedsReconcile):
		return "Pins for the selected proposals already exist, so a blind apply would duplicate them. " +
			"Preview again and press c to record them as applied (ledger only)."
	case errors.Is(err, proposals.ErrNothingSelected):
		return "No pending proposal was selected."
	case errors.Is(err, ErrBusy):
		return "Another operation is running, so this one did not start."
	case errors.Is(err, ErrNoLedger):
		return "The index database is not readable, so the ledger was not changed."
	default:
		return "The decision was refused before any write."
	}
}

// propUnpinned returns the previewed proposals whose pins do not exist
// yet, in preview order.
func propUnpinned(pv *proposals.Preview) []domain.Proposal {
	var out []domain.Proposal

	for _, pr := range pv.Proposals {
		if !slices.Contains(pv.AlreadyPinned, pr.ID) {
			out = append(out, pr)
		}
	}

	return out
}

// propProposalIDs returns the IDs of proposals in their order.
func propProposalIDs(ps []domain.Proposal) []int64 {
	ids := make([]int64, len(ps))
	for i, pr := range ps {
		ids[i] = pr.ID
	}

	return ids
}

// propIDArg renders IDs as one CLI argument, for example "p1,p3". The
// CLI splits the argument at commas, so it needs no spaces.
func propIDArg(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = propID(id)
	}

	return strings.Join(parts, ",")
}
