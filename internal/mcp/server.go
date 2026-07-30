// Package mcp serves the seamark index over the Model Context Protocol's
// stdio transport: newline-delimited JSON-RPC 2.0, the same hand-rolled
// approach the LSP server took (RFC-001 §6.2). Five tools, not forty —
// tool definitions are paid in tokens on every agent turn.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/store"
)

// maxLine bounds a single incoming message; a diff for `check` is the
// largest legitimate payload.
const maxLine = 16 * 1024 * 1024

// Server answers MCP requests from the index at root. Every tool call
// self-repairs freshness first (the indexer's fast path makes that free
// when nothing changed), so answers never come from a stale graph.
type Server struct {
	root    string
	dbPath  string
	version string
	logf    func(format string, a ...any)
}

// New builds a server for the workspace at root; logf receives
// operational notes (stderr in the CLI — stdout is protocol-only).
func New(root, dbPath, version string, logf func(format string, a ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	return &Server{root: root, dbPath: dbPath, version: version, logf: logf}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

const (
	codeParse            = -32700
	codeMethodNotFound   = -32601
	codeInvalidParams    = -32602
	codeInternal         = -32603
	codeInvalidRequest   = -32600
	codeResourceNotFound = -32002
)

// Serve reads newline-delimited JSON-RPC messages until r closes. It
// never returns on a bad message — a protocol server that dies on one
// malformed line takes the whole agent session with it. That includes
// an over-length frame: it is rejected in-band and serving continues,
// rather than tearing the connection down (bufio.Scanner's fatal
// ErrTooLong is exactly the failure mode we must not have here).
func (s *Server) Serve(r io.Reader, w io.Writer) error {
	br := bufio.NewReader(r)

	for {
		line, tooLong, err := readFrame(br, maxLine)

		if tooLong {
			s.reply(w, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: codeInvalidRequest,
					Message: fmt.Sprintf("request exceeds %d-byte limit", maxLine)}})
		} else if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			s.dispatch(w, trimmed)
		}

		if err != nil {
			if err == io.EOF {
				return nil
			}

			return err
		}
	}
}

// readFrame reads one newline-delimited frame, bounded to max bytes so a
// giant line can never exhaust memory. On overflow it keeps draining to
// the newline (discarding the excess) and returns tooLong, so the caller
// rejects that one frame and reads the next normally. The returned line
// excludes the trailing newline; a final line without one is returned
// with err == io.EOF.
func readFrame(br *bufio.Reader, limit int) (line []byte, tooLong bool, err error) {
	var buf []byte

	for {
		b, e := br.ReadByte()
		if e != nil {
			return buf, tooLong, e
		}

		if b == '\n' {
			return buf, tooLong, nil
		}

		if len(buf) < limit {
			buf = append(buf, b)
		} else {
			tooLong = true // keep draining, but stop growing
		}
	}
}

// dispatch parses one frame and writes its response. A request without
// an id is a notification (nothing in our subset acts on one); a parse
// failure replies with a null id, per JSON-RPC.
func (s *Server) dispatch(w io.Writer, line []byte) {
	var req request

	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(w, response{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: codeParse, Message: "parse error"}})

		return
	}

	if len(req.ID) == 0 || string(req.ID) == "null" {
		return
	}

	result, rpcErr := s.handle(req)
	s.reply(w, response{JSONRPC: "2.0", ID: req.ID, Result: result, Error: rpcErr})
}

func (s *Server) reply(w io.Writer, resp response) {
	data, err := json.Marshal(resp)
	if err != nil {
		s.logf("mcp: marshal response: %v", err)

		return
	}

	if _, err := w.Write(append(data, '\n')); err != nil {
		s.logf("mcp: write response: %v", err)
	}
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.initialize(req.Params), nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefs}, nil
	case "tools/call":
		return s.callTool(req.Params)
	case "resources/list":
		return map[string]any{"resources": resourceDefs}, nil
	case "resources/read":
		return s.readResource(req.Params)
	case "prompts/list":
		return map[string]any{"prompts": promptDefs}, nil
	case "prompts/get":
		return s.getPrompt(req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
}

