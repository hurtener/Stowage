-- Phase 22 (D-079): index episodes for the boundary-detection idempotency lookup
-- (GetEpisodeBySession) and the narration scan (episodes needing a narrative).
-- The episodes table is day-one (§8.1); this is an index-only addition.
CREATE INDEX IF NOT EXISTS idx_episodes_tenant_session
  ON episodes (tenant_id, session_id);
CREATE INDEX IF NOT EXISTS idx_episodes_narrative_pending
  ON episodes (started_at)
  WHERE narrative_memory_id = '';
