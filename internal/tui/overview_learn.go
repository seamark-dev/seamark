package tui

import (
	"errors"
	"strconv"
	"strings"

	"github.com/seamark-dev/seamark/internal/learning"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/tui/ui"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// This file holds the learning links of the Overview attention list.
// Each item comes from typed snapshot data, and enter on it opens one
// view:
//
//   - No evidence collected yet (a ready index, no stored finding, no
//     review lesson): Optional, opens Lessons › Evidence.
//   - N findings and no proposal in the ledger: Optional, opens the scope
//     step of a distillation in Lessons › Run.
//   - N findings, K distilled groups and no proposal: Optional, opens the
//     same scope step, as a check for new groups.
//   - An inference agent that does not resolve, or an inference reader
//     that fails: Action, opens Settings › inference.
//   - A pin-write, index or review reader that fails: Action, opens the
//     Settings group of that reader.
//   - Pin writing off while proposals wait: Optional, never Action, opens
//     Settings › pin writing.
//   - A running distillation, evidence refresh or trigger extraction:
//     Optional, opens Lessons › Run.
//
// The functions below state the rule of each item and its why. Two rules
// hold for all of them: a source that has not loaded adds no item, and no
// item suggests paid work that ran before or runs now.

// The Lessons intents that the learning links open. Lessons owns their
// meaning; the Overview and the Doctor only name them.
const (
	// ovIntentEvidence opens Lessons › Evidence.
	ovIntentEvidence = "evidence"
	// ovIntentDistill opens the scope step of a new distillation. The
	// step only plans, so the link starts no work.
	ovIntentDistill = "distill"
	// ovIntentRun opens Lessons › Run.
	ovIntentRun = "run"
)

// ovSettingsIntents are the open intents of the Settings groups. A link
// names the exact group, so the user lands on the form that fixes the
// problem.
var ovSettingsIntents = map[settings.Group]string{
	settings.GroupInference: "group:inference",
	settings.GroupPinWrite:  "group:pinwrite",
	settings.GroupEvidence:  "group:evidence",
	settings.GroupIndexing:  "group:indexing",
}

// ovSettingsLink returns the action that opens one Settings group. The
// Overview items and the Doctor share it, so one problem always opens
// the same group.
func ovSettingsLink(g settings.Group) ovAction {
	return ovNav(destSettings, ovSettingsIntents[g], "open "+g.String()+" settings")
}

// ovEvidenceLink opens Lessons › Evidence, where the user reviews a
// refresh before it starts.
func ovEvidenceLink(label string) ovAction {
	return ovNav(destLessons, ovIntentEvidence, label)
}

// ovConfigArea is the doctor check that an inference settings item
// overlaps. The doctor "agent" check then stays in the Doctor tab, so
// the Action group states the fact once.
const ovConfigArea = "agent"

// dot returns the separator between the parts of a source or a title,
// in the glyph set of the theme.
func (o *overview) dot() string {
	return " " + o.st.theme.Glyphs.Sep + " "
}

// lessonsPath names a Lessons sub-view as the tabs show it, for example
// "Lessons › Run". The arrow comes from the theme, so the ASCII glyph
// set leaves no Unicode in the text.
func (o *overview) lessonsPath(sub string) string {
	return "Lessons " + o.st.theme.Glyphs.Arrow + " " + sub
}

// learningItems links the learning workflow. It reports a run in
// progress, settings that block the workflow, the next evidence step,
// and the pin-write gate. Every item comes from a typed field of a
// loaded snapshot. A source that has not loaded or has failed adds no
// suggestion, because an unknown count is not zero.
//
// Only the items of configItems are actions, and configItems states
// why. Every other item is optional, because it offers a next step and
// reports no fault.
//
// Every link only opens a view, and no link starts work. A paid run, a
// network fetch or a write starts only after its own preview, so the
// user sees the cost and the effect first.
func (o *overview) learningItems() []ovItem {
	var items []ovItem

	if it, ok := o.runItem(); ok {
		items = append(items, it)
	}

	items = append(items, o.configItems()...)

	if it, ok := o.evidenceItem(); ok {
		items = append(items, it)
	}

	if it, ok := o.pinWriteItem(); ok {
		items = append(items, it)
	}

	return items
}

// runItem reports the stoppable run of this session while it runs. It
// is optional: the notice row shows the run too, and the item only
// adds a way to its detail from the list.
func (o *overview) runItem() (ovItem, bool) {
	n := o.st.run
	if n == nil {
		return ovItem{}, false
	}

	name := ui.OneLine(n.verb)

	switch o.st.lastRunKind {
	case runDistill:
		name = "Distillation"
	case runEvidence:
		name = "Evidence refresh"
	case runExtract:
		name = "Trigger extraction"
	}

	state := "running"
	if n.stopping {
		state = "stopping"
	}

	title := name + " " + state

	// A count shows only with a known denominator, as in the notice row.
	if n.total > 0 {
		title += o.dot() + strconv.Itoa(n.done) + " of " + strconv.Itoa(n.total) + " done"
	}

	run := o.lessonsPath("Run")
	body := []string{
		"The work runs while you use the other views. " + run + " shows its progress and its result.",
		"x in " + run + " asks to stop it. A stop keeps the work that is already saved.",
	}

	if n.stopping {
		body[1] = "A stop was requested. The work ends at the next safe point and keeps what is already saved."
	}

	return ovItem{
		key: "learn:run", group: ovOptional, tone: ui.ToneNeutral, badge: state,
		title: title, source: "this session", body: body,
		act: ovNav(destLessons, ovIntentRun, "open the run"),
	}, true
}

// ovReader is one reader of the settings file with the group that
// edits its fields.
type ovReader struct {
	name  string
	group settings.Group
}

// ovReaders lists the readers in the display order of Settings.
var ovReaders = []ovReader{
	{settings.ReaderAgent, settings.GroupInference},
	{settings.ReaderDistill, settings.GroupPinWrite},
	{settings.ReaderIndex, settings.GroupIndexing},
}

// configItems reports settings that block the learning workflow: a
// reader that rejects .seamark/config.yaml, or an inference agent that
// does not resolve. The console plans strictly: it never falls back to
// a default for a paid run, so these are actions, not warnings.
//
// Each item opens the Settings group of the failing section: inference
// for the agent reader and an unresolved agent, pin writing for the
// distill reader, and the group of ovIndexReaderGroup for the index
// reader.
func (o *overview) configItems() []ovItem {
	sec := &o.st.settings
	if !sec.loaded || sec.value == nil {
		return nil
	}

	snap := sec.value

	var failing []ovReader

	for _, r := range ovReaders {
		if snap.ReaderErrs[r.name] != nil {
			failing = append(failing, r)
		}
	}

	switch {
	case len(failing) > 1:
		// A file that no reader can load is one problem, not one per
		// section. The link opens the first affected group.
		return []ovItem{o.configFileItem(snap, failing)}
	case len(failing) == 1:
		return []ovItem{o.readerItem(snap, failing[0])}
	case snap.InvocationErr != nil:
		return []ovItem{{
			key: "learn:agent", group: ovActionable, tone: ui.ToneAttention, badge: "unresolved",
			title: "Inference agent unresolved", source: settings.File + o.dot() + "agent",
			area: ovConfigArea,
			body: []string{
				"The agent settings do not resolve to a command: " + ui.ErrText(snap.InvocationErr) + ".",
				"Distillation needs a resolved agent. The console never substitutes a default agent for a paid run.",
			},
			act: ovSettingsLink(settings.GroupInference),
		}}
	}

	return nil
}

// configFileItem reports a settings file that several readers reject,
// for example malformed YAML.
func (o *overview) configFileItem(snap *settings.Snapshot, failing []ovReader) ovItem {
	body := []string{"Several settings readers reject " + settings.File + "."}

	// A block reason already names the manual route, so the item then
	// repeats no fix text.
	fix := "fix " + settings.File + " by hand"

	if ovBlockExplains(snap, failing) {
		body = append(body, "Reason: "+ui.ErrText(snap.Blocked))
		fix = ""
	} else {
		for _, r := range failing {
			body = append(body, "The "+r.name+" reader: "+ui.ErrText(snap.ReaderErrs[r.name]))
		}

		// A read-only or linked file blocks the save for another cause.
		// The user must fix the reader errors and edit the file by hand.
		if snap.Blocked != nil {
			body = append(body, "The file cannot be edited here: "+ui.ErrText(snap.Blocked))
			fix = ""
		}
	}

	body = append(body, "Distillation, the evidence refresh and pin writing wait for a fix. "+
		"The console never substitutes defaults for them.")

	it := ovItem{
		key: "learn:config", group: ovActionable, tone: ui.ToneFailure, badge: "invalid",
		title: "Settings file does not load", source: settings.File,
		body: body, fix: fix,
		act: ovSettingsLink(failing[0].group),
	}

	if failing[0].name == settings.ReaderAgent {
		it.area = ovConfigArea
	}

	return it
}

// ovBlockExplains reports that the global block of the file states the
// error of every failing reader. A parse error or an unreadable file
// fails each reader with the error that the block quotes. The block
// then replaces the reader lines. A read-only or linked file blocks the
// save for another cause, so the reader errors must stay visible. Mixed
// line breaks are such a cause too, because yamledit refuses to edit
// them.
func ovBlockExplains(snap *settings.Snapshot, failing []ovReader) bool {
	if snap.Blocked == nil {
		return false
	}

	block := snap.Blocked.Error()

	for _, r := range failing {
		err := snap.ReaderErrs[r.name]

		// A reader adds its own prefix, for example "agent config:", to
		// the error that the block quotes. The comparison removes it.
		quoted := strings.Contains(block, err.Error())
		if cause := errors.Unwrap(err); cause != nil && strings.Contains(block, cause.Error()) {
			quoted = true
		}

		if !quoted {
			return false
		}
	}

	return true
}

// readerItem reports one reader that rejects its section.
func (o *overview) readerItem(snap *settings.Snapshot, r ovReader) ovItem {
	sep := o.dot()
	reason := ui.ErrText(snap.ReaderErrs[r.name])

	switch r.name {
	case settings.ReaderAgent:
		return ovItem{
			key: "learn:agent", group: ovActionable, tone: ui.ToneFailure, badge: "invalid",
			title: "Inference settings invalid", source: settings.File + sep + "agent", area: ovConfigArea,
			body: []string{
				"The agent section does not load: " + reason,
				"Distillation needs a valid inference agent. The console blocks a distillation preview until the section loads.",
			},
			fix: "fix the agent section of " + settings.File,
			act: ovSettingsLink(settings.GroupInference),
		}
	case settings.ReaderDistill:
		return ovItem{
			key: "learn:config:distill", group: ovActionable, tone: ui.ToneFailure, badge: "invalid",
			title: "Pin-write setting invalid", source: settings.File + sep + "distill",
			body: []string{
				"The distill section does not load: " + reason,
				"Apply cannot tell whether distill.write allows pin writes until the section loads.",
			},
			fix: "fix the distill section of " + settings.File,
			act: ovSettingsLink(settings.GroupPinWrite),
		}
	default:
		return ovItem{
			key: "learn:config:index", group: ovActionable, tone: ui.ToneFailure, badge: "invalid",
			title: "Index and review settings invalid", source: settings.File + sep + "index, reviews",
			body: []string{
				"The index reader rejects the file: " + reason,
				"One reader owns index and reviews. The evidence refresh stays blocked until it loads.",
			},
			fix: "fix the index or reviews section of " + settings.File,
			act: ovSettingsLink(ovIndexReaderGroup(snap)),
		}
	}
}

// ovIndexReaderGroup picks the Settings group of an index reader error.
// The reader owns index and reviews, and its error names no section. A
// YAML error names a line, so the line of reviews.window_days selects
// the evidence group. Every other error opens the indexing group.
func ovIndexReaderGroup(snap *settings.Snapshot) settings.Group {
	// Editable returns the global block first, and a read-only or linked
	// file has a block without a line. A copy without the block returns
	// the reader error with its own line. The copy only reads.
	own := *snap
	own.Blocked = nil

	var blocked *settings.BlockedError

	if !errors.As(own.Editable(settings.GroupEvidence), &blocked) || blocked.Line == 0 {
		return settings.GroupIndexing
	}

	if f, ok := snap.Field(settings.ReviewWindow); ok && f.Stored.Line == blocked.Line {
		return settings.GroupEvidence
	}

	return settings.GroupIndexing
}

// evidenceItem suggests the next evidence step of a ready index. With
// no stored finding, it suggests to collect evidence. With findings and
// an empty ledger, it suggests to prepare a distillation. A ledger with
// any proposal adds no suggestion, because only a plan can tell whether
// new groups exist.
//
// A stored review lesson is collected evidence too. So a store with
// lessons and no finding gets no suggestion, because "No evidence
// collected yet" would then be false.
//
// The item never suggests a repeat of paid work that ran before or
// runs now. A distillation sends findings to the inference agent, so a
// repeated suggestion can spend tokens and add no proposal. The ledger
// rule above, the record of distilled groups below, and the run checks
// in the body apply this rule.
//
// An empty ledger does not prove that no distillation ran, because a
// group can end without a proposal. The evidence snapshot counts the
// recorded groups, so the item says when an earlier distillation ran.
// It then offers only a check for new groups: the plan skips the
// recorded groups, and new evidence can form new ones.
func (o *overview) evidenceItem() (ovItem, bool) {
	// A run reloads the evidence and the ledger only when it ends. A
	// suggestion from the old snapshots could repeat the running work,
	// and the learn:run item already opens the run.
	if o.st.run != nil {
		return ovItem{}, false
	}

	sec := &o.st.learning
	if !sec.loaded || sec.err != nil {
		return ovItem{}, false
	}

	v := sec.value
	if v.Probe.State != workspace.IndexReady || v.CountsErr != nil {
		return ovItem{}, false
	}

	total := 0
	for _, n := range v.Findings {
		total += n
	}

	if total == 0 && v.Lessons == 0 {
		return ovItem{
			key: "learn:evidence", group: ovOptional, tone: ui.ToneNeutral, badge: "no findings",
			title: "No evidence collected yet", source: "evidence store",
			body: []string{
				"The index stores no finding from local fix commits or GitHub reviews yet. " +
					"Lessons and proposals start from this evidence.",
				o.lessonsPath("Evidence") + " shows a preview before a refresh starts. " +
					"Local fixes need no network; GitHub reviews use gh.",
			},
			sections: []ovSection{{
				title: "Plain commands",
				text:  "seamark index --fixes-only mines local fix commits. seamark index --reviews also fetches GitHub reviews.",
			}},
			act: ovEvidenceLink("collect evidence"),
		}, true
	}

	l := &o.st.ledger
	if total == 0 || !l.loaded || l.err != nil || l.value == nil {
		return ovItem{}, false
	}

	// Superseded rows count too: a prune of every applied pin leaves only
	// them, and Proposals › f still shows them, so the ledger is not empty.
	if len(l.value.Pending)+len(l.value.Applied)+len(l.value.Dismissed)+len(l.value.Superseded) > 0 {
		return ovItem{}, false
	}

	// A distillation of this session can end without a proposal. The
	// Lessons › Run view shows its result, so the list does not offer the
	// same work again. A later evidence refresh sets another run kind,
	// and its new findings can form new groups.
	if o.st.lastRunKind == runDistill {
		return ovItem{}, false
	}

	return o.distillItem(v, total), true
}

// distillItem suggests a distillation for total stored findings and an
// empty ledger. The text follows the record of distilled groups, so an
// earlier run without a proposal never reads as work that nothing did.
func (o *overview) distillItem(v learning.Snapshot, total int) ovItem {
	stored := ovPlural(total, "finding is", "findings are") + " stored: " + ovNum(v.FixFindings) +
		" from fix commits, " + ovNum(v.ReviewFindings) + " from GitHub reviews. The decision ledger holds no proposal."
	it := ovItem{
		key: "learn:distill", group: ovOptional, tone: ui.ToneNeutral, badge: "no proposals",
		title:  ovPlural(total, "finding", "findings") + ", no proposals in the ledger",
		source: "evidence store" + o.dot() + "decision ledger",
		body: []string{
			stored,
			"A distillation sends grouped findings to the inference agent, which drafts proposals for review. " +
				"No evidence group has a distillation record yet.",
			"Preparing one only plans, and nothing is sent before you confirm its preview.",
		},
		sections: []ovSection{{
			title: "Plain commands",
			text:  "seamark lessons --distill --dry-run lists the groups. seamark lessons --distill runs them.",
		}},
		act: ovNav(destLessons, ovIntentDistill, "prepare a distillation"),
	}

	if v.DistilledGroups > 0 {
		it.title = ovPlural(total, "finding", "findings") + ", " +
			ovPlural(v.DistilledGroups, "distilled group", "distilled groups") + ", no proposals"
		it.body = []string{
			stored,
			"An earlier distillation recorded " + ovPlural(v.DistilledGroups, "evidence group", "evidence groups") +
				", and the ledger holds no proposal. A new plan skips every recorded group.",
			"New evidence can form new groups, and only a plan finds them. Preparing one only plans, and " +
				"nothing is sent before you confirm its preview.",
		}
		it.act = ovNav(destLessons, ovIntentDistill, "check for new groups")
	}

	return it
}

// pinWriteItem notes pending proposals under a closed pin-write gate.
// It is optional and never an action: apply still works with the gate
// off, and it then shows the YAML block. The gate comes from the
// settings snapshot, so an unreadable section adds no note. A file that
// Settings cannot edit replaces the Settings route with the block
// reason, which names the manual route.
func (o *overview) pinWriteItem() (ovItem, bool) {
	n := o.st.pendingCount()
	if n <= 0 {
		return ovItem{}, false
	}

	sec := &o.st.settings
	if !sec.loaded || sec.value == nil {
		return ovItem{}, false
	}

	snap := sec.value
	if snap.ReaderErrs[settings.ReaderDistill] != nil || snap.Distill == nil || snap.Distill.Distill.Write {
		return ovItem{}, false
	}

	route := "Settings can turn pin writing on. Saving applies nothing: a new apply preview offers the write, and it waits for y."

	// The link stays, because Settings shows the same block in full.
	if err := snap.Editable(settings.GroupPinWrite); err != nil {
		route = settingsGroupTitle(settings.GroupPinWrite) + " cannot be edited here: " + ui.ErrText(err) + "."
	}

	return ovItem{
		key: "learn:pinwrite", group: ovOptional, tone: ui.ToneNeutral, badge: "off",
		title: "Pin writing is off", source: settings.File + o.dot() + "distill.write",
		body: []string{
			ovPlural(n, "proposal waits", "proposals wait") + " for a decision. " +
				"With distill.write off, apply shows the pin YAML block and writes no pin.",
			route,
		},
		act: ovSettingsLink(settings.GroupPinWrite),
	}, true
}
