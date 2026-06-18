-- Phase 19 (D-077): index outcome-tagged records for the reflection sweep.
-- RecordStore.ListByOutcome filters by (scope, outcome, occurred_at); a partial
-- index over only the tagged rows keeps it cheap (most records carry no outcome).
-- Forward-only; the outcome/occurred_at columns exist since the day-one schema
-- (§8.1/D-024), so this is an index addition, not a schema-inventory change.
CREATE INDEX IF NOT EXISTS idx_records_tenant_outcome_occurred
  ON records (tenant_id, outcome, occurred_at)
  WHERE outcome <> '';
