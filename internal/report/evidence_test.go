package report

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

func TestReadEvidenceNeedsAConfig(t *testing.T) {
	st, _ := seedStore(t)

	_, err := ReadEvidence(st, []string{"scripts/task.py"})
	require.ErrorIs(t, err, errNoConfig)

	_, err = ReadEvidence(st, []string{"scripts/task.py"}, reviews.DefaultConfig(), nil)
	require.ErrorIs(t, err, errNoConfig)
}

func TestEvidenceFoldsEqualTheWrappersForEachConfig(t *testing.T) {
	// One read serves two configs. The current config carries the pins
	// that cover two clusters; the candidate removes them, so both
	// clusters return. Each fold must equal its production wrapper.
	st := goldenStore(t)

	current := goldenConfig(
		reviews.PinRule{Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment."},
		reviews.PinRule{Rule: "multi-source-guard", Region: "db", Note: "Close cursors in a finally block."},
		reviews.PinRule{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
	)

	candidate := goldenConfig(
		reviews.PinRule{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."},
	)
	candidate.Mute = []reviews.MuteRule{{Rule: "RUF001"}}

	files := []string{"scripts/x.py", "db/a.go", "api/b.go"}

	// Both orders: the citations come from every config, not the first.
	for _, cfgs := range [][]*reviews.Config{{current, candidate}, {candidate, current}} {
		ev, err := ReadEvidence(st, files, cfgs...)
		require.NoError(t, err)

		for _, cfg := range cfgs {
			assertFoldsEqualTheWrappers(t, st, ev, cfg, files)
		}

		cur := ev.changeSetFold(current, 0).Lessons
		assert.NotContains(t, symptomsOf(cur), "postgres", "the live pin covers its cluster")
		assert.NotContains(t, symptomsOf(cur), "close every cursor", "the live pin covers its cluster")

		cand := ev.changeSetFold(candidate, 0).Lessons
		assert.Contains(t, symptomsOf(cand), "postgres", "a removed pin no longer covers its cluster")
		assert.Contains(t, symptomsOf(cand), "close every cursor", "a removed pin no longer covers its cluster")
		assert.NotContains(t, symptomsOf(cand), "RUF001", "the candidate mute applies")
	}
}

// assertFoldsEqualTheWrappers compares each fold of ev for cfg with the
// production wrapper that reads the store again.
func assertFoldsEqualTheWrappers(t *testing.T, st *store.Store, ev *Evidence, cfg *reviews.Config, files []string) {
	t.Helper()

	for _, budget := range []LessonBudget{HookBudget(cfg), {Total: HookLessonTotal}, {Total: 3, Pins: 2}} {
		want, wantHeld, err := LessonsForFilesBudget(st, cfg, files, budget)
		require.NoError(t, err)

		got := ev.hookFold(cfg, budget)
		assert.Equal(t, want, got.Lessons)
		assert.Equal(t, wantHeld, got.HeldBack)
	}

	for _, budget := range []int{cfg.ChangeSetBudget(), 0, 2} {
		want, wantTrimmed, err := LessonsForFiles(st, cfg, files, budget)
		require.NoError(t, err)

		got := ev.changeSetFold(cfg, budget)
		assert.Equal(t, want, got.Lessons)
		assert.Equal(t, wantTrimmed, got.HeldBack)
	}
}

func TestEvidenceReadsCitationsOfLivePinsOnly(t *testing.T) {
	// The hook reads the evidence on every edit, so ReadEvidence reads
	// the cluster citations only for an applied row whose pin is live in
	// one of its configs. The selection cannot show an extra read,
	// because coveredBy checks the live state again. So this test reads
	// the internal map.
	st := goldenStore(t)

	weak := goldenConfig(reviews.PinRule{
		Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange.",
	})
	strong := goldenConfig(reviews.PinRule{
		Rule: "multi-source-guard", Region: "db", Note: "Close cursors in a finally block.",
	})
	files := []string{"api/b.go"}

	// No config carries a pin, so no row is live and nothing is read.
	ev, err := ReadEvidence(st, files, reviews.DefaultConfig())
	require.NoError(t, err)
	assert.Empty(t, ev.citations)

	ids := map[string]int64{}
	for _, p := range ev.applied {
		ids[p.Signature] = p.ID
	}

	require.Len(t, ids, 4, "the fixture holds four applied rows")

	// The weak pin and its twin row share one key, so both are live.
	// sig-creds and sig-strong have no live pin in this config.
	ev, err = ReadEvidence(st, files, weak)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{ids["sig-weak"], ids["sig-twin"]}, citedProposals(ev))

	// A pin live in any config counts, whatever the order of the configs.
	for _, cfgs := range [][]*reviews.Config{{weak, strong}, {strong, weak}} {
		ev, err = ReadEvidence(st, files, cfgs...)
		require.NoError(t, err)
		assert.ElementsMatch(t, []int64{ids["sig-weak"], ids["sig-twin"], ids["sig-strong"]}, citedProposals(ev))
	}
}

// citedProposals returns the IDs of the applied rows whose citations ev
// holds, in no fixed order.
func citedProposals(ev *Evidence) []int64 {
	ids := make([]int64, 0, len(ev.citations))
	for id := range ev.citations {
		ids = append(ids, id)
	}

	return ids
}

func TestEvidenceAssessesEachPinOnce(t *testing.T) {
	// The memo keeps the raw verdict of the first fold. A later fold
	// formats it for its own surface and does not probe the disk again.
	st := goldenStore(t)

	root, err := st.GetMeta(store.MetaRepoRoot)
	require.NoError(t, err)

	cfg := goldenConfig(reviews.PinRule{
		Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment.",
	})
	files := []string{"scripts/x.py"}

	ev, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	view := LessonBudget{Total: HookLessonTotal}

	first := ev.hookFold(cfg, view).Lessons
	require.NotEmpty(t, first)
	assert.Equal(t, "fair: 2 event(s), review, age unknown, 1 cited file(s) deleted", first[0].Annotation)

	// The second cited file goes away after the read.
	require.NoError(t, os.Remove(filepath.Join(root, "scripts", "db.py")))

	again := ev.hookFold(cfg, view).Lessons
	assert.Equal(t, first[0].Annotation, again[0].Annotation, "one evidence read gives one verdict")

	hook := ev.hookFold(cfg, HookBudget(cfg)).Lessons
	assert.Empty(t, hook[0].Annotation, "an ambient fold names weak evidence only")

	// A new read sees the deleted file.
	fresh, err := ReadEvidence(st, files, cfg)
	require.NoError(t, err)

	later := fresh.hookFold(cfg, view).Lessons
	assert.Equal(t, "weak: 2 event(s), review, age unknown, 2 cited file(s) deleted", later[0].Annotation)

	ambient := fresh.hookFold(cfg, HookBudget(cfg)).Lessons
	assert.Equal(t, "weak evidence: 2 event(s), review, age unknown, 2 cited file(s) deleted", ambient[0].Annotation,
		"the fold formats the same verdict for the ambient surface")
}

func TestEvidenceWithoutAppliedRowsHasNoAssessor(t *testing.T) {
	// Without applied proposals no pin has a confidence rank. The fold
	// then gets a nil annotator, so every pin ranks 0 and the file order
	// of equal pins stays.
	st, _ := seedStore(t)

	cfg := goldenConfig(
		reviews.PinRule{Rule: "bravo-guard", Region: "scripts", Note: "n"},
		reviews.PinRule{Rule: "alpha-guard", Region: "scripts", Note: "n"},
	)

	ev, err := ReadEvidence(st, []string{"scripts/task.py"}, cfg)
	require.NoError(t, err)
	assert.Nil(t, ev.assessor)
	assert.Nil(t, ev.assessor.annotator(true))

	merged := ev.union(cfg, true)
	require.Len(t, merged.pins, 2)

	for _, sp := range merged.pins {
		assert.Zero(t, sp.Rank)
		assert.Empty(t, sp.Note)
	}

	assert.Equal(t, "bravo-guard", merged.pins[0].Pin.Rule, "equal pins keep the file order")

	hook := ev.hookFold(cfg, HookBudget(cfg)).Lessons
	assert.Equal(t, []model.Lesson{merged.pins[0].Lesson(), merged.pins[1].Lesson(), merged.mined[0]}, hook)
}
