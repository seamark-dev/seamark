package report

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
	"github.com/seamark-dev/seamark/internal/store"
)

func TestChangeSetSaysWhenAFileHasNoIndexedSymbols(t *testing.T) {
	const noSymbols = "  defines                no indexed symbols — its callers and effect reach are unknown, not none\n"

	root := t.TempDir()

	st, err := store.Open(filepath.Join(root, "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// scripts/run.sh has history and no symbol, so the index knows it
	// without any structural evidence.
	sym := model.Symbol{FQN: "scripts/task.run", Name: "run", Kind: model.KindFunction,
		File: "scripts/task.py", Span: model.Span{StartLine: 1, EndLine: 5}}

	require.NoError(t, st.Rebuild(func(tx *store.Tx) error {
		if err := tx.InsertSymbol(&sym); err != nil {
			return err
		}

		return tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit, Ref: strings.Repeat("a", 40), TS: 100,
			Title: "Add the run script", Files: []string{"scripts/run.sh"}})
	}))

	var b strings.Builder
	require.NoError(t, ChangeSet(&b, st, root, []string{"scripts/run.sh", "scripts/task.py", "docs/new.md"}))

	out := b.String()
	assert.Contains(t, out, "scripts/run.sh\n"+noSymbols)
	assert.Contains(t, out, "scripts/task.py\n  defines                1 symbols\n")
	assert.Contains(t, out, "docs/new.md: not in the index (new file, or run `seamark index`)\n")
	assert.Equal(t, 1, strings.Count(out, "no indexed symbols"), "an unindexed name gets no defines line")

	// Each indexed file gets one defines line. A second line with a count
	// would contradict the unknown reach.
	assert.Equal(t, 2, strings.Count(out, "  defines "))
	assert.NotContains(t, out, "0 symbols")
}
