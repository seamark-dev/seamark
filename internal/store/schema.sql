-- Seamark graph schema v1 (RFC-001 §5.1).
-- Six core tables carry the product; meta and decision_file are additive
-- implementation details (see docs/PLAN.md).

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) WITHOUT ROWID;

-- structure ------------------------------------------------------------

CREATE TABLE IF NOT EXISTS symbol (
    id         INTEGER PRIMARY KEY,
    fqn        TEXT NOT NULL,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL,
    file       TEXT NOT NULL DEFAULT '',
    start_line INTEGER NOT NULL DEFAULT 0,
    start_col  INTEGER NOT NULL DEFAULT 0,
    end_line   INTEGER NOT NULL DEFAULT 0,
    end_col    INTEGER NOT NULL DEFAULT 0,
    sig        TEXT NOT NULL DEFAULT '',
    doc_hash   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS symbol_fqn  ON symbol (fqn);
CREATE INDEX IF NOT EXISTS symbol_name ON symbol (name);
CREATE INDEX IF NOT EXISTS symbol_file ON symbol (file);

CREATE TABLE IF NOT EXISTS edge (
    src    INTEGER NOT NULL REFERENCES symbol (id),
    dst    INTEGER NOT NULL REFERENCES symbol (id),
    kind   TEXT    NOT NULL,
    origin TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (src, dst, kind)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS edge_dst ON edge (dst, kind);

-- Full-text search over symbols. External-content table: rows are read from
-- symbol; the indexer issues a full 'rebuild' after each bulk load.
CREATE VIRTUAL TABLE IF NOT EXISTS symbol_fts USING fts5(
    name, fqn, file,
    content='symbol', content_rowid='id'
);

-- history --------------------------------------------------------------

CREATE TABLE IF NOT EXISTS cochange (
    file_a   TEXT    NOT NULL,
    file_b   TEXT    NOT NULL,
    together INTEGER NOT NULL,
    total    INTEGER NOT NULL,
    lift     REAL    NOT NULL,
    PRIMARY KEY (file_a, file_b)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS cochange_b ON cochange (file_b);

CREATE TABLE IF NOT EXISTS decision (
    id     INTEGER PRIMARY KEY,
    kind   TEXT NOT NULL,
    ref    TEXT NOT NULL UNIQUE,
    ts     INTEGER NOT NULL,
    author TEXT NOT NULL DEFAULT '',
    title  TEXT NOT NULL DEFAULT '',
    body   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS decision_ts ON decision (ts);

CREATE TABLE IF NOT EXISTS decision_link (
    decision_id INTEGER NOT NULL REFERENCES decision (id),
    symbol_id   INTEGER NOT NULL REFERENCES symbol (id),
    relation    TEXT    NOT NULL,
    PRIMARY KEY (decision_id, symbol_id, relation)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS decision_file (
    decision_id INTEGER NOT NULL REFERENCES decision (id),
    file        TEXT    NOT NULL,
    PRIMARY KEY (decision_id, file)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS decision_file_file ON decision_file (file);

-- incremental indexing cache (M-freshness L1) --------------------------
-- Per-file parse output, keyed by content hash: an unchanged file reuses
-- its cached FileResult instead of re-running tree-sitter (the dominant
-- cost). Survives Rebuild (it is a cache, not derived graph state); a
-- decode failure or version bump simply forces a reparse.
CREATE TABLE IF NOT EXISTS parse_cache (
    file TEXT PRIMARY KEY,
    hash TEXT NOT NULL,   -- sha256 hex of the file's bytes
    data BLOB NOT NULL    -- gob-encoded parse.FileResult
) WITHOUT ROWID;

-- effects (populated in M4) --------------------------------------------

CREATE TABLE IF NOT EXISTS effect (
    symbol_id INTEGER NOT NULL REFERENCES symbol (id),
    tag       TEXT    NOT NULL,
    origin    TEXT    NOT NULL,           -- DIRECT | PROPAGATED
    depth     INTEGER NOT NULL DEFAULT 0, -- 0 for DIRECT
    PRIMARY KEY (symbol_id, tag)
) WITHOUT ROWID;

-- policy + learning (populated in M3/M6) --------------------------------

CREATE TABLE IF NOT EXISTS rule (
    id         TEXT PRIMARY KEY,
    scope_expr TEXT NOT NULL DEFAULT '',
    tier       TEXT NOT NULL,
    check_expr TEXT NOT NULL,
    message    TEXT NOT NULL DEFAULT '',
    severity   TEXT NOT NULL DEFAULT 'warn',
    hits       INTEGER NOT NULL DEFAULT 0,
    last_hit   INTEGER
) WITHOUT ROWID;

-- A lesson is a cluster of review-comment recurrences (M6). Recomputed
-- from the mined comment window on every rebuild, so occurrences reflect
-- recent activity rather than an all-time tally that never decays.
CREATE TABLE IF NOT EXISTS lesson (
    id               INTEGER PRIMARY KEY,
    cluster_key      TEXT NOT NULL UNIQUE,
    region           TEXT NOT NULL DEFAULT '',  -- file or directory the flag lands in
    reviewer         TEXT NOT NULL DEFAULT '',  -- coderabbit | copilot | bot | human | mixed
    symptom          TEXT NOT NULL DEFAULT '',  -- rule code (RUF001) or a normalized message
    fix              TEXT NOT NULL DEFAULT '',  -- extracted suggestion, when present
    occurrences      INTEGER NOT NULL DEFAULT 1,
    last_ts          INTEGER NOT NULL DEFAULT 0, -- most recent occurrence (unix seconds)
    example_url      TEXT NOT NULL DEFAULT '',   -- a representative comment, for provenance
    promoted_rule_id TEXT
);
CREATE INDEX IF NOT EXISTS lesson_region ON lesson (region);

-- The raw review comments behind the lessons (M6 Tier 2 input): the
-- substance-filtered, top-level comments exactly as clustered, kept in
-- full so distillation and provenance work from what reviewers wrote,
-- not from 80-char fingerprints. Replaced together with lesson on every
-- successful mine; the GitHub comment id is stable across mines, which
-- is what makes incremental distillation (group signatures over member
-- ids) possible.
CREATE TABLE IF NOT EXISTS finding (
    id         INTEGER PRIMARY KEY,        -- GitHub review-comment id
    lesson_key TEXT NOT NULL,              -- lesson.cluster_key this comment fed
    path       TEXT NOT NULL,              -- repo-relative file
    pr         INTEGER NOT NULL DEFAULT 0,
    reviewer   TEXT NOT NULL DEFAULT '',
    body       TEXT NOT NULL,              -- boilerplate-stripped, capped
    url        TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS finding_lesson ON finding (lesson_key);
CREATE INDEX IF NOT EXISTS finding_path ON finding (path);

-- Distillation memory (M6 Tier 2): which candidate-group evidence sets
-- have already been read by the distiller. A signature hashes the
-- member finding ids, so an unchanged group is never paid for twice —
-- and any new finding changes its group's signature, re-opening exactly
-- that group. Proposal rows (their own lifecycle: proposed / applied /
-- dismissed) land in a separate table with the distill command.
CREATE TABLE IF NOT EXISTS distilled (
    signature TEXT PRIMARY KEY,           -- hash of the member finding ids
    region    TEXT NOT NULL DEFAULT '',   -- the group's area, for reporting
    at        INTEGER NOT NULL DEFAULT 0  -- unix seconds of the run
) WITHOUT ROWID;
