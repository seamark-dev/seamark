// Package mcp serves the seamark index over the Model Context Protocol's
// stdio transport: newline-delimited JSON-RPC 2.0, the same hand-rolled
// approach the LSP server took (RFC-001 §6.2). Five tools, not forty —
// tool definitions are paid in tokens on every agent turn.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
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
	// protocol is the protocol version that initialize negotiated. It
	// is "" before initialize. outputResult reads it to decide on the
	// structuredContent of change_set (contract L1).
	protocol string
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

	// The server answers with the version of the client, so that version
	// is the negotiated one. Serve handles one request at a time, so the
	// field needs no lock.
	s.protocol = p.ProtocolVersion

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
		// The text lives in defs.go beside the onboard prompt: the two
		// state the same rules and change together.
		"instructions": serverInstructions,
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

	out, err := run(s, p.Arguments)
	if err != nil {
		// Tool failures travel in-band (isError), not as protocol errors:
		// the model should see "nothing matches X" and correct course.
		return toolResult(err.Error(), true), nil
	}

	return s.outputResult(out), nil
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// structuredSince is the first protocol version whose tool results can
// hold structuredContent. The versions are dates, so a string compare
// orders them (contract L1).
const structuredSince = "2025-06-18"

// toolOutput is the result of one tool runner. Each Text entry is one
// text part of the content list, in order. Structured is the
// structuredContent object. callTool adds it only for a protocol that
// knows the key.
type toolOutput struct {
	Text       []string
	Structured map[string]any
}

// textOutput wraps the report text of a runner as one text part. Every
// tool except change_set returns exactly this shape, so its bytes stay.
func textOutput(text string, err error) (toolOutput, error) {
	return toolOutput{Text: []string{text}}, err
}

// outputResult builds the tools/call result of a successful runner. An
// older client gets no structuredContent, because it cannot read it.
func (s *Server) outputResult(out toolOutput) map[string]any {
	content := make([]map[string]any, 0, len(out.Text))
	for _, text := range out.Text {
		content = append(content, map[string]any{"type": "text", "text": text})
	}

	result := map[string]any{"content": content, "isError": false}

	if len(out.Structured) > 0 && s.protocol >= structuredSince {
		result["structuredContent"] = out.Structured
	}

	return result
}

