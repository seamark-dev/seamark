package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file holds the syntax scan of the shadow tests. Go embedding has
// no virtual dispatch. So a shared type that a view or a holder embeds
// never reads a field or calls a method of the outer type. An outer
// member with the name of an embedded member hides it, and the shared
// code then reads another value than the outer code. The scan finds
// such names for previewFlow, listPane and filterInput.

// embedScan is the part of the package syntax that the shadow tests
// read. It holds the members of each type and the embedders of each
// type.
type embedScan struct {
	// members maps a type name to the names of its fields and methods.
	members map[string][]string
	// embedders maps a type name to the struct types that embed it.
	embedders map[string]map[string]bool
}

// scanEmbedding parses the non-test files of the package. The test
// files stay out, because a test type never embeds a shared type of the
// console.
func scanEmbedding(t *testing.T) embedScan {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()
	scan := embedScan{members: map[string][]string{}, embedders: map[string]map[string]bool{}}

	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		for _, decl := range file.Decls {
			scan.add(decl)
		}
	}

	return scan
}

// add records the members and the embedded types of one declaration.
func (s embedScan) add(decl ast.Decl) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			recv := baseTypeName(d.Recv.List[0].Type)
			s.members[recv] = append(s.members[recv], d.Name.Name)
		}
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}

			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				continue
			}

			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					s.embed(baseTypeName(field.Type), ts.Name.Name)
				}

				for _, n := range field.Names {
					s.members[ts.Name.Name] = append(s.members[ts.Name.Name], n.Name)
				}
			}
		}
	}
}

// embed records that the struct outer embeds the type inner.
func (s embedScan) embed(inner, outer string) {
	if s.embedders[inner] == nil {
		s.embedders[inner] = map[string]bool{}
	}

	s.embedders[inner][outer] = true
}

// hidden returns each member of an embedder of inner that has the name
// of an inner member, as "outer.name", in a stable order.
func (s embedScan) hidden(inner string) []string {
	own := s.members[inner]

	var out []string

	for outer := range s.embedders[inner] {
		for _, name := range s.members[outer] {
			if slices.Contains(own, name) {
				out = append(out, outer+"."+name)
			}
		}
	}

	slices.Sort(out)

	return out
}

// assertShadowsNothing checks that no embedder of inner declares a
// field or a method with the name of an inner member. sample lists inner
// members, and want lists embedders. The scan must find both, so a
// rename cannot make the check pass without a check.
func (s embedScan) assertShadowsNothing(t *testing.T, inner string, sample, want []string) {
	t.Helper()

	require.Subset(t, s.members[inner], sample, "the scan finds the %s members", inner)

	for _, outer := range want {
		assert.True(t, s.embedders[inner][outer], "%s embeds %s", outer, inner)
	}

	assert.Empty(t, s.hidden(inner), "these members hide a member of %s", inner)
}

// baseTypeName returns the base type name of a receiver or a field type:
// "previewFlow" for *previewFlow[P], "maintModal" for *maintModal.
func baseTypeName(expr ast.Expr) string {
	for {
		switch e := expr.(type) {
		case *ast.StarExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.IndexListExpr:
			expr = e.X
		case *ast.Ident:
			return e.Name
		default:
			return ""
		}
	}
}

func TestBaseTypeNameStripsPointersAndTypeArguments(t *testing.T) {
	for src, want := range map[string]string{
		"*previewFlow[P]":         "previewFlow",
		"listPane[pinRow, pinID]": "listPane",
		"maintModal":              "maintModal",
		"*maintModal":             "maintModal",
		"[]int":                   "",
		"pkg.Type":                "",
	} {
		expr, err := parser.ParseExpr(src)
		require.NoError(t, err, src)
		assert.Equal(t, want, baseTypeName(expr), src)
	}
}

func TestEmbedScanFindsAHiddenMember(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "x.go", `package x
type inner struct{ seen int }
func (i *inner) close() {}
type outer struct {
	*inner
	seen bool
}
func (o outer) close() {}
`, parser.SkipObjectResolution)
	require.NoError(t, err)

	scan := embedScan{members: map[string][]string{}, embedders: map[string]map[string]bool{}}

	for _, decl := range file.Decls {
		scan.add(decl)
	}

	assert.ElementsMatch(t, []string{"seen", "close"}, scan.members["inner"])
	assert.Equal(t, map[string]bool{"outer": true}, scan.embedders["inner"])

	assert.Equal(t, []string{"outer.close", "outer.seen"}, scan.hidden("inner"))
	assert.Empty(t, scan.hidden("outer"), "nothing embeds outer")
}
