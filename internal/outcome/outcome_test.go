package outcome

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

func TestLinkedFindingsUseOneHopIndexesAndKeepCorpusOrder(t *testing.T) {
	all := []model.Finding{
		{ID: 9, LessonKey: "other", Body: "unrelated"},
		{ID: 1, LessonKey: "cluster", Body: "cited review"},
		{ID: 2, LessonKey: "cluster", Body: "same review cluster"},
		{ID: 3, Body: "lexically grouped fix"},
		{ID: 4, Body: "area-only fix"},
	}
	groups := []distill.Group{
		{Findings: []model.Finding{all[1], all[3]}},
		{Area: true, Findings: []model.Finding{all[1], all[4]}},
	}

	got := linked([]int64{1}, indexFindingLinks(all, groups))
	require.Len(t, got, 3)
	assert.Equal(t, []int64{1, 2, 3}, []int64{got[0].ID, got[1].ID, got[2].ID})
}

// TestGather tests the whole passive loop end to end: findings and
// commits are seeded around a real RecordFiring timestamp, the audit
// log is written and read through the production code path, and every
// verdict is asserted down to its rendered sentence. One big fixture
// on purpose: it also covers FirstFirings, PinIdentity, FindPin,
// linked, assess, and Line through their consumer.
func TestGather(t *testing.T) {
	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, ".seamark", "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// The timeline is anchored on the RecordFiring call below, which
	// stamps real wall-clock time. Data "before exposure" sits an hour
	// in the past and data "after" an hour in the future, so test
	// runtime cannot shift anything across the boundary.
	now := time.Now()
	pre := now.Add(-time.Hour).Unix()
	post := now.Add(time.Hour).Unix()

	// Review findings. f3/f4 share a cluster key — that pair is P2's
	// recurrence. f4 also deliberately shares two tokens with f1
	// ("session token"), merging f1/f3/f4 into one lexical theme
	// group: p1 staying "working" asserts that KEYED findings never
	// join a pin lexically — cross-cluster recurrence comes from the
	// exact lesson_key join or not at all.
	review := []model.Finding{
		{
			ID: 1, LessonKey: "svc/auth\x00token", Path: "svc/auth/handler.go", PR: 101,
			Body:      "validate the session token before trusting caller identity",
			CreatedAt: pre, Source: "review",
		},
		{
			ID: 3, LessonKey: "svc/billing\x00rounding", Path: "svc/billing/invoice.go", PR: 102,
			Body:      "invoice rounding drops cents on partial refunds",
			CreatedAt: pre, Source: "review",
		},
		{
			ID: 4, LessonKey: "svc/billing\x00rounding", Path: "svc/billing/invoice.go", PR: 103,
			Body:      "session token cents rounding wrong again on invoice refunds",
			CreatedAt: post, Source: "review",
		},
		{
			ID: 5, LessonKey: "svc/metrics\x00buffer", Path: "svc/metrics/exporter.go", PR: 104,
			Body:      "metrics exporter buffers unbounded growth risk",
			CreatedAt: pre, Source: "review",
		},
		{
			ID: 6, LessonKey: "svc/legacy\x00cancel", Path: "svc/legacy/adapter.go", PR: 105,
			Body:      "legacy adapter swallows context cancellation errors",
			CreatedAt: pre, Source: "review",
		},
	}
	require.NoError(t, st.ReplaceLessons(nil, review))

	// Fix findings carry no cluster key, so the grouper is their only
	// join. f7/f8 share more than two salient tokens, so they group.
	// f9 tests the empty-key case: an unrelated post-exposure fix
	// finding that must not count as anyone's recurrence — p1 staying
	// "working" is the assertion that checks it.
	fixes := []model.Finding{
		{
			ID: 7, Path: "pkg/pool/resolver.go",
			Body:      "reset pooled resolver state in Free and clone",
			CreatedAt: pre, Source: "fix:conventional",
		},
		{
			ID: 8, Path: "pkg/pool/resolver.go",
			Body:      "pooled resolver clone missed the state reset",
			CreatedAt: post, Source: "fix:conventional",
		},
		{
			ID: 9, Path: "deps/gradle.txt",
			Body:      "bump gradle wrapper checksum verification",
			CreatedAt: post, Source: "fix:subject",
		},
		// f10/f11 share a directory but no vocabulary: the grouper
		// batches them into an AREA group. p7 staying untested asserts
		// that area buckets never read as recurrence.
		{
			ID: 10, Path: "svc/queue/consumer.go",
			Body:      "queue consumer acked before processing completed",
			CreatedAt: pre, Source: "fix:conventional",
		},
		{
			ID: 11, Path: "svc/queue/worker.go",
			Body:      "tune worker pod memory limits",
			CreatedAt: post, Source: "fix:subject",
		},
	}
	require.NoError(t, st.ReplaceFixFindings(fixes))

	// Region activity: svc/auth is well-tested on both sides of the
	// exposure (3 before, 6 after); svc/metrics sees a single commit
	// after — below MinExposureActivity. Billing and pool get nothing:
	// their pins must read "not landing" on recurrence alone.
	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		seedCommit := func(ref string, ts int64, file string) error {
			return tx.InsertDecision(&model.Decision{
				Kind: model.DecisionCommit, Ref: ref, TS: ts, Files: []string{file},
			})
		}

		for i := range 3 {
			if err := seedCommit("auth-pre-"+string(rune('a'+i)),
				now.Add(-2*time.Hour).Unix()+int64(i), "svc/auth/handler.go"); err != nil {
				return err
			}
		}

		for i := range 6 {
			if err := seedCommit("auth-post-"+string(rune('a'+i)),
				now.Add(10*time.Minute).Unix()+int64(i), "svc/auth/handler.go"); err != nil {
				return err
			}
		}

		return seedCommit("metrics-post-a",
			now.Add(10*time.Minute).Unix(), "svc/metrics/exporter.go")
	}))

	// The corpus must prove it was mined after exposure, or every
	// absence-of-recurrence claim below would honestly degrade to
	// "untested — evidence not mined since exposure".
	minedAt := fmt.Sprint(now.Add(2 * time.Hour).Unix())
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, minedAt))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, minedAt))

	pins := []reviews.PinRule{
		{Rule: "auth-token-check", Region: "svc/auth", Note: "validate before trusting"},
		{Rule: "billing-round-cents", Region: "svc/billing", Note: "round half-even, add cases"},
		{Rule: "metrics-bound-buffers", Region: "svc/metrics", Note: "cap exporter buffers"},
		{Rule: "legacy-honor-cancel", Region: "svc/legacy", Note: "propagate cancellation"},
		{Rule: "pool-reset-state", Region: "pkg/pool", Note: "reset in Free and clone"},
		{Rule: "queue-ack-after-processing", Region: "svc/queue", Note: "ack after processing"},
		{Rule: "ghost-rule", Region: "svc/ghost", Note: "its citations are long gone"},
	}
	cfg := &reviews.Config{Pin: pins}

	applied := []model.Proposal{
		{
			ID: 1, Rule: "auth-token-check", Region: "svc/auth",
			Members: []int64{1}, Status: model.ProposalApplied,
		},
		{
			ID: 2, Rule: "billing-round-cents", Region: "svc/billing",
			Members: []int64{3}, Status: model.ProposalApplied,
		},
		{
			ID: 3, Rule: "metrics-bound-buffers", Region: "svc/metrics",
			Members: []int64{5}, Status: model.ProposalApplied,
		},
		{
			ID: 4, Rule: "legacy-honor-cancel", Region: "svc/legacy",
			Members: []int64{6}, Status: model.ProposalApplied,
		},
		{
			ID: 5, Rule: "pruned-away", Region: "svc/gone",
			Members: []int64{1}, Status: model.ProposalApplied,
		},
		{
			ID: 6, Rule: "pool-reset-state", Region: "pkg/pool",
			Members: []int64{7}, Status: model.ProposalApplied,
		},
		{
			ID: 7, Rule: "queue-ack-after-processing", Region: "svc/queue",
			Members: []int64{10}, Status: model.ProposalApplied,
		},
		{
			ID: 8, Rule: "ghost-rule", Region: "svc/ghost",
			Members: []int64{99}, Status: model.ProposalApplied,
		},
	}

	// Exposure, through the production write path: one edit-hook firing
	// naming every pin except legacy-honor-cancel (p4, never fired) and
	// the pruned p5. Rendered exactly as surfaces render pins, so the
	// identity join is the real one.
	fired := make([]model.Lesson, 0, len(pins))

	for _, p := range pins {
		if p.Rule == "legacy-honor-cancel" {
			continue
		}

		fired = append(fired, reviews.SurfacedPin{Pin: p}.Lesson())
	}
	require.NoError(t, reviews.RecordFiring(root, "svc/auth/handler.go", "Edit", fired))

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	readings, err := Gather(st, cfg, applied, firings)
	require.NoError(t, err)

	// p5's pin is not in lessons.yaml: it gets no reading at all,
	// rather than an empty one.
	require.Len(t, readings, 7)
	assert.NotContains(t, readings, int64(5))

	// p1 — exposed, active region, no recurrence: the working sentence
	// with every number checked. f9 arrived post-exposure with an
	// empty cluster key; if "" ever gets into the review-side join,
	// this verdict flips to "not landing" and the assertion fails.
	p1 := readings[1]
	assert.True(t, p1.Exposed)
	assert.Equal(t, VerdictWorking, p1.Verdict)
	assert.Equal(t, 1, p1.PreEvents)
	assert.Equal(t, 3, p1.PreCommits)
	assert.Equal(t, 0, p1.PostEvents)
	assert.Equal(t, 6, p1.PostCommits)
	assert.Equal(t, 1, p1.Firings)
	assert.Equal(t,
		"working — flagged 1× in ~3 region-commits before exposure; 0× in 6 since (fired 1×)",
		p1.Line())

	// p2 — the review-side join: an uncited finding in the cited
	// cluster arrived after exposure. Region activity is zero, but the
	// verdict is still not landing: a recorded recurrence wins over
	// the activity minimum.
	p2 := readings[2]
	assert.Equal(t, VerdictNotLanding, p2.Verdict)
	assert.Equal(t, 1, p2.PostEvents)
	assert.Equal(t, "not landing — recurred 1× since exposure (fired 1×)", p2.Line())

	// p3 — fired, quiet region: one commit since exposure is not
	// enough history to claim anything.
	p3 := readings[3]
	assert.Equal(t, VerdictUntested, p3.Verdict)
	assert.True(t, p3.Exposed)
	assert.Equal(t, "untested — 1 region-commit since exposure (fired 1×)", p3.Line())

	// p4 — applied and live but never surfaced: untested, no numbers.
	p4 := readings[4]
	assert.Equal(t, VerdictUntested, p4.Verdict)
	assert.False(t, p4.Exposed)
	assert.Equal(t, "untested — never fired", p4.Line())

	// p6 — the fix-side join: no cluster keys anywhere, only the
	// lexical grouper ties f8 to the cited f7.
	p6 := readings[6]
	assert.Equal(t, VerdictNotLanding, p6.Verdict)
	assert.Equal(t, 1, p6.PostEvents)
	assert.Equal(t, "not landing — recurred 1× since exposure (fired 1×)", p6.Line())

	// p7 — the area-group case: f10 and f11 share only a directory,
	// so the grouper batches them into an Area group. Being in the
	// same directory does not mean the same mistake — without the
	// Area skip in linked, f11 would count as p7's recurrence and
	// this verdict would read not landing.
	p7 := readings[7]
	assert.Equal(t, VerdictUntested, p7.Verdict)
	assert.Equal(t, ReasonLowActivity, p7.Reason)
	assert.Equal(t, 0, p7.PostEvents)

	// p8 — every citation aged out of the mining window: zero
	// recurrences proves nothing here, so the verdict is untested.
	p8 := readings[8]
	assert.Equal(t, VerdictUntested, p8.Verdict)
	assert.Equal(t, ReasonDeadCitations, p8.Reason)
	assert.Equal(t,
		"untested — citations aged out of the mining window (fired 1×)", p8.Line())

	// Re-run with mining stamps OLDER than the exposure: zero
	// recurrences now proves nothing, so p1 drops from "working" to
	// untested. p2 keeps "not landing" — its recurrence is recorded,
	// and direct evidence wins over stale-evidence checks.
	staleAt := fmt.Sprint(now.Add(-2 * time.Hour).Unix())
	require.NoError(t, st.SetMeta(store.MetaFixesMinedAt, staleAt))
	require.NoError(t, st.SetMeta(store.MetaReviewsMinedAt, staleAt))

	stale, err := Gather(st, cfg, applied, firings)
	require.NoError(t, err)
	assert.Equal(t, ReasonStaleEvidence, stale[1].Reason)
	assert.Equal(t, "untested — evidence not mined since exposure (fired 1×)", stale[1].Line())
	assert.Equal(t, VerdictNotLanding, stale[2].Verdict)
}
