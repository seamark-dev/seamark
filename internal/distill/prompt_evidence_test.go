package distill

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func TestPromptBodyCapSharesOneEvidenceBudget(t *testing.T) {
	assert.Equal(t, promptBodyMax, promptBodyCapFor(2))
	assert.Equal(t, promptBodyMax, promptBodyCapFor(20))
	assert.Equal(t, 2_000, promptBodyCapFor(30))
	assert.Equal(t, promptBodyMin, promptBodyCapFor(40))
	assert.Equal(t, promptBodyMin, promptBodyCapFor(100),
		"oversized input stays bounded even though grouping normally caps it first")
}

func TestTruncatePromptTextHonorsByteCapAndUTF8(t *testing.T) {
	got := truncatePromptText(strings.Repeat("é", 20), 17)
	assert.LessOrEqual(t, len(got), 17)
	assert.True(t, utf8.ValidString(got))

	tiny := truncatePromptText(strings.Repeat("é", 20), 5)
	assert.LessOrEqual(t, len(tiny), 5)
	assert.True(t, utf8.ValidString(tiny))
	assert.Empty(t, truncatePromptText("must not leak", 0))
}

func TestPromptFindingEvidenceSharesPathsAndBodyBudget(t *testing.T) {
	var footprint []string
	for i := range 20 {
		footprint = append(footprint, fmt.Sprintf("generated/client-%02d-with-a-long-name.ts", i))
	}

	f := model.Finding{
		Path: "api/handler.go", Paths: footprint, Body: strings.Repeat("body ", 500),
	}

	paths, body := promptFindingEvidence(f, 180)
	encoded, err := json.Marshal(paths)
	require.NoError(t, err)

	require.NotEmpty(t, paths)
	assert.Equal(t, f.Path, paths[0], "the primary path is never displaced by footprint entries")
	assert.LessOrEqual(t, len(paths), promptPathItemsPerFact)
	assert.LessOrEqual(t, len(encoded)+len(body), 180,
		"serialized path metadata and body share one bounded slice")

	docPrimary, _ := promptFindingEvidence(model.Finding{
		Path: "docs/contract.md", Paths: []string{"api/handler.go"}, Body: "body",
	}, 180)
	assert.Equal(t, "docs/contract.md", docPrimary[0],
		"the finding's primary identity survives even when auxiliary doc paths are filtered")
}

func TestPromptFindingEvidenceRedactsLegacySecretsAtDispatch(t *testing.T) {
	_, body := promptFindingEvidence(model.Finding{
		Path: "api/handler.go",
		Body: "API_TOKEN=legacy-secret-value keep this diagnostic context",
	}, 500)

	assert.Contains(t, body, "API_TOKEN=[REDACTED]")
	assert.NotContains(t, body, "legacy-secret-value")
}

func TestCompactFixEvidencePrioritizesProductionHunksAcrossLanguages(t *testing.T) {
	body := `fix commit 12345678
subject: keep generated clients and cache adapters synchronized
functions: This project follows Semantic Versioning, function regenerateClient(schema), public Result rebuildCache(Key key), def refresh_adapter(key)
patch:
@@ -1,2 +1,3 @@ func TestGeneratedOutput
+ repetitive test setup
@@ -4,2 +4,3 @@ changelog
+ release note
@@ -8,2 +8,3 @@ function regenerateClient(schema)
+ regenerate the TypeScript client
@@ -12,2 +12,3 @@ public Result rebuildCache(Key key)
+ invalidate the Java cache adapter
@@ -16,2 +16,3 @@ def refresh_adapter(key)
+ refresh the Python adapter`

	got := compactFixEvidence(body, 2_000)
	patchAt := strings.Index(got, "patch:")
	require.NotEqual(t, -1, patchAt)
	patch := got[patchAt:]
	testAt := strings.Index(patch, "TestGeneratedOutput")

	require.Positive(t, testAt)
	assert.Less(t, strings.Index(patch, "regenerateClient"), testAt)
	assert.Less(t, strings.Index(patch, "rebuildCache"), testAt)
	assert.Less(t, strings.Index(patch, "refresh_adapter"), testAt)
	assert.NotContains(t, got, "Semantic Versioning",
		"generic envelope prose must not survive as a function name")
}

func TestCompactFixEvidenceUsesCanonicalPatchMarkerAndHandlesHeaderOnlyPatch(t *testing.T) {
	body := "subject: explain a patch:\npatch:\nin prose\nfunctions: func rebuild()\npatch:\n" +
		"@@ -1 +1 @@ func rebuild()\n+production change"
	got := compactFixEvidence(body, 1_000)
	assert.Contains(t, got, "in prose", "a message line named patch is not the canonical delimiter")
	assert.Contains(t, got, "production change")

	headerOnly := compactFixEvidence("subject: no hunks\npatch:\nraw diff without hunk headers", 1_000)
	assert.Equal(t, "subject: no hunks", headerOnly)
}