// toolRunners maps tool names to implementations. Each returns the full
// report text as its content parts; freshness and store lifecycle live
// in withStore.
var toolRunners = map[string]func(*Server, json.RawMessage) (toolOutput, error){
	"orient": func(s *Server, _ json.RawMessage) (toolOutput, error) {
		return textOutput(s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Orient(&b, st, s.root)

			return b.String(), err
		}))
	},

	"why": func(s *Server, args json.RawMessage) (toolOutput, error) {
		var p struct {
			Query string `json:"query"`
		}

		if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Query) == "" {
			return toolOutput{}, fmt.Errorf("why needs {\"query\": \"<symbol or file>\"}")
		}

		return textOutput(s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Why(&b, st, s.root, p.Query)

			return b.String(), err
		}))
	},

	"change_set": func(s *Server, args json.RawMessage) (toolOutput, error) {
		var p struct {
			Files []string `json:"files"`
		}

		if err := json.Unmarshal(args, &p); err != nil || len(p.Files) == 0 {
			return toolOutput{}, fmt.Errorf("change_set needs {\"files\": [\"path\", …]}")
		}

		// The preparation needs the open store, and withStore closes it
		// when the callback returns. So the callback keeps the result here.
		var out toolOutput

		_, err := s.withStore(func(st *store.Store) (string, error) {
			var err error
			out, err = s.changeSet(st, p.Files)

			return "", err
		})

		return out, err
	},

	"expand": func(s *Server, args json.RawMessage) (toolOutput, error) {
		var p struct {
			Ref string `json:"ref"`
		}

		if err := json.Unmarshal(args, &p); err != nil || strings.TrimSpace(p.Ref) == "" {
			return toolOutput{}, fmt.Errorf("expand needs {\"ref\": \"<symbol, file:start-end, or lessons:<dir>>\"}")
		}

		return textOutput(s.withStore(func(st *store.Store) (string, error) {
			var b bytes.Buffer
			err := report.Expand(&b, st, s.root, p.Ref)

			return b.String(), err
		}))
	},

	"check": func(s *Server, args json.RawMessage) (toolOutput, error) {
		var p struct {
			Diff         string `json:"diff"`
			LessonChecks bool   `json:"lesson_checks"`
		}

		if len(args) > 0 {
			if err := json.Unmarshal(args, &p); err != nil {
				return toolOutput{}, errors.New(checkUsage)
			}
		}

		// The lesson checks read a git work tree. A directory outside git
		// is refused before the index refresh and the audit append, so
		// the refusal writes nothing, as in check --lessons.
		var lessonsRoot string

		if p.LessonChecks {
			root, err := s.lessonsRoot()
			if err != nil {
				return toolOutput{}, err
			}

			lessonsRoot = root
		}

		policyInput := report.PolicyInputStdinDiff

		if p.Diff == "" {
			out, err := exec.Command("git", "-C", s.root, "diff", "HEAD").Output()
			if err != nil {
				return toolOutput{}, fmt.Errorf("no diff given and `git diff HEAD` failed: %w", err)
			}

			p.Diff = string(out)
			policyInput = report.PolicyInputGitDiffHead
		}

		// The lesson checks need the policy decision after withStore
		// returns, so the callback keeps it here.
		var decision *gate.Decision

		text, err := s.withStore(func(st *store.Store) (string, error) {
			policy, err := gate.LoadPolicy(s.root)
			if err != nil {
				return "", err
			}

			decision, err = gate.EvalDiff(policy, st, p.Diff)
			if err != nil {
				return "", err
			}

			if err := gate.Audit(s.root, "mcp.check", p.Diff, policy, decision); err != nil {
				s.logf("mcp: audit log: %v", err)
			}

			var b bytes.Buffer
			report.Decision(&b, decision)

			// Companions before lessons: the forgotten file is the omission
			// a review most often misses, and the lessons block is budgeted
			// and may be long.
			changed := gate.ChangedPaths(p.Diff)
			report.CheckCompanions(&b, st, s.root, changed)
			report.CheckAdvisory(&b, st, s.root, changed)

			return b.String(), nil
		})
		if err != nil || !p.LessonChecks {
			return textOutput(text, err)
		}

		// withStore has closed the index here. SQLite keeps its WAL files
		// next to an open index, and the snapshot must not see them change.
		return textOutput(s.checkLessons(text, lessonsRoot, decision, policyInput))
	},
}

// prepareChangeSet is the preparation service of change_set. It is a
// variable, so a test can make the preparation fail: no repository
// state makes delivery.Prepare fail while the change_set text succeeds.
var prepareChangeSet = delivery.Prepare

// changeSetText writes the change_set text, content[0]. It is a variable,
// so a test can make the text fail after a successful preparation: no
// repository state makes a store read fail between the two calls.
var changeSetText = report.ChangeSetWith

// changeSet answers one change_set call from the open store st
// (contract L1). content[0] is the change_set text of the P2 head, byte
// for byte. A second text part and structuredContent carry the
// lesson-preparation/1 result.
//
// Prepare runs first and returns the one firing record of the call, so
// the text runs without its legacy record. The record is written only
// after the text succeeds: a call that returns an error leaves no
// returned record (P3 review, ruling A). A failed preparation never
// fails the call: the second part names the error, and the fallback
// record keeps the lessons of the text in the firing log.
func (s *Server) changeSet(st *store.Store, files []string) (toolOutput, error) {
	// MCP has no receiver identity and no request context. MCP paths are
	// relative to the workspace root, so the root is also the directory
	// of a relative path. Config stays nil, so Prepare reads lessons.yaml
	// itself and names an undecodable file as invalid.
	prep, prepErr := prepareChangeSet(context.Background(), st, delivery.PrepareRequest{
		Root:           s.root,
		CWD:            s.root,
		Paths:          files,
		Surface:        delivery.SurfaceChangeSet,
		MinedStatus:    minedStatus(st),
		SeamarkVersion: s.version,
	})

	var b bytes.Buffer

	selected, err := changeSetText(&b, st, s.root, files, report.ChangeSetOptions{Record: false})
	if err != nil {
		return toolOutput{}, err
	}

	out := toolOutput{Text: []string{b.String()}}

	if prepErr != nil {
		s.recordFailedPreparation(st, files, selected)
		out.Text = append(out.Text, "lesson preparation unavailable: "+render.Sanitize(prepErr.Error()))

		return out, nil
	}

	// The answer is complete now, so the record of the preparation goes
	// to the firing log. Record sets the event ID that the document names.
	prep.Record()

	// RenderPreparation writes nothing when the preparation adds nothing
	// to the text: no pin, no check, no omission, and no rejected path.
	var supplement bytes.Buffer

	delivery.RenderPreparation(&supplement, prep, delivery.FormSupplement)

	if supplement.Len() > 0 {
		out.Text = append(out.Text, supplement.String())
	}

	out.Structured = map[string]any{"lesson_preparation": prep}

	return out, nil
}

