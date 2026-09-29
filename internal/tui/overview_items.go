package tui

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/seamark-dev/seamark/internal/doctor"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// ovGroup is one heading of the attention list. The constant order is
// the display order: the full summary, then problems, then choices,
// then limits, then gaps in evidence. The full doctor report lives in
// the Doctor tab; the list keeps only the doctor items to act on.
type ovGroup int

const (
	// ovSummary holds the one summary item of the collapsed layout. It
	// has no heading and never takes the first selection, so the
	// attention list still opens on the most urgent fact.
	ovSummary ovGroup = iota
	ovActionable
	ovOptional
	ovUnsupported
	ovUnknown
	ovGroupCount
)

// ovGroupNames are the group headings. R-4 asks for these four classes
// apart, so no overall score can hide a problem behind a success.
var ovGroupNames = [ovGroupCount]string{"Summary", "Action", "Optional", "Unsupported", "Unknown"}

// ovGroupTones are the tones of the group glyphs: "!" for action, "·"
// for optional, "◌" for unsupported and "?" for unknown. An item with
// the failure tone keeps it, so a broken state stays red.
var ovGroupTones = [ovGroupCount]Tone{ToneNeutral, ToneAttention, ToneNeutral, ToneUnsupported, ToneUnknown}

// ovSection is one titled block of the detail pane, for example EFFECT
// or ABOUT THE HOOK. The text is raw; the view sanitizes it.
type ovSection struct {
	title string
	text  string
}

// ovActKind names the in-app action of an attention item.
type ovActKind int

const (
	// ovActNone means no in-app workflow exists; the detail then shows
	// the corrective text for the user to run.
	ovActNone ovActKind = iota
	// ovActNavigate opens a destination with an intent.
	ovActNavigate
	// ovActBuild opens the index build confirmation.
	ovActBuild
)

// ovAction is the in-app next step of an attention item. It opens a
// known workflow; it never runs text from a diagnostic.
type ovAction struct {
	kind   ovActKind
	dest   destination
	intent string
	// label is the short verb phrase for the footer and the detail.
	label string
}

// ovItem is one row of the attention list with its detail. Strings are
// raw: the view sanitizes them, because they quote repository files,
// native settings and reader errors.
type ovItem struct {
	// key is a stable identity, so the cursor stays on the same fact
	// across refreshes.
	key   string
	group ovGroup
	tone  Tone
	// badge names the underlying severity or state, for example "warn"
	// or "partial". Doctor severities stay as doctor reports them.
	badge string
	// title is the list row text; head replaces it as the detail
	// heading when the row repeats text that the body already shows.
	title string
	head  string
	// source says where the fact comes from, for example "doctor check:
	// hooks" or "Codex · MCP registration".
	source string
	// body is the prose of the detail; sections follow it under their
	// own headings.
	body     []string
	sections []ovSection
	// fix is the corrective CLI text. The console shows it as
	// selectable text and never runs it.
	fix string
	act ovAction
	// area names the doctor check that the item overlaps, so the
	// Action group lists one fact once.
	area string
	// summary marks the summary item. Its detail renders the live
	// summary facts instead of body.
	summary bool
}

// ovSummaryKey is the identity of the summary item.
const ovSummaryKey = "index:summary"

// ovBuildAction opens the index build confirmation.
var ovBuildAction = ovAction{kind: ovActBuild, label: "build the index"}

// ovNav returns an action that opens a destination with an intent.
func ovNav(dest destination, intent, label string) ovAction {
	return ovAction{kind: ovActNavigate, dest: dest, intent: intent, label: label}
}

