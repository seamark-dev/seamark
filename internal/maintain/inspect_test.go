package maintain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/confidence"
	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/outcome"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// day is one day of the injected clock.
const day = 24 * time.Hour

// inspectFindings cite two scripts files 10 and 40 days before the
// injected clock, and one api file.
var inspectFindings = []model.Finding{
	{ID: 1, LessonKey: "k", Path: "scripts/a.py", PR: 1, Source: model.SourceReview,
		CreatedAt: fixtureNow.Add(-10 * day).Unix()},
	{ID: 2, LessonKey: "k", Path: "scripts/b.py", PR: 2, Source: model.SourceReview,
		CreatedAt: fixtureNow.Add(-40 * day).Unix()},
	{ID: 3, LessonKey: "k", Path: "api/h.go", PR: 3, Source: model.SourceReview,
		CreatedAt: fixtureNow.Add(-5 * day).Unix()},
}

// inspectLessons holds one pin per relationship state, in this order:
// linked, linked with an edited note, no proposal, pending match, two
// duplicates, several applied rows, a linked twin of the first rule, and
// an unlinked twin.
const inspectLessons = `pin:
  - rule: guard-empty
    region: scripts
    note: Guard empty datasets.
  - rule: edited-note
    region: scripts
    note: The live note.
  - rule: no-row
    region: web
    note: No proposal.
  - rule: pending-one
    region: api
    note: Pending.
  - rule: dup
    region: db
    note: First.
  - rule: dup
    region: db
    note: Second.
  - rule: two-rows
    region: lib
    note: Two rows.
  - rule: guard-empty
    region: api
    note: Guard empty datasets in api.
  - rule: guard-empty
    region: web
    note: A hand-written twin.
`

// inspectFixture seeds inspectLessons and the rows of every state. p7 has
// no entry, and p8 is superseded history. The linked rows cite different
// finding sets, because equal sets alone make proposals restate.
func inspectFixture(t *testing.T) *fixture {
	t.Helper()

	f := newFixture(t, inspectFindings, "scripts/a.py", "scripts/b.py", "api/h.go")
	f.write(inspectLessons, 0o644)
	f.propose(
		model.Proposal{Signature: "s1", Rule: "guard-empty", Region: "scripts", Note: "Guard empty datasets.",
			Members: []int64{1, 2}, Status: model.ProposalApplied},
		model.Proposal{Signature: "s2", Rule: "edited-note", Region: "scripts", Note: "The recorded note.",
			Members: []int64{2}, Status: model.ProposalApplied,
			TriggerPaths: []string{"scripts/a.py"}, TriggerChecked: 1_700_000_000, TriggerPromptVersion: 3},
		model.Proposal{Signature: "s3", Rule: "pending-one", Region: "api", Members: []int64{1},
			Status: model.ProposalProposed},
		model.Proposal{Signature: "s4", Rule: "dup", Region: "db", Members: []int64{1}, Status: model.ProposalApplied},
		model.Proposal{Signature: "s5", Rule: "two-rows", Region: "lib", Members: []int64{1}, Status: model.ProposalApplied},
		model.Proposal{Signature: "s6", Rule: "two-rows", Region: "lib", Members: []int64{2}, Status: model.ProposalApplied},
		model.Proposal{Signature: "s7", Rule: "guard-empty", Region: "docs", Members: []int64{1},
			Status: model.ProposalApplied},
		model.Proposal{Signature: "s8", Rule: "old-rule", Region: "web", Members: []int64{1},
			Status: model.ProposalSuperseded},
		model.Proposal{Signature: "s9", Rule: "guard-empty", Region: "api", Note: "Guard empty datasets in api.",
			Members: []int64{3}, Status: model.ProposalApplied},
	)

	return f
}

// links returns the link of every pin.
func links(in *Inspection) []pinfile.Link {
	out := make([]pinfile.Link, len(in.Pins))
	for i, p := range in.Pins {
		out[i] = p.Link.Link
	}

	return out
}

// ids returns the IDs of rows.
func ids(rows []model.Proposal) []int64 {
	var out []int64
	for _, p := range rows {
		out = append(out, p.ID)
	}

	return out
}

