// Package store owns the on-disk index: a single SQLite file holding the
// structure, history, effects, and policy tables (RFC-001 §5.1).
//
// The store is deliberately dumb: it validates nothing about the graph and
// contains no language knowledge. Writers (the indexer, the history miner)
// batch rows inside Rebuild; readers get focused query helpers tuned for the
// CLI/LSP/MCP surfaces.
package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/seamark-dev/seamark/internal/model"
)

//go:embed schema.sql
var schemaSQL string

// DefaultDir is the workspace-relative directory holding seamark state.
const DefaultDir = ".seamark"

// DefaultDBName is the index filename inside DefaultDir.
const DefaultDBName = "index.db"

// Store wraps the SQLite index database.
type Store struct {
	db *sql.DB
}

// DefaultPath returns the conventional index location for a workspace root.
func DefaultPath(root string) string {
	return filepath.Join(root, DefaultDir, DefaultDBName)
}

// Open opens (creating if necessary) the index database at path and applies
// the schema. The schema is idempotent; opening an existing index is cheap.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create index dir: %w", err)
	}

	// "file:" DSNs are parsed as URIs: a raw %, ? or # in the workspace
	// path would truncate it or swallow the pragmas. Escape those bytes.
	escaped := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)
	dsn := "file:" + escaped + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// The index has a single writer and local readers; one connection avoids
	// SQLITE_BUSY without a retry dance.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close() // the schema error is the one worth reporting
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// SetMeta stores an index bookkeeping value (schema version, repo root, …).
func (s *Store) SetMeta(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value,
	)

	return err
}

// GetMeta returns the stored value or "" when absent.
func (s *Store) GetMeta(key string) (string, error) {
	var v string

	err := s.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}

	return v, err
}

// Tx is the write handle passed to a Rebuild callback.
type Tx struct {
	tx *sql.Tx
}

// Rebuild atomically replaces the derived tables (structure + history +
// effects). Policy and learning tables (rule, lesson) and meta survive, as
// they are user/agent state rather than derivations of the current tree.
// The FTS index is rebuilt after the callback succeeds.
func (s *Store) Rebuild(fn func(tx *Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	// Wipe in dependency order.
	for _, table := range []string{
		"decision_link", "decision_file", "decision",
		"cochange", "effect", "edge", "symbol",
	} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return fmt.Errorf("store: wipe %s: %w", table, err)
		}
	}

	if err := fn(&Tx{tx: tx}); err != nil {
		return err
	}

	if _, err := tx.Exec(`INSERT INTO symbol_fts(symbol_fts) VALUES ('rebuild')`); err != nil {
		return fmt.Errorf("store: rebuild fts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit rebuild: %w", err)
	}

	return nil
}

// InsertSymbol stores sym and sets sym.ID.
func (t *Tx) InsertSymbol(sym *model.Symbol) error {
	res, err := t.tx.Exec(
		`INSERT INTO symbol (fqn, name, kind, file, start_line, start_col, end_line, end_col, sig, doc_hash)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sym.FQN, sym.Name, string(sym.Kind), sym.File,
		sym.Span.StartLine, sym.Span.StartCol, sym.Span.EndLine, sym.Span.EndCol,
		sym.Sig, sym.DocHash,
	)
	if err != nil {
		return fmt.Errorf("store: insert symbol %s: %w", sym.FQN, err)
	}

	sym.ID, err = res.LastInsertId()

	return err
}

// InsertEdge stores a structural edge; duplicate (src,dst,kind) rows are
// ignored so callers need not dedupe.
func (t *Tx) InsertEdge(e model.Edge) error {
	_, err := t.tx.Exec(
		`INSERT INTO edge (src, dst, kind, origin) VALUES (?, ?, ?, ?)
		 ON CONFLICT DO NOTHING`,
		e.Src, e.Dst, string(e.Kind), e.Origin,
	)
	if err != nil {
		return fmt.Errorf("store: insert edge %d->%d: %w", e.Src, e.Dst, err)
	}

	return nil
}

// InsertCoChange stores one co-change pair (canonical file order expected).
func (t *Tx) InsertCoChange(c model.CoChange) error {
	_, err := t.tx.Exec(
		`INSERT INTO cochange (file_a, file_b, together, total, lift)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (file_a, file_b) DO UPDATE SET
		   together = excluded.together, total = excluded.total, lift = excluded.lift`,
		c.FileA, c.FileB, c.Together, c.Total, c.Lift,
	)
	if err != nil {
		return fmt.Errorf("store: insert cochange %s/%s: %w", c.FileA, c.FileB, err)
	}

	return nil
}

// InsertDecision stores d (with its file links) and sets d.ID.
func (t *Tx) InsertDecision(d *model.Decision) error {
	res, err := t.tx.Exec(
		`INSERT INTO decision (kind, ref, ts, author, title, body)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		string(d.Kind), d.Ref, d.TS, d.Author, d.Title, d.Body,
	)
	if err != nil {
		return fmt.Errorf("store: insert decision %s: %w", d.Ref, err)
	}

	if d.ID, err = res.LastInsertId(); err != nil {
		return err
	}

	for _, f := range d.Files {
		if _, err := t.tx.Exec(
			`INSERT INTO decision_file (decision_id, file) VALUES (?, ?)
			 ON CONFLICT DO NOTHING`, d.ID, f,
		); err != nil {
			return fmt.Errorf("store: link decision %s to %s: %w", d.Ref, f, err)
		}
	}

	return nil
}

