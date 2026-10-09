package tui

import (
	"strings"

	"github.com/seamark-dev/seamark/internal/integration"
)

// The content of the Integrations boxes: the client item of the Clients
// box and the sections of the detail box. Configuration, project trust
// and native evidence stay separate columns, because a current file
// says nothing about trust or about what the client really runs.

// Column widths of the capability table. The Where column takes the
// rest of the width. The config and trust columns are wider than in
// the 1a design, so "! unreadable" and "✓ established" fit with their
// gap and are never cut.
const (
	integCapW    = 19
	integConfigW = 13
	integTrustW  = 14
	integNativeW = 12
	// integWhereMin is the narrowest useful Where column. Below it the
	// table moves the detail to its own line under each row.
	integWhereMin = 12
	// integLabelW is the label column of the Gate and Shared resources
	// sections.
	integLabelW = 17
)

// integExpected reports a capability that setup can configure for the
// client. The context reset of a client without a reset hook is absent
// by design, so counts must not call it missing.
func integExpected(insp integration.Inspection, c integration.CapabilityInspection) bool {
	if !c.Supported {
		return false
	}

	return c.Capability != integration.CapabilityResets || insp.Setup.ResetHook || c.State != integration.StateAbsent
}

// integTally is the configuration count of one client. expected counts
// the capabilities that setup can configure; na counts the others.
type integTally struct {
	expected int
	na       int
	states   map[integration.CapabilityState]int
}

// integCount counts the configuration states of one client.
func integCount(insp integration.Inspection) integTally {
	tally := integTally{states: map[integration.CapabilityState]int{}}

	for _, c := range insp.Capabilities {
		if !integExpected(insp, c) {
			tally.na++

			continue
		}

		tally.expected++
		tally.states[c.State]++
	}

	return tally
}

// setUp reports that at least one expected capability has configuration.
func (c integTally) setUp() bool {
	return c.expected > 0 && c.states[integration.StateAbsent] < c.expected
}

// integBadge renders the count at the right of a client name, for
// example "✓ 6/6" or "! 3/6 · 1 n/a". It is a count of current
// capabilities, never "connected": configuration is one dimension of
// three.
func integBadge(t *Theme, c integTally) string {
	g := t.Glyphs

	if !c.setUp() {
		return t.Muted.Render(g.Info + " not set up")
	}

	current := c.states[integration.StateCurrent]
	tone := ToneSuccess

	switch {
	case c.states[integration.StateUnreadable]+c.states[integration.StateConflict] > 0:
		tone = ToneFailure
	case current < c.expected:
		tone = ToneAttention
	}

	badge := t.ToneStyle(tone).Render(t.ToneGlyph(tone) + " " + itoa(current) + "/" + itoa(c.expected))
	if c.na > 0 {
		badge += t.Faint.Render(" " + g.Sep + " " + itoa(c.na) + " n/a")
	}

	return badge
}

// integShortNames are the words of the client summary line for the
// capabilities that are current. The gate has its own part with its
// mode, and the reset hook has no short word of its own.
var integShortNames = map[integration.Capability]string{
	integration.CapabilityEdits:           "lessons",
	integration.CapabilityMCPRegistration: "MCP",
	integration.CapabilitySkills:          "skills",
	integration.CapabilityToolGrants:      "grants",
}

// integSummaryParts returns the summary parts of one client: the gate
// mode, then the count of every state that is not current, or the short
// names of the current capabilities when every one is current.
func integSummaryParts(insp integration.Inspection, c integTally) []string {
	var parts []string

	if insp.GateMode != "" {
		parts = append(parts, "gate "+oneLine(insp.GateMode))
	}

	if c.expected > 0 && c.states[integration.StateCurrent] == c.expected {
		for _, capability := range []integration.Capability{
			integration.CapabilityEdits, integration.CapabilityMCPRegistration,
			integration.CapabilitySkills, integration.CapabilityToolGrants,
		} {
			if entry, ok := insp.Entry(capability); ok && entry.Supported && entry.State == integration.StateCurrent {
				parts = append(parts, integShortNames[capability])
			}
		}

		return parts
	}

	for _, st := range []struct {
		state integration.CapabilityState
		label string
	}{
		{integration.StatePartial, "partial"},
		{integration.StateConflict, "conflict"},
		{integration.StateUnreadable, "unreadable"},
		{integration.StateAbsent, "not configured"},
	} {
		if n := c.states[st.state]; n > 0 {
			parts = append(parts, itoa(n)+" "+st.label)
		}
	}

	return parts
}

