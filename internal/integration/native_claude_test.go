package integration

// A native check of the tools that the installed Claude Code offers. It
// runs only with SEAMARK_NATIVE_CLIENT=claude (`make agents-native-check
// CLIENT=claude`) and skips otherwise, so `make test` stays offline.
//
// No check reaches a model. Claude Code sends every Messages API request
// to a scripted provider on the loopback interface, with a dummy key
// and an empty scratch CLAUDE_CONFIG_DIR. So the check never reads or
// writes the user's login, settings, or history.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tool name, the call ids, and the texts of the scripted Claude
// conversation. The edit tool names come from the guard decoder.
const (
	claudeToolSearch     = "ToolSearch"
	claudeSearchCallID   = "toolu_native_search"
	claudeMultiEditCall  = "toolu_native_multiedit"
	claudeNativeReply    = "done"
	claudeNativeFileBody = "alpha\ngamma\n"
)

// nativeClaude returns the claude executable, or skips when the native
// Claude Code checks are not selected. A selected check without claude
// is blocked, and blocked is a failure: no evidence was produced.
func nativeClaude(t *testing.T) string {
	t.Helper()

	if os.Getenv(nativeClientEnv) != ClaudeID {
		t.Skipf("native Claude Code checks run with %s=%s (make agents-native-check CLIENT=claude)", nativeClientEnv, ClaudeID)
	}

	path, err := exec.LookPath("claude")
	if err != nil {
		t.Fatalf("blocked: claude is not on PATH; install Claude Code to run the native checks")
	}

	return path
}

// claudeToolResult is one tool result that Claude Code sent back.
type claudeToolResult struct {
	isError bool
	text    string
}

// claudeProvider is a scripted Messages API. Each main request gets the
// next step of a fixed script. The step depends on the tool results in
// the conversation, not on a request count, so a side request of the
// client (a title, a token count) never shifts the script:
//
//  1. a ToolSearch call that selects MultiEdit, when the client offers
//     ToolSearch;
//  2. a MultiEdit call on the scratch file;
//  3. a final text.
type claudeProvider struct {
	server *httptest.Server
	// file is the absolute path that the MultiEdit call names.
	file string

	mu sync.Mutex
	// main counts the requests that offered the edit tools.
	main int
	// offered holds every tool name of every main request.
	offered []string
	// results holds the latest tool result of each scripted call id.
	results map[string]claudeToolResult
}

func newClaudeProvider(t *testing.T, file string) *claudeProvider {
	t.Helper()

	p := &claudeProvider{file: file, results: map[string]claudeToolResult{}}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)

	return p
}

