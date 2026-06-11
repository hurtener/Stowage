-- Phase 08: content_hash column for exact-dedup pre-filter (D-044, D-045)
-- Normalization: trim space + collapse runs of whitespace to single space (case-sensitive).
-- Case is preserved because case changes meaning (e.g. "Python" vs "python",
-- acronyms, proper nouns). SHA-256 hex stored as TEXT (64 chars).
-- Backfill not required: no production data -- new memories populated by Commit.
ALTER TABLE memories ADD COLUMN content_hash TEXT;
CREATE INDEX IF NOT EXISTS idx_memories_tenant_hash ON memories(tenant_id, content_hash) WHERE content_hash IS NOT NULL
