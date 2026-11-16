package parse

import (
	"fmt"
	"path"
	"strings"

	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/seamark-dev/seamark/internal/model"
)

// EcmaDialect selects the grammar variant of the ECMAScript-family
// extractor. TSX needs its own grammar; plain JS handles JSX natively.
type EcmaDialect string

// The supported dialects.
const (
	EcmaJS  EcmaDialect = "javascript"
	EcmaTS  EcmaDialect = "typescript"
	EcmaTSX EcmaDialect = "tsx"
)

// ecmaDeclQuery matches declarations shared by every dialect. Field types
// vary between the JS and TS grammars (class names are `identifier` vs
// `type_identifier`), so names are captured with wildcards. A method with
// a plain or a private (#m) name is a symbol. A method with a computed
// ([k]) or string name is no symbol, because only an identifier name
// gives a safe FQN segment.
const ecmaDeclQuery = `
(function_declaration name: (identifier) @func.name) @func.decl
(generator_function_declaration name: (identifier) @func.name) @func.decl
(class_declaration name: (_) @class.name) @class.decl
(method_definition name: (property_identifier) @method.name) @method.decl
(method_definition name: (private_property_identifier) @method.name) @method.decl
(lexical_declaration (variable_declarator name: (identifier) @lex.name) @lex.spec) @lex.decl
(variable_declaration (variable_declarator name: (identifier) @lex.name) @lex.spec) @lex.decl
(import_statement) @import.stmt
`

// ecmaTSExtraQuery adds TypeScript-only declarations; compiling it against
// the JS grammar would fail, hence the split.
const ecmaTSExtraQuery = `
(interface_declaration name: (type_identifier) @type.name) @type.decl
(type_alias_declaration name: (type_identifier) @type.name) @type.decl
(enum_declaration name: (identifier) @type.name) @type.decl
(abstract_class_declaration name: (type_identifier) @class.name) @class.decl
`

// ecmaCallQuery finds call sites; run against a single declaration node.
const ecmaCallQuery = `
(call_expression function: (identifier) @call.bare)
(call_expression function: (member_expression) @call.selector)
`

// EcmaExtractor extracts symbols, imports, and call references from
// JavaScript and TypeScript files. Unlike Go, the ES module system scopes
// per file: the package key is the repo-relative path without extension,
// and relative imports are resolved here (the extractor knows the file's
// location; the indexer does not know ES resolution rules).
type EcmaExtractor struct {
	dialect EcmaDialect
	exts    []string
	parser  *tree_sitter.Parser
	declQ   *tree_sitter.Query
	callQ   *tree_sitter.Query
}

// NewEcmaExtractor compiles the queries for one dialect.
func NewEcmaExtractor(dialect EcmaDialect) (*EcmaExtractor, error) {
	var (
		lang *tree_sitter.Language
		exts []string
	)

	declSrc := ecmaDeclQuery

	switch dialect {
	case EcmaJS:
		lang = tree_sitter.NewLanguage(tree_sitter_javascript.Language())
		exts = []string{".js", ".jsx", ".mjs", ".cjs"}
	case EcmaTS:
		lang = tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
		exts = []string{".ts", ".mts", ".cts"}
		declSrc += ecmaTSExtraQuery
	case EcmaTSX:
		lang = tree_sitter.NewLanguage(tree_sitter_typescript.LanguageTSX())
		exts = []string{".tsx"}
		declSrc += ecmaTSExtraQuery
	default:
		return nil, fmt.Errorf("parse: unknown ecma dialect %q", dialect)
	}

	parser := tree_sitter.NewParser()
	if err := parser.SetLanguage(lang); err != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: set %s language: %w", dialect, err)
	}

	declQ, qerr := tree_sitter.NewQuery(lang, declSrc)
	if qerr != nil {
		parser.Close()
		return nil, fmt.Errorf("parse: compile %s decl query: %s", dialect, qerr.Message)
	}

	callQ, qerr := tree_sitter.NewQuery(lang, ecmaCallQuery)
	if qerr != nil {
		declQ.Close()
		parser.Close()

		return nil, fmt.Errorf("parse: compile %s call query: %s", dialect, qerr.Message)
	}

	return &EcmaExtractor{dialect: dialect, exts: exts, parser: parser, declQ: declQ, callQ: callQ}, nil
}

// Language returns the registry key for this dialect.
func (e *EcmaExtractor) Language() string { return string(e.dialect) }

// Extensions lists the file extensions routed to this extractor.
func (e *EcmaExtractor) Extensions() []string { return e.exts }

