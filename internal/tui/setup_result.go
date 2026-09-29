package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/seamark-dev/seamark/internal/integration"
)

// The apply step shows the running apply, then one box with the log of
// every operation, the result line, and what the new inspection still
// reports, in three groups: problems that need attention, optional
// additions that stay unconfigured, and the steps that remain in the
// client itself. File application, project trust and native evidence
// stay three separate statements.

// Column widths of the operation log. The target takes the rest.
const (
	setupOpGlyphW   = 2
	setupOpKindW    = 7
	setupOpOutcomeW = 14
)

// applyingView shows the running apply and the paths it writes. The
// engine reports no progress, so no path gets a state before the
// result arrives.
func (wz *setupWizard) applyingView(w, h int) string {
	t := wz.st.theme
	inner := boxInner(w)

	lines := []string{t.Accent.Render(t.spinnerFrame(wz.st.tick)) + " " +
		t.Title.Render("Applying setup for "+oneLine(wz.client.Name)+t.Glyphs.Ellipsis), ""}
	lines = append(lines, integLines(t.Muted, "Seamark writes the reviewed files now. This step cannot stop "+
		"safely, so it runs to the end. The result lists every operation.", inner)...)
	lines = append(lines, "")

	for _, e := range setupEntries(wz.preview) {
		if e.kind == setupEntryKeep || (e.kind == setupEntrySkill && !e.skill.Writes()) {
			continue
		}

		lines = append(lines, integRow(t, cell(t.Faint.Render(t.Glyphs.Info), setupOpGlyphW),
			cell(t.Muted.Render(setupEntryNoun(e)), setupOpKindW),
			cell(t.Body.Render(truncateLeft(oneLine(e.path), max(4, inner-setupOpGlyphW-setupOpKindW), t.Glyphs.Ellipsis)),
				max(1, inner-setupOpGlyphW-setupOpKindW))))
	}

	return t.box(boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Apply", true), Lines: lines})
}

// setupEntryNoun names what a path row is: a file or a skill.
func setupEntryNoun(e setupEntry) string {
	if e.kind == setupEntrySkill {
		return "skill"
	}

	return "file"
}

func (wz *setupWizard) resultKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, setupKeys.Plan):
		// A new plan reuses the reviewed request, so the retry plans the
		// same intent against the files as they are now.
		if wz.result != nil && wz.result.err != nil {
			return wz.plan(wz.setups)
		}
	case key.Matches(msg, setupKeys.Done):
		wz.closed = true
	default:
		wz.pager.scroll(msg)
	}

	return nil
}

// setupSucceeded reports a run where every operation was applied or
// kept. Only then may the result say "Configuration updated".
func setupSucceeded(o *setupOutcome) bool {
	if o.err != nil {
		return false
	}

	for _, ops := range [][]integration.OpResult{o.res.Ops, o.res.Skills} {
		for _, op := range ops {
			if op.Status != integration.OpApplied && op.Status != integration.OpKept {
				return false
			}
		}
	}

	return true
}

// setupWroteAny reports whether any operation reached the disk.
func setupWroteAny(o *setupOutcome) bool {
	for _, ops := range [][]integration.OpResult{o.res.Ops, o.res.Skills} {
		for _, op := range ops {
			if op.Status == integration.OpApplied {
				return true
			}
		}
	}

	return false
}

// resultView renders the Apply box: the operation log, the result line,
// the remaining steps and the closest CLI route, in one scrolling box.
func (wz *setupWizard) resultView(w, h int) string {
	t := wz.st.theme
	o := wz.result

	if o == nil {
		return ""
	}

	inner, rows := boxInner(w), boxRows(h)
	content := wz.pager.render("result", strings.Join(wz.resultLines(inner), "\n"), inner, rows)

	return t.box(boxOpts{W: w, H: h, Focus: true, Title: integPaneTitle(t, "Apply", true),
		Right: setupResultState(t, o), Foot: wz.pager.foot(t), Lines: strings.Split(content, "\n")})
}

// setupResultState returns the state word at the right of the Apply box
// border.
func setupResultState(t *Theme, o *setupOutcome) string {
	switch {
	case o.err == nil && len(o.res.Ops)+len(o.res.Skills) == 0:
		return t.Unknown.Render(t.Glyphs.Unknown + " no operation reported")
	case setupSucceeded(o):
		return t.Success.Render(t.Glyphs.OK + " done")
	default:
		return t.Failure.Render(t.Glyphs.Fail + " stopped")
	}
}

