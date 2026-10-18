package maintain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// authorLessons holds a linked pin with its provenance comment, an
// unlinked pin with a region set, and settings around the list.
const authorLessons = `threshold: 3
pin:
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p1)
  - rule: linked-rule
    region: api
    note: Recorded note.
  # a person wrote this one
  - rule: Manual-Rule
    region: web
    regions: [web, docs]
    note: Keep web and docs tidy.
mute:
  - rule: noisy
`

// authorFixture is a workspace with authorLessons, the applied row p1
// of the linked pin, and the gate on. The directory web exists, docs
// does not.
func authorFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, nil, "web/index.html")
	f.write(authorLessons, 0o644)
	f.propose(model.Proposal{
		Signature: "s1", Rule: "linked-rule", Region: "api", Note: "Recorded note.",
		Agent: "claude/v2", Status: model.ProposalApplied,
	})

	return f
}

// authorPlan plans req and requires no error.
func (f *fixture) authorPlan(deps Deps, req AuthorRequest) *AuthorPlan {
	f.t.Helper()

	p, err := PlanAuthor(deps, req)
	require.NoError(f.t, err)

	return p
}

// newPin returns the request of a new pin. No region means
// repository-wide.
func newPin(rule, note string, regions ...string) AuthorRequest {
	req := AuthorRequest{Op: AuthorCreate, Rule: rule, Note: note, Scope: ScopeRepoWide}
	if len(regions) > 0 {
		req.Scope, req.Regions = ScopeRegions, regions
	}

	return req
}

// editNote returns the request of a note edit of ref.
func editNote(ref pinfile.EntryRef, note string) AuthorRequest {
	return AuthorRequest{Op: AuthorEdit, Ref: ref, Note: note, NoteSet: true}
}

// blockerLines returns the blockers of p as lines.
func blockerLines(p *AuthorPlan) []string {
	var out []string

	for _, b := range p.Blockers {
		out = append(out, b.String())
	}

	return out
}

