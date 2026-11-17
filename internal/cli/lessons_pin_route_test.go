package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLessonsFileRouteRuns follows the pin inspection route of the
// lessons --file view. A route that does not run misleads the reader, so
// the test opens each place that the route line names.
func TestLessonsFileRouteRuns(t *testing.T) {
	root, rules := manyPinsRepo(t, 1, "")

	view, err := run(t, "-C", root, "lessons", "--file", "api/handler.go")
	require.NoError(t, err)

	var route string

	for line := range strings.SplitSeq(view, "\n") {
		if strings.HasPrefix(line, "(inspect the pins in ") {
			require.Empty(t, route, "the view names the route once")

			route = line
		}
	}

	require.NotEmpty(t, route, "a view with a pin names the route")

	// The file that the route names exists and holds the pin.
	data, err := os.ReadFile(filepath.Join(root, ".seamark", "lessons.yaml"))
	require.NoError(t, err)
	assert.Contains(t, route, ".seamark/lessons.yaml")
	assert.Contains(t, string(data), rules[0])

	// Each backticked command runs as a known command. Without a terminal
	// the console prints its guidance and writes nothing.
	commands := regexp.MustCompile("`(seamark [^`]+)`").FindAllStringSubmatch(route, -1)
	require.NotEmpty(t, commands)

	for _, m := range commands {
		before := snapshot(t, root)
		args := append([]string{"-C", root}, strings.Fields(strings.TrimPrefix(m[1], "seamark "))...)

		out, err := run(t, args...)
		require.ErrorIs(t, err, errNotInteractive, "the route %q is a known command", m[1])

		assert.Contains(t, out, "Pins that you write live there too")
		assert.Equal(t, before, snapshot(t, root), "the route %q writes nothing", m[1])
	}
}
