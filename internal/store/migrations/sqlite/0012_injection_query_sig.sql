-- Bar-remediation (A6 / RFC §8.1 amendment): durable hub-dampening signal (D-092).
-- The hub signal — count of DISTINCT query clusters that returned a memory in the
-- recent window — was a per-process LRU that reset on restart. It now derives from
-- the durable, scoped injection rows the retrieve path already writes async: one
-- query_sig column makes the signal COUNT(DISTINCT query_sig). Existing rows default
-- to '' and are excluded from the count (only true retrieve queries carry a sig).
ALTER TABLE injections ADD COLUMN query_sig TEXT NOT NULL DEFAULT '';

-- COVERING index for the hub-signal lookup (WHERE memory_id=? AND created_at>=?,
-- then COUNT(DISTINCT query_sig)): including query_sig lets both drivers satisfy the
-- distinct count from an index-only scan, so a high-traffic "hub" memory (the most
-- rows, by definition) stays cheap on the latency-gated retrieve path (D-035).
CREATE INDEX IF NOT EXISTS idx_injections_memory_created ON injections(memory_id, created_at, query_sig);
