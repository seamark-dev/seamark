package report

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

// historyStore builds an index with one function in file, one co-change
// partner of file, and six commits on file, of which two are fixes. Six
// commits pass the least history that the fix-density line needs.
func historyStore(t *testing.T, file string) (st *store.Store, root string) {
	t.Helper()

	root = t.TempDir()

	var err error

	st, err = store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	tax := model.Symbol{FQN: "internal/billing.Tax", Name: "Tax", Kind: model.KindFunction,
		File: file, Span: model.Span{StartLine: 1, EndLine: 3}}

	titles := []string{"fix: round half up", "add VAT table", "fix: zero rate", "rename helpers", "docs", "extend rates"}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		if err := tx.InsertSymbol(&tax); err != nil {
			return err
		}

		pair := model.CoChange{FileA: file, FileB: "internal/billing/rates.go", Together: 4, Total: 18, Lift: 2.5}
		if err := tx.InsertCoChange(pair); err != nil {
			return err
		}

		for i, title := range titles {
			err := tx.InsertDecision(&model.Decision{
				Kind: model.DecisionCommit, Ref: fmt.Sprintf("sha%02d", i), TS: int64(1000 - i),
				Author: "a", Title: title, Files: []string{file},
			})
			if err != nil {
				return err
			}
		}

		return nil
	}))

	return st, root
}

// TestWhyHistoryHeadersNameTheFile proves that each history header of why
// names the file it describes. The co-change, fix-density, and decision
// sections read the history of the whole file, also under a symbol. A
// header without the file lets a reader take them for the history of the
// symbol. The file goes into the existing header line, so the report keeps
// its line count.
func TestWhyHistoryHeadersNameTheFile(t *testing.T) {
	const file = "internal/billing/tax.go"

	st, root := historyStore(t, file)

	coChange := "usually changed with  (whole file internal/billing/tax.go; empirical, lift > 1 means beyond chance)"
	density := "fix density  2 of the last 6 commits to internal/billing/tax.go were fixes"
	decisions := "recent decisions  (whole file internal/billing/tax.go)"

	partnerLine := regexp.MustCompile(`^\s*\d+/\d+\s+commits\s+lift\s+\S+\s+internal/billing/rates\.go`)
	decisionLine := regexp.MustCompile(`^ {2}\d{4}-\d{2}-\d{2} {2}sha00\s+fix: round half up`)

	// The symbol report and the file report print the same history, so
	// both carry the same headers.
	for _, query := range []string{"internal/billing.Tax", file} {
		var b strings.Builder
		require.NoError(t, Why(&b, st, root, query))

		lines := strings.Split(b.String(), "\n")

		for _, header := range []string{coChange, density, decisions} {
			i := slices.Index(lines, header)
			require.NotEqual(t, -1, i, "%s: why prints %q\n%s", query, header, b.String())

			// A blank line opens each section, as before the file went
			// into the header.
			assert.Empty(t, lines[i-1], "%s: a blank line comes before %q", query, header)
		}

		// The content follows its header on the next line, so the change
		// adds no line.
		assert.Regexp(t, partnerLine, lines[slices.Index(lines, coChange)+1], query)
		assert.Regexp(t, decisionLine, lines[slices.Index(lines, decisions)+1], query)
	}
}

// TestWhyHistoryHeadersSanitizeTheFile proves that the file name in a
// history header is sanitized. The name is repository text, so a control
// byte in it must not reach the terminal.
func TestWhyHistoryHeadersSanitizeTheFile(t *testing.T) {
	st, root := historyStore(t, "internal/billing/t\x1b[2Jax.go")

	var b strings.Builder
	require.NoError(t, Why(&b, st, root, "internal/billing.Tax"))

	lines := strings.Split(b.String(), "\n")

	assert.Contains(t, lines, "recent decisions  (whole file internal/billing/t[2Jax.go)", b.String())
	assert.Contains(t, lines,
		"usually changed with  (whole file internal/billing/t[2Jax.go; empirical, lift > 1 means beyond chance)", b.String())
	assert.Contains(t, lines, "fix density  2 of the last 6 commits to internal/billing/t[2Jax.go were fixes", b.String())
}

// TestSkillsQuoteTheWhyHistoryHeaders proves that the skills reference
// quotes the real history headers of why, with <path> in place of the
// file, and says that the history is file grain. Agents read the
// reference to interpret why, so a header change must reach it.
func TestSkillsQuoteTheWhyHistoryHeaders(t *testing.T) {
	refs, err := filepath.Glob(filepath.Join("..", "..", "skills", "*", "references", "interpreting-seamark.md"))
	require.NoError(t, err)
	require.Len(t, refs, 3, "each of the three skills carries the reference")

	for _, ref := range refs {
		data, err := os.ReadFile(ref)
		require.NoError(t, err)

		text := string(data)

		assert.Contains(t, text, "`"+coChangeHeader("<path>")+"`", ref)
		assert.Contains(t, text, "`"+decisionsHeader("<path>")+"`", ref)
		assert.Contains(t, text, "describe the whole file", ref)
	}
}
