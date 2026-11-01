package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/store"
)

// rootMoveFixture builds two real repositories: origin with its index,
// and other with another fingerprint. It returns the resolved origin,
// other, and the database of origin. Only the guard then stops a rebuild.
func rootMoveFixture(t *testing.T) (origin, other, dbPath string) {
	t.Helper()

	origin, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	other = writeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(other, "new.go"), []byte("package main\n"), 0o644))

	_, err = index.Run(index.Options{Root: origin})
	require.NoError(t, err)

	return origin, other, store.DefaultPath(origin)
}

// rootMoveCall returns one tools/call request.
func rootMoveCall(id int, tool, args string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, tool, args)
}

// rootMoveText joins the text parts of a tool result. change_set returns
// more than one part.
func rootMoveText(t *testing.T, r rpcResp) (string, bool) {
	t.Helper()

	require.Nil(t, r.Error)

	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}

	require.NoError(t, json.Unmarshal(r.Result, &res))

	var b strings.Builder

	for _, part := range res.Content {
		b.WriteString(part.Text)
	}

	return b.String(), res.IsError
}

// TestToolsKeepTheRootOfAnotherDatabase covers the refresh of every tool
// call under the database of another repository. The server has no flag
// that allows the move, so it does not rebuild, and each tool answers
// from the existing index. The log names the refusal and its route.
func TestToolsKeepTheRootOfAnotherDatabase(t *testing.T) {
	origin, other, dbPath := rootMoveFixture(t)

	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	var log strings.Builder

	logf := func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) }

	resps := serveDB(t, other, dbPath, logf,
		rootMoveCall(1, "why", `{"query":"helper"}`),
		rootMoveCall(2, "orient", `{}`),
		rootMoveCall(3, "change_set", `{"files":["a.go"]}`),
		rootMoveCall(4, "expand", `{"ref":"helper"}`),
	)

	for id := 1; id <= 4; id++ {
		text, isError := rootMoveText(t, resps[strconv.Itoa(id)])

		assert.False(t, isError, "tool %d answers from the existing index: %s", id, text)
		assert.True(t, strings.HasPrefix(text, staleNote), "tool %d opens with the note: %s", id, text)
		assert.Equal(t, 1, strings.Count(text, staleNote), "tool %d names it once", id)
	}

	why, _ := rootMoveText(t, resps["1"])
	assert.Contains(t, why, "helper", "the answer comes from the index of origin")

	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a tool call writes nothing to this database")

	recorded, err := store.ProbeRoot(dbPath)
	require.NoError(t, err)
	assert.Equal(t, origin, recorded)

	assert.Contains(t, log.String(), "this database records another repository ("+origin+")")
	assert.Contains(t, log.String(), "run `seamark index --db "+dbPath+"` in "+origin)
	assert.NotContains(t, log.String(), "mcp: index refreshed")
}