func TestInspectDistinguishesEveryState(t *testing.T) {
	f := inspectFixture(t)

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)

	require.NoError(t, in.DocErr)
	require.NoError(t, in.LedgerErr)
	require.NoError(t, in.LinkErr)
	require.NoError(t, in.ReadingsErr)
	assert.Equal(t, fixtureNow, in.At)
	assert.Equal(t, f.probe(), in.Index)
	require.Len(t, in.Pins, 9)

	assert.Equal(t, []pinfile.Link{
		pinfile.LinkLinked, pinfile.LinkLinked, pinfile.LinkNoProposal, pinfile.LinkPendingMatch,
		pinfile.LinkAmbiguousFile, pinfile.LinkAmbiguousFile, pinfile.LinkAmbiguousLedger,
		pinfile.LinkLinked, pinfile.LinkNoProposal,
	}, links(in))

	assert.False(t, in.Pins[0].Link.NoteChanged)
	assert.True(t, in.Pins[1].Link.NoteChanged, "the live note differs from the recorded note")
	assert.Equal(t, []int64{5, 6}, ids(in.Pins[6].Link.Applied), "an ambiguous entry lists every match")
	assert.Equal(t, []int64{7}, ids(in.Links.NotPresent), "an applied row without an entry is not present")
	assert.Equal(t, []int64{8}, ids(in.Superseded))
	assert.Equal(t, [][]int{{0, 7}}, in.Clusters, "only present, linked pins are compared")
	assert.Equal(t, []int{7}, in.Pins[0].Readings.Similar)
	assert.Equal(t, []int{0}, in.Pins[7].Readings.Similar)
	assert.Empty(t, in.Pins[8].Readings.Similar, "an unlinked twin is not a retained alternative")

	for _, i := range []int{2, 3, 4, 6, 8} {
		r := in.Pins[i].Readings
		reason := in.Pins[i].Link.Link.Reason()

		assert.False(t, r.Confidence.Known, "pin %d", i)
		assert.Equal(t, reason, r.Confidence.Reason, "pin %d", i)
		assert.Equal(t, reason, r.Scope.Reason, "pin %d", i)
		assert.Equal(t, reason, r.Trigger.Reason, "pin %d", i)
		assert.Equal(t, reason, r.Outcome.Reason, "pin %d", i)
		assert.True(t, r.Exposure.Known, "every pin has a delivery history, pin %d", i)
	}
}

func TestInspectReadsTheLinkedPins(t *testing.T) {
	f := inspectFixture(t)
	f.fire(reviews.PinRule{Rule: "guard-empty", Region: "scripts", Note: "Guard empty datasets."}, "2026-09-01T00:00:00Z")
	f.fire(reviews.PinRule{Rule: "no-row", Region: "web", Note: "No proposal."}, "2026-09-02T00:00:00Z")

	// The recorded wording of pin 1 fired once. The agent no longer gets
	// that wording, so it is not the delivery history of the live pin.
	f.fire(reviews.PinRule{Rule: "edited-note", Region: "scripts", Note: "The recorded note."}, "2026-09-03T00:00:00Z")

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.NoError(t, in.ReadingsErr)

	r := in.Pins[0].Readings

	assert.True(t, r.Confidence.Known)
	assert.Equal(t, confidence.TierFair, r.Confidence.Tier)
	assert.Equal(t, confidence.Facts{
		Cited: 2, Living: 2, Events: 2, Sources: []string{"review"}, NewestAge: 10 * day, AgeKnown: true,
	}, r.Confidence.Facts, "the age uses the injected clock")
	assert.Len(t, r.Confidence.Living, 2)

	assert.True(t, r.Scope.Known)
	assert.Equal(t, distill.ScopeCurrent, r.Scope.Advice.Kind)
	assert.Equal(t, []string{"scripts"}, r.Scope.Advice.Regions)

	assert.True(t, r.Trigger.Known)
	assert.True(t, r.Trigger.Open)
	assert.Equal(t, distill.TriggerOpen, r.Trigger.State)

	assert.True(t, r.Outcome.Known)
	assert.True(t, r.Outcome.Reading.Exposed)
	assert.Equal(t, int64(1), r.Outcome.Reading.ProposalID)

	assert.True(t, r.Exposure.Known)
	assert.True(t, r.Exposure.Fired)
	assert.Equal(t, 1, r.Exposure.Exposure.Count)

	// The edited note is a new delivery identity, and the stored trigger
	// answer closes its question.
	e := in.Pins[1].Readings
	assert.True(t, e.Exposure.Known)
	assert.False(t, e.Exposure.Fired)
	assert.False(t, e.Trigger.Open)
	assert.Equal(t, distill.TriggerAnsweredPositive, e.Trigger.State)
	assert.True(t, e.Outcome.Known)
	assert.False(t, e.Outcome.Reading.Exposed)
	assert.Equal(t, outcome.VerdictUntested, e.Outcome.Reading.Verdict)
	assert.Equal(t, outcome.ReasonNeverFired, e.Outcome.Reading.Reason)

	// An unlinked pin still has its delivery history.
	assert.True(t, in.Pins[2].Readings.Exposure.Fired)

	// Both readings come from one read of the firing log, so they agree.
	for i, p := range in.Pins {
		if p.Link.Link == pinfile.LinkLinked {
			assert.Equal(t, p.Readings.Exposure.Fired, p.Readings.Outcome.Reading.Exposed, "pin %d", i)
		}
	}
}

