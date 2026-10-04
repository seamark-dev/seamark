package integration

// Native checks against the installed Codex CLI. They run only when
// SEAMARK_NATIVE_CLIENT=codex is set (`make agents-native-check
// CLIENT=codex`) and skip otherwise, so `make test` stays offline. When
// the client is selected, a missing precondition fails the check as
// blocked: the operator asked for native evidence and got none, and a
// green exit must not stand in for it.
//
// No check starts a login or reaches a model. Inference goes to a
// scripted provider on the loopback interface, and every run uses a
// scratch CODEX_HOME, so the user's login, configuration, rules, and
// trust records are never read or written.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/agent"
)

// nativeClientEnv selects the client whose native checks run.
const nativeClientEnv = "SEAMARK_NATIVE_CLIENT"

// nativeTimeout bounds one Codex run. A provider failure makes Codex
// retry a few times before it gives up, so the bound is generous.
const nativeTimeout = 4 * time.Minute

// nativeCodex returns the codex executable, or skips when the native
// checks are not selected. A selected check without codex is blocked,
// and blocked is a failure: no evidence was produced.
func nativeCodex(t *testing.T) string {
	t.Helper()

	if os.Getenv(nativeClientEnv) != CodexID {
		t.Skipf("native Codex checks run with %s=%s (make agents-native-check CLIENT=codex)", nativeClientEnv, CodexID)
	}

	path, err := exec.LookPath("codex")
	if err != nil {
		t.Fatalf("blocked: codex is not on PATH; install the Codex CLI to run the native checks")
	}

	return path
}

// codexVersion runs `codex --version` and returns the trimmed line, for
// example "codex-cli 0.157.0". The command reads no configuration and
// starts nothing.
func codexVersion(t *testing.T, codex string) string {
	t.Helper()

	out, err := exec.Command(codex, "--version").Output()
	require.NoError(t, err, "blocked: codex --version failed")

	return strings.TrimSpace(string(out))
}

// scratchCodexHome creates an empty CODEX_HOME for one check and points
// the process at it. Codex then finds no login, no user configuration,
// no rules, and no trust records: the check observes the binary, not
// the user's account.
//
// The directory is removed on a best-effort basis, not through
// t.TempDir: Codex leaves background work in its home for a moment
// after the run (a plugin catalogue clone under .tmp), and a removal
// that races it must not fail a check that produced its evidence.
func scratchCodexHome(t *testing.T) string {
	t.Helper()

	home, err := os.MkdirTemp("", "seamark-native-codex-home-")
	require.NoError(t, err)

	t.Cleanup(func() {
		for range 20 {
			if os.RemoveAll(home) == nil {
				return
			}

			time.Sleep(100 * time.Millisecond)
		}

		t.Logf("scratch CODEX_HOME %s could not be removed; Codex may still write to it", home)
	})

	t.Setenv("CODEX_HOME", home)

	return home
}

// TestNativeCodexRecordsTheVersionAndTheExecSurface records the
// installed version beside the recorded evidence and checks that the
// preset's flags still exist on this version. A flag that disappears
// would make every later check fail with a confusing error; this one
// names the flag.
func TestNativeCodexRecordsTheVersionAndTheExecSurface(t *testing.T) {
	codex := nativeCodex(t)
	scratchCodexHome(t)

	version := codexVersion(t, codex)
	t.Logf("installed: %s", version)
	t.Logf("recorded evidence: edits on %s, skills on %s", codexEditsEvidence.ClientVersion, codexSkillsEvidence.ClientVersion)

	if version != codexEditsEvidence.ClientVersion {
		t.Logf("the installed version differs from the recorded evidence: these checks apply to %s; the recorded evidence keeps its own scope", version)
	}

	help, err := exec.Command(codex, "exec", "--help").CombinedOutput()
	require.NoError(t, err, "blocked: codex exec --help failed")

	spec, err := codexInvocation(t.TempDir())
	require.NoError(t, err)

	for _, arg := range spec.Argv[2:] {
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}

		assert.Contains(t, string(help), arg, "codex exec --help no longer lists the preset flag %s", arg)
	}

	assert.Contains(t, string(help), "stdin", "codex exec --help no longer documents the prompt on stdin")

	// `-c features.hooks=false` names a feature; the feature list is the
	// offline way to see that this version still knows it.
	features, err := exec.Command(codex, "features", "list").CombinedOutput()
	require.NoError(t, err, "blocked: codex features list failed")

	assert.Regexp(t, `(?m)^hooks\s`, string(features), "codex features list no longer names the hooks feature")
}