// attention builds the attention list from the typed snapshots. It
// reads no prose: every class comes from a typed field, because
// diagnostic wording is not a contract.
func (o *overview) attention() []ovItem {
	index := o.indexItems()
	integ := o.integrationItems()
	props := o.proposalItems()

	covered := map[string]bool{}

	for _, it := range append(append([]ovItem(nil), index...), integ...) {
		if it.group == ovActionable && it.area != "" {
			covered[it.area] = true
		}
	}

	items := make([]ovItem, 0, len(index)+len(integ)+len(props)+16)

	// The summary item exists only where the cards collapse into one
	// line, so every card fact stays reachable there. It waits for every
	// source, so it cannot take the first selection before the attention
	// items exist.
	if o.collapsed() && o.settled() {
		items = append(items, o.summaryItem())
	}

	items = append(items, index...)
	items = append(items, o.doctorAttention(covered)...)
	items = append(items, integ...)
	items = append(items, props...)

	// A stable sort keeps the source order inside one group and puts
	// broken states before other actionable items.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].group != items[j].group {
			return items[i].group < items[j].group
		}

		return ovToneRank(items[i].tone) < ovToneRank(items[j].tone)
	})

	return items
}

// attentionCount returns the number of attention items, without the
// summary item. ok is false until every source has loaded or failed,
// because a partial count would read as a result.
func (o *overview) attentionCount() (n int, ok bool) {
	if !o.settled() {
		return 0, false
	}

	for _, it := range o.attention() {
		if it.group != ovSummary {
			n++
		}
	}

	return n, true
}

// ovToneRank orders items inside a group: broken, then attention, then
// the rest.
func ovToneRank(t Tone) int {
	switch t {
	case ToneFailure:
		return 0
	case ToneAttention:
		return 1
	default:
		return 2
	}
}

// summaryItem is the entry to every summary fact in full. The one-line
// summary of the collapsed layout keeps one value per card, and this
// item keeps every card fact reachable there.
func (o *overview) summaryItem() ovItem {
	tone, badge := ToneNeutral, "all facts"
	if o.summaryStale() {
		tone, badge = ToneAttention, "stale"
	}

	return ovItem{
		key: ovSummaryKey, group: ovSummary, tone: tone, badge: badge,
		title: "Workspace summary", source: "status, integrations, ledger, doctor",
		summary: true,
	}
}

// settled reports that every attention source has loaded or failed.
func (o *overview) settled() bool {
	return len(o.pendingSources()) == 0 && o.st.doctor.loaded
}

// indexItems classifies the index probe and the semantic health.
func (o *overview) indexItems() []ovItem {
	sec := &o.st.index
	if !sec.loaded {
		// A first read that failed has no snapshot to classify, but
		// its error is still a fact to act on.
		if sec.err != nil {
			return []ovItem{ovHealthFailedItem(sec.err)}
		}

		return nil
	}

	v := sec.value
	path := v.Probe.Path

	if path == "" {
		path = o.st.svc.DBPath()
	}

	schema := "The database uses schema v" + itoa(v.Probe.Schema) + ". This seamark reads v" +
		itoa(v.Probe.Supported) + "."

	switch v.Probe.State {
	case workspace.IndexAbsent:
		return []ovItem{{
			key: "index:state", group: ovActionable, tone: ToneAttention, badge: "absent",
			title: "No index yet", source: "index database", area: "index",
			body: []string{
				"No index database exists at " + path + ".",
				"Navigation, history and lesson answers need the index. Integration inspection works without it.",
			},
			fix: "seamark index", act: ovBuildAction,
		}}
	case workspace.IndexUnreadable:
		return []ovItem{{
			key: "index:state", group: ovActionable, tone: ToneFailure, badge: "unreadable",
			title: "Index unreadable", source: "index database", area: "index",
			body: []string{
				"The database file at " + path + " exists, but Seamark cannot read it.",
				"Reason: " + errText(v.Probe.Err),
			},
			fix: o.doctorFix("integrity", "index"), act: ovBuildAction,
		}}
	case workspace.IndexNewer:
		return []ovItem{{
			key: "index:state", group: ovActionable, tone: ToneFailure, badge: "newer schema",
			title: "Index written by a newer seamark", source: "index database", area: "index",
			body: []string{
				schema,
				"Seamark never rebuilds or deletes a newer database. Upgrade seamark to read it.",
			},
		}}
	case workspace.IndexOlder:
		return []ovItem{{
			key: "index:state", group: ovActionable, tone: ToneAttention, badge: "older schema",
			title: "Index needs an upgrade", source: "index database", area: "index",
			body: []string{
				schema + " The console opens the index read-only, so it cannot use this database.",
				"A build migrates the schema and rebuilds the derived data. Proposals and mined lessons stay.",
			},
			fix: "seamark index", act: ovBuildAction,
		}}
	case workspace.IndexNotIndexed:
		return []ovItem{{
			key: "index:state", group: ovActionable, tone: ToneAttention, badge: "not indexed",
			title: "Index has decisions but no index run", source: "index database", area: "index",
			body: []string{
				"The database holds decisions, for example from `seamark state import`, but no index run.",
				"A build adds the code graph and the git history. The decisions stay.",
			},
			fix: "seamark index", act: ovBuildAction,
		}}
	}

	return o.healthItems(v)
}

