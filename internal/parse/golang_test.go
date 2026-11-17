package parse

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

const fixture = `// Package billing settles invoices.
package billing

import (
	"fmt"
	stdsql "database/sql"
	_ "embed"

	"example.com/app/internal/ledger"
)

// Charge is the card-charging entry point.
// It settles asynchronously.
func Charge(amount int) error {
	validate(amount)
	fmt.Printf("charging %d", amount)
	ledger.Record(amount)
	return nil
}

func validate(amount int) {}

// Invoice is one billing document.
type Invoice struct{ ID string }

// Settle marks the invoice paid.
func (inv *Invoice) Settle(db *stdsql.DB) error {
	inv.reload()
	return nil
}

func (inv Invoice) reload() {}

const MaxRetries = 3

var ErrClosed = fmt.Errorf("closed")
`

func extractFixture(t *testing.T) *FileResult {
	t.Helper()
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	t.Cleanup(ex.Close)
	res, err := ex.Extract("internal/billing/billing.go", []byte(fixture))
	require.NoError(t, err)
	return res
}

func symbolByFQN(t *testing.T, res *FileResult, fqn string) SymbolDecl {
	t.Helper()
	for _, d := range res.Symbols {
		if d.Symbol.FQN == fqn {
			return d
		}
	}
	require.Failf(t, "symbol not extracted", "want %q, got %v", fqn, fqns(res))
	return SymbolDecl{}
}

func fqns(res *FileResult) []string {
	var out []string
	for _, d := range res.Symbols {
		out = append(out, d.Symbol.FQN)
	}
	return out
}

func TestExtractSymbols(t *testing.T) {
	res := extractFixture(t)

	assert.Equal(t, "internal/billing", res.Package)

	want := map[string]model.SymbolKind{
		"internal/billing.Charge":         model.KindFunction,
		"internal/billing.validate":       model.KindFunction,
		"internal/billing.Invoice":        model.KindType,
		"internal/billing.Invoice.Settle": model.KindMethod,
		"internal/billing.Invoice.reload": model.KindMethod,
		"internal/billing.MaxRetries":     model.KindConst,
		"internal/billing.ErrClosed":      model.KindVar,
	}
	assert.Len(t, res.Symbols, len(want), "extracted FQNs: %v", fqns(res))
	for fqn, kind := range want {
		d := symbolByFQN(t, res, fqn)
		assert.Equal(t, kind, d.Symbol.Kind, "kind of %s", fqn)
	}
}

func TestExtractSignatureSpanDoc(t *testing.T) {
	res := extractFixture(t)

	charge := symbolByFQN(t, res, "internal/billing.Charge")
	assert.Equal(t, "func Charge(amount int) error", charge.Symbol.Sig)
	assert.EqualValues(t, 14, charge.Symbol.Span.StartLine)
	assert.NotEmpty(t, charge.Symbol.DocHash, "Charge has a doc comment")

	validate := symbolByFQN(t, res, "internal/billing.validate")
	assert.Empty(t, validate.Symbol.DocHash, "validate has no doc comment")

	settle := symbolByFQN(t, res, "internal/billing.Invoice.Settle")
	assert.Equal(t, "func (inv *Invoice) Settle(db *stdsql.DB) error", settle.Symbol.Sig)
	assert.NotEmpty(t, settle.Symbol.DocHash, "Settle has a doc comment")
}

func TestExtractImports(t *testing.T) {
	res := extractFixture(t)

	got := map[string]string{} // path -> local name
	for _, imp := range res.Imports {
		got[imp.Path] = imp.LocalName
	}
	assert.Equal(t, map[string]string{
		"fmt":                             "fmt",
		"database/sql":                    "stdsql",
		"embed":                           "",
		"example.com/app/internal/ledger": "ledger",
	}, got)
}

func TestExtractCallRefs(t *testing.T) {
	res := extractFixture(t)

	charge := symbolByFQN(t, res, "internal/billing.Charge")
	got := map[string]string{} // name -> qualifier
	for _, c := range charge.Calls {
		got[c.Name] = c.Qualifier
	}
	assert.Subset(t, got, map[string]string{
		"validate": "",       // bare call, same package
		"Printf":   "fmt",    // qualified via import
		"Record":   "ledger", // qualified via import
	}, "calls inside Charge")

	settle := symbolByFQN(t, res, "internal/billing.Invoice.Settle")
	assert.Contains(t, settle.Calls,
		CallRef{Qualifier: "inv", Name: "reload", Line: 28, Selector: true, Receiver: true},
		"Settle should record the inv.reload() call as a receiver call")

	// Bare calls are not selectors; the resolver depends on the distinction.
	assert.Contains(t, charge.Calls,
		CallRef{Name: "validate", Line: 15},
		"bare same-package call must have Selector=false")
}

