package tui

import (
	"strconv"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovLessonsHookClients returns the names of the clients whose lessons
// hook is configured. The names are raw; the caller sanitizes them.
func ovLessonsHookClients(insps []integration.Inspection) []string {
	var names []string

	for _, insp := range insps {
		if e, ok := insp.Entry(integration.CapabilityEdits); ok && e.Supported && e.State != integration.StateAbsent {
			names = append(names, ovClientName(insp))
		}
	}

	return names
}

// ovGateHookModes returns "<client> <mode>" for every client that can
// run a gate hook. The text is raw; the caller sanitizes it.
func ovGateHookModes(insps []integration.Inspection) []string {
	var parts []string

	for _, insp := range insps {
		e, ok := insp.Entry(integration.CapabilityCommands)
		if !ok || !e.Supported {
			continue
		}

		mode := insp.GateMode

		switch {
		case e.State == integration.StateUnreadable:
			mode = "unreadable"
		case mode == "":
			mode = "none"
		}

		parts = append(parts, ovClientName(insp)+" "+mode)
	}

	return parts
}

// ovClientState grades a client's configuration for its glyph. It covers
// configuration only; trust and evidence have their own items.
func ovClientState(insp integration.Inspection) (tone ui.Tone, word string) {
	if ovNothingConfigured(insp) {
		return ui.ToneNeutral, "not set up"
	}

	tone, word = ui.ToneSuccess, "configured"

	for _, c := range insp.Capabilities {
		if !c.Supported {
			continue
		}

		switch c.State {
		case integration.StateUnreadable:
			return ui.ToneFailure, "unreadable"
		case integration.StatePartial, integration.StateConflict:
			tone, word = ui.ToneAttention, "needs attention"
		}
	}

	for _, f := range insp.Findings {
		if f.Level != integration.FindingInfo && tone == ui.ToneSuccess {
			tone, word = ui.ToneAttention, "needs attention"
		}
	}

	return tone, word
}

// ovClientBrief describes a client's configuration in one line from the
// shared helpers, so the words match doctor and status.
func ovClientBrief(insp integration.Inspection, dot string) string {
	if ovNothingConfigured(insp) {
		return "not set up"
	}

	var parts []string

	edits, _ := insp.Entry(integration.CapabilityEdits)
	commands, _ := insp.Entry(integration.CapabilityCommands)

	if edits.Supported || commands.Supported {
		parts = append(parts, insp.DescribeHooks())
	}

	words := map[integration.Capability][2]string{
		integration.CapabilityMCPRegistration: {"MCP registered", "MCP"},
		integration.CapabilitySkills:          {"skills current", "skills"},
		integration.CapabilityToolGrants:      {"tool grants current", "tool grants"},
	}

	for _, capability := range []integration.Capability{
		integration.CapabilityMCPRegistration, integration.CapabilitySkills, integration.CapabilityToolGrants,
	} {
		c, ok := insp.Entry(capability)
		if !ok {
			continue
		}

		switch {
		case c.Supported && c.State == integration.StateCurrent:
			parts = append(parts, words[capability][0])
		case c.Supported && c.State == integration.StatePartial && capability == integration.CapabilitySkills:
			parts = append(parts, "skills need refresh")
		default:
			parts = append(parts, words[capability][1]+" "+ovStateWord(c))
		}
	}

	return strings.Join(parts, dot)
}

// ovAge renders a Unix time as a coarse age, for example "3h ago".
func ovAge(ts int64, now time.Time) string {
	d := now.Sub(time.Unix(ts, 0))

	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	}
}

// summaryDetail renders every summary fact in full for the summary item
// of the collapsed layout. The summary line keeps one value per card, so
// this detail keeps each fact readable at every size (AC-12). It reads
// the same snapshots as the cards, so both always agree.
func (o *overview) summaryDetail(w int) string {
	t := o.st.theme

	cli := t.Muted.Render(ui.Wrap("The CLI prints the same facts:", w)) + "\n  " +
		commandText(t, "seamark status") + "\n  " + commandText(t, "seamark doctor")

	return ui.JoinBlocks(
		o.summaryStaleNotes(w),
		sectionBlock(t, "Index", kv(t, o.indexFacts(), w)),
		sectionBlock(t, "Navigate", kv(t, o.navigateFacts(), w)),
		sectionBlock(t, "Learn", kv(t, o.learnFacts(), w)),
		sectionBlock(t, "Guard", kv(t, o.guardFacts(), w)),
		sectionBlock(t, "Clients", kv(t, o.clientFacts(), w)),
		cli,
	)
}

