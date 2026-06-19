-- Phase 27 (D-087): index suggestions for the per-session list + expiry scan
-- (ListBySession / ListPendingBefore). The suggestions table is day-one (§8.1); this
-- is an index-only addition.
CREATE INDEX IF NOT EXISTS idx_suggestions_pending
  ON suggestions (tenant_id, user_id, session_id, status, created_at);
