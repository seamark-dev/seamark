// Package store owns the on-disk index: a single SQLite file holding the
// structure, history, effects, and policy tables (RFC-001 §5.1).
//
// The file carries two classes of state with different lifecycles: derived
// tables (symbols, edges, co-change, decisions, effects) that Rebuild
// wipes and recomputes from the workspace, and durable tables (proposal,
// distilled, lesson, finding, rule) holding human decisions and paid
// inference that no rebuild can regenerate. The database is therefore NOT
// disposable; deleting it destroys decisions. Rebuilds preserve the
// durable tables and the schema is versioned (migrate.go). state.go makes
// the decision subset — proposals and distillation marks — portable;
// lessons, findings and rules are re-mined rather than exported.
//
// The store is deliberately dumb: it validates nothing about the graph and
// contains no language knowledge. Writers (the indexer, the history miner)
// batch rows inside Rebuild; readers get focused query helpers tuned for the
// CLI/LSP/MCP surfaces.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	stdpath "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// dsn builds a "file:" DSN. Paths are parsed as URIs: a raw %, ? or #
// in the workspace path would truncate it or swallow the params.
func dsn(path, params string) string {
	escaped := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23").Replace(path)

	return "file:" + escaped + "?" + params
}

// Open opens (creating if necessary) the index database at path and applies
// the schema. The schema is idempotent; opening an existing index is cheap.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create index dir: %w", err)
	}

	db, err := sql.Open("sqlite", dsn(path,
		"_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// The index has a single writer and local readers; one connection avoids
	// SQLITE_BUSY without a retry dance.
	db.SetMaxOpenConns(1)

	// Refuse a NEWER database before applying schema.sql or migrations:
	// re-executing this binary's DDL against a newer schema could
	// half-recreate structures the newer version removed. The check runs
	// first so the refusal's "left untouched" promise is actually true.
	if err := refuseNewer(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		_ = db.Close() // the schema error is the one worth reporting
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	// Version reconciliation runs on every open: new databases are
	// stamped, old ones upgrade in ordered steps, newer ones are refused
	// (see migrate.go).
	if err := ensureVersion(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// OpenReadOnly opens an EXISTING database for querying only: no schema
// application, no migrations, no writes — the opener for diagnostics
// that must leave the target byte-identical. The database must already
// carry this binary's schema version: an older one would fail queries
// on missing columns, a newer one must not be guessed at — both are
// refused with the same advice, run a current `seamark index` against
// it first.
func OpenReadOnly(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("store: open read-only: %w", err)
	}

	// mode=ro is enough for our WAL databases: SQLite reads the
	// -wal/-shm sidecars itself when they exist, and their absence just
	// means the last writer checkpointed cleanly. immutable=1 is
	// deliberately NOT set — another seamark may be writing this
	// database right now, and immutable would serve stale pages as
	// truth.
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(5000)"))
	if err != nil {
		return nil, fmt.Errorf("store: open %s read-only: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	stored, err := readVersion(db)
	if err != nil {
		_ = db.Close()

		// The first actual read: a dirty WAL on a read-only filesystem
		// surfaces here, and the bare driver error names no file.
		return nil, fmt.Errorf("store: read %s read-only: %w", path, err)
	}

	if stored != schemaVersion {
		_ = db.Close()

		return nil, fmt.Errorf("store: database schema is v%d but this seamark reads v%d — "+
			"run `seamark index` against it first (read-only open migrates nothing)",
			stored, schemaVersion)
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

	// Wipe in dependency order. Lessons are NOT wiped here: they are
	// mined from GitHub on a different cadence than local code changes
	// (see ReplaceLessons), so a structural reindex — including the MCP
	// self-repair on every tool call — must leave them intact.
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

// CacheEntry is one file's cached parse output.
type CacheEntry struct {
	Hash string
	Data []byte
}

// LoadParseCache returns the whole per-file parse cache. Callers version-
// check separately (via meta) and tolerate a decode failure per entry, so
// this simply hands back the stored blobs.
func (s *Store) LoadParseCache() (map[string]CacheEntry, error) {
	rows, err := s.db.Query(`SELECT file, hash, data FROM parse_cache`)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	out := map[string]CacheEntry{}

	for rows.Next() {
		var file string
		var e CacheEntry

		if err := rows.Scan(&file, &e.Hash, &e.Data); err != nil {
			return nil, err
		}

		out[file] = e
	}

	return out, rows.Err()
}

// UpsertParseCache stores one file's parse output.
func (t *Tx) UpsertParseCache(file, hash string, data []byte) error {
	_, err := t.tx.Exec(
		`INSERT INTO parse_cache (file, hash, data) VALUES (?, ?, ?)
		 ON CONFLICT (file) DO UPDATE SET hash = excluded.hash, data = excluded.data`,
		file, hash, data,
	)
	if err != nil {
		return fmt.Errorf("store: cache %s: %w", file, err)
	}

	return nil
}

// PruneParseCache drops cache rows for files no longer in the workspace,
// so a deleted file's stale parse output cannot linger.
func (t *Tx) PruneParseCache(keep map[string]bool) error {
	rows, err := t.tx.Query(`SELECT file FROM parse_cache`)
	if err != nil {
		return err
	}

	var stale []string

	for rows.Next() {
		var file string
		if err := rows.Scan(&file); err != nil {
			_ = rows.Close()
			return err
		}

		if !keep[file] {
			stale = append(stale, file)
		}
	}

	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}

	_ = rows.Close()

	for _, file := range stale {
		if _, err := t.tx.Exec(`DELETE FROM parse_cache WHERE file = ?`, file); err != nil {
			return err
		}
	}

	return nil
}

// InsertLesson stores one clustered review lesson. The cluster_key is
// unique; a repeated key accumulates occurrences and keeps the most
// recent example, so callers may insert per-cluster without pre-merging.
func (t *Tx) InsertLesson(l *model.Lesson) error {
	_, err := t.tx.Exec(
		`INSERT INTO lesson
		   (cluster_key, region, reviewer, symptom, fix, occurrences, last_ts, example_url)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (cluster_key) DO UPDATE SET
		   occurrences = lesson.occurrences + excluded.occurrences,
		   last_ts     = MAX(lesson.last_ts, excluded.last_ts),
		   reviewer    = excluded.reviewer,
		   example_url = CASE WHEN excluded.last_ts >= lesson.last_ts
		                 THEN excluded.example_url ELSE lesson.example_url END`,
		l.ClusterKey, l.Region, l.Reviewer, l.Symptom, l.Fix,
		l.Occurrences, l.LastTS, l.ExampleURL,
	)
	if err != nil {
		return fmt.Errorf("store: insert lesson %s: %w", l.ClusterKey, err)
	}

	return nil
}

// ReplaceLessons atomically swaps the lesson set — and the raw findings
// behind it — for a freshly mined one. Lessons are refreshed on the
// review-mining cadence, not the structural-reindex cadence, so this is
// their own transaction rather than part of Rebuild. Wiping both tables
// together keeps a finding's lesson_key from ever dangling.
func (s *Store) ReplaceLessons(lessons []model.Lesson, findings []model.Finding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin lessons: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	if _, err := tx.Exec("DELETE FROM lesson"); err != nil {
		return fmt.Errorf("store: wipe lesson: %w", err)
	}

	// Only the review provider's findings: fix findings live on the git
	// mining cadence and must survive a review re-mine.
	if _, err := tx.Exec("DELETE FROM finding WHERE source = ?", model.SourceReview); err != nil {
		return fmt.Errorf("store: wipe review findings: %w", err)
	}

	t := &Tx{tx: tx}

	for i := range lessons {
		if err := t.InsertLesson(&lessons[i]); err != nil {
			return err
		}
	}

	for i := range findings {
		if err := insertFinding(tx, &findings[i]); err != nil {
			return err
		}
	}

	// Stamp the mine time in the same transaction: the freshness signal
	// `seamark status` reports must never disagree with the data.
	_, err = tx.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		MetaReviewsMinedAt,
		fmt.Sprint(time.Now().Unix()),
	)
	if err != nil {
		return fmt.Errorf("store: stamp review mine: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit lessons: %w", err)
	}

	return nil
}

// ReplaceFixFindings atomically swaps the fix-derived findings — their
// own lifecycle, independent of the review set.
func (s *Store) ReplaceFixFindings(findings []model.Finding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin fix findings: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	sources := model.FixMinedSources()

	args := make([]any, len(sources))
	for i, s := range sources {
		args[i] = s
	}

	_, err = tx.Exec(
		"DELETE FROM finding WHERE source IN ("+strings.Repeat(",?", len(sources))[1:]+")", args...,
	)
	if err != nil {
		return fmt.Errorf("store: wipe fix findings: %w", err)
	}

	for i := range findings {
		if err := insertFinding(tx, &findings[i]); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit fix findings: %w", err)
	}

	return nil
}

// findingCols is the column list every finding query selects, in
// scanFindings order.
const findingCols = `id, lesson_key, path, pr, reviewer, body, url, created_at, source, paths`

// encodeStrings stores a string slice as JSON, ” for none — the
// column defaults pre-migration rows carry.
func encodeStrings(v []string) (string, error) {
	if len(v) == 0 {
		return "", nil
	}

	b, err := json.Marshal(v)

	return string(b), err
}

// decodeStrings is encodeStrings' inverse.
func decodeStrings(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}

	var out []string
	err := json.Unmarshal([]byte(s), &out)

	return out, err
}

func insertFinding(db execer, f *model.Finding) error {
	if f.Source == "" {
		f.Source = model.SourceReview
	}

	paths, err := encodeStrings(f.Paths)
	if err != nil {
		return fmt.Errorf("store: encode finding paths: %w", err)
	}

	_, err = db.Exec(
		`INSERT INTO finding (id, lesson_key, path, pr, reviewer, body, url, created_at, source, paths)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ID, f.LessonKey, f.Path, f.PR, f.Reviewer, f.Body, f.URL, f.CreatedAt, f.Source, paths,
	)
	if err != nil {
		return fmt.Errorf("store: insert finding %d: %w", f.ID, err)
	}

	return nil
}

// AllFindings returns every stored finding — the distiller's input.
func (s *Store) AllFindings() ([]model.Finding, error) {
	rows, err := s.db.Query(
		`SELECT ` + findingCols + ` FROM finding ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return scanFindings(rows)
}

// MarkDistilled records that a candidate group's evidence set has been
// read by the distiller, so the same signature is never paid for again.
func (s *Store) MarkDistilled(signature, region string, at int64) error {
	_, err := s.db.Exec(
		`INSERT INTO distilled (signature, region, at) VALUES (?, ?, ?)
		 ON CONFLICT (signature) DO UPDATE SET region = excluded.region, at = excluded.at`,
		signature, region, at,
	)
	if err != nil {
		return fmt.Errorf("store: mark distilled %s: %w", signature, err)
	}

	return nil
}

// DistilledSignatures returns the evidence sets already processed.
func (s *Store) DistilledSignatures() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT signature FROM distilled`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]bool{}

	for rows.Next() {
		var sig string

		if err := rows.Scan(&sig); err != nil {
			return nil, err
		}

		out[sig] = true
	}

	return out, rows.Err()
}

// proposalCols is the column list every proposal query selects, in
// scanProposals order.
const proposalCols = `id, signature, rule, region, regions, trigger_paths, trigger_checked_at, note, members, agent, status, created_at`

// InsertProposal stores one distilled proposal and returns its id.
func (s *Store) InsertProposal(p *model.Proposal) error {
	return insertProposal(s.db, p)
}

// execer is the slice of database/sql shared by *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertProposal(db execer, p *model.Proposal) error {
	members, err := json.Marshal(p.Members)
	if err != nil {
		return fmt.Errorf("store: encode proposal members: %w", err)
	}

	regions, err := encodeStrings(p.Regions)
	if err != nil {
		return fmt.Errorf("store: encode proposal regions: %w", err)
	}

	triggers, err := encodeStrings(p.TriggerPaths)
	if err != nil {
		return fmt.Errorf("store: encode proposal trigger paths: %w", err)
	}

	res, err := db.Exec(
		`INSERT INTO proposal (signature, rule, region, regions, trigger_paths, trigger_checked_at,
		   note, members, agent, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Signature, p.Rule, p.Region, regions, triggers, p.TriggerChecked,
		p.Note, string(members), p.Agent, p.Status, p.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("store: insert proposal %s: %w", p.Rule, err)
	}

	p.ID, err = res.LastInsertId()

	return err
}

// SaveDistilledGroup persists one distilled group atomically: its
// surviving proposals and its signature memory in a single transaction.
// The two must never diverge — proposals without the mark would be
// duplicated by the retry the unmarked signature invites; a mark
// without the proposals would silently discard the patterns a paid
// agent call found.
func (s *Store) SaveDistilledGroup(signature, region string, at int64, proposals []model.Proposal) ([]model.Proposal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("store: begin distilled group: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	for i := range proposals {
		if err := insertProposal(tx, &proposals[i]); err != nil {
			return nil, err
		}
	}

	_, err = tx.Exec(
		`INSERT INTO distilled (signature, region, at) VALUES (?, ?, ?)
		 ON CONFLICT (signature) DO UPDATE SET region = excluded.region, at = excluded.at`,
		signature, region, at,
	)
	if err != nil {
		return nil, fmt.Errorf("store: mark distilled %s: %w", signature, err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("store: commit distilled group: %w", err)
	}

	return proposals, nil
}

// Proposals returns proposals in one lifecycle state, newest first.
func (s *Store) Proposals(status string) ([]model.Proposal, error) {
	rows, err := s.db.Query(
		`SELECT `+proposalCols+` FROM proposal WHERE status = ? ORDER BY id DESC`, status,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return scanProposals(rows)
}

func scanProposals(rows *sql.Rows) ([]model.Proposal, error) {
	var out []model.Proposal

	for rows.Next() {
		var p model.Proposal

		var members, regions, triggers string

		err := rows.Scan(&p.ID, &p.Signature, &p.Rule, &p.Region, &regions,
			&triggers, &p.TriggerChecked, &p.Note, &members, &p.Agent, &p.Status, &p.CreatedAt)
		if err != nil {
			return nil, err
		}

		if err := json.Unmarshal([]byte(members), &p.Members); err != nil {
			return nil, fmt.Errorf("store: proposal %d members: %w", p.ID, err)
		}

		if p.Regions, err = decodeStrings(regions); err != nil {
			return nil, fmt.Errorf("store: proposal %d regions: %w", p.ID, err)
		}

		if p.TriggerPaths, err = decodeStrings(triggers); err != nil {
			return nil, fmt.Errorf("store: proposal %d trigger paths: %w", p.ID, err)
		}

		out = append(out, p)
	}

	return out, rows.Err()
}

// ProposalsByIDs returns the pending proposals with the given ids, in
// id order. Only 'proposed' rows qualify: applied and dismissed ones
// already carry a decision.
func (s *Store) ProposalsByIDs(ids []int64) ([]model.Proposal, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	marks := strings.Repeat(",?", len(ids))[1:]

	rows, err := s.db.Query(
		`SELECT `+proposalCols+` FROM proposal
		 WHERE status = 'proposed' AND id IN (`+marks+`) ORDER BY id`, args...,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return scanProposals(rows)
}

// SetProposalStatus moves pending proposals to a decided state and
// reports how many actually moved. Only 'proposed' rows transition:
// a decision, once made, is not silently overwritten.
func (s *Store) SetProposalStatus(ids []int64, to string) (int, error) {
	return s.transition(ids, model.ProposalProposed, to)
}

// SupersedeProposals retires applied proposals whose pin was pruned as
// a restatement of another. Only 'applied' rows move: superseding
// something never applied would be meaningless.
func (s *Store) SupersedeProposals(ids []int64) (int, error) {
	return s.transition(ids, model.ProposalApplied, model.ProposalSuperseded)
}

func (s *Store) transition(ids []int64, from, to string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	args := []any{to, from}
	for _, id := range ids {
		args = append(args, id)
	}

	marks := strings.Repeat(",?", len(ids))[1:]

	res, err := s.db.Exec(
		`UPDATE proposal SET status = ? WHERE status = ? AND id IN (`+marks+`)`, args...,
	)
	if err != nil {
		return 0, err
	}

	n, _ := res.RowsAffected()

	return int(n), nil
}

// PruneStaleProposals deletes pending proposals whose evidence group no
// longer exists in the current grouping (its membership changed, so a
// fresh distill of the new signature supersedes them). Applied and
// dismissed rows are never touched — they are the decision memory.
func (s *Store) PruneStaleProposals(liveSignatures map[string]bool) (int, error) {
	rows, err := s.db.Query(`SELECT DISTINCT signature FROM proposal WHERE status = 'proposed'`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var stale []any

	for rows.Next() {
		var sig string

		if err := rows.Scan(&sig); err != nil {
			return 0, err
		}

		if !liveSignatures[sig] {
			stale = append(stale, sig)
		}
	}

	if err := rows.Err(); err != nil {
		return 0, err
	}

	if len(stale) == 0 {
		return 0, nil
	}

	marks := strings.Repeat(",?", len(stale))[1:]

	res, err := s.db.Exec(
		`DELETE FROM proposal WHERE status = 'proposed' AND signature IN (`+marks+`)`, stale...,
	)
	if err != nil {
		return 0, err
	}

	n, _ := res.RowsAffected()

	return int(n), nil
}

// FindingsForLesson returns the raw comments behind one lesson, oldest
// first — the evidence trail a distiller or a provenance view reads.
func (s *Store) FindingsForLesson(clusterKey string) ([]model.Finding, error) {
	rows, err := s.db.Query(
		`SELECT `+findingCols+` FROM finding
		 WHERE lesson_key = ? ORDER BY created_at, id`, clusterKey,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	return scanFindings(rows)
}

// ClusterCited counts one mined cluster's findings: how many are among
// a given citation set, and how many exist in total.
type ClusterCited struct {
	Cited, Total int
}

// ClusterCitation reports, for every mined-lesson cluster the given
// finding ids touch, how much of the cluster those ids cover. The
// caller decides what coverage means — this method only counts.
// Duplicate ids are deduplicated first, so overlapping citation sets
// cannot inflate a count past the cluster's size.
func (s *Store) ClusterCitation(ids []int64) (map[string]ClusterCited, error) {
	seen := make(map[int64]bool, len(ids))
	unique := make([]int64, 0, len(ids))

	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}

	out := map[string]ClusterCited{}

	// Chunked IN lists: cited ids number in the hundreds, and SQLite's
	// parameter limit must never be the thing that breaks a surface.
	err := s.countByKey(`SELECT lesson_key, COUNT(*) FROM finding
		 WHERE lesson_key != '' AND id IN (%s) GROUP BY lesson_key`,
		int64Args(unique), func(key string, n int) {
			c := out[key]
			c.Cited += n
			out[key] = c
		})
	if err != nil {
		return nil, err
	}

	keys := make([]any, 0, len(out))
	for key := range out {
		keys = append(keys, key)
	}

	err = s.countByKey(`SELECT lesson_key, COUNT(*) FROM finding
		 WHERE lesson_key IN (%s) GROUP BY lesson_key`,
		keys, func(key string, n int) {
			c := out[key]
			c.Total += n
			out[key] = c
		})
	if err != nil {
		return nil, err
	}

	return out, nil
}

// UpdateProposalTriggers stores one proposal's validated trigger
// paths and stamps when the question was answered — an empty answer
// is an answer, and must not be re-purchased. Regions are written
// separately: pending rows retarget in the same extraction run,
// applied pins only through the explicit --retarget — an installed
// pin's delivery never changes without the user.
func (s *Store) UpdateProposalTriggers(id int64, paths []string, checkedAt int64) error {
	triggers, err := encodeStrings(paths)
	if err != nil {
		return fmt.Errorf("store: encode proposal trigger paths: %w", err)
	}

	if _, err := s.db.Exec(`UPDATE proposal SET trigger_paths = ?, trigger_checked_at = ? WHERE id = ?`,
		triggers, checkedAt, id); err != nil {
		return fmt.Errorf("store: update proposal %d trigger paths: %w", id, err)
	}

	return nil
}

// UpdateProposalRegionsIfPending rewrites one proposal's regions only
// while it is still 'proposed', atomically. Extraction re-checks the
// status at write time: a proposal applied by a concurrent command
// during the agent call must keep the identity its yaml pin was
// installed with, or liveness, pruning, and outcomes all reason from
// a split identity.
func (s *Store) UpdateProposalRegionsIfPending(p model.Proposal) (bool, error) {
	encoded, err := encodeStrings(p.Regions)
	if err != nil {
		return false, fmt.Errorf("store: encode proposal regions: %w", err)
	}

	res, err := s.db.Exec(
		`UPDATE proposal SET region = ?, regions = ? WHERE id = ? AND status = ?`,
		p.Region, encoded, p.ID, model.ProposalProposed,
	)
	if err != nil {
		return false, fmt.Errorf("store: update proposal %d regions: %w", p.ID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}

	return n > 0, nil
}

// UpdateProposalRegionsBatch rewrites the region sets of the given
// proposals in ONE transaction — the ledger half of retarget is
// all-or-nothing, so a mid-batch failure can never leave some pins
// retargeted in the database and others not while the file already
// carries every new identity.
func (s *Store) UpdateProposalRegionsBatch(ps []model.Proposal) error {
	if len(ps) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin region update: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	for _, p := range ps {
		encoded, err := encodeStrings(p.Regions)
		if err != nil {
			return fmt.Errorf("store: encode proposal regions: %w", err)
		}

		if _, err := tx.Exec(`UPDATE proposal SET region = ?, regions = ? WHERE id = ?`,
			p.Region, encoded, p.ID); err != nil {
			return fmt.Errorf("store: update proposal %d regions: %w", p.ID, err)
		}
	}

	return tx.Commit()
}

// FindingsMetaByIDs returns the cited findings' metadata — everything
// confidence assessment needs (path, pr, source, timestamps) WITHOUT
// the bodies, so ambient surfaces can assess pins on every edit
// without hauling the corpus into memory. Duplicate ids are fine.
func (s *Store) FindingsMetaByIDs(ids []int64) (map[int64]model.Finding, error) {
	seen := make(map[int64]bool, len(ids))
	unique := make([]any, 0, len(ids))

	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}

	out := make(map[int64]model.Finding, len(unique))

	err := s.chunked(
		`SELECT id, path, pr, created_at, source, paths FROM finding
		 WHERE id IN (%s)`, unique,
		func(rows *sql.Rows) error {
			var f model.Finding

			var paths string

			if err := rows.Scan(&f.ID, &f.Path, &f.PR, &f.CreatedAt, &f.Source, &paths); err != nil {
				return err
			}

			decoded, err := decodeStrings(paths)
			if err != nil {
				return fmt.Errorf("store: finding %d paths: %w", f.ID, err)
			}

			f.Paths = decoded
			out[f.ID] = f

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return out, nil
}

// chunked runs an IN-list query against args in slices of 500 — SQLite
// bounds bound-parameter counts, and one giant list would fail exactly
// on the large repositories that need the query most. query must be a
// constant SQL template supplied by an internal caller, never caller
// input, with its single %s reserved for the generated placeholder
// list. scan owns all column reading for one row; iteration errors and
// Close bookkeeping stay here.
func (s *Store) chunked(query string, args []any, scan func(*sql.Rows) error) error {
	for len(args) > 0 {
		chunk := args
		if len(chunk) > 500 {
			chunk = chunk[:500]
		}

		args = args[len(chunk):]

		marks := strings.Repeat(",?", len(chunk))[1:]

		rows, err := s.db.Query(fmt.Sprintf(query, marks), chunk...)
		if err != nil {
			return err
		}

		for rows.Next() {
			if err := scan(rows); err != nil {
				_ = rows.Close()

				return err
			}
		}

		if err := rows.Err(); err != nil {
			_ = rows.Close()

			return err
		}

		_ = rows.Close()
	}

	return nil
}

// countByKey runs a two-column (key, count) GROUP BY query in chunks of
// the given args, feeding each row to add — the same %s template
// contract as chunked.
func (s *Store) countByKey(query string, args []any, add func(key string, n int)) error {
	return s.chunked(query, args, func(rows *sql.Rows) error {
		var key string

		var n int

		if err := rows.Scan(&key, &n); err != nil {
			return err
		}

		add(key, n)

		return nil
	})
}

// int64Args widens ids for the driver.
func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}

	return out
}

func scanFindings(rows *sql.Rows) ([]model.Finding, error) {
	var out []model.Finding

	for rows.Next() {
		var f model.Finding

		var paths string

		err := rows.Scan(&f.ID, &f.LessonKey, &f.Path, &f.PR,
			&f.Reviewer, &f.Body, &f.URL, &f.CreatedAt, &f.Source, &paths)
		if err != nil {
			return nil, err
		}

		if f.Paths, err = decodeStrings(paths); err != nil {
			return nil, fmt.Errorf("store: finding %d paths: %w", f.ID, err)
		}

		out = append(out, f)
	}

	return out, rows.Err()
}

// InsertEffect stores one effect tag on a symbol. origin is "direct" for
// catalogue hits, "propagated" for tags inherited along CALLS edges.
func (t *Tx) InsertEffect(symbolID int64, tag, origin string, depth int) error {
	_, err := t.tx.Exec(
		`INSERT INTO effect (symbol_id, tag, origin, depth) VALUES (?, ?, ?, ?)
		 ON CONFLICT (symbol_id, tag) DO UPDATE SET
		   origin = excluded.origin, depth = excluded.depth
		 WHERE excluded.depth < effect.depth`,
		symbolID, tag, origin, depth,
	)
	if err != nil {
		return fmt.Errorf("store: insert effect %s on %d: %w", tag, symbolID, err)
	}

	return nil
}

// Effect is one tag carried by a symbol.
type Effect struct {
	Tag    string
	Origin string // direct | propagated
	Depth  int    // 0 for direct
}

// EffectsForSymbol returns a symbol's tags, direct first, then by depth.
func (s *Store) EffectsForSymbol(id int64) ([]Effect, error) {
	rows, err := s.db.Query(
		`SELECT tag, origin, depth FROM effect WHERE symbol_id = ?
		 ORDER BY depth, tag`, id,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []Effect

	for rows.Next() {
		var e Effect
		if err := rows.Scan(&e.Tag, &e.Origin, &e.Depth); err != nil {
			return nil, err
		}

		out = append(out, e)
	}

	return out, rows.Err()
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

// SymbolsByName returns every symbol with exactly this name.
func (s *Store) SymbolsByName(name string) ([]model.Symbol, error) {
	return s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol WHERE name = ? ORDER BY fqn`, name,
	)
}

// SymbolAt returns the innermost symbol whose span covers a 1-based line
// of file, or nil when the line is outside every declaration.
func (s *Store) SymbolAt(file string, line uint32) (*model.Symbol, error) {
	syms, err := s.querySymbols(
		`SELECT `+symbolCols+` FROM symbol
		 WHERE file = ? AND start_line <= ? AND end_line >= ?
		 ORDER BY (end_line - start_line), start_line DESC
		 LIMIT 1`, file, line, line,
	)
	if err != nil || len(syms) == 0 {
		return nil, err
	}

	return &syms[0], nil
}

// CallerCounts returns the CALLS in-degree of every symbol defined in
// file, for surfaces that annotate whole files at once (code lenses).
func (s *Store) CallerCounts(file string) (map[int64]int, error) {
	rows, err := s.db.Query(
		`SELECT e.dst, COUNT(*)
		 FROM edge e JOIN symbol s ON s.id = e.dst
		 WHERE s.file = ? AND e.kind = ?
		 GROUP BY e.dst`, file, string(model.EdgeCalls),
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	out := map[int64]int{}

	for rows.Next() {
		var (
			id int64
			n  int
		)

		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}

		out[id] = n
	}

	return out, rows.Err()
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
		 ORDER BY s.fqn`, id,
	)
}

// Callees returns symbols id has a CALLS edge to, with edge origins.
func (s *Store) Callees(id int64) ([]CallEdge, error) {
	return s.queryCallEdges(
		`SELECT `+symbolColsPrefixed+`, e.origin
		 FROM edge e JOIN symbol s ON s.id = e.dst
		 WHERE e.src = ? AND e.kind = ?
		 ORDER BY s.fqn`, id,
	)
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

// CountCommitsTouching counts the distinct commits and reverts whose
// files fall inside any of the given regions within [since, until).
// The outcome loop uses this as its activity denominator: a region
// with no commits since exposure cannot prove a pin works.
//
// A region matches as an exact file or as a directory prefix. Empty
// regions means repo-wide. Times are unix seconds like decision.ts;
// until <= 0 means no upper bound. Only commits and reverts count:
// pr/adr rows carry no file activity of their own, and counting them
// would double-count commits once a PR provider exists.
func (s *Store) CountCommitsTouching(regions []string, since, until int64) (int, error) {
	if until <= 0 {
		until = math.MaxInt64
	}

	// EXISTS filters out decisions with no file rows (merge and empty
	// commits): they are not code activity, and in a PR-merge workflow
	// counting them would roughly double every repo-wide figure.
	q := `SELECT COUNT(*) FROM decision d
	WHERE d.kind IN(?, ?) AND d.ts >= ? AND d.ts < ?
	AND EXISTS (SELECT 1 FROM decision_file df WHERE df.decision_id = d.id)`

	args := []any{string(model.DecisionCommit), string(model.DecisionRevert), since, until}

	if len(regions) > 0 {
		// DISTINCT: a commit touching several region files, or two
		// regions of the same pin, is one commit.
		q = `SELECT COUNT(DISTINCT d.id)
		     FROM decision d
		     JOIN decision_file df ON df.decision_id = d.id
		     WHERE d.kind IN (?, ?) AND d.ts >= ? AND d.ts < ?`

		var clauses []string

		for _, r := range regions {
			clauses = append(clauses, `(df.file = ? OR df.file LIKE ? ESCAPE '\')`)
			args = append(args, r, escapeLike(r)+"/%")
		}

		q += ` AND (` + strings.Join(clauses, " OR ") + `)`
	}

	var count int
	err := s.db.QueryRow(q, args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: count commits touching %v: %w", regions, err)
	}

	return count, nil
}

// Meta keys recording when each finding channel was last mined (unix
// seconds, written with the corresponding finding refresh). All readers and writers
// must use these constants: a misspelled key does not error, it
// silently reads as "never mined".
const (
	MetaFixesMinedAt   = "fixes_mined_at"
	MetaReviewsMinedAt = "reviews_mined_at"
)

// EvidenceHorizon returns the timestamp the finding corpus is known
// current through: the older of the two mining stamps, because a
// recurrence can arrive through either channel. Zero when either
// stamp is missing — a store that cannot prove it looked cannot claim
// nothing was found, so verdicts stay untested until
// `seamark index --reviews` runs once.
func (s *Store) EvidenceHorizon() int64 {
	var horizon int64

	for i, key := range []string{MetaFixesMinedAt, MetaReviewsMinedAt} {
		v, err := s.GetMeta(key)
		if err != nil {
			return 0
		}

		ts, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0
		}

		if i == 0 || ts < horizon {
			horizon = ts
		}
	}

	return horizon
}

// RecentDecisions returns the latest repo-wide decisions, newest first.
func (s *Store) RecentDecisions(limit int) ([]model.Decision, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		`SELECT id, kind, ref, ts, author, title, body
		 FROM decision ORDER BY ts DESC LIMIT ?`, limit,
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

// HotFile is a co-change hub: a file whose changes rarely travel alone.
type HotFile struct {
	File     string
	Partners int
	MaxLift  float64
}

// HotFiles returns the files most coupled to the rest of the repo by
// history. Pairs are stored once per (a, b), so both columns count.
func (s *Store) HotFiles(limit int) ([]HotFile, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		`SELECT f, COUNT(*) AS partners, MAX(lift)
		 FROM (SELECT file_a AS f, lift FROM cochange WHERE lift >= 1.0
		       UNION ALL
		       SELECT file_b AS f, lift FROM cochange WHERE lift >= 1.0)
		 GROUP BY f ORDER BY partners DESC, f LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []HotFile

	for rows.Next() {
		var h HotFile

		if err := rows.Scan(&h.File, &h.Partners, &h.MaxLift); err != nil {
			return nil, err
		}

		out = append(out, h)
	}

	return out, rows.Err()
}

// FileChurn is how much of the repo's history a file has absorbed: the
// number of decisions (commits, PRs, reverts) recorded against it.
type FileChurn struct {
	File    string
	Commits int
}

// FileChurn returns the most-touched files, busiest first. Where
// HotFiles ranks by coupling ("this rarely changes alone"), this ranks
// by sheer activity — the area metric behind the report's hotspot map,
// and the candidate set a fix-density pass then colours.
// A non-positive limit returns every file, as AllLessons does.
func (s *Store) FileChurn(limit int) ([]FileChurn, error) {
	q := `SELECT file, COUNT(*) AS commits FROM decision_file
	      GROUP BY file ORDER BY commits DESC, file`

	args := []any{}

	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []FileChurn

	for rows.Next() {
		var f FileChurn

		if err := rows.Scan(&f.File, &f.Commits); err != nil {
			return nil, err
		}

		out = append(out, f)
	}

	return out, rows.Err()
}

// CalledSymbol is a symbol with its caller count.
type CalledSymbol struct {
	model.Symbol
	Callers int
}

// TopCalled returns the most-called symbols — the load-bearing API
// surface a newcomer (human or agent) should read first.
func (s *Store) TopCalled(limit int) ([]CalledSymbol, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		`SELECT `+symbolColsPrefixed+`, COUNT(*) AS callers
		 FROM edge e JOIN symbol s ON s.id = e.dst
		 WHERE e.kind = ?
		 GROUP BY e.dst ORDER BY callers DESC, s.fqn LIMIT ?`,
		string(model.EdgeCalls), limit,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []CalledSymbol

	for rows.Next() {
		var c CalledSymbol
		var kind string

		if err := rows.Scan(&c.ID, &c.FQN, &c.Name, &kind, &c.File,
			&c.Span.StartLine, &c.Span.StartCol, &c.Span.EndLine, &c.Span.EndCol,
			&c.Sig, &c.DocHash, &c.Callers); err != nil {
			return nil, err
		}

		c.Kind = model.SymbolKind(kind)
		out = append(out, c)
	}

	return out, rows.Err()
}

// FileSymbolCounts returns per-file symbol counts, for module summaries.
func (s *Store) FileSymbolCounts() (map[string]int, error) {
	rows, err := s.db.Query(
		`SELECT file, COUNT(*) FROM symbol WHERE file != '' GROUP BY file`,
	)
	if err != nil {
		return nil, err
	}

	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	out := map[string]int{}

	for rows.Next() {
		var file string
		var n int

		if err := rows.Scan(&file, &n); err != nil {
			return nil, err
		}

		out[file] = n
	}

	return out, rows.Err()
}

// scanLessons reads model.Lesson rows from a query selecting the lesson
// columns in a fixed order.
func scanLessons(rows *sql.Rows) ([]model.Lesson, error) {
	// Close error deliberately dropped: iteration failures surface via rows.Err().
	defer func() { _ = rows.Close() }()

	var out []model.Lesson

	for rows.Next() {
		var l model.Lesson

		if err := rows.Scan(&l.ID, &l.ClusterKey, &l.Region, &l.Reviewer,
			&l.Symptom, &l.Fix, &l.Occurrences, &l.LastTS, &l.ExampleURL); err != nil {
			return nil, err
		}

		out = append(out, l)
	}

	return out, rows.Err()
}

const lessonCols = `id, cluster_key, region, reviewer, symptom, fix, occurrences, last_ts, example_url`

// LessonsForFile returns recurring review feedback whose region is the
// file itself or the directory it lives in, strongest first. minOccur
// filters one-off comments — a lesson is a pattern, not a single note.
func (s *Store) LessonsForFile(file string, minOccur, limit int) ([]model.Lesson, error) {
	if limit <= 0 {
		limit = 10
	}

	// Match the file itself and every ancestor directory: rule-code
	// clustering scopes lessons to the file's package, and cross-file
	// merging can widen a region further up the tree. Enumerating the
	// ancestors keeps the match exact (a LIKE prefix would treat the
	// `_` in ordinary filenames as a wildcard) and on the region index.
	args := []any{file}
	for dir := stdpath.Dir(file); dir != "." && dir != "/"; dir = stdpath.Dir(dir) {
		args = append(args, dir)
	}

	marks := strings.Repeat(",?", len(args))[1:]
	args = append(args, minOccur, limit)

	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lesson
		 WHERE region IN (`+marks+`) AND occurrences >= ?
		 ORDER BY occurrences DESC, last_ts DESC
		 LIMIT ?`, args...,
	)
	if err != nil {
		return nil, err
	}

	return scanLessons(rows)
}

// AllLessons returns every mined lesson repo-wide, strongest first —
// including the one-offs that TopLessons filters out. It is the ledger
// `seamark lessons --list` shows so a user can decide what to mute or
// pin. limit <= 0 means no cap.
func (s *Store) AllLessons(limit int) ([]model.Lesson, error) {
	q := `SELECT ` + lessonCols + ` FROM lesson
	      ORDER BY occurrences DESC, last_ts DESC, region`

	args := []any{}

	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}

	return scanLessons(rows)
}

// TopLessons returns the strongest recurring review patterns repo-wide.
func (s *Store) TopLessons(minOccur, limit int) ([]model.Lesson, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := s.db.Query(
		`SELECT `+lessonCols+` FROM lesson
		 WHERE occurrences >= ?
		 ORDER BY occurrences DESC, last_ts DESC
		 LIMIT ?`, minOccur, limit,
	)
	if err != nil {
		return nil, err
	}

	return scanLessons(rows)
}

// Stats summarizes index contents for `seamark index` output.
type Stats struct {
	Symbols   int
	Edges     int
	CoChanges int
	Decisions int
	// Tagged counts symbols carrying at least one effect tag.
	Tagged int
	// Lessons counts clustered review-feedback patterns (M6).
	Lessons int
}

// Stats returns row counts of the derived tables.
func (s *Store) Stats() (Stats, error) {
	var st Stats
	for _, c := range []struct {
		query string
		dst   *int
	}{
		{"SELECT COUNT(*) FROM symbol", &st.Symbols},
		{"SELECT COUNT(*) FROM edge", &st.Edges},
		{"SELECT COUNT(*) FROM cochange", &st.CoChanges},
		{"SELECT COUNT(*) FROM decision", &st.Decisions},
		{"SELECT COUNT(DISTINCT symbol_id) FROM effect", &st.Tagged},
		{"SELECT COUNT(*) FROM lesson", &st.Lessons},
	} {
		if err := s.db.QueryRow(c.query).Scan(c.dst); err != nil {
			return st, err
		}
	}

	return st, nil
}
