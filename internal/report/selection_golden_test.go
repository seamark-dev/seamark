package report

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/store/storetest"
)

// The tests in this file characterize the lesson selection of the edit
// hook, change_set, and check. They were written before the selection
// split into one evidence read and a fold per surface. The expected
// values come from the code before that split, so they stay fixed.

// writeLessonsYAML writes the lessons file of a test workspace.
func writeLessonsYAML(t *testing.T, root, text string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".seamark", "lessons.yaml"), []byte(text), 0o644))
}

// readOneFiring returns the only record of the firing log. The time
// stamp is checked and then cleared, because it changes on every run.
func readOneFiring(t *testing.T, root string) reviews.Firing {
	t.Helper()

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)
	require.Len(t, firings, 1, "one answer appends exactly one firing record")

	rec := firings[0]
	assert.NotEmpty(t, rec.TS)
	rec.TS = ""

	return rec
}

func TestChangeSetRecordsOneFiringForItsLessons(t *testing.T) {
	st, root := seedStore(t)
	writeLessonsYAML(t, root, "pin:\n  - rule: guard-empty-datasets\n    region: scripts\n"+
		"    note: Guard datasets before reductions.\n")

	assert.NoFileExists(t, reviews.AuditLogPath(root))

	// The record names the file by its resolved repository path, not
	// by the spelling of the request.
	var b strings.Builder
	require.NoError(t, ChangeSet(&b, st, root, []string{"./scripts/task.py"}))

	assert.Equal(t, reviews.Firing{
		Surface: "change_set",
		File:    "scripts/task.py",
		Fired: []reviews.FiredLesson{
			{Region: "scripts", Symptom: "guard-empty-datasets — Guard datasets before reductions."},
			{Region: "scripts", Symptom: "RUF001"},
		},
	}, readOneFiring(t, root))
}

func TestCheckAdvisoryRecordsItsLessons(t *testing.T) {
	st, root := seedStore(t)
	writeLessonsYAML(t, root, "pin:\n  - rule: guard-empty-datasets\n    region: scripts\n"+
		"    note: Guard datasets before reductions.\n")

	// No lesson applies to this file, so the advisory stays silent and
	// records nothing.
	var b strings.Builder
	CheckAdvisory(&b, st, root, []string{"web/new.ts"})
	assert.Empty(t, b.String())
	assert.NoFileExists(t, reviews.AuditLogPath(root))

	CheckAdvisory(&b, st, root, []string{"scripts/task.py", "web/new.ts"})
	assert.Contains(t, b.String(), "advisory — recurring lessons for touched files")

	assert.Equal(t, reviews.Firing{
		Surface: "check",
		Files:   []string{"scripts/task.py", "web/new.ts"},
		Fired: []reviews.FiredLesson{
			{Region: "scripts", Symptom: "guard-empty-datasets — Guard datasets before reductions."},
			{Region: "scripts", Symptom: "RUF001"},
		},
	}, readOneFiring(t, root))
}

func TestChangeSetBudgetDefaultAndNonpositive(t *testing.T) {
	// Ten distinct repository-wide pins and the mined RUF001 lesson
	// give eleven lessons, more than every budget below.
	var pins strings.Builder

	pins.WriteString("pin:\n")

	for _, p := range distinctPins("*", 10) {
		fmt.Fprintf(&pins, "  - rule: %s\n    note: %s\n", p.Rule, p.Note)
	}

	cases := []struct {
		name    string
		setting string
		budget  int
	}{
		{"absent", "", 6},
		{"zero", "change_budget: 0\n", 6},
		{"negative", "change_budget: -1\n", 6},
		{"positive", "change_budget: 4\n", 4},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := pins.String() + tc.setting

			cfg, err := reviews.ParseConfig([]byte(text))
			require.NoError(t, err)
			assert.Equal(t, tc.budget, cfg.ChangeSetBudget())

			// The real answer: LoadConfig, the change budget, the
			// selection, the printed block, and the firing record.
			st, root := seedStore(t)
			writeLessonsYAML(t, root, text)

			var b strings.Builder
			require.NoError(t, ChangeSet(&b, st, root, []string{"scripts/task.py"}))

			out := b.String()
			assert.Equal(t, tc.budget, strings.Count(out, "\n- ["))
			assert.Contains(t, out, fmt.Sprintf("(+%d more: ", 11-tc.budget))
			assert.Len(t, readOneFiring(t, root).Fired, tc.budget)
		})
	}
}

