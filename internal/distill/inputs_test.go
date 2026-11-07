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

// TestNormalizeRegionMatchesTheRootThroughAnAlias proves the CLI
// orientation: the root is the git top level with its links resolved,
// and the input reaches it through a link. Only the root alias is
// matched. The rest of the input stays lexical, so a missing path still
// compares, and a link below the root is never followed. The test
// builds its own alias, so it also runs on Linux, where no /var link
// exists.
func TestNormalizeRegionMatchesTheRootThroughAnAlias(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(root, alias))

	outsideFile := filepath.Join(t.TempDir(), "hosts")
	require.NoError(t, os.WriteFile(outsideFile, []byte("x\n"), 0o644))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "src", "target.go"), []byte("package src\n"), 0o644))
	require.NoError(t, os.Symlink("target.go", filepath.Join(root, "src", "leaf.go")))
	require.NoError(t, os.Symlink(outsideFile, filepath.Join(root, "src", "ext.go")))
	require.NoError(t, os.Symlink("src", filepath.Join(root, "dirlink")))

	cases := map[string]string{
		filepath.Join(alias, "gone.txt"):                  "gone.txt",
		filepath.Join(alias, "gonedir", "x.txt"):          "gonedir/x.txt",
		alias + "/src/../gonedir/x.txt":                   "gonedir/x.txt",
		filepath.Join(alias, "src", "leaf.go"):            "src/leaf.go",
		filepath.Join(alias, "dirlink", "target.go"):      "dirlink/target.go",
		filepath.Join(alias, "src", "ext.go"):             "src/ext.go",
		filepath.Join(alias, "dirlink", "gone", "new.go"): "dirlink/gone/new.go",
		alias:       "",
		alias + "/": "",
	}

	// assert, not require: one run names every input that fails.
	for input, want := range cases {
		got, err := NormalizeRegion(root, input)
		if assert.NoError(t, err, "input %q", input) {
			assert.Equal(t, want, got, "input %q", input)
		}
	}

	// A link outside the root that leads into the root is not an alias
	// of the root, so its paths stay outside, because a link is never
	// followed. This rule covers a value that names the link. The CLI
	// resolves the links of the process directory before it calls
	// NormalizeRegion, so a relative value from such a directory is
	// inside.
	into := filepath.Join(t.TempDir(), "into")
	require.NoError(t, os.Symlink(filepath.Join(root, "src"), into))

	leafInto := filepath.Join(t.TempDir(), "leaf.go")
	require.NoError(t, os.Symlink(filepath.Join(root, "src", "target.go"), leafInto))

	for _, input := range []string{
		filepath.Join(t.TempDir(), "x.txt"),
		filepath.Dir(alias),
		into,
		filepath.Join(into, "target.go"),
		leafInto,
	} {
		_, err := NormalizeRegion(root, input)
		assert.ErrorIs(t, err, ErrRegionEscapes, "input %q", input)
	}
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