// ovHealthFailedItem reports a semantic health read that failed with
// no earlier snapshot to show.
func ovHealthFailedItem(err error) ovItem {
	return ovItem{
		key: "index:health", group: ovActionable, tone: ToneFailure, badge: "read failed",
		title: "Semantic health unreadable", source: "seamark status (read-only)", area: "index",
		body: []string{
			"The index is ready, but reading its semantic health failed: " + errText(err),
			"Refresh the view (r) to read it again. The CLI reads the same data.",
		},
		fix: "seamark status",
	}
}

// healthItems classifies a ready index: a failed health read, then
// freshness, coverage and parse errors.
func (o *overview) healthItems(v IndexView) []ovItem {
	s := v.Status
	if s == nil {
		return []ovItem{ovHealthFailedItem(v.StatusErr)}
	}

	var items []ovItem

	switch s.Freshness {
	case status.FreshCurrent:
	case status.FreshStale:
		source := "index freshness"
		if at := ovIndexedAt(v); at > 0 {
			source = "index " + o.st.theme.Glyphs.Sep + " built " + ovAge(at, time.Now())
		}

		items = append(items, ovItem{
			key: "index:freshness", group: ovActionable, tone: ToneAttention, badge: "stale",
			title: "Index is stale", source: source, area: "index",
			body: []string{
				"Files changed since the last index run, so answers can miss recent changes.",
				"Co-change, blast radius and lessons for the changed files may be missing or out of date.",
			},
			fix: "seamark index", act: ovBuildAction,
		})
	default:
		items = append(items, ovItem{
			key: "index:freshness", group: ovUnknown, tone: ToneUnknown, badge: "unknown",
			title: "Index freshness unknown", source: "index freshness",
			body: []string{
				"The index records no git fingerprint, for example outside a git repository. " +
					"Seamark cannot tell whether the index matches the files.",
				"Build the index again when in doubt.",
			},
			fix: "seamark index", act: ovBuildAction,
		})
	}

	if !s.Coverage.Known {
		items = append(items, ovItem{
			key: "index:coverage", group: ovUnknown, tone: ToneUnknown, badge: "unknown",
			title: "Index coverage unknown", source: "index coverage",
			body: []string{
				"An older index did not record its file coverage, so Seamark cannot tell how many files it parsed.",
				"A build records the coverage.",
			},
			fix: "seamark index", act: ovBuildAction,
		})
	}

	if s.Coverage.ParseErrors > 0 {
		n := s.Coverage.ParseErrors
		items = append(items, ovItem{
			key: "index:parse", group: ovActionable, tone: ToneAttention, badge: "parse errors",
			title: ovPlural(n, "file fails", "files fail") + " to parse", source: "index coverage",
			body: []string{
				ovPlural(n, "file", "files") + " failed to parse in the last index run. " +
					"Seamark answers nothing about them.",
				"A rebuild from scratch lists each parse warning. The CLI prints them too.",
			},
			fix: "seamark index --force", act: ovBuildAction,
		})
	}

	return items
}

