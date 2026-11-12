package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// client drives a Server over in-memory pipes with real LSP framing.
type client struct {
	t      *testing.T
	toSrv  io.WriteCloser
	from   *bufio.Reader
	nextID int
	done   chan error
}

func startServer(t *testing.T, root string) *client {
	t.Helper()

	return startServerWith(t, Options{Root: root, Version: "test", Logf: t.Logf})
}

// startServerWith is startServer with all options of the server.
func startServerWith(t *testing.T, opts Options) *client {
	t.Helper()

	clientIn, srvOut := io.Pipe()
	srvIn, clientOut := io.Pipe()

	c := &client{
		t:     t,
		toSrv: clientOut,
		from:  bufio.NewReader(clientIn),
		done:  make(chan error, 1),
	}

	go func() {
		c.done <- Serve(opts, srvIn, srvOut)
	}()

	t.Cleanup(func() {
		// Full lifecycle: shutdown must answer with an explicit null
		// result (a response with neither result nor error is invalid
		// JSON-RPC and hangs real clients), then exit terminates.
		result := c.request("shutdown", nil)
		assert.Equal(t, "null", string(result), "shutdown result must be literal null")

		c.notify("exit", nil)
		_ = c.toSrv.Close()

		select {
		case err := <-c.done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("server did not exit")
		}
	})

	return c
}

func (c *client) send(msg map[string]any) {
	c.t.Helper()

	body, err := json.Marshal(msg)
	require.NoError(c.t, err)
	_, err = fmt.Fprintf(c.toSrv, "Content-Length: %d\r\n\r\n%s", len(body), body)
	require.NoError(c.t, err)
}

func (c *client) notify(method string, params any) {
	c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

// request sends a request and returns the raw result, skipping any
// notifications that arrive first.
func (c *client) request(method string, params any) json.RawMessage {
	c.t.Helper()

	c.nextID++
	id := c.nextID

	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(c.t, err)

	// The pipe has no buffer. A server that publishes a notification stops
	// reading until the client reads it, so this write must not stop the
	// read below. Without that, a test deadlocks instead of failing.
	written := make(chan error, 1)

	go func() {
		_, err := fmt.Fprintf(c.toSrv, "Content-Length: %d\r\n\r\n%s", len(body), body)
		written <- err
	}()

	for {
		msg := c.readMessage()
		if int(msg.ID) == id {
			require.NoError(c.t, <-written)
			require.Nil(c.t, msg.Error, "request %s failed: %+v", method, msg.Error)

			return msg.Result
		}
	}
}

// awaitNotification reads messages until one with the wanted method arrives.
func (c *client) awaitNotification(method string) json.RawMessage {
	c.t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		msg := c.readMessage()
		if msg.Method == method {
			return msg.Params
		}
	}

	c.t.Fatalf("no %s notification arrived", method)

	return nil
}

type inMessage struct {
	ID     float64         `json:"id"`
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Params json.RawMessage `json:"params"`
	Error  *rpcError       `json:"error"`
}

func (c *client) readMessage() inMessage {
	c.t.Helper()

	length := -1

	for {
		line, err := c.from.ReadString('\n')
		require.NoError(c.t, err)

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}

		if name, v, ok := strings.Cut(line, ":"); ok &&
			strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			require.NoError(c.t, err)
			length = n
		}
	}

	require.GreaterOrEqual(c.t, length, 0)

	body := make([]byte, length)
	_, err := io.ReadFull(c.from, body)
	require.NoError(c.t, err)

	var msg inMessage
	require.NoError(c.t, json.Unmarshal(body, &msg))

	return msg
}