// oracleScratch is the file layout the apply_patch oracle was recorded
// against: every patch updates or deletes these names, and every added
// name is absent. The layout is inferred from the patches, because the
// 2026-09-20 recording did not write it down.
var oracleScratch = map[string]string{
	"a.txt": "x\n",
	"b.txt": "b\n",
	"c.txt": "c\n",
	"h.txt": "*** Delete File: b.txt\nx\n",
}

// TestNativeCodexApplyPatchOracleHoldsOnTheInstalledBinary reruns the
// recorded patches through the installed binary. The oracle pins what
// codex-cli 0.154.0 accepted and touched; the decoder follows that
// parser. A newer binary that answers differently means the parser
// moved and the decoder needs a new recording.
func TestNativeCodexApplyPatchOracleHoldsOnTheInstalledBinary(t *testing.T) {
	codex := nativeCodex(t)
	scratchCodexHome(t)

	// Codex applies a patch when it runs under the name apply_patch.
	bin := filepath.Join(t.TempDir(), "apply_patch")
	require.NoError(t, os.Symlink(codex, bin))

	var oracle struct {
		Cases []struct {
			Name     string   `json:"name"`
			Patch    string   `json:"patch"`
			Accepted bool     `json:"accepted"`
			Touched  []string `json:"touched"`
		} `json:"cases"`
	}

	require.NoError(t, json.Unmarshal(codexFixture(t, "apply_patch_oracle.json"), &oracle))
	require.NotEmpty(t, oracle.Cases)

	for _, tc := range oracle.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			dir := t.TempDir()

			for name, content := range oracleScratch {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
			}

			cmd := exec.Command(bin, tc.Patch)
			cmd.Dir = dir
			out, err := cmd.Output()

			accepted := err == nil
			assert.Equal(t, tc.Accepted, accepted, "acceptance differs from the oracle; output: %s", out)

			if !accepted || !tc.Accepted {
				return
			}

			assert.ElementsMatch(t, tc.Touched, touchedPaths(string(out)), "the touched set differs from the oracle")
		})
	}
}

// touchedPaths reads the `A/M/D <path>` lines of the apply_patch report.
func touchedPaths(report string) []string {
	var paths []string

	for _, line := range strings.Split(report, "\n") {
		if len(line) > 2 && strings.ContainsRune("AMD", rune(line[0])) && line[1] == ' ' {
			paths = append(paths, line[2:])
		}
	}

	return paths
}

// scriptedProvider is a loopback Responses API that answers the first
// request with one tool call and every later request with a final
// message. It records the tools Codex advertised and the tool outputs
// Codex sent back, so a check can read what the sandbox did to the
// command without trusting the model-visible text alone.
type scriptedProvider struct {
	server *httptest.Server
	mu     sync.Mutex
	// command is the shell command the scripted call runs.
	command string
	// reply is the final message text.
	reply string
	// fail makes every request answer 401, for the failure path.
	fail bool

	requests int
	tools    []string
	outputs  []string
}

func newScriptedProvider(t *testing.T, command, reply string) *scriptedProvider {
	t.Helper()

	p := &scriptedProvider{command: command, reply: reply}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)

	return p
}

// overrides are the `-c` settings that point Codex at the provider.
// They go before the trailing `-`, which must stay last. The plugin
// catalogue fetch is off: it reaches the network from the scratch home
// and says nothing about the sandbox under check.
func (p *scriptedProvider) overrides() []string {
	return []string{
		"-c", "model_provider=mock",
		"-c", `model_providers.mock.name="mock"`,
		"-c", fmt.Sprintf(`model_providers.mock.base_url="%s/v1"`, p.server.URL),
		"-c", `model_providers.mock.wire_api="responses"`,
		"-c", "features.plugins=false",
		"-c", "features.remote_plugin=false",
	}
}

