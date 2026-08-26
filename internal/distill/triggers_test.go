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

func TestRecomputeRegionsSelectsExactConfirmedTriggers(t *testing.T) {
	root := scopeRoot(t, "api/schemas.py", "cmd/gen.go", "web/src/api/schema.ts")
	st := scopeStore(t,
		cochange("web/src/api/schema.ts", "api/schemas.py", 38, 6.6),
		cochange("web/src/api/schema.ts", "cmd/gen.go", 12, 4.0))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py", "cmd/gen.go"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/schemas.py", "cmd/gen.go"}, regions,
		"verified trigger files replace broader evidence coverage")
	require.Len(t, facts, 2)
	assert.Equal(t, TriggerFact{Path: "api/schemas.py", Region: "api/schemas.py", Together: 38,
		Selected: true}, facts[0])
	assert.Equal(t, TriggerFact{Path: "cmd/gen.go", Region: "cmd/gen.go", Together: 12,
		Selected: true}, facts[1])
}

func TestRecomputeRegionsSelectsDirectlyCitedOpenTelemetryTrigger(t *testing.T) {
	root := scopeRoot(t,
		"sdk/metric/internal/aggregate/exponential_histogram.go",
		"sdk/metric/internal/aggregate/histogram.go",
	)
	st := scopeStore(t)
	p := model.Proposal{TriggerPaths: []string{"sdk/metric/internal/aggregate/histogram.go"}}
	living := []model.Finding{{
		ID:   1,
		Path: "sdk/metric/internal/aggregate/exponential_histogram.go",
		Paths: []string{
			"sdk/metric/internal/aggregate/exponential_histogram.go",
			"sdk/metric/internal/aggregate/histogram.go",
		},
		Source: model.SourceFixConventional,
	}}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"sdk/metric/internal/aggregate/histogram.go"}, regions)
	require.Len(t, facts, 1)
	assert.True(t, facts[0].Direct)
	assert.True(t, facts[0].Selected)
	assert.Zero(t, facts[0].Together, "direct evidence needs no historical inference")
}

func TestRecomputeRegionsDoesNotSelectDirectlyCitedTestPath(t *testing.T) {
	root := scopeRoot(t, "api/handler.go", "api/handler_test.go")
	st := scopeStore(t)
	p := model.Proposal{TriggerPaths: []string{"api/handler_test.go"}}
	living := []model.Finding{{
		ID: 1, Path: "api/handler.go",
		Paths:  []string{"api/handler.go", "api/handler_test.go"},
		Source: model.SourceFixConventional,
	}}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api"}, regions, "test evidence cannot replace production coverage")
	require.Len(t, facts, 1)
	assert.False(t, facts[0].Direct)
	assert.False(t, facts[0].Selected)
}

func TestRecomputeRegionsTestEvidenceDoesNotCiteParentDirectory(t *testing.T) {
	root := scopeRoot(t, "api/handler_test.go")
	st := scopeStore(t)
	p := model.Proposal{TriggerPaths: []string{"api"}}
	living := []model.Finding{{
		ID: 1, Path: "api/handler_test.go", Source: model.SourceFixConventional,
	}}

	_, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)
	require.Len(t, facts, 1)
	assert.False(t, facts[0].Direct,
		"a test-only citation cannot establish production delivery at its parent")
	assert.False(t, facts[0].Selected)
}

func TestRecomputeRegionsDoesNotTreatBroadAncestorAsDirectEvidence(t *testing.T) {
	root := scopeRoot(t, "sdk/metric/internal/aggregate/histogram.go")
	st := scopeStore(t)
	p := model.Proposal{TriggerPaths: []string{"sdk"}}
	living := []model.Finding{{
		ID: 1, Path: "sdk/metric/internal/aggregate/histogram.go",
		Source: model.SourceFixConventional,
	}}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"sdk/metric/internal"}, regions,
		"an uncorroborated ancestor cannot replace evidence coverage")
	require.Len(t, facts, 1)
	assert.False(t, facts[0].Direct)
	assert.False(t, facts[0].Selected)
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

	assert.Equal(t, []string{"api/routes"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "api/routes", facts[0].Region)
	assert.True(t, facts[0].Selected)
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

	assert.Equal(t, []string{"api"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "api", facts[0].Region)
	assert.True(t, facts[0].Selected)
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
	assert.False(t, facts[0].Selected)
	assert.Contains(t, facts[0].BlockedLine(), "absent from the working tree")
}

func TestDirectVanishedTriggerExplainsWhyItIsBlocked(t *testing.T) {
	root := scopeRoot(t, "other/live.go")
	st := scopeStore(t)
	p := model.Proposal{TriggerPaths: []string{"api/handler.go"}}
	living := []model.Finding{{ID: 1, Path: "api/handler.go", Source: model.SourceReview}}

	_, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)
	require.Len(t, facts, 1)
	assert.True(t, facts[0].Direct)
	assert.Contains(t, facts[0].BlockedLine(), "directly cited by the evidence")
	assert.Contains(t, facts[0].BlockedLine(), "not deliverable")
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
			assert.False(t, facts[0].Selected)
			assert.Zero(t, facts[0].Together)
		})
	}
}