// doctorFix returns the Fix of the first named doctor check that has
// one. The doctor Fix is the shared corrective text, so the console
// repeats it instead of writing its own.
func (o *overview) doctorFix(names ...string) string {
	r := o.st.doctor.value
	if r == nil {
		return ""
	}

	for _, name := range names {
		for _, c := range r.Checks {
			if c.Name == name && c.Fix != "" {
				return c.Fix
			}
		}
	}

	return ""
}

// ovCapLabels are the capability names in the doctor vocabulary.
var ovCapLabels = map[integration.Capability]string{
	integration.CapabilitySkills:          "skills",
	integration.CapabilityMCPRegistration: "MCP registration",
	integration.CapabilityToolGrants:      "tool grants",
	integration.CapabilityEdits:           "lessons hook",
	integration.CapabilityCommands:        "gate hook",
	integration.CapabilityResets:          "context reset hook",
}

// ovCapPurpose says what each capability does, so an item explains why
// its state matters.
var ovCapPurpose = map[integration.Capability]string{
	integration.CapabilitySkills: "Managed skills tell the agent when and how to use the Seamark tools.",
	integration.CapabilityMCPRegistration: "The MCP registration lets the agent call the Seamark tools " +
		"(orient, why, change_set, check).",
	integration.CapabilityToolGrants: "Tool grants let the Seamark tools run without a prompt. " +
		"They cover project configuration only; user or managed policy can still prompt.",
	integration.CapabilityEdits:    "The lessons hook shows relevant review lessons before the agent edits a file.",
	integration.CapabilityCommands: "The gate hook checks shell commands against the repository policy.",
	integration.CapabilityResets:   "The context reset hook tells Seamark that the agent compacted its context.",
}

// ovCapAbout names the ABOUT section of each capability. The three hooks
// share one title, as in the 1a detail pane.
var ovCapAbout = map[integration.Capability]string{
	integration.CapabilitySkills:          "About skills",
	integration.CapabilityMCPRegistration: "About the MCP registration",
	integration.CapabilityToolGrants:      "About tool grants",
	integration.CapabilityEdits:           "About the hook",
	integration.CapabilityCommands:        "About the hook",
	integration.CapabilityResets:          "About the hook",
}

// ovCapSections returns the detail sections of a capability item: the
// adapter's detail, then what the capability does. The detail heading
// is EFFECT for a missing capability, because the adapter then explains
// what the absence changes, and STATE otherwise. A capability without
// an adapter detail gets no such section, because Describe would only
// repeat the state word of the title.
func ovCapSections(c integration.CapabilityInspection) []ovSection {
	var out []ovSection

	if c.Detail != "" {
		title := "State"
		if c.State == integration.StateAbsent {
			title = "Effect"
		}

		out = append(out, ovSection{title: title, text: ovSentenceEnd(c.Detail)})
	}

	if purpose := ovCapPurpose[c.Capability]; purpose != "" {
		out = append(out, ovSection{title: ovCapAbout[c.Capability], text: purpose})
	}

	return out
}

// ovSentenceEnd turns a detail phrase into a sentence: a capital first
// letter and a final period. Adapter details are lower-case phrases,
// because the CLI prints them inside its own lines.
func ovSentenceEnd(s string) string {
	s = ovSentence(strings.TrimSpace(s))
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}

	return s
}

// ovStatePurpose explains each non-current configuration state.
var ovStatePurpose = map[integration.CapabilityState]string{
	integration.StateAbsent:  "Seamark has not configured this. It is optional.",
	integration.StatePartial: "Some Seamark entries are present, but others are missing or outdated.",
	integration.StateConflict: "Foreign or restrictive settings stop Seamark from configuring this. " +
		"Setup never overwrites them.",
	integration.StateUnreadable: "The native configuration cannot be read or parsed, so its state is unknown.",
}

// ovCapLabel returns the capability name in the doctor vocabulary.
func ovCapLabel(c integration.Capability) string {
	if l, ok := ovCapLabels[c]; ok {
		return l
	}

	return string(c)
}

