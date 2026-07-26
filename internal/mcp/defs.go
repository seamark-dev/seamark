package mcp

import (
	"bytes"
	"encoding/json"

	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

// toolDefs is the public tool surface. Descriptions are terse on
// purpose: clients replay them to the model on every turn, so each
// word here is a recurring cost across every agent session.
var toolDefs = []map[string]any{
	{
		"name": "orient",
		"description": "One-screen repo overview: scale, modules, most-called API, " +
			"files that change in groups, recent decisions. Call once before the first edit.",
		"inputSchema": objSchema(nil, nil),
	},
	{
		"name": "why",
		"description": "Explain a symbol or file: definition, callers/callees with confidence " +
			"origins, files that historically change with it, commits that explain it.",
		"inputSchema": objSchema(map[string]any{
			"query": map[string]any{"type": "string",
				"description": "symbol name, FQN, or repo-relative file path"},
		}, []string{"query"}),
	},
	{
		"name": "change_set",
		"description": "Pre-edit blast radius for planned files: what history says changes " +
			"together with them, who calls their symbols, which effects they can reach.",
		"inputSchema": objSchema(map[string]any{
			"files": map[string]any{"type": "array",
				"items":       map[string]any{"type": "string"},
				"description": "repo-relative paths you plan to edit"},
		}, []string{"files"}),
	},
	{
		"name": "check",
		"description": "Evaluate a unified diff's reachable effects against workspace policy " +
			"(.seamark/policy.yaml). Omit diff to use `git diff HEAD`.",
		"inputSchema": objSchema(map[string]any{
			"diff": map[string]any{"type": "string",
				"description": "unified diff; optional"},
		}, nil),
	},
	{
		"name": "expand",
		"description": "Progressive disclosure: turn a ref from another report " +
			"(symbol, FQN, or file:start-end) into source lines.",
		"inputSchema": objSchema(map[string]any{
			"ref": map[string]any{"type": "string",
				"description": "symbol name, FQN, or file:start[-end]"},
		}, []string{"ref"}),
	},
}

func objSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}

	schema := map[string]any{"type": "object", "properties": props}

	if len(required) > 0 {
		schema["required"] = required
	}

	return schema
}

const orientURI = "seamark://orient"

var resourceDefs = []map[string]any{
	{
		"uri":         orientURI,
		"name":        "Repository orientation",
		"description": "The orient report as a readable resource: modules, hot paths, decisions.",
		"mimeType":    "text/plain",
	},
}

func (s *Server) readResource(params json.RawMessage) (any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}

	if p.URI != orientURI {
		return nil, &rpcError{Code: codeResourceNotFound, Message: "unknown resource: " + p.URI}
	}

	text, err := s.withStore(func(st *store.Store) (string, error) {
		var b bytes.Buffer
		err := report.Orient(&b, st, s.root)

		return b.String(), err
	})
	if err != nil {
		return nil, &rpcError{Code: codeInternal, Message: err.Error()}
	}

	return map[string]any{
		"contents": []map[string]any{
			{"uri": orientURI, "mimeType": "text/plain", "text": text},
		},
	}, nil
}

var promptDefs = []map[string]any{
	{
		"name":        "onboard",
		"description": "Walk through this repository using the seamark index before making changes.",
	},
}

const onboardPrompt = `Orient yourself in this repository using the seamark tools, cheapest first:
1. Call orient for the shape of the repo: modules, the most-called API, change hubs.
2. For each change hub or load-bearing symbol relevant to your task, call why — read the co-change partners and recent decisions, not just the call graph.
3. expand only the symbols you actually need to read; prefer refs over whole files.
4. Before editing multiple files, call change_set with your planned files and review what history says you might be forgetting.
5. Before committing, call check on your diff to see the reachable effects and the policy verdict.
Summarize what you learned about the architecture and the risks before proposing any edit.`

func (s *Server) getPrompt(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name string `json:"name"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}

	if p.Name != "onboard" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown prompt: " + p.Name}
	}

	return map[string]any{
		"description": "Repo onboarding walkthrough",
		"messages": []map[string]any{
			{"role": "user", "content": map[string]any{"type": "text", "text": onboardPrompt}},
		},
	}, nil
}