// fixtureRepo builds a git-backed workspace whose files a.go and b.go have
// strong co-change history, then leaves a.go modified in the working tree.
func fixtureRepo(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()

		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "git %v\n%s", args, out)
	}

	write := func(rel, content string) {
		t.Helper()

		p := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	write("go.mod", "module example.com/fix\n")
	write("a.go", "package main\n\nfunc main() { helper() }\n\n// helper does the work.\nfunc helper() {}\n")
	write("b.go", "package main\n\nfunc other() {}\n")
	// Shape exists in BOTH families: cross-language name mirror.
	write("shape.go", "package main\n\ntype Shape struct{}\n\nfunc useShape(s Shape) {}\n")
	write("shape.ts", "export class Shape {}\n")
	// version is a same-file const AND a distant function: the const wins.
	write("vers.go", "package main\n\nconst version = \"1\"\n\nfunc useVersion() string { return version }\n")
	write("util/util.go", "package util\n\nfunc version() string { return \"u\" }\n")
	git("init", "-b", "main")
	git("add", "-A")
	git("commit", "-m", "initial layout")

	for i, msg := range []string{"feat: grow together", "fix: again together", "fix: third time"} {
		write("a.go", fmt.Sprintf("package main\n\nfunc main() { helper() }\n\n// helper does the work.\nfunc helper() {} // rev %d\n", i))
		write("b.go", fmt.Sprintf("package main\n\nfunc other() {} // rev %d\n", i))
		git("add", "-A")
		git("commit", "-m", msg)
	}

	// Independent churn pushes the (a,b) lift above chance level: with
	// every commit touching both files lift is exactly 1.0 and the
	// coupling would (correctly) be dismissed as noise.
	for i := range 5 {
		write("c.go", fmt.Sprintf("package main\n\nfunc lone() {} // rev %d\n", i))
		git("add", "-A")
		git("commit", "-m", fmt.Sprintf("chore: unrelated %d", i))
	}

	// Leave a.go modified so a save is "a change in progress" that does
	// not touch its partner b.go.
	write("a.go", "package main\n\nfunc main() { helper() }\n\n// helper does the work.\nfunc helper() {} // wip\n")

	return root
}

func uri(root, rel string) string {
	return "file://" + filepath.Join(root, rel)
}

func TestLifecycleHoverAndCodeLens(t *testing.T) {
	root := fixtureRepo(t)
	c := startServer(t, root)

	init := c.request("initialize", map[string]any{"rootUri": "file://" + root})
	assert.Contains(t, string(init), `"hoverProvider":true`)
	assert.Contains(t, string(init), `"seamark"`)
	c.notify("initialized", map[string]any{})

	// Hover over helper's declaration (0-based line 5).
	hover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
		"position":     map[string]any{"line": 5, "character": 6},
	})
	for _, want := range []string{"func helper()", "1 callers", "b.go", "Recent decisions"} {
		assert.Contains(t, string(hover), want, "hover markdown")
	}

	// Hover on a CALL SITE describes the callee, not the enclosing
	// function: line 2 is "func main() { helper() }", character 14 is
	// inside "helper".
	callSite := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
		"position":     map[string]any{"line": 2, "character": 14},
	})
	assert.Contains(t, string(callSite), "func helper()", "call-site hover resolves the callee")
	assert.Contains(t, string(callSite), "1 callers")

	// Cross-language mirrors resolve within the hovered file's family:
	// "Shape" exists in Go and TS; hovering its usage in shape.go (line 4
	// "func useShape(s Shape) {}", char 18) must pick the Go type, not
	// bail out as ambiguous.
	mirror := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "shape.go")},
		"position":     map[string]any{"line": 4, "character": 18},
	})
	assert.Contains(t, string(mirror), "Shape struct{}", "the Go mirror wins in a Go file")
	assert.Contains(t, string(mirror), "**type**")

	// A same-file const must win over a distant same-named function:
	// hovering "version" inside useVersion (line 4, char 36 of vers.go).
	verHover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "vers.go")},
		"position":     map[string]any{"line": 4, "character": 36},
	})
	assert.Contains(t, string(verHover), `version = \"1\"`, "the local const's card")
	assert.NotContains(t, string(verHover), "util", "never the cross-file function")

	// Hover on an empty line yields null, not an error.
	empty := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
		"position":     map[string]any{"line": 1, "character": 0},
	})
	assert.Equal(t, "null", string(empty))

	lenses := c.request("textDocument/codeLens", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
	})
	assert.Contains(t, string(lenses), "1 callers", "helper has one caller")
	assert.Contains(t, string(lenses), "usually changed with", "file-level coupling lens")
}

