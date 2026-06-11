-- Phase 08: content_hash column for exact-dedup pre-filter (D-044, D-045).
-- Normalization: trim space + collapse runs of whitespace to single space (case-sensitive).
-- Case is preserved because case changes meaning (e.g. "Python" vs "python",
-- acronyms, proper nouns). SHA-256 hex stored as TEXT (64 chars).
-- Backfill not required: no production data -- new memories populated by Commit.
--
-- m7 (TOCTOU): UNIQUE index on full scope + hash using COALESCE for nullable
-- scope columns so that (tenant, '', '', '', hash) is the canonical key.
-- SQLite supports partial indexes with WHERE clauses.
-- The unique constraint fires on concurrent identical-hash inserts and the
-- sqlitestore driver maps this to ErrDuplicateContent.
ALTER TABLE memories ADD COLUMN content_hash TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS idx_memories_content_hash_unique
  ON memories (tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), content_hash)
  WHERE content_hash IS NOT NULL;
