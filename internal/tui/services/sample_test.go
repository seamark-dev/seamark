package services

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/integration"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/pinfile"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/tui/services/servicestest"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// The engine tests of the selection sample. They run the production
// engine over a real index with mined lessons and pins. They compare the
// sample with the production entries of the hook and of change_set.

// samplePins holds one pin of the api region and five repository-wide
// pins, so both surfaces hold some pins back.
const samplePins = "pin:\n" +
	"  - rule: close-db-handles\n    region: api\n    note: Close the handle on every error path.\n" +
	"  - rule: keep-docs-current\n    note: Keep the docs current.\n" +
	"  - rule: check-errors\n    note: Check every returned error.\n" +
	"  - rule: name-tests-by-behavior\n    note: Name a test by the behavior it proves.\n" +
	"  - rule: no-global-state\n    note: Pass state; do not keep it global.\n" +
	"  - rule: wrap-errors\n    note: Wrap an error with its context.\n"

// sampleMute is a candidate text that mutes the mined lesson of a.go.
const sampleMute = "mute:\n  - rule: RUF001\n"

// sampleWorkspace builds a ready index with three mined lessons: RUF001
// on a.go, and E702 and a one-off on api. It also writes the pin file
// samplePins, the directory api, and the link lnk.go to a.go.
func sampleWorkspace(t *testing.T) workspace.Workspace {
	t.Helper()

	ws, _ := servicestest.Workspace(t, "")

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons([]domain.Lesson{
		{ClusterKey: "a.go\x00RUF001", Region: "a.go", Reviewer: "coderabbit", Symptom: "RUF001", Occurrences: 6, LastTS: 100},
		{ClusterKey: "api\x00E702", Region: "api", Reviewer: "person", Symptom: "E702", Occurrences: 3, LastTS: 90},
		{ClusterKey: "api\x00once", Region: "api", Reviewer: "person", Symptom: "solitary finding", Occurrences: 1, LastTS: 50},
	}, nil))
	require.NoError(t, st.Close())

	servicestest.Write(t, ws, pinfile.File, samplePins)
	require.NoError(t, os.Mkdir(filepath.Join(ws.Root, "api"), 0o755))
	require.NoError(t, os.Symlink("a.go", filepath.Join(ws.Root, "lnk.go")))

	return ws
}

// setMeta stores one meta value in the index of ws.
func setMeta(t *testing.T, ws workspace.Workspace, key, value string) {
	t.Helper()

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(key, value))
	require.NoError(t, st.Close())
}

// sampleState is one state of the index for a sample, with the words
// that Unavailable must hold, or "" when the sample can run.
type sampleState struct {
	ws      workspace.Workspace
	reason  string
	foreign bool
}

// sampleStates builds a workspace for every index state, and two with a
// foreign root. Each holds the pin file samplePins under its root.
func sampleStates(t *testing.T) map[string]sampleState {
	t.Helper()

	absent := t.TempDir()
	servicestest.Git(t, absent, "init", "-b", "main")
	absentWS, err := workspace.Resolve(absent, "")
	require.NoError(t, err)
	servicestest.Write(t, absentWS, pinfile.File, samplePins)

	garbage := sampleWorkspace(t)
	require.NoError(t, os.WriteFile(garbage.DBPath, []byte("this is not a SQLite database"), 0o644))

	newer := sampleWorkspace(t)
	setMeta(t, newer, store.MetaSchemaVersion, strconv.Itoa(store.SupportedSchema()+1))

	older := sampleWorkspace(t)
	setMeta(t, older, store.MetaSchemaVersion, strconv.Itoa(store.SupportedSchema()-1))

	notIndexed := sampleWorkspace(t)
	setMeta(t, notIndexed, store.MetaIndexedAt, "")

	copied := sampleWorkspace(t)
	setMeta(t, copied, store.MetaRepoRoot, t.TempDir())

	origin := sampleWorkspace(t)
	dbOf, err := workspace.Resolve(t.TempDir(), origin.DBPath)
	require.NoError(t, err)

	return map[string]sampleState{
		"absent":                       {ws: absentWS, reason: "no readable index. Without an index the hook still delivers pins only"},
		"unreadable":                   {ws: garbage, reason: "the index is unreadable"},
		"newer":                        {ws: newer, reason: "the index is newer"},
		"older":                        {ws: older, reason: "the index is older"},
		"not indexed":                  {ws: notIndexed, reason: "the index is not indexed"},
		"ready":                        {ws: sampleWorkspace(t)},
		"a copied checkout":            {ws: copied, reason: ErrForeignRoot.Error(), foreign: true},
		"a --db of another repository": {ws: dbOf, reason: ErrForeignRoot.Error(), foreign: true},
	}
}