func TestDidSavePublishesCoChangeDiagnostics(t *testing.T) {
	root := fixtureRepo(t)
	c := startServer(t, root)

	c.request("initialize", map[string]any{"rootUri": "file://" + root})
	c.notify("initialized", map[string]any{})

	c.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
	})

	params := c.awaitNotification("textDocument/publishDiagnostics")
	assert.Contains(t, string(params), "b.go", "the unmodified partner is named")
	assert.Contains(t, string(params), "usually changed together")
	assert.Contains(t, string(params), `"severity":3`)
	assert.Equal(t, 1, strings.Count(string(params), `"severity"`),
		"partners merge into ONE diagnostic; stacked ones fight over the virtual-text slot")

	// Committing everything makes the next save quiet: diagnostics clear.
	git := exec.Command("git", "commit", "-am", "done")
	git.Dir = root
	git.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := git.CombinedOutput()
	require.NoError(t, err, "%s", out)

	c.notify("textDocument/didSave", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
	})

	params = c.awaitNotification("textDocument/publishDiagnostics")
	assert.Contains(t, string(params), `"diagnostics":[]`, "clean tree clears diagnostics")
}

func TestSubdirectoryWorkspaceServesRepoIndex(t *testing.T) {
	root := fixtureRepo(t)

	// A package inside a subdirectory, as a monorepo sub-project workspace.
	sub := filepath.Join(root, "svc")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "svc.go"),
		[]byte("package svc\n\n// Run runs.\nfunc Run() { Run() }\n"), 0o644))

	// The server is started on the SUBDIRECTORY — it must widen to the
	// repo root, not create a stray empty index inside svc/.
	c := startServer(t, sub)

	c.request("initialize", map[string]any{"rootUri": "file://" + sub})
	c.notify("initialized", map[string]any{})

	hover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(root, "a.go")},
		"position":     map[string]any{"line": 5, "character": 6},
	})
	assert.Contains(t, string(hover), "func helper()",
		"subdirectory workspace must serve the repo's real index")

	assert.NoDirExists(t, filepath.Join(sub, ".seamark"),
		"no stray index may be created in the subdirectory")
}

func TestSymlinkedRootMatchesResolvedURIs(t *testing.T) {
	root := fixtureRepo(t)

	// The editor may resolve symlinks in URIs while the server was
	// started through an alias (macOS /tmp vs /private/tmp).
	link := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(root, link))

	resolved, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	c := startServer(t, link)
	c.request("initialize", map[string]any{"rootUri": "file://" + link})

	hover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": "file://" + filepath.Join(resolved, "a.go")},
		"position":     map[string]any{"line": 5, "character": 6},
	})
	assert.Contains(t, string(hover), "func helper()",
		"resolved URIs must match a symlinked root")
}

func TestOversizedContentLengthRejected(t *testing.T) {
	c := newConn(strings.NewReader("Content-Length: 999999999999\r\n\r\n"), io.Discard)

	_, err := c.read()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds", "hostile length must error, not allocate")
}

func TestUnknownMethodAndOutsideURI(t *testing.T) {
	root := fixtureRepo(t)
	c := startServer(t, root)

	c.request("initialize", map[string]any{"rootUri": "file://" + root})

	// Unknown request → MethodNotFound, server keeps running.
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": "textDocument/definition",
		"params": map[string]any{}})

	msg := c.readMessage()
	require.NotNil(t, msg.Error)
	assert.Equal(t, codeMethodNotFound, msg.Error.Code)

	// URI outside the workspace → clean error, not a crash.
	c.nextID++
	c.send(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": "textDocument/hover",
		"params": map[string]any{
			"textDocument": map[string]any{"uri": "file:///etc/passwd"},
			"position":     map[string]any{"line": 0, "character": 0},
		}})

	msg = c.readMessage()
	require.NotNil(t, msg.Error)
	assert.Contains(t, msg.Error.Message, "outside the workspace")
}

// TestDidSaveKeepsTheRootOfAnotherDatabase covers a server under the
// database of another repository. A save does not rebuild there, because
// the server has no flag that allows the move. Reads keep their answers.
func TestDidSaveKeepsTheRootOfAnotherDatabase(t *testing.T) {
	origin, other := fixtureRepo(t), fixtureRepo(t)
	dbPath := store.DefaultPath(origin)

	_, err := index.Run(index.Options{Root: origin})
	require.NoError(t, err)

	// A new file gives the workspace another fingerprint, so only the
	// guard stops the rebuild.
	require.NoError(t, os.WriteFile(filepath.Join(other, "new.go"), []byte("package main\n"), 0o644))

	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	resolved, err := filepath.EvalSymlinks(origin)
	require.NoError(t, err)

	c := startServerWith(t, Options{Root: other, DBPath: dbPath, Version: "test", Logf: t.Logf})

	c.request("initialize", map[string]any{"rootUri": "file://" + other})
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri(other, "a.go")}})

	hover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(other, "a.go")},
		"position":     map[string]any{"line": 5, "character": 6},
	})
	assert.Contains(t, string(hover), "func helper()", "the reads keep working")

	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the save writes nothing")

	recorded, err := store.ProbeRoot(dbPath)
	require.NoError(t, err)
	assert.Equal(t, resolved, recorded)
}