func TestCheckDraftUsesTheSharedChecks(t *testing.T) {
	base := &reviews.PinRule{Rule: "Rule", Region: "api", Note: "Legacy note.\n"}

	cases := []struct {
		name string
		req  AuthorRequest
		base *reviews.PinRule
		want []FieldIssue
	}{
		{
			name: "a blank new form is incomplete",
			req:  AuthorRequest{Op: AuthorCreate},
			want: []FieldIssue{
				{Field: FieldRule, Index: -1, Text: "invalid text: the rule is empty"},
				{Field: FieldNote, Index: -1, Text: issueNoNote},
				{Field: FieldScope, Index: -1, Text: issueScope},
			},
		},
		{
			name: "a complete repository-wide pin",
			req:  newPin("rule", "Guidance.\n\twith a tab"),
		},
		{
			name: "a note of only white space is no guidance",
			req:  newPin("rule", " \n\t"),
			want: []FieldIssue{{Field: FieldNote, Index: -1, Text: issueNoNote}},
		},
		{
			name: "the text checks name the character",
			req:  newPin("ru\u202ele", "a note\n"),
			want: []FieldIssue{
				{Field: FieldRule, Index: -1, Text: "invalid text: the text contains the control character U+202E"},
				{Field: FieldNote, Index: -1, Text: "invalid text: the note ends with an empty line; remove it"},
			},
		},
		{
			name: "region rows are normalized and the changes are named",
			req:  newPin("rule", "note", " ./api/ ", "docs"),
			want: []FieldIssue{
				{Field: FieldRegions, Index: 0, Text: "surrounding spaces removed", Info: true},
				{Field: FieldRegions, Index: 0, Text: `leading "./" removed`, Info: true},
				{Field: FieldRegions, Index: 0, Text: `trailing "/" removed`, Info: true},
			},
		},
		{
			name: "a repeat is found after the normalization",
			req:  newPin("rule", "note", "./api", "api/", "api", "db"),
			want: []FieldIssue{
				{Field: FieldRegions, Index: 0, Text: `leading "./" removed`, Info: true},
				{Field: FieldRegions, Index: 1, Text: `trailing "/" removed`, Info: true},
				{Field: FieldRegions, Index: 1, Text: "region 2 repeats region 1"},
				{Field: FieldRegions, Index: 2, Text: "region 3 repeats region 1"},
			},
		},
		{
			// The pin key removes one trailing "/" more than the check.
			// The check refuses "//", so no hidden repeat reaches the key.
			name: "a double slash is refused before the repeat check",
			req:  newPin("rule", "note", "api//", "api"),
			want: []FieldIssue{{
				Field: FieldRegions, Index: 0,
				Text: `invalid region: the region contains "//"; use one "/" between path names`,
			}},
		},
		{
			name: "a refused region keeps its row",
			req:  newPin("rule", "note", "api", "../x"),
			want: []FieldIssue{{
				Field: FieldRegions, Index: 1,
				Text: `invalid region: the region contains ".."; it must stay inside the repository`,
			}},
		},
		{
			name: "these regions need a region",
			req:  AuthorRequest{Op: AuthorCreate, Rule: "rule", Note: "note", Scope: ScopeRegions},
			want: []FieldIssue{{Field: FieldRegions, Index: -1, Text: issueNoRegion}},
		},
		{
			name: "repository-wide takes no regions",
			req:  AuthorRequest{Op: AuthorCreate, Rule: "rule", Note: "note", Scope: ScopeRepoWide, Regions: []string{"api"}},
			want: []FieldIssue{{Field: FieldRegions, Index: -1, Text: issueRepoRegions}},
		},
		{
			name: "an edit checks no field that it does not set",
			req:  AuthorRequest{Op: AuthorEdit, Rule: "", Note: ""},
			base: base,
		},
		{
			name: "an edit checks no value equal to the entry",
			req:  editNote(pinfile.EntryRef{}, "Legacy note.\n"),
			base: base,
		},
		{
			name: "an edited note meets the text check",
			req:  editNote(pinfile.EntryRef{}, "Legacy note, edited.\n"),
			base: base,
			want: []FieldIssue{{Field: FieldNote, Index: -1, Text: "invalid text: the note ends with an empty line; remove it"}},
		},
		{
			name: "an edit can clear the note while the rule stays",
			req:  editNote(pinfile.EntryRef{}, ""),
			base: base,
		},
		{
			name: "an edit cannot clear the note and the rule",
			req:  AuthorRequest{Op: AuthorEdit, Rule: "", RuleSet: true, NoteSet: true},
			base: base,
			want: []FieldIssue{
				{Field: FieldRule, Index: -1, Text: "invalid text: the rule is empty"},
				{Field: FieldNote, Index: -1, Text: issueBareRule},
			},
		},
		{
			name: "an edited note of only spaces is refused",
			req:  editNote(pinfile.EntryRef{}, "   "),
			base: base,
			want: []FieldIssue{{Field: FieldNote, Index: -1, Text: issueBlankNote}},
		},
		{
			name: "an edited scope needs a choice",
			req:  AuthorRequest{Op: AuthorEdit, ScopeSet: true},
			base: base,
			want: []FieldIssue{{Field: FieldScope, Index: -1, Text: issueScope}},
		},
		{
			// A set scope is checked as a whole, as an edited note is. So a
			// legacy region meets the region check, also when it is equal
			// to the entry.
			name: "a set scope checks every row, also a legacy one",
			req:  AuthorRequest{Op: AuthorEdit, Scope: ScopeRegions, Regions: []string{"src/*"}, ScopeSet: true},
			base: &reviews.PinRule{Rule: "Rule", Region: "src/*", Note: "Legacy."},
			want: []FieldIssue{{
				Field: FieldRegions, Index: 0,
				Text: `invalid region: the region contains "*"; regions are literal path prefixes, ` +
					"and repository-wide is a separate choice",
			}},
		},
		{
			name: "a removal has no field",
			req:  AuthorRequest{Op: AuthorRemove, Rule: "", Scope: ScopeUnset},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CheckDraft(tc.req, tc.base))
		})
	}
}

func TestPlanAuthorCreateAppendsAfterTheLastPin(t *testing.T) {
	f := authorFixture(t)
	ledger := f.ledger()

	p := f.authorPlan(f.deps(), newPin("new-rule", "Line one.\nLine two.", "./web/", "docs"))

	require.True(t, p.Confirmable, "blocked: %v issues: %v", p.Blockers, p.Issues)
	assert.Empty(t, p.Blockers)
	assert.True(t, p.LinksKnown)
	assert.True(t, p.Write)
	assert.Equal(t, f.root, p.Root)
	assert.True(t, p.Guard.Exists)
	assert.Equal(t, authorLessons, string(p.Before))

	// The normalized values reach the file: no "./", no trailing "/".
	assert.Equal(t, strings.Replace(authorLessons, "mute:\n", `  - rule: new-rule
    region: web
    regions: [web, docs]
    note: |-
      Line one.
      Line two.
mute:
`, 1), string(p.After))
	assert.Contains(t, p.Diff, "+  - rule: new-rule")
	assert.Equal(t, 6, p.Added)
	assert.Equal(t, 0, p.Removed)

	assert.Equal(t, reviews.PinRule{Rule: "new-rule", Region: "web", Regions: []string{"web", "docs"},
		Note: "Line one.\nLine two."}, p.New)
	assert.Equal(t, reviews.PinRule{}, p.Old)
	assert.Equal(t, []string{"docs"}, p.Missing, "docs names no path yet; web does")
	assert.False(t, p.NewOwners.Any())

	require.NotNil(t, p.NewRef)
	assert.Equal(t, 2, p.NewRef.Index)
	assert.Equal(t, reviews.NewPinKey("new-rule", "", []string{"web", "docs"}), p.NewRef.Key)

	assert.Equal(t, []string{noteCreateLast, textLedgerEffect, noteRedacted}, p.Notes)
	assert.Equal(t, authorLessons, f.lessons(), "a plan writes nothing")
	assert.Equal(t, ledger, f.ledger())
}

