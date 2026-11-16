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
	"strings"

	"github.com/seamark-dev/seamark/internal/delivery"
	"github.com/seamark-dev/seamark/internal/gate"
	"github.com/seamark-dev/seamark/internal/history"
	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/integration"
	"github.com/seamark-dev/seamark/internal/lessoncheck"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/ownership"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/report"
	"github.com/seamark-dev/seamark/internal/reviews"
	"github.com/seamark-dev/seamark/internal/status"
	"github.com/seamark-dev/seamark/internal/store"
)

// maxLine bounds a single incoming message; a diff for `check` is the
// largest legitimate payload.
const maxLine = 16 * 1024 * 1024

// Server answers MCP requests from the index at root. Every tool call
// self-repairs freshness first (the indexer's fast path makes that free
// when nothing changed), so answers come from a current graph. The one
// exception is a repair that fails, as under a database that records
// another repository. A read then opens with a note, and a verdict
// refuses (withCurrentStore).
type Server struct {
	root    string
	dbPath  string
	version string
	logf    func(format string, a ...any)
	// protocol is the protocol version that initialize negotiated. It
	// is "" before initialize. outputResult reads it to decide on the
	// structuredContent of change_set.
	protocol string
	// client is the raw clientInfo.name of initialize. It is "" before
	// initialize. The usage record cleans it and names only this value.
	client string
	// usageFailed counts the usage appends that failed in this session,
	// and usageErr holds the last failure. Serve logs them when it ends.
	usageFailed int
	usageErr    error
	// binary compares the seamark binary on disk with the binary that
	// this server started from. callTool reads it on every call.
	binary binaryWatch
}

// New builds a server for the workspace at root; logf receives
// operational notes (stderr in the CLI — stdout is protocol-only).
func New(root, dbPath, version string, logf func(format string, a ...any)) *Server {
	if logf == nil {
		logf = func(string, ...any) {}
	}

	return &Server{root: root, dbPath: dbPath, version: version, logf: logf, binary: startBinaryWatch(logf)}
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
	defer s.logUsageFailures()

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
		return s.countedCall(req.Params)
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
		ClientInfo      struct {
			Name string `json:"name"`
		} `json:"clientInfo"`
	}

	_ = json.Unmarshal(params, &p)

	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "2025-06-18"
	}

	// The server answers with the version of the client, so that version
	// is the negotiated one. Serve handles one request at a time, so the
	// fields need no lock.
	s.protocol = p.ProtocolVersion
	s.client = p.ClientInfo.Name

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

// The notes that open an answer from an index that did not refresh. An
// agent acts on the answer, and the log of the server does not reach it.
const (
	// staleNote follows the refusal of a move when the recorded root does
	// not read, so no note can name it.
	staleNote = "note: seamark did not refresh this index, because a rebuild here could move the root of " +
		"another repository; this answer can miss recent changes.\n"
	// brokenNote follows any other failed refresh. The log names the cause.
	brokenNote = "note: seamark could not refresh this index; this answer can miss recent changes.\n"
)

// refusedNote follows the refusal of a move of the root that o records.
// The answer describes that root, and it can hold symbols and effects
// that this workspace does not have, so the note names the root.
func refusedNote(o ownership.State) string {
	return "note: seamark did not refresh this index, because it " + status.RecordedClause(o) +
		" and a rebuild here would move its root; this answer describes " + render.Sanitize(o.Recorded) +
		" and can differ from this workspace.\n"
}

// matchNote follows a skip under the index of another root, as for a
// sibling worktree at one commit or a copied checkout whose file refuses
// writes. The answer is current, but it still describes that root.
func matchNote(o ownership.State) string {
	return "note: this index " + status.RecordedClause(o) + "; it matches this workspace now, and this answer describes " +
		render.Sanitize(o.Recorded) + ".\n"
}