// minedStatus says what st holds for the mined lessons of a preparation.
// withStore refreshes the index and opens it, but a refresh outside git
// fails and leaves an empty database. The rule is the one of lessons
// --prepare: no completed index run is no-index, and a failed meta read
// is unavailable.
func minedStatus(st *store.Store) string {
	at, err := st.GetMeta(store.MetaIndexedAt)

	switch {
	case err != nil:
		return delivery.MinedUnavailable
	case at == "":
		return delivery.MinedNoIndex
	default:
		return delivery.MinedOK
	}
}

// recordFailedPreparation writes the firing record of a change_set call
// whose preparation failed (contract L3). The record keeps the lessons
// of the text, so the firing log still counts the call. It carries no
// pins, because no preparation named them. A call without lessons
// writes nothing, as in the legacy rule. The write is best effort.
func (s *Server) recordFailedPreparation(st *store.Store, files []string, selected []model.Lesson) {
	if len(selected) == 0 {
		return
	}

	// The legacy record named the files as the text names them, so the
	// fallback record does the same.
	names, _ := report.ChangeSetNames(st, s.root, files)

	_, err := reviews.RecordEvent(reviews.AuditEvent{
		Root:          s.root,
		Surface:       string(delivery.SurfaceChangeSet),
		Files:         names,
		Records:       []reviews.EventRecord{{Lessons: selected}},
		Emission:      delivery.EmissionReturned,
		Evaluation:    delivery.EvaluationFailed,
		PathsComplete: false,
	})
	if err != nil {
		s.logf("mcp: firing log: %v", err)
	}
}

// checkUsage is the refusal of malformed check arguments (contracts
// §4.1). It names every argument, so the model can correct the call.
const checkUsage = `check takes {"diff": "<unified diff>", "lesson_checks": true|false} (both optional)`

// lessonsRoot returns the top level of the git work tree of the server
// root. It refuses a root outside git with the text of check --lessons.
// The server cannot import cli, so it repeats that check here.
func (s *Server) lessonsRoot() (string, error) {
	if !history.IsRepo(s.root) {
		return "", fmt.Errorf("lesson checks: %s is not inside a git work tree", s.root)
	}

	return history.ResolveRoot(s.root)
}

// checkLessons appends the lesson verification to the policy text of
// one check call (contract K5): a blank line, the lesson report, and the
// combined line. report.NewCheckLessonsEnvelope derives the combined
// values, so this line and check --lessons print the same verdict.
//
// A violation and a blocking policy stay in band: the result is text,
// not an isError result. Only a usage or root failure of Verify is an
// error. No path here constructs an invoker (AC-1).
func (s *Server) checkLessons(policyText, root string, decision *gate.Decision, policyInput string) (string, error) {
	// Verify sets the verify-timeout deadline itself. The server has no
	// request context, so the call starts from the background context.
	v, err := lessoncheck.Verify(context.Background(), lessoncheck.VerifyRequest{
		Root:           root,
		Caller:         lessoncheck.CallerMCP,
		SeamarkVersion: s.version,
	})
	if err != nil {
		return "", err
	}

	env := report.NewCheckLessonsEnvelope(decision, v, policyInput)

	var b strings.Builder

	b.WriteString(policyText)
	b.WriteString("\n")
	lessoncheck.RenderText(&b, v)
	fmt.Fprintf(&b, "combined: %s (policy %s, lessons %s; CLI exit %d)\n",
		env.Combined.State, env.Combined.Policy, env.Combined.Lessons, env.Combined.ExitCode)

	return b.String(), nil
}
