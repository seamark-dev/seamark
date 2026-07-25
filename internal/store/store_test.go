package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "index.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// seed populates a small two-package graph:
//
//	pkg/a.Run -> pkg/b.Helper (CALLS), with co-change and one decision.
func seed(t *testing.T, s *Store) (run, helper model.Symbol) {
	t.Helper()
	run = model.Symbol{FQN: "pkg/a.Run", Name: "Run", Kind: model.KindFunction,
		File: "pkg/a/a.go", Span: model.Span{StartLine: 10, EndLine: 20}, Sig: "func Run() error"}
	helper = model.Symbol{FQN: "pkg/b.Helper", Name: "Helper", Kind: model.KindFunction,
		File: "pkg/b/b.go", Span: model.Span{StartLine: 5, EndLine: 8}}

	err := s.Rebuild(func(tx *Tx) error {
		for _, sym := range []*model.Symbol{&run, &helper} {
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}
		}
		if err := tx.InsertEdge(model.Edge{Src: run.ID, Dst: helper.ID,
			Kind: model.EdgeCalls, Origin: model.OriginQualified}); err != nil {
			return err
		}
		if err := tx.InsertCoChange(model.CoChange{FileA: "pkg/a/a.go", FileB: "pkg/b/b.go",
			Together: 7, Total: 40, Lift: 3.5}); err != nil {
			return err
		}
		return tx.InsertDecision(&model.Decision{Kind: model.DecisionCommit,
			Ref: "abc123", TS: 1700000000, Author: "yuri",
			Title: "extract Helper", Files: []string{"pkg/a/a.go", "pkg/b/b.go"}})
	})
	require.NoError(t, err, "seed rebuild")
	return run, helper
}

func TestFindSymbolsExactAndSuffix(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	for _, query := range []string{"pkg/a.Run", "Run", "a.Run"} {
		syms, err := s.FindSymbols(query, 5)
		require.NoError(t, err, "FindSymbols(%q)", query)
		require.NotEmpty(t, syms, "FindSymbols(%q)", query)
		assert.Equal(t, "pkg/a.Run", syms[0].FQN, "FindSymbols(%q) best match", query)
	}
}

func TestFindSymbolsFTSPrefix(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	// "help" matches Helper only via FTS prefix search.
	syms, err := s.FindSymbols("help", 5)
	require.NoError(t, err)
	require.Len(t, syms, 1)
	assert.Equal(t, "Helper", syms[0].Name)
}

func TestCallersAndCallees(t *testing.T) {
	s := openTestStore(t)
	run, helper := seed(t, s)

	callers, err := s.Callers(helper.ID)
	require.NoError(t, err)
	require.Len(t, callers, 1)
	assert.Equal(t, run.FQN, callers[0].FQN)

	callees, err := s.Callees(run.ID)
	require.NoError(t, err)
	require.Len(t, callees, 1)
	assert.Equal(t, helper.FQN, callees[0].FQN)
}

func TestCoChangePartnersEitherOrientation(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	for _, file := range []string{"pkg/a/a.go", "pkg/b/b.go"} {
		partners, err := s.CoChangePartners(file, 1.0, 10)
		require.NoError(t, err, "CoChangePartners(%s)", file)
		require.Len(t, partners, 1, "CoChangePartners(%s)", file)
		assert.Equal(t, 7, partners[0].Together)
	}

	// Below-lift pairs are filtered.
	partners, err := s.CoChangePartners("pkg/a/a.go", 4.0, 10)
	require.NoError(t, err)
	assert.Empty(t, partners, "minLift must filter chance-level pairs")
}

func TestDecisionsForFile(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	decisions, err := s.DecisionsForFile("pkg/b/b.go", 10)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	assert.Equal(t, "abc123", decisions[0].Ref)
}

func TestRebuildReplacesDerivedTables(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	// Second rebuild with a single different symbol replaces everything.
	err := s.Rebuild(func(tx *Tx) error {
		return tx.InsertSymbol(&model.Symbol{FQN: "pkg/c.New", Name: "New",
			Kind: model.KindFunction, File: "pkg/c/c.go"})
	})
	require.NoError(t, err)

	st, err := s.Stats()
	require.NoError(t, err)
	assert.Equal(t, Stats{Symbols: 1}, st, "only the replacement symbol should remain")

	// FTS must reflect the new content, not the old.
	stale, err := s.FindSymbols("help", 5)
	require.NoError(t, err)
	assert.Empty(t, stale, "stale FTS rows survived rebuild")

	syms, err := s.FindSymbols("new", 5)
	require.NoError(t, err)
	assert.Len(t, syms, 1)
}

func TestRebuildRollsBackOnError(t *testing.T) {
	s := openTestStore(t)
	seed(t, s)

	errBoom := errors.New("intentional")
	err := s.Rebuild(func(tx *Tx) error {
		if err := tx.InsertSymbol(&model.Symbol{FQN: "x.Boom", Name: "Boom",
			Kind: model.KindFunction}); err != nil {
			return err
		}
		return errBoom
	})
	require.ErrorIs(t, err, errBoom)

	// Original content must be intact.
	syms, err := s.FindSymbols("Helper", 5)
	require.NoError(t, err)
	assert.Len(t, syms, 1, "failed rebuild must roll back to the previous graph")
}

func TestOpenPathWithURISpecialChars(t *testing.T) {
	// "file:" DSNs are URIs: ?, # and %XX in the path used to truncate it,
	// silently creating the database at the wrong location.
	for _, dir := range []string{"what?repo", "repo#1", "pct%41"} {
		path := filepath.Join(t.TempDir(), dir, "index.db")
		s, err := Open(path)
		require.NoError(t, err, "Open(%q)", path)
		require.NoError(t, s.SetMeta("k", "v"))
		require.NoError(t, s.Close())
		assert.FileExists(t, path, "db must be created at the exact requested path")
	}
}

func TestFindSymbolsFTSRankOrder(t *testing.T) {
	s := openTestStore(t)

	// The weaker match gets the LOWER rowid: with the old IN-subquery form
	// results came back in rowid order, so this ordering would flip.
	weak := model.Symbol{FQN: "zz.chargeback", Name: "chargeback",
		Kind: model.KindFunction, File: "zz/b.go"}
	strong := model.Symbol{FQN: "charge/charge.Charge", Name: "Charge",
		Kind: model.KindFunction, File: "charge/charge.go"}

	err := s.Rebuild(func(tx *Tx) error {
		for _, sym := range []*model.Symbol{&weak, &strong} {
			if err := tx.InsertSymbol(sym); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)

	// "charg" hits neither the exact nor the suffix stage; only FTS ranks.
	syms, err := s.FindSymbols("charg", 5)
	require.NoError(t, err)
	require.Len(t, syms, 2)
	assert.Equal(t, strong.FQN, syms[0].FQN, "best-ranked FTS match must come first")
}

func TestMetaSurvivesRebuild(t *testing.T) {
	s := openTestStore(t)
	require.NoError(t, s.SetMeta("repo_root", "/tmp/x"))
	seed(t, s)

	v, err := s.GetMeta("repo_root")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/x", v)

	missing, err := s.GetMeta("missing")
	require.NoError(t, err)
	assert.Empty(t, missing)
}