// refreshed is what one refresh leaves for the answer.
type refreshed struct {
	// failure says why the refresh failed, or it is nil.
	failure error
	// owner holds the facts of the database that index.Run read for a skip
	// or a refusal, so no answer probes it again. It is zero after a build,
	// which records this workspace, and after any other failure.
	owner ownership.State
}

// recordsAnotherRoot reports facts o of an index that records another
// root than the workspace. The answer then describes that root.
func recordsAnotherRoot(o ownership.State) bool {
	return o.Recorded != "" && o.Root != ownership.RootSame
}

// describedRoot returns the root that an answer over the facts o
// describes: the root that the index records, else root. The CLI index
// readers read the same root, so both surfaces describe one root.
func describedRoot(o ownership.State, root string) string {
	if recordsAnotherRoot(o) {
		return o.Recorded
	}

	return root
}

// matched reports a skip under the index of another root. A skip happens
// only when the fingerprint of this workspace equals the recorded one, so
// the index matches this workspace, although its answer describes the
// recorded root. The note and the workspace line of the status resource
// both read this one test.
func (r refreshed) matched() bool {
	return r.failure == nil && recordsAnotherRoot(r.owner)
}

// note returns the note that opens an answer after this refresh, or "".
// A skip under the index of another root gets the note that names it.
// The note depends on what the index records, not on the route of a
// rebuild: a file that refuses writes keeps its skip without a route, and
// its answer still describes that root.
func (r refreshed) note() string {
	switch {
	case r.matched():
		return matchNote(r.owner)
	case r.failure == nil:
		return ""
	case errors.Is(r.failure, index.ErrRootMove) && r.owner.Recorded != "":
		return refusedNote(r.owner)
	case errors.Is(r.failure, index.ErrRootMove):
		return staleNote
	default:
		return brokenNote
	}
}

// named returns the root that the answer after this refresh describes.
func (r refreshed) named(root string) string {
	return describedRoot(r.owner, root)
}

// withNote opens text, or the error, with note.
func withNote(note, text string, err error) (string, error) {
	switch {
	case note == "":
		return text, err
	case err != nil:
		return "", fmt.Errorf("%s%w", note, err)
	default:
		return note + text, nil
	}
}

// withStore refreshes the index if the workspace changed, then runs fn
// against the opened store. A failed refresh must not kill reads, so the
// answer comes from what exists. It opens with a note, also when it is an
// error such as "nothing matches", because the index can be the cause.
func (s *Server) withStore(fn func(st *store.Store) (string, error)) (string, error) {
	return s.withRefreshedStore(func(st *store.Store, _ refreshed) (string, error) { return fn(st) })
}

// withRefreshedStore is withStore for an answer that also needs what the
// refresh found, as the root that the index records.
func (s *Server) withRefreshedStore(fn func(st *store.Store, r refreshed) (string, error)) (string, error) {
	r := s.refresh()

	st, err := s.open(r.failure)
	if err != nil {
		return "", err
	}
	defer func() { _ = st.Close() }() // read-only surface, nothing to lose

	text, err := fn(st, r)

	return withNote(r.note(), text, err)
}

// withCurrentStore is withStore for a verdict. A verdict needs a current
// graph: changed lines inside an old symbol still map to it, so a stale
// graph can allow what a current one denies. So any failed refresh ends
// the call, as it ends `seamark check`. A current index of another root
// keeps its verdict, as for a sibling worktree at one commit, and note
// names that root. The caller opens the whole answer with note, because
// it can put lines before the text of fn.
func (s *Server) withCurrentStore(fn func(st *store.Store) (string, error)) (text, note string, err error) {
	r := s.refresh()
	if r.failure != nil {
		return "", "", r.failure
	}

	st, err := s.open(nil)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = st.Close() }() // read-only surface, nothing to lose

	text, err = fn(st)

	return text, r.note(), err
}

