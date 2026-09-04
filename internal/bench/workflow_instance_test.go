package bench

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

func TestWorkflowInstancesValidateAndResolve(t *testing.T) {
	seen := map[string]bool{}

	for _, instance := range WorkflowInstances() {
		require.NoError(t, instance.Validate(), instance.ID)
		assert.False(t, seen[instance.ID], "duplicate workflow instance %q", instance.ID)
		seen[instance.ID] = true

		// The workflow variants take no part in the lessons scoping claims.
		assert.Empty(t, instance.ComparisonFamily, instance.ID)
		assert.Empty(t, instance.ProtocolInstance, instance.ID)

		resolved, err := WorkflowInstanceByID(instance.ID)
		require.NoError(t, err)
		assert.Equal(t, instance.Trigger, resolved.Trigger)
		assert.Equal(t, instance.Companion, resolved.Companion)
	}

	assert.Equal(t, []string{
		SchemaSyncCochangeInstanceID, CacheVersionCochangeInstanceID, ExportRegistryCochangeInstanceID,
	}, WorkflowInstanceIDs())

	_, err := WorkflowInstanceByID("missing")
	require.ErrorContains(t, err, "unknown workflow instance")

	// The lessons catalogue must not pick the variants up: `-instance all` in
	// the lessons preflight stays a lessons-only selector.
	for _, id := range InstanceIDs() {
		assert.NotContains(t, id, "cochange")
	}
}

func TestWorkflowInstanceValidateRejectsBadPairs(t *testing.T) {
	for name, mutate := range map[string]func(*WorkflowInstance){
		"empty trigger":       func(w *WorkflowInstance) { w.Trigger = "" },
		"empty companion":     func(w *WorkflowInstance) { w.Companion = "" },
		"same file":           func(w *WorkflowInstance) { w.Companion = w.Trigger },
		"absolute path":       func(w *WorkflowInstance) { w.Trigger = "/server/schema.py" },
		"parent path":         func(w *WorkflowInstance) { w.Companion = "../web/src/api/generated.ts" },
		"unclean path":        func(w *WorkflowInstance) { w.Trigger = "server//schema.py" },
		"backslash separator": func(w *WorkflowInstance) { w.Companion = `web\src\api\generated.ts` },
		"base instance":       func(w *WorkflowInstance) { w.Task = "" },
	} {
		t.Run(name, func(t *testing.T) {
			instance := SchemaSyncCochangeInstance()
			mutate(&instance)
			require.Error(t, instance.Validate())
		})
	}
}

