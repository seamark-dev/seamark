package bench

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// transcriptLines joins stream-json records the way Claude Code writes them.
func transcriptLines(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n") + "\n")
}

func toolUseLine(id, name, input string) string {
	return `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

func toolResultLine(id, text string) string {
	return `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + text + `}]}}`
}

const changeSetResultText = "server/schema.py\\n  usually changes with  web/src/api/generated.ts   2/6 commits, lift 1.3\\n\\nhistory suggests also reviewing\\n  web/src/api/generated.ts   2 shared commits, lift 1.3\\n"

func TestWorkflowTraceKeepsToolOrderAndResultAssociation(t *testing.T) {
	trace := parseWorkflowTrace(transcriptLines(
		toolResultLine("later", `"verdict deny"`),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"checking"},{"type":"tool_use","name":"Read","input":{}},{"type":"tool_use","id":"shared","name":"mcp__seamark__check","input":{}}]}}`,
		toolUseLine("edit", "Edit", `{}`),
		toolUseLine("shared", "mcp__seamark__check", `{}`),
		toolResultLine("shared", `"verdict allow"`),
		toolResultLine("shared", `[{"type":"text","text":"verdict require_approval"}]`),
		toolResultLine("", `"verdict deny"`),
		toolUseLine("later", "Read", `{}`),
	), "companion.go")

	assert.Equal(t, 3, trace.FirstEditSeq, "unnamed calls still occupy a sequence position")
	assert.Equal(t, 1, trace.Edits)
	assert.Equal(t, 2, trace.SeamarkCalls)
	assert.True(t, trace.CheckAfterLastEdit)
	assert.Equal(t, "require_approval", trace.CheckVerdict, "the latest result attaches to the latest call with that ID")
}

func TestParseWorkflowTraceReadsTheFullWorkflow(t *testing.T) {
	stream := transcriptLines(
		`{"type":"system","subtype":"init","model":"claude-test"}`,
		toolUseLine("t1", "Skill", `{"skill":"seamark-plan-change"}`),
		toolResultLine("t1", `"loaded"`),
		toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py","server/presenters.py"]}`),
		// MCP results arrive as a list of text blocks.
		toolResultLine("t2", `[{"type":"text","text":"`+changeSetResultText+`"}]`),
		toolUseLine("t3", "mcp__seamark__why", `{"query":"./web/src/api/generated.ts"}`),
		toolResultLine("t3", `"web/src/api/generated.ts\n"`),
		toolUseLine("t4", "Edit", `{"file_path":"server/schema.py"}`),
		toolUseLine("t5", "MultiEdit", `{"file_path":"server/presenters.py"}`),
		// A repeated activation counts once; the "name" key is accepted too.
		toolUseLine("t6", "Skill", `{"name":"seamark-review-change"}`),
		toolUseLine("t7", "Skill", `{"skill":"seamark-plan-change"}`),
		toolUseLine("t8", "mcp__seamark__check", `{}`),
		toolResultLine("t8", `"verdict  allow (mode: warn)\neffects  fs:write\n"`),
		`{"type":"result","subtype":"success"}`,
	)

	trace := parseWorkflowTrace(stream, "web/src/api/generated.ts")

	assert.True(t, trace.ChangeSetBeforeFirstEdit)
	assert.Equal(t, []string{"server/schema.py", "server/presenters.py"}, trace.ChangeSetFiles)
	assert.True(t, trace.CompanionNamedByChangeSet)
	assert.True(t, trace.WhyFollowedCompanion)
	assert.True(t, trace.CheckAfterLastEdit)
	assert.Equal(t, "allow (mode: warn)", trace.CheckVerdict)
	assert.Equal(t, []string{"seamark-plan-change", "seamark-review-change"}, trace.Activations)
	assert.Equal(t, 3, trace.SeamarkCalls)
	assert.Equal(t, map[string]int{"change_set": 1, "why": 1, "check": 1}, trace.SeamarkToolCalls)
	assert.Equal(t, 2, trace.Edits)
	assert.Equal(t, 4, trace.FirstEditSeq)
}