// resultLines renders the content of the Apply box.
func (wz *setupWizard) resultLines(w int) []string {
	t := wz.st.theme
	o := wz.result

	stale := errors.Is(o.err, integration.ErrStalePlan)
	wrote := setupWroteAny(o)
	ok := t.Success.Render(t.Glyphs.OK + " ")

	var lines []string

	switch {
	case o.err == nil && len(o.res.Ops)+len(o.res.Skills) == 0:
		lines = append(lines, t.Unknown.Render(t.Glyphs.Unknown+" ")+t.Title.Render("No operation reported"))
		lines = append(lines, integLines(t.Muted, "The setup engine returned no operation, so nothing is known "+
			"to have changed. The remaining steps below come from a new inspection.", w)...)
	case setupSucceeded(o):
		lines = append(lines, ok+t.Title.Render("Configuration updated"))
		lines = append(lines, integLines(t.Muted, "The reviewed files for "+oneLine(wz.client.Name)+" are written. "+
			"This says nothing about project trust or native evidence; see the remaining steps below.", w)...)
	case stale && !wrote:
		lines = append(lines, t.Attention.Render(t.Glyphs.Warn+" ")+t.Title.Render("Setup stopped"))
		lines = append(lines, integLines(t.Body, "Files changed since the preview. Nothing was written.", w)...)
		lines = append(lines, integLines(t.Muted, "Press p to plan again from the files as they are now.", w)...)
	default:
		lines = append(lines, t.Failure.Render(t.Glyphs.Fail+" ")+t.Title.Render("Setup stopped"))
		lines = append(lines, integLines(t.Failure, errText(o.err), w)...)
		lines = append(lines, integLines(t.Muted, "Seamark does not roll back. Operations marked created or "+
			"updated stay written, and the operations after the failure did not run. Press p to plan again: "+
			"a new plan starts from the files as they are.", w)...)
	}

	if ops := slices.Concat(o.res.Ops, o.res.Skills); len(ops) > 0 {
		lines = append(lines, "", integHeading(t, "Operations"))
		lines = append(lines, setupOpLines(t, wz.clients, ops, w)...)
	}

	if !stale || wrote {
		lines = append(lines, "")
		lines = append(lines, wz.remaining(w)...)
	}

	cmd, caveat := setupCLIRoute(wz.setups)
	lines = append(lines, "", integHeading(t, "CLI route"))
	lines = append(lines, integLines(t.Code, cmd, w)...)

	return append(lines, integLines(t.Muted, caveat, w)...)
}

// setupOpLines renders one log row per operation: the glyph, the kind,
// the path and the outcome, then the detail and the error. The outcome
// words come from the typed status: an applied path is "created" or
// "updated".
func setupOpLines(t *Theme, clients []integration.Client, ops []integration.OpResult, w int) []string {
	g := t.Glyphs
	targetW := max(4, w-setupOpGlyphW-setupOpKindW-setupOpOutcomeW)
	pad := setupOpGlyphW + setupOpKindW

	var lines []string

	for _, op := range ops {
		kind := "file"
		if op.Kind == integration.OpSkill {
			kind = "skill"
		}

		var glyph, outcome string

		switch op.Status {
		case integration.OpApplied:
			word := "updated"
			if op.Created {
				word = "created"
			}

			glyph, outcome = t.Success.Render(g.OK), t.Success.Render(word)
		case integration.OpKept:
			glyph, outcome = t.Faint.Render(g.Info), t.Faint.Render("kept")
		case integration.OpFailed:
			glyph, outcome = t.Failure.Render(g.Fail), t.Failure.Render("failed")
		case integration.OpNotAttempted:
			glyph, outcome = t.Attention.Render(g.Warn), t.Attention.Render("not attempted")
		default:
			glyph, outcome = t.Unknown.Render(g.Unknown), t.Unknown.Render(op.Status.String())
		}

		lines = append(lines, integRow(t, cell(glyph, setupOpGlyphW), cell(t.Muted.Render(kind), setupOpKindW),
			cell(t.Body.Render(truncateLeft(oneLine(op.Path), targetW-1, g.Ellipsis)), targetW),
			cell(outcome, setupOpOutcomeW)))

		var meta []string

		if op.Detail != "" {
			meta = append(meta, oneLine(op.Detail))
		}

		meta = append(meta, integConsumers(clients, op.Consumers))
		lines = append(lines, integIndent(integLines(t.Muted, strings.Join(meta, " "+g.Sep+" "), max(1, w-pad)), pad)...)

		if op.Err != nil {
			lines = append(lines, integIndent(integLines(t.Failure, errText(op.Err), max(1, w-pad)), pad)...)
		}
	}

	return lines
}

// remaining renders what the new inspection still reports for the
// client. It waits for the reinspection after the apply. A capability
// that the user did not choose is optional, not an unfinished step, so
// it gets its own neutral group.
func (wz *setupWizard) remaining(w int) []string {
	t := wz.st.theme

	if !wz.result.inspected {
		return []string{integHeading(t, "Remaining native steps"),
			t.Faint.Render("Reading the updated configuration" + t.Glyphs.Ellipsis)}
	}

	insp, ok := wz.inspection()
	if !ok {
		return append([]string{integHeading(t, "Remaining native steps")},
			integHanging(t.Unknown.Render(t.Glyphs.Unknown+" "), t.Body,
				"The updated inspection is not available. Press r to read the files again.", w)...)
	}

	attention, optional := wz.remainingCaps(insp, w)

	var lines []string

	if len(attention) > 0 {
		lines = append(lines, integHeading(t, "Needs attention"))
		lines = append(lines, attention...)
		lines = append(lines, "")
	}

	if len(optional) > 0 {
		lines = append(lines, integHeading(t, "Not configured (optional)"))
		lines = append(lines, integLines(t.Muted, "You did not choose these additions. They stay optional; "+
			"the command adds one later.", w)...)
		lines = append(lines, optional...)
		lines = append(lines, "")
	}

	if len(attention)+len(optional) == 0 {
		lines = append(lines, t.Success.Render(t.Glyphs.OK)+" "+
			t.Muted.Render("Every capability that setup can configure is current."), "")
	}

	lines = append(lines, integHeading(t, "Remaining native steps"))

	return append(lines, wz.nativeSteps(insp, w)...)
}

