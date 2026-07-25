package parse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/seamark-dev/seamark/internal/model"
)

// pyDeclQuery matches module-level declarations. The assignment pattern
// nests through `module` directly, so only module-scope constants match;
// functions and classes are position-checked in code because decorators
// wrap them in an extra node.
const pyDeclQuery = `
(function_definition name: (identifier) @func.name) @func.decl
(class_definition name: (identifier) @class.name) @class.decl
(module (expression_statement (assignment left: (identifier) @assign.name) @assign.spec))
(import_statement) @import.plain
(import_from_statement) @import.from
`

// pyCallQuery finds call sites; run against a single declaration node.
const pyCallQuery = `
(call function: (identifier) @call.bare)
(call function: (attribute) @call.selector)
`

// PyExtractor extracts symbols, imports, and call references from Python
// files. Python modules scope per file, like ES modules: the package key is
// the repo-relative path without ".py", and "pkg/__init__.py" collapses to
// "pkg" — the module Python itself calls pkg.
type PyExtractor struct {
	parser *tree_sitter.Parser
	declQ  *tree_sitter.Query
	callQ  *tree_sitter.Query
}

// NewPyExtractor compiles the tree-sitter queries for Python.
func NewPyExtractor() (*PyExtractor, error) {
	lang := tree_sitter.NewLanguage(tree_sitter_python.Language())

	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: set python language: %w", err)
	}

	declQ, qerr := tree_sitter.NewQuery(lang, pyDeclQuery)
	if qerr != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: compile python decl query: %s", qerr.Message)
	}

	callQ, qerr := tree_sitter.NewQuery(lang, pyCallQuery)
	if qerr != nil {
		declQ.Close()
		parser.Close()

		return nil, fmt.Errorf("parse: compile python call query: %s", qerr.Message)
	}

	return &PyExtractor{parser: parser, declQ: declQ, callQ: callQ}, nil
}

// Language returns the registry key for Python.
func (p *PyExtractor) Language() string { return "python" }

// Extensions lists the file extensions routed to this extractor.
func (p *PyExtractor) Extensions() []string { return []string{".py"} }

// Close releases the underlying parser and queries.
func (p *PyExtractor) Close() {
	p.callQ.Close()
	p.declQ.Close()
	p.parser.Close()
}

// pyModuleKey maps a file path to its Python module key.
func pyModuleKey(relPath string) string {
	key := strings.TrimSuffix(toSlash(relPath), ".py")
	if path.Base(key) == "__init__" {
		key = path.Dir(key)
		if key == "." {
			key = ""
		}
	}

	return key
}

// Extract parses one Python file.
func (p *PyExtractor) Extract(relPath string, src []byte) (*FileResult, error) {
	tree := p.parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: python parse failed for %s", relPath)
	}
	defer tree.Close()

	moduleKey := pyModuleKey(relPath)
	// Relative imports resolve against the file's directory, which for an
	// __init__ module equals its own (collapsed) key.
	pkgDir := path.Dir(strings.TrimSuffix(toSlash(relPath), ".py"))
	if pkgDir == "." {
		pkgDir = ""
	}

	res := &FileResult{Path: relPath, Language: "python", Package: moduleKey}

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := p.declQ.CaptureNames()
	matches := qc.Matches(p.declQ, tree.RootNode(), src)

	for m := matches.Next(); m != nil; m = matches.Next() {
		caps := map[string]*tree_sitter.Node{}
		for i := range m.Captures {
			caps[names[m.Captures[i].Index]] = &m.Captures[i].Node
		}

		switch {
		case caps["func.decl"] != nil:
			p.addFunction(res, src, moduleKey, caps["func.decl"], caps["func.name"])
		case caps["class.decl"] != nil:
			if atPyModuleTopLevel(caps["class.decl"]) {
				p.addSymbol(res, src, moduleKey, caps["class.decl"], caps["class.name"], "", model.KindType, "", false)
			}
		case caps["assign.spec"] != nil:
			p.addAssignment(res, src, moduleKey, caps["assign.spec"], caps["assign.name"])
		case caps["import.plain"] != nil:
			res.Imports = append(res.Imports, pyPlainImports(caps["import.plain"], src)...)
		case caps["import.from"] != nil:
			res.Imports = append(res.Imports, pyFromImports(caps["import.from"], src, pkgDir)...)
		}
	}

	return res, nil
}

// addFunction places a function_definition: module-level function, class
// method, or (skipped) nested function. For methods, the first parameter
// name (self/cls by convention, but any spelling) marks receiver calls.
func (p *PyExtractor) addFunction(res *FileResult, src []byte, moduleKey string,
	decl, nameNode *tree_sitter.Node) {
	if atPyModuleTopLevel(decl) {
		p.addSymbol(res, src, moduleKey, decl, nameNode, "", model.KindFunction, "", true)
		return
	}

	if class := enclosingPyClassName(decl, src); class != "" {
		p.addSymbol(res, src, moduleKey, decl, nameNode, class, model.KindMethod,
			pyFirstParamName(decl, src), true)
	}
}

