package integration

// A native check of the tools that the installed Claude Code offers. It
// runs only with SEAMARK_NATIVE_CLIENT=claude (`make agents-native-check
// CLIENT=claude`) and skips otherwise, so `make test` stays offline.
//
// No check reaches a model. Claude Code sends every Messages API request
// to a scripted provider on the loopback interface, with a dummy key
// and an empty scratch CLAUDE_CONFIG_DIR. So the check never reads or
// writes the user's login, settings, or history.
//
// The offline tests at the end of the file send the requests of fake
// clients to the same provider. They run in `make test` and show that
// the verdict fails each client that has a MultiEdit tool.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The tool name, the call ids, and the texts of the scripted Claude
// conversation. The edit tool names come from the guard decoder. The
// last two texts are the answers of Claude Code 2.1.325: the ToolSearch
// result that loads nothing, and the refusal of a call to a tool that
// the client does not have.
const (
	claudeToolSearch     = "ToolSearch"
	claudeSearchCallID   = "toolu_native_search"
	claudeMultiEditCall  = "toolu_native_multiedit"
	claudeNativeReply    = "done"
	claudeNativeFileBody = "alpha\ngamma\n"
	claudeSearchNoMatch  = "No matching deferred tools found"
	claudeNoSuchTool     = "No such tool available: " + claudeToolMultiEdit
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

// claudeToolResult is one tool result that Claude Code sent back. The
// raw content keeps every block, so a loaded tool schema or a tool
// reference stays visible also when it carries no text.
type claudeToolResult struct {
	isError bool
	text    string
	raw     string
}

// claudeProbe is what the provider recorded of the main requests.
type claudeProbe struct {
	// main counts the requests that offered the edit tools.
	main int
	// offered holds every tool name of every main request.
	offered []string
	// lists counts the deferred tool lists in the texts of those requests.
	lists int
	// deferred holds every tool name of those lists.
	deferred []string
	// results holds the latest tool result of each scripted call id.
	results map[string]claudeToolResult
}

// multiEditProblems returns each reason why the probe does not show that
// the client has no MultiEdit tool. An empty list means that it shows
// it. A reason that starts with "blocked:" means that a scripted step
// did not run, so the probe gave no evidence.
func (p claudeProbe) multiEditProblems() []string {
	if p.main == 0 {
		return []string{"blocked: the provider saw no request that offered the edit tools"}
	}

	var problems []string

	if slices.Contains(p.offered, claudeToolMultiEdit) {
		problems = append(problems, "a request offers a MultiEdit tool")
	}

	if slices.Contains(p.deferred, claudeToolMultiEdit) {
		problems = append(problems, "a request lists MultiEdit as a deferred tool")
	}

	// A client with ToolSearch loads a deferred tool when the model asks
	// for it. So the probe must read the deferred tool list, and the
	// scripted search must load nothing.
	if slices.Contains(p.offered, claudeToolSearch) {
		problems = append(problems, p.searchProblems()...)
	}

	call, ok := p.results[claudeMultiEditCall]

	// Only the no-such-tool refusal shows a missing tool. A refusal for
	// another reason, such as an unread file, comes from a tool that exists.
	switch {
	case !ok:
		problems = append(problems, "blocked: the client sent no result for the scripted MultiEdit call")
	case !call.isError:
		problems = append(problems, fmt.Sprintf("the client runs the MultiEdit call: %q", call.text))
	case !strings.Contains(call.text, claudeNoSuchTool):
		problems = append(problems, fmt.Sprintf("the refusal of the MultiEdit call does not say %q: %q", claudeNoSuchTool, call.text))
	}

	return problems
}

// searchProblems returns each reason why the ToolSearch part of the
// probe does not show that the client has no deferred MultiEdit tool.
func (p claudeProbe) searchProblems() []string {
	var problems []string

	if p.lists == 0 {
		problems = append(problems, "blocked: the client offers ToolSearch, but no request holds a deferred tool list that the probe can read")
	}

	search, ok := p.results[claudeSearchCallID]
	if !ok {
		return append(problems, "blocked: the client sent no result for the scripted ToolSearch call")
	}

	// A loaded tool comes back as a schema or a tool reference that
	// names it. The raw content holds both forms.
	if strings.Contains(search.raw, claudeToolMultiEdit) {
		problems = append(problems, fmt.Sprintf("ToolSearch select:MultiEdit loads a MultiEdit tool: %s", search.raw))
	}

	if !strings.Contains(search.text, claudeSearchNoMatch) {
		problems = append(problems, fmt.Sprintf("the ToolSearch result does not say %q: %q", claudeSearchNoMatch, search.text))
	}

	return problems
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

	mu    sync.Mutex
	probe claudeProbe
}

func newClaudeProvider(t *testing.T, file string) *claudeProvider {
	t.Helper()

	p := &claudeProvider{file: file, probe: claudeProbe{results: map[string]claudeToolResult{}}}
	p.server = httptest.NewServer(http.HandlerFunc(p.handle))
	t.Cleanup(p.server.Close)

	return p
}

// claudeRequest is the part of a Messages API request that the provider
// reads.
type claudeRequest struct {
	Model  string          `json:"model"`
	System json.RawMessage `json:"system"`
	Tools  []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

// claudeBlock is one content block of a request message.
type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
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

	p.probe.main++
	p.probe.offered = append(p.probe.offered, names...)

	texts := []string{blockText(request.System)}

	for _, message := range request.Messages {
		var blocks []claudeBlock

		if json.Unmarshal(message.Content, &blocks) != nil {
			texts = append(texts, blockText(message.Content))

			continue
		}

		for _, b := range blocks {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "tool_result":
				text := blockText(b.Content)
				texts = append(texts, text)
				p.probe.results[b.ToolUseID] = claudeToolResult{isError: b.IsError, text: text, raw: string(b.Content)}
			}
		}
	}

	for _, text := range texts {
		for _, list := range deferredToolLists(text) {
			p.probe.lists++
			p.probe.deferred = append(p.probe.deferred, list...)
		}
	}

	_, searched := p.probe.results[claudeSearchCallID]
	_, called := p.probe.results[claudeMultiEditCall]

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

// snapshot copies what the provider recorded, for assertions.
func (p *claudeProvider) snapshot() claudeProbe {
	p.mu.Lock()
	defer p.mu.Unlock()

	probe := p.probe
	probe.offered = slices.Clone(p.probe.offered)
	probe.deferred = slices.Clone(p.probe.deferred)
	probe.results = maps.Clone(p.probe.results)

	return probe
}

// blockText reads a text field of the Messages API: a plain string, or a
// list of blocks whose texts it joins with line breaks. The line breaks
// keep a list line of one block apart from the next block.
func blockText(raw json.RawMessage) string {
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

	return strings.Join(parts, "\n")
}

// claudeToolNameLine matches a line that holds one tool name, such as
// NotebookEdit or mcp__server__tool.
var claudeToolNameLine = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)

