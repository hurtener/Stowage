-- Phase 18: subject-index on events for ListBySubject (D-064).
-- Enables efficient newest-first event lookup by (tenant_id, subject_id),
-- which the rollback endpoint uses to find the invertible reconcile event.
CREATE INDEX IF NOT EXISTS idx_events_subject
    ON events (tenant_id, subject_id, created_at);