// pyFirstParamName returns the name of a function's first parameter — the
// receiver by Python convention, whatever it is spelled.
func pyFirstParamName(fn *tree_sitter.Node, src []byte) string {
	params := fn.ChildByFieldName("parameters")
	if params == nil || params.NamedChildCount() == 0 {
		return ""
	}

	first := params.NamedChild(0)

	switch first.Kind() {
	case "identifier":
		return first.Utf8Text(src)
	case "typed_parameter":
		if n := first.NamedChild(0); n != nil && n.Kind() == "identifier" {
			return n.Utf8Text(src)
		}
	case "default_parameter", "typed_default_parameter":
		if n := first.ChildByFieldName("name"); n != nil && n.Kind() == "identifier" {
			return n.Utf8Text(src)
		}
	}

	return ""
}

// addAssignment records a module-level assignment. SCREAMING_SNAKE names
// follow the constant convention; everything else is a var.
func (p *PyExtractor) addAssignment(res *FileResult, src []byte, moduleKey string,
	spec, nameNode *tree_sitter.Node) {
	if nameNode == nil {
		return
	}

	kind := model.KindVar
	if name := nameNode.Utf8Text(src); name == strings.ToUpper(name) {
		kind = model.KindConst
	}

	p.addSymbol(res, src, moduleKey, spec, nameNode, "", kind, "", false)
}

func (p *PyExtractor) addSymbol(res *FileResult, src []byte, moduleKey string,
	decl, nameNode *tree_sitter.Node, recv string, kind model.SymbolKind,
	selfName string, mineCalls bool) {
	if nameNode == nil {
		return
	}

	name := nameNode.Utf8Text(src)
	sd := SymbolDecl{Symbol: model.Symbol{
		FQN:     joinFQN(moduleKey, recv, name),
		Name:    name,
		Kind:    kind,
		File:    res.Path,
		Span:    spanOf(decl),
		Sig:     pySignature(decl, src),
		DocHash: pyDocHash(decl, src),
	}}
	if mineCalls {
		sd.Calls = p.callsIn(decl, src, selfName)
	}

	res.Symbols = append(res.Symbols, sd)
}

// callsIn runs the call query inside one declaration. selfName is the
// enclosing method's receiver parameter ("" for functions): calls
// qualified by exactly that identifier are receiver calls.
func (p *PyExtractor) callsIn(decl *tree_sitter.Node, src []byte, selfName string) []CallRef {
	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := p.callQ.CaptureNames()

	var out []CallRef

	matches := qc.Matches(p.callQ, decl, src)
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
				attr := node.ChildByFieldName("attribute")
				if attr == nil {
					continue
				}

				ref := CallRef{
					Name:     attr.Utf8Text(src),
					Line:     uint32(node.StartPosition().Row) + 1,
					Selector: true,
				}
				if obj := node.ChildByFieldName("object"); obj != nil && obj.Kind() == "identifier" {
					ref.Qualifier = obj.Utf8Text(src)
					ref.Receiver = selfName != "" && ref.Qualifier == selfName
				}

				out = append(out, ref)
			}
		}
	}

	return out
}

// atPyModuleTopLevel reports whether a definition sits directly in module
// scope, looking through a decorator wrapper.
func atPyModuleTopLevel(decl *tree_sitter.Node) bool {
	p := decl.Parent()
	if p != nil && p.Kind() == "decorated_definition" {
		p = p.Parent()
	}

	return p != nil && p.Kind() == "module"
}

// enclosingPyClassName returns the class name for a function_definition
// that is a direct member of a module-level class; "" otherwise. Strict
// shape (function → [decorator] → block → class_definition), mirroring the
// ecma extractor: a loose walk would claim nested helpers as methods.
func enclosingPyClassName(fn *tree_sitter.Node, src []byte) string {
	p := fn.Parent()
	if p != nil && p.Kind() == "decorated_definition" {
		p = p.Parent()
	}

	if p == nil || p.Kind() != "block" {
		return ""
	}

	class := p.Parent()
	if class == nil || class.Kind() != "class_definition" {
		return ""
	}

	if !atPyModuleTopLevel(class) {
		return ""
	}

	if name := class.ChildByFieldName("name"); name != nil {
		return name.Utf8Text(src)
	}

	return ""
}

// pyPlainImports reads `import a.b, x as y`: one Import per clause. Only
// aliased or single-segment imports contribute a qualifier — calls through
// dotted paths (`a.b.f()`) have a non-identifier operand and never match a
// local name anyway.
func pyPlainImports(stmt *tree_sitter.Node, src []byte) []Import {
	cursor := stmt.Walk()
	defer cursor.Close()

	var out []Import

	for _, child := range stmt.ChildrenByFieldName("name", cursor) {
		switch child.Kind() {
		case "dotted_name":
			p := child.Utf8Text(src)
			imp := Import{Path: p, Resolved: pyAbsoluteKey(p)}
			if !strings.Contains(p, ".") {
				imp.LocalName = p
			}

			out = append(out, imp)
		case "aliased_import":
			nameNode := child.ChildByFieldName("name")
			alias := child.ChildByFieldName("alias")
			if nameNode == nil || alias == nil {
				continue
			}

			p := nameNode.Utf8Text(src)
			out = append(out, Import{
				Path:      p,
				LocalName: alias.Utf8Text(src),
				Resolved:  pyAbsoluteKey(p),
			})
		}
	}

	return out
}