func TestPlanAuthorCreatesAnAbsentFileWithThePinAlone(t *testing.T) {
	f := newFixture(t, nil)

	p := f.authorPlan(f.deps(), newPin("first", "The first pin."))

	require.True(t, p.Confirmable, "blocked: %v", p.Blockers)
	assert.False(t, p.Guard.Exists)
	assert.Nil(t, p.Before)
	assert.Equal(t, "pin:\n  - rule: first\n    region: '*'\n    note: The first pin.\n", string(p.After))
	assert.Empty(t, p.Missing, "repository-wide names no region")
}

func TestPlanAuthorEditChangesOnlyTheSetFields(t *testing.T) {
	cases := []struct {
		name       string
		req        func(ref pinfile.EntryRef) AuthorRequest
		old, new   string
		keyChanged bool
	}{
		{
			name: "the note",
			req:  func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, "Keep web and docs clean.") },
			old:  "    note: Keep web and docs tidy.\n", new: "    note: Keep web and docs clean.\n",
		},
		{
			name: "the rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Rule: "renamed-rule", RuleSet: true}
			},
			old: "  - rule: Manual-Rule\n", new: "  - rule: renamed-rule\n", keyChanged: true,
		},
		{
			name: "the scope",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"web"}, ScopeSet: true}
			},
			old: "    region: web\n    regions: [web, docs]\n", new: "    region: web\n", keyChanged: true,
		},
		{
			name: "repository-wide",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRepoWide, ScopeSet: true}
			},
			old: "    region: web\n    regions: [web, docs]\n", new: "    region: '*'\n", keyChanged: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := authorFixture(t)
			ref := f.ref("Manual-Rule", "")

			p := f.authorPlan(f.deps(), tc.req(ref))

			require.True(t, p.Confirmable, "blocked: %v issues: %v", p.Blockers, p.Issues)
			assert.Equal(t, pinfile.LinkNoProposal, p.Link)
			assert.Equal(t, strings.Replace(authorLessons, tc.old, tc.new, 1), string(p.After))
			assert.Equal(t, tc.keyChanged, p.KeyChanged)
			assert.True(t, p.IdentityChanged)
			assert.Equal(t, []string{textIdentityChanged, textLedgerEffect, noteRedacted}, p.Notes)

			require.NotNil(t, p.NewRef)
			assert.Equal(t, ref.Index, p.NewRef.Index)
			assert.Equal(t, reviews.NewPinKey(p.New.Rule, p.New.Region, p.New.Regions), p.NewRef.Key)
		})
	}
}

func TestPlanAuthorRemovesAnUnlinkedEntryWithItsProvenanceRun(t *testing.T) {
	f := newFixture(t, nil)

	// p7 is no row of this ledger, so the entry is unlinked. Its
	// provenance run goes with it. A run that stays would read as the
	// provenance of the next entry.
	lessons := `pin:
  - rule: keep
    region: api
    note: Stays.
  # distilled by claude/v2 from 2 findings (seamark lessons --distill, p7)
  - rule: drop
    region: web
    note: Goes.
  - rule: also-keep
    region: db
    note: Stays too.
`
	f.write(lessons, 0o644)

	p := f.authorPlan(f.deps(), AuthorRequest{Op: AuthorRemove, Ref: f.ref("drop", "")})

	require.True(t, p.Confirmable, "blocked: %v", p.Blockers)
	assert.Equal(t, `pin:
  - rule: keep
    region: api
    note: Stays.
  - rule: also-keep
    region: db
    note: Stays too.
`, string(p.After))
	assert.Equal(t, reviews.PinRule{Rule: "drop", Region: "web", Note: "Goes."}, p.Old)
	assert.Equal(t, reviews.PinRule{}, p.New)
	assert.Nil(t, p.NewRef)
	assert.Equal(t, 4, p.Removed)
	assert.Equal(t, []string{textLedgerEffect, noteRedacted}, p.Notes)
}

