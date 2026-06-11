-- Phase 09: memory_vectors table (BYTEA float32-LE, D-046) + tsvector lexical search.
-- No pgvector extension; brute-force cosine in Go; CI stays postgres:17.
-- Language 'simple' for tsvector (no stemming surprises across deployments).

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
    vec        BYTEA   NOT NULL  -- float32 values in little-endian byte order
);
CREATE INDEX IF NOT EXISTS idx_memory_vectors_tenant ON memory_vectors(tenant_id);

-- ---------------------------------------------------------------------------
-- Lexical search: generated tsvector column on memories
-- 'simple' dictionary: case-fold only, no stemming.
-- ---------------------------------------------------------------------------

ALTER TABLE memories ADD COLUMN IF NOT EXISTS tsv tsvector
    GENERATED ALWAYS AS (
        to_tsvector('simple', COALESCE(content, '') || ' ' || COALESCE(context, ''))
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_memories_tsv ON memories USING GIN(tsv);

-- ---------------------------------------------------------------------------
-- Lexical search: generated tsvector column on memory_queries
-- ---------------------------------------------------------------------------

ALTER TABLE memory_queries ADD COLUMN IF NOT EXISTS tsv tsvector
    GENERATED ALWAYS AS (
        to_tsvector('simple', COALESCE(query, ''))
    ) STORED;

CREATE INDEX IF NOT EXISTS idx_memory_queries_tsv ON memory_queries USING GIN(tsv);
