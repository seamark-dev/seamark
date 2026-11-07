package lessonreport

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/reviews"
)

// observe builds the overview and the expansion of the first config pin
// from Inputs and returns the observation and the session windows.
func observe(t *testing.T, in Inputs) (Observation, []SessionWindow) {
	t.Helper()

	id, _ := pinOf(in.Config, 0)

	overview := mustBuild(t, in, "")
	expanded := mustBuild(t, in, id[:12])
	require.NotNil(t, expanded.Expanded)
	require.NotEmpty(t, overview.Pins)

	// The overview and the expansion read one window per session.
	o := overview.Pins[0].Observation
	assert.Equal(t, o.Linked+o.NoLinkedCorrection+o.Incomplete, len(expanded.Expanded.Sessions))

	return o, expanded.Expanded.Sessions
}

// TestObservationWindows proves the window reading of this build. Every
// delivered session reads incomplete with the reason
// session-not-captured and an unknown timing.
// The openings still follow the delivery rules of the audit.
func TestObservationWindows(t *testing.T) {
	cfg := testConfig("n")
	id, ver := pinOf(cfg, 0)

	injected := func(event, session string) reviews.Firing {
		return v2(event, session, "m-"+event, pin(id, ver, reviews.PinInjected))
	}

	t.Run("every delivered session reads incomplete", func(t *testing.T) {
		first := injected("e1", "s1")
		first.Lane = "root"

		in := Inputs{Audit: auditLines(first, injected("e2", "s2"), injected("e3", "s1")), Config: cfg, Now: fixedNow}

		o, sessions := observe(t, in)

		require.Len(t, sessions, 2, "the second injection of s1 opens no second window")

		for _, w := range sessions {
			assert.Equal(t, PartitionIncomplete, w.Partition)
			assert.Equal(t, []Code{CodeSessionNotCaptured}, w.Reasons)
			assert.Equal(t, TimingUnknown, w.Timing)
			assert.Equal(t, ver, w.PinVersion)
			assert.Empty(t, w.Endpoint)
		}

		assert.Equal(t, "root", sessions[0].Lane)
		assert.Equal(t, laneUnknown, sessions[1].Lane, "a record without a lane reads an unknown lane")

		assert.Equal(t, Observation{
			Unit: ObservationUnit, Incomplete: 2,
			IncompleteByReason: map[Code]int{CodeSessionNotCaptured: 2},
			ByTiming:           map[Timing]Partition{TimingUnknown: {Incomplete: 2}},
			Endpoint:           ObservationEndpoint,
		}, o)
	})

	t.Run("a record without a receiving context reads an unknown lane", func(t *testing.T) {
		rec := injected("e1", "s1")
		rec.Lane, rec.ContextSHA = "root", ""

		_, sessions := observe(t, Inputs{Audit: auditLines(rec), Config: cfg, Now: fixedNow})

		require.Len(t, sessions, 1)
		assert.Equal(t, laneUnknown, sessions[0].Lane)
	})

	t.Run("a session opened only by a denied call opens no window", func(t *testing.T) {
		denied := injected("e1", "denied")
		checkAudit := lessoncheck.AuditPrefix{
			Generations: 1,
			Records: []lessoncheck.AuditRecord{{
				Kind: lessoncheck.AuditGuard, MatchSHA: denied.MatchSHA,
				Guard: &lessoncheck.GuardDecision{Decision: "deny"},
			}},
		}

		r := mustBuild(t, Inputs{Audit: auditLines(denied), CheckAudit: checkAudit, Config: cfg, Now: fixedNow}, "")

		assert.Equal(t, 1, r.Coverage.DeniedCallInjections)
		assert.Equal(t, 0, r.Pins[0].Delivery.Sessions)
		assert.Equal(t, 0, r.Pins[0].Observation.Linked+r.Pins[0].Observation.NoLinkedCorrection+r.Pins[0].Observation.Incomplete)
		assert.Empty(t, r.Pins[0].Observation.ByTiming)

		t.Run("a later real injection opens it", func(t *testing.T) {
			later := injected("e2", "denied")
			in := Inputs{Audit: auditLines(denied, later), CheckAudit: checkAudit, Config: cfg, Now: fixedNow}

			_, sessions := observe(t, in)

			require.Len(t, sessions, 1)
			assert.Equal(t, "lessons-audit.jsonl:2", sessions[0].StartRecord)
		})
	})

	t.Run("an exploration injection opens a window", func(t *testing.T) {
		scan := injected("e1", "s1")
		scan.Surface = reviews.ExploreSurface

		_, sessions := observe(t, Inputs{Audit: auditLines(scan), Config: cfg, Now: fixedNow})

		require.Len(t, sessions, 1)
	})

	t.Run("a preparation record opens no window", func(t *testing.T) {
		prep := injected("e1", "s1")
		prep.Surface = surfacePrepare

		o, sessions := observe(t, Inputs{Audit: auditLines(prep), Config: cfg, Now: fixedNow})

		assert.Empty(t, sessions)
		assert.Equal(t, 0, o.Incomplete)
	})

	t.Run("the windows of an expansion follow the file order", func(t *testing.T) {
		in := Inputs{
			Audit:  auditLines(injected("e1", "b"), injected("e2", "a"), injected("e3", "c")),
			Config: cfg, Now: fixedNow,
		}

		_, sessions := observe(t, in)

		require.Len(t, sessions, 3)
		assert.Equal(t, []string{"lessons-audit.jsonl:1", "lessons-audit.jsonl:2", "lessons-audit.jsonl:3"},
			[]string{sessions[0].StartRecord, sessions[1].StartRecord, sessions[2].StartRecord})
		assert.Equal(t, []string{shortRef(digest64("b")), shortRef(digest64("a")), shortRef(digest64("c"))},
			[]string{sessions[0].SessionRef, sessions[1].SessionRef, sessions[2].SessionRef})
	})

	t.Run("check units add to no session count", func(t *testing.T) {
		in := Inputs{Audit: auditLines(injected("e1", "s1")), Config: cfg, Now: fixedNow}
		before := mustBuild(t, in, "").Pins[0]

		in.CheckAudit = lessoncheck.AuditPrefix{
			Generations: 1,
			Records: []lessoncheck.AuditRecord{
				{Kind: lessoncheck.AuditGuard, MatchSHA: digest64("unrelated"), Guard: &lessoncheck.GuardDecision{Decision: "deny"}},
				{Kind: lessoncheck.AuditGuard, MatchSHA: digest64("unrelated-2"), Guard: &lessoncheck.GuardDecision{Decision: "deny"}},
			},
		}

		after := mustBuild(t, in, "").Pins[0]

		assert.Equal(t, before, after, "guard decisions are check units of their own")
		assert.Equal(t, 1, after.Delivery.Sessions)
	})

	// The window builder keeps one result per pin version. Two config
	// entries of one version read that one result, so each reading must
	// get its own maps. Otherwise an edit of one reading changes the other.
	t.Run("two readings of one version own their maps", func(t *testing.T) {
		dup := testConfig("n")
		dup.Pin = []reviews.PinRule{dup.Pin[0], dup.Pin[0]}

		r := mustBuild(t, Inputs{Audit: auditLines(injected("e1", "s1")), Config: dup, Now: fixedNow}, "")
		require.Len(t, r.Pins, 2)

		wantReasons := map[Code]int{CodeSessionNotCaptured: 1}
		wantTiming := map[Timing]Partition{TimingUnknown: {Incomplete: 1}}

		require.Equal(t, wantReasons, r.Pins[0].Observation.IncompleteByReason)
		require.Equal(t, wantTiming, r.Pins[0].Observation.ByTiming)

		r.Pins[0].Observation.ByTiming[TimingUnknown] = Partition{Linked: 9}
		r.Pins[0].Observation.IncompleteByReason[CodeSessionNotCaptured] = 9

		assert.Equal(t, wantReasons, r.Pins[1].Observation.IncompleteByReason)
		assert.Equal(t, wantTiming, r.Pins[1].Observation.ByTiming)
	})
}