func TestInspectReadsTheLiveIdentityOfAnEditedPin(t *testing.T) {
	f := inspectFixture(t)
	f.fire(reviews.PinRule{Rule: "edited-note", Region: "scripts", Note: "The live note."}, "2026-09-03T00:00:00Z")

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.NoError(t, in.ReadingsErr)

	e := in.Pins[1].Readings
	require.True(t, in.Pins[1].Link.NoteChanged)
	assert.True(t, e.Exposure.Fired, "the live wording fired")
	assert.True(t, e.Outcome.Reading.Exposed)
}

func TestInspectClustersTheLiveWording(t *testing.T) {
	const (
		guardNote   = "Validate every uploaded dataset against emptiness before reductions."
		timeoutNote = "Prefer explicit timeouts around outbound network calls everywhere."
	)

	// The two rules share no word, so only the notes can restate. Each
	// row cites other findings, because equal evidence restates too.
	cases := []struct {
		name               string
		liveBeta, recorded string
		want               [][]int
	}{
		{name: "a live edit restates another pin", liveBeta: guardNote, recorded: timeoutNote, want: [][]int{{0, 1}}},
		{name: "only the recorded wording restates", liveBeta: timeoutNote, recorded: guardNote},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, inspectFindings, "scripts/a.py", "scripts/b.py", "api/h.go")
			f.write("pin:\n  - rule: alpha-check\n    region: scripts\n    note: "+guardNote+"\n"+
				"  - rule: beta-guard\n    region: api\n    note: "+tc.liveBeta+"\n", 0o644)
			f.propose(
				model.Proposal{Signature: "a", Rule: "alpha-check", Region: "scripts", Note: guardNote,
					Members: []int64{1}, Status: model.ProposalApplied},
				model.Proposal{Signature: "b", Rule: "beta-guard", Region: "api", Note: tc.recorded,
					Members: []int64{3}, Status: model.ProposalApplied},
			)

			in := Inspect(f.root, f.st, f.probe(), fixtureNow)
			require.NoError(t, in.ReadingsErr)
			require.Equal(t, []pinfile.Link{pinfile.LinkLinked, pinfile.LinkLinked}, links(in))
			assert.Equal(t, tc.want, in.Clusters)
		})
	}
}

