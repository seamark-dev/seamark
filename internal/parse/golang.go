package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_go "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/seamark-dev/seamark/internal/model"
)

// declQuery finds every top-level declaration we model. Receiver types are
// resolved by AST walking in code (generics and pointers make the query form
// unwieldy).
const goDeclQuery = `
(function_declaration name: (identifier) @func.name) @func.decl
(method_declaration name: (field_identifier) @method.name) @method.decl
(type_declaration (type_spec name: (type_identifier) @type.name) @type.spec) @type.decl
(type_declaration (type_alias name: (type_identifier) @type.name) @type.spec) @type.decl
(const_declaration (const_spec name: (identifier) @const.name) @const.spec)
(var_declaration (var_spec name: (identifier) @var.name) @var.spec)
(var_declaration (var_spec_list (var_spec name: (identifier) @var.name) @var.spec))
(import_spec) @import.spec
`

// callQuery finds call sites; run against a single declaration node.
const goCallQuery = `
(call_expression function: (identifier) @call.bare)
(call_expression function: (selector_expression) @call.selector)
`

// GoExtractor extracts symbols, imports, and call references from Go files.
type GoExtractor struct {
	parser    *tree_sitter.Parser
	lang      *tree_sitter.Language
	declQuery *tree_sitter.Query
	callQuery *tree_sitter.Query
}

// NewGoExtractor compiles the tree-sitter queries for Go.
func NewGoExtractor() (*GoExtractor, error) {
	lang := tree_sitter.NewLanguage(tree_sitter_go.Language())
	parser := tree_sitter.NewParser()

	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: set go language: %w", err)
	}

	declQ, qerr := tree_sitter.NewQuery(lang, goDeclQuery)
	if qerr != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: compile go decl query: %s", qerr.Message)
	}

	callQ, qerr := tree_sitter.NewQuery(lang, goCallQuery)
	if qerr != nil {
		declQ.Close()
		parser.Close()

		return nil, fmt.Errorf("parse: compile go call query: %s", qerr.Message)
	}

	return &GoExtractor{parser: parser, lang: lang, declQuery: declQ, callQuery: callQ}, nil
}

// Language returns the registry key for Go.
func (g *GoExtractor) Language() string { return "go" }

// Extensions lists the file extensions routed to this extractor.
func (g *GoExtractor) Extensions() []string { return []string{".go"} }

// Close releases the underlying parser and queries.
func (g *GoExtractor) Close() {
	g.callQuery.Close()
	g.declQuery.Close()
	g.parser.Close()
}

// Extract parses one Go file. The package key is the file's repo-relative
// directory ("" at the repo root), which keeps FQNs stable across module
// renames and readable in output: "internal/store.Store.UpsertSymbols".
func (g *GoExtractor) Extract(relPath string, src []byte) (*FileResult, error) {
	tree := g.parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: go parse failed for %s", relPath)
	}
	defer tree.Close()

	pkg := path.Dir(toSlash(relPath))
	if pkg == "." {
		pkg = ""
	}

	res := &FileResult{Path: relPath, Language: "go", Package: pkg}

	root := tree.RootNode()
	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	captureNames := g.declQuery.CaptureNames()
	matches := qc.Matches(g.declQuery, root, src)

	for m := matches.Next(); m != nil; m = matches.Next() {
		caps := map[string]*tree_sitter.Node{}

		for i := range m.Captures {
			caps[captureNames[m.Captures[i].Index]] = &m.Captures[i].Node
		}

		switch {
		case caps["func.decl"] != nil:
			g.addFuncLike(res, src, pkg, caps["func.decl"], caps["func.name"], "", model.KindFunction)
		case caps["method.decl"] != nil:
			recv := goReceiverType(caps["method.decl"], src)
			g.addFuncLike(res, src, pkg, caps["method.decl"], caps["method.name"], recv, model.KindMethod)
		case caps["type.spec"] != nil:
			g.addValueDecl(res, src, pkg, caps["type.spec"], caps["type.name"], model.KindType)
		case caps["const.spec"] != nil:
			g.addValueDecl(res, src, pkg, caps["const.spec"], caps["const.name"], model.KindConst)
		case caps["var.spec"] != nil:
			g.addValueDecl(res, src, pkg, caps["var.spec"], caps["var.name"], model.KindVar)
		case caps["import.spec"] != nil:
			if imp, ok := goImport(caps["import.spec"], src); ok {
				res.Imports = append(res.Imports, imp)
			}
		}
	}

	return res, nil
}