func TestFixHunkRankUsesTestTokensNotSubstrings(t *testing.T) {
	assert.Equal(t, 0, fixHunkRank("@@ -1 +1 @@ func latestBuffer()"))
	assert.Equal(t, 0, fixHunkRank("@@ -1 +1 @@ func verifyAttestation()"))
	assert.Equal(t, 2, fixHunkRank("@@ -1 +1 @@ func TestLatestBuffer()"))
	assert.Equal(t, 2, fixHunkRank("@@ -1 +1 @@ def test_latest_buffer()"))
	assert.Equal(t, 2, fixHunkRank("file: api/handler_test.go\n@@ -1 +1 @@ func helper()"),
		"a structurally identified test file wins over a generic helper name")
	assert.Equal(t, 0, fixHunkRank("file: api/handler.go\n@@ -1 +1 @@ func latestBuffer()"))
}

func TestCompactFixHeaderDropsTrailersCaseInsensitively(t *testing.T) {
	got := compactFixHeader("subject: fix\nCo-Authored-By: A <a@example.com>\nSIGNED-OFF-BY: B <b@example.com>")
	assert.Equal(t, "subject: fix", got)
}

func TestPromptDisclosesFullFixFootprintAndSamplesParallelProductionHunks(t *testing.T) {
	f := model.Finding{
		ID: 728759585664303326, PR: 8403,
		Path: "sdk/metric/internal/aggregate/exponential_histogram.go",
		Paths: []string{
			"sdk/metric/internal/aggregate/exponential_histogram.go",
			"sdk/metric/internal/aggregate/exponential_histogram_test.go",
			"sdk/metric/internal/aggregate/histogram.go",
			"CHANGELOG.md",
		},
		Source: model.SourceFixConventional,
		Body: `fix commit 0a1d12fb
subject: fix: clear stale histogram fields on datapoint reuse (#8403)
Fixes #8399
functions: This project adheres to Semantic Versioning, func (e *expoHistogram[N]) delta(, func (e *expoHistogram[N]) cumulative(, func (d *deltaHistogram[N]) collect(
patch:
@@ -1,2 +1,3 @@ func (e *expoHistogram[N]) delta(
+ clear Sum, Min, and Max for a reused exponential delta datapoint
@@ -4,2 +4,3 @@ func (e *expoHistogram[N]) cumulative(
+ clear Sum, Min, and Max for a reused exponential cumulative datapoint
@@ -8,2 +8,3 @@ func (d *deltaHistogram[N]) collect(
+ clear Sum, Min, and Max for a reused explicit delta datapoint
@@ -12,2 +12,3 @@ func TestHistogramReuse
` + strings.Repeat("+ repetitive test table data\n", 150) + `
@@ -16,2 +16,3 @@ changelog
+ describe the release note`,
	}

	prompt := buildPrompt(makeGroup([]model.Finding{f, {
		ID: 5121900077751038754, PR: 8428,
		Path:   "sdk/metric/internal/aggregate/histogram.go",
		Body:   "A second independent histogram buffer-reuse fix.",
		Source: model.SourceFixIssueLink,
	}}), nil)

	assert.Contains(t, prompt, `"sdk/metric/internal/aggregate/exponential_histogram.go"`)
	assert.Contains(t, prompt, `"sdk/metric/internal/aggregate/exponential_histogram_test.go"`)
	assert.Contains(t, prompt, `"sdk/metric/internal/aggregate/histogram.go"`)
	assert.NotContains(t, prompt, `"CHANGELOG.md"`, "documentation does not define delivery scope")
	assert.Contains(t, prompt, "reused exponential delta datapoint")
	assert.Contains(t, prompt, "reused exponential cumulative datapoint")
	assert.Contains(t, prompt, "reused explicit delta datapoint")
	assert.NotContains(t, prompt, "This project adheres to Semantic Versioning",
		"generic function-envelope noise should not consume the evidence budget")
	assert.Contains(t, prompt, "parallel implementations")
	assert.Contains(t, prompt, `"trigger_paths": REQUIRED`)
	assert.Contains(t, prompt, `"trigger_paths": ["path/to/entry.go"]`)
	assert.LessOrEqual(t, len(promptFindingBody(f, promptBodyMax)), promptBodyMax)

	paths := relevantFindingPaths(f)
	require.Len(t, paths, 3)
	assert.Equal(t, f.Path, paths[0], "the semantic home remains first")
}
