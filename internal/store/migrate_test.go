package store

import (
	"database/sql"
	"fmt"
	"os"
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

	v, err := s.GetMeta(MetaSchemaVersion)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v, "a fresh database is stamped with the current version")
}

func TestOpenRefusesNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.SetMeta(MetaSchemaVersion, strconv.Itoa(schemaVersion+7)))
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
		strconv.Itoa(schemaVersion), MetaSchemaVersion)
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
	_, err = db.Exec(`DELETE FROM meta WHERE key = ?`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// Reopening runs the ordered migrations and stamps the version.
	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(MetaSchemaVersion)
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
	_, err = db.Exec(`DELETE FROM meta WHERE key = ?`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(MetaSchemaVersion)
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

func TestMigrationVersionsLegacyTriggerAnswersWithoutRewritingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "r", Note: "n", Status: model.ProposalProposed,
		TriggerChecked: 1700000000,
	}))
	require.NoError(t, s.Close())

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE proposal DROP COLUMN trigger_prompt_version`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE meta SET value = '5' WHERE key = ?`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	got, err := s.Proposals(model.ProposalProposed)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, int64(1700000000), got[0].TriggerChecked)
	assert.Zero(t, got[0].TriggerPromptVersion,
		"the additive migration marks historical answers as legacy without erasing them")
}

func TestMigrateV7AddsSourceAndProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")

	// Rewind a current database to v6: drop the v7 columns and stamp the
	// old version. The row inserted before the rewind is a v6 row.
	s, err := Open(path)
	require.NoError(t, err)
	require.NoError(t, s.InsertProposal(&model.Proposal{
		Signature: "s1", Rule: "old-row", Note: "n", Members: []int64{1, 2},
		Status: model.ProposalApplied,
	}))
	require.NoError(t, s.Close())

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE proposal DROP COLUMN source`)
	require.NoError(t, err)
	_, err = db.Exec(`ALTER TABLE proposal DROP COLUMN provenance`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE meta SET value = '6' WHERE key = ?`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)

	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(MetaSchemaVersion)
	require.NoError(t, err)
	assert.Equal(t, "7", v)

	// The existing row reads as findings without a rewrite.
	old, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, old, 1)
	assert.Equal(t, model.ProposalSourceFindings, old[0].Source)
	assert.Empty(t, old[0].Provenance)

	// A new row round-trips both new columns. An empty source stores
	// findings, and the caller's row gets the stored source.
	row := model.Proposal{Signature: "s2", Rule: "new-row", Note: "n", Members: []int64{3},
		Status: model.ProposalProposed, Provenance: `{"kind":"test"}`}
	require.NoError(t, s.InsertProposal(&row))
	assert.Equal(t, model.ProposalSourceFindings, row.Source)

	got, err := s.ProposalsWithIDs([]int64{row.ID})
	require.NoError(t, err)
	assert.Equal(t, []model.Proposal{row}, got)

	// The step guards on the live schema, so a second run is a no-op. A
	// pre-versioning database can carry the columns already.
	tx, err := s.db.Begin()
	require.NoError(t, err)
	require.NoError(t, addProposalSourceAndProvenance(tx))
	require.NoError(t, tx.Rollback())
}

// TestOpenUpgradesTheV080Database proves the upgrade from the last release.
// testdata/schema-v0.8.0.sql is a byte copy of the schema.sql of v0.8.0,
// which stamps schema 6. After Open, each table must equal the table of a
// fresh database, column by column, and the old row must keep its fields.
func TestOpenUpgradesTheV080Database(t *testing.T) {
	ddl, err := os.ReadFile(filepath.Join("testdata", "schema-v0.8.0.sql"))
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "index.db")

	db, err := rawOpen(t, path)
	require.NoError(t, err)
	_, err = db.Exec(string(ddl))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO proposal (signature, rule, region, regions, note, members, status, created_at)
		VALUES ('s1', 'old-row', 'api', '["api","web"]', 'n', '[1,2]', ?, 5)`, model.ProposalApplied)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO meta (key, value) VALUES (?, '6')`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err := Open(path)
	require.NoError(t, err)

	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(MetaSchemaVersion)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(schemaVersion), v)

	got, err := s.Proposals(model.ProposalApplied)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "old-row", got[0].Rule)
	assert.Equal(t, []string{"api", "web"}, got[0].Regions)
	assert.Equal(t, []int64{1, 2}, got[0].Members)
	assert.Equal(t, int64(5), got[0].CreatedAt)
	assert.Equal(t, model.ProposalSourceFindings, got[0].Source, "a row of v0.8.0 reads as findings")
	assert.Empty(t, got[0].Provenance)

	fresh := openTestStore(t)
	tables := tableNames(t, fresh)
	require.Contains(t, tables, "proposal")
	assert.Equal(t, tables, tableNames(t, s))

	for _, table := range tables {
		assert.Equal(t, tableColumns(t, fresh, table), tableColumns(t, s, table),
			"the migrated %s table equals the table of a fresh database", table)
	}
}

// tableNames lists the user tables of a database by name.
func tableNames(t *testing.T, s *Store) []string {
	t.Helper()

	rows, err := s.db.Query(`SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var names []string

	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		names = append(names, name)
	}

	require.NoError(t, rows.Err())

	return names
}

// tableColumns describes each column of one table in column order: name,
// type, NOT NULL flag, default, and primary key position.
func tableColumns(t *testing.T, s *Store, table string) []string {
	t.Helper()

	rows, err := s.db.Query(`SELECT name, type, "notnull", COALESCE(dflt_value, ''), pk
		FROM pragma_table_info(?) ORDER BY cid`, table)
	require.NoError(t, err)

	defer func() { _ = rows.Close() }()

	var columns []string

	for rows.Next() {
		var (
			name, typ, dflt string
			notNull, pk     int
		)

		require.NoError(t, rows.Scan(&name, &typ, &notNull, &dflt, &pk))
		columns = append(columns, fmt.Sprintf("%s %s notnull=%d default=%s pk=%d", name, typ, notNull, dflt, pk))
	}

	require.NoError(t, rows.Err())

	return columns
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
	_, err = db.Exec(`UPDATE meta SET value = '2' WHERE key = ?`, MetaSchemaVersion)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	s, err = Open(path)
	require.NoError(t, err)
	defer func() { _ = s.Close() }()

	v, err := s.GetMeta(MetaSchemaVersion)
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
