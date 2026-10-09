// Package servicestest holds the fixtures of the tests that run the
// production engine of internal/tui/services. The engine tests of that
// package and the console tests that drive a screen over the real
// engine share them, so both build the same workspace and the same
// fakes.
//
// Only two boundaries are fakes: the GitHub fetch (Fetch) and the agent
// invoker (Agent). Everything else is real: git, the index, the store
// and the configuration files under a temporary directory.
//
// The package does not import internal/tui/services, so the internal
// tests of that package can import it without an import cycle. Only
// tests import this package; the services guard test checks that the
// seamark binary does not.
package servicestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	domain "github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/settings"
	"github.com/seamark-dev/seamark/internal/store"
	"github.com/seamark-dev/seamark/internal/workspace"
)

// Now is the clock of the engine tests.
var Now = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// Origin is a GitHub origin, so a review refresh has a target.
const Origin = "git@github.com:o/r.git"

// Page is one gh page with one inline comment on a.go. It clusters
// into one review lesson.
const Page = `[{"id":11,"user":{"login":"rev","type":"User"},` +
	`"body":"Close the handle on every error path.","path":"a.go","line":3,` +
	`"html_url":"https://github.com/o/r/pull/1#discussion_r11","created_at":"2026-07-21T10:00:00Z",` +
	`"pull_request_url":"https://api.github.com/repos/o/r/pulls/1"}]`

// Git runs git in root with a fixed identity. Global config must not
// leak in, because it can rewrite remotes.
func Git(t *testing.T, root string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// Write writes a repository-relative file of ws.
func Write(t *testing.T, ws workspace.Workspace, rel, content string) {
	t.Helper()

	path := filepath.Join(ws.Root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// findings builds two review findings per directory with words that
// no other finding shares, so the grouper makes one group per
// directory. Directory i gets the IDs 21+2i and 22+2i.
func findings(dirs ...string) []domain.Finding {
	var out []domain.Finding

	for i, dir := range dirs {
		for j := range 2 {
			id := int64(21 + 2*i + j)
			out = append(out, domain.Finding{
				ID: id, Path: fmt.Sprintf("%s/f%d.go", dir, id), PR: int(id), Reviewer: "person",
				Body: fmt.Sprintf("Uniq%dalpha uniq%dbravo uniq%dcharlie.", id, id, id),
			})
		}
	}

	return out
}

// Workspace builds an indexed git workspace with one fix commit, the
// given origin, and two review findings per directory of dirs. Its
// config.yaml names an agent command that leaves the returned marker
// when it runs, so a test can prove that a read never runs the agent.
func Workspace(t *testing.T, origin string, dirs ...string) (ws workspace.Workspace, marker string) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/eng\n\ngo 1.25\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package eng\n\n// A returns one.\nfunc A() int { return 1 }\n"), 0o644))

	Git(t, root, "init", "-b", "main")
	Git(t, root, "add", "-A")
	Git(t, root, "commit", "-m", "initial")

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"),
		[]byte("package eng\n\n// A returns one.\nfunc A() int { return 1 }\n\nfunc resetState() {}\n"), 0o644))
	Git(t, root, "add", "-A")
	Git(t, root, "commit", "-m", "fix: reset state before reuse (#42)")

	if origin != "" {
		Git(t, root, "remote", "add", "origin", origin)
	}

	ws, err := workspace.Resolve(root, "")
	require.NoError(t, err)

	_, err = index.Run(index.Options{Root: ws.Root, DBPath: ws.DBPath})
	require.NoError(t, err)

	st, err := store.Open(ws.DBPath)
	require.NoError(t, err)
	require.NoError(t, st.ReplaceLessons(nil, findings(dirs...)))
	require.NoError(t, st.Close())

	marker = filepath.Join(t.TempDir(), "agent-ran")
	Write(t, ws, settings.File, fmt.Sprintf(
		"agent:\n  argv: [\"sh\", \"-c\", \"touch %s; cat >/dev/null; echo '{\\\"patterns\\\":[]}'\"]\n", marker))

	return ws, marker
}

// Tree returns the SHA-256 of every file under root, the git directory
// included. SQLite may create the -wal and -shm side files of a
// read-only open, so the tree leaves them out; the database file itself
// stays in.
func Tree(t *testing.T, root string) map[string]string {
	t.Helper()

	tree := map[string]string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		if strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		sum := sha256.Sum256(data)
		tree[path] = hex.EncodeToString(sum[:])

		return nil
	})
	require.NoError(t, err)

	return tree
}

// Fetch is a scripted GitHub fetch that counts its calls. Its Fetch
// method is a reviews.FetchFunc.
type Fetch struct {
	mu    sync.Mutex
	calls []string
	// Raw is the page that every call returns.
	Raw string
	// Err fails every call when it is set.
	Err error
	// During runs inside the fetch, while the refresh holds the lock.
	During func()
}

// Fetch records the call and returns Raw, or Err when it is set.
func (f *Fetch) Fetch(_ context.Context, owner, repo string, _ reviews.Options) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, owner+"/"+repo)
	f.mu.Unlock()

	if f.During != nil {
		f.During()
	}

	if f.Err != nil {
		return nil, f.Err
	}

	return []byte(f.Raw), nil
}

// Called returns the repositories of the calls so far, as "owner/repo".
func (f *Fetch) Called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.calls...)
}

// Agent is a scripted invoker that counts its calls. By default it
// answers with one proposal that cites the finding IDs of the prompt.
type Agent struct {
	mu    sync.Mutex
	calls int
	// Fn answers in place of the default reply when it is set.
	Fn func(ctx context.Context, prompt string) (string, error)
}

// Invoke records the call and answers with Fn, or with Reply.
func (a *Agent) Invoke(ctx context.Context, prompt string) (string, error) {
	a.mu.Lock()
	a.calls++
	a.mu.Unlock()

	if a.Fn != nil {
		return a.Fn(ctx, prompt)
	}

	return Reply(prompt), nil
}

// Name names the fake in the agent column of a run.
func (a *Agent) Name() string { return "fake" }

// Count returns the number of calls so far.
func (a *Agent) Count() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	return a.calls
}

// promptID finds the finding IDs of a prompt.
var promptID = regexp.MustCompile(`finding id=(\d+) `)

// lessons are the distinct replies per group, keyed by the first
// finding ID. Similar words would make the deduplication drop a group.
var lessons = map[int]string{
	21: `"rule":"close-db-handles","note":"Close the database handle on every error path."`,
	23: `"rule":"verify-webhook-signatures","note":"Verify webhook signatures before parsing payloads."`,
}

// Reply answers a prompt with one proposal that cites every finding of
// the prompt, so the store keeps one pending proposal per group.
func Reply(prompt string) string {
	var ids []string

	for _, m := range promptID.FindAllStringSubmatch(prompt, -1) {
		ids = append(ids, m[1])
	}

	first, _ := strconv.Atoi(ids[0])

	return fmt.Sprintf(`{"patterns":[{%s,"finding_ids":[%s],"trigger_paths":[]}]}`,
		lessons[first], strings.Join(ids, ","))
}
