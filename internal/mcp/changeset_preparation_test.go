package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/store"
)

// The tests of the change_set preparation part. Preparation adds a
// second content part and structuredContent to change_set. The first
// content part must keep its earlier text byte for byte.

// prepFixtureLessons pins two lessons on internal/parse/ under a
// change budget of one. So the lesson block shows one pin and the
// "+1 more" line, and the goldens hold every line kind of the block.
const prepFixtureLessons = `change_budget: 1
pin:
  - rule: keep-parser-strict
    region: internal/parse/
    note: Reject unknown fields in the parser.
  - rule: parse-errors-name-the-field
    region: internal/parse/
    note: Name the field in every parse error.
`

// prepFixtureNoPins is a lessons configuration without pins. The
// change_set text then holds no lesson block.
const prepFixtureNoPins = "change_budget: 1\n"

// changeSetPrepFixture builds a repository whose change_set text holds
// every part that the preparation must keep: the not-in-index line, the
// co-change lines, the defines, caller, and effect lines, a companion
// with its functions and its last fix, and the pinned lesson block.
// Fixed identities and dates keep every commit ID stable, because the
// companion reason prints the short ID of the last fix.
func changeSetPrepFixture(t *testing.T) string {
	t.Helper()

	// The server runs git itself. A global or system git configuration
	// must not change the history that the index reads.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := t.TempDir()

	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	commit := func(subject string) {
		git("add", "-A")
		git("commit", "-q", "-m", subject)
	}

	save := func(comment string) string {
		return "package main\n\nimport \"os\"\n\n// save writes the payload.\n" +
			"func save(path string, data []byte) error {\n" + comment +
			"\treturn os.WriteFile(path, data, 0o600)\n}\n"
	}

	load := func(comment string) string {
		return "package main\n\nimport \"os\"\n\n// load reads the payload.\n" +
			"func load(path string) ([]byte, error) {\n" + comment +
			"\treturn os.ReadFile(path)\n}\n"
	}

	git("init", "-q", "-b", "main")

	fixtureWrite(t, root, "go.mod", "module example.com/prep\n")
	fixtureWrite(t, root, "main.go", "package main\n\nfunc main() {\n\t_ = save(\"out\", nil)\n}\n")
	fixtureWrite(t, root, "store.go", save(""))
	fixtureWrite(t, root, "load.go", load(""))
	fixtureWrite(t, root, "internal/parse/strict.go",
		"package parse\n\n// Strict rejects unknown fields.\nfunc Strict(fields []string) error { return nil }\n")
	fixtureWrite(t, root, "docs/notes.md", "notes\n")
	fixtureWrite(t, root, ".seamark/lessons.yaml", prepFixtureLessons)
	commit("Add the store and the parser")

	// save and load change together in three commits, and the notes
	// change alone in two. So load.go is a companion of store.go with a
	// lift above one. The fix subject gives the companion a last fix.
	fixtureWrite(t, root, "store.go", save("\t// Callers own the path.\n"))
	fixtureWrite(t, root, "load.go", load("\t// Callers own the path.\n"))
	commit("Document path ownership")

	fixtureWrite(t, root, "store.go", save("\t// The path is trusted.\n"))
	fixtureWrite(t, root, "load.go", load("\t// The path is trusted.\n"))
	commit("fix: State the path trust in load and save")

	fixtureWrite(t, root, "docs/notes.md", "notes\nmore\n")
	commit("Extend the notes")

	fixtureWrite(t, root, "docs/notes.md", "notes\nmore\nlast\n")
	commit("Finish the notes")

	return root
}

// prepIndexedText is content[0] of change_set for store.go in the
// fixture of changeSetPrepFixture under the pinned configuration.
const prepIndexedText = `store.go
  usually changes with  load.go                                         3/5 commits, lift 1.7
  defines                1 symbols
  called from outside    save (1) — callers in other non-test files
  effects reachable      fs:write

history suggests also reviewing
  load.go                                            3 shared commits with store.go, lift 1.7
    mostly load · last fix here: fix: State the path trust in load and save (f564665)
`

// toolParts is a tools/call result with every content part. toolText
// requires exactly one part, and change_set has a second part. So the
// preparation tests decode the parts themselves.
type toolParts struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	IsError           bool            `json:"isError"`
}

// decodeToolParts decodes the tool result of r and requires at least
// one content part.
func decodeToolParts(t *testing.T, r rpcResp) toolParts {
	t.Helper()

	require.Nil(t, r.Error, "expected a tool result, got a protocol error")

	var res toolParts
	require.NoError(t, json.Unmarshal(r.Result, &res))
	require.NotEmpty(t, res.Content)

	return res
}

// changeSetCall is one change_set request line with the given id. The
// files are JSON-encoded, because an absolute path can hold any byte.
func changeSetCall(t *testing.T, id int, files ...string) string {
	t.Helper()

	params, err := json.Marshal(map[string]any{
		"name":      "change_set",
		"arguments": map[string]any{"files": files},
	})
	require.NoError(t, err)

	return `{"jsonrpc":"2.0","id":` + strconv.Itoa(id) + `,"method":"tools/call","params":` + string(params) + `}`
}

// prepProtocolBranches are the three ways a session starts. change_set
// adds structuredContent only for 2025-06-18 or later, and the first
// content part must not depend on that choice. A session without
// initialize negotiates no version at all.
var prepProtocolBranches = []struct {
	name       string
	initialize string
}{
	{name: "protocol 2025-03-26",
		initialize: `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`},
	{name: "protocol 2025-06-18",
		initialize: `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test"}}}`},
	{name: "no initialize"},
}

// prepTextCase is one change_set request and the text that content[0]
// must hold for it.
type prepTextCase struct {
	name  string
	files []string
	want  string
}