// refresh refreshes the index and returns what it found. The log names
// each failure. The facts of the database come from index.Run, so a
// refresh probes the database no second time.
func (s *Server) refresh() refreshed {
	sum, err := index.Run(index.Options{Root: s.root, DBPath: s.dbPath, Logf: s.logf})

	switch {
	case err != nil:
		s.logf("mcp: index refresh: %v", err)

		owner, _ := index.RefusedState(err)

		return refreshed{failure: err, owner: owner}
	case !sum.Skipped:
		s.logf("mcp: index refreshed")
	}

	return refreshed{owner: sum.Ownership}
}

// ownerFacts returns the facts of the database for the status resource.
// A refresh that failed for another reason gave none, so it reads them
// as the CLI status does. Only that failure pays for the read.
func (s *Server) ownerFacts(r refreshed) ownership.State {
	if r.failure == nil || errors.Is(r.failure, index.ErrRootMove) {
		return r.owner
	}

	db := store.AbsolutePath(s.dbPath)
	if db == "" {
		db = store.DefaultPath(s.root)
	}

	return ownership.Gather(s.root, db)
}

// open opens the index after refresh. After a refused move the open is
// read-only, so the file also keeps its schema. A file that this open
// cannot read gives the refusal, because it names the route.
func (s *Server) open(failure error) (*store.Store, error) {
	if errors.Is(failure, index.ErrRootMove) {
		st, err := store.OpenReadOnly(s.dbPath)
		if err != nil {
			return nil, failure
		}

		return st, nil
	}

	st, err := store.Open(s.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open index: %w", err)
	}

	return st, nil
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

	// The call notes open every result, also an error. An agent acts on
	// the reply, and the log of the server does not reach it. The note on
	// ignored keys goes first, because it is about this call.
	notes := unknownArgsNote(p.Name, p.Arguments) + s.binary.note(s.version, s.logf)

	if err != nil {
		// Tool failures travel in-band (isError), not as protocol errors:
		// the model should see "nothing matches X" and correct course.
		return toolResult(notes+err.Error()+s.newerSchemaAdvice(err), true), nil
	}

	result := s.outputResult(out)
	prefixNotes(result, notes)

	return result, nil
}

