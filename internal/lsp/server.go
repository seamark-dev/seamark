package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/seamark-dev/seamark/internal/index"
	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/parse"
	"github.com/seamark-dev/seamark/internal/render"
	"github.com/seamark-dev/seamark/internal/store"
)

// Diagnostic thresholds: a co-change partner must be this coupled before
// its absence from the current change is worth a note. Deliberately
// conservative — a nagging diagnostic gets the whole feature disabled
// (RFC-001 §10).
const (
	diagMinTogether = 3
	diagMinLift     = 2.0
	diagMaxPerFile  = 5
)

// Options configures Serve.
type Options struct {
	// Root is the workspace directory as given by the client; it is
	// widened to the git toplevel exactly like `seamark index`, so an
	// editor that opens a repo SUBDIRECTORY still reads the repo's index
	// instead of creating a stray empty one.
	Root string
	// DBPath overrides the index location (default: <root>/.seamark/index.db).
	DBPath  string
	Version string
	// Logf receives progress and warnings; nil discards them. Must write
	// to stderr — stdout carries the protocol.
	Logf func(format string, args ...any)
}

// Server answers LSP requests from the seamark index. Requests are handled
// sequentially on one goroutine: didSave triggers a synchronous reindex
// (fast enough at repo scale; the daemon will make this incremental).
type Server struct {
	root    string
	dbPath  string
	version string
	st      *store.Store
	conn    *conn
	logf    func(format string, args ...any)
}

// Serve runs the LSP server over in/out until the client disconnects or
// sends exit. The workspace index is built on startup when absent.
func Serve(opts Options, in io.Reader, out io.Writer) error {
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	root, err := index.ResolveRoot(opts.Root)
	if err != nil {
		return err
	}

	// Editors send symlink-resolved URIs (macOS /tmp → /private/tmp);
	// resolve the root the same way or every path looks out-of-workspace.
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	dbPath := opts.DBPath
	if dbPath == "" {
		dbPath = store.DefaultPath(root)
	}

	if _, err := os.Stat(dbPath); err != nil {
		logf("no index found; building one for %s", root)

		if _, err := index.Run(index.Options{Root: root, DBPath: dbPath, Logf: logf}); err != nil {
			return fmt.Errorf("lsp: initial index: %w", err)
		}
	}

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }() // read-only handle

	s := &Server{root: root, dbPath: dbPath, version: opts.Version,
		st: st, conn: newConn(in, out), logf: logf}

	return s.loop()
}

func (s *Server) loop() error {
	for {
		body, err := s.conn.read()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		var req rpcRequest
		if err := json.Unmarshal(body, &req); err != nil {
			s.logf("warn: undecodable message: %v", err)
			continue
		}

		if req.Method == "exit" {
			return nil
		}

		result, handleErr, known := s.dispatch(&req)

		switch {
		case !known:
			// Unknown requests get MethodNotFound; unknown notifications
			// are ignored per the JSON-RPC spec.
			err = s.conn.respond(req.ID, nil, &rpcError{Code: codeMethodNotFound, Message: req.Method})
		case handleErr != nil:
			s.logf("warn: %s: %v", req.Method, handleErr)

			err = s.conn.respond(req.ID, nil, &rpcError{Code: codeInternalError, Message: handleErr.Error()})
		default:
			err = s.conn.respond(req.ID, result, nil)
		}

		if err != nil {
			return err
		}
	}
}

// dispatch routes one message; known=false for methods this server does
// not implement.
func (s *Server) dispatch(req *rpcRequest) (result any, err error, known bool) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(), nil, true
	case "initialized", "textDocument/didOpen", "textDocument/didClose",
		"textDocument/didChange", "$/cancelRequest", "shutdown":
		return nil, nil, true // lifecycle noise: acknowledged, nothing to do
	case "textDocument/hover":
		var p hoverParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err, true
		}

		r, err := s.handleHover(&p)

		return r, err, true
	case "textDocument/codeLens":
		var p codeLensParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err, true
		}

		r, err := s.handleCodeLens(&p)

		return r, err, true
	case "textDocument/didSave":
		var p didSaveParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, err, true
		}

		return nil, s.handleDidSave(&p), true
	default:
		return nil, nil, false
	}
}