// TestCochangeVariantsKeepTheBaseTreeAndCarryThePair is the AC-2 test for the
// three synthetic variants: deterministic, judged like the base, byte-equal to
// the base tree, and with the trigger and companion sharing commits.
func TestCochangeVariantsKeepTheBaseTreeAndCarryThePair(t *testing.T) {
	cases := []struct {
		variant WorkflowInstance
		base    Instance
		fix     string
	}{
		{SchemaSyncCochangeInstance(), SchemaSyncInstance(), "fix: refresh web types after workspace schema change"},
		{CacheVersionCochangeInstance(), CacheVersionInstance(), "fix: invalidate cached workspace summaries"},
		{ExportRegistryCochangeInstance(), ExportRegistryInstance(), "fix: register CSV formatter for queued exports"},
	}

	for _, tc := range cases {
		t.Run(tc.variant.ID, func(t *testing.T) {
			a := filepath.Join(t.TempDir(), "a")
			b := filepath.Join(t.TempDir(), "b")
			base := filepath.Join(t.TempDir(), "base")
			require.NoError(t, tc.variant.Generate(a))
			require.NoError(t, tc.variant.Generate(b))
			require.NoError(t, tc.base.Generate(base))
			assert.Equal(t, head(t, a), head(t, b), "generation must be deterministic")
			assert.NotEqual(t, head(t, a), head(t, base), "the variant is a different fixture")

			// Only the history differs: the working trees are byte-equal.
			assert.Equal(t, treeFiles(t, base), treeFiles(t, a))

			// The base's task, judges, patches, checks, and lesson carry over.
			assert.Equal(t, tc.base.Task, tc.variant.Task)
			assert.Equal(t, tc.base.LessonYAML, tc.variant.LessonYAML)
			assert.Equal(t, tc.base.JudgeVersion, tc.variant.JudgeVersion)
			assert.Equal(t, tc.base.Checks, tc.variant.Checks)

			// The fingerprint binds the judge (base file) and the history
			// (variant file), so a judge change moves the variant's cohorts.
			assert.Equal(t, tc.base.sourceFile, tc.variant.sourceFile)
			assert.NotEmpty(t, tc.variant.variantSourceFile)
			baseDigest, err := fingerprintInstanceSource(tc.base)
			require.NoError(t, err)
			variantDigest, err := fingerprintInstanceSource(tc.variant.Instance)
			require.NoError(t, err)
			assert.NotEqual(t, baseDigest, variantDigest)

			log, err := exec.Command("git", "-C", a, "log", "--format=%s").Output()
			require.NoError(t, err)
			assert.Contains(t, string(log), tc.fix, "the fix commit keeps the lesson plausible")

			shared := sharedCommits(t, a, tc.variant.Trigger, tc.variant.Companion)
			assert.GreaterOrEqual(t, shared, 2,
				"co-change mining needs at least two shared commits for %s and %s",
				tc.variant.Trigger, tc.variant.Companion)

			untouched, err := tc.variant.Judge(a)
			require.NoError(t, err)
			assert.False(t, untouched.TaskDone)
			assertChecksPass(t, mustRunChecks(t, a, tc.variant.Checks))

			require.NoError(t, tc.variant.ApplyNaive(a))
			naive, err := tc.variant.Judge(a)
			require.NoError(t, err)
			assert.True(t, naive.TaskDone)
			assert.False(t, naive.Avoided)
			assertChecksPass(t, mustRunChecks(t, a, tc.variant.Checks))

			require.NoError(t, tc.variant.ApplyGold(a))
			gold, err := tc.variant.Judge(a)
			require.NoError(t, err)
			assert.True(t, gold.TaskDone)
			assert.True(t, gold.Avoided)
			assertChecksPass(t, mustRunChecks(t, a, tc.variant.Checks))

			// The same code path the binary runs: after indexing, `why <trigger>`
			// names the companion among the files that usually change with it.
			partners := whyPartners(t, b, tc.variant.Trigger)
			assert.GreaterOrEqual(t, partners[tc.variant.Companion], 2,
				"why %s must name %s with at least two shared commits", tc.variant.Trigger, tc.variant.Companion)
		})
	}
}

// TestBaseFixturesDoNotCarryThePair documents the fact behind the variants:
// the lessons fixtures never commit the trigger with its companion twice.
func TestBaseFixturesDoNotCarryThePair(t *testing.T) {
	for _, tc := range []struct {
		base               Instance
		trigger, companion string
	}{
		{SchemaSyncInstance(), schemaSyncTrigger, schemaSyncCompanion},
		{CacheVersionInstance(), cacheVersionTrigger, cacheVersionCompanion},
		{ExportRegistryInstance(), exportRegistryTrigger, exportRegistryCompanion},
	} {
		dir := filepath.Join(t.TempDir(), "base")
		require.NoError(t, tc.base.Generate(dir))
		assert.Less(t, sharedCommits(t, dir, tc.trigger, tc.companion), 2, tc.base.ID)
	}
}

// treeFiles maps every path under dir, except .git, to its content.
func treeFiles(t *testing.T, dir string) map[string]string {
	t.Helper()

	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".bench-cache" {
				return filepath.SkipDir
			}

			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		files[filepath.ToSlash(rel)] = string(data)

		return nil
	})
	require.NoError(t, err)

	return files
}

// sharedCommits counts the commits in dir that touch both files.
func sharedCommits(t *testing.T, dir, first, second string) int {
	t.Helper()

	out, err := exec.Command("git", "-C", dir, "log", "--format=%x00%H", "--name-only").Output()
	require.NoError(t, err)

	shared := 0
	for _, record := range strings.Split(string(out), "\x00") {
		if strings.Contains(record, "\n"+first+"\n") && strings.Contains(record, "\n"+second+"\n") {
			shared++
		}
	}

	return shared
}

// whyPartners indexes dir in-process and returns the co-change partners the
// `why <file>` report names, keyed by path with their shared-commit counts.
func whyPartners(t *testing.T, dir, file string) map[string]int {
	t.Helper()

	dbPath := store.DefaultPath(dir)
	_, err := index.Run(index.Options{Root: dir, DBPath: dbPath})
	require.NoError(t, err)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = st.Close() }()

	var out bytes.Buffer
	require.NoError(t, report.Why(&out, st, dir, file))

	return parseWhyPartners(out.String())
}
