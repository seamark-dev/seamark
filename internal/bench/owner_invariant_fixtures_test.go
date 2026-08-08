package bench

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheVersionFixture(t *testing.T) {
	instance := CacheVersionInstance()
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	require.NoError(t, instance.Generate(a))
	require.NoError(t, instance.Generate(b))
	assert.Equal(t, head(t, a), head(t, b))
	assertFixtureHealthyAndUntreated(t, a, instance)

	base, err := instance.Judge(a)
	require.NoError(t, err)
	assert.False(t, base.TaskDone)

	require.NoError(t, instance.ApplyNaive(a))
	naive, err := instance.Judge(a)
	require.NoError(t, err)
	assert.True(t, naive.TaskDone)
	assert.False(t, naive.Avoided)
	assert.Contains(t, naive.Notes, "cache namespace")
	assert.True(t, checksPass(runChecks(context.Background(), a, instance.Checks)))

	require.NoError(t, instance.ApplyGold(a))
	gold, err := instance.Judge(a)
	require.NoError(t, err)
	assert.True(t, gold.TaskDone)
	assert.True(t, gold.Avoided)
	goldChecks := runChecks(context.Background(), a, instance.Checks)
	assert.True(t, checksPass(goldChecks), failedChecks(goldChecks))

	log, err := exec.Command("git", "-C", b, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Contains(t, string(log), "invalidate cached workspace summaries")
	assert.NotContains(t, instance.Task, "cache")
	assert.NotContains(t, instance.Task, "WORKSPACE_SUMMARY_VERSION")
	assert.Contains(t, instance.LessonYAML, "WORKSPACE_SUMMARY_VERSION")
}

func TestExportRegistryFixture(t *testing.T) {
	instance := ExportRegistryInstance()
	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	require.NoError(t, instance.Generate(a))
	require.NoError(t, instance.Generate(b))
	assert.Equal(t, head(t, a), head(t, b))
	assertFixtureHealthyAndUntreated(t, a, instance)

	base, err := instance.Judge(a)
	require.NoError(t, err)
	assert.False(t, base.TaskDone)

	require.NoError(t, instance.ApplyNaive(a))
	naive, err := instance.Judge(a)
	require.NoError(t, err)
	assert.True(t, naive.TaskDone)
	assert.False(t, naive.Avoided)
	assert.Contains(t, naive.Notes, "registry is stale")
	assert.True(t, checksPass(runChecks(context.Background(), a, instance.Checks)))

	require.NoError(t, instance.ApplyGold(a))
	gold, err := instance.Judge(a)
	require.NoError(t, err)
	assert.True(t, gold.TaskDone)
	assert.True(t, gold.Avoided)
	goldChecks := runChecks(context.Background(), a, instance.Checks)
	assert.True(t, checksPass(goldChecks), failedChecks(goldChecks))

	log, err := exec.Command("git", "-C", b, "log", "--format=%s").Output()
	require.NoError(t, err)
	assert.Contains(t, string(log), "register JSON formatter for queued exports")
	assert.NotContains(t, instance.Task, "worker")
	assert.NotContains(t, instance.Task, "registry")
	assert.Contains(t, instance.LessonYAML, "internal/worker/registry.go")
}

func TestExportRegistryJudgeAcceptsEquivalentMarkdown(t *testing.T) {
	instance := ExportRegistryInstance()
	dir := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, instance.Generate(dir))
	require.NoError(t, instance.ApplyNaive(dir))

	// GFM permits flexible whitespace and one or more divider hyphens. Generic
	// solution helper names must not collide with the injected judge helpers.
	alternate := `package export

import (
	"fmt"
	"strings"
)

func FormatMarkdown(rows []Row) (string, error) {
	var out strings.Builder
	out.WriteString("| Name | Total |\n")
	out.WriteString("|-|-:|\n")
	for _, row := range rows {
		fmt.Fprintf(&out, "| %s | %d |\n", row.Name, row.Total)
	}
	return out.String(), nil
}

func sameCells() {}
func validDivider() {}
`
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "internal", "export", "markdown.go"), []byte(alternate), 0o644,
	))

	naive, err := instance.Judge(dir)
	require.NoError(t, err)
	assert.True(t, naive.TaskDone)
	assert.False(t, naive.Avoided)

	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "internal", "worker", "registry.go"),
		[]byte(exportRegistryWorkerGold), 0o644,
	))
	gold, err := instance.Judge(dir)
	require.NoError(t, err)
	assert.True(t, gold.TaskDone)
	assert.True(t, gold.Avoided)
}

func TestCacheVersionJudgeAcceptsEquivalentNamespaceInvalidation(t *testing.T) {
	for name, cache := range map[string]string{
		"later version": `WORKSPACE_SUMMARY_VERSION = 4

def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary:v{WORKSPACE_SUMMARY_VERSION}:{workspace_id}"
`,
		"new prefix": `WORKSPACE_SUMMARY_VERSION = 2

def workspace_summary_key(workspace_id: int) -> str:
    return f"workspace-summary-next:v{WORKSPACE_SUMMARY_VERSION}:{workspace_id}"
`,
	} {
		t.Run(name, func(t *testing.T) {
			instance := CacheVersionInstance()
			dir := filepath.Join(t.TempDir(), "fixture")
			require.NoError(t, instance.Generate(dir))
			require.NoError(t, instance.ApplyNaive(dir))
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "server", "cache.py"), []byte(cache), 0o644,
			))

			verdict, err := instance.Judge(dir)
			require.NoError(t, err)
			assert.True(t, verdict.TaskDone)
			assert.True(t, verdict.Avoided)
		})
	}
}

func TestOwnerInvariantFixturePreflights(t *testing.T) {
	for _, instance := range []Instance{CacheVersionInstance(), ExportRegistryInstance()} {
		t.Run(instance.ID, func(t *testing.T) {
			err := Preflight(context.Background(), RunConfig{
				Instance: instance, SeamarkBin: "/opt/seamark/bin/seamark",
				PrepareIndex: false,
			})
			require.NoError(t, err)
		})
	}
}

func assertFixtureHealthyAndUntreated(t *testing.T, dir string, instance Instance) {
	t.Helper()

	results := runChecks(context.Background(), dir, instance.Checks)
	assert.True(t, checksPass(results), failedChecks(results))
	for _, rel := range []string{".seamark", ".claude"} {
		_, err := os.Stat(filepath.Join(dir, rel))
		assert.True(t, os.IsNotExist(err), rel)
	}
}