// summaryStale reports a summary source that failed to reload, so its
// facts come from an older snapshot.
func (o *overview) summaryStale() bool {
	return o.st.index.stale() || o.st.ledger.stale() || o.st.integrations.stale()
}

// summaryStaleNotes names each summary source that failed to reload,
// with the error and the age of the snapshot that stays on screen.
func (o *overview) summaryStaleNotes(w int) string {
	t := o.st.theme
	now := time.Now()

	var notes []string

	add := func(what string, stale bool, err error, at time.Time) {
		if !stale {
			return
		}

		notes = append(notes, ui.Wrap(o.staleNote(err)+t.Muted.Render(". "+what+" come from the read "+
			ovAge(at.Unix(), now)+"; r reads again."), w))
	}

	add("The index facts", o.st.index.stale(), o.st.index.err, o.st.index.at)
	add("The proposal counts", o.st.ledger.stale(), o.st.ledger.err, o.st.ledger.at)
	add("The client facts", o.st.integrations.stale(), o.st.integrations.err, o.st.integrations.at)

	return strings.Join(notes, "\n")
}

// probeState renders a probe state other than ready as a badge and a
// short note. ok is false for a ready index, whose facts come from the
// semantic health instead.
func (o *overview) probeState(p workspace.IndexProbe) (badge, note string, ok bool) {
	t := o.st.theme
	schema := t.Muted.Render("schema v" + strconv.Itoa(p.Schema) + ", this seamark reads v" + strconv.Itoa(p.Supported))

	switch p.State {
	case workspace.IndexAbsent:
		return t.Badge(ui.ToneAttention, "no index"),
			t.Muted.Render("b builds it; nothing is written before you confirm"), true
	case workspace.IndexUnreadable:
		return t.Badge(ui.ToneFailure, "unreadable"), t.Muted.Render(ui.ErrText(p.Err)), true
	case workspace.IndexNewer:
		return t.Badge(ui.ToneFailure, "newer schema"), schema, true
	case workspace.IndexOlder:
		return t.Badge(ui.ToneAttention, "needs upgrade"), schema, true
	case workspace.IndexNotIndexed:
		return t.Badge(ui.ToneAttention, "not indexed"), t.Muted.Render("decisions only, no index run yet"), true
	}

	return "", "", false
}

// indexFacts lists the index facts: the probe state, or the freshness
// and coverage of a ready index, then the database and its schema.
func (o *overview) indexFacts() [][2]string {
	t := o.st.theme
	sec := &o.st.index

	if !sec.loaded {
		if sec.err != nil {
			return [][2]string{
				{"State", t.Badge(ui.ToneFailure, "read failed")},
				{"Reason", t.Body.Render(ui.ErrText(sec.err))},
				{"Database", t.Body.Render(ui.OneLine(o.st.svc.DBPath()))},
			}
		}

		return [][2]string{{"State", o.reading("the index")}}
	}

	v := sec.value
	p := v.Probe

	path := p.Path
	if path == "" {
		path = o.st.svc.DBPath()
	}

	database := [2]string{"Database", t.Body.Render(ui.OneLine(path))}

	var rows [][2]string

	switch badge, note, ok := o.probeState(p); {
	case ok && p.State == workspace.IndexAbsent:
		// No database exists, so it has no index time and no schema.
		return [][2]string{{"State", badge + " " + note}, database}
	case ok && (p.State == workspace.IndexNewer || p.State == workspace.IndexOlder):
		// The note repeats the schema, which has its own row below.
		rows = append(rows, [2]string{"State", badge})
	case ok:
		rows = append(rows, [2]string{"State", badge + " " + note})
	case v.Status == nil:
		rows = append(rows, [2]string{"Health", t.Badge(ui.ToneFailure, "read failed") + " " +
			t.Body.Render(ui.ErrText(v.StatusErr))})
	default:
		rows = append(rows, [2]string{"Freshness", o.freshnessBadge(v.Status.Freshness)})
		rows = append(rows, o.coverageFacts(v.Status.Coverage)...)
	}

	rows = append(rows, [2]string{"Indexed", o.ageText(v, "")}, database)

	if p.Schema > 0 {
		rows = append(rows, [2]string{"Schema", t.Body.Render("v" + strconv.Itoa(p.Schema) +
			", this seamark reads v" + strconv.Itoa(p.Supported))})
	}

	return rows
}

