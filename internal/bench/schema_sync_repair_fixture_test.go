package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepairVariantDiffersOnlyInScoping is the design gate: if the
// variant drifts from the original in anything but the region line,
// the delivery-scoping comparison measures something else.
func TestRepairVariantDiffersOnlyInScoping(t *testing.T) {
	base := SchemaSyncInstance()
	repair := SchemaSyncRepairInstance()

	assert.NotEqual(t, base.ID, repair.ID)
	assert.Equal(t, base.JudgeVersion, repair.JudgeVersion, "the variants share one hidden judge")
	assert.Equal(t, base.Rule, repair.Rule, "one pin identity — only its delivery moves")
	assert.Equal(t, base.Task, repair.Task)
	assert.Equal(t, base.ComparisonFamily, repair.ComparisonFamily)
	assert.Equal(t, base.ProtocolInstance, repair.ProtocolInstance)
	assert.Equal(t, HookExposureRequired, base.effectiveHookExposure())
	assert.Equal(t, HookExposureOptional, repair.effectiveHookExposure())

	// The yaml delta is exactly one region line, both files.
	assert.NotEqual(t, base.LessonYAML, repair.LessonYAML, "the region swap must not silently no-op")
	assert.Contains(t, repair.LessonYAML, "region: "+schemaSyncRepairRegion)
	assert.NotContains(t, repair.LessonYAML, "region: server")
	assert.Equal(t, base.LessonYAML,
		strings.Replace(repair.LessonYAML, "region: "+schemaSyncRepairRegion, "region: server", 1),
		"everything except the region line is byte-identical")
	assert.Equal(t, base.PlaceboYAML,
		strings.Replace(repair.PlaceboYAML, "region: "+schemaSyncRepairRegion, "region: server", 1))

	// The repair region is a real directory in the shared fixture, so
	// the pin is plausible — mis-scoped, not broken.
	dir := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, repair.Generate(dir))

	info, err := os.Stat(filepath.Join(dir, schemaSyncRepairRegion))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestRepairVariantPreflights(t *testing.T) {
	err := Preflight(context.Background(), RunConfig{
		Instance: SchemaSyncRepairInstance(), SeamarkBin: "/opt/seamark/bin/seamark",
		PrepareIndex: false,
	})
	require.NoError(t, err)
}

func TestRepairVariantIsCatalogued(t *testing.T) {
	instance, err := InstanceByID(SchemaSyncRepairInstanceID)
	require.NoError(t, err)
	assert.Equal(t, SchemaSyncRepairInstanceID, instance.ID)
}
