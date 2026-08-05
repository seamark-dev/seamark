package store

import (
	"database/sql"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/seamark-dev/seamark/internal/model"
)

// rawOpen opens the database file directly, bypassing Open's version
// reconciliation — for crafting fixture states no supported path writes.
func rawOpen(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()

	return sql.Open("sqlite", "file:"+path)
}

func TestOpenStampsSchemaVersion(t *testing.T) {
	s := openTestStore(t)

	v, err := s.GetMeta(schemaVersionKey)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v, "a fresh database is stamped with the current version")
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.SetMeta(schemaVersionKey, strconv.Itoa(schemaVersion+7)))
	require.NoError(t, s.Close())

	// Simulate the newer schema having removed a table this binary's
	// schema.sql would recreate: the refusal must fire BEFORE any DDL, or
	// "left untouched" is a lie.
	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`DROP TABLE effect`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Open(path)
	require.Error(t, err, "an older binary must refuse a newer database")
	assert.Contains(t, err.Error(), "upgrade seamark")
	assert.Contains(t, err.Error(), "do not delete", "the message must protect the durable decisions")

	db, err = rawOpen(t, path)
	require.NoError(t, err)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'effect'`).Scan(&n))
	assert.Zero(t, n, "the refused open must not have re-applied this binary's schema")
	require.NoError(t, db.Close())

	// The refusal left the database intact: a newer seamark still opens it
	// (simulated by resetting the stamp out of band).
	db, err = rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE meta SET value = ? WHERE key = ?`,
		strconv.Itoa(schemaVersion), schemaVersionKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())
}

func TestOpenUpgradesVersionOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// Rewind a current database to v1: drop the migrated column and the
	// version stamp — exactly what a database written before fix mining
	// looks like.
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE finding DROP COLUMN source`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM meta WHERE key = ?`, schemaVersionKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Reopening runs the ordered migrations and stamps the version.
	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(schemaVersionKey)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v)

	// The migrated column exists and defaults historical rows to review.
	require.NoError(t, s.ReplaceLessons(nil, []model.Finding{
		{ID: 1, LessonKey: "k", Path: "a.go", Body: "b", Source: model.SourceReview},
	}))

	findings, err := s.AllFindings()
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, model.SourceReview, findings[0].Source)
}

func TestOpenStampsPreVersioningDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// A database migrated by the pre-versioning code: the column exists,
	// but no stamp was ever written. The guarded migration is a no-op and
	// the stamp lands.
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM meta WHERE key = ?`, schemaVersionKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(schemaVersionKey)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v)
}

func TestMigrationsAreOrdered(t *testing.T) {
	last := 1

	for _, m := range migrations {
		assert.Greater(t, m.to, last, "migrations must ascend without duplicates")
		last = m.to
	}

	assert.Equal(t, schemaVersion, last,
		"the last migration must land on the current version — a version bump without a migration strands existing databases")
}

func TestOpenUpgradesVersionTwoDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// Rewind a current database to v2: drop the v3 columns and stamp
	// the old version — a database written before region sets.
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.Close())

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE proposal DROP COLUMN regions`)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE finding DROP COLUMN paths`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE meta SET value = '2' WHERE key = ?`, schemaVersionKey)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(schemaVersionKey)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v)

	// Both migrated columns round-trip, and pre-set rows ('' defaults)
	// read back as empty sets rather than errors.
	p := model.Proposal{Signature: "sig", Rule: "r", Region: "api",
		Regions: []string{"api", "db"}, Note: "n", Members: []int64{1},
		Status: model.ProposalProposed}
	require.NoError(t, s.InsertProposal(&p))

	require.NoError(t, s.ReplaceLessons(nil, []model.Finding{
		{ID: 5, LessonKey: "k", Path: "workers/w.py", Body: "b",
			Paths: []string{"workers/w.py", "tests/test_w.py"}, Source: model.SourceReview},
	}))

	got, err := s.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, []string{"api", "db"}, got[0].Regions)

	findings, err := s.AllFindings()
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, []string{"workers/w.py", "tests/test_w.py"}, findings[0].Paths)
}
