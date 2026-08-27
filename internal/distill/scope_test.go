package distill

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The fixture shape mirrors the audit's true case:
// the pin is scoped to the repair side (web/src/api), the note names
// the trigger file (api/schemas.py), and the cited evidence file's
// strongest co-change partner is that same trigger file.

// scopeRoot creates a working tree that holds the given files, so S1
// can check that a note path exists on disk.
func scopeRoot(t *testing.T, paths ...string) string {
	t.Helper()

	root := t.TempDir()

	for _, p := range paths {
		full := filepath.Join(root, filepath.FromSlash(p))

		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte("x\n"), 0o644))
	}

	return root
}

// scopeStore opens a temporary store seeded with co-change pairs.
// Findings are passed to AuditScope as values and need no rows.
func scopeStore(t *testing.T, pairs ...model.CoChange) *store.Store {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)

	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		for _, c := range pairs {
			if err := tx.InsertCoChange(c); err != nil {
				return err
			}
		}

		return nil
	}))

	return st
}

// cochange builds one pair in the canonical file order the store
// expects, so a test cannot break the storage contract by accident.
func cochange(a, b string, together int, lift float64) model.CoChange {
	if a > b {
		a, b = b, a
	}

	return model.CoChange{FileA: a, FileB: b, Together: together, Total: 400, Lift: lift}
}

func reviewFinding(id int64, path string) model.Finding {
	return model.Finding{ID: id, Path: path, Source: model.SourceReview}
}

func TestAuditScopeFlagsTriggerDivergence(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	// Backticks and the comma must not defeat token extraction.
	note := "Edit `api/schemas.py`, then run make sync-api to regenerate the client."

	adv, ok, err := AuditScope(st, root, note, []string{"web/src/api"},
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	require.True(t, ok, "S1 and S2 agree — the advisory must fire")

	assert.Equal(t, "api/schemas.py", adv.NotePath)
	assert.Equal(t, "api/schemas.py", adv.Partner)
	assert.Equal(t, "web/src/api/schema.ts", adv.Evidence)
	assert.Equal(t, 38, adv.Together)
	assert.Equal(t, []string{"web/src/api", "api"}, adv.Suggested,
		"current regions first, trigger region appended")
}

func TestAuditScopeNeedsBothSignals(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	regions := []string{"web/src/api"}
	cited := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	t.Run("note without cochange support", func(t *testing.T) {
		st := scopeStore(t)

		_, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions, cited)

		require.NoError(t, err)
		assert.False(t, ok, "S1 alone must not fire")
	})

	t.Run("cochange without note support", func(t *testing.T) {
		st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

		_, ok, err := AuditScope(st, root, "Keep the generated client in sync.", regions, cited)

		require.NoError(t, err)
		assert.False(t, ok, "S2 alone must not fire")
	})
}

func TestAuditScopeValidatesNotePathsOnDisk(t *testing.T) {
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))
	regions := []string{"web/src/api"}
	cited := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	t.Run("path-shaped token that is not a file", func(t *testing.T) {
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")

		// A timezone name parses like a path. Only the token that
		// exists on disk may survive validation.
		note := "Store timestamps in America/New_York. Then edit api/schemas.py."

		adv, ok, err := AuditScope(st, root, note, regions, cited)

		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, "api/schemas.py", adv.NotePath)
	})

	t.Run("named trigger deleted from the tree", func(t *testing.T) {
		root := scopeRoot(t, "web/src/api/schema.ts")

		_, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions, cited)

		require.NoError(t, err)
		assert.False(t, ok, "a missing path must not become a delivery target")
	})
}

func TestAuditScopeNoteDirectoryAgreement(t *testing.T) {
	// The note names a directory with a wildcard; the partner file
	// lives under it. Co-change always names files, notes often name
	// directories — agreement must bridge the two.
	root := scopeRoot(t, "api/routes/orders.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/routes/orders.py", 12, 4.1))

	note := "Update the handlers in api/routes/* after schema changes."

	adv, ok, err := AuditScope(st, root, note, []string{"web/src/api"},
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, "api/routes", adv.NotePath)
	assert.Equal(t, "api/routes/orders.py", adv.Partner)
	assert.Equal(t, []string{"web/src/api", "api/routes"}, adv.Suggested)
}

func TestAuditScopeIgnoresPathsInsideRegions(t *testing.T) {
	t.Run("note path inside the regions", func(t *testing.T) {
		root := scopeRoot(t, "web/src/api/schema.ts", "web/src/api/client.ts")
		st := scopeStore(t, cochange("web/src/api/client.ts", "web/src/api/schema.ts", 20, 5.0))

		_, ok, err := AuditScope(st, root, "Regenerate web/src/api/schema.ts.",
			[]string{"web/src/api"}, []model.Finding{reviewFinding(1, "web/src/api/client.ts")})

		require.NoError(t, err)
		assert.False(t, ok, "a path already covered by delivery is not a miss")
	})

	t.Run("partner inside the regions", func(t *testing.T) {
		// The note names web (outside the region web/src/api), and the
		// partner under web sits inside the region. The edge points at
		// covered ground, so the advisory must stay silent.
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("api/schemas.py", "web/src/api/schema.ts", 38, 6.6))

		_, ok, err := AuditScope(st, root, "Run make sync-api and commit everything under web/.",
			[]string{"web/src/api"}, []model.Finding{reviewFinding(1, "api/schemas.py")})

		require.NoError(t, err)
		assert.False(t, ok, "a partner inside the regions is already delivered to")
	})
}

