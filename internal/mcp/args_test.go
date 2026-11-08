package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// toolCall is one tools/call request line with a raw arguments value.
func toolCall(id int, tool, args string) string {
	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":{"name":"` + tool +
		`","arguments":` + args + `}}`
}

// tenKeys is a why argument object with the query and ten unknown keys.
func tenKeys() string {
	var b strings.Builder

	b.WriteString(`{"query":"helper"`)

	for i := range 10 {
		fmt.Fprintf(&b, `,"k%d":1`, i)
	}

	b.WriteString("}")

	return b.String()
}

func TestToolsNameIgnoredArguments(t *testing.T) {
	root := writeFixture(t)

	cases := []struct {
		name     string
		tool     string
		baseline string
		args     string
		note     string
	}{
		{name: "orient", tool: "orient", baseline: `{}`, args: `{"task":"x"}`,
			note: `note: seamark ignored the unknown argument "task" and ran the call without it; orient takes no arguments.`},
		{name: "why", tool: "why", baseline: `{"query":"helper"}`, args: `{"query":"helper","callers":true}`,
			note: `note: seamark ignored the unknown argument "callers" and ran the call without it; why takes query.`},
		{name: "change_set", tool: "change_set", baseline: `{"files":["a.go"]}`, args: `{"files":["a.go"],"dry_run":true}`,
			note: `note: seamark ignored the unknown argument "dry_run" and ran the call without it; change_set takes files.`},
		{name: "expand", tool: "expand", baseline: `{"ref":"helper"}`, args: `{"ref":"helper","limit":3}`,
			note: `note: seamark ignored the unknown argument "limit" and ran the call without it; expand takes ref.`},
		{name: "check", tool: "check", baseline: `{}`, args: `{"dry_run":true}`,
			note: `note: seamark ignored the unknown argument "dry_run" and ran the call without it; check takes diff, lesson_checks.`},
		{name: "two keys", tool: "check", baseline: `{}`, args: `{"dry_run":true,"base":"HEAD~1"}`,
			note: `note: seamark ignored the unknown arguments "base", "dry_run" and ran the call without them; check takes diff, lesson_checks.`},
		{name: "folded case is known", tool: "why", baseline: `{"query":"helper"}`, args: `{"QUERY":"helper"}`},
		{name: "empty key", tool: "why", baseline: `{"query":"helper"}`, args: `{"query":"helper","":1}`,
			note: `note: seamark ignored the unknown argument "" and ran the call without it; why takes query.`},
		{name: "ten keys", tool: "why", baseline: `{"query":"helper"}`, args: tenKeys(),
			note: `note: seamark ignored the unknown arguments "k0", "k1", "k2", "k3", "k4", "k5", "k6", "k7" and 2 more ` +
				`and ran the call without them; why takes query.`},
		{name: "control character", tool: "why", baseline: `{"query":"helper"}`, args: `{"query":"helper","a\u0007b":1}`,
			note: `note: seamark ignored the unknown argument "ab" and ran the call without it; why takes query.`},
		{name: "long key", tool: "why", baseline: `{"query":"helper"}`, args: `{"query":"helper","` + strings.Repeat("x", 100) + `":1}`,
			note: `note: seamark ignored the unknown argument "` + strings.Repeat("x", 63) + `…" and ran the call without it; why takes query.`},
		{name: "usage error", tool: "why", baseline: `{}`, args: `{"q":"x"}`,
			note: `note: seamark ignored the unknown argument "q" and ran the call without it; why takes query.`},
		{name: "not an object", tool: "why", baseline: `"helper"`, args: `"helper"`},
		{name: "null", tool: "orient", baseline: `{}`, args: `null`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resps := serve(t, root, initializeLine("2025-06-18"),
				toolCall(1, tc.tool, tc.baseline), toolCall(2, tc.tool, tc.args))

			base := decodeToolParts(t, resps["1"])
			got := decodeToolParts(t, resps["2"])

			assert.Equal(t, base.IsError, got.IsError, "the note never changes isError")
			require.Len(t, got.Content, len(base.Content))

			want := base.Content[0].Text
			if tc.note != "" {
				want = tc.note + "\n" + want
			}

			assert.Equal(t, want, got.Content[0].Text)

			for i := 1; i < len(base.Content); i++ {
				assert.Equal(t, base.Content[i].Text, got.Content[i].Text, "later parts stay")
			}

			if tc.tool == "change_set" {
				assert.Equal(t, got.Content[0].Text, structuredReport(t, got), "the structured report gets the note too")
			}
		})
	}
}

// TestCallNotesPutTheArgumentNoteFirst sends an unknown key after the
// binary went away. The argument note goes before the binary note,
// because it is about this call. The structured report gets the same
// order.
func TestCallNotesPutTheArgumentNoteFirst(t *testing.T) {
	root := writeFixture(t)
	bin := filepath.Join(t.TempDir(), "seamark")
	writeBinary(t, bin, "old", time.Now().Add(-time.Hour))
	watchExecutable(t, bin, nil)

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)
	callServer(t, srv, initializeLine("2025-06-18"))

	require.NoError(t, os.Remove(bin))

	got := decodeToolParts(t, callServer(t, srv, toolCall(1, "change_set", `{"files":["a.go"],"zz":1}`))["1"])

	notes := `note: seamark ignored the unknown argument "zz" and ran the call without it; change_set takes files.` +
		"\n" + removedNote(bin)

	assert.True(t, strings.HasPrefix(got.Content[0].Text, notes), "text: %s", got.Content[0].Text)
	assert.True(t, strings.HasPrefix(structuredReport(t, got), notes), "report: %s", structuredReport(t, got))
}

// TestUnknownArgumentNamesNeverReachTheLog keeps client key names out of
// the server log, because a key can hold private text.
func TestUnknownArgumentNamesNeverReachTheLog(t *testing.T) {
	root := writeFixture(t)

	var log strings.Builder

	serveDB(t, root, root+"/.seamark/index.db", func(format string, a ...any) {
		fmt.Fprintf(&log, format+"\n", a...)
	}, toolCall(1, "why", `{"query":"helper","SENTINELkey":1}`))

	assert.NotContains(t, log.String(), "SENTINELkey")
}

// TestUnknownArgumentNoteNamesSchemaProperties ties the known keys to
// the schemas, so a new property needs no second list.
func TestUnknownArgumentNoteNamesSchemaProperties(t *testing.T) {
	for _, def := range toolDefs {
		name, _ := def["name"].(string)
		schema, _ := def["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)

		args := map[string]any{}
		for key := range props {
			args[key] = nil
		}

		data, err := json.Marshal(args)
		require.NoError(t, err)
		assert.Empty(t, unknownArgsNote(name, data), "%s knows every schema property", name)
	}
}
