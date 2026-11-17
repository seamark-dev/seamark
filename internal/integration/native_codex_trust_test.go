package integration

// A native check that Codex setup grants no hook trust. It runs only
// with SEAMARK_NATIVE_CLIENT=codex (`make agents-native-check
// CLIENT=codex`), like the other native Codex checks.
//
// Codex keeps project trust and hook trust in its home, and runs a
// project hook only after the user reviews it. The check runs the setup
// of `seamark init --client codex --lesson-checks enforce` in a scratch
// repository with an empty scratch CODEX_HOME. Then it asks
// `codex app-server` for its hook list. The app server starts no thread
// and no turn, so no model is called. The check never writes a trust
// record: a review or a grant would make the result say nothing about
// setup.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/hooks"
)

// codexTrusted is the trust status of a hook that Codex runs.
const codexTrusted = "trusted"

// appServerHook is one hook of a `hooks/list` reply, with the fields
// that the check reads.
type appServerHook struct {
	Key         string `json:"key"`
	Source      string `json:"source"`
	EventName   string `json:"eventName"`
	Command     string `json:"command"`
	TrustStatus string `json:"trustStatus"`
}

// hooksListing is what one `hooks/list` call returned for one
// directory: the hooks, and the summaries of the configuration warnings
// that the app server sent before its reply.
type hooksListing struct {
	hooks    []appServerHook
	warnings []string
}

// codexClientEnv returns the environment of a Codex process without
// credentials. The app server then has no way to reach a model, even by
// mistake. CODEX_HOME stays, because it points at the scratch home.
func codexClientEnv() []string {
	return slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, prefix := range []string{"OPENAI_", "CODEX_API_KEY=", "ANTHROPIC_", "CLAUDE"} {
			if strings.HasPrefix(kv, prefix) {
				return true
			}
		}

		return false
	})
}

