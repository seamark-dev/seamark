package store

import (
	"database/sql"

	"github.com/seamark-dev/seamark/internal/model"
)

// Health queries: the aggregate reads behind `seamark status`. Each is a
// plain count over one table — cheap enough to run on every status call.

// EdgeOriginCounts returns CALL edge counts by resolution origin
// (qualified, same-package, unique-name, …) — the confidence
// distribution a consumer needs to weigh the graph's answers. Only call
// edges: defines/imports edges are structural facts with no resolution
// uncertainty, and mixing them in understates the call percentages.
func (s *Store) EdgeOriginCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT origin, COUNT(*) FROM edge WHERE kind = ? GROUP BY origin`,
		model.EdgeCalls)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Lazily allocated: an empty result stays nil, so the JSON forms
	// (omitempty) round-trip without an empty-vs-nil mismatch.
	var out map[string]int

	for rows.Next() {
		var (
			origin string
			n      int
		)

		if err := rows.Scan(&origin, &n); err != nil {
			return nil, err
		}

		if out == nil {
			out = map[string]int{}
		}

		out[origin] = n
	}

	return out, rows.Err()
}

// EffectOriginCounts returns how many distinct symbols carry direct sink
// tags versus propagated ones. Matching is case-insensitive: the indexer
// writes lowercase origins while the schema comment long claimed
// uppercase — tolerate both in databases that already exist.
func (s *Store) EffectOriginCounts() (direct, propagated int, err error) {
	err = s.db.QueryRow(
		`SELECT COUNT(DISTINCT symbol_id) FROM effect WHERE lower(origin) = 'direct'`).Scan(&direct)
	if err != nil {
		return 0, 0, err
	}

	err = s.db.QueryRow(
		`SELECT COUNT(DISTINCT symbol_id) FROM effect WHERE lower(origin) != 'direct'`).Scan(&propagated)

	return direct, propagated, err
}

// HistoryWindow describes the mined decision evidence: how much and how
// old — an answer backed by three commits from 2019 must not read like
// one backed by three hundred from last month.
type HistoryWindow struct {
	Decisions int
	OldestTS  int64
	NewestTS  int64
	MedianTS  int64
}

// History returns the decision evidence window; zero values when no
// history was mined.
func (s *Store) History() (HistoryWindow, error) {
	var w HistoryWindow

	err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(MIN(ts), 0), COALESCE(MAX(ts), 0) FROM decision`).
		Scan(&w.Decisions, &w.OldestTS, &w.NewestTS)
	if err != nil {
		return w, err
	}

	if w.Decisions == 0 {
		return w, nil
	}

	err = s.db.QueryRow(
		`SELECT ts FROM decision ORDER BY ts LIMIT 1 OFFSET ?`, w.Decisions/2).
		Scan(&w.MedianTS)
	if err == sql.ErrNoRows {
		return w, nil
	}

	return w, err
}

// FindingCounts returns mined findings by source (review, fix:…, revert).
func (s *Store) FindingCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT source, COUNT(*) FROM finding GROUP BY source`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Lazily allocated for the same empty-vs-nil reason as
	// EdgeOriginCounts; readers index into it, which is nil-safe.
	var out map[string]int

	for rows.Next() {
		var (
			source string
			n      int
		)

		if err := rows.Scan(&source, &n); err != nil {
			return nil, err
		}

		if out == nil {
			out = map[string]int{}
		}

		out[source] = n
	}

	return out, rows.Err()
}