// coverageFacts lists the parsed, seen and skipped files, the parse
// errors, and the deleted paths when any exist. An older index without
// coverage says unknown, never zero.
func (o *overview) coverageFacts(c status.Coverage) [][2]string {
	t := o.st.theme

	if !c.Known {
		return [][2]string{{"Files", t.Unknown.Render("coverage unknown")}}
	}

	errs := t.Body.Render("none")
	if c.ParseErrors > 0 {
		errs = t.Attention.Render(ovNum(c.ParseErrors))
	}

	rows := [][2]string{
		{"Files", t.Body.Render(ovNum(c.FilesParsed) + " of " + ovNum(c.FilesSeen) + " parsed, " +
			ovNum(c.FilesSkipped) + " skipped")},
		{"Parse errors", errs},
	}

	// The row exists only with a count, as the INDEX card line does.
	if c.FilesDeleted > 0 {
		rows = append(rows, [2]string{"Deleted", t.Body.Render(
			ovPlural(c.FilesDeleted, "tracked file", "tracked files") + ", not staged")})
	}

	return rows
}

// navigateFacts lists the code graph size and the decisions mined from
// history, or the reason they are missing.
func (o *overview) navigateFacts() [][2]string {
	t := o.st.theme

	s, why := o.health()
	if s == nil {
		return [][2]string{{"Health", why}}
	}

	decisions := t.Muted.Render("none mined from history")
	if s.History.Decisions > 0 {
		decisions = t.Body.Render(ovNum(s.History.Decisions) + " from history")
	}

	return [][2]string{
		{"Symbols", t.Body.Render(ovNum(s.Symbols))},
		{"Edges", t.Body.Render(ovNum(s.Edges))},
		{"Decisions", decisions},
	}
}

// learnFacts lists the lesson loop facts: proposals, lessons, review
// mining, the distill agent and the lessons hook.
func (o *overview) learnFacts() [][2]string {
	t := o.st.theme
	rows := [][2]string{{"Proposals", o.proposalsText()}}

	if s, why := o.health(); s != nil {
		distill := t.Unknown.Render("unresolved")
		if s.DistillClient != "" {
			distill = t.Muted.Render(ui.OneLine(s.DistillClient))
		}

		rows = append(rows,
			[2]string{"Lessons", t.Body.Render(ovNum(s.Lessons))},
			[2]string{"Reviews", o.reviewsText(s, "")},
			[2]string{"Distill agent", distill})

		if s.DistillAgent != "" {
			rows = append(rows, [2]string{"Distill command", t.Muted.Render(ui.Diag(s.DistillAgent))})
		}
	} else {
		rows = append(rows, [2]string{"Lessons", why})
	}

	if v, ok := o.evidenceFact(); ok {
		rows = append(rows, [2]string{"Evidence", v})
	}

	if v, ok := o.pinWriteFact(); ok {
		rows = append(rows, [2]string{"Pin writing", v})
	}

	return append(rows, [2]string{"Lessons hook", o.clientListFact(ovLessonsHookClients, "none configured")})
}

// evidenceFact renders the stored findings by source for the LEARN
// facts. ok is false while the evidence has not loaded, or without a
// ready index, because the index facts already explain that. A failed
// count read is unknown, never zero.
func (o *overview) evidenceFact() (string, bool) {
	t := o.st.theme
	sec := &o.st.learning

	if !sec.loaded || sec.value.Probe.State != workspace.IndexReady {
		return "", false
	}

	v := sec.value
	if v.CountsErr != nil {
		return t.Unknown.Render("unknown: " + ui.ErrText(v.CountsErr)), true
	}

	text := t.Body.Render(ovPlural(v.FixFindings, "fix finding", "fix findings")) + t.Muted.Render(o.dot()) +
		t.Body.Render(ovPlural(v.ReviewFindings, "review finding", "review findings"))

	if sec.stale() {
		text += " " + o.staleNote(sec.err)
	}

	return text, true
}

