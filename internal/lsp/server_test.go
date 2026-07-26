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

	clientIn, srvOut := io.Pipe()
	srvIn, clientOut := io.Pipe()

	c := &client{
		t:     t,
		toSrv: clientOut,
		from:  bufio.NewReader(clientIn),
		done:  make(chan error, 1),
	}

	go func() {
		c.done <- Serve(Options{Root: root, Version: "test", Logf: t.Logf}, srvIn, srvOut)
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
	c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})

	for {
		msg := c.readMessage()
		if int(msg.ID) == id {
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
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}

	write("go.mod", "module example.com/fix\n")
	write("a.go", "package main\n\nfunc main() { helper() }\n\n// helper does the work.\nfunc helper() {}\n")
	write("b.go", "package main\n\nfunc other() {}\n")
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
