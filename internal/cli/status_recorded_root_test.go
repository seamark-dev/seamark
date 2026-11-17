package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration/inspecttest"
	"github.com/seamark-dev/seamark/internal/store"
)

// recordedRootOrigin builds a git repository with an enforcing gate
// hook and an index, as `seamark init --gate-mode enforce` and
// `seamark index` leave it.
func recordedRootOrigin(t *testing.T) string {
	t.Helper()

	origin := rootWriteTree(t)
	gitify(t, origin)

	var b testWriter

	require.NoError(t, runInit(&b, origin, inspecttest.Binary, gateModeEnforce, false, "", false))

	_, err := run(t, "-C", origin, "index")
	require.NoError(t, err)

	return origin
}

// statusJSONKeys returns the top-level object of `status --json` that
// runs with the global flags.
func statusJSONKeys(t *testing.T, flags ...string) map[string]any {
	t.Helper()

	out, err := run(t, append(flags, "status", "--json")...)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &doc))

	return doc
}

func TestStatusNamesTheRootThatItsIndexRecords(t *testing.T) {
	// Status reads the root that the index records. In a copied or a
	// moved checkout that is not the workspace, so the first line names
	// that root and the index run that records this workspace.
	t.Run("copied checkout", func(t *testing.T) {
		origin := recordedRootOrigin(t)
		copied := rootWriteCopy(t, origin)

		require.NoError(t, os.Remove(filepath.Join(copied, ".claude", "settings.json")))

		policy := filepath.Join(copied, ".seamark", "policy.yaml")
		data, err := os.ReadFile(policy)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(policy, []byte(strings.Replace(string(data), "mode: enforce", "mode: warn", 1)), 0o644))

		before := files(t, copied)

		out, err := run(t, "-C", copied, "status")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(out, "root           the index of this checkout records another repository ("+origin+
			"), so this report describes "+origin+", not this workspace; run `seamark index` in "+copied+" first\n"), out)
		assert.Equal(t, origin, statusJSONKeys(t, "-C", copied)["recorded_root"])
		assert.Equal(t, before, files(t, copied), "status writes nothing")

		// The route runs and clears the line: status then reads the copy.
		_, err = run(t, "-C", copied, "index")
		require.NoError(t, err)

		out, err = run(t, "-C", copied, "status")
		require.NoError(t, err)
		assert.NotContains(t, out, "root   ")
		assert.Contains(t, out, "gate           policy mode warn; no gate hook installed")
		assert.NotContains(t, statusJSONKeys(t, "-C", copied), "recorded_root")
	})

	t.Run("moved checkout", func(t *testing.T) {
		origin := recordedRootOrigin(t)
		moved := filepath.Join(rootWriteDir(t), "moved")
		require.NoError(t, os.Rename(origin, moved))

		out, err := run(t, "-C", moved, "status")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(out, "root           the index of this checkout records a directory that no longer exists ("+
			origin+"), so this report describes "+origin+", not this workspace; run `seamark index` in "+moved+" first\n"), out)

		_, err = run(t, "-C", moved, "index")
		require.NoError(t, err)

		out, err = run(t, "-C", moved, "status")
		require.NoError(t, err)
		assert.NotContains(t, out, "root   ")
		assert.Contains(t, out, "gate           enforce (claude hook carries --enforce")
	})

	t.Run("the recorded workspace and a link to it", func(t *testing.T) {
		origin := recordedRootOrigin(t)
		link := filepath.Join(rootWriteDir(t), "link")
		require.NoError(t, os.Symlink(origin, link))

		for _, root := range []string{origin, link} {
			out, err := run(t, "-C", root, "status")
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(out, "workspace      "), out)
			assert.NotContains(t, statusJSONKeys(t, "-C", root), "recorded_root")
		}
	})

	// A command in a directory inside the recorded root runs in that
	// repository, inside git and outside it. So --db with that directory
	// prints no line.
	t.Run("a directory inside the recorded root", func(t *testing.T) {
		for name, root := range map[string]string{"outside git": rootWritePlainRoot(t), "in git": rootWriteRepo(t)} {
			inside := filepath.Join(root, "pkg")
			require.NoError(t, os.MkdirAll(inside, 0o755))

			flags := []string{"-C", inside, "--db", store.DefaultPath(root)}

			out, err := run(t, append(flags, "status")...)
			require.NoError(t, err, name)
			assert.True(t, strings.HasPrefix(out, "workspace      "), "%s: %s", name, out)
			assert.NotContains(t, statusJSONKeys(t, flags...), "recorded_root", name)
		}
	})

	// The recorded root comes from the database. A control byte in it
	// must not reach the terminal or the JSON.
	t.Run("a recorded root with control bytes", func(t *testing.T) {
		origin := rootWriteRepo(t)
		copied := rootWriteCopy(t, origin)
		clean := origin + "[31m-x -y"

		writeIndexDB(t, store.DefaultPath(copied), map[string]string{store.MetaRepoRoot: origin + "\x1b[31m-x\n-y"})

		out, err := run(t, "-C", copied, "status")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(out, "root           the index of this checkout records a directory that no longer exists ("+
			clean+"), so this report describes "+clean+", not this workspace; run `seamark index` in "+copied+
			" first\nworkspace      "), out)
		assert.NotContains(t, out, "\x1b")
		assert.Equal(t, clean, statusJSONKeys(t, "-C", copied)["recorded_root"])
	})
}