// remainingCaps sorts the capabilities that are not current into two
// groups. A capability of the reviewed request, or one in a partial,
// conflicting or unreadable state, needs attention. An absent
// capability that the user did not choose is optional.
func (wz *setupWizard) remainingCaps(insp integration.Inspection, w int) (attention, optional []string) {
	t := wz.st.theme

	for _, c := range insp.Capabilities {
		if !integExpected(insp, c) || c.State == integration.StateCurrent {
			continue
		}

		requested := setupCapRequested(wz.setups, c.Capability)
		word := integStateWord(t, c)

		// A requested capability that is still absent is a problem: the
		// apply did not produce it.
		if requested && c.State == integration.StateAbsent {
			word = t.Badge(ToneAttention, "not configured")
		}

		line := t.Body.Render(padRight(integCapLabel(c.Capability), 20)) + word

		if !requested && c.State != integration.StateAbsent {
			line += t.Muted.Render(" " + t.Glyphs.Sep + " not part of this setup")
		}

		block := []string{line}
		if c.Action != "" {
			block = append(block, integIndent(integCommandLines(t, c.Action, max(1, w-2)), 2)...)
		}

		if requested || c.State != integration.StateAbsent {
			attention = append(attention, block...)
		} else {
			optional = append(optional, block...)
		}
	}

	return attention, optional
}

// setupCapRequested reports whether a reviewed request asked for a
// capability. The hooks request covers all three hook capabilities.
func setupCapRequested(setups []integration.ClientSetup, c integration.Capability) bool {
	for _, cs := range setups {
		switch c {
		case integration.CapabilityMCPRegistration:
			if cs.RegisterMCP {
				return true
			}
		case integration.CapabilityEdits, integration.CapabilityCommands, integration.CapabilityResets:
			if cs.Hooks {
				return true
			}
		case integration.CapabilitySkills:
			if cs.Skills {
				return true
			}
		case integration.CapabilityToolGrants:
			if cs.ApproveTools {
				return true
			}
		}
	}

	return false
}

// nativeSteps renders the steps that remain in the client itself: the
// findings, project trust and native evidence. Seamark reports trust
// and evidence; it cannot establish either.
func (wz *setupWizard) nativeSteps(insp integration.Inspection, w int) []string {
	t := wz.st.theme
	name := oneLine(wz.client.Name)

	var trust []string

	for _, c := range insp.Capabilities {
		if !c.Supported || c.State == integration.StateAbsent {
			continue
		}

		label := c.Trust.String()
		if !slices.Contains(trust, label) {
			trust = append(trust, label)
		}
	}

	trustText := "no configured capability to trust yet"
	if len(trust) > 0 {
		trustText = strings.Join(trust, ", ")
	}

	evidence := insp.DescribeVerification()
	if evidence == "" {
		evidence = "no native evidence recorded"
	}

	lines := integFindings(t, insp.Findings, w)
	lines = append(lines, "")
	lines = append(lines, integLabeled(t, "Project trust", 17, trustText+" "+t.Glyphs.Sep+
		" Seamark reports trust and never grants it", t.Body, w)...)
	lines = append(lines, integLabeled(t, "Native evidence", 17, oneLine(evidence), t.Body, w)...)
	lines = append(lines, "")

	return append(lines, integLines(t.Muted, "Written configuration does not prove that "+name+" runs the hooks or "+
		"the tools. Open the project in "+name+" to review and trust them there.", w)...)
}

// setupCLIRoute returns the closest `seamark init` command for the
// reviewed request and what it does differently. The CLI has no form
// for every combination, so the caveat says what the command adds.
func setupCLIRoute(setups []integration.ClientSetup) (cmd, caveat string) {
	if len(setups) == 0 {
		return "seamark init", "Reference text only; the console does not run it."
	}

	cs := setups[0]
	cmd = "seamark init --client " + cs.ClientID

	if cs.Skills {
		cmd += " --skills"
	}

	if cs.ApproveTools {
		cmd += " --approve-tools"
	}

	caveat = "Reference text only; the console does not run it. The CLI form plans the same additions."
	if !cs.Hooks || !cs.RegisterMCP {
		caveat = "Reference text only; the console does not run it. The CLI form also installs hooks and " +
			"registers MCP; it has no form for this exact selection."
	}

	return cmd, caveat
}
