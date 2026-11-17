package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The mcp help names each resource that the server lists, by URI and by
// role. The server test in package mcp pins the exact list, so a new
// resource fails there until this help and this test name it.
func TestMCPHelpNamesBothResources(t *testing.T) {
	// The help wraps its lines, so the test joins the words first. Then a
	// role that wraps onto the next line still matches.
	long := strings.Join(strings.Fields(newMCPCmd(&options{}).Long), " ")

	// Each role repeats the resource name in the server list. A swapped
	// or dropped role fails here.
	assert.Contains(t, long, "two resources")
	assert.Contains(t, long, "seamark://orient for repository orientation")
	assert.Contains(t, long, "seamark://status for index health")
	assert.NotContains(t, long, "an orientation resource")
}