// Close releases the underlying parser and queries.
func (e *EcmaExtractor) Close() {
	e.callQ.Close()
	e.declQ.Close()
	e.parser.Close()
}

// Extract parses one JS/TS file. The package key is the file's module path:
// repo-relative, extension stripped ("web/src/api/client").
func (e *EcmaExtractor) Extract(relPath string, src []byte) (*FileResult, error) {
	tree := e.parser.Parse(src, nil)
	if tree == nil {
		return nil, fmt.Errorf("parse: %s parse failed for %s", e.dialect, relPath)
	}
	defer tree.Close()

	moduleKey := strings.TrimSuffix(toSlash(relPath), path.Ext(relPath))
	res := &FileResult{Path: relPath, Language: string(e.dialect), Package: moduleKey}

	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := e.declQ.CaptureNames()
	matches := qc.Matches(e.declQ, tree.RootNode(), src)

	for m := matches.Next(); m != nil; m = matches.Next() {
		caps := map[string]*tree_sitter.Node{}
		for i := range m.Captures {
			caps[names[m.Captures[i].Index]] = &m.Captures[i].Node
		}

		switch {
		case caps["func.decl"] != nil:
			if atModuleTopLevel(caps["func.decl"]) {
				e.addDecl(res, src, moduleKey, caps["func.decl"], caps["func.name"], "", model.KindFunction, true)
			}
		case caps["class.decl"] != nil:
			// The class carries the calls of its body outside its methods:
			// field initializers, static blocks, and decorators of members.
			if atModuleTopLevel(caps["class.decl"]) {
				e.addDecl(res, src, moduleKey, caps["class.decl"], caps["class.name"], "", model.KindType, true)
			}
		case caps["method.decl"] != nil:
			if class := enclosingClassName(caps["method.decl"], src); class != "" {
				e.addDecl(res, src, moduleKey, caps["method.decl"], caps["method.name"], class, model.KindMethod, true)
			}
		case caps["type.decl"] != nil:
			if atModuleTopLevel(caps["type.decl"]) {
				e.addDecl(res, src, moduleKey, caps["type.decl"], caps["type.name"], "", model.KindType, false)
			}
		case caps["lex.decl"] != nil:
			e.addBinding(res, src, moduleKey, caps)
		case caps["import.stmt"] != nil:
			if imp, ok := ecmaImport(caps["import.stmt"], src, moduleKey); ok {
				res.Imports = append(res.Imports, imp)
			}
		}
	}

	return res, nil
}

// addDecl records one named declaration, optionally mining its body for
// call references.
func (e *EcmaExtractor) addDecl(res *FileResult, src []byte, moduleKey string,
	decl, nameNode *tree_sitter.Node, recv string, kind model.SymbolKind, mineCalls bool) {
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
		Sig:     ecmaSignature(decl, src),
		DocHash: docHash(decl, src),
	}}
	if mineCalls {
		sd.Calls = e.callsIn(decl, src)
	}

	res.Symbols = append(res.Symbols, sd)
}

// addBinding classifies a top-level const/let/var declarator: function
// values (arrow functions, function expressions) become functions — the
// dominant declaration style in modern JS/TS — everything else a const/var.
// Every binding carries the calls of its initializer. The span covers the
// initializer, so a change there counts as attributed, and a table of
// handlers ({ save: () => fs.writeFileSync(p, d) }) must not read as
// reaching nothing.
func (e *EcmaExtractor) addBinding(res *FileResult, src []byte, moduleKey string,
	caps map[string]*tree_sitter.Node) {
	decl, spec, nameNode := caps["lex.decl"], caps["lex.spec"], caps["lex.name"]
	if spec == nil || nameNode == nil || !atModuleTopLevel(decl) {
		return
	}

	kind := model.KindVar
	if strings.HasPrefix(decl.Utf8Text(src), "const") {
		kind = model.KindConst
	}

	if v := spec.ChildByFieldName("value"); v != nil {
		switch v.Kind() {
		case "arrow_function", "function_expression", "generator_function":
			kind = model.KindFunction
		}
	}

	e.addDecl(res, src, moduleKey, spec, nameNode, "", kind, true)
}