func TestInspectReportsBlockedTriggers(t *testing.T) {
	f := newFixture(t, nil, "scripts/a.py", "lib/x.go")
	f.write("pin:\n  - rule: moved-trigger\n    region: scripts\n    note: Moved.\n", 0o644)

	// Both triggers are cited evidence. lib/gone.go is no longer in the
	// working tree, so the region set cannot deliver it.
	require.NoError(t, f.st.ReplaceLessons(nil, []model.Finding{{
		ID: 1, PR: 1, Path: "scripts/a.py", Paths: []string{"scripts/a.py", "lib/x.go", "lib/gone.go"},
		Source: model.SourceFixConventional,
	}}))
	f.propose(model.Proposal{Signature: "s1", Rule: "moved-trigger", Region: "scripts", Members: []int64{1},
		Status: model.ProposalApplied, TriggerPaths: []string{"lib/gone.go", "lib/x.go"}})

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.NoError(t, in.ReadingsErr)

	r := in.Pins[0].Readings
	require.True(t, r.Scope.Known)
	assert.Equal(t, distill.ScopeChanged, r.Scope.Advice.Kind)
	assert.Equal(t, []string{"lib/x.go"}, r.Scope.Advice.Regions)

	require.Len(t, r.Trigger.Blocked, 1, "the trigger reading reuses the facts of the scope advice")
	assert.Equal(t, "lib/gone.go", r.Trigger.Blocked[0].Path)
	assert.NotEmpty(t, r.Trigger.Blocked[0].BlockedLine())
}

func TestInspectScopeNeedsAReadyIndex(t *testing.T) {
	f := inspectFixture(t)

	probe := f.probe()
	probe.State = workspace.IndexNotIndexed

	in := Inspect(f.root, f.st, probe, fixtureNow)

	r := in.Pins[0].Readings
	assert.False(t, r.Scope.Known)
	assert.Equal(t, noIndexReason(probe), r.Scope.Reason)
	assert.True(t, r.Confidence.Known, "the other readings stay")
	assert.True(t, r.Trigger.Known)
	assert.Empty(t, r.Trigger.Blocked)
}

func TestInspectWithoutALedgerKeepsThePinsListed(t *testing.T) {
	check := func(t *testing.T, in *Inspection) {
		t.Helper()

		require.ErrorIs(t, in.LedgerErr, ErrNoLedger)
		assert.Equal(t, in.LedgerErr, in.LinkErr)
		assert.Nil(t, in.Links, "no link is claimed without a ledger")
		assert.Nil(t, in.Clusters)
		require.Len(t, in.Pins, 9, "the pin file stays inspectable")

		for i, p := range in.Pins {
			assert.Equal(t, pinfile.LinkUnknown, p.Link.Link, "pin %d", i)
			assert.False(t, p.Readings.Confidence.Known)
			assert.Equal(t, in.LinkErr.Error(), p.Readings.Confidence.Reason)
			assert.Equal(t, in.LinkErr.Error(), p.Readings.Outcome.Reason)
			assert.True(t, p.Readings.Exposure.Known, "the firing log needs no ledger")
		}
	}

	t.Run("no store", func(t *testing.T) {
		f := inspectFixture(t)
		check(t, Inspect(f.root, nil, workspace.IndexProbe{State: workspace.IndexAbsent}, fixtureNow))
	})

	t.Run("a store that fails", func(t *testing.T) {
		f := inspectFixture(t)
		require.NoError(t, f.st.Close())
		check(t, Inspect(f.root, f.st, f.probe(), fixtureNow))
	})
}

func TestInspectPrefersAnUnreadableFileOverAMissingLedger(t *testing.T) {
	f := inspectFixture(t)
	f.write(inspectLessons+"threshold: lots\n", 0o644)

	in := Inspect(f.root, nil, workspace.IndexProbe{State: workspace.IndexAbsent}, fixtureNow)

	// Without a readable file no pin is known to be present or absent, so
	// the file is the first thing to fix, not the index.
	require.ErrorIs(t, in.LinkErr, pinfile.ErrUnreadable)
	require.ErrorIs(t, in.LedgerErr, ErrNoLedger)
	require.Len(t, in.Pins, 9, "the listing keeps what still decodes")

	for i, p := range in.Pins {
		r := p.Readings

		for _, reason := range []string{r.Confidence.Reason, r.Scope.Reason, r.Trigger.Reason, r.Outcome.Reason} {
			assert.Equal(t, pinfile.ErrUnreadable.Error(), reason, "pin %d", i)
		}
	}
}

func TestInspectOfAnAbsentFileMakesEveryAppliedRowNotPresent(t *testing.T) {
	f := inspectFixture(t)
	require.NoError(t, os.Remove(f.pinPath()))

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.NoError(t, in.DocErr)
	require.NoError(t, in.LinkErr)
	assert.Empty(t, in.Pins)
	assert.False(t, in.Doc.Guard.Exists)
	assert.Equal(t, []int64{1, 2, 4, 5, 6, 7, 9}, ids(in.Links.NotPresent))
}