// claudeMarkupTag matches one markup tag, such as <system-reminder>.
var claudeMarkupTag = regexp.MustCompile(`<[^>]*>`)

// deferredToolLists returns the tool names of each deferred tool list in
// a text. Claude Code 2.1.325 does not put its deferred tools in the tools
// field. It names them in a message text: a header line that names
// deferred tools and ends with a colon, then one name on each line. The
// parser also takes a header that ends with a tag, such as
// <available-deferred-tools>, because another version can use that form.
// A list ends at the first line that is not one name, or at a closing tag.
func deferredToolLists(text string) [][]string {
	var lists [][]string

	lines := strings.Split(text, "\n")

	for i, line := range lines {
		header := strings.TrimSpace(line)

		opens := strings.HasSuffix(header, ":") || strings.HasSuffix(header, ">")
		if !opens || !strings.Contains(strings.ToLower(header), "deferred") || strings.HasPrefix(header, "</") {
			continue
		}

		var names []string

		for _, next := range lines[i+1:] {
			name := strings.TrimSpace(claudeMarkupTag.ReplaceAllString(next, ""))
			if !claudeToolNameLine.MatchString(name) {
				break
			}

			names = append(names, name)

			if strings.Contains(next, "</") {
				break
			}
		}

		if len(names) > 0 {
			lists = append(lists, names)
		}
	}

	return lists
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
// Code has no MultiEdit tool. No request offers one in the tools field
// or in a deferred tool list. When the client offers ToolSearch,
// "select:MultiEdit" loads nothing. A scripted MultiEdit call gets the
// no-such-tool refusal, and the file keeps its bytes. The guard reads
// MultiEdit inputs, but no recording can show a native MultiEdit deny
// while the client has no such tool. So the MultiEdit paths keep
// active-unverified; this check gives the reason for the installed
// version without a model.
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

	probe := provider.snapshot()
	t.Logf("main requests: %d; tools offered: %v", probe.main, compactNames(probe.offered))
	t.Logf("deferred tool lists: %d; deferred tools: %v", probe.lists, compactNames(probe.deferred))

	if search, ok := probe.results[claudeSearchCallID]; ok {
		t.Logf("ToolSearch select:MultiEdit: error %v, %q", search.isError, search.text)
	}

	if call, ok := probe.results[claudeMultiEditCall]; ok {
		t.Logf("MultiEdit call: error %v, %q", call.isError, call.text)
	}

	assert.Empty(t, probe.multiEditProblems(), "the probe shows that the client has no MultiEdit tool (run error: %v)", runErr)

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

// claudeFakeResult is the result that a fake client sends for one
// scripted call. A nil content means that the client sends none.
type claudeFakeResult struct {
	isError bool
	content any
}

// claudeFakeClient describes the requests of a fake Claude Code: its
// tools, the text that lists its deferred tools, and its results for the
// two scripted calls.
type claudeFakeClient struct {
	tools  []string
	list   string
	search claudeFakeResult
	call   claudeFakeResult
}

// run sends the requests of one scripted conversation to the provider:
// the prompt, then the conversation after each tool result. The provider
// reads only the tools, the texts, and the tool results, so the requests
// leave out the assistant turns. It returns the reply streams in order.
func (c claudeFakeClient) run(t *testing.T, provider *claudeProvider) []string {
	t.Helper()

	tools := make([]map[string]any, 0, len(c.tools))
	for _, name := range c.tools {
		tools = append(tools, map[string]any{"name": name})
	}

	opening := []map[string]any{{"type": "text", "text": c.list}, {"type": "text", "text": "Follow the scripted steps."}}
	messages := []map[string]any{{"role": "user", "content": opening}}

	var replies []string

	post := func() {
		body, err := json.Marshal(map[string]any{"model": "claude-haiku", "stream": true, "tools": tools, "messages": messages})
		require.NoError(t, err)

		resp, err := http.Post(provider.server.URL+"/v1/messages", "application/json", strings.NewReader(string(body)))
		require.NoError(t, err)

		reply, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		require.Equal(t, http.StatusOK, resp.StatusCode)

		replies = append(replies, string(reply))
	}

	post()

	steps := []struct {
		id     string
		result claudeFakeResult
	}{{claudeSearchCallID, c.search}, {claudeMultiEditCall, c.call}}

	for _, step := range steps {
		if step.result.content == nil {
			continue
		}

		block := map[string]any{"type": "tool_result", "tool_use_id": step.id, "content": step.result.content}
		if step.result.isError {
			block["is_error"] = true
		}

		messages = append(messages, map[string]any{"role": "user", "content": []map[string]any{block}})
		post()
	}

	return replies
}

// The deferred tool list and the two answers in the form that Claude
// Code 2.1.325 sends them to the loopback provider.
const (
	claudeFakeList = "<system-reminder>\nThe following deferred tools are now available via ToolSearch. " +
		"Use ToolSearch with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\n" +
		"NotebookEdit\nWebFetch\n\nAvailable agent types for the Agent tool:\n- claude: Catch-all agent.\n</system-reminder>"
	claudeFakeRefusal = "<tool_use_error>Error: No such tool available: MultiEdit</tool_use_error>"
)

// TestClaudeMultiEditProbeVerdict sends the requests of fake clients to
// the scripted provider, with no Claude Code. The verdict passes only a
// client that has no MultiEdit tool, and it blocks a probe whose
// scripted steps did not run. The third case is a client that defers
// MultiEdit, loads it through ToolSearch, and refuses the call because
// the file is unread: a check that reads only the tools field and any
// refusal that names MultiEdit passes that client.
func TestClaudeMultiEditProbeVerdict(t *testing.T) {
	noMatch := claudeFakeResult{content: claudeSearchNoMatch}
	refused := claudeFakeResult{isError: true, content: claudeFakeRefusal}
	searchTools := []string{"Edit", "Read", claudeToolSearch, "Write"}

	cases := []struct {
		name   string
		client claudeFakeClient
		// want holds a part of each problem, in order.
		want []string
	}{
		{
			name:   "client without MultiEdit",
			client: claudeFakeClient{tools: searchTools, list: claudeFakeList, search: noMatch, call: refused},
		},
		{
			name:   "client without ToolSearch",
			client: claudeFakeClient{tools: []string{"Edit", "Read", "Write"}, call: refused},
		},
		{
			name: "deferred MultiEdit that ToolSearch loads and an unread-file refusal",
			client: claudeFakeClient{
				tools:  append(slices.Clone(searchTools), "DeferredToolPlaceholder"),
				list:   "<system-reminder>deferred tools are now available via ToolSearch:\nMultiEdit\nNotebookEdit</system-reminder>",
				search: claudeFakeResult{content: `<functions><function>{"name":"MultiEdit"}</function></functions>`},
				call: claudeFakeResult{isError: true,
					content: "<tool_use_error>MultiEdit: File has not been read yet. Read it first before writing to it.</tool_use_error>"},
			},
			want: []string{
				"lists MultiEdit as a deferred tool",
				"loads a MultiEdit tool",
				`does not say "` + claudeSearchNoMatch,
				`does not say "` + claudeNoSuchTool,
			},
		},
		{
			name: "ToolSearch loads MultiEdit as a tool reference",
			client: claudeFakeClient{tools: searchTools, list: claudeFakeList, call: refused,
				search: claudeFakeResult{content: []map[string]any{{"type": "tool_reference", "tool_name": "MultiEdit"}}}},
			want: []string{"loads a MultiEdit tool", `does not say "` + claudeSearchNoMatch},
		},
		{
			name: "MultiEdit in a tagged deferred tool list",
			client: claudeFakeClient{tools: searchTools, search: noMatch, call: refused,
				list: "<available-deferred-tools>\nMultiEdit\nNotebookEdit\n</available-deferred-tools>"},
			want: []string{"lists MultiEdit as a deferred tool"},
		},
		{
			name: "inline MultiEdit that runs the call",
			client: claudeFakeClient{tools: []string{"Edit", "MultiEdit", "Read", "Write"},
				call: claudeFakeResult{content: "The file has been updated."}},
			want: []string{"offers a MultiEdit tool", "runs the MultiEdit call"},
		},
		{
			name:   "no request offers the edit tools",
			client: claudeFakeClient{tools: []string{"Read"}, call: refused},
			want:   []string{"blocked: the provider saw no request"},
		},
		{
			name:   "ToolSearch without a deferred tool list",
			client: claudeFakeClient{tools: searchTools, search: noMatch, call: refused},
			want:   []string{"blocked: the client offers ToolSearch"},
		},
		{
			name:   "no result for the scripted calls",
			client: claudeFakeClient{tools: searchTools, list: claudeFakeList},
			want: []string{
				"blocked: the client sent no result for the scripted ToolSearch call",
				"blocked: the client sent no result for the scripted MultiEdit call",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newClaudeProvider(t, "/scratch/notes/multi.txt")
			tc.client.run(t, provider)

			problems := provider.snapshot().multiEditProblems()
			require.Len(t, problems, len(tc.want), "problems: %q", problems)

			for i, want := range tc.want {
				assert.Contains(t, problems[i], want)
			}
		})
	}
}

// TestClaudeProviderScript checks the order of the scripted replies: a
// ToolSearch call first when the client offers ToolSearch, then the
// MultiEdit call, then the final text.
func TestClaudeProviderScript(t *testing.T) {
	refused := claudeFakeResult{isError: true, content: claudeFakeRefusal}

	withSearch := claudeFakeClient{tools: []string{"Edit", claudeToolSearch, "Write"}, list: claudeFakeList,
		search: claudeFakeResult{content: claudeSearchNoMatch}, call: refused}
	replies := withSearch.run(t, newClaudeProvider(t, "/scratch/notes/multi.txt"))

	require.Len(t, replies, 3)
	assert.Contains(t, replies[0], `"name":"ToolSearch"`)
	assert.Contains(t, replies[0], `select:MultiEdit`)
	assert.Contains(t, replies[1], `"name":"MultiEdit"`)
	assert.Contains(t, replies[2], `"text":"`+claudeNativeReply+`"`)

	withoutSearch := claudeFakeClient{tools: []string{"Edit", "Write"}, call: refused}
	replies = withoutSearch.run(t, newClaudeProvider(t, "/scratch/notes/multi.txt"))

	require.Len(t, replies, 2)
	assert.Contains(t, replies[0], `"name":"MultiEdit"`)
	assert.Contains(t, replies[1], `"stop_reason":"end_turn"`)
}