// callsIn runs the call query inside one declaration.
func (e *EcmaExtractor) callsIn(decl *tree_sitter.Node, src []byte) []CallRef {
	qc := tree_sitter.NewQueryCursor()
	defer qc.Close()

	names := e.callQ.CaptureNames()

	var out []CallRef

	matches := qc.Matches(e.callQ, decl, src)
	for m := matches.Next(); m != nil; m = matches.Next() {
		for i := range m.Captures {
			node := &m.Captures[i].Node

			if inEcmaMethodOf(node, decl) {
				continue
			}

			switch names[m.Captures[i].Index] {
			case "call.bare":
				out = append(out, CallRef{
					Name: node.Utf8Text(src),
					Line: uint32(node.StartPosition().Row) + 1,
				})
			case "call.selector":
				prop := node.ChildByFieldName("property")
				if prop == nil {
					continue
				}

				ref := CallRef{
					Name:     prop.Utf8Text(src),
					Line:     uint32(node.StartPosition().Row) + 1,
					Selector: true,
				}
				if obj := node.ChildByFieldName("object"); obj != nil {
					switch obj.Kind() {
					case "identifier":
						ref.Qualifier = obj.Utf8Text(src)
					case "this":
						// `this` is its own node kind — unforgeable, unlike
						// Python's self. It only counts as the receiver when
						// no nested non-arrow function rebinds it.
						ref.Receiver = thisIsReceiver(node, decl)
					}
				}

				out = append(out, ref)
			}
		}
	}

	return out
}

// inEcmaMethodOf reports whether node lies in a method that is its own
// symbol as a member of the class decl. That method carries the call, so
// the class must not carry it twice. The shape follows the decl query and
// enclosingClassName: a method_definition with a symbol name, directly in
// the class body. A computed or string-named method is no symbol, so its
// calls stay with the class. For any other decl the shape never matches.
func inEcmaMethodOf(node, decl *tree_sitter.Node) bool {
	for p := node.Parent(); p != nil && !sameNode(p, decl); p = p.Parent() {
		if p.Kind() != "method_definition" {
			continue
		}

		if symbolMethodName(p.ChildByFieldName("name")) && memberOfClass(p, decl) {
			return true
		}
	}

	return false
}

// symbolMethodName reports whether a method with this name node is a
// symbol. It must match the method patterns of the decl query, or a call
// in the method is lost or counted on both the method and the class.
func symbolMethodName(name *tree_sitter.Node) bool {
	if name == nil {
		return false
	}

	switch name.Kind() {
	case "property_identifier", "private_property_identifier":
		return true
	}

	return false
}

// sameNode reports whether a and b are the same node of one tree.
func sameNode(a, b *tree_sitter.Node) bool {
	return a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte() && a.Kind() == b.Kind()
}

// thisIsReceiver reports whether `this` at node still refers to the
// instance of the enclosing method or class. Arrow functions keep lexical
// this; any plain function or nested method (object literals, class
// expressions) between the call and the declaration rebinds it.
//
// A method directly in the body of the class decl keeps it. Such a method
// reaches here only when it is no symbol, such as a computed ([k])
// method. The class carries its calls, and its this is the same as in a
// named method of that class.
func thisIsReceiver(node, decl *tree_sitter.Node) bool {
	for p := node.Parent(); p != nil; p = p.Parent() {
		if p.StartByte() == decl.StartByte() && p.EndByte() == decl.EndByte() {
			return true
		}

		switch p.Kind() {
		case "function_expression", "function_declaration",
			"generator_function", "generator_function_declaration":
			return false
		case "method_definition":
			if !memberOfClass(p, decl) {
				return false
			}
		}
	}

	return false
}

// memberOfClass reports whether method lies directly in the body of the
// class decl. For any other decl it is false.
func memberOfClass(method, decl *tree_sitter.Node) bool {
	body := method.Parent()

	return body != nil && body.Kind() == "class_body" &&
		body.Parent() != nil && sameNode(body.Parent(), decl)
}

// atModuleTopLevel reports whether a declaration sits directly in the
// module scope (possibly wrapped in an export statement). Function-local
// declarations are not module symbols.
func atModuleTopLevel(decl *tree_sitter.Node) bool {
	p := decl.Parent()
	if p != nil && p.Kind() == "export_statement" {
		p = p.Parent()
	}

	return p != nil && p.Kind() == "program"
}

// enclosingClassName returns the class name for a method_definition that is
// a direct member of a top-level class declaration; "" otherwise. The shape
// check is strict (method → class_body → class declaration) because both
// grammars also nest method_definition inside object literals — a loose
// ancestor walk would claim `{ onClick() {} }` handlers inside a class
// member as methods of the class.
func enclosingClassName(method *tree_sitter.Node, src []byte) string {
	body := method.Parent()
	if body == nil || body.Kind() != "class_body" {
		return ""
	}

	class := body.Parent()
	if class == nil {
		return ""
	}

	switch class.Kind() {
	case "class_declaration", "abstract_class_declaration":
	default:
		return ""
	}

	if !atModuleTopLevel(class) {
		return ""
	}

	if name := class.ChildByFieldName("name"); name != nil {
		return name.Utf8Text(src)
	}

	return ""
}

