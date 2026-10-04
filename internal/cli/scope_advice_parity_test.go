package cli

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/distill"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// The console reads scope advice from distill.RecommendScope, and the
// CLI keeps its own retarget loop (decision D-1). This test proves that
// both compute the same plan on the fixture of the retarget goldens: it
// rebuilds the whole gate-off stdout of `lessons --retarget` from the
// advice alone and compares it with the real CLI output.

func TestRecommendScopeMatchesTheRetargetPlanner(t *testing.T) {
	root := retargetGoldenWorkspace(t, retargetGoldenLessons)

	st, err := store.OpenReadOnly(store.DefaultPath(root))
	require.NoError(t, err)

	defer func() { _ = st.Close() }()

	applied, err := st.Proposals(model.ProposalApplied)
	require.NoError(t, err)

	// The CLI plans the selection in ID order.
	ps := slices.SortedFunc(slices.Values(applied), func(a, b model.Proposal) int { return cmp.Compare(a.ID, b.ID) })

	var cited []int64
	for _, p := range ps {
		cited = append(cited, p.Members...)
	}

	meta, err := st.FindingsMetaByIDs(cited)
	require.NoError(t, err)

	var (
		want    strings.Builder
		updated []model.Proposal
		kinds   = map[int64]distill.ScopeAdvice{}
	)

	for _, p := range ps {
		var living []model.Finding

		for _, id := range p.Members {
			if f, ok := meta[id]; ok {
				living = append(living, f)
			}
		}

		advice, err := distill.RecommendScope(st, root, p, living)
		require.NoError(t, err)

		kinds[p.ID] = advice

		switch advice.Kind {
		case distill.ScopeNoEvidence:
			fmt.Fprintf(&want, "  p%-4d %s — evidence aged out; nothing to recompute from (prune instead?)\n",
				p.ID, render.Sanitize(p.Rule))
		case distill.ScopeCurrent:
			fmt.Fprintf(&want, "  p%-4d %s — regions already current\n", p.ID, render.Sanitize(p.Rule))
		case distill.ScopeChanged:
			next := p
			next.Regions, next.Region = advice.Regions, ""

			if len(advice.Regions) > 0 {
				next.Region = advice.Regions[0]
			}

			updated = append(updated, next)
		}
	}

	block, err := distill.RenderPins(updated)
	require.NoError(t, err)

	fmt.Fprintf(&want, "distill.write is off in .seamark/config.yaml — replace these pins in "+
		".seamark/lessons.yaml by hand:\n\n%s\n(nothing was changed; enable distill.write "+
		"to let retarget edit the file and the ledger together)\n", block)

	stdout, stderr := goldenRun(t, root, "lessons", "--retarget", "p1..p6")
	assert.Equal(t, want.String(), stdout, "the advice reproduces the CLI plan byte for byte")
	assert.Empty(t, stderr)

	// The rows that the retarget goldens describe, with the labels that
	// the console adds.
	assert.Equal(t, distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Regions: []string{"scripts"}, Facts: []distill.TriggerFact{}, Narrower: true,
	}, kinds[1], "p1: repository-wide to [scripts]")
	assert.Equal(t, distill.ScopeCurrent, kinds[2].Kind, "p2: current")
	assert.Equal(t, distill.ScopeNoEvidence, kinds[3].Kind, "p3: no living evidence")
	assert.Equal(t, []string{"scripts"}, kinds[4].Regions, "p4: the CLI plans it although its pin is missing")
	assert.Equal(t, distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Regions: []string{"api", "db"}, Facts: []distill.TriggerFact{}, Broader: true,
	}, kinds[5], "p5: widened to [api, db]")
	assert.Equal(t, distill.ScopeAdvice{
		Kind: distill.ScopeChanged, Facts: []distill.TriggerFact{}, RepoWide: true, Broader: true,
	}, kinds[6], "p6: widened to repository scope")
}