// integClientItem builds the list item of one inspected client.
func integClientItem(t *Theme, insp integration.Inspection) integItem {
	tally := integCount(insp)

	return integItem{name: oneLine(insp.Name), badge: integBadge(t, tally), summary: integSummaryParts(insp, tally)}
}

// detailLines renders every section of one client's inspection as box
// lines of w cells.
func (s *integrations) detailLines(insp integration.Inspection, w int) []string {
	t := s.st.theme
	c := s.clientOf(insp)

	var out []string

	if support := c.DescribeSupport(); support != "" {
		out = append(out, integLines(t.Muted, "Supports "+oneLine(support)+".", w)...)
	}

	if sec := &s.st.integrations; sec.stale() {
		out = append(out, integHanging(t.Attention.Render(t.Glyphs.Warn+" "), t.Attention,
			"stale: "+errText(sec.err), w)...)
	}

	out = append(out, "", integHeading(t, "Capabilities"))
	out = append(out, integCapTable(t, insp, integName(c, insp), w)...)

	if note := integEvidenceNote(insp); note != "" {
		out = append(out, "")
		out = append(out, integLines(t.Faint, note, w)...)
	}

	out = append(out, "", integHeading(t, "Gate"))
	out = append(out, integGate(t, insp, w)...)

	out = append(out, "", integHeading(t, "Shared resources"))
	out = append(out, integShared(t, s.clients, w)...)

	out = append(out, "", integHeading(t, "Findings"))
	out = append(out, integFindings(t, insp.Findings, w)...)

	if next := integNext(t, insp, w); len(next) > 0 {
		out = append(out, "", integHeading(t, "Next"))
		out = append(out, next...)
	}

	return out
}

// integCapTable renders one row per capability with the configuration,
// the location or detail, the project trust and the native evidence in
// separate columns. A narrow box moves the detail under the row.
func integCapTable(t *Theme, insp integration.Inspection, client string, w int) []string {
	if len(insp.Capabilities) == 0 {
		return integLines(t.Muted, "The inspection reports no capabilities.", w)
	}

	capW := integCapW
	whereW := w - integCapW - integConfigW - integTrustW - integNativeW

	if whereW < integWhereMin {
		whereW = 0
		capW = max(10, w-integConfigW-integTrustW-integNativeW)
	}

	row := func(label, config, where, trust, native string) string {
		cells := []colCell{cell(label, capW), cell(config, integConfigW)}
		if whereW > 0 {
			cells = append(cells, cell(where, whereW))
		}

		return integRow(t, append(cells, cell(trust, integTrustW), cell(native, integNativeW))...)
	}

	f := t.Faint
	lines := []string{row(f.Render("Capability"), f.Render("Config"), f.Render("Where"), f.Render("Trust"), f.Render("Native"))}

	for _, c := range insp.Capabilities {
		where := integWhere(insp, c, client)

		lines = append(lines, row(t.Body.Render(integCapLabel(c.Capability)), integConfigCell(t, insp, c),
			t.Muted.Render(where), integTrustCell(t, c), integNativeCell(t, c)))

		if whereW == 0 && where != "" {
			lines = append(lines, "  "+t.Muted.Render(truncate(where, max(1, w-2), t.Glyphs.Ellipsis)))
		}
	}

	return lines
}

// integWhere returns the location or the detail of one capability from
// the typed inspection. It is display text only: no code reads it back.
func integWhere(insp integration.Inspection, c integration.CapabilityInspection, client string) string {
	if !c.Supported {
		return "not supported by " + oneLine(client)
	}

	if !integExpected(insp, c) && c.Detail == "" {
		return "not installed by setup"
	}

	return oneLine(c.Describe())
}

// integConfigCell renders the configuration state of one capability.
// A capability that does not apply shows "n/a"; the glyph tells an
// unsupported one from one that setup does not install.
func integConfigCell(t *Theme, insp integration.Inspection, c integration.CapabilityInspection) string {
	g := t.Glyphs

	switch {
	case !c.Supported:
		return t.Muted.Render(g.Unsupported + " n/a")
	case !integExpected(insp, c):
		return t.Faint.Render(g.Info + " n/a")
	}

	switch c.State {
	case integration.StateCurrent:
		return t.Badge(ToneSuccess, "current")
	case integration.StatePartial:
		return t.Badge(ToneAttention, "partial")
	case integration.StateConflict:
		return t.Badge(ToneFailure, "conflict")
	case integration.StateUnreadable:
		return t.Badge(ToneFailure, "unreadable")
	default:
		return t.Muted.Render(g.Info + " absent")
	}
}