// addFuncLike records a function or method declaration and mines its body
// for call references.
func (g *GoExtractor) addFuncLike(res *FileResult, src []byte, pkg string,
	decl, nameNode *tree_sitter.Node, recv string, kind model.SymbolKind,
) {
	if nameNode == nil {
		return
	}

	name := nameNode.Utf8Text(src)
	fqn := joinFQN(pkg, recv, name)

	sym := model.Symbol{
		FQN:     fqn,
		Name:    name,
		Kind:    kind,
		File:    res.Path,
		Span:    spanOf(decl),
		Sig:     goSignature(decl, src),
		DocHash: docHash(decl, src),
	}

	// Methods flag calls through their named receiver (s.helper()) so the
	// resolver can pin them to the receiver type instead of guessing.
	selfName := ""
	if kind == model.KindMethod {
		selfName = goReceiverName(decl, src)
	}

	res.Symbols = append(res.Symbols, SymbolDecl{Symbol: sym, Calls: g.callsIn(decl, src, selfName)})
}

// addValueDecl records a type/const/var spec (no body mining — initializer
// calls are rarely useful edges and often noise).
func (g *GoExtractor) addValueDecl(res *FileResult, src []byte, pkg string,
	spec, nameNode *tree_sitter.Node, kind model.SymbolKind,
) {
	if nameNode == nil {
		return
	}

	// Only package-level declarations are symbols; const/var/type blocks
	// inside function bodies are implementation detail. Grouped var blocks
	// nest their specs one level deeper (var_spec_list).
	p := spec.Parent()
	if p != nil && p.Kind() == "var_spec_list" {
		p = p.Parent()
	}

	if p == nil || p.Parent() == nil || p.Parent().Kind() != "source_file" {
		return
	}

	name := nameNode.Utf8Text(src)

	res.Symbols = append(res.Symbols, SymbolDecl{Symbol: model.Symbol{
		FQN:     joinFQN(pkg, "", name),
		Name:    name,
		Kind:    kind,
		File:    res.Path,
		Span:    spanOf(spec),
		Sig:     firstLine(spec.Utf8Text(src)),
		DocHash: docHash(spec, src),
	}})
}

// callsIn runs the call query inside one declaration. selfName is the
// method's receiver identifier ("" for functions).
func (g *GoExtractor) callsIn(decl *tree_sitter.Node, src []byte, selfName string) []CallRef {
	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := g.callQuery.CaptureNames()
	var out []CallRef
	matches := qc.Matches(g.callQuery, decl, src)

	for m := matches.Next(); m != nil; m = matches.Next() {
		for i := range m.Captures {
			node := &m.Captures[i].Node

			switch names[m.Captures[i].Index] {
			case "call.bare":
				out = append(out, CallRef{
					Name: node.Utf8Text(src),
					Line: uint32(node.StartPosition().Row) + 1,
				})
			case "call.selector":
				field := node.ChildByFieldName("field")
				if field == nil {
					continue
				}
				ref := CallRef{
					Name:     field.Utf8Text(src),
					Line:     uint32(node.StartPosition().Row) + 1,
					Selector: true,
				}
				if op := node.ChildByFieldName("operand"); op != nil && op.Kind() == "identifier" {
					ref.Qualifier = op.Utf8Text(src)
					ref.Receiver = selfName != "" && ref.Qualifier == selfName
				}
				out = append(out, ref)
			}
		}
	}

	return out
}

// goReceiverName extracts the receiver's identifier: "s" in (s *Store).
func goReceiverName(methodDecl *tree_sitter.Node, src []byte) string {
	recv := methodDecl.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}

	for i := uint(0); i < recv.NamedChildCount(); i++ {
		param := recv.NamedChild(i)
		if param.Kind() != "parameter_declaration" {
			continue
		}

		if n := param.ChildByFieldName("name"); n != nil {
			return n.Utf8Text(src)
		}
	}

	return ""
}

// goReceiverType extracts the receiver type name of a method declaration,
// stripping pointers and type parameters: (s *Store[T]) → "Store".
func goReceiverType(methodDecl *tree_sitter.Node, src []byte) string {
	recv := methodDecl.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}

	for i := uint(0); i < recv.NamedChildCount(); i++ {
		param := recv.NamedChild(i)
		if param.Kind() != "parameter_declaration" {
			continue
		}

		t := param.ChildByFieldName("type")

		for t != nil {
			switch t.Kind() {
			case "pointer_type":
				t = t.NamedChild(0)
			case "generic_type":
				t = t.ChildByFieldName("type")
			case "type_identifier":
				return t.Utf8Text(src)
			default:
				return firstLine(t.Utf8Text(src))
			}
		}
	}

	return ""
}