// serveChangeSetCases runs every case through one server per protocol
// branch. It asserts that the result is no error and that content[0]
// is a text part equal to the golden.
func serveChangeSetCases(t *testing.T, root string, cases []prepTextCase) {
	t.Helper()

	for _, branch := range prepProtocolBranches {
		t.Run(branch.name, func(t *testing.T) {
			var requests []string
			if branch.initialize != "" {
				requests = append(requests, branch.initialize)
			}

			for i, c := range cases {
				requests = append(requests, changeSetCall(t, i+1, c.files...))
			}

			resps := serve(t, root, requests...)

			for i, c := range cases {
				res := decodeToolParts(t, resps[strconv.Itoa(i+1)])
				assert.False(t, res.IsError, c.name)
				assert.Equal(t, "text", res.Content[0].Type, c.name)
				assert.Equal(t, c.want, res.Content[0].Text, c.name)
			}
		})
	}
}

// TestChangeSetPreparationKeepsText freezes content[0] of the MCP
// change_set tool as it was before the preparation part existed. The
// cases are an indexed file, a new path, a pinned region, a new path in
// a pinned region, the "./" and absolute spellings, and a configuration
// without pins. Each case runs under the three protocol branches.
// content[1] and structuredContent came later, and they must not change
// these goldens.
func TestChangeSetPreparationKeepsText(t *testing.T) {
	// The goldens hold the text from before the preparation part. The
	// pinned text holds two blank lines before the lesson block, because
	// the file block ends with one and the lesson block starts with one.
	// The bytes stay as they are.
	const (
		indexedText = prepIndexedText
		newPathText = "docs/new.md: not in the index (new file, or run `seamark index`)\n\n"
		pinnedText  = `internal/parse/strict.go
  defines                1 symbols


lessons for this change  (regions map to the files above; avoid repeating these)
- [pin · internal/parse] keep-parser-strict — Reject unknown fields in the parser.
(+1 more: ` + "`seamark lessons --file <path>`" + ` shows a file's full view)
`
		newPinnedText = "internal/parse/fresh.go: not in the index (new file, or run `seamark index`)\n\n" + `
lessons for this change  (regions map to the files above; avoid repeating these)
- [pin · internal/parse] keep-parser-strict — Reject unknown fields in the parser.
(+1 more: ` + "`seamark lessons --file <path>`" + ` shows a file's full view)
`
		mixedText = "internal/parse/fresh.go: not in the index (new file, or run `seamark index`)\n\n" +
			"docs/new.md: not in the index (new file, or run `seamark index`)\n\n" + `store.go
  usually changes with  load.go                                         3/5 commits, lift 1.7
  defines                1 symbols
  called from outside    save (1) — callers in other non-test files
  effects reachable      fs:write

history suggests also reviewing
  load.go                                            3 shared commits with store.go, lift 1.7
    mostly load · last fix here: fix: State the path trust in load and save (f564665)

lessons for this change  (regions map to the files above; avoid repeating these)
- [pin · internal/parse] keep-parser-strict — Reject unknown fields in the parser.
(+1 more: ` + "`seamark lessons --file <path>`" + ` shows a file's full view)
`

		pinnedNoPinsText    = "internal/parse/strict.go\n  defines                1 symbols\n\n"
		newPinnedNoPinsText = "internal/parse/fresh.go: not in the index (new file, or run `seamark index`)\n\n"
	)

	root := changeSetPrepFixture(t)

	// The absolute spelling joins the root string that the server gets,
	// so a symbolic link in the temporary directory resolves alike.
	abs := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	t.Run("pinned configuration", func(t *testing.T) {
		serveChangeSetCases(t, root, []prepTextCase{
			{name: "indexed file", files: []string{"store.go"}, want: indexedText},
			{name: "new path", files: []string{"docs/new.md"}, want: newPathText},
			{name: "pinned region", files: []string{"internal/parse/strict.go"}, want: pinnedText},
			{name: "new path in a pinned region", files: []string{"internal/parse/fresh.go"}, want: newPinnedText},
			{name: "./ spelling", files: []string{"./internal/parse/strict.go"}, want: pinnedText},
			{name: "absolute spelling", files: []string{abs("internal/parse/strict.go")}, want: pinnedText},
			{name: "./ spelling of a new path", files: []string{"./internal/parse/fresh.go"}, want: newPinnedText},
			{name: "absolute spelling of a new path", files: []string{abs("internal/parse/fresh.go")}, want: newPinnedText},
			{name: "several files in mixed spellings",
				files: []string{"./internal/parse/fresh.go", abs("store.go"), "docs/new.md"}, want: mixedText},
		})
	})

	// The same paths under a configuration without pins give no lesson
	// block. The rewrite changes only the working tree, which is the
	// file that change_set reads.
	fixtureWrite(t, root, ".seamark/lessons.yaml", prepFixtureNoPins)

	t.Run("configuration without pins", func(t *testing.T) {
		serveChangeSetCases(t, root, []prepTextCase{
			{name: "pinned region", files: []string{"internal/parse/strict.go"}, want: pinnedNoPinsText},
			{name: "new path in a pinned region", files: []string{"internal/parse/fresh.go"}, want: newPinnedNoPinsText},
		})
	})
}

// initializeLine is an initialize request with the given protocol
// version and id 0.
func initializeLine(version string) string {
	return `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"` + version +
		`","capabilities":{},"clientInfo":{"name":"test"}}}`
}

// structuredKeys decodes the structuredContent of a change_set result.
// It requires exactly the keys lesson_preparation and change_set_report.
func structuredKeys(t *testing.T, res toolParts) map[string]json.RawMessage {
	t.Helper()

	require.NotEmpty(t, res.StructuredContent, "the result holds structuredContent")

	var structured map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(res.StructuredContent, &structured))
	require.Len(t, structured, 2, "structuredContent holds two keys")
	require.Contains(t, structured, "lesson_preparation")
	require.Contains(t, structured, "change_set_report")

	return structured
}

// structuredPreparation decodes the lesson_preparation key of a
// change_set result.
func structuredPreparation(t *testing.T, res toolParts) delivery.Preparation {
	t.Helper()

	var p delivery.Preparation
	require.NoError(t, json.Unmarshal(structuredKeys(t, res)["lesson_preparation"], &p))

	return p
}