func TestPinsIgnoreMutes(t *testing.T) {
	// A mute hides mined lessons only. A mute that names a pin's rule
	// or region leaves the pin in every selection.
	st, _ := seedStore(t)

	cfg := reviews.DefaultConfig()
	cfg.Pin = []reviews.PinRule{
		{Rule: "alpha-guard", Region: "scripts", Note: "n"},
		{Rule: "bravo-guard", Region: "*", Note: "n"},
	}
	cfg.Mute = []reviews.MuteRule{
		{Rule: "ALPHA-GUARD"},
		{Rule: "bravo-guard", Region: "*"},
		{Region: "scripts"},
	}

	files := []string{"scripts/task.py"}
	want := "alpha-guard — n\nbravo-guard — n\n"

	hook, _, err := LessonsForFilesBudget(st, cfg, files, HookBudget(cfg))
	require.NoError(t, err)
	assert.Equal(t, want, symptomsOf(hook), "the hook keeps both pins and mutes RUF001")

	view, _, err := LessonsForFilesBudget(st, cfg, files, LessonBudget{Total: HookLessonTotal})
	require.NoError(t, err)
	assert.Equal(t, want, symptomsOf(view), "the deliberate view keeps both pins")

	union, _, err := LessonsForFiles(st, cfg, files, cfg.ChangeSetBudget())
	require.NoError(t, err)
	assert.Equal(t, want, symptomsOf(union), "change_set and check keep both pins")

	scope, err := LessonsForScope(st, cfg, files[0], 6)
	require.NoError(t, err)
	assert.Equal(t, want, symptomsOf(scope), "the single-file selector keeps both pins")
}