// goImport reads one import_spec into an Import.
func goImport(spec *tree_sitter.Node, src []byte) (Import, bool) {
	pathNode := spec.ChildByFieldName("path")
	if pathNode == nil {
		return Import{}, false
	}

	p := strings.Trim(pathNode.Utf8Text(src), "`\"")
	imp := Import{Path: p, LocalName: defaultLocalName(p)}

	if nameNode := spec.ChildByFieldName("name"); nameNode != nil {
		switch alias := nameNode.Utf8Text(src); alias {
		case "_", ".":
			imp.LocalName = "" // blank/dot imports contribute no qualifier
		default:
			imp.LocalName = alias
		}
	}

	return imp, true
}

var (
	versionSegRe = regexp.MustCompile(`^v\d+$`)
	identRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// defaultLocalName predicts the package identifier of an unaliased import:
// the last path segment, skipping a trailing major-version segment
// ("example.com/mod/v2" → "mod"). When the segment is not a valid Go
// identifier (e.g. "go-thing"), the real package name cannot be derived
// from the path; return "" so the resolver treats calls through this import
// as unresolvable instead of guessing wrong.
func defaultLocalName(importPath string) string {
	segs := strings.Split(importPath, "/")
	name := segs[len(segs)-1]

	if len(segs) > 1 && versionSegRe.MatchString(name) {
		name = segs[len(segs)-2]
	}

	if !identRe.MatchString(name) {
		return ""
	}

	return name
}

// goSignature returns the declaration text up to (excluding) the body,
// collapsed to one line: "func (s *Store) Rebuild(fn func(tx *Tx) error) error".
func goSignature(decl *tree_sitter.Node, src []byte) string {
	end := decl.EndByte()
	if body := decl.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}

	return collapseWS(string(src[decl.StartByte():end]))
}

// docHash hashes the contiguous comment block immediately above a
// declaration; "" when there is none. Storing the hash rather than the text
// keeps the index small while still detecting doc drift (RFC §5.1).
func docHash(decl *tree_sitter.Node, src []byte) string {
	// Specs and declarators sit inside wrapper nodes (Go declaration
	// blocks, JS lexical declarations and export statements); the doc
	// comment attaches to the outermost wrapper.
	anchor := decl

	for p := anchor.Parent(); p != nil; p = anchor.Parent() {
		switch p.Kind() {
		case "type_declaration", "const_declaration", "var_declaration",
			"var_spec_list", "lexical_declaration", "variable_declaration",
			"export_statement", "expression_statement", "decorated_definition":
			anchor = p
			continue
		}

		break
	}

	var parts []string
	expectRow := anchor.StartPosition().Row

	for prev := anchor.PrevSibling(); prev != nil && prev.Kind() == "comment"; prev = prev.PrevSibling() {
		if prev.EndPosition().Row+1 != expectRow && prev.EndPosition().Row != expectRow {
			break // blank line detaches the comment block
		}

		parts = append([]string{prev.Utf8Text(src)}, parts...)
		expectRow = prev.StartPosition().Row
	}

	if len(parts) == 0 {
		return ""
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))

	return hex.EncodeToString(sum[:])
}

// joinFQN builds "<pkg>.<recv>.<name>" skipping empty segments.
func joinFQN(pkg, recv, name string) string {
	parts := make([]string, 0, 3)

	for _, p := range []string{pkg, recv, name} {
		if p != "" {
			parts = append(parts, p)
		}
	}

	return strings.Join(parts, ".")
}

func spanOf(n *tree_sitter.Node) model.Span {
	start, end := n.StartPosition(), n.EndPosition()

	return model.Span{
		StartLine: uint32(start.Row) + 1,
		StartCol:  uint32(start.Column),
		EndLine:   uint32(end.Row) + 1,
		EndCol:    uint32(end.Column),
	}
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(s, "\n")

	return strings.TrimSpace(first)
}

// collapseWS folds a multi-line declaration header into one line: runs of
// whitespace (newlines, indentation) become single spaces, so signatures
// spanning several lines don't truncate at the first ("async def f(").
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// toSlash normalizes to forward slashes so package keys are OS-independent.
func toSlash(p string) string { return strings.ReplaceAll(p, "\\", "/") }