// ovCapShort returns the short capability name for list rows, where
// "MCP not registered" reads better than "MCP registration not
// registered".
func ovCapShort(c integration.Capability) string {
	if c == integration.CapabilityMCPRegistration {
		return "MCP"
	}

	return ovCapLabel(c)
}

// ovCapArea maps a capability to the doctor check that reports it.
func ovCapArea(c integration.Capability) string {
	switch c {
	case integration.CapabilitySkills:
		return "skills"
	case integration.CapabilityMCPRegistration:
		return "mcp"
	case integration.CapabilityToolGrants:
		return "approvals"
	default:
		return "hooks"
	}
}

// ovSetupInstalls reports whether setup can configure the capability
// for the client. A hook that setup never installs is not missing.
func ovSetupInstalls(insp integration.Inspection, c integration.Capability) bool {
	switch c {
	case integration.CapabilitySkills:
		return true
	case integration.CapabilityMCPRegistration:
		return insp.Setup.RegisterMCP
	case integration.CapabilityToolGrants:
		return insp.Setup.ApproveTools
	case integration.CapabilityEdits:
		return insp.Setup.Hooks
	case integration.CapabilityCommands:
		return insp.Setup.Hooks && insp.Setup.GateHook
	case integration.CapabilityResets:
		return insp.Setup.Hooks && insp.Setup.ResetHook
	default:
		return false
	}
}

// ovClientName returns the display name of a client.
func ovClientName(insp integration.Inspection) string {
	if insp.Name != "" {
		return insp.Name
	}

	return insp.ClientID
}

// ovNothingConfigured reports a client without any Seamark
// configuration: every supported capability is absent.
func ovNothingConfigured(insp integration.Inspection) bool {
	for _, c := range insp.Capabilities {
		if c.Supported && c.State != integration.StateAbsent {
			return false
		}
	}

	return true
}

// ovStateWord names a capability state in the words the CLI uses. It
// asks Describe for the absent wording, without the adapter detail, so
// "not registered" and "not installed" match doctor and status.
func ovStateWord(c integration.CapabilityInspection) string {
	bare := c
	bare.Detail = ""

	return bare.Describe()
}

// integrationItems classifies every client's capabilities, trust,
// compatibility evidence and findings.
func (o *overview) integrationItems() []ovItem {
	sec := &o.st.integrations
	if !sec.loaded {
		return nil
	}

	// The separator comes from the theme, so the ASCII glyph set
	// leaves no Unicode in the titles.
	dot := " " + o.st.theme.Glyphs.Sep + " "

	var items []ovItem

	for _, insp := range sec.value {
		items = append(items, ovClientItems(insp, dot)...)
	}

	return items
}

// ovClientItems classifies one client. dot is the separator between
// the client name and the fact.
func ovClientItems(insp integration.Inspection, dot string) []ovItem {
	id, name := insp.ClientID, ovClientName(insp)
	nothing := ovNothingConfigured(insp)

	var items []ovItem

	if nothing {
		items = append(items, ovItem{
			key: "client:" + id, group: ovOptional, tone: ToneNeutral, badge: "not set up",
			title: name + dot + "not set up", source: name + dot + "integration",
			body: []string{
				name + " has no Seamark configuration in this repository. Setting it up is optional.",
				"Setup shows every file change for review before it writes anything.",
			},
			act: ovNav(destIntegrations, "setup:"+id, "set up "+name),
		})
	}

	for _, c := range insp.Capabilities {
		if it, ok := ovCapabilityItem(insp, c, nothing, dot); ok {
			items = append(items, it)
		}
	}

	items = append(items, ovTrustItems(insp, dot)...)
	items = append(items, ovFindingItems(insp, dot)...)

	return items
}