func TestAuditScopeRepoWidePinNeverFires(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	_, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", nil,
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	assert.False(t, ok, "repo-wide delivery already reaches every edit")
}

func TestAuditScopePartnerThresholds(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	regions := []string{"web/src/api"}
	cited := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	cases := []struct {
		name     string
		together int
		lift     float64
	}{
		{"below minimum shared commits", 4, 6.6},
		{"below minimum lift", 38, 2.0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", tc.together, tc.lift))

			_, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions, cited)

			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

func TestAuditScopePartnerRankBound(t *testing.T) {
	// Five stronger pairs fill the rank window, so the true pair sits
	// at rank six and is not read. The bound is deliberate: a trigger
	// that cannot reach the top five is weak evidence for delivery.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")

	pairs := []model.CoChange{cochange("web/src/api/schema.ts", "api/schemas.py", 8, 6.6)}

	for i := range 5 {
		pairs = append(pairs, cochange("web/src/api/schema.ts",
			fmt.Sprintf("web/src/api/gen_%d.ts", i), 20+i, 4.0))
	}

	st := scopeStore(t, pairs...)

	_, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", []string{"web/src/api"},
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestAuditScopeSkipsTestAndDocPartners(t *testing.T) {
	// Both strong partners under the named directory are excluded
	// kinds: tests co-change with everything, and docs are not code.
	root := scopeRoot(t, "api/test_schemas.py", "api/README.md", "web/src/api/schema.ts")
	st := scopeStore(t,
		cochange("web/src/api/schema.ts", "api/test_schemas.py", 30, 5.0),
		cochange("web/src/api/schema.ts", "api/README.md", 25, 5.0))

	_, ok, err := AuditScope(st, root, "Check the modules under api/ after schema changes.",
		[]string{"web/src/api"}, []model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	assert.False(t, ok, "test and doc partners are not delivery targets")
}

func TestAuditScopeSuggestedRegionShape(t *testing.T) {
	t.Run("trigger region depth is capped", func(t *testing.T) {
		root := scopeRoot(t, "api/routes/v2/handlers/orders.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("web/src/api/schema.ts",
			"api/routes/v2/handlers/orders.py", 15, 4.5))

		note := "Update api/routes/v2/handlers/orders.py after schema changes."

		adv, ok, err := AuditScope(st, root, note, []string{"web/src/api"},
			[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []string{"web/src/api", "api/routes/v2"}, adv.Suggested,
			"the trigger region stops at maxRegionDepth")
	})

	t.Run("full region set suppresses the suggestion", func(t *testing.T) {
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

		regions := []string{"web/src/api", "cmd/sync", "internal/gen"}

		adv, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions,
			[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

		require.NoError(t, err)
		require.True(t, ok, "the advisory still names the miss")
		assert.Nil(t, adv.Suggested, "past maxRegions the report leaves removal to the reviewer")
	})

	// Delivery regions form a union, so a region contained by the
	// trigger region adds nothing. The suggestion must remove it, not
	// carry it along.
	t.Run("trigger region subsumes a later region", func(t *testing.T) {
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

		regions := []string{"web/src/api", "api/routes"}

		adv, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions,
			[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []string{"web/src/api", "api"}, adv.Suggested,
			"api contains api/routes — the contained region must leave")
	})

	t.Run("subsumed regions free cap slots", func(t *testing.T) {
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

		// At the cap, but two entries live under the trigger region:
		// after subsumption the suggestion fits and must appear.
		regions := []string{"web/src/api", "api/routes", "api/models"}

		adv, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions,
			[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []string{"web/src/api", "api"}, adv.Suggested,
			"subsumption runs before the cap check")
	})

	t.Run("trigger region subsumes the first region", func(t *testing.T) {
		// Order only feeds the legacy region: field; pin identity
		// sorts regions. A subsumed first entry leaves like any other.
		root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
		st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

		regions := []string{"api/routes", "web/src/api"}

		adv, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", regions,
			[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, []string{"web/src/api", "api"}, adv.Suggested)
	})
}

func TestAuditScopeStrongestAgreementWins(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "api/models.py", "web/src/api/schema.ts")
	st := scopeStore(t,
		cochange("web/src/api/schema.ts", "api/models.py", 10, 3.5),
		cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	note := "Edit api/schemas.py and api/models.py together."

	adv, ok, err := AuditScope(st, root, note, []string{"web/src/api"},
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	require.NoError(t, err)
	require.True(t, ok)

	assert.Equal(t, "api/schemas.py", adv.Partner, "the strongest agreeing edge speaks for the pin")
	assert.Equal(t, 38, adv.Together)
}

func TestAuditScopeReadsFixFindingFootprint(t *testing.T) {
	// A fix finding carries its full code footprint in Paths. S2 must
	// read partners for every footprint file, not only the primary.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts", "web/src/api/client.ts")
	st := scopeStore(t, cochange("web/src/api/client.ts", "api/schemas.py", 21, 5.2))

	f := model.Finding{
		ID:     2,
		Path:   "web/src/api/schema.ts",
		Paths:  []string{"web/src/api/schema.ts", "web/src/api/client.ts"},
		Source: "fix:conventional",
	}

	adv, ok, err := AuditScope(st, root, "Edit api/schemas.py first.", []string{"web/src/api"},
		[]model.Finding{f})

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "web/src/api/client.ts", adv.Evidence)
}

func TestAuditScopes(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	meta := map[int64]model.Finding{1: reviewFinding(1, "web/src/api/schema.ts")}

	named := "Edit api/schemas.py first."
	reworded := "Sync the client."

	// The live pins: p2's yaml note dropped the trigger path, p4's
	// gained it. Both carry only the single region: field — the common
	// shape — so the helper must read AllRegions, not the raw list.
	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "applied-reworded", Region: "web/src/api", Note: reworded},
		{Rule: "applied-named", Region: "web/src/api", Note: named},
	}

	ps := []model.Proposal{
		{ID: 1, Rule: "pending-named", Region: "web/src/api", Note: named,
			Members: []int64{1}, Status: model.ProposalProposed},
		{ID: 2, Rule: "applied-reworded", Region: "web/src/api", Note: named,
			Members: []int64{1}, Status: model.ProposalApplied},
		{ID: 3, Rule: "applied-pruned", Region: "web/src/api", Note: named,
			Members: []int64{1}, Status: model.ProposalApplied},
		{ID: 4, Rule: "applied-named", Region: "web/src/api", Note: reworded,
			Members: []int64{1}, Status: model.ProposalApplied},
		{ID: 5, Rule: "dead-citations", Region: "web/src/api", Note: named,
			Members: []int64{99}, Status: model.ProposalProposed},
		{ID: 6, Rule: "dismissed-named", Region: "web/src/api", Note: named,
			Members: []int64{1}, Status: model.ProposalDismissed},
	}

	out, err := AuditScopes(st, cfg, root, ps, meta)
	require.NoError(t, err)

	assert.Contains(t, out, int64(1), "pending rows audit their stored note")
	assert.Contains(t, out, int64(4), "applied rows audit the LIVE yaml note")
	assert.NotContains(t, out, int64(2), "a reworded live note clears the advisory")
	assert.NotContains(t, out, int64(3), "a pruned pin has no delivery to audit")
	assert.NotContains(t, out, int64(5), "dead citations cannot drive partner lookups")
	assert.NotContains(t, out, int64(6), "dismissed rows are not delivery")

	assert.Equal(t, []string{"web/src/api", "api"}, out[int64(4)].Suggested,
		"a live pin carrying only region: still audits its full region set")

	t.Run("store errors carry the proposal id", func(t *testing.T) {
		broken := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))
		require.NoError(t, broken.Close())

		_, err := AuditScopes(broken, cfg, root, ps[:1], meta)
		require.ErrorContains(t, err, "scope audit for proposal 1")
	})
}

func TestScopeAdvisoryLine(t *testing.T) {
	base := ScopeAdvisory{
		NotePath:  "api/schemas.py",
		Partner:   "api/schemas.py",
		Evidence:  "web/src/api/schema.ts",
		Together:  38,
		Suggested: []string{"web/src/api", "api"},
	}

	t.Run("file agreement collapses the repetition", func(t *testing.T) {
		assert.Equal(t,
			"delivery may miss the trigger: the note names api/schemas.py (outside the regions) "+
				"and evidence web/src/api/schema.ts co-changes with it (38 shared commits) "+
				"— consider regions: [web/src/api, api]",
			base.Line())
	})

	t.Run("directory agreement names the partner file", func(t *testing.T) {
		adv := base
		adv.NotePath = "api/routes"
		adv.Partner = "api/routes/orders.py"

		assert.Contains(t, adv.Line(), "the note names api/routes (outside the regions)")
		assert.Contains(t, adv.Line(), "co-changes with api/routes/orders.py")
	})

	t.Run("no suggestion tail at the region cap", func(t *testing.T) {
		adv := base
		adv.Suggested = nil

		assert.NotContains(t, adv.Line(), "consider regions")
	})
}

func TestAuditScopePropagatesStoreErrors(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	require.NoError(t, st.Close())

	_, _, err := AuditScope(st, root, "Edit api/schemas.py first.", []string{"web/src/api"},
		[]model.Finding{reviewFinding(1, "web/src/api/schema.ts")})

	assert.Error(t, err, "a broken store must surface, not read as a quiet pin")
}