// integTrustCell renders the project trust of one capability. Unknown
// trust is never success: Seamark cannot read most trust records.
func integTrustCell(t *Theme, c integration.CapabilityInspection) string {
	g := t.Glyphs

	if !c.Supported || c.State == integration.StateAbsent {
		return t.Faint.Render(g.Info)
	}

	switch c.Trust {
	case integration.TrustEstablished:
		return t.Badge(ToneSuccess, "established")
	case integration.TrustPending:
		return t.Attention.Render(g.Info + " pending")
	default:
		return t.Unknown.Render(g.Unknown) + " " + t.Muted.Render("unknown")
	}
}

// integNativeCell renders the native evidence level in one word. The
// scope of the evidence, the client version and the surface, is in the
// note under the table, because the level alone is no claim about other
// versions.
func integNativeCell(t *Theme, c integration.CapabilityInspection) string {
	g := t.Glyphs

	if !c.Supported {
		return t.Faint.Render(g.Info)
	}

	switch c.Verification.Level {
	case integration.VerificationVerified:
		return t.Badge(ToneSuccess, "verified")
	case integration.VerificationPending:
		return t.Faint.Render(g.Info + " pending")
	case integration.VerificationUnverified:
		return t.Faint.Render(g.Info + " unverified")
	default:
		return t.Unknown.Render(g.Unknown) + " " + t.Muted.Render("unknown")
	}
}

// integEvidenceNote replaces the repeated trust and evidence lines of
// the capabilities with one note. It names the evidence scope whenever
// a capability has any, so a verified level never reads as a claim
// about other client versions.
func integEvidenceNote(insp integration.Inspection) string {
	var configured []integration.CapabilityInspection

	for _, c := range insp.Capabilities {
		if c.Supported && c.State != integration.StateAbsent {
			configured = append(configured, c)
		}
	}

	if len(configured) == 0 {
		return ""
	}

	unknownTrust, scoped := true, false

	for _, c := range configured {
		if c.Trust != integration.TrustUnknown {
			unknownTrust = false
		}

		v := c.Verification
		if v.Level == integration.VerificationVerified || v.Level == integration.VerificationPending || v.Surface != "" {
			scoped = true
		}
	}

	var parts []string

	if unknownTrust {
		parts = append(parts, "Project trust is unknown for every configured capability; Seamark reports trust and never grants it.")
	}

	switch evidence := insp.DescribeVerification(); {
	case !scoped:
		parts = append(parts, "No configured capability is natively verified.")
	case evidence != "":
		parts = append(parts, "Native evidence: "+oneLine(evidence)+".")
	}

	return strings.Join(parts, " ")
}

// integGate renders the effective and the managed gate mode. The
// effective mode covers every gate hook that runs, managed or not.
func integGate(t *Theme, insp integration.Inspection, w int) []string {
	commands, ok := insp.Entry(integration.CapabilityCommands)
	if !ok || !commands.Supported {
		return integLines(t.Muted, "No command gate support.", w)
	}

	effective := t.Title.Render(oneLine(insp.GateMode)) + t.Muted.Render("  from every gate hook that runs")

	switch {
	case commands.State == integration.StateUnreadable:
		effective = t.Unknown.Render(t.Glyphs.Unknown) + " " + t.Muted.Render("unknown  a hook document is unreadable")
	case insp.GateMode == "":
		effective = t.Muted.Render("none  no gate hook runs")
	}

	managed := t.Muted.Render("none")
	if insp.ManagedGateMode != "" {
		managed = t.Title.Render(oneLine(insp.ManagedGateMode))
	}

	return []string{
		integRow(t, cell(t.Muted.Render("Effective mode"), integLabelW), cell(effective, max(1, w-integLabelW))),
		integRow(t, cell(t.Muted.Render("Managed hook"), integLabelW), cell(managed, max(1, w-integLabelW))),
	}
}

