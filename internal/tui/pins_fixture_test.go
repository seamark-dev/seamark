package tui

import (
	"errors"
	"fmt"
	"time"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/maintain"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The fixtures of the Pins sub-view tests and captures. They build a
// maintain.Inspection by hand, so every relationship state, reading and
// error state is present without a real pin file or store.

// pinsAt is the fixed clock of the fixtures, so captures stay stable.
var pinsAt = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// pinsHostileRule is a rule with an OSC title sequence and a bell. The
// view must print it as inert text.
const pinsHostileRule = "evil-rule\x1b]0;pwned\x07-and-a-very-long-rule-name-that-keeps-going-past-the-column"

// pinsEntry builds one entry of the pin file. Raw holds the own lines,
// so Ref() differs between entries with different fields.
func pinsEntry(index, line int, rule, region string, regions []string, note string) pinfile.Entry {
	e := pinfile.Entry{
		Index: index, Line: line, EndLine: line + 2, Rule: rule, Region: region, Regions: regions, Note: note,
		Key: reviews.NewPinKey(rule, region, regions),
	}
	e.Raw = fmt.Sprintf("- rule: %s\n  region: %s\n  regions: %v\n  note: %s\n", rule, region, regions, note)

	return e
}

// pinsRow builds a ledger row.
func pinsRow(id int64, status, rule, region string, regions []string, note string) domain.Proposal {
	return domain.Proposal{
		ID: id, Status: status, Rule: rule, Region: region, Regions: regions, Note: note,
		Members: []int64{100 + id, 200 + id}, Agent: "claude/v2",
		CreatedAt: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC).Unix(),
	}
}

// pinsApplied is an applied row with the fields of entry e.
func pinsApplied(id int64, e pinfile.Entry) domain.Proposal {
	return pinsRow(id, domain.ProposalApplied, e.Rule, e.Region, e.Regions, e.Note)
}

// pinsKnown returns readings that every source could compute. Without a
// living finding, confidence.Assess counts only the citations, and
// distill.RecommendScope cannot recompute the scope, so the readings do
// the same.
func pinsKnown(tier confidence.Tier, living []domain.Finding) maintain.Readings {
	facts := confidence.Facts{Cited: 2}
	scope := distill.ScopeAdvice{Kind: distill.ScopeNoEvidence}

	if len(living) > 0 {
		facts = confidence.Facts{
			Cited: 2, Living: len(living), Events: 2, Sources: []string{"review"},
			NewestAge: 14 * 24 * time.Hour, AgeKnown: true,
		}
		scope = distill.ScopeAdvice{Kind: distill.ScopeCurrent}
	}

	return maintain.Readings{
		Confidence: maintain.ConfidenceReading{Known: true, Tier: tier, Living: living, Facts: facts},
		Scope:      maintain.ScopeReading{Known: true, Advice: scope},
		Trigger:    maintain.TriggerReading{Known: true, Open: true, State: distill.TriggerOpen},
		Outcome: maintain.OutcomeReading{
			Known: true, Reading: outcome.Reading{Verdict: outcome.VerdictUntested, Reason: outcome.ReasonNeverFired},
		},
		Exposure: maintain.ExposureReading{Known: true},
	}
}

// pinsUnknown returns readings that all carry reason, as maintain gives
// a pin without one linked proposal.
func pinsUnknown(reason string) maintain.Readings {
	return maintain.Readings{
		Confidence: maintain.ConfidenceReading{Reason: reason},
		Scope:      maintain.ScopeReading{Reason: reason},
		Trigger:    maintain.TriggerReading{Reason: reason},
		Outcome:    maintain.OutcomeReading{Reason: reason},
		Exposure:   maintain.ExposureReading{Known: true},
	}
}

// pinsFinding is a living cited finding.
func pinsFinding(id int64, pr int, path string) domain.Finding {
	return domain.Finding{ID: id, PR: pr, Reviewer: "coderabbit", Source: "review", Path: path}
}

