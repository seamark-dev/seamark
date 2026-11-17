package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
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
	v, found, err := probeMeta(path, MetaSchemaVersion)
	if err != nil || !found {
		return 0, err
	}

	version, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("store: unreadable schema version %q: %w", v, err)
	}

	return version, nil
}

// ProbeRoot reports the repository root that an existing database
// records, without touching it. It reads every schema version, because
// the root of an older or a newer database still names its owner. ""
// means that the database records no root.
func ProbeRoot(path string) (string, error) {
	root, _, err := probeMeta(path, MetaRepoRoot)

	return root, err
}

// IsReadOnlyDirectory reports a SQLite failure because the directory of a
// WAL database refuses writes. SQLite must create the -wal and -shm files
// beside the database, also to read it, so no read can work in that state.
// The store package owns the driver, so the code test stays here.
func IsReadOnlyDirectory(err error) bool {
	var sqlErr *sqlite.Error

	return errors.As(err, &sqlErr) && sqlErr.Code() == sqlite3.SQLITE_READONLY_DIRECTORY
}

// probeMeta reads one meta value without schema or migration. found is
// false when the database has no meta table or no such key.
func probeMeta(path, key string) (value string, found bool, err error) {
	db, err := sql.Open("sqlite", dsn(path, "mode=ro&_pragma=busy_timeout(2000)"))
	if err != nil {
		return "", false, err
	}
	defer func() { _ = db.Close() }()

	var n int

	err = db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'meta'`).Scan(&n)
	if err != nil {
		return "", false, err
	}

	if n == 0 {
		return "", false, nil
	}

	err = db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}

	if err != nil {
		return "", false, err
	}

	return value, true, nil
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