// structuredReport decodes the change_set_report key of a change_set
// result.
func structuredReport(t *testing.T, res toolParts) string {
	t.Helper()

	var text string
	require.NoError(t, json.Unmarshal(structuredKeys(t, res)["change_set_report"], &text))

	return text
}

// supplementOf renders p in the supplement form, which is the second
// content part of change_set.
func supplementOf(p delivery.Preparation) string {
	var b strings.Builder
	delivery.RenderPreparation(&b, &p, delivery.FormSupplement)

	return b.String()
}

// prepFirings returns every firing record under root.
func prepFirings(t *testing.T, root string) []reviews.Firing {
	t.Helper()

	firings, err := reviews.ReadFirings(root)
	require.NoError(t, err)

	return firings
}

// prepFixtureChecks binds a check to an enforce pin on docs/. The
// change_set text then holds a pin, and the preparation lists the check.
const prepFixtureChecks = `lesson_checks_version: 1
checks:
  frozen-docs:
    kind: forbid-path-change/1
pin:
  - rule: frozen-docs
    region: docs/
    note: Change the docs only in a release.
    mode: enforce
    check: frozen-docs
`

// TestChangeSetPreparationAddsSupplementAndStructuredContent proves the
// second content part and structuredContent of change_set. The second
// part is the supplement form of the same preparation and never repeats
// a pin note. structuredContent follows the negotiated protocol version
// only. No tool declares an outputSchema.
func TestChangeSetPreparationAddsSupplementAndStructuredContent(t *testing.T) {
	root := changeSetPrepFixture(t)

	t.Run("the supplement renders the structured preparation", func(t *testing.T) {
		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"))

		res := decodeToolParts(t, resps["1"])
		assert.False(t, res.IsError)
		require.Len(t, res.Content, 2)
		assert.Equal(t, "text", res.Content[1].Type)

		p := structuredPreparation(t, res)
		assert.Equal(t, delivery.PreparationSchema, p.Schema)
		assert.Equal(t, "test", p.SeamarkVersion)
		assert.Equal(t, delivery.SurfaceChangeSet, p.Surface)
		assert.Equal(t, delivery.Receiver{Lane: "unknown", Reason: "no-receiver-identity"}, p.Receiver)
		assert.Equal(t, []string{"internal/parse/strict.go"}, p.Request.Requested)
		require.Len(t, p.Request.Paths, 1)
		assert.Equal(t, "internal/parse/strict.go", p.Request.Paths[0].Path)
		assert.Equal(t, "ok", p.Guidance.Status)
		assert.Equal(t, delivery.MinedOK, p.Guidance.MinedStatus)
		assert.Equal(t, 1, p.Guidance.Budget)

		// The change budget of one selects the first pin and leaves the
		// second out, as the "+1 more" line of the text says.
		require.Len(t, p.Pins, 1)
		assert.Equal(t, "keep-parser-strict", p.Pins[0].Rule)
		require.Len(t, p.Omitted, 1)
		assert.Equal(t, "parse-errors-name-the-field", p.Omitted[0].Rule)
		assert.Equal(t, reviews.CauseOverTotal, p.Omitted[0].Reason)
		assert.NotEmpty(t, p.EventID)

		assert.Equal(t, supplementOf(p), res.Content[1].Text, "content[1] is the supplement form of the same preparation")
		assert.Contains(t, res.Content[1].Text, "lesson preparation (lesson-preparation/1) for 1 path(s) — receiver unknown\n")
		assert.Contains(t, res.Content[1].Text, p.Omitted[0].Inspect)
		assert.True(t, strings.HasSuffix(res.Content[1].Text, delivery.PreparationNotice+"\n"))

		// content[0] already shows the pin text, so the supplement names
		// the rule and never repeats the note.
		assert.Contains(t, res.Content[0].Text, "Reject unknown fields in the parser.")
		assert.NotContains(t, res.Content[1].Text, "Reject unknown fields in the parser.")
	})

	t.Run("structuredContent follows the negotiated protocol", func(t *testing.T) {
		for _, c := range []struct {
			name       string
			initialize string
			structured bool
		}{
			{name: "2025-03-26", initialize: initializeLine("2025-03-26")},
			{name: "2025-06-18", initialize: initializeLine("2025-06-18"), structured: true},
			{name: "a later version", initialize: initializeLine("2025-11-25"), structured: true},
			{name: "no version in initialize", structured: true,
				initialize: `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"capabilities":{}}}`},
			{name: "no initialize"},
		} {
			t.Run(c.name, func(t *testing.T) {
				var requests []string
				if c.initialize != "" {
					requests = append(requests, c.initialize)
				}

				resps := serve(t, root, append(requests, changeSetCall(t, 1, "internal/parse/strict.go"))...)

				res := decodeToolParts(t, resps["1"])
				assert.False(t, res.IsError)

				// The supplement does not depend on the protocol.
				require.Len(t, res.Content, 2)
				assert.Contains(t, res.Content[1].Text, "lesson preparation (lesson-preparation/1)")

				if !c.structured {
					assert.Empty(t, res.StructuredContent, "an older client gets no structuredContent")

					return
				}

				p := structuredPreparation(t, res)
				assert.Equal(t, supplementOf(p), res.Content[1].Text)
			})
		}
	})

	t.Run("a path without pins gets structuredContent and no supplement", func(t *testing.T) {
		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "store.go"))

		res := decodeToolParts(t, resps["1"])
		require.Len(t, res.Content, 1, "the preparation adds nothing to the text")

		p := structuredPreparation(t, res)
		assert.Empty(t, p.Pins)
		assert.Empty(t, p.Checks)
		assert.Empty(t, p.Omitted)
		assert.Empty(t, p.EventID, "a call without lessons records nothing")
	})

	t.Run("a rejected path adds the supplement", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.go")
		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "store.go", outside))

		res := decodeToolParts(t, resps["1"])
		assert.False(t, res.IsError, "a rejected path never fails change_set")
		require.Len(t, res.Content, 2)

		p := structuredPreparation(t, res)
		assert.Equal(t, 1, p.Request.RejectedCount)
		require.Len(t, p.Request.Rejected, 1)
		assert.Equal(t, "outside the workspace", p.Request.Rejected[0].Reason)
		assert.Equal(t, supplementOf(p), res.Content[1].Text)
		assert.Contains(t, res.Content[1].Text, "outside the workspace")
	})

	t.Run("no tool declares an outputSchema", func(t *testing.T) {
		resps := serve(t, root, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		require.Nil(t, resps["1"].Error)

		var list struct {
			Tools []map[string]json.RawMessage `json:"tools"`
		}

		require.NoError(t, json.Unmarshal(resps["1"].Result, &list))
		require.NotEmpty(t, list.Tools)

		for _, tool := range list.Tools {
			assert.NotContains(t, tool, "outputSchema", "tool %s", tool["name"])
		}
	})

	t.Run("a failed index refresh keeps the pins", func(t *testing.T) {
		// An index config that does not decode stops the index run after
		// the database exists. The server then opens a database without a
		// completed index run. The preparation says so and keeps the pins.
		dir := t.TempDir()
		fixtureWrite(t, dir, ".seamark/lessons.yaml", prepFixtureLessons)
		fixtureWrite(t, dir, ".seamark/config.yaml", "index: [\n")

		resps := serve(t, dir, initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"))

		res := decodeToolParts(t, resps["1"])
		assert.False(t, res.IsError)

		p := structuredPreparation(t, res)
		assert.Equal(t, delivery.MinedNoIndex, p.Guidance.MinedStatus)
		require.Len(t, p.Pins, 1)
		assert.Equal(t, "keep-parser-strict", p.Pins[0].Rule)
	})

	// The check subtest rewrites lessons.yaml in the working tree, so it
	// runs last.
	t.Run("the supplement lists every check in scope", func(t *testing.T) {
		fixtureWrite(t, root, ".seamark/lessons.yaml", prepFixtureChecks)

		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "docs/notes.md"))

		res := decodeToolParts(t, resps["1"])
		require.Len(t, res.Content, 2)

		p := structuredPreparation(t, res)
		assert.Equal(t, "valid", p.Guidance.ChecksStatus)
		require.Len(t, p.Checks, 1)
		assert.Equal(t, "enforce", p.Checks[0].Mode)
		assert.True(t, p.Checks[0].Runs)
		assert.Equal(t, "frozen-docs", p.Checks[0].Check.ID)
		assert.Equal(t, []string{"docs/notes.md"}, p.Checks[0].Paths)
		require.NotEmpty(t, p.Checks[0].Predicate)

		assert.Equal(t, supplementOf(p), res.Content[1].Text)
		assert.Contains(t, res.Content[1].Text, "checks (every check in scope; no budget applies)")
		assert.Contains(t, res.Content[1].Text, p.Checks[0].Predicate)
		assert.NotContains(t, res.Content[1].Text, "Change the docs only in a release.")
	})
}

