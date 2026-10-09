package ui

import (
	"go/build"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uiSeamarkImports are the only seamark packages that ui may import. The
// sanitizers of text.go need them.
var uiSeamarkImports = []string{
	"github.com/seamark-dev/seamark/internal/redact",
	"github.com/seamark-dev/seamark/internal/render",
}

// TestUIStaysBelowTheConsole keeps the layering of the console. ui draws
// and tracks positions, so it never imports a screen package or an
// engine. An import of internal/tui would also be a cycle, which Go
// refuses, but an import of internal/tui/services or of an engine
// compiles. No linter rule checks this direction, so this test does. The
// tests of this package follow the same rule.
func TestUIStaysBelowTheConsole(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	imports := append(append(append([]string{}, pkg.Imports...), pkg.TestImports...), pkg.XTestImports...)

	for _, path := range imports {
		assert.NotEqual(t, "github.com/seamark-dev/seamark/internal/tui", path,
			"internal/tui/ui must not import internal/tui; the screens import ui")
		assert.NotEqual(t, "github.com/seamark-dev/seamark/internal/tui/services", path,
			"internal/tui/ui must not import internal/tui/services; a primitive knows no engine")

		if strings.HasPrefix(path, "github.com/seamark-dev/seamark/") {
			assert.Contains(t, uiSeamarkImports, path,
				"internal/tui/ui imports %s; only internal/redact and internal/render are allowed", path)
		}
	}
}
