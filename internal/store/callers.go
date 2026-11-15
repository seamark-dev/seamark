package store

import (
	"database/sql/driver"
	"strings"

	"modernc.org/sqlite"

	"github.com/seamark-dev/seamark/internal/model"
)

// The caller queries classify caller files and rank edge origins inside
// SQL. They call the Go rules through these functions, so SQL holds no
// second copy of a rule that could drift from model.IsTestPath or
// model.OriginConfidence.
const (
	sqlIsTest     = "seamark_is_test"
	sqlOriginRank = "seamark_origin_rank"
)

// init registers the SQL functions once per process. The driver adds them
// to each connection that opens after the registration, and package
// initialization runs before any Open.
func init() {
	sqlite.MustRegisterDeterministicScalarFunction(sqlIsTest, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if model.IsTestPath(textArg(args[0])) {
			return int64(1), nil
		}

		return int64(0), nil
	})

	sqlite.MustRegisterDeterministicScalarFunction(sqlOriginRank, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		return int64(model.OriginConfidence(textArg(args[0]))), nil
	})
}

// textArg returns a SQL argument as text. NULL and a non-text value give
// the empty text. The rules treat an empty path as production code and an
// empty origin as the lowest rank, so a missing value never matches.
func textArg(v driver.Value) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}

// CallerStat counts the CALLS callers of one symbol. The edge key holds
// the caller symbol, so each count is a count of distinct caller symbols,
// not of call expressions.
type CallerStat struct {
	// Total counts every caller: same-file callers, callers without a
	// file (package nodes), and test callers included.
	Total int

	// OtherFiles counts the callers whose file is not empty, is not the
	// file of the symbol, and is not test code.
	OtherFiles int

	// Tests counts the callers in test files, the file of the symbol
	// included.
	Tests int

	// NameMatch counts the callers that the indexer resolved by a unique
	// name only, the least trusted origin.
	NameMatch int
}

// CallerStats returns the caller counts of each symbol in file. It runs
// one query for the whole file, so the work does not grow with the number
// of symbols. A symbol without callers has no entry.
func (s *Store) CallerStats(file string) (map[int64]CallerStat, error) {
	rows, err := s.db.Query(
		`SELECT e.dst,
		        COUNT(*),
		        SUM(c.file <> '' AND c.file <> d.file AND NOT `+sqlIsTest+`(c.file)),
		        SUM(`+sqlIsTest+`(c.file)),
		        SUM(e.origin = ?)
		 FROM edge e
		 JOIN symbol d ON d.id = e.dst
		 JOIN symbol c ON c.id = e.src
		 WHERE d.file = ? AND e.kind = ?
		 GROUP BY e.dst`,
		model.OriginUniqueName, file, string(model.EdgeCalls),
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	out := map[int64]CallerStat{}

	for rows.Next() {
		var (
			id int64
			st CallerStat
		)

		if err := rows.Scan(&id, &st.Total, &st.OtherFiles, &st.Tests, &st.NameMatch); err != nil {
			return nil, err
		}

		out[id] = st
	}

	return out, rows.Err()
}

// CallerSample returns at most limit callers of each symbol in ids. The
// order puts callers in other non-test files first, then the other callers
// outside test files, then test callers. Inside a group, the most trusted
// origin leads, and FQN, file, start line, and ID make the order stable.
// SQL applies the limit, so the rows read do not grow with the fan-in.
// file is the file of the symbols in ids.
func (s *Store) CallerSample(file string, ids []int64, limit int) (map[int64][]CallEdge, error) {
	if len(ids) == 0 || limit <= 0 {
		return nil, nil
	}

	args := make([]any, 0, len(ids)+4)
	args = append(args, file, string(model.EdgeCalls))

	for _, id := range ids {
		args = append(args, id)
	}

	args = append(args, limit)

	rows, err := s.db.Query(
		`SELECT dst, `+callerCols+`, origin FROM (
		   SELECT e.dst AS dst, e.origin AS origin, `+symbolColsPrefixed+`,
		          ROW_NUMBER() OVER (
		            PARTITION BY e.dst
		            ORDER BY CASE
		                       WHEN `+sqlIsTest+`(s.file) THEN 2
		                       WHEN s.file <> '' AND s.file <> ? THEN 0
		                       ELSE 1
		                     END,
		                     `+sqlOriginRank+`(e.origin) DESC,
		                     s.fqn, s.file, s.start_line, s.id
		          ) AS rn
		   FROM edge e JOIN symbol s ON s.id = e.src
		   WHERE e.kind = ? AND e.dst IN (`+placeholders(len(ids))+`)
		 )
		 WHERE rn <= ?
		 ORDER BY dst, rn`,
		args...,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	out := map[int64][]CallEdge{}

	for rows.Next() {
		var (
			dst  int64
			c    CallEdge
			kind string
		)

		err := rows.Scan(&dst, &c.ID, &c.FQN, &c.Name, &kind, &c.File,
			&c.Span.StartLine, &c.Span.StartCol, &c.Span.EndLine, &c.Span.EndCol,
			&c.Sig, &c.DocHash, &c.Origin)
		if err != nil {
			return nil, err
		}

		c.Kind = model.SymbolKind(kind)
		out[dst] = append(out[dst], c)
	}

	return out, rows.Err()
}

// callerCols names the symbol columns of the inner caller query, in the
// order of symbolColsPrefixed.
const callerCols = `id, fqn, name, kind, file, start_line, start_col, end_line, end_col, sig, doc_hash`

// placeholders returns n comma-separated SQL parameters.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}