func (p *scriptedProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	var request struct {
		Tools []toolDef `json:"tools"`
		Input []struct {
			Type   string          `json:"type"`
			Output json.RawMessage `json:"output"`
			Tools  []struct {
				Tools []toolDef `json:"tools"`
			} `json:"tools"`
		} `json:"input"`
	}

	_ = json.Unmarshal(body, &request)

	p.mu.Lock()
	p.requests++
	n := p.requests

	tools := request.Tools
	for _, item := range request.Input {
		switch item.Type {
		case "additional_tools":
			for _, ns := range item.Tools {
				tools = append(tools, ns.Tools...)
			}
		case "custom_tool_call_output", "function_call_output":
			p.outputs = append(p.outputs, string(item.Output))
		}
	}

	for _, tool := range tools {
		p.tools = append(p.tools, tool.Name)
	}

	p.mu.Unlock()

	if p.fail {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"scripted authentication failure","type":"invalid_request_error"}}`)

		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	event := func(kind string, fields map[string]any) {
		fields["type"] = kind
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
	}

	id := fmt.Sprintf("resp_%d", n)
	event("response.created", map[string]any{"response": map[string]any{"id": id}})

	var item map[string]any

	if call := p.toolCall(tools); n == 1 && call != nil {
		item = call
	} else {
		item = map[string]any{
			"type": "message", "id": "msg_1", "role": "assistant", "status": "completed",
			"content": []map[string]any{{"type": "output_text", "text": p.reply}},
		}
	}

	event("response.output_item.done", map[string]any{"item": item, "output_index": 0})
	event("response.completed", map[string]any{"response": map[string]any{
		"id": id, "usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
	}})
}

// toolDef is the part of a tool definition the provider reads.
type toolDef struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// toolCall scripts the command through whichever shell surface this
// Codex version advertises: the code-mode `exec` custom tool (0.157.0,
// nested exec_command), or a plain `exec_command` or `shell` function
// tool on versions before it. Nil means no known surface.
func (p *scriptedProvider) toolCall(tools []toolDef) map[string]any {
	args, _ := json.Marshal(p.command)

	for _, tool := range tools {
		switch {
		case tool.Type == "custom" && tool.Name == "exec":
			script := fmt.Sprintf("const r = await tools.exec_command({cmd: %s}); text(JSON.stringify(r));", args)

			return map[string]any{
				"type": "custom_tool_call", "id": "ctc_1", "call_id": "call_1", "name": "exec",
				"input": script, "status": "completed",
			}
		case tool.Type == "function" && tool.Name == "exec_command":
			return map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "exec_command",
				"arguments": fmt.Sprintf(`{"cmd":%s}`, args), "status": "completed",
			}
		case tool.Type == "function" && tool.Name == "shell":
			argv, _ := json.Marshal(strings.Fields(p.command))

			return map[string]any{
				"type": "function_call", "id": "fc_1", "call_id": "call_1", "name": "shell",
				"arguments": fmt.Sprintf(`{"command":%s}`, argv), "status": "completed",
			}
		}
	}

	return nil
}

// snapshot reads what the provider recorded, for assertions.
func (p *scriptedProvider) snapshot() (requests int, tools, outputs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.requests, slices.Clone(p.tools), slices.Clone(p.outputs)
}

// allowRule lets `touch` run outside the sandbox when Codex loads the
// rule: the escalation the preset must not inherit.
const allowRule = "prefix_rule(\n    pattern = [\"touch\"],\n    decision = \"allow\",\n)\n"

// writeAllowRule installs the rule in the scratch home's rules directory.
func writeAllowRule(t *testing.T, home string) {
	t.Helper()

	rules := filepath.Join(home, "rules")
	require.NoError(t, os.MkdirAll(rules, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rules, "allow-touch.rules"), []byte(allowRule), 0o644))
}

// nativeWorkspace is a small workspace to run the preset in.
func nativeWorkspace(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "README.md"), []byte("native check\n"), 0o644))

	return root
}

// treeDigest hashes every file under root by path and content, so a
// check can prove that a run left the workspace byte-identical.
func treeDigest(t *testing.T, root string) string {
	t.Helper()

	var paths []string

	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, path)
		}

		return err
	}))

	sort.Strings(paths)
	h := sha256.New()

	for _, path := range paths {
		data, err := os.ReadFile(path)
		require.NoError(t, err)

		rel, _ := filepath.Rel(root, path)
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
		h.Write(data)
	}

	return hex.EncodeToString(h.Sum(nil))
}

// runPreset runs the Codex preset for root through the shared invoker,
// with the provider overrides inserted before the stdin marker.
// withRules drops --ignore-rules, for the control that shows the
// escalation the flag prevents.
func runPreset(t *testing.T, root string, provider *scriptedProvider, withRules bool) (string, error) {
	t.Helper()

	spec, err := codexInvocation(root)
	require.NoError(t, err)

	argv := spec.Argv[:len(spec.Argv)-1]

	if withRules {
		argv = slices.DeleteFunc(slices.Clone(argv), func(arg string) bool { return arg == "--ignore-rules" })
	}

	spec.Argv = append(append(slices.Clone(argv), provider.overrides()...), "-")

	invoker, err := agent.NewCommand(spec)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), nativeTimeout)
	defer cancel()

	return invoker.Invoke(ctx, "run the scripted command")
}

// requireShellSurface fails as blocked when the provider saw no shell
// surface it knows, or when the shell host did not start. Both mean
// the check produced no evidence about the sandbox.
func requireShellSurface(t *testing.T, provider *scriptedProvider) {
	t.Helper()

	requests, tools, outputs := provider.snapshot()
	require.GreaterOrEqual(t, requests, 2, "blocked: Codex sent no tool output back; advertised tools: %s", strings.Join(tools, ", "))
	require.NotNil(t, provider.toolCall(toolDefsNamed(tools)), "blocked: no known shell surface among the advertised tools: %s", strings.Join(tools, ", "))

	for _, output := range outputs {
		if strings.Contains(output, "code-mode host") {
			t.Fatalf("blocked: the shell host did not start, so the command never ran: %s (run the check from a normal terminal with codex-code-mode-host resolvable)", output)
		}
	}
}

// toolDefsNamed rebuilds definitions from recorded names, for the
// surface check. The custom exec tool is the only custom one.
func toolDefsNamed(names []string) []toolDef {
	defs := make([]toolDef, 0, len(names))

	for _, name := range names {
		kind := "function"
		if name == "exec" {
			kind = "custom"
		}

		defs = append(defs, toolDef{Type: kind, Name: name})
	}

	return defs
}

// TestNativeCodexPresetKeepsTheWorkspaceReadOnly is compatibility check
// 13 and the credential-free part of check 5: with an allow rule for
// `touch` in the (scratch) user rules, the preset must refuse the
// write, leave the tree byte-identical, and return the final message
// on stdout. Hook recursion is not observable here: a scratch home
// trusts no hook, so `features.hooks=false` has nothing to switch off.
func TestNativeCodexPresetKeepsTheWorkspaceReadOnly(t *testing.T) {
	nativeCodex(t)
	home := scratchCodexHome(t)
	writeAllowRule(t, home)

	root := nativeWorkspace(t)
	before := treeDigest(t, root)

	provider := newScriptedProvider(t, "touch escaped.txt", "NATIVE-REPLY-7f3d91")
	reply, err := runPreset(t, root, provider, false)
	require.NoError(t, err)

	requireShellSurface(t, provider)

	assert.Equal(t, "NATIVE-REPLY-7f3d91", strings.TrimSpace(reply), "the final message is the reply on stdout")
	assert.NoFileExists(t, filepath.Join(root, "escaped.txt"), "the sandbox let the command write despite --sandbox read-only --ignore-rules")
	assert.Equal(t, before, treeDigest(t, root), "the workspace changed")

	_, _, outputs := provider.snapshot()
	assert.NotEmpty(t, outputs)

	for _, output := range outputs {
		t.Logf("sandbox reported: %s", output)
		assert.NotContains(t, output, `"exit_code":0`, "the write command reported success: %s", output)
	}
}

// TestNativeCodexAllowRuleEscalatesWithoutIgnoreRules is the control
// for the check above: the same rule and command without
// --ignore-rules write the file. It proves the check can fail. A
// version that no longer honors an allow rule outside the sandbox
// makes this control fail on its own, with the preset check intact.
func TestNativeCodexAllowRuleEscalatesWithoutIgnoreRules(t *testing.T) {
	nativeCodex(t)
	home := scratchCodexHome(t)
	writeAllowRule(t, home)

	root := nativeWorkspace(t)

	provider := newScriptedProvider(t, "touch escaped.txt", "NATIVE-REPLY-7f3d91")
	_, err := runPreset(t, root, provider, true)
	require.NoError(t, err)

	requireShellSurface(t, provider)

	assert.FileExists(t, filepath.Join(root, "escaped.txt"),
		"the control did not reproduce the escalation: an allow rule no longer lets touch run outside the sandbox on this version")
}

// TestNativeCodexPresetFailsWithTheProviderReason is the failure half
// of check 5: a provider failure ends the run with a nonzero status,
// and the shared invoker's diagnostic tail carries the reason instead
// of the version banner.
func TestNativeCodexPresetFailsWithTheProviderReason(t *testing.T) {
	nativeCodex(t)
	scratchCodexHome(t)

	root := nativeWorkspace(t)
	before := treeDigest(t, root)

	provider := newScriptedProvider(t, "true", "unused")
	provider.fail = true

	_, err := runPreset(t, root, provider, false)
	require.Error(t, err, "a 401 from the provider must fail the run")

	assert.Contains(t, err.Error(), "agent codex:", "the error names the client")
	assert.Contains(t, err.Error(), "401", "the diagnostic tail carries the provider status, not the banner")
	assert.Equal(t, before, treeDigest(t, root), "a failed run changed the workspace")
}