// pyFromImports reads `from M import a, b as c`. Un-aliased names become
// Named entries resolving into module M; `from . import sub [as alias]`
// forms yield one qualifier Import per submodule. Aliased symbol imports
// and wildcards are skipped — their local names cannot be looked up in the
// target module, and no edge beats a wrong edge.
func pyFromImports(stmt *tree_sitter.Node, src []byte, pkgDir string) []Import {
	modNode := stmt.ChildByFieldName("module_name")
	if modNode == nil {
		return nil
	}

	modText := modNode.Utf8Text(src)
	resolved, inRepo := pyResolveModule(modText, pkgDir)

	cursor := stmt.Walk()
	defer cursor.Close()

	// `from . import sub` (module_name is dots only): each name is a
	// submodule used as a qualifier, not a symbol.
	dotsOnly := modNode.Kind() == "relative_import" && strings.Trim(modText, ".") == ""

	base := Import{Path: modText, Resolved: resolved}

	var out []Import

	for _, child := range stmt.ChildrenByFieldName("name", cursor) {
		name, local := "", ""

		switch child.Kind() {
		case "dotted_name":
			name = child.Utf8Text(src)
			local = name
		case "aliased_import":
			nameNode := child.ChildByFieldName("name")
			alias := child.ChildByFieldName("alias")
			if nameNode == nil || alias == nil || !dotsOnly {
				continue
			}
			// `from . import tracker as tk`: the alias is a module
			// qualifier and the target module is fully known — resolvable
			// despite the alias, unlike aliased symbol imports.
			name = nameNode.Utf8Text(src)
			local = alias.Utf8Text(src)
		default:
			continue
		}

		if strings.Contains(name, ".") {
			continue
		}

		if dotsOnly {
			sub := Import{Path: modText, LocalName: local}
			if inRepo {
				sub.Resolved = path.Join(resolved, name)
			}

			out = append(out, sub)

			continue
		}

		base.Named = append(base.Named, name)
	}

	if len(base.Named) > 0 || len(out) == 0 {
		out = append(out, base)
	}

	return out
}

// pyAbsoluteKey converts a dotted absolute module path to a slash key.
// Known limitation: without an installed-package list there is no way to
// tell `from utils import x` (external lib) from a repo-local `utils/`
// package; a same-named repo directory therefore captures the import. The
// resolver only emits an edge when the name actually exists there.
func pyAbsoluteKey(dotted string) string {
	return strings.ReplaceAll(dotted, ".", "/")
}

// pyResolveModule resolves a from-import module reference: relative forms
// walk up from the importing file's package directory; absolute forms map
// dots to slashes. inRepo is false when the relative form climbs past the
// repo root — an empty key alone cannot distinguish "the root package"
// from "escaped and unresolvable".
func pyResolveModule(modText, pkgDir string) (key string, inRepo bool) {
	if !strings.HasPrefix(modText, ".") {
		return pyAbsoluteKey(modText), true
	}

	dots := 0
	for dots < len(modText) && modText[dots] == '.' {
		dots++
	}

	base := pkgDir
	for range dots - 1 {
		if base == "" {
			return "", false // escapes the repo: unresolvable
		}

		base = path.Dir(base)
		if base == "." {
			base = ""
		}
	}

	rest := pyAbsoluteKey(modText[dots:])

	switch {
	case rest == "":
		return base, true
	case base == "":
		return rest, true
	default:
		return base + "/" + rest, true
	}
}

// pySignature is the `def`/`class` line without decorators or the trailing
// colon.
func pySignature(decl *tree_sitter.Node, src []byte) string {
	end := decl.EndByte()
	if body := decl.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	}

	text := strings.TrimSpace(string(src[decl.StartByte():end]))

	return strings.TrimSuffix(strings.TrimSpace(firstLine(text)), ":")
}

// pyDocHash hashes the declaration's docstring (the first statement of its
// body); Python documentation lives inside the body, not above it. Falls
// back to a preceding comment block for module constants.
func pyDocHash(decl *tree_sitter.Node, src []byte) string {
	if body := decl.ChildByFieldName("body"); body != nil && body.NamedChildCount() > 0 {
		first := body.NamedChild(0)
		if first.Kind() == "expression_statement" && first.NamedChildCount() > 0 {
			if s := first.NamedChild(0); s.Kind() == "string" {
				sum := sha256.Sum256([]byte(s.Utf8Text(src)))
				return hex.EncodeToString(sum[:])
			}
		}
	}

	return docHash(decl, src)
}