// statusRootLine returns the first line of `status` that runs with the
// global flags.
func statusRootLine(t *testing.T, flags ...string) string {
	t.Helper()

	out, err := run(t, append(flags, "status")...)
	require.NoError(t, err)

	line, _, _ := strings.Cut(out, "\n")

	return line
}

func TestStatusRootLineFollowsTheRuleOfTheDatabase(t *testing.T) {
	// With --db the subject is "this database". The line names only a
	// route that makes the index record this workspace. That route runs
	// and clears the line. A route into the recorded root prints the same
	// report again, so the line then names no route.
	describes := func(origin string) string {
		return "(" + origin + "), so this report describes " + origin + ", not this workspace"
	}

	t.Run("a custom database of a copy", func(t *testing.T) {
		origin, copied, custom := rootWriteCustomCopy(t)
		flags := []string{"-C", copied, "--db", custom}

		assert.Equal(t, "root           this database records another repository "+describes(origin)+
			"; run `seamark index --move-root --db "+custom+"` in "+copied+" first", statusRootLine(t, flags...))

		_, err := run(t, "-C", copied, "index", "--move-root", "--db", custom)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(statusRootLine(t, flags...), "workspace      "))
	})

	t.Run("a custom database of a moved checkout", func(t *testing.T) {
		origin := rootWriteRepo(t)
		moved := filepath.Join(rootWriteDir(t), "moved")
		custom := filepath.Join(moved, "custom.db")

		require.NoError(t, os.Rename(origin, moved))
		require.NoError(t, os.Rename(store.DefaultPath(moved), custom))

		flags := []string{"-C", moved, "--db", custom}

		assert.Equal(t, "root           this database records a directory that no longer exists "+describes(origin)+
			"; run `seamark index --db "+custom+"` in "+moved+" first", statusRootLine(t, flags...))

		_, err := run(t, append(flags, "index")...)
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(statusRootLine(t, flags...), "workspace      "))
	})

	t.Run("the database of the origin from a copy", func(t *testing.T) {
		origin := rootWriteRepo(t)
		copied := rootWriteCopy(t, origin)

		assert.Equal(t, "root           this database records another repository "+describes(origin),
			statusRootLine(t, "-C", copied, "--db", store.DefaultPath(origin)))
	})

	// With --db, status keeps -C as the workspace and does not widen it
	// to the git top level. The rule then reads the copy as a third
	// checkout that owns the file, so the line names no route.
	t.Run("the database of a copy from a directory inside it", func(t *testing.T) {
		origin := rootWriteRepo(t)
		copied := rootWriteCopy(t, origin)
		inside := filepath.Join(copied, "pkg")

		require.NoError(t, os.MkdirAll(inside, 0o755))

		assert.Equal(t, "root           this database records another repository "+describes(origin),
			statusRootLine(t, "-C", inside, "--db", store.DefaultPath(copied)))
	})
}