func TestInspectNeverTurnsAReadErrorIntoAbsence(t *testing.T) {
	cases := []struct {
		name    string
		content string
		pins    int
	}{
		{name: "a reader error", content: inspectLessons + "threshold: lots\n", pins: 9},
		{name: "a syntax error", content: "pin: [\n", pins: 0},
		{
			// The reader delivers the pin through an alias, so an empty
			// listing is not an absence.
			name:    "a listing that misses delivered pins",
			content: "defs: &p\n  - rule: guard-empty\n    region: scripts\n    note: Guard empty datasets.\npin: *p\n",
			pins:    0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := inspectFixture(t)
			f.write(tc.content, 0o644)

			in := Inspect(f.root, f.st, f.probe(), fixtureNow)
			require.NoError(t, in.DocErr)
			require.ErrorIs(t, in.LinkErr, pinfile.ErrUnreadable)
			assert.Nil(t, in.Links.NotPresent, "no applied row reads as not present")
			assert.Equal(t, pinfile.ErrUnreadable, in.Links.NotPresentErr)
			assert.Len(t, in.Pins, tc.pins, "the listing keeps what still decodes")
			assert.Equal(t, []int64{8}, ids(in.Superseded), "the history stays readable")

			for _, p := range in.Pins {
				assert.Equal(t, pinfile.LinkUnknown, p.Link.Link)
				assert.Equal(t, pinfile.ErrUnreadable.Error(), p.Readings.Scope.Reason)
			}
		})
	}

	t.Run("a path that is not a file", func(t *testing.T) {
		f := inspectFixture(t)
		require.NoError(t, os.Remove(f.pinPath()))
		require.NoError(t, os.Mkdir(f.pinPath(), 0o755))

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.Error(t, in.DocErr)
		assert.Nil(t, in.Doc)
		require.ErrorIs(t, in.LinkErr, pinfile.ErrUnreadable)
		assert.Empty(t, in.Pins)
		assert.Nil(t, in.Links.NotPresent)
	})
}

func TestInspectReportsAnUnreadableFiringLog(t *testing.T) {
	f := inspectFixture(t)
	require.NoError(t, os.Mkdir(reviews.AuditLogPath(f.root), 0o755))

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.Error(t, in.ReadingsErr)
	assert.Equal(t, 1, strings.Count(in.ReadingsErr.Error(), "read the firing log"), "the log is read once")

	for _, p := range in.Pins {
		assert.False(t, p.Readings.Exposure.Known)
		assert.Equal(t, reasonNoFiringLog, p.Readings.Exposure.Reason)
	}

	r := in.Pins[0].Readings
	assert.False(t, r.Outcome.Known)
	assert.Equal(t, reasonNoFiringLog, r.Outcome.Reason)
	assert.True(t, r.Confidence.Known, "the other readings stay")
}