func TestComplexOperandCallHasNoQualifier(t *testing.T) {
	src := `package p

type W struct{ db interface{ Close() } }

func (w *W) Run() {
	w.db.Close()
}
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("w.go", []byte(src))
	require.NoError(t, err)

	run := symbolByFQN(t, res, "W.Run")
	require.Len(t, run.Calls, 1)
	// The operand w.db is a selector, not an identifier: the call must be
	// marked Selector with no qualifier, so the resolver never treats it
	// as a bare same-package function reference.
	// Root keeps the start of the operand chain for the resolver, and the
	// qualifier stays empty.
	assert.Equal(t, CallRef{Name: "Close", Line: 6, Selector: true, Root: "w"}, run.Calls[0])
}

// TestGoSelectorOperandRoot proves which operand shapes give a root. The
// walk follows call functions and selector operands only. Every other node
// ends it with "", because its value can have any type.
func TestGoSelectorOperandRoot(t *testing.T) {
	src := `package p

func F() {
	strings.NewReplacer("a", "b").Replace("s")
	os.Stdout.Close()
	w.db.Flush()
	mcp.New(1).Serve()
	x.(T).Assert()
	xs[0].Index()
	pkg.G[int]().Inst()
	pkg.H[map[string]int]().TypeArgs()
	(&T{}).Paren()
	T{}.Lit()
	func() T { return T{} }().FuncLit()
	plain.Ident()
}
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	type got struct{ qualifier, root string }

	roots := map[string]got{}
	for _, c := range symbolByFQN(t, res, "F").Calls {
		roots[c.Name] = got{c.Qualifier, c.Root}
	}

	assert.Equal(t, got{"", "strings"}, roots["Replace"], "a chain from a package call")
	assert.Equal(t, got{"", "os"}, roots["Close"], "a chain from a package variable")
	assert.Equal(t, got{"", "w"}, roots["Flush"], "a chain from a local value")
	assert.Equal(t, got{"", "mcp"}, roots["Serve"], "a chain from a constructor call")
	assert.Equal(t, got{"", ""}, roots["Assert"], "a type assertion ends the walk")
	assert.Equal(t, got{"", ""}, roots["Index"], "an index ends the walk")
	assert.Equal(t, got{"", ""}, roots["Inst"], "an index or an instantiation ends the walk")
	assert.Equal(t, got{"", ""}, roots["TypeArgs"], "a type argument list ends the walk")
	assert.Equal(t, got{"", ""}, roots["Paren"], "a parenthesized operand ends the walk")
	assert.Equal(t, got{"", ""}, roots["Lit"], "a composite literal ends the walk")
	assert.Equal(t, got{"", ""}, roots["FuncLit"], "a function literal ends the walk")
	assert.Equal(t, got{"plain", ""}, roots["Ident"], "a plain identifier is the qualifier, not a root")
}

func TestImportLocalNamePrediction(t *testing.T) {
	src := `package p

import (
	"example.com/mod/v2"
	"github.com/x/go-thing"
	"strings"
)
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	got := map[string]string{}
	for _, imp := range res.Imports {
		got[imp.Path] = imp.LocalName
	}
	assert.Equal(t, map[string]string{
		"example.com/mod/v2":    "mod", // /vN is a version, not the package name
		"github.com/x/go-thing": "",    // not a valid identifier: unknowable, never guess
		"strings":               "strings",
	}, got)
}

func TestGroupedVarBlocks(t *testing.T) {
	src := `package p

// Grouped vars nest inside var_spec_list in the grammar.
var (
	A = 1
	B = 2
)

var C = 3

func f() {
	var local = 4
	_ = local
}
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	names := make([]string, 0, len(res.Symbols))
	for _, d := range res.Symbols {
		names = append(names, d.Symbol.Name)
	}
	assert.ElementsMatch(t, []string{"A", "B", "C", "f"}, names)

	a := symbolByFQN(t, res, "A")
	assert.Equal(t, model.KindVar, a.Symbol.Kind)
	assert.NotEmpty(t, a.Symbol.DocHash, "doc comment above the grouped block must hash")
}

