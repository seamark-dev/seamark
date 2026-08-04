package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

// schemaVersion is the schema this binary understands and writes. Bump it
// together with a new migrations entry — never alone: a version without a
// migration would leave every existing database behind.
const schemaVersion = 3

// schemaVersionKey is the meta row recording a database's version.
const schemaVersionKey = "schema_version"

// migration is one ordered upgrade step: run brings a database from any
// version below `to` up to `to`.
type migration struct {
	to  int
	run func(tx *sql.Tx) error
}

// migrations, in ascending `to` order. Rules:
//
//   - Steps must stay idempotent (guard on the live schema) while
//     pre-versioning databases exist in the wild: those carry v2 columns
//     but no version stamp.
//   - Every step so far is additive. A destructive step (dropping or
//     rewriting the durable tables: proposal, distilled, rule) must first
//     copy the database file aside — that machinery gets built with the
//     first such step, not before.
var migrations = []migration{
	{to: 2, run: addFindingSource},
	{to: 3, run: addRegionSetsAndPaths},
}

// addFindingSource (v1 → v2): finding.source arrived with fix mining —
// providers beyond review comments. Every pre-existing finding came from
// review mining.
func addFindingSource(tx *sql.Tx) error {
	has, err := hasColumn(tx, "finding", "source")
	if err != nil {
		return err
	}

	if !has {
		_, err = tx.Exec(`ALTER TABLE finding ADD COLUMN source TEXT NOT NULL DEFAULT 'review'`)
	}

	return err
}

// addRegionSetsAndPaths (v2 → v3): proposals gained evidence-coverage
// region sets and fix findings their full code footprint (RFC-002
// Phase B). Empty-string defaults mean "derive from the single-value
// column", so pre-existing rows need no rewrite.
func addRegionSetsAndPaths(tx *sql.Tx) error {
	for _, col := range []struct{ table, name, ddl string }{
		{"proposal", "regions", `ALTER TABLE proposal ADD COLUMN regions TEXT NOT NULL DEFAULT ''`},
		{"finding", "paths", `ALTER TABLE finding ADD COLUMN paths TEXT NOT NULL DEFAULT ''`},
	} {
		has, err := hasColumn(tx, col.table, col.name)
		if err != nil {
			return err
		}

		if !has {
			if _, err := tx.Exec(col.ddl); err != nil {
				return err
			}
		}
	}

	return nil
}

// refuseNewer rejects a database stamped by a newer seamark BEFORE any
// DDL has run — guessing at unknown schema would risk the durable
// decisions it holds. A missing meta table means fresh or pre-versioning;
// both proceed.
func refuseNewer(db *sql.DB) error {
	var n int

	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return err
	}

	if n == 0 {
		return nil
	}

	stored, err := readVersion(db)
	if err != nil {
		return err
	}

	return checkNotNewer(stored)
}

// checkNotNewer is the shared newer-database refusal.
func checkNotNewer(stored int) error {
	if stored > schemaVersion {
		return fmt.Errorf(
			"store: database schema is v%d but this seamark understands v%d — "+
				"upgrade seamark (the database was left untouched; do not delete it: "+
				"it holds your proposal decisions)", stored, schemaVersion)
	}

	return nil
}

// ensureVersion reconciles a freshly-opened database with this binary's
// schema version: stamps new databases and upgrades old ones step by
// step. Newer databases were already refused by refuseNewer before any
// DDL ran; the re-check here is a backstop.
func ensureVersion(db *sql.DB) error {
	stored, err := readVersion(db)
	if err != nil {
		return err
	}

	switch {
	case stored == schemaVersion:
		return nil
	case stored > schemaVersion:
		return checkNotNewer(stored)
	}

	// Each step commits together with its version stamp: an interrupted
	// upgrade resumes at exactly the step it stopped on.
	for _, m := range migrations {
		if m.to <= stored {
			continue
		}

		tx, err := db.Begin()
		if err != nil {
			return err
		}

		if err := m.run(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migrate to v%d: %w", m.to, err)
		}

		if err := writeVersion(tx, m.to); err != nil {
			_ = tx.Rollback()
			return err
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration to v%d: %w", m.to, err)
		}
	}

	// Fresh databases got the full current schema from schema.sql and may
	// have no steps to run — stamp them (and any migration-list gap)
	// unconditionally.
	_, err = db.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersionKey, strconv.Itoa(schemaVersion))

	return err
}

// readVersion returns the stamped schema version, 0 when the database
// predates versioning (or is brand new).
func readVersion(db *sql.DB) (int, error) {
	var v string

	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("store: unreadable schema version %q: %w", v, err)
	}

	return n, nil
}

// writeVersion stamps a version inside a migration's transaction.
func writeVersion(tx *sql.Tx, v int) error {
	_, err := tx.Exec(
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersionKey, strconv.Itoa(v))

	return err
}

// querier is the query slice of database/sql shared by *sql.DB and
// *sql.Tx.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// hasColumn inspects the live schema — migrations guard on it so they
// stay idempotent across pre-versioning databases.
func hasColumn(q querier, table, column string) (bool, error) {
	rows, err := q.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string

		if err := rows.Scan(&name); err != nil {
			return false, err
		}

		if name == column {
			return true, nil
		}
	}

	return false, rows.Err()
}