func (s *Server) handleInitialize() initializeResult {
	return initializeResult{
		Capabilities: serverCapabilities{
			HoverProvider:    true,
			CodeLensProvider: codeLensOptions{},
			TextDocumentSync: textDocumentSync{OpenClose: true, Change: 0, Save: saveSync{}},
		},
		ServerInfo: serverInfo{Name: "seamark", Version: s.version},
	}
}

func (s *Server) handleHover(p *hoverParams) (*hoverResult, error) {
	rel, err := s.relPath(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}

	sym, err := s.symbolUnderCursor(rel, p.Position)
	if err != nil || sym == nil {
		return nil, err // nil hover: nothing under the cursor
	}

	md, err := s.hoverMarkdown(sym)
	if err != nil {
		return nil, err
	}

	return &hoverResult{
		Contents: markupContent{Kind: "markdown", Value: md},
		Range: &Range{
			Start: Position{Line: sym.Span.StartLine - 1},
			End:   Position{Line: sym.Span.StartLine - 1, Character: ^uint32(0) >> 1},
		},
	}, nil
}

// symbolUnderCursor resolves what the cursor is on: the identifier at the
// position when it names a known symbol (so hovering a CALL SITE describes
// the callee, like gopls), falling back to the innermost enclosing
// declaration (hovering a body line describes its function).
func (s *Server) symbolUnderCursor(rel string, pos Position) (*model.Symbol, error) {
	enclosing, err := s.st.SymbolAt(rel, pos.Line+1)
	if err != nil {
		return nil, err
	}

	word := s.wordAt(rel, pos)
	if word == "" || (enclosing != nil && word == enclosing.Name) {
		return enclosing, nil
	}

	if target := s.resolveName(word, rel, enclosing); target != nil {
		return target, nil
	}

	return enclosing, nil
}

// resolveName finds the symbol an identifier most plausibly refers to:
// a same-file match first, then a repo-wide unique match. Ambiguity
// yields nil — the enclosing-declaration fallback beats a wrong guess.
func (s *Server) resolveName(word, rel string, enclosing *model.Symbol) *model.Symbol {
	cands, err := s.st.SymbolsByName(word)
	if err != nil || len(cands) == 0 {
		return nil
	}

	// Callable/type symbols outrank consts and vars for hover purposes.
	preferred := make([]model.Symbol, 0, len(cands))

	for _, c := range cands {
		switch c.Kind {
		case model.KindFunction, model.KindMethod, model.KindType:
			preferred = append(preferred, c)
		}
	}

	if len(preferred) == 0 {
		preferred = cands
	}

	// Cross-language name mirrors are common (a Python schema type and
	// its generated TS twin); prefer candidates from the hovered file's
	// own language family before declaring ambiguity.
	if len(preferred) > 1 {
		if fam := parse.FamilyForPath(rel); fam != "" {
			var sameFam []model.Symbol

			for _, c := range preferred {
				if parse.FamilyForPath(c.File) == fam {
					sameFam = append(sameFam, c)
				}
			}

			if len(sameFam) > 0 {
				preferred = sameFam
			}
		}
	}

	var sameFile []model.Symbol

	for _, c := range preferred {
		if c.File == rel {
			sameFile = append(sameFile, c)
		}
	}

	switch {
	case len(sameFile) == 1 && (enclosing == nil || sameFile[0].ID != enclosing.ID):
		return &sameFile[0]
	case len(sameFile) == 0 && len(preferred) == 1:
		return &preferred[0]
	default:
		return nil
	}
}