// sampleRequests are the request variants of the state table. Each
// names the words of its own reason, or "" when it can run.
func sampleRequests(saved []byte) map[string]struct {
	req    SampleRequest
	reason string
	stale  bool
} {
	paths := []string{"a.go", "api/new.go"}
	review := append(append([]byte{}, saved...), sampleMute...)

	return map[string]struct {
		req    SampleRequest
		reason string
		stale  bool
	}{
		"a review":            {req: SampleRequest{Paths: paths, Before: saved, After: review}},
		"no paths":            {req: SampleRequest{Before: saved, After: review}, reason: "no paths"},
		"an unparsable saved": {req: SampleRequest{Paths: paths, Before: []byte("hook_delivery: sometimes\n"), After: review}, reason: "the saved configuration cannot be read", stale: true},
		"an unparsable review": {req: SampleRequest{Paths: paths, Before: saved, After: []byte("pin: [\n")},
			reason: "the configuration of this review cannot be read"},
		"changed disk bytes": {req: SampleRequest{Paths: paths, Before: append([]byte("threshold: 3\n"), saved...), After: review},
			stale: true},
	}
}

func TestEngineSampleStates(t *testing.T) {
	saved := []byte(samplePins)

	for stateName, state := range sampleStates(t) {
		e := engNew(state.ws, Deps{})

		for reqName, rc := range sampleRequests(saved) {
			for _, surface := range []SampleSurface{SampleHook, SampleChangeSet} {
				t.Run(stateName+"/"+reqName+"/"+surface.String(), func(t *testing.T) {
					req := rc.req
					req.Surface = surface

					view := e.Sample(req)

					// The first reason wins: the index state and the root,
					// then the paths, then the texts.
					want := state.reason
					if want == "" {
						want = rc.reason
					}

					assert.Equal(t, surface, view.Surface)

					if want != "" {
						require.NotEmpty(t, view.Unavailable, "an unavailable sample says why")
						assert.True(t, strings.HasPrefix(view.Unavailable, sampleUnavailable), view.Unavailable)
						assert.Contains(t, view.Unavailable, want)
						assert.Zero(t, view.Current, "an unavailable sample computes no selection")
						assert.Zero(t, view.Candidate)

						return
					}

					require.Empty(t, view.Unavailable)
					assert.NotEmpty(t, view.Current.Lessons, "both lists are computed")
					assert.NotEmpty(t, view.Candidate.Lessons)
					assert.Equal(t, []string{"a.go", "api/new.go"}, view.Paths)
					assert.Equal(t, servicestest.Now, view.ReadAt)
					assert.False(t, view.IndexedAt.IsZero(), "a ready index names its build time")

					if !state.foreign {
						assert.Equal(t, rc.stale, view.Stale, "Stale compares the disk bytes with Before")
					}
				})
			}
		}
	}
}