func TestParseWorkflowTraceOrderRules(t *testing.T) {
	companion := "web/src/api/generated.ts"

	t.Run("change_set after the first edit does not count", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Write", `{"file_path":"server/schema.py"}`),
			toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py"]}`),
			toolResultLine("t2", `"`+changeSetResultText+`"`),
		), companion)

		assert.False(t, trace.ChangeSetBeforeFirstEdit)
		assert.True(t, trace.CompanionNamedByChangeSet, "naming is independent of the edit order")
		assert.Equal(t, 1, trace.FirstEditSeq)
	})

	t.Run("a companion the agent planned itself is not named by the tool", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "mcp__seamark__change_set", `{"files":["server/schema.py","web/src/api/generated.ts"]}`),
			toolResultLine("t1", `"`+changeSetResultText+`"`),
			toolUseLine("t2", "mcp__seamark__why", `{"query":"web/src/api/generated.ts"}`),
		), companion)

		assert.True(t, trace.ChangeSetBeforeFirstEdit, "no edit at all still counts as before the first edit")
		assert.False(t, trace.CompanionNamedByChangeSet)
		assert.False(t, trace.WhyFollowedCompanion, "why cannot follow a companion that was never named")
	})

	t.Run("a companion planned by its absolute path is not named by the tool", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "mcp__seamark__change_set", `{"files":["/tmp/trial/server/schema.py","/tmp/trial/web/src/api/generated.ts"]}`),
			toolResultLine("t1", `"`+changeSetResultText+`"`),
		), companion)

		assert.False(t, trace.CompanionNamedByChangeSet, "change_set accepts absolute paths, so the plan is read the same way")
	})

	t.Run("why before the naming change_set does not follow it", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "mcp__seamark__why", `{"query":"web/src/api/generated.ts"}`),
			toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py"]}`),
			toolResultLine("t2", `"`+changeSetResultText+`"`),
			toolUseLine("t3", "mcp__seamark__why", `{"query":"server/schema.py"}`),
		), companion)

		assert.True(t, trace.CompanionNamedByChangeSet)
		assert.False(t, trace.WhyFollowedCompanion)
		assert.Equal(t, 2, trace.SeamarkToolCalls["why"])
	})

	t.Run("check before the last edit does not count", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Edit", `{"file_path":"server/schema.py"}`),
			toolUseLine("t2", "mcp__seamark__check", `{}`),
			toolResultLine("t2", `"verdict  allow (mode: warn)\n"`),
			toolUseLine("t3", "Edit", `{"file_path":"server/presenters.py"}`),
		), companion)

		assert.False(t, trace.CheckAfterLastEdit)
		assert.Equal(t, "allow (mode: warn)", trace.CheckVerdict, "the verdict is recorded even when the check came early")
	})

	t.Run("a failed check leaves no verdict", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Edit", `{"file_path":"server/schema.py"}`),
			toolUseLine("t2", "mcp__seamark__check", `{}`),
			toolResultLine("t2", `"no index found; run seamark index first"`),
		), companion)

		assert.True(t, trace.CheckAfterLastEdit)
		assert.Empty(t, trace.CheckVerdict)
	})

	t.Run("tool results and prose never create calls", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ghost","content":"mcp__seamark__check Edit Skill"}]}}`,
			`{"type":"assistant","message":{"content":[{"type":"text","text":"I will call mcp__seamark__change_set now"}]}}`,
			`not json at all`,
		), companion)

		assert.Equal(t, WorkflowTrace{}, trace)
	})
}

func TestVerdictLineAndPaths(t *testing.T) {
	assert.Equal(t, "deny (mode: enforce)", verdictLine("effects  fs:write\nverdict  deny (mode: enforce)\n"))
	assert.Equal(t, "", verdictLine("verdicts are not lines\n"))
	assert.Equal(t, "", verdictLine(""))

	assert.True(t, samePath("./web/src/api/generated.ts", "web/src/api/generated.ts"))
	assert.True(t, samePath(`web\src\api\generated.ts`, "web/src/api/generated.ts"))
	assert.False(t, samePath("web/src/api/generated.ts", "web/src/api/client.ts"))
	assert.False(t, samePath("", "web/src/api/generated.ts"))
}