// listCodexHooks starts `codex app-server` in repo and returns its hook
// list for repo. The plugin catalogue stays off, because its fetch
// reaches the network and says nothing about hooks.
func listCodexHooks(t *testing.T, codex, repo string) hooksListing {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, codex, "app-server",
		"-c", "features.plugins=false", "-c", "features.remote_plugin=false")
	cmd.Dir = repo
	cmd.Env = codexClientEnv()

	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)

	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)

	require.NoError(t, cmd.Start(), "blocked: codex app-server did not start")

	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()

	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	send := func(message map[string]any) {
		data, err := json.Marshal(message)
		require.NoError(t, err)

		_, err = fmt.Fprintf(stdin, "%s\n", data)
		require.NoError(t, err, "blocked: codex app-server closed its input")
	}

	var listing hooksListing

	// reply reads messages until the reply with the given id. It keeps
	// the summary of each configuration warning on the way.
	reply := func(id int) json.RawMessage {
		for lines.Scan() {
			var message struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}

			if json.Unmarshal(lines.Bytes(), &message) != nil {
				continue
			}

			if message.Method == "configWarning" {
				var warning struct {
					Summary string `json:"summary"`
				}

				_ = json.Unmarshal(message.Params, &warning)
				listing.warnings = append(listing.warnings, warning.Summary)
			}

			if message.ID == nil || *message.ID != id || message.Method != "" {
				continue
			}

			require.Empty(t, message.Error, "blocked: codex app-server refused request %d", id)

			return message.Result
		}

		t.Fatalf("blocked: codex app-server stopped before its reply to request %d (%v)", id, lines.Err())

		return nil
	}

	send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{
		"clientInfo": map[string]any{"name": "seamark-native-check", "version": "1"},
	}})
	reply(1)
	send(map[string]any{"method": "initialized"})
	send(map[string]any{"id": 2, "method": "hooks/list", "params": map[string]any{"cwds": []string{repo}}})

	var result struct {
		Data []struct {
			Cwd    string          `json:"cwd"`
			Hooks  []appServerHook `json:"hooks"`
			Errors []any           `json:"errors"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(reply(2), &result), "blocked: the hooks/list reply has an unknown shape")
	require.Len(t, result.Data, 1, "blocked: hooks/list answers for one directory")
	assert.Empty(t, result.Data[0].Errors, "hooks/list reports no error for the scratch repository")

	listing.hooks = result.Data[0].Hooks

	return listing
}

// scratchGitRepo creates an empty git repository and returns its
// physical path. Codex keys a project by its resolved directory, and a
// temporary directory on macOS sits behind a link.
func scratchGitRepo(t *testing.T) string {
	t.Helper()

	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	out, err := exec.Command("git", "init", "-q", repo).CombinedOutput()
	require.NoError(t, err, "blocked: git init failed: %s", out)

	return repo
}

// generatedCommands returns the sorted commands of a hook document.
func generatedCommands(t *testing.T, path string) []string {
	t.Helper()

	var commands []string

	for _, e := range readHookForm(t, path).entries {
		command, _ := e.Handler["command"].(string)
		commands = append(commands, command)
	}

	slices.Sort(commands)

	return commands
}

// TestNativeCodexSetupGrantsNoHookTrust checks on the installed Codex
// that setup leaves every Seamark hook without trust. Seamark cannot
// read the trust record, so this checks the absence of a grant, not the
// user's later review: every Codex path keeps trust-unknown.
func TestNativeCodexSetupGrantsNoHookTrust(t *testing.T) {
	codex := nativeCodex(t)
	home := scratchCodexHome(t)
	repo := scratchGitRepo(t)

	setups, err := ExplicitSetups(Builtin(), []string{CodexID}, false, false, "")
	require.NoError(t, err)
	applyLessonSetup(t, repo, WithLessonChecks(setups, hooks.LessonChecksEnforce, "")...)

	doc := filepath.Join(repo, ".codex", "hooks.json")
	commands := generatedCommands(t, doc)
	require.True(t, slices.ContainsFunc(commands, func(c string) bool {
		return strings.HasSuffix(c, " "+codexGuardMarker)
	}), "enforce setup installs the guard, so the check covers it")

	// Codex writes trust records only into its home. Setup must leave
	// the home as it found it. The check runs before the first codex
	// command, because each command writes its own files there.
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "setup writes nothing into CODEX_HOME")

	t.Logf("installed: %s", codexVersion(t, codex))

	// The project layer. On codex-cli 0.169.0 Codex lists no project hook,
	// because it loads no project hook before the user trusts the project.
	// A later version can list them; then each must read untrusted.
	project := listCodexHooks(t, codex, repo)

	for _, h := range project.hooks {
		assert.NotEqual(t, codexTrusted, h.TrustStatus, "hook %s reads trusted after setup alone", h.Key)
	}

	t.Logf("project layer: %d hooks listed", len(project.hooks))

	for _, w := range project.warnings {
		t.Logf("config warning: %s", strings.Join(strings.Fields(w), " "))
	}

	// The control. The same entries in the user layer of the scratch home
	// are always loaded, so the list must show them. This proves that the
	// list can see the Seamark entries, and that Codex has no trust record
	// for them. An empty list here means the probe saw nothing.
	data, err := os.ReadFile(doc)
	require.NoError(t, err)

	user := filepath.Join(home, "hooks.json")
	require.NoError(t, os.WriteFile(user, data, 0o600))

	defer func() { _ = os.Remove(user) }()

	control := listCodexHooks(t, codex, repo)

	var listed []string

	for _, h := range control.hooks {
		assert.NotEqual(t, codexTrusted, h.TrustStatus, "hook %s reads trusted with no trust record", h.Key)

		if h.Source == "user" {
			listed = append(listed, h.Command)
		}
	}

	if len(listed) == 0 {
		t.Fatalf("blocked: hooks/list shows no user-layer hook from %s, so it gives no evidence on this version", user)
	}

	slices.Sort(listed)
	assert.Equal(t, commands, listed, "hooks/list shows every generated entry, untrusted")

	for _, h := range control.hooks {
		t.Logf("control: %s %s %s", h.Source, h.EventName, h.TrustStatus)
	}
}