// claudeRequest is the part of a Messages API request that the provider
// reads.
type claudeRequest struct {
	Model string `json:"model"`
	Tools []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func (p *claudeProvider) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/messages/count_tokens"):
		_, _ = io.WriteString(w, `{"input_tokens":1}`)

		return
	case r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/v1/messages"):
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"scripted provider"}}`)

		return
	}

	var request claudeRequest
	_ = json.Unmarshal(body, &request)

	names := make([]string, 0, len(request.Tools))
	for _, tool := range request.Tools {
		names = append(names, tool.Name)
	}

	block := map[string]any{"type": "text", "text": claudeNativeReply}

	// Only the main conversation offers the edit tools. A side request
	// gets the final text.
	if slices.Contains(names, claudeToolEdit) {
		block = p.step(names, request)
	}

	writeClaudeStream(w, request.Model, block)
}

// step records one main request and returns the next scripted block.
func (p *claudeProvider) step(names []string, request claudeRequest) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.main++
	p.offered = append(p.offered, names...)

	for _, message := range request.Messages {
		var blocks []struct {
			Type      string          `json:"type"`
			ToolUseID string          `json:"tool_use_id"`
			IsError   bool            `json:"is_error"`
			Content   json.RawMessage `json:"content"`
		}

		if json.Unmarshal(message.Content, &blocks) != nil {
			continue
		}

		for _, b := range blocks {
			if b.Type == "tool_result" {
				p.results[b.ToolUseID] = claudeToolResult{isError: b.IsError, text: toolResultText(b.Content)}
			}
		}
	}

	_, searched := p.results[claudeSearchCallID]
	_, called := p.results[claudeMultiEditCall]

	switch {
	case called:
		return map[string]any{"type": "text", "text": claudeNativeReply}
	case slices.Contains(names, claudeToolSearch) && !searched:
		return map[string]any{"type": "tool_use", "id": claudeSearchCallID, "name": claudeToolSearch,
			"input": map[string]any{"query": "select:" + claudeToolMultiEdit, "max_results": 5}}
	default:
		return map[string]any{"type": "tool_use", "id": claudeMultiEditCall, "name": claudeToolMultiEdit,
			"input": map[string]any{"file_path": p.file,
				"edits": []map[string]any{{"old_string": "alpha", "new_string": "beta"}}}}
	}
}

// snapshot reads what the provider recorded, for assertions.
func (p *claudeProvider) snapshot() (main int, offered []string, results map[string]claudeToolResult) {
	p.mu.Lock()
	defer p.mu.Unlock()

	results = make(map[string]claudeToolResult, len(p.results))
	for id, r := range p.results {
		results[id] = r
	}

	return p.main, slices.Clone(p.offered), results
}

// toolResultText reads the content of a tool result: a plain string, or
// a list of text blocks.
func toolResultText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	var blocks []struct {
		Text string `json:"text"`
	}

	_ = json.Unmarshal(raw, &blocks)

	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}

	return strings.Join(parts, " ")
}

// writeClaudeStream writes one assistant message with one content block
// as a Messages API event stream. A tool call ends the turn with
// stop_reason tool_use, so the client runs the tool and asks again.
func writeClaudeStream(w http.ResponseWriter, model string, block map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	event := func(kind string, fields map[string]any) {
		fields["type"] = kind
		data, _ := json.Marshal(fields)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
	}

	usage := map[string]any{"input_tokens": 1, "output_tokens": 1}
	event("message_start", map[string]any{"message": map[string]any{
		"id": "msg_native", "type": "message", "role": "assistant", "model": model,
		"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage,
	}})

	stop := "end_turn"

	if block["type"] == "tool_use" {
		stop = "tool_use"
		input, _ := json.Marshal(block["input"])

		event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{
			"type": "tool_use", "id": block["id"], "name": block["name"], "input": map[string]any{},
		}})
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
			"type": "input_json_delta", "partial_json": string(input),
		}})
	} else {
		event("content_block_start", map[string]any{"index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		event("content_block_delta", map[string]any{"index": 0, "delta": map[string]any{
			"type": "text_delta", "text": block["text"],
		}})
	}

	event("content_block_stop", map[string]any{"index": 0})
	event("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": stop, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 1},
	})
	event("message_stop", map[string]any{})
}

// claudeLoopbackEnv returns the environment of a Claude Code run against
// the provider. It drops every inherited Claude and Anthropic variable,
// so a login or a setting of the calling session cannot apply. The key
// is a dummy: the provider checks none. The switches keep background
// traffic, the auto-updater, and the advisor tool off.
func claudeLoopbackEnv(config, baseURL string) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "CLAUDE") || strings.HasPrefix(kv, "ANTHROPIC_")
	})

	return append(env,
		"CLAUDE_CONFIG_DIR="+config,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY=seamark-native-check-no-model",
		"DISABLE_AUTOUPDATER=1",
		"DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_ADVISOR_TOOL=1",
	)
}

// TestNativeClaudeOffersNoMultiEditTool checks that the installed Claude
// Code offers no MultiEdit tool, inline or deferred, and refuses a
// MultiEdit call. The guard reads MultiEdit inputs, but no recording can
// show a native MultiEdit deny while the client has no such tool. So
// the MultiEdit paths keep active-unverified; this check gives the
// reason for the installed version without a model.
func TestNativeClaudeOffersNoMultiEditTool(t *testing.T) {
	claude := nativeClaude(t)

	version, err := exec.Command(claude, "--version").Output()
	require.NoError(t, err, "blocked: claude --version failed")
	t.Logf("installed: %s", strings.TrimSpace(string(version)))

	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	file := filepath.Join(repo, "notes", "multi.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o755))
	require.NoError(t, os.WriteFile(file, []byte(claudeNativeFileBody), 0o644))

	provider := newClaudeProvider(t, file)
	config := scratchClientDir(t, "seamark-native-claude-config-")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// MultiEdit is allowed, so a refusal cannot come from a permission
	// rule. The setting sources leave out the user scope, and the strict
	// MCP flag loads no server.
	cmd := exec.CommandContext(ctx, claude, "-p", "--model", "haiku", "--output-format", "json",
		"--setting-sources", "project,local", "--strict-mcp-config", "--max-budget-usd", "0.01",
		"--allowedTools", "Read", "Edit", "Write", claudeToolMultiEdit)
	cmd.Dir = repo
	cmd.Env = claudeLoopbackEnv(config, provider.server.URL)
	cmd.Stdin = strings.NewReader("Follow the scripted steps.")

	out, runErr := cmd.Output()

	var result struct {
		IsError  bool   `json:"is_error"`
		NumTurns int    `json:"num_turns"`
		Result   string `json:"result"`
	}

	_ = json.Unmarshal(out, &result)
	t.Logf("run: error %v, is_error %v, turns %d, result %q", runErr, result.IsError, result.NumTurns, result.Result)

	main, offered, results := provider.snapshot()
	if main == 0 {
		t.Fatalf("blocked: the provider saw no request that offered the edit tools (run error: %v)", runErr)
	}

	t.Logf("main requests: %d; tools offered: %v", main, compactNames(offered))

	assert.NotContains(t, offered, claudeToolMultiEdit, "no request offers a MultiEdit tool")

	if search, ok := results[claudeSearchCallID]; ok {
		t.Logf("ToolSearch select:MultiEdit: %q", search.text)
	} else {
		assert.NotContains(t, offered, claudeToolSearch, "an offered ToolSearch received the scripted call")
	}

	call, ok := results[claudeMultiEditCall]
	if !ok {
		t.Fatalf("blocked: the client sent no result for the scripted MultiEdit call")
	}

	t.Logf("MultiEdit call: error %v, %q", call.isError, call.text)
	assert.True(t, call.isError, "the client refuses the MultiEdit call")
	assert.Contains(t, call.text, claudeToolMultiEdit, "the refusal names the tool")

	after, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, claudeNativeFileBody, string(after), "the refused call leaves the file unchanged")
}

// compactNames returns each name once, in first-seen order.
func compactNames(names []string) []string {
	var out []string

	for _, name := range names {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}

	return out
}