// TestChangeSetPreparationRepeatsTheReportInStructuredContent proves the
// change_set_report key. A client can show structuredContent in place of
// the text parts, so the key carries the report of the first text part.
// The key equals that part byte for byte, and it sits beside the
// preparation document, because lesson-preparation/1 does not change. An
// older session gets no key. TestChangeSetPreparationSurvivesAPrepareFailure
// proves the failed state, which has no structured result at all.
func TestChangeSetPreparationRepeatsTheReportInStructuredContent(t *testing.T) {
	root := changeSetPrepFixture(t)

	t.Run("the key equals the first text part", func(t *testing.T) {
		cases := []struct {
			name  string
			files []string
		}{
			{name: "a pinned path", files: []string{"internal/parse/strict.go"}},
			{name: "a path without pins", files: []string{"store.go"}},
			{name: "a new path", files: []string{"docs/new.md"}},
			{name: "a rejected path", files: []string{"store.go", filepath.Join(t.TempDir(), "outside.go")}},
			{name: "two paths", files: []string{"internal/parse/strict.go", "store.go"}},

			// The report sanitizes the name that the client sends. The key
			// must hold the sanitized text, not the raw request.
			{name: "a path with an escape byte", files: []string{"docs/new\x1b[2J.md"}},
		}

		for _, version := range []string{"2025-06-18", "2025-11-25"} {
			requests := []string{initializeLine(version)}

			for i, c := range cases {
				requests = append(requests, changeSetCall(t, i+1, c.files...))
			}

			resps := serve(t, root, requests...)

			for i, c := range cases {
				res := decodeToolParts(t, resps[strconv.Itoa(i+1)])
				assert.False(t, res.IsError, c.name)

				text := structuredReport(t, res)
				assert.NotEmpty(t, text, c.name)
				assert.Equal(t, res.Content[0].Text, text, c.name)
				assert.NotContains(t, text, "\x1b", c.name)
			}
		}
	})

	t.Run("the preparation document does not hold the key", func(t *testing.T) {
		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"))

		var document map[string]json.RawMessage

		structured := structuredKeys(t, decodeToolParts(t, resps["1"]))
		require.NoError(t, json.Unmarshal(structured["lesson_preparation"], &document))
		assert.Contains(t, document, "schema")
		assert.NotContains(t, document, "change_set_report")
	})

	t.Run("a session before 2025-06-18 gets no key", func(t *testing.T) {
		for name, requests := range map[string][]string{
			"2025-03-26":    {initializeLine("2025-03-26"), changeSetCall(t, 1, "internal/parse/strict.go")},
			"no initialize": {changeSetCall(t, 1, "internal/parse/strict.go")},
		} {
			resps := serve(t, root, requests...)

			res := decodeToolParts(t, resps["1"])
			assert.Empty(t, res.StructuredContent, name)
			assert.NotContains(t, string(resps["1"].Result), "change_set_report", name)
		}
	})
}

// breakRefresh writes a configuration that does not decode. Each later
// refresh of root then fails, and the answers come from the existing
// index.
func breakRefresh(t *testing.T, root string) {
	t.Helper()

	fixtureWrite(t, root, filepath.Join(store.DefaultDir, "config.yaml"), "index: [\n")
}