// TestDidSaveKeepsARootOnADetachedDisk covers a recorded root that is gone
// with a file outside this workspace. A stat cannot tell a detached disk
// from a moved repository, so a save does not take the root.
func TestDidSaveKeepsARootOnADetachedDisk(t *testing.T) {
	origin, other := fixtureRepo(t), fixtureRepo(t)
	dbPath := filepath.Join(t.TempDir(), "shared.db")

	resolved, err := filepath.EvalSymlinks(origin)
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: origin, DBPath: dbPath})
	require.NoError(t, err)
	require.NoError(t, os.Rename(origin, origin+".away"))

	c := startServerWith(t, Options{Root: other, DBPath: dbPath, Version: "test", Logf: t.Logf})

	c.request("initialize", map[string]any{"rootUri": "file://" + other})
	c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri(other, "a.go")}})

	hover := c.request("textDocument/hover", map[string]any{
		"textDocument": map[string]any{"uri": uri(other, "a.go")},
		"position":     map[string]any{"line": 5, "character": 6},
	})
	assert.Contains(t, string(hover), "func helper()", "the reads keep working")

	recorded, err := store.ProbeRoot(dbPath)
	require.NoError(t, err)
	assert.Equal(t, resolved, recorded)
}

func TestModifiedFilesIgnoresHiddenUntrackedConfig(t *testing.T) {
	// status.showUntrackedFiles=no drops new files from git status. A saved
	// new file must still read as modified, so it gets diagnostics.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := fixtureRepo(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "new.go"), []byte("package main\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "pkg", "fresh.go"), []byte("package pkg\n"), 0o644))

	cmd := exec.Command("git", "config", "status.showUntrackedFiles", "no")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)

	modified, err := (&Server{root: root}).modifiedFiles()
	require.NoError(t, err)

	assert.True(t, modified("new.go"), "an untracked file")
	assert.True(t, modified("pkg/fresh.go"), "a file under an untracked directory")
	assert.True(t, modified("a.go"), "a modified tracked file")
	assert.False(t, modified("b.go"), "an unchanged tracked file")
}

func TestModifiedFilesKeepsGitIndex(t *testing.T) {
	// The server reads git status on each save. A touch changes no
	// content, so the read must not rewrite .git/index and take
	// .git/index.lock away from a concurrent git command of the user.
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "GIT_OPTIONAL_LOCKS" || strings.HasPrefix(k, "GIT_CONFIG_") {
			t.Setenv(k, "")
			require.NoError(t, os.Unsetenv(k))
		}
	}

	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	root := fixtureRepo(t)
	gitIndex := filepath.Join(root, ".git", "index")

	at := time.Now().Add(-2 * time.Hour)
	require.NoError(t, os.Chtimes(filepath.Join(root, "b.go"), at, at))

	beforeInfo, err := os.Stat(gitIndex)
	require.NoError(t, err)

	beforeBytes, err := os.ReadFile(gitIndex)
	require.NoError(t, err)

	modified, err := (&Server{root: root}).modifiedFiles()
	require.NoError(t, err)

	assert.True(t, modified("a.go"), "a modified tracked file")
	assert.False(t, modified("b.go"), "a touched tracked file has no change")

	afterInfo, err := os.Stat(gitIndex)
	require.NoError(t, err)

	afterBytes, err := os.ReadFile(gitIndex)
	require.NoError(t, err)

	assert.True(t, os.SameFile(beforeInfo, afterInfo), ".git/index has a new inode")
	assert.Equal(t, beforeInfo.ModTime(), afterInfo.ModTime(), ".git/index has a new mtime")
	assert.Equal(t, beforeBytes, afterBytes, ".git/index has new bytes")
}
