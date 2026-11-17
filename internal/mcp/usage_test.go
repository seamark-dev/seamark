package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/mcpusage"
)

const usageInitialize = `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26",` +
	`"capabilities":{},"clientInfo":{"name":"test-client"}}}`

// usageRecords returns the records of the usage log under root.
func usageRecords(t *testing.T, root string) []mcpusage.Record {
	t.Helper()

	data, err := os.ReadFile(mcpusage.Path(root))
	require.NoError(t, err)

	var records []mcpusage.Record

	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var rec mcpusage.Record

		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		records = append(records, rec)
	}

	return records
}

// rawServe runs requests against root and returns the raw stdout and
// the format string of each log line.
func rawServe(t *testing.T, root, dbPath string, requests ...string) (stdout string, logs []string) {
	t.Helper()

	srv := New(root, dbPath, "test", func(format string, _ ...any) { logs = append(logs, format) })

	var out strings.Builder

	require.NoError(t, srv.Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out))

	return out.String(), logs
}

func TestMCPUsageRecordsEachToolCall(t *testing.T) {
	root := writeFixture(t)

	resps := serve(t, root, usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"why","arguments":{"query":"no-such-thing"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":"not an object"}`,
	)

	records := usageRecords(t, root)
	require.Len(t, records, 4)

	for _, rec := range records {
		assert.Equal(t, 1, rec.V)
		assert.NotEmpty(t, rec.At)
		assert.Equal(t, "test-client", rec.Client)
		assert.GreaterOrEqual(t, rec.MS, int64(0))
	}

	text, _ := toolText(t, resps["1"])
	assert.Equal(t, "why", records[0].Tool)
	assert.True(t, records[0].OK)
	assert.Equal(t, int64(len(text)), records[0].Bytes, "bytes measure the text parts")

	failed, isErr := toolText(t, resps["2"])
	require.True(t, isErr)
	assert.Equal(t, "why", records[1].Tool)
	assert.False(t, records[1].OK, "isError is a failed call")
	assert.Equal(t, int64(len(failed)), records[1].Bytes)

	assert.Equal(t, mcpusage.Unknown, records[2].Tool, "a name that no tool has stays out of the file")
	assert.False(t, records[2].OK)
	assert.Zero(t, records[2].Bytes)

	assert.Equal(t, mcpusage.Unknown, records[3].Tool)
	assert.False(t, records[3].OK)
}

func TestMCPUsageSumsEveryTextPart(t *testing.T) {
	srv := New(t.TempDir(), "", "test", nil)

	size, ok := replyUsage(srv.outputResult(toolOutput{Text: []string{"four", "sixsix"}}), nil)

	assert.Equal(t, int64(10), size)
	assert.True(t, ok)
}

func TestMCPUsageIgnoresOtherRequests(t *testing.T) {
	root := writeFixture(t)

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".seamark"), 0o755))

	resps := serve(t, root, usageInitialize,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"prompts/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"no/such/method"}`,
		`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
	)

	require.Len(t, resps, 6, "the call without an id is a notification and has no reply")
	assert.NoFileExists(t, mcpusage.Path(root))
}

func TestMCPUsageClientName(t *testing.T) {
	const call = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`

	initialize := func(info string) string {
		return `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26",` +
			`"capabilities":{}` + info + `}}`
	}

	for _, tc := range []struct {
		name     string
		requests []string
		want     string
	}{
		{name: "no initialize", requests: []string{call}, want: mcpusage.Unknown},
		{name: "no clientInfo", requests: []string{initialize(""), call}, want: mcpusage.Unknown},
		{name: "a wrong type", requests: []string{initialize(`,"clientInfo":"text"`), call}, want: mcpusage.Unknown},
		{name: "an escape", requests: []string{initialize(`,"clientInfo":{"name":"a\u001b[31mb"}`), call}, want: "a[31mb"},
		{
			name:     "a long name",
			requests: []string{initialize(`,"clientInfo":{"name":"` + strings.Repeat("x", 500) + `"}`), call},
			want:     strings.Repeat("x", 64),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFixture(t)

			serve(t, root, tc.requests...)

			records := usageRecords(t, root)
			require.Len(t, records, 1)
			assert.Equal(t, tc.want, records[0].Client)
		})
	}
}

func TestMCPUsageKeepsAnInitializeWithAWrongClientInfo(t *testing.T) {
	resps := serve(t, writeFixture(t),
		`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","clientInfo":"text"}}`)

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}

	require.Nil(t, resps["0"].Error)
	require.NoError(t, json.Unmarshal(resps["0"].Result, &init))
	assert.Equal(t, "2025-03-26", init.ProtocolVersion, "a bad clientInfo must not change the reply")
}

