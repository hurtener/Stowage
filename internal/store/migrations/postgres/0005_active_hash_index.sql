-- Phase 14: scope the content-hash uniqueness to ACTIVE rows only.
-- Superseded rows legitimately retain their content_hash (history, D-017);
-- the original non-filtered index blocked sweep merges that keep the
-- surviving content identical (the merged row collided with its own
-- superseded source). Forward-only: drop and recreate as a partial index.
DROP INDEX IF EXISTS idx_memories_content_hash_unique;
CREATE UNIQUE INDEX idx_memories_content_hash_unique
  ON memories (tenant_id, COALESCE(project_id,''), COALESCE(user_id,''), COALESCE(session_id,''), content_hash)
  WHERE content_hash IS NOT NULL AND status = 'active';