// integShared names every skill destination with its consumers and the
// Seamark files that every client shares.
func integShared(t *Theme, clients []integration.Client, w int) []string {
	valueW := max(1, w-integLabelW)

	var lines []string

	for _, d := range integration.SkillDestinations(clients) {
		lines = append(lines, integRow(t, cell(t.Code.Render(oneLine(d.Dir)), integLabelW),
			cell(t.Body.Render(integConsumers(clients, d.Consumers)), valueW)))
	}

	shared := t.Body.Render("policy, lessons and config files") +
		t.Muted.Render(" "+t.Glyphs.Dash+" shared by every client")

	return append(lines, integRow(t, cell(t.Code.Render(".seamark/"), integLabelW), cell(shared, valueW)))
}

// integFindings renders findings with their level, reason, path and
// action. The level word stays beside the glyph, because a warning and
// an error share the "!" glyph and differ in color only.
func integFindings(t *Theme, findings []integration.Finding, w int) []string {
	return integFindingLines(t, findings, w, true)
}

// integFindingLines renders findings. withPath=false leaves the path
// line out, for the Plan box, whose rows already name every path.
func integFindingLines(t *Theme, findings []integration.Finding, w int, withPath bool) []string {
	if len(findings) == 0 {
		return []string{t.Success.Render(t.Glyphs.OK) + " " + t.Muted.Render("No findings.")}
	}

	var lines []string

	for i, f := range findings {
		if i > 0 {
			lines = append(lines, "")
		}

		tone := ToneNeutral

		switch f.Level {
		case integration.FindingWarning:
			tone = ToneAttention
		case integration.FindingError:
			tone = ToneFailure
		}

		glyph := t.ToneGlyph(tone)
		if tone == ToneNeutral {
			glyph = t.Glyphs.Info
		}

		lines = append(lines, integHanging(t.ToneStyle(tone).Render(glyph+" "+f.Level.String()+" "), t.Body,
			diag(f.Reason), w)...)

		if withPath && f.Path != "" {
			lines = append(lines, "  "+t.Code.Render(truncateLeft(oneLine(f.Path), max(1, w-2), t.Glyphs.Ellipsis)))
		}

		if f.Action != "" {
			lines = append(lines, integIndent(integCommandLines(t, diag(f.Action), max(1, w-2)), 2)...)
		}
	}

	return lines
}

// integNext lists every capability that setup can configure and that is
// not current: its full detail and the corrective action. The table
// clips the detail, so the full text is here.
func integNext(t *Theme, insp integration.Inspection, w int) []string {
	var lines []string

	labelW := min(integCapW, max(1, w/3))
	valueW := max(1, w-labelW)

	for _, c := range insp.Capabilities {
		if !integExpected(insp, c) || c.State == integration.StateCurrent {
			continue
		}

		lead := t.Title.Render(padRight(truncate(integCapLabel(c.Capability), labelW-1, t.Glyphs.Ellipsis), labelW))
		lines = append(lines, integHanging(lead, t.Muted, oneLine(c.Describe()), w)...)

		if c.Action != "" {
			lines = append(lines, integIndent(integCommandLines(t, c.Action, valueW), labelW)...)
		}
	}

	return lines
}

// integCapLabel returns the human name of a capability.
func integCapLabel(c integration.Capability) string {
	if label, ok := integCapLabels[c]; ok {
		return label
	}

	return oneLine(string(c))
}

// integStateWord renders the configuration state of one capability in
// the words of the setup result: "✓ current", "! partial" or
// "· not configured".
func integStateWord(t *Theme, c integration.CapabilityInspection) string {
	if !c.Supported {
		return t.Muted.Render(t.Glyphs.Unsupported + " not supported")
	}

	switch c.State {
	case integration.StateCurrent:
		return t.Badge(ToneSuccess, "current")
	case integration.StatePartial:
		return t.Badge(ToneAttention, "partial")
	case integration.StateConflict:
		return t.Badge(ToneFailure, "conflict")
	case integration.StateUnreadable:
		return t.Badge(ToneFailure, "unreadable")
	default:
		return t.Muted.Render(t.Glyphs.Info + " not configured")
	}
}

// integConsumers renders client IDs as display names, in the given
// order. An empty list names a shared Seamark file.
func integConsumers(clients []integration.Client, ids []string) string {
	if len(ids) == 0 {
		return "shared Seamark file"
	}

	names := make([]string, 0, len(ids))

	for _, id := range ids {
		name := id

		for _, c := range clients {
			if c.ID == id {
				name = c.Name
			}
		}

		names = append(names, oneLine(name))
	}

	return strings.Join(names, ", ")
}