// ovCapabilityItem classifies one capability. A client that is not set
// up at all gets one Optional item instead of one per capability.
func ovCapabilityItem(insp integration.Inspection, c integration.CapabilityInspection, nothing bool, dot string) (ovItem, bool) {
	id, name := insp.ClientID, ovClientName(insp)
	label, short := ovCapLabel(c.Capability), ovCapShort(c.Capability)

	it := ovItem{
		key:    "cap:" + id + ":" + string(c.Capability),
		source: name + dot + label,
		area:   ovCapArea(c.Capability),
		badge:  c.State.String(),
		fix:    c.Action,
	}

	it.sections = ovCapSections(c)

	switch {
	case !c.Supported:
		it.group, it.tone, it.badge = ovUnsupported, ToneUnsupported, "not supported"
		it.title = name + dot + short + " not supported"
		it.body = []string{name + " does not offer this capability, so Seamark cannot configure it."}
		it.fix = ""

		return it, true
	case c.State == integration.StateCurrent:
		return ovItem{}, false
	case c.State == integration.StateAbsent && nothing:
		return ovItem{}, false
	case c.State == integration.StateAbsent && !ovSetupInstalls(insp, c.Capability):
		// The client can handle the event, but setup never installs
		// this hook for it. Nothing is missing and nothing can be added.
		it.group, it.tone, it.badge = ovUnsupported, ToneUnsupported, "not installed by setup"
		it.title = name + dot + short + " not installed by setup"
		it.head = name + dot + label
		it.body = []string{"Seamark setup does not install the " + label + " for " + name + "."}

		return it, true
	case c.State == integration.StateAbsent:
		it.group, it.tone = ovOptional, ToneNeutral
		it.title = name + dot + short + " " + ovStateWord(c)
		it.act = ovNav(destIntegrations, "setup:"+id, "set up "+name)
	default:
		it.group, it.tone = ovActionable, ToneAttention

		if c.State == integration.StateUnreadable {
			it.tone = ToneFailure
		}

		it.title = name + dot + short + " " + c.State.String()
		it.act = ovRepairAction(insp, c)
	}

	it.body = []string{ovStatePurpose[c.State]}

	return it, true
}

// ovRepairAction picks the workflow for a broken capability. Setup can
// complete a partial entry; a conflict or an unreadable file needs the
// user, so the action opens the client details instead.
func ovRepairAction(insp integration.Inspection, c integration.CapabilityInspection) ovAction {
	id, name := insp.ClientID, ovClientName(insp)

	if c.State == integration.StatePartial {
		if c.Capability == integration.CapabilitySkills {
			return ovNav(destIntegrations, "skills:"+id, "refresh "+name+" skills")
		}

		if ovSetupInstalls(insp, c.Capability) {
			return ovNav(destIntegrations, "setup:"+id, "repair "+name+" in setup")
		}
	}

	return ovNav(destIntegrations, "client:"+id, "inspect "+name)
}

// ovTrustItems reports project trust and compatibility evidence for the
// configured capabilities. Both stay apart from the configuration
// state: a current file says nothing about trust or native behaviour.
func ovTrustItems(insp integration.Inspection, dot string) []ovItem {
	id, name := insp.ClientID, ovClientName(insp)
	inspect := ovNav(destIntegrations, "client:"+id, "inspect "+name)

	var (
		unknownTrust, pendingTrust bool
		evidence                   []string
	)

	for _, c := range insp.Capabilities {
		if !c.Supported || c.State == integration.StateAbsent {
			continue
		}

		switch c.Trust {
		case integration.TrustUnknown:
			unknownTrust = true
		case integration.TrustPending:
			pendingTrust = true
		}

		if c.State == integration.StateUnreadable {
			continue
		}

		switch c.Verification.Level {
		case integration.VerificationUnknown:
			evidence = append(evidence, ovCapLabel(c.Capability)+" (unknown)")
		case integration.VerificationPending:
			evidence = append(evidence, ovCapLabel(c.Capability)+" (check pending)")
		}
	}

	var items []ovItem

	notes := ovInfoNotes(insp)

	if pendingTrust {
		items = append(items, ovItem{
			key: "trust-pending:" + id, group: ovActionable, tone: ToneAttention, badge: "trust pending",
			title: name + dot + "project trust pending", source: name + dot + "project trust",
			body: append([]string{
				name + " waits for the user to review and trust this project's configuration.",
				"Seamark reports trust. It never grants it.",
			}, notes...),
			act: inspect,
		})
	}

	if unknownTrust {
		items = append(items, ovItem{
			key: "trust:" + id, group: ovUnknown, tone: ToneUnknown, badge: "unknown",
			title: name + dot + "trust not verifiable by Seamark", source: name + dot + "project trust",
			body: append([]string{
				"Seamark cannot read whether " + name + " trusts this project's configuration.",
				"Configuration state and project trust are separate. A current file can still wait " +
					"for the user's review in the client. Seamark never grants trust.",
			}, notes...),
			act: inspect,
		})
	}

	if len(evidence) > 0 {
		body := []string{
			"No recorded native check covers: " + strings.Join(evidence, ", ") + ".",
			"Compatibility evidence covers one tested client version and surface. " +
				"It does not show that the hooks run in this project.",
		}

		if all := insp.DescribeVerification(); all != "" {
			body = append(body, "Recorded evidence: "+all+".")
		}

		items = append(items, ovItem{
			key: "evidence:" + id, group: ovUnknown, tone: ToneUnknown, badge: "unknown",
			title: name + dot + "compatibility evidence incomplete", source: name + dot + "native compatibility evidence",
			body: body, act: inspect,
		})
	}

	return items
}