// TestChangeSetPreparationRepeatsTheRefreshNote proves the
// change_set_report key after a failed refresh. withStore opens
// content[0] with a note that says the answer can be stale. The log does
// not reach the agent, so a client that shows only structuredContent gets
// this warning from the key or not at all.
func TestChangeSetPreparationRepeatsTheRefreshNote(t *testing.T) {
	const pinned, plain = "internal/parse/strict.go", "store.go"

	assertNoted := func(t *testing.T, res toolParts, note string) {
		t.Helper()

		text := res.Content[0].Text

		assert.False(t, res.IsError, text)
		assert.True(t, strings.HasPrefix(text, note), text)
		assert.Equal(t, 1, strings.Count(text, note), "the report names the note once")
		assert.Equal(t, text, structuredReport(t, res), "the key equals content[0]")
	}

	t.Run("a refresh that fails", func(t *testing.T) {
		root := changeSetPrepFixture(t)
		requests := []string{initializeLine("2025-06-18"), changeSetCall(t, 1, pinned), changeSetCall(t, 2, plain)}

		// The call before the break gives the text without the note.
		clean := serve(t, root, requests...)

		breakRefresh(t, root)

		noted := serve(t, root, requests...)

		for _, id := range []string{"1", "2"} {
			res := decodeToolParts(t, noted[id])

			assertNoted(t, res, brokenNote)
			assert.Equal(t, brokenNote+decodeToolParts(t, clean[id]).Content[0].Text, res.Content[0].Text,
				"content[0] gains only the note")
		}
	})

	t.Run("a refused move of another root", func(t *testing.T) {
		_, other, dbPath := rootMoveFixture(t)

		resps := serveDB(t, other, dbPath, t.Logf, initializeLine("2025-06-18"), changeSetCall(t, 1, "a.go"))

		assertNoted(t, decodeToolParts(t, resps["1"]), staleNote)
	})

	t.Run("a linked worktree after an edit", func(t *testing.T) {
		_, tree := rootMoveLinked(t, true)

		rootMoveEdit(t, tree)

		resps := serveDB(t, tree, store.DefaultPath(tree), t.Logf, initializeLine("2025-06-18"), changeSetCall(t, 1, "a.go"))

		assertNoted(t, decodeToolParts(t, resps["1"]), staleNote)
	})

	t.Run("a session before 2025-06-18 gets the note and no key", func(t *testing.T) {
		root := changeSetPrepFixture(t)

		serve(t, root, changeSetCall(t, 1, pinned))
		breakRefresh(t, root)

		resps := serve(t, root, initializeLine("2025-03-26"), changeSetCall(t, 1, pinned))

		res := decodeToolParts(t, resps["1"])
		assert.True(t, strings.HasPrefix(res.Content[0].Text, brokenNote), res.Content[0].Text)
		assert.Empty(t, res.StructuredContent)
		assert.NotContains(t, string(resps["1"].Result), "change_set_report")
	})

	// A failed preparation has no structured result. So the key has no
	// map to go in, and the text parts carry the note.
	t.Run("a failed preparation gets the note and no structured result", func(t *testing.T) {
		root := changeSetPrepFixture(t)

		serve(t, root, changeSetCall(t, 1, pinned))
		breakRefresh(t, root)
		failPreparation(t, errors.New("select lessons: disk I/O error"))

		resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, pinned))

		res := decodeToolParts(t, resps["1"])
		assert.False(t, res.IsError, "a failed preparation never fails change_set")
		require.Len(t, res.Content, 2)
		assert.True(t, strings.HasPrefix(res.Content[0].Text, brokenNote), res.Content[0].Text)
		assert.Empty(t, res.StructuredContent)
	})
}

// failPreparation makes every change_set preparation fail with err for
// the rest of the test. It returns the requests that the server passed.
func failPreparation(t *testing.T, err error) *[]delivery.PrepareRequest {
	t.Helper()

	var requests []delivery.PrepareRequest

	saved := prepareChangeSet
	prepareChangeSet = func(_ context.Context, _ *store.Store, req delivery.PrepareRequest) (*delivery.Preparation, error) {
		requests = append(requests, req)

		return nil, err
	}

	t.Cleanup(func() { prepareChangeSet = saved })

	return &requests
}

// TestChangeSetPreparationSurvivesAPrepareFailure proves the failure
// path of the change_set preparation and its fallback record.
// content[0] keeps its bytes, the second part names the sanitized error,
// and no structuredContent follows. A call with lessons writes one
// record with evaluation failed and no pins. A call without lessons
// writes none.
func TestChangeSetPreparationSurvivesAPrepareFailure(t *testing.T) {
	root := changeSetPrepFixture(t)

	// A call with a working preparation gives the reference text of
	// content[0] and the reference record of the same lessons.
	resps := serve(t, root, initializeLine("2025-06-18"),
		changeSetCall(t, 1, "internal/parse/strict.go"), changeSetCall(t, 2, "docs/new.md"))
	wantPinned := decodeToolParts(t, resps["1"]).Content[0].Text
	wantNew := decodeToolParts(t, resps["2"]).Content[0].Text

	reference := prepFirings(t, root)
	require.Len(t, reference, 1)

	const cause = "select lessons: disk I/O error \x1b[2J"

	requests := failPreparation(t, errors.New(cause))
	unavailable := "lesson preparation unavailable: " + render.Sanitize(cause)

	resps = serve(t, root, initializeLine("2025-06-18"),
		changeSetCall(t, 1, "internal/parse/strict.go"), changeSetCall(t, 2, "docs/new.md"))

	for id, want := range map[string]string{"1": wantPinned, "2": wantNew} {
		res := decodeToolParts(t, resps[id])
		assert.False(t, res.IsError, "a failed preparation never fails change_set")
		require.Len(t, res.Content, 2)
		assert.Equal(t, want, res.Content[0].Text, "content[0] keeps its bytes")
		assert.Equal(t, unavailable, res.Content[1].Text)
		assert.NotContains(t, res.Content[1].Text, "\x1b")
		assert.Empty(t, res.StructuredContent, "a failed preparation has no structuredContent")
	}

	// The server asks for the change_set surface with the workspace root
	// as the directory of relative paths, and lets Prepare read the config.
	require.Len(t, *requests, 2)

	for _, req := range *requests {
		assert.Equal(t, root, req.Root)
		assert.Equal(t, root, req.CWD)
		assert.Equal(t, delivery.SurfaceChangeSet, req.Surface)
		assert.Equal(t, delivery.MinedOK, req.MinedStatus)
		assert.Equal(t, "test", req.SeamarkVersion)
		assert.Nil(t, req.Config)
		assert.NoError(t, req.ConfigErr)
	}

	assert.Equal(t, []string{"internal/parse/strict.go"}, (*requests)[0].Paths)

	// Only the call with lessons adds a record.
	firings := prepFirings(t, root)
	require.Len(t, firings, 2)

	rec := firings[1]
	assert.Equal(t, "change_set", rec.Surface)
	assert.Equal(t, "internal/parse/strict.go", rec.File)
	assert.Equal(t, reference[0].Fired, rec.Fired, "the record keeps the lessons of content[0]")
	assert.Empty(t, rec.Delivery)
	assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion)
	assert.Len(t, rec.EventID, 32)
	assert.Equal(t, 1, rec.EventSeq)
	assert.Equal(t, 1, rec.EventRecords)
	assert.Equal(t, "returned", rec.Emission)
	assert.Equal(t, "failed", rec.Evaluation)
	require.NotNil(t, rec.PathsComplete)
	assert.False(t, *rec.PathsComplete)
	assert.Empty(t, rec.Pins, "no preparation named the pins")
	assert.Empty(t, rec.ScopeSHA)
}