func TestInspectReadsUnknownWithAReason(t *testing.T) {
	linked := []int{0, 1, 7}

	t.Run("the cited findings cannot be read", func(t *testing.T) {
		f := inspectFixture(t)
		f.exec("ALTER TABLE finding RENAME TO finding_moved")

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.NoError(t, in.LinkErr, "the ledger still reads")
		require.ErrorContains(t, in.ReadingsErr, "read the cited findings")

		for _, i := range linked {
			r := in.Pins[i].Readings
			want := "unknown: the cited findings could not be read"

			assert.False(t, r.Confidence.Known || r.Scope.Known || r.Trigger.Known || r.Outcome.Known, "pin %d", i)
			assert.Equal(t, []string{want, want, want, want},
				[]string{r.Confidence.Reason, r.Scope.Reason, r.Trigger.Reason, r.Outcome.Reason}, "pin %d", i)
			assert.True(t, r.Exposure.Known, "the firing log needs no finding")
		}
	})

	t.Run("the scope cannot be computed", func(t *testing.T) {
		f := inspectFixture(t)
		f.exec("ALTER TABLE cochange RENAME TO cochange_moved")

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)

		// Only p2 has a trigger that needs the co-change history: its
		// trigger scripts/a.py is not the cited path scripts/b.py.
		require.ErrorContains(t, in.ReadingsErr, "compute the scope of p2")

		r := in.Pins[1].Readings
		assert.False(t, r.Scope.Known)
		assert.Equal(t, "unknown: the scope could not be computed", r.Scope.Reason)
		assert.True(t, r.Confidence.Known, "the other readings stay")
		assert.True(t, r.Trigger.Known)
		assert.True(t, in.Pins[0].Readings.Scope.Known, "a pin without a trigger needs no history")
	})

	t.Run("the outcome cannot be measured", func(t *testing.T) {
		f := inspectFixture(t)
		f.exec("ALTER TABLE decision RENAME TO decision_moved")

		// An exposed pin makes the outcome engine count the commits.
		f.fire(reviews.PinRule{Rule: "guard-empty", Region: "scripts", Note: "Guard empty datasets."},
			"2026-09-01T00:00:00Z")

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.ErrorContains(t, in.ReadingsErr, "measure the outcomes")

		// The error of outcome.Gather follows the prefix directly. The
		// inspection thus does not use proposals.MeasureOutcomes, which
		// adds a second prefix to the console text.
		assert.Contains(t, in.ReadingsErr.Error(),
			"measure the outcomes: failed to count pre-exposure commits for {guard-empty scripts [] Guard empty datasets.}: "+
				"store: count commits touching [scripts]: ")

		for _, i := range linked {
			r := in.Pins[i].Readings

			assert.False(t, r.Outcome.Known, "pin %d", i)
			assert.Equal(t, "unknown: the outcome could not be measured", r.Outcome.Reason, "pin %d", i)
			assert.True(t, r.Confidence.Known, "the other readings stay, pin %d", i)
		}
	})

	t.Run("the proposal cites no finding", func(t *testing.T) {
		f := newFixture(t, inspectFindings)
		f.write("pin:\n  - rule: bare\n    region: scripts\n    note: Bare.\n", 0o644)
		f.propose(model.Proposal{Signature: "s1", Rule: "bare", Region: "scripts", Note: "Bare.",
			Status: model.ProposalApplied})

		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.NoError(t, in.ReadingsErr)
		require.Equal(t, []pinfile.Link{pinfile.LinkLinked}, links(in))

		r := in.Pins[0].Readings
		assert.False(t, r.Outcome.Known, "no evidence is not a zero outcome")
		assert.Equal(t, "no reading: the proposal cites no finding", r.Outcome.Reason)
	})

	t.Run("a pin file that does not parse", func(t *testing.T) {
		f := inspectFixture(t)
		in := Inspect(f.root, f.st, f.probe(), fixtureNow)
		require.NoError(t, in.ReadingsErr)

		// Inspect never reaches this state, because the linked readings
		// need a file that parsed. The guard keeps a change of that rule
		// from giving a zero outcome.
		in.Doc.Bytes = []byte("threshold: lots\n")

		errs := in.readOutcomes(f.st, []int{0}, []model.Proposal{in.Pins[0].Link.Applied[0]}, firingLog{})
		require.Len(t, errs, 1)
		assert.False(t, in.Pins[0].Readings.Outcome.Known)
		assert.Equal(t, pinfile.ErrUnreadable.Error(), in.Pins[0].Readings.Outcome.Reason)
	})
}

func TestInspectWritesNothing(t *testing.T) {
	f := inspectFixture(t)

	ledger := f.ledger()
	audit := reviews.AuditLogPath(f.root)

	_, err := os.Stat(audit)
	require.ErrorIs(t, err, os.ErrNotExist)

	entries, err := os.ReadDir(filepath.Join(f.root, ".seamark"))
	require.NoError(t, err)

	in := Inspect(f.root, f.st, f.probe(), fixtureNow)
	require.NoError(t, in.ReadingsErr)

	assert.Equal(t, inspectLessons, f.lessons())
	assert.Equal(t, ledger, f.ledger())

	_, err = os.Stat(audit)
	require.ErrorIs(t, err, os.ErrNotExist, "a read never records a firing")

	after, err := os.ReadDir(filepath.Join(f.root, ".seamark"))
	require.NoError(t, err)
	assert.Equal(t, len(entries), len(after), "no new file appears")
}