const symbolCols = `id, fqn, name, kind, file, start_line, start_col, end_line, end_col, sig, doc_hash`

func scanSymbol(row interface{ Scan(...any) error }) (model.Symbol, error) {
	var s model.Symbol
	var kind string

	err := row.Scan(&s.ID, &s.FQN, &s.Name, &kind, &s.File,
		&s.Span.StartLine, &s.Span.StartCol, &s.Span.EndLine, &s.Span.EndCol,
		&s.Sig, &s.DocHash)

	s.Kind = model.SymbolKind(kind)

	return s, err
}

func (s *Store) querySymbols(q string, args ...any) ([]model.Symbol, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []model.Symbol

	for rows.Next() {
		sym, err := scanSymbol(rows)
		if err != nil {
			return nil, err
		}

		out = append(out, sym)
	}

	return out, rows.Err()
}

// FindSymbols resolves a user query to symbols, best matches first. It tries
// exact FQN, exact name, FQN suffix, then FTS prefix search, deduplicating
// across stages.
func (s *Store) FindSymbols(query string, limit int) ([]model.Symbol, error) {
	if limit <= 0 {
		limit = 10
	}

	seen := make(map[int64]bool)
	var out []model.Symbol

	add := func(syms []model.Symbol) {
		for _, sym := range syms {
			if !seen[sym.ID] && len(out) < limit {
				seen[sym.ID] = true
				out = append(out, sym)
			}
		}
	}

	exact, err := s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol WHERE fqn = ? OR name = ? LIMIT ?`,
		query, query, limit,
	)
	if err != nil {
		return nil, err
	}

	add(exact)

	if len(out) < limit {
		suffix, err := s.querySymbols(
			`SELECT `+symbolCols+` FROM symbol WHERE fqn LIKE ? ESCAPE '\' LIMIT ?`,
			"%"+escapeLike(query), limit,
		)
		if err != nil {
			return nil, err
		}

		add(suffix)
	}

	if len(out) < limit {
		if match := ftsQuery(query); match != "" {
			// JOIN, not `IN (subquery)`: IN is a set filter, so it would
			// return rows in table-scan (rowid) order and discard the FTS
			// ranking. Bind the remaining capacity so the best-ranked
			// matches are the ones that survive the cut.
			fts, err := s.querySymbols(
				`SELECT `+symbolCols+` FROM symbol
				 JOIN (SELECT rowid, rank FROM symbol_fts
				       WHERE symbol_fts MATCH ? ORDER BY rank LIMIT ?) f
				   ON f.rowid = symbol.id
				 ORDER BY f.rank`,
				match, limit-len(out),
			)
			if err != nil {
				return nil, err
			}

			add(fts)
		}
	}

	return out, nil
}

// escapeLike escapes LIKE wildcards in user input.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

	return r.Replace(s)
}

// ftsQuery converts free-form user input into a safe FTS5 prefix query:
// each alphanumeric token is double-quoted and given a * suffix.
func ftsQuery(input string) string {
	tokens := strings.FieldsFunc(input, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	})

	if len(tokens) == 0 {
		return ""
	}

	quoted := make([]string, len(tokens))

	for i, t := range tokens {
		quoted[i] = `"` + t + `"*`
	}

	return strings.Join(quoted, " ")
}

// SymbolsInFile returns all symbols defined in a repo-relative file,
// in source order.
func (s *Store) SymbolsInFile(file string) ([]model.Symbol, error) {
	return s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol WHERE file = ? ORDER BY start_line`, file,
	)
}

// EdgesFrom returns symbols id has an outgoing edge of the given kind to.
func (s *Store) EdgesFrom(id int64, kind model.EdgeKind) ([]model.Symbol, error) {
	return s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol
		 WHERE id IN (SELECT dst FROM edge WHERE src = ? AND kind = ?)
		 ORDER BY fqn`, id, string(kind),
	)
}