// pinWriteFact renders the effective distill.write gate from the
// settings snapshot. ok is false while the settings have not loaded. An
// unreadable distill section is unknown, because the gate then does not
// open.
func (o *overview) pinWriteFact() (string, bool) {
	t := o.st.theme
	sec := &o.st.settings

	if !sec.loaded || sec.value == nil {
		return "", false
	}

	snap := sec.value

	if err := snap.ReaderErrs[settings.ReaderDistill]; err != nil {
		return t.Unknown.Render("unknown: " + ui.ErrText(err)), true
	}

	if snap.Distill == nil {
		return t.Unknown.Render("unknown"), true
	}

	// The words are the notes of the Settings choices. They name no
	// action, because every pin write reads this gate (spec R-5).
	if snap.Distill.Distill.Write {
		return t.Body.Render("on") + t.Muted.Render(o.dot()+settingsPinWriteOnNote), true
	}

	return t.Body.Render("off") + t.Muted.Render(o.dot()+settingsPinWriteOffNote), true
}

// guardFacts lists the policy mode, its rules and its load result, then
// the gate hook of each client. A warn hook still follows an enforcing
// policy, so both stay apart.
func (o *overview) guardFacts() [][2]string {
	t := o.st.theme
	sec := &o.st.index
	hooks := [2]string{"Gate hooks", o.clientListFact(ovGateHookModes, "no client runs a gate hook")}

	switch {
	case !sec.loaded && sec.err != nil:
		return [][2]string{{"Policy", t.Unknown.Render("unknown: " + ui.ErrText(sec.err))}, hooks}
	case !sec.loaded:
		return [][2]string{{"Policy", o.reading("the policy")}, hooks}
	}

	p := sec.value.Policy

	switch {
	case p.Err != nil:
		return [][2]string{{"Policy", t.Badge(ui.ToneFailure, "error") + " " + t.Body.Render(ui.ErrText(p.Err))}, hooks}
	case p.Mode == "":
		return [][2]string{{"Policy", t.Unknown.Render("unknown")}, hooks}
	}

	return [][2]string{
		{"Policy", t.Body.Render("mode " + ui.OneLine(p.Mode) + ", compiles")},
		{"Rules", t.Body.Render(ovNum(p.Deny) + " deny, " + ovPlural(p.Approval, "approval rule", "approval rules"))},
		hooks,
	}
}

// clientListFact renders a list that comes from the integration
// inspection, or the reading state before the inspection loads.
func (o *overview) clientListFact(list func([]integration.Inspection) []string, none string) string {
	t := o.st.theme
	sec := &o.st.integrations

	if !sec.loaded {
		return o.reading("the integrations")
	}

	items := list(sec.value)
	if len(items) == 0 {
		return t.Muted.Render(none)
	}

	return t.Body.Render(ui.OneLine(strings.Join(items, ", ")))
}

// clientFacts lists each client with its configuration state and its
// one-line brief, without the cut that the list rows need.
func (o *overview) clientFacts() [][2]string {
	t := o.st.theme
	sec := &o.st.integrations

	if !sec.loaded {
		return [][2]string{{"Clients", o.reading("the integrations")}}
	}

	if len(sec.value) == 0 {
		return [][2]string{{"Clients", t.Muted.Render("no registered clients")}}
	}

	dot := " " + t.Glyphs.Sep + " "
	rows := make([][2]string, 0, len(sec.value))

	for _, insp := range sec.value {
		tone, word := ovClientState(insp)
		rows = append(rows, [2]string{
			ui.OneLine(ovClientName(insp)),
			t.Badge(tone, word) + dot + t.Muted.Render(ui.OneLine(ovClientBrief(insp, dot))),
		})
	}

	return rows
}

// ovDoctorCounts returns the check counts per doctor state, so the
// summary line agrees with `seamark doctor`.
func ovDoctorCounts(r *doctor.Report) (ok, info, warn, fail int) {
	for _, c := range r.Checks {
		switch c.State {
		case doctor.StateOK:
			ok++
		case doctor.StateInfo:
			info++
		case doctor.StateWarn:
			warn++
		case doctor.StateFail:
			fail++
		}
	}

	return ok, info, warn, fail
}
