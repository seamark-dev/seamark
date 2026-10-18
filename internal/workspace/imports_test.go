package workspace

import (
	"go/build"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkspaceStaysBelowIndex keeps the index package out of the
// production imports of this package. The import would pull the cgo
// parsers into every reader of a workspace, such as the console's
// maintenance engine. It would also stop index from importing
// workspace, because Go refuses an import cycle. No linter rule checks
// this direction, so this test does. Use history.ResolveRoot for the
// root rule. The tests of this package may still import index.
func TestWorkspaceStaysBelowIndex(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	assert.NotContains(t, pkg.Imports, "github.com/seamark-dev/seamark/internal/index",
		"internal/workspace must not import internal/index; call history.ResolveRoot instead")
}
