package mcp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWhyOutlineRouteRunsThroughExpand(t *testing.T) {
	root := writeFixture(t)

	var src strings.Builder

	src.WriteString("package main\n\n")

	for i := 1; i <= 21; i++ {
		fmt.Fprintf(&src, "func f%d() {\n}\n\n", i)
	}

	require.NoError(t, os.WriteFile(filepath.Join(root, "many.go"), []byte(src.String()), 0o644))

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"many.go"}}}`,
	)

	why, isErr := toolText(t, resps["1"])
	require.False(t, isErr)

	_, route, found := strings.Cut(why, "source of the next one (f21): expand ")
	require.True(t, found, "the why tool prints the route:\n%s", why)

	ref, _, found := strings.Cut(route, " (MCP)\n")
	require.True(t, found)

	resps = serve(t, root,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"expand","arguments":{"ref":%q}}}`, ref),
	)

	expand, isErr := toolText(t, resps["1"])
	require.False(t, isErr, expand)

	// The first line is the header; the source lines follow it.
	lines := strings.Split(strings.TrimSuffix(expand, "\n"), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	assert.Contains(t, lines[1], "func f21(")
	assert.True(t, strings.HasSuffix(lines[len(lines)-1], "}"), "the last line closes the function: %q", lines[len(lines)-1])
}