// TestChangeSetPreparationRecordsNothingWhenTheTextFails proves that a
// failed change_set text leaves no record. The preparation succeeds
// with a selection, and then the change_set text fails on a store read.
// The call returns the error, so no answer reaches the caller, and the
// firing log gets no returned record for it.
func TestChangeSetPreparationRecordsNothingWhenTheTextFails(t *testing.T) {
	root := changeSetPrepFixture(t)

	// The control call shows that this path writes a record when the
	// text succeeds. So the absence below cannot pass by doing nothing.
	resps := serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"))
	control := structuredPreparation(t, decodeToolParts(t, resps["1"]))
	require.NotEmpty(t, control.EventID)
	require.Len(t, prepFirings(t, root), 1, "a call with lessons writes one record")

	// The real preparation runs, and the test keeps its result.
	var prepared []*delivery.Preparation

	savedPrepare := prepareChangeSet
	prepareChangeSet = func(ctx context.Context, st *store.Store, req delivery.PrepareRequest) (*delivery.Preparation, error) {
		p, err := savedPrepare(ctx, st, req)
		if err == nil {
			prepared = append(prepared, p)
		}

		return p, err
	}

	t.Cleanup(func() { prepareChangeSet = savedPrepare })

	const cause = "co-change partners: disk I/O error"

	savedText := changeSetText
	changeSetText = func(io.Writer, *store.Store, string, []string, report.ChangeSetOptions) ([]model.Lesson, error) {
		return nil, errors.New(cause)
	}

	t.Cleanup(func() { changeSetText = savedText })

	resps = serve(t, root, initializeLine("2025-06-18"), changeSetCall(t, 1, "internal/parse/strict.go"))

	res := decodeToolParts(t, resps["1"])
	assert.True(t, res.IsError, "a failed text fails the call")
	require.Len(t, res.Content, 1)
	assert.Equal(t, cause, res.Content[0].Text)
	assert.Empty(t, res.StructuredContent)

	require.Len(t, prepared, 1, "the preparation ran before the text")
	assert.NotEmpty(t, prepared[0].Pins, "the preparation selected a pin")
	assert.Empty(t, prepared[0].EventID, "no record names the preparation")
	assert.Len(t, prepFirings(t, root), 1, "a failed call adds no record")
}