// goldenStore builds an index with applied proposals of each confidence
// tier. The recorded root holds some cited files, so the dead-path
// check runs. Mined lessons have distinct occurrence and time values,
// so the store order is fixed.
func goldenStore(t *testing.T) *store.Store {
	t.Helper()

	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	require.NoError(t, st.SetMeta(store.MetaRepoRoot, root))

	for _, rel := range []string{"scripts/db.py", "db/a.go", "db/b.go", "db/c.go", "lib/a.go", "lib/b.go", "lib/c.go"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("x\n"), 0o644))
	}

	fresh := time.Now().Add(-10 * 24 * time.Hour).Unix()

	require.NoError(t, st.ReplaceLessons([]model.Lesson{
		{ClusterKey: "scripts\x00RUF001", Region: "scripts", Reviewer: "coderabbit", Symptom: "RUF001", Occurrences: 6, LastTS: 100},
		{ClusterKey: "scripts\x00once", Region: "scripts", Reviewer: "person", Symptom: "solitary finding", Occurrences: 1, LastTS: 50},
		{ClusterKey: "k-creds", Region: "scripts", Reviewer: "copilot", Symptom: "hard codes a postgres url including credentials", Occurrences: 2, LastTS: 90},
		{ClusterKey: "k-sub", Region: "scripts/sub", Reviewer: "coderabbit", Symptom: "E702", Occurrences: 3, LastTS: 80},
		{ClusterKey: "k-api", Region: "api", Reviewer: "person", Symptom: "wrap engine context", Occurrences: 3, LastTS: 70},
		{ClusterKey: "k-web", Region: "web", Reviewer: "coderabbit", Symptom: "E501", Occurrences: 4, LastTS: 60},
		{ClusterKey: "k-db", Region: "db", Reviewer: "person", Symptom: "close every cursor", Occurrences: 2, LastTS: 40},
	}, []model.Finding{
		// k-creds: two events, review only, no time stamp, one cited
		// file gone: fair evidence.
		{ID: 1, LessonKey: "k-creds", Path: "scripts/db.py", Body: "b", Source: model.SourceReview},
		{ID: 2, LessonKey: "k-creds", Path: "scripts/etl.py", Body: "b", Source: model.SourceReview},
		// k-api: the weak pin cites two of three findings, one event.
		{ID: 10, LessonKey: "k-api", Path: "api/a.go", PR: 9, Body: "b", Source: model.SourceReview},
		{ID: 11, LessonKey: "k-api", Path: "api/b.go", PR: 9, Body: "b", Source: model.SourceReview},
		{ID: 12, LessonKey: "k-api", Path: "api/c.go", PR: 12, Body: "b", Source: model.SourceReview},
		// k-db: three events, review and fix, fresh, all files live:
		// strong evidence that covers the whole cluster.
		{ID: 20, LessonKey: "k-db", Path: "db/a.go", PR: 1, Body: "b", Source: model.SourceReview, CreatedAt: fresh},
		{ID: 21, LessonKey: "k-db", Path: "db/b.go", PR: 2, Body: "b", Source: model.SourceFixSubject, CreatedAt: fresh},
		{ID: 22, LessonKey: "k-db", Path: "db/c.go", PR: 3, Body: "b", Source: model.SourceReview, CreatedAt: fresh},
		// k-lib: strong evidence of a second applied row that shares
		// the key of the weak pin.
		{ID: 30, LessonKey: "k-lib", Path: "lib/a.go", PR: 4, Body: "b", Source: model.SourceReview, CreatedAt: fresh},
		{ID: 31, LessonKey: "k-lib", Path: "lib/b.go", PR: 5, Body: "b", Source: model.SourceFixSubject, CreatedAt: fresh},
		{ID: 32, LessonKey: "k-lib", Path: "lib/c.go", PR: 6, Body: "b", Source: model.SourceReview, CreatedAt: fresh},
	}))

	// Insertion order sets the IDs. The weak row gets a lower ID than
	// the strong row with the same key.
	for _, p := range []model.Proposal{
		{Signature: "sig-creds", Rule: "hardcoded-db-credentials", Region: "scripts",
			Note: "Read credentials from the environment.", Members: []int64{1, 2}},
		{Signature: "sig-weak", Rule: "single-event-guidance", Region: "api",
			Note: "Distilled from one review exchange.", Members: []int64{10, 11}},
		{Signature: "sig-strong", Rule: "multi-source-guard", Region: "db",
			Note: "Close cursors in a finally block.", Members: []int64{20, 21, 22}},
		{Signature: "sig-twin", Rule: "single-event-guidance", Region: "api",
			Note: "A second row for the same pin.", Members: []int64{30, 31, 32}},
	} {
		p.Status = model.ProposalApplied
		require.NoError(t, storetest.InsertProposal(st, &p))
	}

	return st
}

// renderGolden writes one selection as lines of
// region|symptom|reviewer|annotation after a header with the held-back
// count.
func renderGolden(b *strings.Builder, label string, lessons []model.Lesson, held int, err error) {
	if err != nil {
		fmt.Fprintf(b, "%s error %v\n", label, err)

		return
	}

	fmt.Fprintf(b, "%s held %d\n", label, held)

	for _, l := range lessons {
		fmt.Fprintf(b, "  %s|%s|%s|%s\n", l.Region, l.Symptom, l.Reviewer, l.Annotation)
	}
}

// goldenSelections renders the selections of every surface and budget
// for one store, config, and file list.
func goldenSelections(st *store.Store, cfg *reviews.Config, files []string) string {
	var b strings.Builder

	hookBudgets := []struct {
		label  string
		budget LessonBudget
	}{
		{"hook", HookBudget(cfg)},
		{"hook total 3 pins 2", LessonBudget{Total: 3, Pins: 2}},
		{"view total 8 pins 0", LessonBudget{Total: HookLessonTotal}},
	}

	for _, hb := range hookBudgets {
		lessons, held, err := LessonsForFilesBudget(st, cfg, files, hb.budget)
		renderGolden(&b, hb.label, lessons, held, err)
	}

	for _, budget := range []int{cfg.ChangeSetBudget(), 0} {
		lessons, held, err := LessonsForFiles(st, cfg, files, budget)
		renderGolden(&b, fmt.Sprintf("change_set %d", budget), lessons, held, err)
	}

	return b.String()
}