func TestReadAgentSessionParsesInitShapes(t *testing.T) {
	t.Run("object servers and named skills", func(t *testing.T) {
		session := readAgentSession(transcriptLines(
			`{"type":"system","subtype":"init","model":"claude-test","tools":["Read","Skill"],"mcp_servers":[{"name":"seamark","status":"connected"}],"skills":["seamark-plan-change"],"plugins":[{"name":"extra","path":"/x"}]}`,
			`{"type":"result","subtype":"success","duration_ms":9,"num_turns":3,"total_cost_usd":0.02,"modelUsage":{"claude-test":{}},"usage":{"input_tokens":50,"output_tokens":5,"cache_read_input_tokens":150}}`,
		))

		require.True(t, session.InitSeen)
		assert.Equal(t, []string{"Read", "Skill"}, session.Tools)
		assert.Equal(t, []MCPServerState{{Name: "seamark", Status: "connected"}}, session.MCPServers)
		assert.Equal(t, []string{"seamark-plan-change"}, session.Skills)
		assert.Equal(t, []string{"extra"}, session.Plugins)
		assert.True(t, session.ResultSeen)
		assert.True(t, session.Valid)
		assert.Equal(t, "claude-test", session.Usage.Model)
		assert.Equal(t, int64(200), session.Usage.ContextTokens)
		assert.Equal(t, 3, session.Usage.Turns)
		assert.InDelta(t, 0.02, session.Usage.CostUSD, 1e-9)
	})

	t.Run("init model outranks a multi-model result", func(t *testing.T) {
		// A helper call on a second model leaves the result without one
		// model name; the init record still says which model ran.
		session := readAgentSession(transcriptLines(
			`{"type":"system","subtype":"init","model":"claude-haiku-4-5-20251001"}`,
			`{"type":"result","subtype":"success","modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":10},"claude-opus-5":{"inputTokens":20}},"usage":{"input_tokens":30}}`,
		))

		assert.Equal(t, "claude-haiku-4-5-20251001", session.Usage.Model)
		assert.Len(t, session.Usage.ModelUsage, 2)
	})

	t.Run("plain-string servers", func(t *testing.T) {
		session := readAgentSession(transcriptLines(
			`{"type":"system","subtype":"init","mcp_servers":["seamark"],"skills":[{"name":"seamark-review-change"}]}`,
		))

		require.True(t, session.InitSeen)
		assert.Equal(t, []MCPServerState{{Name: "seamark"}}, session.MCPServers)
		assert.Equal(t, []string{"seamark-review-change"}, session.Skills)
		assert.False(t, session.ResultSeen)
	})

	t.Run("provider failure invalidates", func(t *testing.T) {
		session := readAgentSession(transcriptLines(
			`{"type":"system","subtype":"init","model":"claude-test"}`,
			`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected"}}`,
			`{"type":"result","is_error":true,"api_error_status":429,"result":"session limit","usage":{}}`,
		))

		assert.False(t, session.Valid)
		assert.True(t, session.InfrastructureFailure)
		assert.Contains(t, session.InvalidReason, "rate limit")
	})

	t.Run("stub output leaves everything zero", func(t *testing.T) {
		session := readAgentSession([]byte("done\n"))
		assert.Equal(t, agentSession{Valid: true}, session)
	})
}

