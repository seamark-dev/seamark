package distill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
)

func TestNormalizeRegionIsRootRelative(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api", "x"), 0o755))

	cases := map[string]string{
		"":                               "",
		"   ":                            "",
		".":                              "",
		"./":                             "",
		root:                             "",
		root + "/":                       "",
		"api":                            "api",
		"./api":                          "api",
		"api/":                           "api",
		" api/x/ ":                       "api/x",
		"api/../web":                     "web",
		"api//x":                         "api/x",
		filepath.Join(root, "api", "x"):  "api/x",
		filepath.Join(root, "not-there"): "not-there",
	}

	for input, want := range cases {
		got, err := NormalizeRegion(root, input)
		require.NoError(t, err, "input %q", input)
		assert.Equal(t, want, got, "input %q", input)
	}
}

func TestNormalizeRegionRejectsEscapes(t *testing.T) {
	root := t.TempDir()

	for _, input := range []string{"..", "../x", "api/../../x", filepath.Dir(root), "/elsewhere/api"} {
		_, err := NormalizeRegion(root, input)
		require.ErrorIs(t, err, ErrRegionEscapes, "input %q", input)
	}
}

func TestNormalizeRegionAcceptsTheResolvedRoot(t *testing.T) {
	// On macOS a temporary root under /var also reads as /private/var.
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "api"), 0o755))

	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	got, err := NormalizeRegion(root, filepath.Join(resolved, "api"))
	require.NoError(t, err)
	assert.Equal(t, "api", got)

	// A link inside the root that leads outside it is an escape.
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "link")))

	got, err = NormalizeRegion(root, "link")
	require.NoError(t, err, "a relative input is a path in the tree, not a link target")
	assert.Equal(t, "link", got)
}

func TestPinsFromConfigKeepsTheCLIShape(t *testing.T) {
	cfg := &reviews.Config{Pin: []reviews.PinRule{
		{Rule: "one", Note: "First.", Region: "api"},
		{Rule: "two", Note: "Second.", Region: "api", Regions: []string{"web"}},
	}}

	pins := PinsFromConfig(cfg)
	require.Len(t, pins, 2)
	assert.Equal(t, model.Proposal{Rule: "one", Note: "First.", Region: "api", Regions: []string{"api"}}, pins[0])
	assert.Equal(t, cfg.Pin[1].AllRegions(), pins[1].Regions, "both region keys govern")

	assert.Empty(t, PinsFromConfig(reviews.DefaultConfig()))
	assert.NotNil(t, PinsFromConfig(nil))
}

func TestDefaultGroupLimitIsTheCLIDefault(t *testing.T) {
	assert.Equal(t, 10, DefaultGroupLimit)
}