// ecmaImport reads one import statement: source module, an optional
// namespace qualifier (`* as ns`), and the named imports.
func ecmaImport(stmt *tree_sitter.Node, src []byte, moduleKey string) (Import, bool) {
	srcNode := stmt.ChildByFieldName("source")
	if srcNode == nil {
		return Import{}, false
	}

	spec := strings.Trim(srcNode.Utf8Text(src), "'\"`")
	imp := Import{Path: spec, Resolved: resolveEcmaImport(moduleKey, spec)}

	// import_clause holds: default identifier, namespace_import, and/or
	// named_imports. Walk it by kind; fields are not declared for these.
	for i := uint(0); i < stmt.NamedChildCount(); i++ {
		clause := stmt.NamedChild(i)
		if clause.Kind() != "import_clause" {
			continue
		}

		for j := uint(0); j < clause.NamedChildCount(); j++ {
			switch c := clause.NamedChild(j); c.Kind() {
			case "namespace_import":
				// `* as ns` — the last named child is the identifier.
				if n := c.NamedChild(c.NamedChildCount() - 1); n != nil && n.Kind() == "identifier" {
					imp.LocalName = n.Utf8Text(src)
				}
			case "named_imports":
				for k := uint(0); k < c.NamedChildCount(); k++ {
					s := c.NamedChild(k)
					if s.Kind() != "import_specifier" {
						continue
					}
					// Aliased imports (`{foo as f}`) are skipped: the local
					// name differs from the exported one, so a lookup in the
					// target module would miss. No edge beats a wrong edge.
					if s.ChildByFieldName("alias") != nil {
						continue
					}
					if n := s.ChildByFieldName("name"); n != nil {
						imp.Named = append(imp.Named, n.Utf8Text(src))
					}
				}
				// Default imports (bare identifier) bind the module's default
				// export; resolving that requires export analysis — skipped.
			}
		}
	}

	return imp, true
}

// ecmaSourceExts are the extensions stripped when resolving a specifier to
// a module key. Only source extensions: trimming asset imports
// ("./button.css") would collide them with a sibling source module
// ("./button.ts" → key "button"), fabricating IMPORTS edges.
var ecmaSourceExts = map[string]bool{
	".js": true, ".jsx": true, ".mjs": true, ".cjs": true,
	".ts": true, ".tsx": true, ".mts": true, ".cts": true,
}

// resolveEcmaImport turns a relative specifier into a repo-relative module
// key ("" for bare/external specifiers). Directory imports ("./api" meaning
// ./api/index.ts) resolve to the directory path, which matches no file
// module key — they miss rather than guess; the index-file convention is
// future work.
func resolveEcmaImport(moduleKey, spec string) string {
	if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
		return ""
	}

	resolved := path.Join(path.Dir(moduleKey), spec)
	if ext := path.Ext(resolved); ecmaSourceExts[ext] {
		resolved = strings.TrimSuffix(resolved, ext)
	}

	if resolved == "" || strings.HasPrefix(resolved, "..") {
		return ""
	}

	return resolved
}

// ecmaSignature is the declaration's first non-decorator line up to any
// body, including `export`/`const` wrappers — exported-ness is part of the
// API. Arrow-function declarators find their body on the value node.
func ecmaSignature(decl *tree_sitter.Node, src []byte) string {
	start := decl

	for p := start.Parent(); p != nil; p = start.Parent() {
		switch p.Kind() {
		case "lexical_declaration", "variable_declaration", "export_statement":
			start = p
			continue
		}

		break
	}

	end := decl.EndByte()
	if body := decl.ChildByFieldName("body"); body != nil {
		end = body.StartByte()
	} else if v := decl.ChildByFieldName("value"); v != nil {
		if b := v.ChildByFieldName("body"); b != nil {
			end = b.StartByte()
		}
	}

	// Skip decorator NODES entirely: their arguments span lines
	// (@Component({...})), so line-based "@" filtering would leak the
	// decorator body into the signature.
	textStart := start.StartByte()

	for _, holder := range []*tree_sitter.Node{start, decl} {
		for i := uint(0); i < holder.NamedChildCount(); i++ {
			child := holder.NamedChild(i)
			if child.Kind() == "decorator" && child.EndByte() > textStart {
				textStart = child.EndByte()
			}
		}
	}

	if textStart >= end {
		textStart = start.StartByte()
	}

	return collapseWS(string(src[textStart:end]))
}