// ovInfoNotes renders the informational findings of a client for the
// trust item, because they explain the limits of what Seamark reads.
func ovInfoNotes(insp integration.Inspection) []string {
	var notes []string

	for _, f := range insp.Findings {
		if f.Level != integration.FindingInfo {
			continue
		}

		note := "Note"
		if f.Path != "" {
			note += " (" + f.Path + ")"
		}

		note += ": " + f.Reason + "."

		if f.Action != "" {
			note += " Next: " + f.Action + "."
		}

		notes = append(notes, note)
	}

	return notes
}

// ovFindingItems lists warnings and errors of a client as actionable.
// Informational findings go to the trust item instead.
func ovFindingItems(insp integration.Inspection, dot string) []ovItem {
	id, name := insp.ClientID, ovClientName(insp)

	var items []ovItem

	for i, f := range insp.Findings {
		if f.Level == integration.FindingInfo {
			continue
		}

		tone := ToneAttention
		if f.Level == integration.FindingError {
			tone = ToneFailure
		}

		source := name + dot + "finding"
		if f.Path != "" {
			source = name + dot + f.Path
		}

		items = append(items, ovItem{
			key:   "finding:" + id + ":" + itoa(i) + ":" + f.Path,
			group: ovActionable, tone: tone, badge: f.Level.String(),
			title: name + dot + f.Reason, source: source,
			body: []string{f.Reason + "."},
			fix:  f.Action, area: "client:" + id,
			act: ovNav(destIntegrations, "client:"+id, "inspect "+name),
		})
	}

	return items
}

// proposalItems reports pending decisions, or an unknown count when the
// ledger read failed. A failed read never counts as zero.
func (o *overview) proposalItems() []ovItem {
	sec := &o.st.ledger

	if sec.loaded && sec.value != nil {
		n := len(sec.value.Pending)
		if n == 0 {
			return nil
		}

		body := []string{
			ovPlural(n, "distilled proposal waits", "distilled proposals wait") +
				" for a decision: apply as a pin, or dismiss.",
			"Nothing changes until you confirm a preview in Lessons.",
		}

		if sec.stale() {
			body = append(body, "This count is stale: the last read failed ("+errText(sec.err)+").")
		}

		// A pending decision is attention, not a failure: it sorts after
		// broken states, and its glyph is the warn "!" of the group.
		return []ovItem{{
			key: "proposals", group: ovActionable, tone: ToneAttention, badge: "pending",
			title: ovPlural(n, "proposal awaits", "proposals await") + " a decision", source: "decision ledger",
			body: body, fix: "seamark lessons --proposals",
			act: ovNav(destLessons, "proposals", "review proposals"),
		}}
	}

	// Without a readable index the index item already explains the
	// missing ledger; a second item would say the same thing.
	if sec.err == nil || !o.indexReadable() {
		return nil
	}

	return []ovItem{{
		key: "proposals", group: ovUnknown, tone: ToneUnknown, badge: "unknown",
		title: "Pending proposal count unknown", source: "decision ledger",
		body: []string{
			"Reading the decision ledger failed: " + errText(sec.err),
			"The count stays unknown until a read works. It is not zero.",
		},
		fix: "seamark lessons --proposals",
	}}
}