// A surface outside the enumeration is a programming error. The sample
// says so before any other reason, and the label shows the bad value.
func TestEngineSampleRefusesAnUnknownSurface(t *testing.T) {
	e := engNew(sampleWorkspace(t), Deps{})
	saved := []byte(samplePins)
	bad := SampleSurface(99)

	assert.Equal(t, "hook selection", SampleHook.String())
	assert.Equal(t, "change-set selection", SampleChangeSet.String())
	assert.Equal(t, "SampleSurface(99)", bad.String())

	for name, paths := range map[string][]string{"with paths": {"a.go"}, "without paths": nil} {
		t.Run(name, func(t *testing.T) {
			view := e.Sample(SampleRequest{Surface: bad, Paths: paths, Before: saved, After: saved})

			assert.Equal(t, sampleUnavailable+"unknown surface SampleSurface(99).", view.Unavailable)
			assert.Equal(t, bad, view.Surface)
			assert.Nil(t, view.Paths, "the sample names no path")
			assert.Zero(t, view.Current, "an unavailable sample computes no selection")
			assert.Zero(t, view.Candidate)
		})
	}
}

func TestEngineSampleComparesTheReviewWithTheSavedFile(t *testing.T) {
	ws := sampleWorkspace(t)
	e := engNew(ws, Deps{})
	saved := []byte(samplePins)

	view := e.Sample(SampleRequest{
		Surface: SampleChangeSet,
		Paths:   []string{"a.go", "api", "api/new.go"},
		Before:  saved,
		After:   append(append([]byte{}, saved...), sampleMute...),
	})
	require.Empty(t, view.Unavailable)

	assert.Equal(t, time.Unix(workspace.Probe(ws).IndexedAt, 0), view.IndexedAt)
	assert.False(t, view.Stale)
	assert.Equal(t, reviews.DefaultThreshold, view.CurrentThreshold, "an absent threshold reads as the default")
	assert.Equal(t, reviews.DefaultThreshold, view.CandidateThreshold)
	assert.Equal(t, []string{"api/new.go"}, view.NoFile, "a future path is a label")
	assert.Equal(t, []string{"api", "api/new.go"}, view.Unindexed, "the index knows a.go only")

	hookLabels := e.Sample(SampleRequest{Surface: SampleHook, Paths: []string{"api/new.go", "lnk.go"}, Before: saved, After: saved})
	require.Empty(t, hookLabels.Unavailable)
	assert.Equal(t, []string{"api/new.go", "a.go"}, hookLabels.Paths, "the hook resolves the link")
	assert.Equal(t, []string{"api/new.go"}, hookLabels.Unindexed, "the hook surface is labelled too")
	assert.Equal(t, []string{"api/new.go"}, hookLabels.NoFile)

	symptoms := func(ls []domain.Lesson) []string {
		var out []string
		for _, l := range ls {
			out = append(out, l.Symptom)
		}

		return out
	}

	// change_set fills its budget of six with the six pins, so its mined
	// lessons are over the total. The hook keeps room for them.
	assert.Contains(t, symptoms(view.Current.MinedOverTotal), "RUF001")
	assert.NotContains(t, symptoms(view.Candidate.MinedOverTotal), "RUF001", "the review mutes it")

	hook := e.Sample(SampleRequest{
		Surface: SampleHook, Paths: []string{"a.go"}, Before: saved,
		After: append(append([]byte{}, saved...), sampleMute...),
	})
	require.Empty(t, hook.Unavailable)
	assert.Contains(t, symptoms(hook.Current.Mined), "RUF001")
	assert.NotContains(t, symptoms(hook.Candidate.Mined), "RUF001", "the review mutes it")
	assert.Equal(t, hook.Current.Pins, hook.Candidate.Pins, "a mute never hides a pin")

	// Each side names its own threshold, so a view can word an exclusion
	// below it.
	raised := e.Sample(SampleRequest{
		Surface: SampleHook, Paths: []string{"a.go"}, Before: saved,
		After: append([]byte("threshold: 7\n"), saved...),
	})
	require.Empty(t, raised.Unavailable)
	assert.Equal(t, reviews.DefaultThreshold, raised.CurrentThreshold)
	assert.Equal(t, 7, raised.CandidateThreshold)

	var muted *report.Exclusion

	for i := range view.Candidate.Excluded {
		if view.Candidate.Excluded[i].Lesson.Symptom == "RUF001" {
			muted = &view.Candidate.Excluded[i]
		}
	}

	require.NotNil(t, muted, "the exclusion names its reason")
	assert.Equal(t, []reviews.MuteRule{{Rule: "RUF001"}}, muted.Mutes)

	// Another text on disk marks the saved side stale.
	servicestest.Write(t, ws, pinfile.File, samplePins+"threshold: 3\n")

	view = e.Sample(SampleRequest{Surface: SampleHook, Paths: []string{"a.go"}, Before: saved, After: saved})
	require.Empty(t, view.Unavailable)
	assert.True(t, view.Stale)

	// An absent file holds no bytes: a review that creates the file starts
	// from no bytes, so its saved side is the disk (P4-9).
	require.NoError(t, os.Remove(filepath.Join(ws.Root, filepath.FromSlash(pinfile.File))))

	view = e.Sample(SampleRequest{Surface: SampleHook, Paths: []string{"a.go"}, After: saved})
	require.Empty(t, view.Unavailable)
	assert.False(t, view.Stale, "no bytes match an absent file")

	view = e.Sample(SampleRequest{Surface: SampleHook, Paths: []string{"a.go"}, Before: saved, After: saved})
	assert.True(t, view.Stale, "a text does not match an absent file")
}

