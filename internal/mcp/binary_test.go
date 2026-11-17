package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// watchExecutable points the binary watch at path for one test. The
// server reads executable once, in New.
func watchExecutable(t *testing.T, path string, err error) {
	t.Helper()

	old := executable
	executable = func() (string, error) { return path, err }

	t.Cleanup(func() { executable = old })
}

// writeBinary writes a stand-in binary with the given bytes and time.
func writeBinary(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	require.NoError(t, os.Chtimes(path, mtime, mtime))
}

// callServer sends request lines to srv and returns the responses keyed
// by id. The server keeps its state between calls, as in one session.
func callServer(t *testing.T, srv *Server, requests ...string) map[string]rpcResp {
	t.Helper()

	var out strings.Builder
	require.NoError(t, srv.Serve(strings.NewReader(strings.Join(requests, "\n")+"\n"), &out))

	responses := map[string]rpcResp{}

	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var r rpcResp
		require.NoError(t, json.Unmarshal([]byte(line), &r), "response line: %s", line)
		responses[string(r.ID)] = r
	}

	return responses
}

const whyCall = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{"query":"helper"}}}`

func changedNote(path string) string {
	return "note: the seamark binary at " + path + " changed after this MCP server started; " +
		"this server still runs seamark test. Restart the agent or reconnect its MCP server to use the " +
		"installed binary.\n"
}

func removedNote(path string) string {
	return "note: the seamark binary at " + path + " was removed or moved after this MCP server started; " +
		"this server still runs seamark test. Restart the agent or reconnect its MCP server to start the " +
		"seamark that is installed now.\n"
}

// whyText runs one why call and returns its text and isError.
func whyText(t *testing.T, srv *Server) (string, bool) {
	t.Helper()

	return toolText(t, callServer(t, srv, whyCall)["1"])
}

func TestBinaryWatchNotesAReplacedBinary(t *testing.T) {
	root := writeFixture(t)
	bin := filepath.Join(t.TempDir(), "bin", "seamark")
	start := time.Now().Add(-time.Hour)
	writeBinary(t, bin, "old", start)
	watchExecutable(t, bin, nil)

	var logs []string

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", func(format string, a ...any) {
		logs = append(logs, fmt.Sprintf(format, a...))
	})

	text, isErr := whyText(t, srv)
	require.False(t, isErr)
	assert.NotContains(t, text, "note: the seamark binary")

	plain := text

	// go build -o and make install write a new file and rename it onto
	// the old path.
	fresh := bin + ".new"
	writeBinary(t, fresh, "new", start)
	require.NoError(t, os.Rename(fresh, bin))

	for range 2 {
		text, isErr = whyText(t, srv)
		require.False(t, isErr)
		assert.Equal(t, changedNote(bin)+plain, text, "the note repeats until a restart")
	}

	changeLogs := 0

	for _, line := range logs {
		if strings.Contains(line, "changed after start") {
			changeLogs++
		}
	}

	assert.Equal(t, 1, changeLogs, "the log names the change once")
}

func TestBinaryWatchNotesAMovedHomebrewLink(t *testing.T) {
	root := writeFixture(t)
	prefix := t.TempDir()
	mtime := time.Now().Add(-time.Hour)

	// keg/2 is a hard link to keg/1, so both paths name one file with one
	// size and time. Only the target of the symlink differs. A copy would
	// fail os.SameFile, and the test would pass without the target compare.
	keg1 := filepath.Join(prefix, "keg", "1", "bin", "seamark")
	keg2 := filepath.Join(prefix, "keg", "2", "bin", "seamark")
	writeBinary(t, keg1, "same", mtime)
	require.NoError(t, os.MkdirAll(filepath.Dir(keg2), 0o755))
	require.NoError(t, os.Link(keg1, keg2))

	link := filepath.Join(prefix, "bin", "seamark")
	require.NoError(t, os.MkdirAll(filepath.Dir(link), 0o755))
	require.NoError(t, os.Symlink(keg1, link))
	watchExecutable(t, link, nil)

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)

	text, _ := whyText(t, srv)
	assert.NotContains(t, text, "note: the seamark binary")

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(keg2, link))

	text, _ = whyText(t, srv)
	assert.True(t, strings.HasPrefix(text, changedNote(link)), "text: %s", text)
}

// TestBinaryWatchNotesAChangeInPlace changes the file at the launch path
// and keeps its inode and target. Only the size or the time shows such a
// change, for example a write in place or a touch.
func TestBinaryWatchNotesAChangeInPlace(t *testing.T) {
	root := writeFixture(t)
	start := time.Now().Add(-time.Hour)

	cases := []struct {
		name   string
		change func(t *testing.T, bin string)
	}{
		// os.WriteFile truncates the existing file, so the inode stays.
		// writeBinary then sets the start time again, so only the size
		// differs.
		{name: "size", change: func(t *testing.T, bin string) { writeBinary(t, bin, "longer", start) }},
		{name: "time", change: func(t *testing.T, bin string) {
			later := start.Add(time.Minute)
			require.NoError(t, os.Chtimes(bin, later, later))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(t.TempDir(), "seamark")
			writeBinary(t, bin, "old", start)
			watchExecutable(t, bin, nil)

			srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)

			before, err := os.Stat(bin)
			require.NoError(t, err)

			tc.change(t, bin)

			after, err := os.Stat(bin)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after), "the change keeps the inode")

			text, _ := whyText(t, srv)
			assert.True(t, strings.HasPrefix(text, changedNote(bin)), "text: %s", text)
		})
	}
}

func TestBinaryWatchNotesARemovedBinary(t *testing.T) {
	root := writeFixture(t)
	bin := filepath.Join(t.TempDir(), "seamark")
	writeBinary(t, bin, "old", time.Now().Add(-time.Hour))
	watchExecutable(t, bin, nil)

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)

	require.NoError(t, os.Remove(bin))

	text, _ := whyText(t, srv)
	assert.True(t, strings.HasPrefix(text, removedNote(bin)), "text: %s", text)
}

func TestBinaryWatchStaysQuietWithoutAChange(t *testing.T) {
	root := writeFixture(t)
	bin := filepath.Join(t.TempDir(), "seamark")
	writeBinary(t, bin, "old", time.Now().Add(-time.Hour))
	watchExecutable(t, bin, nil)

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)

	for range 3 {
		text, _ := whyText(t, srv)
		assert.NotContains(t, text, "note: the seamark binary")
	}
}

// TestBinaryWatchTurnsOffWhenTheStartFails covers a start without a
// launch path and a start without a signature. The watch stays off for
// the session, no reply gets a note, and the log names the failure once.
func TestBinaryWatchTurnsOffWhenTheStartFails(t *testing.T) {
	root := writeFixture(t)

	cases := []struct {
		name string
		path string
		err  error
	}{
		{name: "no launch path", err: errors.New("no executable")},
		{name: "launch path missing", path: filepath.Join(t.TempDir(), "seamark")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			watchExecutable(t, tc.path, tc.err)

			var logs []string

			srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", func(format string, a ...any) {
				logs = append(logs, fmt.Sprintf(format, a...))
			})

			// EvalSymlinks("") gives "." with no error. A watch that goes on
			// after an os.Executable error follows the working directory,
			// and an unchanged reply does not show it. So the test reads
			// the watch.
			assert.Nil(t, srv.binary.start, "the watch is off for the session")

			for range 2 {
				text, _ := whyText(t, srv)
				assert.NotContains(t, text, "note: the seamark binary")
			}

			off := 0

			for _, line := range logs {
				if strings.HasPrefix(line, "mcp: binary watch off") {
					off++
				}
			}

			assert.Equal(t, 1, off, "the log names the failure once")
		})
	}
}

// TestBinaryWatchScrubsTheLaunchPath puts control characters in the
// launch path. The note and the log drop them, because a terminal can act
// on them.
func TestBinaryWatchScrubsTheLaunchPath(t *testing.T) {
	root := writeFixture(t)
	dir := t.TempDir()

	// The directory name holds ESC and BEL. gocritic reads the backslash
	// of an escape in a literal argument of filepath.Join as a separator,
	// so the name goes through a variable.
	unsafeDir := "b\x1b[31m\adir"
	bin := filepath.Join(dir, unsafeDir, "seamark")
	writeBinary(t, bin, "old", time.Now().Add(-time.Hour))
	watchExecutable(t, bin, nil)

	var logs []string

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", func(format string, a ...any) {
		logs = append(logs, fmt.Sprintf(format, a...))
	})

	require.NoError(t, os.Remove(bin))

	scrubbed := filepath.Join(dir, "b[31mdir", "seamark")

	text, _ := whyText(t, srv)
	assert.True(t, strings.HasPrefix(text, removedNote(scrubbed)), "text: %q", text)

	var changeLogs []string

	for _, line := range logs {
		if strings.Contains(line, "changed after start") {
			changeLogs = append(changeLogs, line)
		}
	}

	require.Len(t, changeLogs, 1)
	assert.Contains(t, changeLogs[0], scrubbed)
}

func TestBinaryWatchNoteOpensErrorsAndTheStructuredReport(t *testing.T) {
	root := writeFixture(t)
	bin := filepath.Join(t.TempDir(), "seamark")
	writeBinary(t, bin, "old", time.Now().Add(-time.Hour))
	watchExecutable(t, bin, nil)

	srv := New(root, filepath.Join(root, ".seamark", "index.db"), "test", t.Logf)
	callServer(t, srv, initializeLine("2025-06-18"))

	require.NoError(t, os.Remove(bin))

	resps := callServer(t, srv,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"why","arguments":{}}}`,
		changeSetCall(t, 2, "a.go"),
	)

	text, isErr := toolText(t, resps["1"])
	require.True(t, isErr)
	assert.Equal(t, removedNote(bin)+`why needs {"query": "<symbol or file>"}`, text)

	parts := decodeToolParts(t, resps["2"])
	assert.True(t, strings.HasPrefix(parts.Content[0].Text, removedNote(bin)))
	assert.Equal(t, parts.Content[0].Text, structuredReport(t, parts))
}
