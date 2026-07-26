package mcp

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/fix\n",
		"a.go": `package main

func main() { helper() }

// helper does the work.
func helper() {}
`,
	}

	for rel, content := range files {
		p := filepath.Join(root, rel)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	for _, args := range [][]string{
		{"init", "-b", "main"}, {"add", "-A"}, {"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	return root
}

type rpcResp struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// serve feeds a newline-delimited request script through the server and
// returns responses keyed by request id.
func serve(t *testing.T, root string, requests ...string) map[string]rpcResp {
	t.Helper()

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test",
		func(format string, a ...any) { t.Logf("server: "+format, a...) })

	var out strings.Builder
	require.NoError(t, srv.Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out))

	responses := map[string]rpcResp{}

	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}

		var r rpcResp
		require.NoError(t, json.Unmarshal([]byte(line), &r), "response line: %s", line)
		responses[string(r.ID)] = r
	}

	return responses
}

func toolText(t *testing.T, r rpcResp) (string, bool) {
	t.Helper()

	require.Nil(t, r.Error, "expected a tool result, got protocol error")

	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	require.NoError(t, json.Unmarshal(r.Result, &res))
	require.Len(t, res.Content, 1)

	return res.Content[0].Text, res.IsError
}

func TestLifecycleAndTools(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"orient","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"change_set","arguments":{"files":["a.go"]}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"expand","arguments":{"ref":"helper"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"check","arguments":{}}}`,
	)

	// initialize: identity + echoed protocol version.
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	require.Nil(t, resps["1"].Error)
	require.NoError(t, json.Unmarshal(resps["1"].Result, &init))
	assert.Equal(t, "seamark", init.ServerInfo.Name)
	assert.Equal(t, "2025-03-26", init.ProtocolVersion)

	// The notification produced no response.
	assert.Len(t, resps, 7)

	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.Nil(t, resps["2"].Error)
	require.NoError(t, json.Unmarshal(resps["2"].Result, &list))
	assert.Len(t, list.Tools, 5, "five tools, not forty")

	why, isErr := toolText(t, resps["3"])
	assert.False(t, isErr)
	assert.Contains(t, why, "helper")
	assert.Contains(t, why, "callers")

	orient, isErr := toolText(t, resps["4"])
	assert.False(t, isErr)
	assert.Contains(t, orient, "orientation")
	assert.Contains(t, orient, "most-called")

	changeSet, isErr := toolText(t, resps["5"])
	assert.False(t, isErr)
	assert.Contains(t, changeSet, "a.go")
	assert.Contains(t, changeSet, "defines",
		"a file with no callers/effects must still show it was indexed")

	expand, isErr := toolText(t, resps["6"])
	assert.False(t, isErr)
	assert.Contains(t, expand, "func helper()")

	check, isErr := toolText(t, resps["7"])
	assert.False(t, isErr)
	assert.Contains(t, check, "verdict")
}

func TestToolErrorsStayInBand(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"why","arguments":{"query":"no_such_symbol_xyz"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"nonexistent","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"no/such/method"}`,
	)

	// Missing and unmatched arguments surface as isError results the
	// model can read and correct — not as protocol failures.
	text, isErr := toolText(t, resps["1"])
	assert.True(t, isErr)
	assert.Contains(t, text, "query")

	text, isErr = toolText(t, resps["2"])
	assert.True(t, isErr)
	assert.Contains(t, text, "no_such_symbol_xyz")

	// Unknown tool and unknown method ARE protocol errors.
	require.NotNil(t, resps["3"].Error)
	assert.Equal(t, codeInvalidParams, resps["3"].Error.Code)

	require.NotNil(t, resps["4"].Error)
	assert.Equal(t, codeMethodNotFound, resps["4"].Error.Code)
}

func TestMalformedLineDoesNotKillServer(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root,
		`this is not json`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
	)

	// The garbage line got a parse error with a null id…
	require.NotNil(t, resps["null"].Error)
	assert.Equal(t, codeParse, resps["null"].Error.Code)

	// …and the server kept serving.
	require.Nil(t, resps["1"].Error)
}

func TestOversizedFrameDoesNotKillServer(t *testing.T) {
	root := writeFixture(t)

	// A single frame past maxLine must be rejected in-band, not tear the
	// connection down — the next request still has to be answered. (An
	// earlier bufio.Scanner implementation died here with ErrTooLong.)
	huge := `{"jsonrpc":"2.0","id":1,"method":"ping","pad":"` +
		strings.Repeat("x", maxLine+16) + `"}`

	resps := serve(t, root,
		huge,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
	)

	require.NotNil(t, resps["null"].Error, "oversized frame gets an in-band error")
	assert.Equal(t, codeInvalidRequest, resps["null"].Error.Code)

	require.Nil(t, resps["2"].Error, "server keeps serving after an oversized frame")
	assert.NotNil(t, resps["2"].Result)
}

func TestResourcesAndPrompts(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"seamark://orient"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/read","params":{"uri":"seamark://nope"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"prompts/get","params":{"name":"onboard"}}`,
	)

	require.Nil(t, resps["1"].Error)
	assert.Contains(t, string(resps["1"].Result), "seamark://orient")

	var read struct {
		Contents []struct {
			Text string `json:"text"`
		} `json:"contents"`
	}
	require.Nil(t, resps["2"].Error)
	require.NoError(t, json.Unmarshal(resps["2"].Result, &read))
	require.Len(t, read.Contents, 1)
	assert.Contains(t, read.Contents[0].Text, "orientation")

	require.NotNil(t, resps["3"].Error)
	assert.Equal(t, codeResourceNotFound, resps["3"].Error.Code)

	require.Nil(t, resps["4"].Error)
	assert.Contains(t, string(resps["4"].Result), "onboard")

	require.Nil(t, resps["5"].Error)
	assert.Contains(t, string(resps["5"].Result), "orient")
}

func TestFreshnessSelfRepair(t *testing.T) {
	root := writeFixture(t)

	// First call builds the index; a workspace change between calls must
	// be visible in the next answer without any explicit re-index.
	resps := serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
	)

	_, isErr := toolText(t, resps["1"])
	require.False(t, isErr)

	require.NoError(t, os.WriteFile(filepath.Join(root, "b.go"),
		[]byte("package main\n\nfunc extra() { helper() }\n"), 0o644))

	resps = serve(t, root,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"extra"}}}`,
	)

	text, isErr := toolText(t, resps["1"])
	require.False(t, isErr)
	assert.Contains(t, text, "extra", "new symbol must be answerable after self-repair")
}
