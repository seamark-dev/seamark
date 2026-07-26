package lsp

// Minimal LSP 3.x structures — only the fields this server reads or
// writes. LSP is plain JSON; clients ignore absent optional fields.

// Position is zero-based; Line is all we use (seamark spans are
// line-grained, sidestepping LSP's UTF-16 column semantics).
type Position struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// Range is a half-open [start, end) span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextDocumentIdentifier names a document by URI.
type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
	ServerInfo   serverInfo         `json:"serverInfo"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type serverCapabilities struct {
	HoverProvider    bool             `json:"hoverProvider"`
	CodeLensProvider codeLensOptions  `json:"codeLensProvider"`
	TextDocumentSync textDocumentSync `json:"textDocumentSync"`
}

type codeLensOptions struct {
	ResolveProvider bool `json:"resolveProvider"`
}

type textDocumentSync struct {
	OpenClose bool `json:"openClose"`
	// Change 0 = None: we read files from disk on save, never buffers.
	Change int      `json:"change"`
	Save   saveSync `json:"save"`
}

type saveSync struct {
	IncludeText bool `json:"includeText"`
}

type hoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type hoverResult struct {
	Contents markupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type markupContent struct {
	Kind  string `json:"kind"` // "markdown"
	Value string `json:"value"`
}

type codeLensParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type codeLens struct {
	Range   Range   `json:"range"`
	Command command `json:"command"`
}

type command struct {
	Title   string `json:"title"`
	Command string `json:"command"`
}

type didSaveParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// Diagnostic severities (LSP numeric values).
const (
	severityWarning = 2
	severityInfo    = 3
)

type diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI string `json:"uri"`
	// Diagnostics must be non-nil: an empty array clears previous ones.
	Diagnostics []diagnostic `json:"diagnostics"`
}