func TestEngineSampleWritesNothing(t *testing.T) {
	ws := sampleWorkspace(t)
	e := engNew(ws, Deps{})
	saved := []byte(samplePins)

	before := servicestest.Tree(t, ws.Root)
	probe := workspace.Probe(ws)

	afters := [][]byte{
		saved,
		append(append([]byte{}, saved...), sampleMute...),
		append(append([]byte{}, saved...), "hook_delivery: once-per-context\n"...),
	}

	for _, surface := range []SampleSurface{SampleHook, SampleChangeSet} {
		for _, paths := range [][]string{
			{"a.go", "api", "api/new.go", "lnk.go", "../outside.go"},
			{"../outside.go"},
			nil,
		} {
			for _, after := range afters {
				view := e.Sample(SampleRequest{Surface: surface, Paths: paths, Before: saved, After: after})
				assert.True(t, view.Unavailable != "" || len(view.Current.Lessons) > 0, "%s %v", surface, paths)
			}
		}
	}

	assert.Equal(t, before, servicestest.Tree(t, ws.Root), "a sample writes nothing")
	assert.NoFileExists(t, reviews.AuditLogPath(ws.Root), "a sample records no firing")
	assert.NoFileExists(t, filepath.Join(ws.Root, ".seamark", "lessons-hook-state.json"), "nor delivery state")
	assert.NoFileExists(t, filepath.Join(ws.Root, ".seamark", "lessons-hook-state.lock"), "nor a delivery lease")

	after := workspace.Probe(ws)
	assert.Equal(t, probe.IndexedAt, after.IndexedAt, "a sample builds no index")
	assert.Equal(t, probe.Schema, after.Schema, "a sample migrates nothing")
	engUnlocked(t, e)
}

