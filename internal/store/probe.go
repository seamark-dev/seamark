package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

// Read-only diagnostics for `seamark doctor`. Unlike Open, nothing here
// applies schema, migrates, or stamps: a doctor must diagnose a
// database, never mutate it.

// SupportedSchema is the schema version this binary reads and writes.
func SupportedSchema() int { return schemaVersion }

// ProbeVersion reports an existing database's stamped schema version
// without touching it. 0 means unstamped (a pre-versioning database);
// an unopenable or unreadable file is an error.
func ProbeVersion(path string) (int, error) {
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(2000)"))
	if err != nil {
		return 0, err
	}
	defer func() { _ = db.Close() }()

	var n int

	err = db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return 0, err
	}

	if n == 0 {
		return 0, nil
	}

	var v string

	err = db.QueryRow(`SELECT value FROM meta WHERE key = ?`, schemaVersionKey).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}

	if err != nil {
		return 0, err
	}

	version, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("store: unreadable schema version %q: %w", v, err)
	}

	return version, nil
}

// Integrity runs SQLite's integrity check read-only and returns its
// verdict lines ("ok" when healthy).
func Integrity(path string) (string, error) {
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(2000)"))
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`PRAGMA integrity_check`)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()

	out := ""

	for rows.Next() {
		var line string

		if err := rows.Scan(&line); err != nil {
			return "", err
		}

		if out != "" {
			out += "; "
		}

		out += line
	}

	return out, rows.Err()
}
