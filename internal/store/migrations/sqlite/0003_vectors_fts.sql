-- Phase 09: memory_vectors table (float32-LE BLOB, D-046) + FTS5 lexical search.
-- No pgvector extension; brute-force cosine in Go; CI stays postgres:17.
-- Language 'simple' for FTS (no stemming surprises across deployments).

-- ---------------------------------------------------------------------------
-- Vector embeddings (D-046)
-- ---------------------------------------------------------------------------

CREATE TABLE IF NOT EXISTS memory_vectors (
    memory_id  TEXT    NOT NULL PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    tenant_id  TEXT    NOT NULL,
    project_id TEXT,
    user_id    TEXT,
    session_id TEXT,
    model      TEXT    NOT NULL DEFAULT '',
    dims       INTEGER NOT NULL DEFAULT 0,
    vec        BLOB    NOT NULL  -- float32 values in little-endian byte order
);
CREATE INDEX IF NOT EXISTS idx_memory_vectors_tenant ON memory_vectors(tenant_id);

-- ---------------------------------------------------------------------------
-- FTS5: memories (content + context)
-- External-content table pointing at memories; sync'd by triggers below.
-- ---------------------------------------------------------------------------

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
    content,
    context,
    content='memories',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 1'
);

-- Populate from existing rows (idempotent via INSERT OR IGNORE on the backing store).
INSERT INTO memories_fts(rowid, content, context)
    SELECT rowid, content, context FROM memories;

CREATE TRIGGER IF NOT EXISTS memories_fts_ai AFTER INSERT ON memories BEGIN
    INSERT INTO memories_fts(rowid, content, context)
        VALUES (new.rowid, new.content, new.context);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_ad AFTER DELETE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, context)
        VALUES ('delete', old.rowid, old.content, old.context);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_au AFTER UPDATE ON memories BEGIN
    INSERT INTO memories_fts(memories_fts, rowid, content, context)
        VALUES ('delete', old.rowid, old.content, old.context);
    INSERT INTO memories_fts(rowid, content, context)
        VALUES (new.rowid, new.content, new.context);
END;

-- ---------------------------------------------------------------------------
-- FTS5: memory_queries (anticipated queries)
-- ---------------------------------------------------------------------------

CREATE VIRTUAL TABLE IF NOT EXISTS memory_queries_fts USING fts5(
    query,
    content='memory_queries',
    content_rowid='rowid',
    tokenize='unicode61 remove_diacritics 1'
);

INSERT INTO memory_queries_fts(rowid, query)
    SELECT rowid, query FROM memory_queries;

CREATE TRIGGER IF NOT EXISTS memory_queries_fts_ai AFTER INSERT ON memory_queries BEGIN
    INSERT INTO memory_queries_fts(rowid, query) VALUES (new.rowid, new.query);
END;

CREATE TRIGGER IF NOT EXISTS memory_queries_fts_ad AFTER DELETE ON memory_queries BEGIN
    INSERT INTO memory_queries_fts(memory_queries_fts, rowid, query)
        VALUES ('delete', old.rowid, old.query);
END;

CREATE TRIGGER IF NOT EXISTS memory_queries_fts_au AFTER UPDATE ON memory_queries BEGIN
    INSERT INTO memory_queries_fts(memory_queries_fts, rowid, query)
        VALUES ('delete', old.rowid, old.query);
    INSERT INTO memory_queries_fts(rowid, query) VALUES (new.rowid, new.query);
END;