// pinsInspection is a populated pin file with every relationship state.
// It holds a linked pin, a pin with an edited note, a similar pair, a pin
// without a proposal and with an unknown key, and a pending match. It
// also holds a duplicate pin, two applied rows, disagreeing fields, and a
// hostile CJK entry that cannot be edited. The ledger has a missing pin
// (p7) and a superseded row (p2). The readings of each pin are a state
// that the engines can give, so the captures show real combinations.
func pinsInspection() *maintain.Inspection {
	entries := []pinfile.Entry{
		pinsEntry(0, 3, "wrap-sql-errors", "internal/store", nil, "Wrap SQL errors with the query name."),
		pinsEntry(1, 7, "check-nil-errors", "internal/api", nil,
			"Check every error returned by the store before you use the value; wrap it with the call site."),
		pinsEntry(2, 12, "close-db-handles", "internal/db", nil, "Close database handles in the same function."),
		pinsEntry(3, 16, "close-database-handles", "internal/db/sql", []string{"internal/db/sql", "internal/cache"},
			"Close every database handle before the function returns."),
		pinsEntry(4, 21, "keep-changelog-current", "", nil, "Add a changelog line to every user-visible change."),
		pinsEntry(5, 25, "prefer-table-tests", "internal/cli", nil, "Write table tests for the CLI flags."),
		pinsEntry(6, 29, "dup-rule", "internal/x", nil, "First copy."),
		pinsEntry(7, 33, "dup-rule", "internal/x", nil, "Second copy."),
		pinsEntry(8, 37, "two-proposals", "docs", nil, "Keep the docs in step with the flags."),
		pinsEntry(9, 41, "日本語のルール "+pinsHostileRule, "\x1b[31mapi\u009b2J", nil,
			"ノートの説明 \x1b[2J and a very long note line that goes on and on so that the detail pane must wrap it."),
		pinsEntry(10, 46, "split-fields", "web", nil, "Keep the fields in one place."),
	}

	entries[4].Unknown = []string{"regoin"}
	entries[9].Block = "a multi-line rule value"

	p4 := pinsApplied(4, entries[0])
	p4.TriggerPaths = []string{"internal/store/sql.go", "internal/store/legacy.go"}
	p9 := pinsApplied(9, entries[1])
	p9.Note = "Check every error returned by the store before you use the value."
	p12 := pinsApplied(12, entries[2])
	p14 := pinsApplied(14, entries[3])
	p3 := pinsRow(3, domain.ProposalProposed, entries[5].Rule, entries[5].Region, nil, entries[5].Note)
	p20 := pinsApplied(20, entries[8])
	p21 := pinsApplied(21, entries[8])
	p30 := pinsApplied(30, entries[9])
	p31 := pinsRow(31, domain.ProposalApplied, "split-fields", "web", []string{"*", "api"}, entries[10].Note)
	p7 := pinsRow(7, domain.ProposalApplied, "retired-guidance", "internal/old", nil, "Use the old client.")
	p2 := pinsRow(2, domain.ProposalSuperseded, "old-rule", "internal/legacy", nil, "Avoid the legacy adapter.")

	links := []pinfile.EntryLink{
		{Link: pinfile.LinkLinked, Applied: []domain.Proposal{p4}},
		{Link: pinfile.LinkLinked, Applied: []domain.Proposal{p9}, NoteChanged: true},
		{Link: pinfile.LinkLinked, Applied: []domain.Proposal{p12}, Superseded: []domain.Proposal{p2}},
		{Link: pinfile.LinkLinked, Applied: []domain.Proposal{p14}},
		{Link: pinfile.LinkNoProposal},
		{Link: pinfile.LinkPendingMatch, Pending: []domain.Proposal{p3}},
		{Link: pinfile.LinkAmbiguousFile},
		{Link: pinfile.LinkAmbiguousFile},
		{Link: pinfile.LinkAmbiguousLedger, Applied: []domain.Proposal{p20, p21}},
		{Link: pinfile.LinkLinked, Applied: []domain.Proposal{p30}},
		{Link: pinfile.LinkAmbiguousFields, Applied: []domain.Proposal{p31}},
	}

	pins := make([]maintain.Pin, len(entries))

	for i, e := range entries {
		pins[i] = maintain.Pin{Entry: e, Link: links[i], Readings: pinsUnknown(links[i].Link.Reason())}
	}

	// p4: strong evidence, current scope, answered trigger, working. The
	// stored trigger legacy.go co-changes with the evidence but left the
	// working tree, so it is blocked. The cited sql.go still delivers the
	// pin, so the scope reads current. No retarget and no extraction is
	// eligible.
	pins[0].Readings = pinsKnown(confidence.TierStrong, []domain.Finding{pinsFinding(104, 45, "internal/store/sql.go")})
	pins[0].Readings.Trigger = maintain.TriggerReading{
		Known: true, State: distill.TriggerAnsweredPositive,
		Blocked: []distill.TriggerFact{{Path: "internal/store/legacy.go", Together: 3}},
	}
	pins[0].Readings.Outcome.Reading = outcome.Reading{
		Exposed: true, Firings: 3, Matches: 3, PreEvents: 4, PreCommits: 12, PostCommits: 9,
		Verdict: outcome.VerdictWorking,
	}
	pins[0].Readings.Exposure = maintain.ExposureReading{
		Known: true, Fired: true,
		Exposure: reviews.Exposure{First: time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC), Count: 3, Matches: 5},
	}

	// p9: the living findings cover internal/api and internal/store, so
	// a broader scope is computed. Its trigger question is open. A
	// blocked trigger needs stored trigger paths, so p9 has none. Every
	// action is eligible.
	pins[1].Readings = pinsKnown(confidence.TierFair, []domain.Finding{
		pinsFinding(109, 52, "internal/api/handler.go"), pinsFinding(209, 53, "internal/store/db.go"),
	})
	pins[1].Readings.Scope.Advice = distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Regions: []string{"internal/api", "internal/store"}, Broader: true,
	}

	// p12 and p14 restate each other.
	pins[2].Readings = pinsKnown(confidence.TierWeak, nil)
	pins[2].Readings.Similar = []int{3}
	pins[3].Readings = pinsKnown(confidence.TierFair, []domain.Finding{pinsFinding(114, 60, "internal/db/sql/conn.go")})
	pins[3].Readings.Scope = maintain.ScopeReading{
		Reason: "unknown: scope advice needs an indexed database (the index is stale); build it with `seamark index`",
	}
	pins[3].Readings.Similar = []int{2}

	// p30: no cited finding is living, so its scope cannot be computed
	// and the extraction has no evidence to send.
	pins[9].Readings = pinsKnown(confidence.TierWeak, nil)

	doc := &pinfile.Document{
		Entries: entries, Bytes: []byte("pin:\n"),
		Guard: integration.FileGuard{Path: pinfile.File, Exists: true},
	}

	return &maintain.Inspection{
		Doc: doc, Pins: pins, Superseded: []domain.Proposal{p2}, Clusters: [][]int{{2, 3}},
		Links: &pinfile.Links{
			Entries: links, NotPresent: []domain.Proposal{p7},
			ByProposal: map[int64]pinfile.Link{4: pinfile.LinkLinked, 7: pinfile.LinkNotPresent},
		},
		Index: workspace.IndexProbe{State: workspace.IndexReady, Root: "/work/repo"},
		At:    pinsAt,
	}
}

