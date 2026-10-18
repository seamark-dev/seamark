package services

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The call rule of the selection sample (contracts §3.5). A sample never
// emits advice, records a firing, leases delivery state, opens the index
// for a write, or builds the index. A store that opens for a write and
// changes nothing leaves no trace on disk, so no behavior test can see
// it. These tests read the calls in the source instead.

// modulePath is the import path prefix of this module. The walk removes
// it from every name, so each entry of the banned list stays short.
const modulePath = "github.com/seamark-dev/seamark/"

// servicesPath is the import path of this package below the module.
const servicesPath = "internal/tui/services"

// sampleBannedCalls are the names that a sample must never reach. A name
// is "<package path below the module>.<name>"; a method of this package
// adds its receiver type. Each entry is a path.Match pattern, so one
// entry can match several names.
var sampleBannedCalls = []string{
	"internal/delivery.Deliver",
	"internal/report.ChangeSet",
	"internal/report.CheckAdvisory",
	"internal/reviews.RecordFiring*",
	"internal/reviews.BeginContextDelivery",
	"internal/store.Open",
	"internal/index.*",
	servicesPath + ".engine.openWrite",
}

// funcSource is one function of this package with the imports of its
// file. A selector resolves its package through these imports.
type funcSource struct {
	decl    *ast.FuncDecl
	imports map[string]string
}

// TestSampleCallsNoWriterOrRecorder checks that engine.Sample reaches no
// banned name. The walk follows the functions of this package and the
// methods of engine, so a helper such as openRead is covered too. It
// does not follow a method of another type of this package; Sample calls
// none. It does not follow a call into another package, because each
// engine owns the rules of its own calls.
func TestSampleCallsNoWriterOrRecorder(t *testing.T) {
	funcs := packageFuncs(t)
	names := reachedNames(t, funcs, "engine.Sample")

	for _, name := range names {
		for _, pattern := range sampleBannedCalls {
			banned, err := path.Match(pattern, name)
			require.NoError(t, err)
			assert.False(t, banned, "Sample reaches %s, which the call rule bans (%s)", name, pattern)
		}
	}

	// The walk reaches the open and the read of the sample, and it sees a
	// banned name where one exists. So a pass is not the result of an
	// empty walk.
	assert.Contains(t, names, servicesPath+".engine.openRead", "the walk reaches the index open of the sample")
	assert.Contains(t, names, "internal/report.ReadEvidence", "the walk reaches the evidence read")
	assert.Contains(t, reachedNames(t, funcs, "engine.openWrite"), "internal/store.Open")
}

// packageFuncs parses the production files of this package. The key is
// "<receiver type>.<name>" for a method and "<name>" for a function.
func packageFuncs(t *testing.T) map[string]funcSource {
	t.Helper()

	pkg, err := build.ImportDir(".", 0)
	require.NoError(t, err)

	fset := token.NewFileSet()
	funcs := map[string]funcSource{}

	for _, name := range pkg.GoFiles {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)

		imports := map[string]string{}

		for _, spec := range file.Imports {
			full, err := strconv.Unquote(spec.Path.Value)
			require.NoError(t, err)

			local := path.Base(full)
			if spec.Name != nil {
				local = spec.Name.Name
			}

			imports[local] = strings.TrimPrefix(full, modulePath)
		}

		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs[funcKey(fn)] = funcSource{decl: fn, imports: imports}
			}
		}
	}

	return funcs
}

// funcKey returns the key of fn in packageFuncs.
func funcKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}

	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}

	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}

	return fn.Name.Name
}

// reachedNames returns the sorted names that start reaches: the
// functions of this package that it calls, directly or through other
// functions of this package, and the names of other packages that they
// use.
func reachedNames(t *testing.T, funcs map[string]funcSource, start string) []string {
	t.Helper()

	require.Contains(t, funcs, start)

	seen := map[string]bool{}
	names := map[string]bool{}
	queue := []string{start}

	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]

		if seen[key] {
			continue
		}

		seen[key] = true
		names[servicesPath+"."+key] = true

		local, external := references(funcs[key])

		for _, name := range external {
			names[name] = true
		}

		for _, name := range local {
			if _, ok := funcs[name]; ok {
				queue = append(queue, name)
			}
		}
	}

	out := make([]string, 0, len(names))
	for name := range names {
		out = append(out, name)
	}

	sort.Strings(out)

	return out
}

// references returns the names that the body of fn uses. local holds
// the candidate keys of this package: a method on the receiver of fn,
// or a plain identifier. external holds the selectors of an imported
// package. A selector on any other value is a field or a method of that
// value, so the walk ignores it.
func references(fn funcSource) (local, external []string) {
	recvName, recvType := "", ""

	if key := funcKey(fn.decl); strings.Contains(key, ".") && len(fn.decl.Recv.List[0].Names) > 0 {
		recvName = fn.decl.Recv.List[0].Names[0].Name
		recvType = strings.SplitN(key, ".", 2)[0]
	}

	var visit func(ast.Node) bool

	visit = func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			id, ok := n.X.(*ast.Ident)
			if !ok {
				// The selected name belongs to a value, so only the
				// expression before the dot can hold a reference.
				ast.Inspect(n.X, visit)

				return false
			}

			if pkg, ok := fn.imports[id.Name]; ok {
				external = append(external, pkg+"."+n.Sel.Name)
			} else if recvName != "" && id.Name == recvName {
				local = append(local, recvType+"."+n.Sel.Name)
			}

			return false
		case *ast.Ident:
			local = append(local, n.Name)
		}

		return true
	}

	ast.Inspect(fn.decl.Body, visit)

	return local, external
}
