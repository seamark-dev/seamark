package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWhyShowsCallsOfAPackageLevelVar proves that why on a package-level
// var lists the calls in its function literals and the effects they
// reach, and that why on the callee lists the var as a caller.
func TestWhyShowsCallsOfAPackageLevelVar(t *testing.T) {
	root := writeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "runners.go"), []byte(`package main

import "os"

var runners = map[string]func(string) error{
	"audit": func(p string) error { return audit(p) },
}

func audit(p string) error {
	return os.WriteFile(p, nil, 0o600)
}
`), 0o644))

	_, err := run(t, "-C", root, "index")
	require.NoError(t, err)

	out, err := run(t, "-C", root, "why", "runners")
	require.NoError(t, err)
	assert.Contains(t, out, "runners  (var)\n")
	assert.Contains(t, out, "  effects  fs:write [depth 1]\n")
	assert.Contains(t, out, fmt.Sprintf("\ncalls (1)\n  [same-package]  %-44s %s\n", "audit", "runners.go:9"))

	out, err = run(t, "-C", root, "why", "audit")
	require.NoError(t, err)
	assert.Contains(t, out, fmt.Sprintf("\ncallers (1)  — can be incomplete: a call through a function value or a shared method name gets no edge\n  [same-package]  %-44s %s\n", "runners", "runners.go:5"))
}