// pinsASCIIInspection is pinsInspection without the text in other
// scripts. The ASCII glyph set changes only the symbols of the console,
// so a CJK rule stays CJK; the ASCII checks use this variant.
func pinsASCIIInspection() *maintain.Inspection {
	in := pinsInspection()
	e := &in.Pins[9].Entry
	e.Rule = "ascii-rule " + pinsHostileRule
	e.Note = "A note \x1b[2J with a very long line that goes on and on so that the detail pane must wrap it."
	in.Doc.Entries[9] = *e

	return in
}

// pinsFake returns services over pinsInspection and a ledger with the
// write gate on.
func pinsFake() *fakeServices {
	f := newFake()
	f.ledger = lesLedger()
	f.pins = pinsInspection()

	return f
}

// pinsNoLedger is the inspection without a readable index: the entries
// stay listed and every link is unknown with the reason.
func pinsNoLedger() *maintain.Inspection {
	in := pinsInspection()
	in.LinkErr = fmt.Errorf("%w: no readable index", maintain.ErrNoLedger)
	in.LedgerErr = in.LinkErr
	in.Links = nil
	in.Superseded = nil
	in.Clusters = nil
	in.Index = workspace.IndexProbe{State: workspace.IndexAbsent, Root: "/work/repo"}

	for i := range in.Pins {
		in.Pins[i].Link = pinfile.EntryLink{}
		in.Pins[i].Readings = pinsUnknown(in.LinkErr.Error())
	}

	return in
}

// pinsMalformed is the inspection of a pin file whose reader rejects
// line 9. The decoded entries stay listed read-only. No row is known to
// be missing, and every link is unknown (D-5).
func pinsMalformed() *maintain.Inspection {
	in := pinsInspection()
	in.Doc.ReaderErr = errors.New("lessons config: yaml: line 9: cannot unmarshal !!map into string")
	in.Doc.ErrLine = 9
	in.LinkErr = pinfile.ErrUnreadable
	in.Links = &pinfile.Links{NotPresentErr: pinfile.ErrUnreadable, ByProposal: map[int64]pinfile.Link{}}
	in.Clusters = nil

	for i := range in.Pins {
		in.Pins[i].Link = pinfile.EntryLink{Applied: in.Pins[i].Link.Applied}
		in.Pins[i].Readings = pinsUnknown(pinfile.ErrUnreadable.Error())
	}

	return in
}
