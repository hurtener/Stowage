-- Phase 24 (D-083): index provenance by (record_id, tenant_id) for the reverse
-- lookup ListMemoriesByRecords — gathering an episode's decision memories from its
-- records for causal inference. The provenance table is day-one (§8.1); this is an
-- index-only addition.
CREATE INDEX IF NOT EXISTS idx_provenance_record
  ON provenance (record_id, tenant_id);