func TestTypeAliases(t *testing.T) {
	src := `package p

// Legacy is the old name.
type Legacy = Modern

type Modern struct{}

type (
	Grouped      struct{}
	GroupedAlias = Grouped
)

func f() {
	type local = int

	var x local
	_ = x
}
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	// Aliases use the type_alias node kind, not type_spec — both grouped
	// and ungrouped forms must extract; function-local aliases must not.
	for _, fqn := range []string{"Legacy", "Modern", "Grouped", "GroupedAlias"} {
		d := symbolByFQN(t, res, fqn)
		assert.Equal(t, model.KindType, d.Symbol.Kind, "kind of %s", fqn)
	}

	legacy := symbolByFQN(t, res, "Legacy")
	assert.Equal(t, "Legacy = Modern", legacy.Symbol.Sig, "spec-text convention, like type_spec sigs")
	assert.NotEmpty(t, legacy.Symbol.DocHash, "doc comment above an alias must hash")

	for _, d := range res.Symbols {
		assert.NotEqual(t, "local", d.Symbol.Name, "function-local alias leaked")
	}
}

func TestFunctionLocalDeclsAreNotSymbols(t *testing.T) {
	src := `package p

func Work() {
	const local = 1
	var buf string
	type pair struct{ a, b int }
	_ = buf
	_ = pair{local, 2}
}

const Exported = 2
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	names := make([]string, 0, len(res.Symbols))
	for _, d := range res.Symbols {
		names = append(names, d.Symbol.Name)
	}
	assert.ElementsMatch(t, []string{"Work", "Exported"}, names,
		"function-local const/var/type must not become module symbols")
}

func TestReceiverForms(t *testing.T) {
	src := `package p

type Box[T any] struct{}

func (b *Box[T]) Put(v T) {}
func (Box[T]) Len() int { return 0 }
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("box.go", []byte(src))
	require.NoError(t, err)
	for _, fqn := range []string{"Box", "Box.Put", "Box.Len"} {
		symbolByFQN(t, res, fqn)
	}
}

// TestVarInitializerCalls proves that a package-level var carries the
// calls of its initializer, also inside function literals and in a
// grouped block, that every name of a spec carries all calls of that spec,
// and that a const or a var without a call carries none.
func TestVarInitializerCalls(t *testing.T) {
	src := `package p

import "os"

var runners = map[string]func(string) error{
	"write": func(p string) error {
		return os.WriteFile(p, nil, 0o600)
	},
	"audit": func(p string) error { return audit(p) },
}

var (
	ready = setup()
	plain = 4
)

var left, right = pick(), os.Getenv("X")

var prepare = audit

const Size = len("abc")

func audit(p string) error {
	var local = setup()
	_ = local
	return nil
}

func setup() bool { return true }

func pick() int { return 1 }
`
	ex, err := NewGoExtractor()
	require.NoError(t, err)
	defer ex.Close()

	res, err := ex.Extract("p.go", []byte(src))
	require.NoError(t, err)

	runners := symbolByFQN(t, res, "runners")
	assert.Equal(t, model.KindVar, runners.Symbol.Kind)
	assert.ElementsMatch(t, []CallRef{
		{Qualifier: "os", Name: "WriteFile", Line: 7, Selector: true},
		{Name: "audit", Line: 9},
	}, runners.Calls)

	assert.Equal(t, []CallRef{{Name: "setup", Line: 13}}, symbolByFQN(t, res, "ready").Calls,
		"a spec in a grouped block carries its own calls only")
	assert.Empty(t, symbolByFQN(t, res, "plain").Calls)

	both := []CallRef{
		{Name: "pick", Line: 17},
		{Qualifier: "os", Name: "Getenv", Line: 17, Selector: true},
	}
	assert.ElementsMatch(t, both, symbolByFQN(t, res, "left").Calls, "every name carries every call of its spec")
	assert.ElementsMatch(t, both, symbolByFQN(t, res, "right").Calls, "every name carries every call of its spec")

	assert.Empty(t, symbolByFQN(t, res, "prepare").Calls, "a function value without a call is no call")
	assert.Empty(t, symbolByFQN(t, res, "Size").Calls, "const initializers are not mined")

	assert.NotContains(t, fqns(res), "local", "a function-local var is no symbol")
	assert.Contains(t, symbolByFQN(t, res, "audit").Calls, CallRef{Name: "setup", Line: 24},
		"the call of a local var initializer stays with its function")
}