func TestPlanAuthorIdentityConsequence(t *testing.T) {
	const inline = `pin:
  - rule: Pool-Reset
    region: pkg/b
    regions: [pkg/b, pkg/a]
    note: Reset in Free and clone.
`

	// A literal block keeps the line break at the end of the note.
	const literal = `pin:
  - rule: Pool-Reset
    region: pkg/b
    regions: [pkg/b, pkg/a]
    note: |
      Reset in Free and clone.
`

	note := func(n string) func(ref pinfile.EntryRef) AuthorRequest {
		return func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, n) }
	}

	cases := []struct {
		name    string
		lessons string
		req     func(ref pinfile.EntryRef) AuthorRequest
		changed bool
	}{
		{name: "note case only", lessons: inline, req: note("reset in free and CLONE."), changed: false},
		{name: "trailing note newline", lessons: literal, req: note("Reset in Free and clone."), changed: false},
		{name: "inner double space", lessons: inline, req: note("Reset in  Free and clone."), changed: true},
		{name: "leading space", lessons: inline, req: note(" Reset in Free and clone."), changed: true},
		{name: "cleared note", lessons: inline, req: note(""), changed: true},
		{
			name: "rule case", lessons: inline, changed: false,
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Rule: "pool-reset", RuleSet: true}
			},
		},
		{
			name: "region order", lessons: inline, changed: false,
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{
					Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"pkg/a", "pkg/b"}, ScopeSet: true,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.write(tc.lessons, 0o644)

			p := f.authorPlan(f.deps(), tc.req(f.ref("Pool-Reset", "")))

			require.True(t, p.Confirmable, "blocked: %v issues: %v", p.Blockers, p.Issues)
			assert.NotEqual(t, tc.lessons, string(p.After), "every case is a real edit")
			assert.False(t, p.KeyChanged, "no case changes the link key")
			assert.Equal(t, tc.changed, p.IdentityChanged)
			assert.Equal(t, tc.changed, reviews.PinIdentity(p.Old) != reviews.PinIdentity(p.New))

			want := textIdentityKept
			if tc.changed {
				want = textIdentityChanged
			}

			assert.Equal(t, []string{want, textLedgerEffect, noteRedacted}, p.Notes)
		})
	}
}

func TestPlanAuthorAnUnchangedDraftIsANoOp(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		req   func(ref pinfile.EntryRef) AuthorRequest
	}{
		{
			name: "no field set", entry: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest { return AuthorRequest{Op: AuthorEdit, Ref: ref} },
		},
		{
			name: "the same note", entry: "Manual-Rule",
			req: func(ref pinfile.EntryRef) AuthorRequest { return editNote(ref, "Keep web and docs tidy.") },
		},
		{name: "the same regions", entry: "Manual-Rule", req: sameScope},
		{
			// A draft that started while the relationship was unknown can
			// set the rule or the scope of a linked pin. Equal values are
			// no request for Retarget.
			name: "the same scope of a linked pin", entry: "linked-rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"api"}, ScopeSet: true}
			},
		},
		{
			name: "the same rule of a linked pin", entry: "linked-rule",
			req: func(ref pinfile.EntryRef) AuthorRequest {
				return AuthorRequest{Op: AuthorEdit, Ref: ref, Rule: "linked-rule", RuleSet: true}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := authorFixture(t)

			p := f.authorPlan(f.deps(), tc.req(f.ref(tc.entry, "")))

			assert.True(t, p.NoOp)
			assert.False(t, p.Confirmable)
			assert.Empty(t, p.Blockers, "a no-op is a state, not a blocker")
			assert.Nil(t, p.After)
			assert.Equal(t, []string{noteNoOp}, p.Notes)
		})
	}
}

// sameScope returns a scope edit of ref to its current region set.
func sameScope(ref pinfile.EntryRef) AuthorRequest {
	return AuthorRequest{Op: AuthorEdit, Ref: ref, Scope: ScopeRegions, Regions: []string{"web", "docs"}, ScopeSet: true}
}

func TestPlanAuthorListsTheHistoryOfTheKeyAsHistory(t *testing.T) {
	f := authorFixture(t)
	f.propose(
		model.Proposal{Signature: "s2", Rule: "new-rule", Region: "api", Note: "Old.", Status: model.ProposalSuperseded},
		model.Proposal{Signature: "s3", Rule: "other", Region: "api", Note: "Other key.", Status: model.ProposalSuperseded},
		model.Proposal{Signature: "s4", Rule: "NEW-RULE", Region: "api/", Note: "Dismissed.", Status: model.ProposalDismissed},
	)

	p := f.authorPlan(f.deps(), newPin("new-rule", "Fresh guidance.", "api"))

	require.True(t, p.Confirmable, "history never blocks a new pin: %v", p.Blockers)
	assert.Equal(t, []int64{2, 4}, proposalIDs(p.History))
	assert.False(t, p.NewOwners.Any())
}

