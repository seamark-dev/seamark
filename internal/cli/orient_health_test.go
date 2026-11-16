package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lineAfter returns the rest of the first output line that holds marker,
// from the end of marker.
func lineAfter(t *testing.T, out, marker string) string {
	t.Helper()

	for line := range strings.SplitSeq(out, "\n") {
		if _, rest, ok := strings.Cut(line, marker); ok {
			return rest
		}
	}

	require.Failf(t, "marker not found", "%q in:\n%s", marker, out)

	return ""
}

// TestOrientHealthAgreesWithStatus proves that orient and status read one
// index in the same way: the same call-edge origin shares and the same
// freshness. It also proves that the CLI orient gives the index an age,
// which needs the real clock.
func TestOrientHealthAgreesWithStatus(t *testing.T) {
	root := writeFixture(t)
	gitify(t, root)

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	orient, err := run(t, "-C", root, "orient")
	require.NoError(t, err)

	status, err := run(t, "-C", root, "status")
	require.NoError(t, err)

	origins := lineAfter(t, orient, "call edges by origin: ")
	assert.Equal(t, lineAfter(t, status, "call resolution "), origins)
	assert.Contains(t, origins, "% ", "the fixture has call edges")

	assert.Regexp(t, `\n  health indexed \d{4}-\d\d-\d\d \d\d:\d\d UTC \(\d+m ago\) · freshness current\n`, orient,
		"the index runs in this test, so its age is a count of minutes")
	assert.True(t, strings.HasPrefix(lineAfter(t, status, "workspace      "), "current "), status)
	assert.Contains(t, orient, "         parsed 1 of 2 seen files · 1 with no language extractor\n",
		"go.mod is seen and has no language extractor")
	assert.Contains(t, status, "parsed         1 of 2 seen files (0 skipped by config, 1 with no language extractor)\n")
}