// gitCalls puts a git script first on PATH. The script appends the
// arguments of each call to the returned file and runs the real git.
func gitCalls(t *testing.T) string {
	t.Helper()

	gitPath, err := exec.LookPath("git")
	require.NoError(t, err)

	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(calls) + "\nexec " + shellQuote(gitPath) + " \"$@\"\n"

	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	return calls
}

func TestStatusResolvesTheWorkspaceOnce(t *testing.T) {
	// Status runs often. The root line compares the recorded root with
	// the workspace that opened the index, because each resolution of
	// the workspace starts a git process.
	origin := recordedRootOrigin(t)
	calls := gitCalls(t)

	out, err := run(t, "-C", origin, "status")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(out, "workspace      "), out)

	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(string(data), "rev-parse --show-toplevel\n"), string(data))
}

// mcpStatusText reads the resource seamark://status through
// `seamark mcp` that runs with the global flags.
func mcpStatusText(t *testing.T, flags ...string) string {
	t.Helper()

	var out, errOut bytes.Buffer

	cmd := New()
	cmd.SetIn(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"seamark://status"}}` + "\n"))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(append(flags, "mcp"))
	require.NoError(t, cmd.Execute(), "stderr: %s", errOut.String())

	var resp struct {
		Result struct {
			Contents []struct {
				Text string `json:"text"`
			} `json:"contents"`
		} `json:"result"`
	}

	require.NoError(t, json.Unmarshal(out.Bytes(), &resp), out.String())
	require.Len(t, resp.Result.Contents, 1, out.String())

	return resp.Result.Contents[0].Text
}

// statusLines returns the root lines and the workspace lines of a status
// text, in their order.
func statusLines(text string) []string {
	var lines []string

	for line := range strings.SplitSeq(text, "\n") {
		if strings.HasPrefix(line, "root ") || strings.HasPrefix(line, "workspace ") {
			lines = append(lines, line)
		}
	}

	return lines
}

// TestStatusRootAndWorkspaceLinesAreTheSameOverMCP covers a copy that
// reads the database of its origin, through both surfaces. The MCP status
// resource prints the root line and the workspace line of seamark status.
// Both compare the index with the origin. An edit in the copy alone
// leaves the line current, because the origin still matches the index.
// An edit in the origin as well makes the line stale, and the route of
// the line makes it current again. An edit in the origin alone differs
// (see the test after this one), and so does an origin that is gone.
func TestStatusRootAndWorkspaceLinesAreTheSameOverMCP(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	flags := []string{"-C", copied, "--db", store.DefaultPath(origin)}

	require.Equal(t, index.WorkspaceState(origin, ""), index.WorkspaceState(copied, ""), "the copy starts at one fingerprint")

	schema := " (schema v" + strconv.Itoa(store.SupportedSchema()) + ")"
	root := "root           this database records another repository (" + origin + "), so this report describes " +
		origin + ", not this workspace"
	current := "workspace      current" + schema
	stale := "workspace      STALE — run `seamark index` in " + origin + schema

	for _, step := range []struct {
		name string
		edit func()
		want string
	}{
		{"a matching fingerprint", func() {}, current},
		{"an edit in the copy", func() {
			require.NoError(t, os.WriteFile(filepath.Join(copied, "b.go"), []byte("package main\n"), 0o644))
		}, current},
		{"an edit in the origin", func() {
			require.NoError(t, os.WriteFile(filepath.Join(origin, "c.go"), []byte("package main\n\nfunc c() {}\n"), 0o644))
		}, stale},
		{"the route of the stale line", func() {
			_, err := run(t, "-C", origin, "index")
			require.NoError(t, err)
		}, current},
	} {
		step.edit()

		out, err := run(t, append(flags, "status")...)
		require.NoError(t, err)

		cli := statusLines(out)
		require.Equal(t, []string{root, step.want}, cli, "%s: %s", step.name, out)

		text := mcpStatusText(t, flags...)
		assert.Equal(t, cli, statusLines(text), "%s: %s", step.name, text)
	}
}

// TestStatusOverMCPReadsCurrentWhileTheIndexMatchesItsWorkspace covers
// a state in which the two surfaces print another workspace line. An
// origin that is gone is the other such state.
// A copy reads the database of its origin, and only the origin has an
// edit. seamark status compares the index with the origin, so it prints
// the stale route into the origin. The MCP refresh skips, because the
// index matches the copy. Its answer opens with the note that says so, and
// its line reads current, because the run in the origin would make the
// index differ from the copy. Both print the same root line.
func TestStatusOverMCPReadsCurrentWhileTheIndexMatchesItsWorkspace(t *testing.T) {
	origin := rootWriteRepo(t)
	copied := rootWriteCopy(t, origin)
	flags := []string{"-C", copied, "--db", store.DefaultPath(origin)}

	require.NoError(t, os.WriteFile(filepath.Join(origin, "c.go"), []byte("package main\n\nfunc c() {}\n"), 0o644))

	schema := " (schema v" + strconv.Itoa(store.SupportedSchema()) + ")"

	out, err := run(t, append(flags, "status")...)
	require.NoError(t, err)

	cli := statusLines(out)
	require.Len(t, cli, 2, out)
	assert.Equal(t, "workspace      STALE — run `seamark index` in "+origin+schema, cli[1], out)

	text := mcpStatusText(t, flags...)
	assert.True(t, strings.HasPrefix(text, "note: this index records another repository ("+origin+"); it matches this workspace now"), text)
	assert.Equal(t, []string{cli[0], "workspace      current" + schema}, statusLines(text), text)
}

// TestStaleHintsNameTheRouteOfTheDatabase covers a stale index that a
// plain index run in this workspace cannot refresh, because the database
// records another repository. The status line and the note of a read
// command name the route of that state. An own database keeps the plain
// index run, byte for byte.
func TestStaleHintsNameTheRouteOfTheDatabase(t *testing.T) {
	t.Run("the database of the origin from a copy", func(t *testing.T) {
		origin := rootWriteRepo(t)
		copied := rootWriteCopy(t, origin)
		flags := []string{"-C", copied, "--db", store.DefaultPath(origin)}

		require.NoError(t, os.WriteFile(filepath.Join(origin, "b.go"), []byte("package main\n"), 0o644))

		out, err := run(t, append(flags, "status")...)
		require.NoError(t, err)
		assert.Contains(t, out, "\nworkspace      STALE — run `seamark index` in "+origin+" (schema v", out)

		_, stderr, err := runErr(t, append(flags, "why", "main")...)
		require.NoError(t, err)
		assert.Contains(t, stderr, "note: the workspace changed since the last index — run `seamark index` in "+origin+"\n")
		assert.NotContains(t, stderr, "to refresh")
	})

	t.Run("an own database", func(t *testing.T) {
		root := rootWriteRepo(t)

		require.NoError(t, os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n"), 0o644))

		out, err := run(t, "-C", root, "status")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(out, "workspace      STALE — run `seamark index` (schema v"), out)

		_, stderr, err := runErr(t, "-C", root, "why", "main")
		require.NoError(t, err)
		assert.Contains(t, stderr, "note: the workspace changed since the last index — run `seamark index` to refresh\n")
	})
}