// TestChangeSetPreparationRecordsOneEventPerCall proves the audit order
// of the change_set preparation: the record of the preparation is the
// one record of a call with lessons, and the text writes no legacy
// record (Record false). A repeat is a new event. A call without
// lessons writes nothing. The record names no receiver, and no call
// touches a delivery state.
func TestChangeSetPreparationRecordsOneEventPerCall(t *testing.T) {
	root := changeSetPrepFixture(t)
	abs := filepath.Join(root, "store.go")

	resps := serve(t, root, initializeLine("2025-06-18"),
		changeSetCall(t, 1, "internal/parse/strict.go"),
		changeSetCall(t, 2, "internal/parse/strict.go"),
		changeSetCall(t, 3, "store.go"),
		changeSetCall(t, 4, "./internal/parse/fresh.go", abs, "docs/new.md"),
	)

	firings := prepFirings(t, root)
	require.Len(t, firings, 3, "one record for each call with lessons, and no legacy record")

	none := structuredPreparation(t, decodeToolParts(t, resps["3"]))
	assert.Empty(t, none.EventID)

	eventIDs := map[string]bool{}

	for i, id := range []string{"1", "2", "4"} {
		p := structuredPreparation(t, decodeToolParts(t, resps[id]))
		rec := firings[i]

		assert.Equal(t, p.EventID, rec.EventID, "call %s names its record", id)
		assert.Equal(t, reviews.AuditSchemaVersion, rec.SchemaVersion, "call %s", id)
		assert.Equal(t, "change_set", rec.Surface, "call %s", id)
		assert.Equal(t, 1, rec.EventSeq, "call %s", id)
		assert.Equal(t, 1, rec.EventRecords, "call %s", id)
		assert.Equal(t, "returned", rec.Emission, "call %s", id)
		assert.Equal(t, "complete", rec.Evaluation, "call %s", id)
		assert.Equal(t, p.Request.ScopeSHA256, rec.ScopeSHA, "call %s", id)
		assert.Equal(t, p.Guidance.GuidanceSHA256, rec.GuidanceSHA, "call %s", id)

		// The selected pin is returned, and the budget held the other back.
		require.Len(t, rec.Pins, 2, "call %s", id)
		assert.Equal(t, reviews.FiringPin{Identity: p.Pins[0].PinIdentity, Version: p.Pins[0].PinVersion,
			Delivery: reviews.PinReturned}, rec.Pins[0], "call %s", id)
		assert.Equal(t, p.Omitted[0].PinVersion, rec.Pins[1].Version, "call %s", id)
		assert.Equal(t, reviews.PinHeldBack, rec.Pins[1].Delivery, "call %s", id)
		assert.Equal(t, reviews.CauseOverTotal, rec.Pins[1].Cause, "call %s", id)

		eventIDs[rec.EventID] = true
	}

	assert.Len(t, eventIDs, 3, "every call is its own event")
	assert.Equal(t, "internal/parse/strict.go", firings[0].File)
	assert.Equal(t, []string{"internal/parse/fresh.go", "store.go", "docs/new.md"}, firings[2].Files)

	// The text and the preparation select one lesson set for paths inside
	// the workspace, whatever their spelling.
	mixed := decodeToolParts(t, resps["4"])
	p := structuredPreparation(t, mixed)
	require.Len(t, p.Pins, 1)
	assert.Equal(t, "keep-parser-strict", p.Pins[0].Rule)
	assert.Contains(t, mixed.Content[0].Text, "- [pin · internal/parse] keep-parser-strict — Reject unknown fields in the parser.\n")
	assert.Contains(t, mixed.Content[0].Text, "(+1 more:", "the text holds back the pin that the preparation omits")

	// The receiver is unknown, so no record carries a receiver key.
	data, err := os.ReadFile(reviews.AuditLogPath(root))
	require.NoError(t, err)

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var keys map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(line), &keys))

		for _, key := range []string{"delivery", "session_sha256", "match_sha256", "context_sha256",
			"turn_sha256", "lane", "context_resettable", "record_evaluations", "context_generation"} {
			assert.NotContains(t, keys, key)
		}
	}

	// Preparation takes no delivery lease, so neither state file exists.
	for _, name := range []string{
		"lessons-hook-state.json", "lessons-hook-state.lock",
		"lessons-explore-state.json", "lessons-explore-state.lock",
	} {
		assert.NoFileExists(t, filepath.Join(root, ".seamark", name))
	}
}

// TestChangeSetPreparationNeverInvokes proves that change_set calls no
// model: a call with pins and checks, a new path, a path without pins,
// and a rejected path start no agent, no model CLI, and no network
// client.
func TestChangeSetPreparationNeverInvokes(t *testing.T) {
	root, sentinel := lessonChecksFixture(t, "warn")
	outside := filepath.Join(t.TempDir(), "outside.go")

	resps := serve(t, root, initializeLine("2025-06-18"),
		changeSetCall(t, 1, "generated/a.txt"),
		changeSetCall(t, 2, "src/handler.go", "src/new.go"),
		changeSetCall(t, 3, "docs/readme.md"),
		changeSetCall(t, 4, outside),
	)

	for _, id := range []string{"1", "2", "3", "4"} {
		res := decodeToolParts(t, resps[id])
		assert.False(t, res.IsError, "call %s: %s", id, res.Content[0].Text)
		structuredPreparation(t, res)
	}

	// The preparation lists the checks of the armed pins, so the call
	// read the check definitions and still ran nothing.
	p := structuredPreparation(t, decodeToolParts(t, resps["1"]))
	require.Len(t, p.Checks, 1)
	assert.Equal(t, "frozen-assets", p.Checks[0].Check.ID)
	assert.True(t, p.Checks[0].Runs)

	p = structuredPreparation(t, decodeToolParts(t, resps["2"]))
	require.Len(t, p.Checks, 1)
	assert.Equal(t, "no-todo", p.Checks[0].Check.ID)
	assert.Equal(t, []string{"src/handler.go", "src/new.go"}, p.Checks[0].Paths)

	sentinel.requireNoCall(t)
}