// EdgesTo returns symbols with an edge of the given kind into id.
func (s *Store) EdgesTo(id int64, kind model.EdgeKind) ([]model.Symbol, error) {
	return s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol
		 WHERE id IN (SELECT src FROM edge WHERE dst = ? AND kind = ?)
		 ORDER BY fqn`, id, string(kind),
	)
}

// CallEdge is a call-graph neighbor together with the edge's declared
// derivation (model.Origin*), so surfaces can show confidence instead of
// presenting every edge as equally trustworthy.
type CallEdge struct {
	model.Symbol
	Origin string
}

const symbolColsPrefixed = `s.id, s.fqn, s.name, s.kind, s.file, s.start_line, s.start_col, s.end_line, s.end_col, s.sig, s.doc_hash`

func (s *Store) queryCallEdges(q string, id int64) ([]CallEdge, error) {
	rows, err := s.db.Query(q, id, string(model.EdgeCalls))
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []CallEdge

	for rows.Next() {
		var (
			c    CallEdge
			kind string
		)

		err := rows.Scan(&c.ID, &c.FQN, &c.Name, &kind, &c.File,
			&c.Span.StartLine, &c.Span.StartCol, &c.Span.EndLine, &c.Span.EndCol,
			&c.Sig, &c.DocHash, &c.Origin)
		if err != nil {
			return nil, err
		}

		c.Kind = model.SymbolKind(kind)
		out = append(out, c)
	}

	return out, rows.Err()
}

// Callers returns symbols with a CALLS edge into id, with edge origins.
func (s *Store) Callers(id int64) ([]CallEdge, error) {
	return s.queryCallEdges(
		`SELECT `+symbolColsPrefixed+`, e.origin
		 FROM edge e JOIN symbol s ON s.id = e.src
		 WHERE e.dst = ? AND e.kind = ?
		 ORDER BY s.fqn`, id)
}

// Callees returns symbols id has a CALLS edge to, with edge origins.
func (s *Store) Callees(id int64) ([]CallEdge, error) {
	return s.queryCallEdges(
		`SELECT `+symbolColsPrefixed+`, e.origin
		 FROM edge e JOIN symbol s ON s.id = e.dst
		 WHERE e.src = ? AND e.kind = ?
		 ORDER BY s.fqn`, id)
}

// CoChangePartner is a co-change row oriented around a queried file.
type CoChangePartner struct {
	File     string
	Together int
	Total    int
	Lift     float64
}

// CoChangePartners returns files that historically change together with
// file, strongest coupling first. minLift filters chance-level pairs;
// 1.0 is the neutral threshold.
func (s *Store) CoChangePartners(file string, minLift float64, limit int) ([]CoChangePartner, error) {
	if limit <= 0 {
		limit = 15
	}

	rows, err := s.db.Query(
		`SELECT CASE WHEN file_a = ? THEN file_b ELSE file_a END AS partner,
		        together, total, lift
		 FROM cochange
		 WHERE (file_a = ? OR file_b = ?) AND lift >= ?
		 ORDER BY together DESC, lift DESC
		 LIMIT ?`, file, file, file, minLift, limit,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()
	var out []CoChangePartner

	for rows.Next() {
		var p CoChangePartner

		if err := rows.Scan(&p.File, &p.Together, &p.Total, &p.Lift); err != nil {
			return nil, err
		}

		out = append(out, p)
	}

	return out, rows.Err()
}

// DecisionsForFile returns the most recent decisions touching a file.
// Bodies are included; callers decide how much to show.
func (s *Store) DecisionsForFile(file string, limit int) ([]model.Decision, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		`SELECT d.id, d.kind, d.ref, d.ts, d.author, d.title, d.body
		 FROM decision d
		 JOIN decision_file df ON df.decision_id = d.id
		 WHERE df.file = ?
		 ORDER BY d.ts DESC
		 LIMIT ?`, file, limit,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []model.Decision

	for rows.Next() {
		var d model.Decision
		var kind string

		if err := rows.Scan(&d.ID, &kind, &d.Ref, &d.TS, &d.Author, &d.Title, &d.Body); err != nil {
			return nil, err
		}

		d.Kind = model.DecisionKind(kind)
		out = append(out, d)
	}

	return out, rows.Err()
}

// Stats summarizes index contents for `seamark index` output.
type Stats struct {
	Symbols   int
	Edges     int
	CoChanges int
	Decisions int
}

// Stats returns row counts of the derived tables.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	for _, c := range []struct {
		table string
		dst   *int
	}{
		{"symbol", &st.Symbols},
		{"edge", &st.Edges},
		{"cochange", &st.CoChanges},
		{"decision", &st.Decisions},
	} {
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + c.table).Scan(c.dst); err != nil {
			return st, err
		}
	}

	return st, nil
}
