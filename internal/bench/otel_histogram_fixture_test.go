package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOTelHistogramInstancesShareProtocolExceptScope(t *testing.T) {
	trigger := OTelHistogramInstance()
	repair := OTelHistogramRepairInstance()

	require.NoError(t, trigger.Validate())
	require.NoError(t, repair.Validate())
	assert.Equal(t, OTelHistogramScopingFamily, trigger.ComparisonFamily)
	assert.Equal(t, trigger.ComparisonFamily, repair.ComparisonFamily)
	assert.Equal(t, trigger.ProtocolInstance, repair.ProtocolInstance)
	assert.Equal(t, HookExposureRequired, trigger.effectiveHookExposure())
	assert.Equal(t, HookExposureOptional, repair.effectiveHookExposure())
	assert.Equal(t, trigger.Task, repair.Task)
	assert.Equal(t, trigger.JudgeVersion, repair.JudgeVersion)
	assert.Contains(t, trigger.Task, "delta explicit-bucket")
	assert.Contains(t, trigger.Task, "same destination aggregation")
	assert.Contains(t, trigger.Task, "go -C sdk/metric test ./internal/aggregate")
	assert.NotContains(t, trigger.Task, "exponential")

	triggerPin, err := parseSinglePin(trigger.LessonYAML)
	require.NoError(t, err)
	repairPin, err := parseSinglePin(repair.LessonYAML)
	require.NoError(t, err)
	assert.Equal(t, []string{otelHistogramTrigger}, triggerPin.AllRegions())
	assert.Equal(t, []string{otelHistogramRepair}, repairPin.AllRegions())
	assert.Equal(t, triggerPin.Note, repairPin.Note)
	assert.Equal(t, strings.Replace(trigger.LessonYAML, otelHistogramTrigger, otelHistogramRepair, 1),
		repair.LessonYAML)
}

func TestOTelHistogramPreparedFixtureContract(t *testing.T) {
	cache, err := otelHistogramRepositorySpec.cacheDir()
	require.NoError(t, err)
	if err := otelHistogramRepositorySpec.validateCache(cache); err != nil {
		t.Skip("public OpenTelemetry fixture is not prepared")
	}

	instance := OTelHistogramInstance()
	base := filepath.Join(t.TempDir(), "base")
	require.NoError(t, instance.Generate(base))
	assert.Equal(t, otelHistogramBaseCommit, fixtureHead(base))
	assert.FileExists(t, filepath.Join(base, filepath.FromSlash(otelHistogramModule), "vendor", "modules.txt"))

	verdict, err := instance.Judge(base)
	require.NoError(t, err)
	assert.False(t, verdict.TaskDone)
	assert.False(t, verdict.Avoided)
	assertChecksPass(t, mustRunChecks(t, base, instance.Checks))

	require.NoError(t, instance.ApplyNaive(base))
	verdict, err = instance.Judge(base)
	require.NoError(t, err)
	assert.True(t, verdict.TaskDone)
	assert.False(t, verdict.Avoided)
	assertChecksPass(t, mustRunChecks(t, base, instance.Checks))

	gold := filepath.Join(t.TempDir(), "gold")
	require.NoError(t, instance.Generate(gold))
	require.NoError(t, instance.ApplyGold(gold))
	verdict, err = instance.Judge(gold)
	require.NoError(t, err)
	assert.True(t, verdict.TaskDone)
	assert.True(t, verdict.Avoided)
	assertChecksPass(t, mustRunChecks(t, gold, instance.Checks))

	status, err := os.ReadFile(filepath.Join(gold, ".git", "info", "exclude"))
	require.NoError(t, err)
	assert.Contains(t, string(status), "/sdk/metric/vendor/")
}

func assertChecksPass(t *testing.T, results []CheckResult) {
	t.Helper()
	for _, result := range results {
		assert.True(t, result.Pass, "%s: %s", result.Command, result.Output)
	}
}
