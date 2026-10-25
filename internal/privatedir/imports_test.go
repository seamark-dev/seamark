package privatedir

import (
	"go/build"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivateDirImportsOnlyStandardLibrary keeps this package free of
// module imports. The archive and the lesson checks both import it, so
// any internal import can make an import cycle. The test reads every
// file, also the files of other platforms, because perm_other.go has
// its own imports.
func TestPrivateDirImportsOnlyStandardLibrary(t *testing.T) {
	ctx := build.Default
	ctx.UseAllFiles = true

	pkg, err := ctx.ImportDir(".", 0)
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Imports, "the import list must not be empty, or the test checks nothing")

	for _, path := range pkg.Imports {
		// The go tool uses this rule too: a standard library path has
		// no dot in its first element, and a module path has one.
		first, _, _ := strings.Cut(path, "/")
		assert.NotContains(t, first, ".", "privatedir imports %s, which is outside the standard library", path)
	}
}