// wordAt reads the identifier at a position from the file on disk (sync
// kind None: the server never holds buffer contents; on-save flows make
// disk current). Position.Character counts UTF-16 code units per LSP.
func (s *Server) wordAt(rel string, pos Position) string {
	data, err := os.ReadFile(filepath.Join(s.root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}

	lines := strings.Split(string(data), "\n")
	if int(pos.Line) >= len(lines) {
		return ""
	}

	line := lines[pos.Line]

	// UTF-16 offset -> byte offset.
	byteOff, units := 0, uint32(0)

	for i, r := range line {
		if units >= pos.Character {
			byteOff = i
			break
		}

		units += uint32(len(utf16.Encode([]rune{r})))
		byteOff = i + utf8.RuneLen(r)
	}

	isIdent := func(b byte) bool {
		return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
	}

	// Editors put the cursor ON a character; accept being just past one.
	if byteOff >= len(line) || (!isIdent(line[byteOff]) && byteOff > 0 && isIdent(line[byteOff-1])) {
		byteOff--
	}

	if byteOff < 0 || byteOff >= len(line) || !isIdent(line[byteOff]) {
		return ""
	}

	start, end := byteOff, byteOff+1

	for start > 0 && isIdent(line[start-1]) {
		start--
	}

	for end < len(line) && isIdent(line[end]) {
		end++
	}

	return line[start:end]
}

// hoverMarkdown renders the decision layer for one symbol: what it is,
// who calls it, what its file empirically changes with, and the commits
// that explain it.
func (s *Server) hoverMarkdown(sym *model.Symbol) (string, error) {
	var b strings.Builder

	if sym.Sig != "" {
		fmt.Fprintf(&b, "```\n%s\n```\n\n", render.Sanitize(sym.Sig))
	}

	callers, err := s.st.Callers(sym.ID)
	if err != nil {
		return "", err
	}

	guesses := 0

	for _, c := range callers {
		if c.Origin == model.OriginUniqueName {
			guesses++
		}
	}

	fmt.Fprintf(&b, "**%s** `%s` — %d callers", sym.Kind, sym.FQN, len(callers))

	if guesses > 0 {
		fmt.Fprintf(&b, " (%d by name match only)", guesses)
	}

	b.WriteString("\n")

	partners, err := s.st.CoChangePartners(sym.File, diagMinLift, 3)
	if err != nil {
		return "", err
	}

	if len(partners) > 0 {
		b.WriteString("\n**Usually changed with**\n")

		for _, p := range partners {
			fmt.Fprintf(&b, "- %s — %d/%d commits, lift %.1f\n",
				mdCodeSpan(p.File), p.Together, p.Total, p.Lift)
		}
	}

	decisions, err := s.st.DecisionsForFile(sym.File, 3)
	if err != nil {
		return "", err
	}

	if len(decisions) > 0 {
		b.WriteString("\n**Recent decisions**\n")

		for _, d := range decisions {
			marker := ""
			if d.Kind == model.DecisionRevert {
				marker = " ⚠ revert"
			}

			// Titles and authors are attacker-controlled (git history) and
			// hovers render as markdown: code spans keep a title like
			// "[click me](https://evil)" from becoming a live link.
			fmt.Fprintf(&b, "- %s `%.8s` %s (%s)%s\n",
				time.Unix(d.TS, 0).Format("2006-01-02"), d.Ref,
				mdCodeSpan(render.Truncate(render.Sanitize(d.Title), 60)),
				mdCodeSpan(render.Sanitize(d.Author)), marker)
		}
	}

	return b.String(), nil
}

func (s *Server) handleCodeLens(p *codeLensParams) ([]codeLens, error) {
	rel, err := s.relPath(p.TextDocument.URI)
	if err != nil {
		return nil, err
	}

	syms, err := s.st.SymbolsInFile(rel)
	if err != nil {
		return nil, err
	}

	counts, err := s.st.CallerCounts(rel)
	if err != nil {
		return nil, err
	}

	lenses := []codeLens{}

	// One file-level lens for the empirical coupling headline.
	partners, err := s.st.CoChangePartners(rel, diagMinLift, 10)
	if err != nil {
		return nil, err
	}

	if len(partners) > 0 {
		lenses = append(lenses, codeLens{
			Range:   Range{Start: Position{Line: 0}, End: Position{Line: 0}},
			Command: command{Title: fmt.Sprintf("seamark: usually changed with %d files", len(partners))},
		})
	}

	for i := range syms {
		sym := &syms[i]
		if sym.Kind != model.KindFunction && sym.Kind != model.KindMethod {
			continue
		}

		n := counts[sym.ID]
		if n == 0 {
			continue // a zero-caller lens on every helper is noise
		}

		line := sym.Span.StartLine - 1
		lenses = append(lenses, codeLens{
			Range:   Range{Start: Position{Line: line}, End: Position{Line: line}},
			Command: command{Title: fmt.Sprintf("%d callers", n)},
		})
	}

	return lenses, nil
}

// handleDidSave refreshes the index and publishes co-change omission
// diagnostics: strong empirical partners of the saved file that the
// current change does not touch.
func (s *Server) handleDidSave(p *didSaveParams) error {
	rel, err := s.relPath(p.TextDocument.URI)
	if err != nil {
		return err
	}

	start := time.Now()

	if _, err := index.Run(index.Options{Root: s.root, DBPath: s.dbPath, Logf: s.logf}); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}

	s.logf("reindexed %s in %s", s.root, time.Since(start).Round(time.Millisecond))

	modified, err := s.modifiedFiles()
	if err != nil {
		s.logf("warn: git status: %v", err)
		return s.publishDiagnostics(p.TextDocument.URI, nil)
	}

	// A save of an unmodified (committed) file is not a change in
	// progress; clear any stale diagnostics and stay quiet.
	if !modified(rel) {
		return s.publishDiagnostics(p.TextDocument.URI, nil)
	}

	partners, err := s.st.CoChangePartners(rel, diagMinLift, 20)
	if err != nil {
		return err
	}

	// One diagnostic listing every missing partner, strongest first —
	// stacked whole-file diagnostics fight over the single virtual-text
	// slot editors render, and the weakest one can win.
	var missing []string

	for _, partner := range partners {
		if partner.Together < diagMinTogether || modified(partner.File) {
			continue
		}

		missing = append(missing, fmt.Sprintf("%s (%d/%d commits, lift %.1f)",
			partner.File, partner.Together, partner.Total, partner.Lift))

		if len(missing) == diagMaxPerFile {
			break
		}
	}

	diags := []diagnostic{}

	if len(missing) > 0 {
		diags = append(diags, diagnostic{
			Range:    Range{Start: Position{Line: 0}, End: Position{Line: 0}},
			Severity: severityInfo,
			Source:   "seamark",
			Message:  "usually changed together, not in this change: " + strings.Join(missing, "; "),
		})
	}

	return s.publishDiagnostics(p.TextDocument.URI, diags)
}