func TestRecomputeRegionsNarrowsToConfirmedTriggerInsideCoverage(t *testing.T) {
	// A confirmed file inside broad evidence coverage is still useful:
	// it narrows delivery to the place where the mistake is introduced.
	root := scopeRoot(t, "api/schemas.py", "api/handler.py")
	st := scopeStore(t, cochange("api/handler.py", "api/schemas.py", 20, 5.0))

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{reviewFinding(1, "api/handler.py")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/schemas.py"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, 20, facts[0].Together, "confirmation is still reported")
	assert.True(t, facts[0].Selected)
	assert.Empty(t, facts[0].BlockedLine(), "selected facts are deliverable")
}

func TestAddDeliveryRegionEnforcesCapAndCollapsesChildren(t *testing.T) {
	regions := []string{"api/a.go", "cmd/b.go", "internal/c.go"}
	got, selected := addDeliveryRegion(regions, "web/d.go")
	assert.False(t, selected)
	assert.Equal(t, regions, got)

	got, selected = addDeliveryRegion(regions, "api")
	assert.True(t, selected)
	assert.Equal(t, []string{"cmd/b.go", "internal/c.go", "api"}, got,
		"a parent replaces its child without exceeding the cap")
}

func TestRecomputeRegionsNarrowsRepoWideEvidenceToDirectTrigger(t *testing.T) {
	// A root-level evidence path makes coverage repo-wide, but a
	// directly cited trigger still identifies the bounded edit surface.
	root := scopeRoot(t, "api/schemas.py", "main.go")
	st := scopeStore(t)

	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{{
		ID: 1, Path: "main.go", Paths: []string{"main.go", "api/schemas.py"},
		Source: model.SourceFixConventional,
	}}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/schemas.py"}, regions)
	require.Len(t, facts, 1)
	assert.True(t, facts[0].Direct)
	assert.True(t, facts[0].Selected)
}

func TestRecomputeRegionsNarrowsRepoWideEvidenceToConfirmedTrigger(t *testing.T) {
	root := scopeRoot(t, "main.go", "api/schemas.py")
	st := scopeStore(t, cochange("main.go", "api/schemas.py", 15, 4.0))
	p := model.Proposal{TriggerPaths: []string{"api/schemas.py"}}
	living := []model.Finding{{ID: 1, Path: "main.go", Source: model.SourceReview}}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"api/schemas.py"}, regions,
		"strong history can replace repo-wide fallback with an exact trigger")
	require.Len(t, facts, 1)
	assert.False(t, facts[0].Direct)
	assert.Equal(t, 15, facts[0].Together)
	assert.True(t, facts[0].Selected)
}

func TestRecomputeRegionsSelectsRootLevelFileTrigger(t *testing.T) {
	// Exact file regions make root-level triggers expressible.
	root := scopeRoot(t, "Makefile", "web/src/api/schema.ts")
	st := scopeStore(t, cochange("web/src/api/schema.ts", "Makefile", 15, 4.0))

	p := model.Proposal{TriggerPaths: []string{"Makefile"}}
	living := []model.Finding{reviewFinding(1, "web/src/api/schema.ts")}

	regions, facts, err := RecomputeRegions(st, root, p, living)
	require.NoError(t, err)

	assert.Equal(t, []string{"Makefile"}, regions)
	require.Len(t, facts, 1)
	assert.Equal(t, "Makefile", facts[0].Region)
	assert.Equal(t, 15, facts[0].Together)
	assert.True(t, facts[0].Selected)
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
