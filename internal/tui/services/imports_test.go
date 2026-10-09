package services

import (
	"go/build"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// servicesBannedImports are the packages that services must not import.
// The adapter serves the screens, so it must not know them: a screen
// rule would leak into an engine call, and an import of internal/tui
// would also be a cycle.
var servicesBannedImports = []string{
	"github.com/charmbracelet/bubbletea",
	"github.com/charmbracelet/lipgloss",
	"github.com/seamark-dev/seamark/internal/tui",
	"github.com/seamark-dev/seamark/internal/tui/ui",
}

// servicesBannedPrefix bans every Bubbles package, because a widget
// package is a screen concern.
const servicesBannedPrefix = "github.com/charmbracelet/bubbles"

// TestServicesStayBelowTheConsole keeps the layering of the console. No
// linter rule checks this direction, so this test does. The tests of
// this package follow the same rule.
func TestServicesStayBelowTheConsole(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	imports := append(append(append([]string{}, pkg.Imports...), pkg.TestImports...), pkg.XTestImports...)

	for _, path := range imports {
		assert.NotContains(t, servicesBannedImports, path,
			"internal/tui/services must not import %s; the screens import services", path)
		assert.False(t, strings.HasPrefix(path, servicesBannedPrefix),
			"internal/tui/services must not import %s; a widget is a screen concern", path)
	}
}

// TestServicestestStaysOutOfTheBinary keeps the test fixtures out of
// production code. servicestest builds workspaces and fakes for tests;
// a production import would ship them in the seamark binary.
func TestServicestestStaysOutOfTheBinary(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "github.com/seamark-dev/seamark/cmd/seamark")
	cmd.Dir = "../../.."

	out, err := cmd.Output()
	require.NoError(t, err, "go list -deps of the seamark binary")

	deps := strings.Fields(string(out))
	require.Contains(t, deps, "github.com/seamark-dev/seamark/internal/tui/services",
		"the binary links the console adapter, so the list is complete")
	assert.NotContains(t, deps, "github.com/seamark-dev/seamark/internal/tui/services/servicestest",
		"production code must not import servicestest; only tests use it")
}