// TestMCPUsageHoldsNoArgument searches the file for a sentinel value in
// each argument of each tool, because a query can hold private text.
func TestMCPUsageHoldsNoArgument(t *testing.T) {
	const sentinel = "SENTINELprivatetext"

	root := writeFixture(t)

	serve(t, root, usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"`+sentinel+`","`+sentinel+`key":1}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"expand","arguments":{"ref":"`+sentinel+`"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"change_set","arguments":{"files":["`+sentinel+`.go"]}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"check","arguments":{"diff":"+++ `+sentinel+`"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"`+sentinel+`","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"orient","`+sentinel+`":"`+sentinel+`"}}`,
	)

	data, err := os.ReadFile(mcpusage.Path(root))
	require.NoError(t, err)
	assert.Equal(t, 6, strings.Count(string(data), "\n"), "each call has a line, or the search proves nothing")
	assert.NotContains(t, string(data), sentinel)
}

// TestMCPUsageFailureNeverChangesTheReply compares the raw stdout of a
// server whose append fails with the stdout of a control run on the same
// repository. The fixture commit has a new hash in each repository, so a
// second repository cannot be the control.
func TestMCPUsageFailureNeverChangesTheReply(t *testing.T) {
	requests := []string{
		usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
	}

	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
	}{
		{
			name: "the lock is a directory",
			damage: func(t *testing.T, path string) {
				t.Helper()
				require.NoError(t, os.Mkdir(path+".lock", 0o755))
			},
		},
		{
			name: "the log is a link",
			damage: func(t *testing.T, path string) {
				t.Helper()
				require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "elsewhere"), path))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeFixture(t)
			dbPath := filepath.Join(t.TempDir(), "index.db")

			control, controlLogs := rawServe(t, root, dbPath, requests...)
			require.NotEmpty(t, control)
			assert.NotContains(t, controlLogs, usageFailureNote)

			require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o755))
			tc.damage(t, mcpusage.Path(root))

			out, logs := rawServe(t, root, dbPath, requests...)

			assert.Equal(t, control, out)
			assert.Contains(t, logs, usageFailureNote, "the failure reaches the log and not the client")
		})
	}
}

// replyCounter counts the reply frames of a server. Serve writes one line
// per reply.
type replyCounter struct {
	frames int
}

func (c *replyCounter) Write(p []byte) (int, error) {
	c.frames += bytes.Count(p, []byte("\n"))

	return len(p), nil
}

// TestMCPUsageFailureLogsOnceAfterTheLastReply covers a stderr pipe that
// the client does not read, or closes. A write there blocks the server or
// ends the process, and a count must not cost a call. So the session writes
// no failure note before its last reply, and it writes one note.
func TestMCPUsageFailureLogsOnceAfterTheLastReply(t *testing.T) {
	root := writeFixture(t)

	require.NoError(t, os.Mkdir(filepath.Join(root, ".seamark"), 0o755))
	require.NoError(t, os.Mkdir(mcpusage.Path(root)+".lock", 0o755))

	var (
		replies   replyCounter
		repliesAt []int
		failed    []any
	)

	srv := New(root, filepath.Join(t.TempDir(), "index.db"), "test", func(format string, a ...any) {
		if format == usageFailureNote {
			repliesAt = append(repliesAt, replies.frames)
			failed = append(failed, a[0])
		}
	})

	requests := []string{
		usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":"not an object"}`,
	}

	require.NoError(t, srv.Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &replies))

	require.Equal(t, len(requests), replies.frames)
	assert.Equal(t, []int{len(requests)}, repliesAt, "one note, after the last reply")
	assert.Equal(t, []any{3}, failed, "the note counts the calls that the log lost")
}

func TestMCPUsageCreatesNoSeamarkDirectory(t *testing.T) {
	root := writeFixture(t)

	// A database elsewhere keeps the index refresh out of root/.seamark.
	out, logs := rawServe(t, root, filepath.Join(t.TempDir(), "index.db"), usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`)

	assert.Contains(t, out, `"id":1`)
	assert.NoDirExists(t, filepath.Join(root, ".seamark"))
	assert.NotContains(t, logs, usageFailureNote, "an absent directory is no failure")
}

func TestMCPUsageReadOnlyRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the mode")
	}

	root := writeFixture(t)
	dir := filepath.Join(root, ".seamark")

	require.NoError(t, os.Mkdir(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	out, logs := rawServe(t, root, filepath.Join(t.TempDir(), "index.db"), usageInitialize,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`)

	assert.Contains(t, out, `"isError":false`)
	assert.Contains(t, logs, usageFailureNote)
	assert.NoFileExists(t, mcpusage.Path(root))
}
