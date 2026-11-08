package integration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// This file holds the generic setup test helpers that the lesson, Claude,
// and Codex setup tests share.

// The hook fixtures of the tests name these second binaries beside
// testBinary, which a test machine does not need to hold. The lifecycle
// hook inspection reads them as hooks that can start.
func init() {
	for _, binary := range []string{"/home/me/bin/seamark", "/opt/bin/seamark"} {
		TreatHookBinaryAsPresent(binary)
	}
}

// applyHooks installs the lesson and gate hooks of one client, as init
// does.
func applyHooks(t *testing.T, root, clientID string) {
	t.Helper()

	_, err := ApplySetup(mustPlan(t, Builtin(), SetupRequest{
		Root: root, Binary: testBinary, Clients: []ClientSetup{{ClientID: clientID, Hooks: true}},
	}), ApplyOptions{})
	require.NoError(t, err)
}

// parseRel parses one repository-relative JSON document.
func parseRel(t *testing.T, root, rel string) map[string]any {
	t.Helper()

	var doc map[string]any

	require.NoError(t, json.Unmarshal([]byte(readRel(t, root, rel)), &doc))

	return doc
}

// readPaths lists the paths of the guarded reads of a plan.
func readPaths(reads []FileGuard) []string {
	var out []string

	for _, g := range reads {
		out = append(out, g.Path)
	}

	return out
}
