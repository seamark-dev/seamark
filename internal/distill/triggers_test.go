package distill

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestCleanTriggerPaths(t *testing.T) {
	got := cleanTriggerPaths([]string{
		" `api/schemas.py`, ", // wrapping and punctuation
		"/etc/passwd",         // absolute
		"api/../secrets.txt",  // parent escape
		"api//schemas.py",     // normalizes into a duplicate
		"",                    // empty
		"web/src/api/schema.ts",
		"cmd/gen.go",
		"one/too/many.go", // fourth survivor — past the cap
	})

	assert.Equal(t, []string{"api/schemas.py", "web/src/api/schema.ts", "cmd/gen.go"}, got)
}

func TestValidateTriggerPaths(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py")

	got := validateTriggerPaths(root, []string{"api/schemas.py", "api/ghost.py"})
	assert.Equal(t, []string{"api/schemas.py"}, got, "only paths in the working tree survive")

	assert.Nil(t, validateTriggerPaths("", []string{"api/schemas.py"}),
		"no tree to check against — nothing is verified")
}

func TestRecomputeRegionsWidensConfirmedTriggers(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "cmd/gen.go", "web/src/api/schema.ts")
	st := scopeStore(t,
		cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6),
		cochange("web/src/api/schema.ts", "cmd/gen.go", 12, 4.0))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py", "cmd/gen.go"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"web/src/api", "api", "cmd"}, regions,
		"coverage first, confirmed triggers appended in stored order")
	require.Len(t, facts, 2)
	assert.Equal(t, TriggerFact{Path: "api/schemas.py", Region: "api", Together: 38, Widened: true}, facts[0])
	assert.Equal(t, TriggerFact{Path: "cmd/gen.go", Region: "cmd", Together: 12, Widened: true}, facts[1])
}

func TestRecomputeRegionsDirectoryTrigger(t *testing.T) {
	// A directory trigger is its own region — not its parent's. The
	// tree decides file vs directory; a name alone cannot.
	root := scopeRoot(t, "api/routes/orders.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/routes/orders.py", 12, 4.1))

	p := model.Proposal{TriggerPaths: []string{"api/routes"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"web/src/api", "api/routes"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "api/routes", facts[0].Region)
	assert.True(t, facts[0].Widened)
}

func TestRecomputeRegionsTopLevelDirectoryTrigger(t *testing.T) {
	// The old file-only derivation made a top-level directory vanish
	// (Dir("api") is "."); it must widen to itself.
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	p := model.Proposal{TriggerPaths: []string{"api"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"web/src/api", "api"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "api", facts[0].Region)
	assert.True(t, facts[0].Widened)
}

func TestRecomputeRegionsVanishedTriggerNeverWidens(t *testing.T) {
	// The path left the tree since it was stored. A vanished path is
	// never a delivery target, whatever history says.
	root := scopeRoot(t, "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"web/src/api"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "", facts[0].Region)
	assert.False(t, facts[0].Widened)
}

func TestRecomputeRegionsLeavesUnconfirmedTriggersAlone(t *testing.T) {
	cases := []struct {
		name string
		pair []model.CoChange
	}{
		{"no cochange row", nil},
		{"below minimum shared commits",
			[]model.CoChange{cochange("web/src/api/schema.ts", "api/schemas.py", 4, 6.6)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
			st := scopeStore(t, tc.pair...)

			p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
			living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

			regions, facts, err := RecomputeRegions(st, root, p, living)
			require.NoError(t, err)

			assert.Equal(t, []string{"web/src/api"}, regions, "an unconfirmed name must not widen delivery")
			require.Len(t, facts, 1)
			assert.False(t, facts[0].Widened)
			assert.Zero(t, facts[0].Together)
		})
	}
}

func TestRecomputeRegionsMarksCoveredTriggers(t *testing.T) {
	// The trigger is confirmed and its region already sits inside the
	// coverage: delivered, nothing to do — and the fact says which.
	root := scopeRoot(t, "api/schemas.py", "api/handler.py")
	st := scopeStore(t, cochange("api/handler.py", "api/schemas.py", 20, 5.0))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{reviewFinding(1, "api/handler.py")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, 20, facts[0].Together, "confirmation is still reported")
	assert.True(t, facts[0].Covered)
	assert.False(t, facts[0].Widened)
	assert.Empty(t, facts[0].BlockedLine(), "covered facts render nothing")
}

func TestRecomputeRegionsCapBlockedTriggerIsVisible(t *testing.T) {
	// Three regions already cover the evidence; the confirmed trigger
	// cannot join. Neither Covered nor Widened — the surfaces read
	// that combination as BLOCKED and must say so.
	root := scopeRoot(t, "api/schemas.py",
		"web/src/api/schema.ts", "cmd/a.go", "internal/b.go")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{
		reviewFinding(1, "web/src/api/schema.ts"),
		reviewFinding(2, "cmd/a.go"),
		reviewFinding(3, "internal/b.go"),
	}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Len(t, regions, 3, "the cap holds")
	require.Len(t, facts, 1)
	assert.Equal(t, 38, facts[0].Together)
	assert.False(t, facts[0].Covered)
	assert.False(t, facts[0].Widened)
	assert.Contains(t, facts[0].BlockedLine(), "confirmed by co-change (38 shared commits) but not deliverable",
		"a blocked fact renders the shared sentence")
}

func TestRecomputeRegionsKeepsRepoWideRepoWide(t *testing.T) {
	// Repo-wide coverage already reaches every trigger; widening it
	// would NARROW delivery. The trigger reads as covered.
	root := scopeRoot(t, "api/schemas.py", "main.go")
	st := scopeStore(t, cochange("main.go", "api/schemas.py", 30, 5.0))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{reviewFinding(1, "main.go")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Nil(t, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, 30, facts[0].Together)
	assert.True(t, facts[0].Covered)
	assert.False(t, facts[0].Widened)
}

func TestRecomputeRegionsRootLevelTriggerNeverWidens(t *testing.T) {
	// A root-level trigger has no region to express it: confirmed but
	// blocked, visible to the surfaces.
	root := scopeRoot(t, "Makefile", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "Makefile", 15, 4.0))

	p := model.Proposal{TriggerPaths: []string{"Makefile"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"web/src/api"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "", facts[0].Region)
	assert.Equal(t, 15, facts[0].Together)
	assert.False(t, facts[0].Covered)
	assert.False(t, facts[0].Widened)
}

func TestRecomputeRegionsPropagatesStoreErrors(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6))
	require.NoError(t, st.Close())

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	_, _, err := RecomputeRegions(st, root, p, living)
	assert.Error(t, err)
}