// newerSchemaAdvice returns a line for the refusal of a newer database,
// or "". The store text says "upgrade seamark". A long-running server
// can be the old binary after an upgrade, and then only a restart helps.
func (s *Server) newerSchemaAdvice(err error) string {
	var newer *store.NewerSchemaError
	if !errors.As(err, &newer) {
		return ""
	}

	return "\nThis MCP server runs seamark " + s.version + " until it restarts. If a newer seamark is " +
		"installed, restart the agent or reconnect its MCP server; otherwise upgrade seamark first."
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

// structuredSince is the first protocol version whose tool results can
// hold structuredContent. The versions are dates, so a string compare
// orders them.
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
		return textOutput(s.withRefreshedStore(func(st *store.Store, r refreshed) (string, error) {
			var b bytes.Buffer
			err := report.OrientNamed(&b, st, s.root, r.named(s.root))

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

		// The check runs before withStore. So a rejected call refreshes
		// no index and writes no firing record.
		if err := report.CheckChangeSetFiles(p.Files); err != nil {
			return toolOutput{}, err
		}

		// The preparation needs the open store, and withStore closes it
		// when the callback returns. So the callback keeps the result here.
		// The first text part goes through withStore, which can add a note.
		// The preparation document gets the facts of the same refresh.
		var out toolOutput

		text, err := s.withRefreshedStore(func(st *store.Store, r refreshed) (string, error) {
			var err error

			if out, err = s.changeSet(st, p.Files, r); err != nil {
				return "", err
			}

			return out.Text[0], nil
		})
		if err != nil {
			return toolOutput{}, err
		}

		out.Text[0] = text

		// A client can show structuredContent in place of the text parts, and
		// the blast radius then never reaches the agent. So the structured
		// result repeats the first part. The key gets its value here, after
		// withStore adds its note, because that note warns of a stale answer.
		// The same string gives the report one renderer and one sanitizing
		// path. A failed preparation has no structured result, so it gets no
		// key.
		if out.Structured != nil {
			out.Structured["change_set_report"] = text
		}

		return out, nil
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
		source := gate.SourceDiffArgument

		if p.Diff == "" {
			out, err := gate.GitDiffHead(s.root)
			if err != nil {
				return toolOutput{}, fmt.Errorf("%w; %s", err, gate.DiffRouteMCP)
			}

			p.Diff = out
			policyInput = report.PolicyInputGitDiffHead
			source = gate.SourceGitDiffHead
		}

		// The lesson checks need the policy decision and the changed paths
		// after withCurrentStore returns, so the callback keeps them here.
		// It keeps a failed audit append too, because its note ends the
		// text.
		var (
			decision *gate.Decision
			changed  []string
			auditErr error
		)

		// The note of the refresh qualifies the whole answer, so it opens
		// the text, also before the combined line of the lesson checks.
		text, note, err := s.withCurrentStore(func(st *store.Store) (string, error) {
			policy, err := gate.LoadPolicy(s.root)
			if err != nil {
				return "", err
			}

			decision, err = gate.EvalDiff(policy, st, p.Diff, source)
			if err != nil {
				return "", err
			}

			if auditErr = gate.Audit(s.root, "mcp.check", p.Diff, policy, decision); auditErr != nil {
				s.logf("mcp: audit log: %v", auditErr)
			}

			var b bytes.Buffer
			report.Decision(&b, decision)

			// Companions before lessons: the forgotten file is the omission
			// a review most often misses, and the lessons block is budgeted
			// and may be long.
			changed = gate.ChangedPaths(p.Diff)
			report.CheckCompanions(&b, st, s.root, changed)
			report.CheckAdvisory(&b, st, s.root, changed)

			return b.String(), nil
		})
		if err != nil {
			return textOutput(withNote(note, text, err))
		}

		if !p.LessonChecks {
			// The agent reads no server log, so a lost audit record is
			// named in the text. The advisory can list a pin whose check
			// exists, and the last line says that it did not run.
			return textOutput(note+text+trailer(auditNote(auditErr), report.LessonChecksNotRequested(s.root, changed)), nil)
		}

		// withCurrentStore has closed the index here. SQLite keeps its WAL
		// files next to an open index, and the snapshot must not see them
		// change.
		text, err = s.checkLessons(text, lessonsRoot, decision, checkInputs{
			policyInput: policyInput, changed: changed, auditErr: auditErr,
		})

		return textOutput(withNote(note, text, err))
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

// changeSet answers one change_set call from the open store st.
// content[0] is exactly the text of report.ChangeSetWith, byte for byte:
// the preparation never changes it. A second text part and structuredContent carry the
// lesson-preparation/1 result. The change_set runner adds the key
// change_set_report beside that document, because only the runner holds
// the final content[0].
//
// Prepare runs first and returns the one firing record of the call, so
// the text runs without its legacy record. The record is written only
// after the text succeeds: a call that returns an error leaves no
// returned record. A failed preparation never fails the call: the
// second part names the error, and the fallback record keeps the
// lessons of the text in the firing log.
//
// r is the refresh of the call. The note of content[0] says that the
// index was not refreshed or records another root, and a client that
// reads only the preparation document needs the same facts there.
func (s *Server) changeSet(st *store.Store, files []string, r refreshed) (toolOutput, error) {
	// MCP has no receiver identity and no request context. MCP paths are
	// relative to the workspace root, so the root is also the directory
	// of a relative path. Config stays nil, so Prepare reads lessons.yaml
	// itself and names an undecodable file as invalid.
	//
	// The badges of each check come from the capability report of the
	// workspace, so preparation computes no badge. Every change_set call
	// runs this read, so the narrow reading reads only the hook
	// documents.
	capability := integration.LessonCapability(integration.Builtin().InspectLessonHooks(s.root), s.root,
		integration.SelfIdentity(s.version))

	prep, prepErr := prepareChangeSet(context.Background(), st, delivery.PrepareRequest{
		Root:           s.root,
		CWD:            s.root,
		Paths:          files,
		Surface:        delivery.SurfaceChangeSet,
		MinedStatus:    minedStatus(st),
		MinedFreshness: minedFreshness(r),
		MinedRoot:      minedRoot(r, s.root),
		SeamarkVersion: s.version,
		Capability:     &capability,
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
	// to the text: no pin, no check, no omission, no rejected path, and
	// no invalid guidance or checks. The text drops the pins of an
	// invalid lessons.yaml without a word, so the supplement says why.
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

// minedFreshness is the mined freshness of a preparation after refresh
// r: a failed or refused refresh leaves the index as it was.
func minedFreshness(r refreshed) string {
	if r.failure != nil {
		return delivery.MinedNotRefreshed
	}

	return ""
}

// minedRoot is the root that the index records when it is another
// root than root, else "". refreshed.named gives the root the answer
// describes.
func minedRoot(r refreshed, root string) string {
	if named := r.named(root); named != root {
		return named
	}

	return ""
}

// recordFailedPreparation writes the firing record of a change_set call
// whose preparation failed. The record keeps the lessons of the text,
// so the firing log still counts the call. It carries no pins, because
// no preparation named them. A call without lessons writes nothing, as
// in the legacy rule. The write is best effort.
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

// checkUsage is the refusal of malformed check arguments. It names
// every argument, so the model can correct the call.
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

// checkInputs holds what the policy path of one check call leaves for
// the lesson verification.
type checkInputs struct {
	policyInput string   // report.PolicyInputStdinDiff or report.PolicyInputGitDiffHead
	changed     []string // the changed paths of the diff that the policy read
	auditErr    error    // the error of the policy audit append, or nil
}

// auditNote names a failed append of the policy audit record, or gives
// "". The verdict stays, because the audit is best effort.
func auditNote(err error) string {
	if err == nil {
		return ""
	}

	return "note: the audit record of this verdict was not written (" + render.Sanitize(err.Error()) + ")\n"
}

// lessonAuditNote names a failed append of the lesson-check audit
// record, or gives "". The result stays, as in lessons --verify.
func lessonAuditNote(err error) string {
	if err == nil {
		return ""
	}

	return "note: the lesson-check audit record was not written (" + render.Sanitize(err.Error()) + ")\n"
}

// trailer joins the lines that end a check text. A blank line sets them
// apart from the block before, as between the other blocks. No line
// gives "", so the text keeps its bytes.
func trailer(lines ...string) string {
	tail := strings.Join(lines, "")
	if tail == "" {
		return ""
	}

	return "\n" + tail
}

// checkLessons adds the lesson verification to the policy text of one
// check call, in the layout of report.WriteCheckLessons: the combined
// line first, then the policy text and the lesson report. The runner puts
// the note of the refresh before the combined line. The CLI exit
// code joins the combined line. report.NewCheckLessonsEnvelope derives
// the combined values, so this text and check --lessons print the same
// verdict. A failed audit append of either record adds a note at the
// end.
//
// A violation and a blocking policy stay in band: the result is text,
// not an isError result. Only a usage or root failure of Verify is an
// error. No path here constructs an invoker.
func (s *Server) checkLessons(policyText, root string, decision *gate.Decision, in checkInputs) (string, error) {
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

	if err := v.AuditError(); err != nil {
		s.logf("mcp: lesson-check audit: %v", err)
	}

	env := report.NewCheckLessonsEnvelope(decision, v, in.policyInput, in.changed)

	var b strings.Builder

	report.WriteCheckLessons(&b, env, policyText, true)
	b.WriteString(trailer(auditNote(in.auditErr), lessonAuditNote(v.AuditError())))

	return b.String(), nil
}
