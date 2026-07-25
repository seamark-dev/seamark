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
		CallRef{Qualifier: "inv", Name: "reload", Line: 28, Selector: true},
		"Settle should record the inv.reload() call")

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
	assert.Equal(t, CallRef{Name: "Close", Line: 6, Selector: true}, run.Calls[0])
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