func (s *Server) initialize(params json.RawMessage) any {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}

	_ = json.Unmarshal(params, &p)

	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2025-06-18"
	}

	return map[string]any{
		// Echo the client's version: this server depends on nothing
		// version-specific in the core initialize/tools/resources flow.
		"protocolVersion": p.ProtocolVersion,
		"capabilities": map[string]any{
			"tools":     map[string]any{},
			"resources": map[string]any{},
			"prompts":   map[string]any{},
		},
		"serverInfo": map[string]any{"name": "seamark", "version": s.version},
		// The mental model here came verbatim from an agent's post-session
		// self-review: encode it up front instead of letting every agent
		// re-derive it.
		"instructions": "seamark answers orientation, risk, and history questions about this repo: " +
			"orient once before the first edit, why for unfamiliar code, change_set before " +
			"a multi-file edit, check on a diff before committing, expand to read any ref a " +
			"report returned. Answers are always current (the index self-repairs on every call) " +
			"and empirical (co-change means usually, not must). For pinpoint lookups of a " +
			"known file or symbol, direct reads are cheaper — use seamark before the edit, " +
			"direct tools for the edit itself.",
	}
}

// withStore refreshes the index if the workspace changed, then runs fn
// against the opened store.
func (s *Server) withStore(fn func(st *store.Store) (string, error)) (string, error) {
	sum, err := index.Run(index.Options{Root: s.root, DBPath: s.dbPath, Logf: s.logf})
	if err != nil {
		// A broken refresh (e.g. not a git repo yet) must not kill reads:
		// answer from what exists, the staleness is logged.
		s.logf("mcp: index refresh: %v", err)
	} else if !sum.Skipped {
		s.logf("mcp: index refreshed")
	}

	st, err := store.Open(s.dbPath)
	if err != nil {
		return "", fmt.Errorf("open index: %w", err)
	}
	defer func() { _ = st.Close() }() // read-only surface, nothing to lose

	return fn(st)
}

func (s *Server) callTool(params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}

	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: err.Error()}
	}

	run, ok := toolRunners[p.Name]
	if !ok {
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
	}

	text, err := run(s, p.Arguments)
	if err != nil {
		// Tool failures travel in-band (isError), not as protocol errors:
		// the model should see "nothing matches X" and correct course.
		return toolResult(err.Error(), true), nil
	}

	return toolResult(text, false), nil
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// toolRunners maps tool names to implementations. Each returns the full
// report text; freshness and store lifecycle live in withStore.
var toolRunners = map[string]func(*Server, json.RawMessage) (string, error){
	"orient": func(s *Server, _ json.RawMessage) (string, error) {
		return s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Orient(&b, st, s.root)

			return b.String(), err
		})
	},

	"why": func(s *Server, args json.RawMessage) (string, error) {
		var p struct {
			Query string `json:"query"`
		}

		if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Query) == "" {
			return "", fmt.Errorf("why needs {\"query\": \"<symbol or file>\"}")
		}

		return s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Why(&b, st, s.root, p.Query)

			return b.String(), err
		})
	},

	"change_set": func(s *Server, args json.RawMessage) (string, error) {
		var p struct {
			Files []string `json:"files"`
		}

		if err := json.Unmarshal(args, &p); err != nil || len(p.Files) == 0 {
			return "", fmt.Errorf("change_set needs {\"files\": [\"path\", …]}")
		}

		return s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.ChangeSet(&b, st, s.root, p.Files)

			return b.String(), err
		})
	},

	"expand": func(s *Server, args json.RawMessage) (string, error) {
		var p struct {
			Ref string `json:"ref"`
		}

		if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Ref) == "" {
			return "", fmt.Errorf("expand needs {\"ref\": \"<symbol, file:start-end, or lessons:<dir>>\"}")
		}

		return s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Expand(&b, st, s.root, p.Ref)

			return b.String(), err
		})
	},

	"check": func(s *Server, args json.RawMessage) (string, error) {
		var p struct {
			Diff string `json:"diff"`
		}

		if len(args) > 0 {
			if err := json.Unmarshal(args, &p); err != nil {
				return "", fmt.Errorf("check takes {\"diff\": \"<unified diff>\"} (optional)")
			}
		}

		if p.Diff == "" {
			out, err := exec.Command("git", "-C", s.root, "diff", "HEAD").Output()
			if err != nil {
				return "", fmt.Errorf("no diff given and `git diff HEAD` failed: %w", err)
			}

			p.Diff = string(out)
		}

		return s.withStore(func(st *store.Store) (string, error) {
			policy, err := gate.LoadPolicy(s.root)
			if err != nil {
				return "", err
			}

			decision, err := gate.EvalDiff(policy, st, p.Diff)
			if err != nil {
				return "", err
			}

			if err := gate.Audit(s.root, "mcp.check", p.Diff, policy, decision); err != nil {
				s.logf("mcp: audit log: %v", err)
			}

			var b bytes.Buffer
			report.Decision(&b, decision)

			return b.String(), nil
		})
	},
}