func TestParseWorkflowTraceCompanionOpenedAndNamedByCheck(t *testing.T) {
	companion := "web/src/api/generated.ts"
	checkWithCompanion := `"verdict  allow (mode: warn)\n\nhistory suggests also reviewing  (usually changes with the diff's files, absent from this diff)\n  web/src/api/generated.ts   2 shared commits with server/schema.py, lift 1.3\n    last fix here: fix: refresh web types (7263562)\n"`

	t.Run("a Read of the companion after change_set named it counts as opened", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Read", `{"file_path":"/tmp/trial/web/src/api/generated.ts"}`),
			toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py"]}`),
			toolResultLine("t2", `"`+changeSetResultText+`"`),
			toolUseLine("t3", "Read", `{"file_path":"/tmp/trial/web/src/api/generated.ts"}`),
		), companion)

		assert.True(t, trace.CompanionNamedByChangeSet)
		assert.True(t, trace.CompanionOpenedAfterNamed)
		assert.False(t, trace.CompanionNamedByCheck)
	})

	t.Run("a Read before the naming does not count", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Read", `{"file_path":"/tmp/trial/web/src/api/generated.ts"}`),
			toolUseLine("t2", "mcp__seamark__change_set", `{"files":["server/schema.py"]}`),
			toolResultLine("t2", `"`+changeSetResultText+`"`),
			toolUseLine("t3", "Read", `{"file_path":"/tmp/trial/web/src/api/other.ts"}`),
		), companion)

		assert.True(t, trace.CompanionNamedByChangeSet)
		assert.False(t, trace.CompanionOpenedAfterNamed)
	})

	t.Run("check names the companion only under its section", func(t *testing.T) {
		trace := parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Edit", `{"file_path":"/tmp/trial/server/schema.py"}`),
			toolUseLine("t2", "mcp__seamark__check", `{}`),
			toolResultLine("t2", `"verdict  allow (mode: warn)\n  note: 1 of 2 changed files have changes outside any indexed symbol (web/src/api/generated.ts)\n"`),
		), companion)

		assert.False(t, trace.CompanionNamedByCheck, "the unindexed-files note quotes a path in the diff")

		trace = parseWorkflowTrace(transcriptLines(
			toolUseLine("t1", "Edit", `{"file_path":"/tmp/trial/server/schema.py"}`),
			toolUseLine("t2", "mcp__seamark__check", `{}`),
			toolResultLine("t2", checkWithCompanion),
			toolUseLine("t3", "mcp__seamark__why", `{"query":"web/src/api/generated.ts"}`),
			toolUseLine("t4", "Edit", `{"file_path":"/tmp/trial/web/src/api/generated.ts"}`),
		), companion)

		assert.True(t, trace.CompanionNamedByCheck)
		assert.False(t, trace.CompanionNamedByChangeSet)
		assert.True(t, trace.WhyFollowedCompanion, "why may follow a companion check named")
		assert.True(t, trace.CompanionOpenedAfterNamed, "an Edit counts as opening")
		assert.False(t, trace.CheckAfterLastEdit, "the companion edit came after the check")
	})

	t.Run("path helpers", func(t *testing.T) {
		assert.True(t, pathIs("/private/var/x/mcp-skills-01/server/cache.py", "server/cache.py"))
		assert.True(t, pathIs("./server/cache.py", "server/cache.py"))
		assert.False(t, pathIs("/x/other/server/cache.py.bak", "server/cache.py"))
		assert.False(t, pathIs("/x/myserver/cache.py", "server/cache.py"), "a suffix must start at a path boundary")
		assert.True(t, companionSuggested("history suggests also reviewing\n  server/cache.py   2 shared commits with a.py, lift 1.3\n", "server/cache.py"))
		assert.False(t, companionSuggested("history suggests also reviewing\n\n  server/cache.py\n", "server/cache.py"), "the section ends at a blank line")
	})
}

func TestPathIsMatchesAbsolutePathsByTailOnly(t *testing.T) {
	assert.True(t, pathIs("/tmp/trial/web/src/api/generated.ts", "web/src/api/generated.ts"))
	assert.True(t, pathIs("./web/src/api/generated.ts", "web/src/api/generated.ts"))
	assert.False(t, pathIs("old/web/src/api/generated.ts", "web/src/api/generated.ts"),
		"a relative path that ends with the companion is another file")
}
