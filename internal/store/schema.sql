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

CREATE TABLE IF NOT EXISTS lesson (
    id               INTEGER PRIMARY KEY,
    cluster_key      TEXT NOT NULL,
    region           TEXT NOT NULL DEFAULT '',
    symptom          TEXT NOT NULL DEFAULT '',
    fix              TEXT NOT NULL DEFAULT '',
    occurrences      INTEGER NOT NULL DEFAULT 1,
    promoted_rule_id TEXT
);
CREATE INDEX IF NOT EXISTS lesson_cluster ON lesson (cluster_key);