// TestChangeSetPreparationGuidance pins the preparation guidance to the
// three surfaces a client reads: the instructions of initialize, the
// change_set description of tools/list, and the onboard prompt. Each
// surface says on its own that preparation comes before the
// construction of any change, one file and a new path included, that a
// later step constructs the change, and that preparation authorizes no
// write. Each surface is read through the protocol, so a client sees
// what the test sees.
func TestChangeSetPreparationGuidance(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root,
		initializeLine("2025-06-18"),
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"prompts/get","params":{"name":"onboard"}}`,
	)

	var init struct {
		Instructions string `json:"instructions"`
	}

	require.Nil(t, resps["0"].Error)
	require.NoError(t, json.Unmarshal(resps["0"].Result, &init))

	var list struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}

	require.Nil(t, resps["1"].Error)
	require.NoError(t, json.Unmarshal(resps["1"].Result, &list))

	description := ""

	for _, tool := range list.Tools {
		if tool.Name == "change_set" {
			description = tool.Description
		}
	}

	var prompt struct {
		Messages []struct {
			Content struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}

	require.Nil(t, resps["2"].Error)
	require.NoError(t, json.Unmarshal(resps["2"].Result, &prompt))
	require.Len(t, prompt.Messages, 1)

	surfaces := map[string]string{
		"instructions":           init.Instructions,
		"change_set description": description,
		"onboard prompt":         prompt.Messages[0].Content.Text,
	}

	// The construction and authorization phrases are the words of the
	// preparation notice, so the guidance and every preparation result
	// give one meaning to each term.
	for _, phrase := range []string{"in a later step, not in the same generated program", "authorizes no write"} {
		require.Contains(t, delivery.PreparationNotice, phrase)
	}

	for name, text := range surfaces {
		require.NotEmpty(t, text, name)

		for _, phrase := range []string{
			"before you construct any change",
			"one file",
			"a new path",
			"in a later step, not in the same generated program",
			"authorizes no write",
		} {
			assert.Contains(t, text, phrase, "%s states the preparation obligation", name)
		}

		// The multi-file rule of the earlier guidance skipped one file
		// and a new path, which a pin can cover.
		assert.NotContains(t, text, "more than one file", name)
	}

	// Instructions and the onboard prompt also ask for a new preparation
	// when the file set grows; the description stays terse, because
	// clients replay it on every turn.
	for _, name := range []string{"instructions", "onboard prompt"} {
		assert.Contains(t, surfaces[name], "again when the file set grows", name)
	}
}

// TestChangeSetRejectsAnEmptyBlankOrNULPath proves that change_set
// fails a call with an entry that names no path. Before the check, the
// text listed the package nodes and every pin for an empty entry, while
// the preparation rejected the same entry. The first bad entry names the
// error. A rejected call refreshes no index, runs neither part, and
// writes no firing record. The session still answers a later valid call.
func TestChangeSetRejectsAnEmptyBlankOrNULPath(t *testing.T) {
	const prefix = "change_set needs a path in every files entry: "

	root := changeSetPrepFixture(t)

	// JSON null decodes to the empty string, so it is an empty entry.
	nullCall := `{"jsonrpc":"2.0","id":7,"method":"tools/call",` +
		`"params":{"name":"change_set","arguments":{"files":[null]}}}`

	cases := []struct {
		id      string
		request string
		want    string
	}{
		{"1", changeSetCall(t, 1, ""), prefix + "entry 1 is empty"},
		{"2", changeSetCall(t, 2, "  "), prefix + "entry 1 holds only white space"},
		{"3", changeSetCall(t, 3, "\t"), prefix + "entry 1 holds only white space"},
		{"4", changeSetCall(t, 4, "store.go", ""), prefix + "entry 2 is empty"},
		{"5", changeSetCall(t, 5, "internal/parse/x\x00.go"), prefix + "entry 1 holds a NUL byte"},
		{"6", changeSetCall(t, 6, "store.go", " ", ""), prefix + "entry 2 holds only white space"},
		{"7", nullCall, prefix + "entry 1 is empty"},
	}

	audit := filepath.Join(root, ".seamark", "lessons-audit.jsonl")

	readAudit := func() []byte {
		data, err := os.ReadFile(audit)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		require.NoError(t, err)

		return data
	}

	for n, branch := range prepProtocolBranches {
		t.Run(branch.name, func(t *testing.T) {
			// The server logs each refresh. The log is the only trace of a
			// refresh that changes no answer, so the test reads it.
			var (
				mu  sync.Mutex
				log []string
			)

			logf := func(format string, a ...any) {
				mu.Lock()
				defer mu.Unlock()

				log = append(log, fmt.Sprintf(format, a...))
			}

			takeLog := func() string {
				mu.Lock()
				defer mu.Unlock()

				lines := strings.Join(log, "\n")
				log = nil

				return lines
			}

			srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", logf)

			if branch.initialize != "" {
				callServer(t, srv, branch.initialize)
			}

			// A new file changes the workspace, so a refresh would rebuild
			// the index. Each branch needs its own file, because the valid
			// call of the branch before has indexed the earlier one.
			fixtureWrite(t, root, fmt.Sprintf("docs/draft%d.md", n), "draft\n")
			takeLog()

			// The two parts must not run for a rejected call. A rejected
			// call that reached them would fail here, and not later.
			savedPrepare, savedText := prepareChangeSet, changeSetText

			prepareChangeSet = func(context.Context, *store.Store, delivery.PrepareRequest) (*delivery.Preparation, error) {
				t.Error("a rejected call runs no preparation")

				return nil, errors.New("preparation ran")
			}

			changeSetText = func(io.Writer, *store.Store, string, []string, report.ChangeSetOptions) ([]model.Lesson, error) {
				t.Error("a rejected call writes no change_set text")

				return nil, errors.New("text ran")
			}

			restore := func() { prepareChangeSet, changeSetText = savedPrepare, savedText }
			t.Cleanup(restore)

			before := readAudit()

			requests := make([]string, 0, len(cases))
			for _, c := range cases {
				requests = append(requests, c.request)
			}

			resps := callServer(t, srv, requests...)
			require.Len(t, resps, len(cases))

			for _, c := range cases {
				res := decodeToolParts(t, resps[c.id])
				assert.True(t, res.IsError, c.want)
				require.Len(t, res.Content, 1, c.want)
				assert.Equal(t, c.want, res.Content[0].Text)
				assert.Empty(t, res.StructuredContent, c.want)
			}

			restore()

			assert.NotContains(t, takeLog(), "mcp: index refresh", "a rejected call refreshes no index")
			assert.Equal(t, before, readAudit(), "a rejected call writes no firing record")

			valid := decodeToolParts(t, callServer(t, srv, changeSetCall(t, 8, "store.go"))["8"])
			assert.False(t, valid.IsError)
			assert.Equal(t, prepIndexedText, valid.Content[0].Text)
			assert.Contains(t, takeLog(), "mcp: index refreshed", "the new file makes the valid call refresh")
		})
	}
}

// TestChangeSetPathRuleMatchesTheMalformedRule keeps the change_set
// entry check in step with the malformed rule of the preparation. The
// check rejects every malformed path, and it rejects nothing else but a
// blank path.
func TestChangeSetPathRuleMatchesTheMalformedRule(t *testing.T) {
	root := t.TempDir()

	inputs := []string{"", "\x00", "a\x00b", " ", "\t\n", "store.go", "./store.go", " store.go",
		"../x.go", "/etc/passwd", "."}

	for _, in := range inputs {
		n := delivery.NormalizePaths(root, root, []string{in})
		malformed := len(n.Rejected) == 1 && n.Rejected[0].Reason == delivery.RejectMalformed
		rejected := report.CheckChangeSetFiles([]string{in}) != nil

		if malformed {
			assert.True(t, rejected, "%q is malformed, so change_set must reject it", in)
		}

		if rejected {
			assert.True(t, malformed || strings.TrimSpace(in) == "",
				"%q is not malformed and not blank, so change_set must accept it", in)
		}
	}
}