func (s *Server) publishDiagnostics(uri string, diags []diagnostic) error {
	if diags == nil {
		diags = []diagnostic{} // empty array clears; null does not
	}

	return s.conn.notify("textDocument/publishDiagnostics",
		publishDiagnosticsParams{URI: uri, Diagnostics: diags})
}

// modifiedFiles returns a predicate over repo-relative paths the working
// tree has changed (staged, unstaged, or untracked). Untracked
// directories appear in porcelain output as a single "dir/" entry, so
// files under them match by prefix.
func (s *Server) modifiedFiles() (func(rel string) bool, error) {
	cmd := exec.Command("git", "status", "--porcelain", "-z", "--no-renames")
	cmd.Dir = s.root

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files := map[string]bool{}

	var dirs []string

	for entry := range strings.SplitSeq(string(out), "\x00") {
		// Entries are "XY <path>"; renames are disabled so no second field.
		if len(entry) <= 3 {
			continue
		}

		p := entry[3:]
		if strings.HasSuffix(p, "/") {
			dirs = append(dirs, p)
		} else {
			files[p] = true
		}
	}

	return func(rel string) bool {
		if files[rel] {
			return true
		}

		for _, d := range dirs {
			if strings.HasPrefix(rel, d) {
				return true
			}
		}

		return false
	}, nil
}

// mdCodeSpan wraps untrusted text in a markdown code span, neutralizing
// links, images, and emphasis. The delimiter uses one more backtick than
// the longest run inside, so embedded backticks cannot close it early.
func mdCodeSpan(s string) string {
	longest, run := 0, 0

	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}

	delim := strings.Repeat("`", longest+1)

	return delim + " " + s + " " + delim
}

// relPath converts a file:// URI into a repo-relative slash path.
func (s *Server) relPath(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("bad uri %q: %w", uri, err)
	}

	if u.Scheme != "file" {
		return "", fmt.Errorf("unsupported uri scheme %q", u.Scheme)
	}

	// The root is symlink-resolved at startup; resolve the URI path too,
	// or the textual Rel comparison breaks across symlink aliases.
	p := u.Path
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		p = resolved
	}

	rel, err := filepath.Rel(s.root, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("uri %q is outside the workspace", uri)
	}

	return filepath.ToSlash(rel), nil
}
