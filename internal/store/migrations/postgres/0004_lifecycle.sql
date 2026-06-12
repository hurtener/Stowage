-- Phase 14: lifecycle sweep support — index on valid_until for decay-grace scan.
-- valid_until already exists on the memories table (Phase 01 init migration).
CREATE INDEX IF NOT EXISTS idx_memories_valid_until
    ON memories(tenant_id, valid_until)
    WHERE valid_until > 0;