// indexReadable reports an index that the console can open read-only.
func (o *overview) indexReadable() bool {
	return o.st.index.loaded && o.st.index.value.Probe.State.Readable()
}

// ledgerUnavailable reports a ledger failure that comes from a missing
// or unreadable index, not from the ledger itself.
func ledgerUnavailable(err error) bool {
	return errors.Is(err, ErrNoLedger)
}

// doctorAttention lists doctor warnings and failures as actionable. A
// check whose area an actionable capability or index item already
// covers stays in the Doctor tab only, so the list states one fact once.
func (o *overview) doctorAttention(covered map[string]bool) []ovItem {
	r := o.st.doctor.value
	if r == nil {
		return nil
	}

	clients := map[string]bool{}
	for _, insp := range o.st.integrations.value {
		clients[insp.ClientID] = true
	}

	var items []ovItem

	for i := range r.Checks {
		c := r.Checks[i]
		if c.State != doctor.StateWarn && c.State != doctor.StateFail {
			continue
		}

		area := c.Name
		if clients[c.Name] {
			area = "client:" + c.Name
		}

		if covered[area] {
			continue
		}

		tone := ovDoctorTone(c.State)

		items = append(items, ovItem{
			key:   "doctor:" + itoa(i) + ":" + c.Name,
			group: ovActionable, tone: tone, badge: c.State,
			title: c.Name + ": " + c.Detail, head: "Doctor check: " + c.Name, source: "seamark doctor",
			body: []string{c.Detail}, fix: c.Fix, act: o.doctorAction(c),
		})
	}

	return items
}

// ovDoctorTone maps a doctor state to a tone. Doctor "info" mixes
// optional, unknown and absent facts, so it stays neutral here; the
// typed groups make the finer split.
func ovDoctorTone(state string) Tone {
	switch state {
	case doctor.StateOK:
		return ToneSuccess
	case doctor.StateWarn:
		return ToneAttention
	case doctor.StateFail:
		return ToneFailure
	case doctor.StateInfo:
		return ToneNeutral
	default:
		return ToneUnknown
	}
}

// doctorAction maps a doctor check name to a known workflow. The name
// is a stable identifier; the detail text is never read.
func (o *overview) doctorAction(c doctor.Check) ovAction {
	if c.State == doctor.StateOK {
		return ovAction{}
	}

	switch c.Name {
	case "index":
		if o.st.index.loaded && o.st.index.value.Probe.State == workspace.IndexNewer {
			return ovAction{}
		}

		return ovBuildAction
	case "hooks", "mcp", "approvals":
		return ovNav(destIntegrations, "setup", "open setup")
	case "skills":
		return ovNav(destIntegrations, "", "open Integrations")
	}

	for _, insp := range o.st.integrations.value {
		if insp.ClientID == c.Name {
			return ovNav(destIntegrations, "client:"+c.Name, "inspect "+ovClientName(insp))
		}
	}

	return ovAction{}
}

// ovPlural renders a count with the singular or plural phrase.
func ovPlural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return ovNum(n) + " " + many
}

// ovNum formats a count with thousands separators, so large index
// counts stay readable.
func ovNum(n int) string {
	s := itoa(n)
	neg := strings.HasPrefix(s, "-")

	if neg {
		s = s[1:]
	}

	var b strings.Builder

	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}

		b.WriteRune(r)
	}

	if neg {
		return "-" + b.String()
	}

	return b.String()
}