func TestPlanAuthorWithABlockingIssueHasNoCandidate(t *testing.T) {
	cases := []struct {
		name    string
		regions []string
		want    []FieldIssue
	}{
		{
			name: "a repeat after the trailing slash", regions: []string{"api", "api/"},
			want: []FieldIssue{
				{Field: FieldRegions, Index: 1, Text: `trailing "/" removed`, Info: true},
				{Field: FieldRegions, Index: 1, Text: "region 2 repeats region 1"},
			},
		},
		{
			// The file would hold "api/" and "api", which the pin key
			// reads as one region. The region check refuses "//" first.
			name: "a double slash that would hide a repeat", regions: []string{"api//", "api"},
			want: []FieldIssue{{
				Field: FieldRegions, Index: 0,
				Text: `invalid region: the region contains "//"; use one "/" between path names`,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := authorFixture(t)

			p := f.authorPlan(f.deps(), newPin("rule", "note", tc.regions...))

			assert.Equal(t, tc.want, p.Issues)
			assert.True(t, Blocking(p.Issues))

			if p.Issues[0].Info {
				assert.False(t, Blocking(p.Issues[:1]), "a normalization line never blocks")
			}

			assert.Nil(t, p.After)
			assert.Equal(t, reviews.PinRule{}, p.New)
			assert.Empty(t, p.Blockers, "an issue is no blocker")
			assert.False(t, p.Confirmable)
		})
	}
}

func TestPlanAuthorNewIsThePinThatAfterHolds(t *testing.T) {
	// An entry with only a regions key keeps that layout on a rescope.
	// So New must come from the candidate, not from the layout of
	// reviews.PinForRegions.
	f := newFixture(t, nil)
	f.write("pin:\n  - rule: spread\n    regions: [a, b]\n    note: Two regions.\n", 0o644)

	p := f.authorPlan(f.deps(), AuthorRequest{
		Op: AuthorEdit, Ref: f.ref("spread", ""), Scope: ScopeRegions, Regions: []string{"c", "d"}, ScopeSet: true,
	})

	require.True(t, p.Confirmable, "blocked: %v issues: %v", blockerLines(p), p.Issues)
	assert.Equal(t, "pin:\n  - rule: spread\n    regions: [c, d]\n    note: Two regions.\n", string(p.After))
	require.NotNil(t, p.NewRef)

	d := pinfile.Parse(p.After)
	require.Greater(t, len(d.Entries), p.NewRef.Index)
	assert.Equal(t, pinRule(d.Entries[p.NewRef.Index]), p.New, "New is the entry that the review writes")
	assert.Equal(t, reviews.PinRule{Rule: "spread", Regions: []string{"c", "d"}, Note: "Two regions."}, p.New)
	assert.True(t, p.KeyChanged)
	assert.True(t, p.IdentityChanged)
}

func TestPlanAuthorRefusesInvalidRequests(t *testing.T) {
	f := authorFixture(t)

	_, err := PlanAuthor(f.deps(), AuthorRequest{})
	require.EqualError(t, err, "maintain: unknown authoring operation AuthorOp(0)")

	_, err = PlanAuthor(f.deps(), AuthorRequest{Op: AuthorEdit})
	require.ErrorIs(t, err, ErrNothingSelected)

	_, err = PlanAuthor(f.deps(), AuthorRequest{Op: AuthorRemove})
	require.ErrorIs(t, err, ErrNothingSelected)

	deps := f.deps()
	deps.Root = ""

	_, err = PlanAuthor(deps, newPin("rule", "note"))
	require.EqualError(t, err, "maintain: workspace root is empty")
}

func TestPlanAuthorBlocksAnEntryThatChanged(t *testing.T) {
	f := authorFixture(t)
	ref := f.ref("Manual-Rule", "")

	f.write(strings.Replace(authorLessons, "tidy.", "tidy, edited.", 1), 0o644)

	p := f.authorPlan(f.deps(), editNote(ref, "A new note."))

	assert.Equal(t, []string{textEntryGone + "; " + routeRefresh}, blockerLines(p))
	assert.Equal(t, BlockEntry, p.Blockers[0].Kind)
	assert.Nil(t, p.After)
	assert.False(t, p.Confirmable)
}