// golden joins the expected lines of goldenSelections.
func golden(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// goldenConfig returns the default config with the given pins.
func goldenConfig(pins ...reviews.PinRule) *reviews.Config {
	cfg := reviews.DefaultConfig()
	cfg.Pin = pins

	return cfg
}

func TestSelectionGoldens(t *testing.T) {
	weak := reviews.PinRule{Rule: "single-event-guidance", Region: "api", Note: "Distilled from one review exchange."}
	manual := reviews.PinRule{Rule: "hand-written-guard", Region: "api", Note: "This guidance was added manually."}
	strong := reviews.PinRule{Rule: "multi-source-guard", Region: "db", Note: "Close cursors in a finally block."}
	creds := reviews.PinRule{Rule: "hardcoded-db-credentials", Region: "scripts", Note: "Read credentials from the environment."}
	wide := reviews.PinRule{Rule: "secrets-hygiene", Region: "*", Note: "Applies to every file exactly once."}

	threshold := goldenConfig(manual)
	threshold.Threshold = 4

	mutes := goldenConfig(manual, creds)
	mutes.Threshold = 1
	mutes.Mute = []reviews.MuteRule{
		{Rule: "ruf001"},
		{Region: "web"},
		{Rule: "E702", Region: "scripts/sub"},
		{Rule: "hand-written-guard"},
		{},
	}

	capAbove := goldenConfig(distinctPins("*", 10)...)
	capAbove.PinBudget = 20
	capAbove.ChangeBudget = 9

	cases := []struct {
		name string
		// golden selects goldenStore, which holds applied proposals.
		// Without it the case reads seedStore.
		golden bool
		cfg    *reviews.Config
		files  []string
		want   string
	}{
		{
			name:   "no applied rows",
			golden: false,
			cfg: goldenConfig(
				reviews.PinRule{Rule: "wide-guard", Region: "*", Note: "n"},
				reviews.PinRule{Rule: "dir-guard", Region: "scripts", Note: "n"},
				reviews.PinRule{Rule: "file-guard", Region: "scripts/task.py", Note: "n"},
				reviews.PinRule{Rule: "multi-guard", Region: "web", Regions: []string{"scripts"}, Note: "n"},
			),
			files: []string{"scripts/task.py"},
			want: golden(
				"hook held 1",
				"  scripts/task.py|file-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  web, scripts|multi-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 2",
				"  scripts/task.py|file-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts/task.py|file-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  web, scripts|multi-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 0",
				"  scripts/task.py|file-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  web, scripts|multi-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 0",
				"  scripts/task.py|file-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  web, scripts|multi-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "confidence ties",
			golden: true,
			cfg:    goldenConfig(weak, manual, strong, creds, wide),
			files:  []string{"api/x.go", "db/y.go", "scripts/foo.py"},
			want: golden(
				"hook held 2",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  api|wrap engine context|person|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 3",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  api|wrap engine context|person|",
				"view total 8 pins 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|strong: 3 event(s), fix+review, newest 10d",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|fair: 2 event(s), review, age unknown, 1 cited file(s) deleted",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  api|wrap engine context|person|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 1",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  api|wrap engine context|person|",
				"change_set 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  api|wrap engine context|person|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "specificity",
			golden: false,
			cfg: goldenConfig(
				reviews.PinRule{Rule: "wide-guard", Region: "*", Note: "n"},
				reviews.PinRule{Rule: "dir-guard", Region: "scripts", Note: "n"},
				reviews.PinRule{Rule: "sub-guard", Region: "scripts/sub", Note: "n"},
				reviews.PinRule{Rule: "file-guard", Region: "scripts/sub/task.py", Note: "n"},
			),
			files: []string{"scripts/sub/task.py"},
			want: golden(
				"hook held 1",
				"  scripts/sub/task.py|file-guard — n|pinned|",
				"  scripts/sub|sub-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 2",
				"  scripts/sub/task.py|file-guard — n|pinned|",
				"  scripts/sub|sub-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts/sub/task.py|file-guard — n|pinned|",
				"  scripts/sub|sub-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 0",
				"  scripts/sub/task.py|file-guard — n|pinned|",
				"  scripts/sub|sub-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 0",
				"  scripts/sub/task.py|file-guard — n|pinned|",
				"  scripts/sub|sub-guard — n|pinned|",
				"  scripts|dir-guard — n|pinned|",
				"  *|wide-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "file order",
			golden: true,
			cfg: goldenConfig(
				reviews.PinRule{Rule: "db-guard", Region: "db", Note: "n"},
				reviews.PinRule{Rule: "api-guard", Region: "api", Note: "n"},
				reviews.PinRule{Rule: "web-guard", Region: "web", Note: "n"},
			),
			files: []string{"web/c.ts", "api/a.go", "db/b.go", "scripts/x.py"},
			want: golden(
				"hook held 1",
				"  web|web-guard — n|pinned|",
				"  api|api-guard — n|pinned|",
				"  web|E501|coderabbit|",
				"  api|wrap engine context|person|",
				"  db|close every cursor|person|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"hook total 3 pins 2 held 1",
				"  web|web-guard — n|pinned|",
				"  api|api-guard — n|pinned|",
				"  web|E501|coderabbit|",
				"view total 8 pins 0 held 0",
				"  web|web-guard — n|pinned|",
				"  api|api-guard — n|pinned|",
				"  db|db-guard — n|pinned|",
				"  web|E501|coderabbit|",
				"  api|wrap engine context|person|",
				"  db|close every cursor|person|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"change_set 6 held 2",
				"  web|web-guard — n|pinned|",
				"  api|api-guard — n|pinned|",
				"  web|E501|coderabbit|",
				"  api|wrap engine context|person|",
				"  db|close every cursor|person|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 1",
				"  web|web-guard — n|pinned|",
				"  api|api-guard — n|pinned|",
				"  web|E501|coderabbit|",
				"  api|wrap engine context|person|",
				"  db|close every cursor|person|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
			),
		},
		{
			name:   "restatements",
			golden: false,
			cfg: goldenConfig(
				reviews.PinRule{Rule: "guard-empty-datasets", Region: "scripts", Note: "Guard datasets before reductions."},
				reviews.PinRule{Rule: "guard-empty-datasets", Region: "*", Note: "Guard datasets before reductions."},
				reviews.PinRule{Rule: "transaction-atomicity", Region: "scripts/task.py", Note: "Wrap dependent writes in one transaction."},
				reviews.PinRule{Rule: "E702", Region: "scripts"},
				reviews.PinRule{Rule: "e702", Region: "*"},
			),
			files: []string{"scripts/task.py"},
			want: golden(
				"hook held 2",
				"  scripts/task.py|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  scripts|E702|pinned|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 3",
				"  scripts/task.py|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts/task.py|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  scripts|E702|pinned|",
				"  *|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  *|e702|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 2",
				"  scripts/task.py|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  scripts|E702|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 2",
				"  scripts/task.py|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|guard-empty-datasets — Guard datasets before reductions.|pinned|",
				"  scripts|E702|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "repeated pin identity",
			golden: false,
			cfg: goldenConfig(
				reviews.PinRule{Rule: "error-handling", Region: "scripts", Note: "Wrap errors with context."},
				reviews.PinRule{Rule: "transaction-atomicity", Region: "*", Note: "Wrap dependent writes in one transaction."},
				reviews.PinRule{Rule: "error-handling", Region: "scripts/", Note: "Never swallow an error."},
				reviews.PinRule{Rule: "error-handling", Region: "scripts", Note: "Wrap errors with context."},
			),
			files: []string{"scripts/task.py", "scripts/other.py"},
			want: golden(
				"hook held 2",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  *|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 2",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  *|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  scripts|error-handling — Never swallow an error.|pinned|",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  *|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 0",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  *|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 0",
				"  scripts|error-handling — Wrap errors with context.|pinned|",
				"  *|transaction-atomicity — Wrap dependent writes in one transaction.|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "overlapping paths",
			golden: true,
			cfg:    goldenConfig(wide, reviews.PinRule{Rule: "dir-guard", Region: "scripts", Note: "n"}),
			files:  []string{"scripts", "scripts/sub/task.py", "scripts/sub/task.py", "scripts/other.py"},
			want: golden(
				"hook held 0",
				"  scripts|dir-guard — n|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  scripts/sub|E702|coderabbit|",
				"hook total 3 pins 2 held 0",
				"  scripts|dir-guard — n|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts|dir-guard — n|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  scripts/sub|E702|coderabbit|",
				"change_set 6 held 0",
				"  scripts|dir-guard — n|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  scripts/sub|E702|coderabbit|",
				"change_set 0 held 0",
				"  scripts|dir-guard — n|pinned|",
				"  *|secrets-hygiene — Applies to every file exactly once.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  scripts/sub|E702|coderabbit|",
			),
		},
		{
			name:   "mutes",
			golden: true,
			cfg:    mutes,
			files:  []string{"scripts/sub/x.py", "web/a.ts", "api/b.go"},
			want: golden(
				"hook held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  scripts|solitary finding|person|",
				"  api|wrap engine context|person|",
				"hook total 3 pins 2 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  scripts|solitary finding|person|",
				"view total 8 pins 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|fair: 2 event(s), review, age unknown, 1 cited file(s) deleted",
				"  scripts|solitary finding|person|",
				"  api|wrap engine context|person|",
				"change_set 6 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  scripts|solitary finding|person|",
				"  api|wrap engine context|person|",
				"change_set 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  scripts|solitary finding|person|",
				"  api|wrap engine context|person|",
			),
		},
		{
			name:   "threshold",
			golden: true,
			cfg:    threshold,
			files:  []string{"scripts/x.py", "web/a.ts", "api/b.go"},
			want: golden(
				"hook held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  web|E501|coderabbit|",
				"hook total 3 pins 2 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  web|E501|coderabbit|",
				"view total 8 pins 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  web|E501|coderabbit|",
				"change_set 6 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  web|E501|coderabbit|",
				"change_set 0 held 0",
				"  api|hand-written-guard — This guidance was added manually.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"  web|E501|coderabbit|",
			),
		},
		{
			name:   "coverage by an applied live pin",
			golden: true,
			cfg:    goldenConfig(creds, strong, weak),
			files:  []string{"scripts/x.py", "db/a.go", "api/b.go"},
			want: golden(
				"hook held 0",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  api|wrap engine context|person|",
				"hook total 3 pins 2 held 1",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|strong: 3 event(s), fix+review, newest 10d",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|fair: 2 event(s), review, age unknown, 1 cited file(s) deleted",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  api|wrap engine context|person|",
				"change_set 6 held 0",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  api|wrap engine context|person|",
				"change_set 0 held 0",
				"  db|multi-source-guard — Close cursors in a finally block.|pinned|",
				"  scripts|hardcoded-db-credentials — Read credentials from the environment.|pinned|",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  api|wrap engine context|person|",
			),
		},
		{
			name:   "hand-removed pin",
			golden: true,
			cfg:    goldenConfig(weak),
			files:  []string{"scripts/x.py", "db/a.go", "api/b.go"},
			want: golden(
				"hook held 0",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  db|close every cursor|person|",
				"  api|wrap engine context|person|",
				"hook total 3 pins 2 held 0",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"view total 8 pins 0 held 0",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  db|close every cursor|person|",
				"  api|wrap engine context|person|",
				"change_set 6 held 0",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  db|close every cursor|person|",
				"  api|wrap engine context|person|",
				"change_set 0 held 0",
				"  api|single-event-guidance — Distilled from one review exchange.|pinned|weak evidence: 1 event(s), review, age unknown, 2 cited file(s) deleted",
				"  scripts|RUF001|coderabbit|",
				"  scripts|hard codes a postgres url including credentials|copilot|",
				"  db|close every cursor|person|",
				"  api|wrap engine context|person|",
			),
		},
		{
			name:   "pin cap above 8",
			golden: false,
			cfg:    capAbove,
			files:  []string{"scripts/task.py", "web/c.ts"},
			want: golden(
				"hook held 2",
				"  *|alpha-guard — n|pinned|",
				"  *|bravo-guard — n|pinned|",
				"  *|charlie-guard — n|pinned|",
				"  *|delta-guard — n|pinned|",
				"  *|echo-guard — n|pinned|",
				"  *|foxtrot-guard — n|pinned|",
				"  *|golf-guard — n|pinned|",
				"  *|hotel-guard — n|pinned|",
				"hook total 3 pins 2 held 8",
				"  *|alpha-guard — n|pinned|",
				"  *|bravo-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 2",
				"  *|alpha-guard — n|pinned|",
				"  *|bravo-guard — n|pinned|",
				"  *|charlie-guard — n|pinned|",
				"  *|delta-guard — n|pinned|",
				"  *|echo-guard — n|pinned|",
				"  *|foxtrot-guard — n|pinned|",
				"  *|golf-guard — n|pinned|",
				"  *|hotel-guard — n|pinned|",
				"change_set 9 held 2",
				"  *|alpha-guard — n|pinned|",
				"  *|bravo-guard — n|pinned|",
				"  *|charlie-guard — n|pinned|",
				"  *|delta-guard — n|pinned|",
				"  *|echo-guard — n|pinned|",
				"  *|foxtrot-guard — n|pinned|",
				"  *|golf-guard — n|pinned|",
				"  *|hotel-guard — n|pinned|",
				"  *|india-guard — n|pinned|",
				"change_set 0 held 0",
				"  *|alpha-guard — n|pinned|",
				"  *|bravo-guard — n|pinned|",
				"  *|charlie-guard — n|pinned|",
				"  *|delta-guard — n|pinned|",
				"  *|echo-guard — n|pinned|",
				"  *|foxtrot-guard — n|pinned|",
				"  *|golf-guard — n|pinned|",
				"  *|hotel-guard — n|pinned|",
				"  *|india-guard — n|pinned|",
				"  *|juliet-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "pins above the pin cap",
			golden: false,
			cfg: goldenConfig(append(distinctPins("scripts", 5),
				reviews.PinRule{Rule: "alpha-guard", Region: "*", Note: "n"})...),
			files: []string{"scripts/task.py"},
			want: golden(
				"hook held 3",
				"  scripts|alpha-guard — n|pinned|",
				"  scripts|bravo-guard — n|pinned|",
				"  scripts|charlie-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"hook total 3 pins 2 held 4",
				"  scripts|alpha-guard — n|pinned|",
				"  scripts|bravo-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"view total 8 pins 0 held 0",
				"  scripts|alpha-guard — n|pinned|",
				"  scripts|bravo-guard — n|pinned|",
				"  scripts|charlie-guard — n|pinned|",
				"  scripts|delta-guard — n|pinned|",
				"  scripts|echo-guard — n|pinned|",
				"  *|alpha-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 6 held 1",
				"  scripts|alpha-guard — n|pinned|",
				"  scripts|bravo-guard — n|pinned|",
				"  scripts|charlie-guard — n|pinned|",
				"  scripts|delta-guard — n|pinned|",
				"  scripts|echo-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
				"change_set 0 held 1",
				"  scripts|alpha-guard — n|pinned|",
				"  scripts|bravo-guard — n|pinned|",
				"  scripts|charlie-guard — n|pinned|",
				"  scripts|delta-guard — n|pinned|",
				"  scripts|echo-guard — n|pinned|",
				"  scripts|RUF001|coderabbit|",
			),
		},
		{
			name:   "no files",
			golden: true,
			cfg:    goldenConfig(wide, creds),
			files:  nil,
			want: golden(
				"hook held 0",
				"hook total 3 pins 2 held 0",
				"view total 8 pins 0 held 0",
				"change_set 6 held 0",
				"change_set 0 held 0",
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var st *store.Store
			if tc.golden {
				st = goldenStore(t)
			} else {
				st, _ = seedStore(t)
			}

			assert.Equal(t, tc.want, goldenSelections(st, tc.cfg, tc.files))
		})
	}
}