func TestEngineSampleMatchesTheProductionEntries(t *testing.T) {
	ws := sampleWorkspace(t)
	e := engNew(ws, Deps{})
	saved := []byte(samplePins)

	cfg, err := reviews.ParseConfig(saved)
	require.NoError(t, err)
	require.Equal(t, reviews.HookDeliveryAlways, cfg.HookDelivery(), "no lease can drop a lesson")

	st, err := store.OpenReadOnly(ws.DBPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	for name, paths := range map[string][]string{
		"an existing file":  {"a.go"},
		"a directory":       {"api"},
		"a future path":     {"api/new.go"},
		"an outside path":   {"../outside.go"},
		"a symlinked path":  {"lnk.go"},
		"overlapping paths": {"api/new.go", "a.go", "api", "lnk.go", "../outside.go", "a.go"},
	} {
		t.Run(name, func(t *testing.T) {
			hook := e.Sample(SampleRequest{Surface: SampleHook, Paths: paths, Before: saved, After: saved})
			sampleMatchesTheHook(t, ws, st, cfg, paths, hook)

			cs := e.Sample(SampleRequest{Surface: SampleChangeSet, Paths: paths, Before: saved, After: saved})
			sampleMatchesChangeSet(t, ws, st, cfg, paths, cs)
		})
	}
}

// sampleMatchesTheHook compares a hook sample with delivery.Deliver in
// the mode "always": the files, the rejected paths, the held-back count,
// and the emitted text.
func sampleMatchesTheHook(t *testing.T, ws workspace.Workspace, st *store.Store, cfg *reviews.Config,
	paths []string, view SampleView,
) {
	t.Helper()

	var emitted []string

	out, err := delivery.Deliver(context.Background(), st, delivery.Request{
		Root: ws.Root, ClientID: "claude",
		Event:  integration.EditEvent{EventMeta: integration.EventMeta{CWD: ws.Root}, Paths: paths},
		Config: cfg,
	}, func(advice string) error {
		emitted = append(emitted, advice)

		return nil
	})
	require.NoError(t, err)

	assert.Equal(t, out.Files, view.Paths, "the hook names the files")
	assert.Equal(t, out.Rejected, view.Rejected)

	if len(out.Files) == 0 {
		assert.Contains(t, view.Unavailable, sampleNoUsablePath)
		assert.Empty(t, emitted, "the hook is silent too")

		return
	}

	require.Empty(t, view.Unavailable)

	_, unindexed := report.ChangeSetNames(st, ws.Root, out.Files)
	assert.Equal(t, unindexed, view.Unindexed, "the hook files get the change_set label of the index")

	lessons, held, err := report.LessonsForFilesBudget(st, cfg, out.Files, report.HookBudget(cfg))
	require.NoError(t, err)
	assert.Equal(t, lessons, view.Current.Lessons)
	assert.Equal(t, held, view.Current.HeldBack)
	assert.Equal(t, out.HeldBackPins, view.Current.HeldBack)
	assert.Equal(t, view.Current, view.Candidate, "one text gives one selection")

	if len(view.Current.Lessons) == 0 {
		assert.Empty(t, emitted)

		return
	}

	var want strings.Builder
	require.NoError(t, report.PrintEditReminder(&want, view.Paths, view.Current.Lessons, view.Current.HeldBack))
	require.Len(t, emitted, 1)
	assert.Equal(t, want.String(), emitted[0], "the hook emits the sample selection")
}

// sampleMatchesChangeSet compares a change_set sample with
// report.ChangeSet: the names, the unindexed names, and the lesson block.
func sampleMatchesChangeSet(t *testing.T, ws workspace.Workspace, st *store.Store, cfg *reviews.Config,
	paths []string, view SampleView,
) {
	t.Helper()

	require.Empty(t, view.Unavailable, "change_set rejects no path")

	names, unindexed := report.ChangeSetNames(st, ws.Root, paths)
	assert.Equal(t, names, view.Paths)
	assert.Equal(t, unindexed, view.Unindexed)
	assert.Nil(t, view.Rejected)

	lessons, trimmed, err := report.LessonsForFiles(st, cfg, names, cfg.ChangeSetBudget())
	require.NoError(t, err)
	assert.Equal(t, lessons, view.Current.Lessons)
	assert.Equal(t, trimmed, view.Current.HeldBack)

	var out strings.Builder
	require.NoError(t, report.ChangeSet(&out, st, ws.Root, paths))

	for _, name := range view.Unindexed {
		assert.Contains(t, out.String(), name+": not in the index")
	}

	if len(view.Current.Lessons) == 0 {
		assert.NotContains(t, out.String(), "lessons for this change")

		return
	}

	// The block body without its header is the sample selection.
	var block strings.Builder
	report.PrintLessonBlock(&block, "H", view.Current.Lessons, view.Current.HeldBack)

	body := strings.TrimPrefix(block.String(), "\nH\n")
	assert.Contains(t, out.String(), body, "change_set prints the sample selection")
}