// TestToolsKeepAnOlderSchemaOfAnotherDatabase covers a file that a read
// cannot use without a migration. A migration is a write to another
// repository, so the tool refuses and its text names the route.
func TestToolsKeepAnOlderSchemaOfAnotherDatabase(t *testing.T) {
	origin, other, dbPath := rootMoveFixture(t)

	st, err := store.Open(dbPath)
	require.NoError(t, err)
	require.NoError(t, st.SetMeta(store.MetaSchemaVersion, strconv.Itoa(store.SupportedSchema()-1)))
	require.NoError(t, st.Close())

	before, err := os.ReadFile(dbPath)
	require.NoError(t, err)

	resps := serveDB(t, other, dbPath, t.Logf, rootMoveCall(1, "why", `{"query":"helper"}`))

	text, isError := rootMoveText(t, resps["1"])
	assert.True(t, isError)
	assert.Contains(t, text, "this database records another repository ("+origin+")")
	assert.Contains(t, text, "run `seamark index --db "+dbPath+"` in "+origin)

	after, err := os.ReadFile(dbPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the schema stays as it is")
}

// TestToolsNoteAnEmptyAnswerOfAnIndexTheyCannotRefresh covers a read that
// finds nothing. A symbol can be missing only because the index is old,
// so the error needs the note most of all.
func TestToolsNoteAnEmptyAnswerOfAnIndexTheyCannotRefresh(t *testing.T) {
	_, other, dbPath := rootMoveFixture(t)

	resps := serveDB(t, other, dbPath, t.Logf, rootMoveCall(1, "why", `{"query":"noSuchSymbol"}`))

	text, isError := rootMoveText(t, resps["1"])
	assert.True(t, isError)
	assert.True(t, strings.HasPrefix(text, staleNote), text)
	assert.Contains(t, text, "noSuchSymbol")
}

// TestResourcesNoteAnIndexTheyCannotRefresh covers the two resources.
// They read the same store, so they carry the same note.
func TestResourcesNoteAnIndexTheyCannotRefresh(t *testing.T) {
	_, other, dbPath := rootMoveFixture(t)

	for id, uri := range map[int]string{1: orientURI, 2: statusURI} {
		resps := serveDB(t, other, dbPath, t.Logf,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"resources/read","params":{"uri":%q}}`, id, uri))

		var res struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		}

		require.Nil(t, resps[strconv.Itoa(id)].Error)
		require.NoError(t, json.Unmarshal(resps[strconv.Itoa(id)].Result, &res))
		require.Len(t, res.Contents, 1)
		assert.True(t, strings.HasPrefix(res.Contents[0].Text, staleNote), uri)
	}
}

// rootMovePolicy denies a diff that can reach a process. It is the policy
// that a stale graph passes, because the graph lacks the new effect.
const rootMovePolicy = `mode: enforce
deny:
  - id: no-exec
    when: 'diff.effects.contains("proc:exec")'
    message: no process
`

// rootMoveLinked builds a repository with rootMovePolicy and a worktree
// of it. With linked, the default path of the worktree links to the
// database of the repository.
func rootMoveLinked(t *testing.T, linked bool) (origin, tree string) {
	t.Helper()

	origin, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(filepath.Join(origin, store.DefaultDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(origin, store.DefaultDir, "policy.yaml"), []byte(rootMovePolicy), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(origin, ".gitignore"), []byte(".seamark/*\n!.seamark/policy.yaml\n"), 0o644))
	rootMoveGit(t, origin, "add", "-A")
	rootMoveGit(t, origin, "commit", "-m", "policy")

	_, err = index.Run(index.Options{Root: origin})
	require.NoError(t, err)

	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	tree = filepath.Join(parent, "tree")
	rootMoveGit(t, origin, "worktree", "add", "-q", "-b", "side", tree)

	if linked {
		require.NoError(t, os.Symlink(store.DefaultPath(origin), store.DefaultPath(tree)))
	}

	return origin, tree
}

// rootMoveEdit adds a process to the function helper in root. The index
// already holds helper, so the changed lines keep their symbol.
func rootMoveEdit(t *testing.T, root string) {
	t.Helper()

	source := `package main

import "os/exec"

func main() { helper() }

// helper does the work.
func helper() { _ = exec.Command("ls").Run() }
`

	require.NoError(t, os.WriteFile(filepath.Join(root, "a.go"), []byte(source), 0o644))
}

// rootMoveGit runs git in dir with a fixed identity.
func rootMoveGit(t *testing.T, dir string, args ...string) {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")

	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v\n%s", args, out)
}

// TestCheckRefusesAnIndexItCannotRefresh covers a verdict from a stale
// graph. The changed lines keep their old symbol, so the graph still
// covers them and lacks the new effect. The tool answered allow under
// enforce where a current graph denies. A verdict needs a current graph,
// so the tool refuses, as `seamark check` does, and appends no audit line.
func TestCheckRefusesAnIndexItCannotRefresh(t *testing.T) {
	t.Run("a worktree linked to the database of main", func(t *testing.T) {
		origin, tree := rootMoveLinked(t, true)
		dbPath := store.DefaultPath(tree)

		rootMoveEdit(t, tree)

		before, err := os.ReadFile(store.DefaultPath(origin))
		require.NoError(t, err)

		resps := serveDB(t, tree, dbPath, t.Logf, rootMoveCall(1, "check", `{}`))

		text, isError := rootMoveText(t, resps["1"])
		assert.True(t, isError)
		assert.Contains(t, text, "this database records another repository ("+origin+")")
		assert.NotContains(t, text, "verdict")
		assert.NoFileExists(t, filepath.Join(tree, store.DefaultDir, "audit.jsonl"))

		after, err := os.ReadFile(store.DefaultPath(origin))
		require.NoError(t, err)
		assert.Equal(t, before, after)
	})

	t.Run("a worktree with its own index denies", func(t *testing.T) {
		_, tree := rootMoveLinked(t, false)

		rootMoveEdit(t, tree)

		resps := serveDB(t, tree, store.DefaultPath(tree), t.Logf, rootMoveCall(1, "check", `{}`))

		text, isError := rootMoveText(t, resps["1"])
		assert.False(t, isError, "a block stays in band")
		assert.Contains(t, text, "deny")
		assert.Contains(t, text, "proc:exec")
	})

	t.Run("a check at one commit keeps its verdict and needs no note", func(t *testing.T) {
		_, tree := rootMoveLinked(t, true)

		resps := serveDB(t, tree, store.DefaultPath(tree), t.Logf,
			rootMoveCall(1, "check", `{}`), rootMoveCall(2, "why", `{"query":"helper"}`))

		text, isError := rootMoveText(t, resps["1"])
		assert.False(t, isError, text)
		assert.Contains(t, text, "verdict")
		assert.NotContains(t, text, staleNote, "a skip is current")

		why, isError := rootMoveText(t, resps["2"])
		assert.False(t, isError, why)
		assert.NotContains(t, why, staleNote, "a skip is current")
	})
}

// TestToolsKeepRefreshingTheirOwnDatabase bounds the rule. A database
// that records this workspace refreshes on a tool call, as it always did.
func TestToolsKeepRefreshingTheirOwnDatabase(t *testing.T) {
	root, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	dbPath := filepath.Join(t.TempDir(), "own.db")

	var log strings.Builder

	logf := func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) }

	resps := serveDB(t, root, dbPath, logf, rootMoveCall(1, "why", `{"query":"helper"}`))

	text, isError := rootMoveText(t, resps["1"])
	assert.False(t, isError)
	assert.NotContains(t, text, staleNote, "only a refused refresh adds the note")
	assert.Contains(t, log.String(), "mcp: index refreshed")

	recorded, err := store.ProbeRoot(dbPath)
	require.NoError(t, err)
	assert.Equal(t, root, recorded)
}

// TestToolsKeepARootOnADetachedDisk covers a recorded root that is gone
// with a file outside this workspace. A skip keeps its answer with no
// note. After an edit a refresh would take the root, so it refuses, and
// the read carries the note.
func TestToolsKeepARootOnADetachedDisk(t *testing.T) {
	origin, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	shared := filepath.Join(t.TempDir(), "shared.db")

	_, err = index.Run(index.Options{Root: origin, DBPath: shared})
	require.NoError(t, err)

	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	sibling := filepath.Join(parent, "sibling")
	require.NoError(t, os.CopyFS(sibling, os.DirFS(origin)))
	require.Equal(t, index.WorkspaceState(origin), index.WorkspaceState(sibling))
	require.NoError(t, os.Rename(origin, filepath.Join(parent, "away")))

	resps := serveDB(t, sibling, shared, t.Logf, rootMoveCall(1, "orient", `{}`))

	text, isError := rootMoveText(t, resps["1"])
	assert.False(t, isError, text)
	assert.NotContains(t, text, staleNote, "a skip is current")

	require.NoError(t, os.WriteFile(filepath.Join(sibling, "b.go"), []byte("package main\n"), 0o644))

	resps = serveDB(t, sibling, shared, t.Logf, rootMoveCall(1, "orient", `{}`), rootMoveCall(2, "check", `{}`))

	text, isError = rootMoveText(t, resps["1"])
	assert.False(t, isError, text)
	assert.True(t, strings.HasPrefix(text, staleNote), text)

	_, isError = rootMoveText(t, resps["2"])
	assert.True(t, isError, "a verdict needs a current graph")

	recorded, err := store.ProbeRoot(shared)
	require.NoError(t, err)
	assert.Equal(t, origin, recorded)
}

// TestToolsNoteAFailedRefresh covers a refresh that fails for another
// reason. A read keeps its answer with its own note. The check tool
// answered allow under enforce where a current graph denies, so it stops
// with the failure, as `seamark check` does.
func TestToolsNoteAFailedRefresh(t *testing.T) {
	_, tree := rootMoveLinked(t, false)

	rootMoveEdit(t, tree)

	resps := serveDB(t, tree, store.DefaultPath(tree), t.Logf, rootMoveCall(1, "check", `{}`))

	text, isError := rootMoveText(t, resps["1"])
	require.False(t, isError, text)
	require.Contains(t, text, "deny", "a current graph denies this edit")

	require.NoError(t, os.WriteFile(filepath.Join(tree, store.DefaultDir, "config.yaml"), []byte("index: [\n"), 0o644))

	before, err := os.ReadFile(filepath.Join(tree, store.DefaultDir, "audit.jsonl"))
	require.NoError(t, err)

	resps = serveDB(t, tree, store.DefaultPath(tree), t.Logf,
		rootMoveCall(1, "check", `{}`), rootMoveCall(2, "why", `{"query":"helper"}`),
		rootMoveCall(3, "why", `{"query":"noSuchSymbol"}`))

	text, isError = rootMoveText(t, resps["1"])
	assert.True(t, isError, text)
	assert.NotContains(t, text, "verdict")
	assert.Contains(t, text, "config")

	after, err := os.ReadFile(filepath.Join(tree, store.DefaultDir, "audit.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "a refused check appends no audit line")

	text, isError = rootMoveText(t, resps["2"])
	assert.False(t, isError, text)
	assert.True(t, strings.HasPrefix(text, brokenNote), text)
	assert.NotContains(t, text, staleNote, "the reason differs")

	text, isError = rootMoveText(t, resps["3"])
	assert.True(t, isError)
	assert.True(t, strings.HasPrefix(text, brokenNote), text)
}

// TestToolsKeepAReadOnlyFileOfAMovedRepository covers a file that moved
// with its repository and refuses writes. A rebuild would fail on every
// tool call, so the skip stays and the answer needs no note.
func TestToolsKeepAReadOnlyFileOfAMovedRepository(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a file with mode 0444")
	}

	origin, err := filepath.EvalSymlinks(writeFixture(t))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(filepath.Join(origin, ".gitignore"), []byte(".seamark/\ncustom.db*\n"), 0o644))
	rootMoveGit(t, origin, "add", "-A")
	rootMoveGit(t, origin, "commit", "-m", "ignore")

	custom := filepath.Join(origin, "custom.db")

	_, err = index.Run(index.Options{Root: origin, DBPath: custom})
	require.NoError(t, err)

	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	moved := filepath.Join(parent, "moved")
	require.NoError(t, os.Rename(origin, moved))

	custom = filepath.Join(moved, "custom.db")
	require.NoError(t, os.Chmod(custom, 0o444))

	var log strings.Builder

	logf := func(format string, a ...any) { fmt.Fprintf(&log, format+"\n", a...) }

	resps := serveDB(t, moved, custom, logf, rootMoveCall(1, "why", `{"query":"helper"}`))

	text, isError := rootMoveText(t, resps["1"])
	assert.False(t, isError, text)
	assert.NotContains(t, text, "note:")
	assert.NotContains(t, log.String(), "index refresh")
}
