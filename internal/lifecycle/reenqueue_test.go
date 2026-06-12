package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// insertRecord inserts a verbatim record and returns its ID.
func insertRecord(t *testing.T, st store.Store, tenant, sessID, branchID string, processedAt int64, occurredAt int64) string {
	t.Helper()
	ctx := context.Background()
	scope := identity.Scope{Tenant: tenant}
	now := time.Now().UnixMilli()
	if occurredAt == 0 {
		occurredAt = now
	}
	rec := store.Record{
		ID:          ulid.Make().String(),
		TenantID:    tenant,
		SessionID:   sessID,
		BranchID:    branchID,
		Role:        "user",
		Content:     "test record content",
		OccurredAt:  occurredAt,
		CreatedAt:   now,
		ProcessedAt: processedAt,
	}
	if err := st.Records().Append(ctx, scope, []store.Record{rec}); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	return rec.ID
}

func TestReenqueueSweepStalledRecord(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	tenant := "requeue-t1"
	// Insert a record that is older than the reenqueue deadline (10m).
	// processedAt = 0 (not yet processed), createdAt = 15 minutes ago.
	old := time.Now().Add(-15 * time.Minute).UnixMilli()
	recID := insertRecord(t, st, tenant, "sess-1", "branch-1", 0, old)

	ingest := make(chan pipeline.Item, 16)
	profile := lifecycle.Profile{
		ReenqueueDeadline:  10 * time.Minute,
		ReenqueueBatchSize: 10,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// The stalled record should have been re-enqueued.
	select {
	case item := <-ingest:
		if item.RecordID != recID {
			t.Errorf("re-enqueued record ID mismatch: got %q, want %q", item.RecordID, recID)
		}
		if item.TenantID != tenant {
			t.Errorf("re-enqueued record tenant mismatch: got %q, want %q", item.TenantID, tenant)
		}
	default:
		t.Error("expected re-enqueued item in ingest channel, got none")
	}
}

func TestReenqueueSweepFreshRecord(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	tenant := "requeue-t2"
	// Fresh record (1 minute old) — should NOT be re-enqueued with 10m deadline.
	fresh := time.Now().Add(-1 * time.Minute).UnixMilli()
	insertRecord(t, st, tenant, "sess-2", "branch-2", 0, fresh)

	ingest := make(chan pipeline.Item, 16)
	profile := lifecycle.Profile{
		ReenqueueDeadline:  10 * time.Minute,
		ReenqueueBatchSize: 10,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	select {
	case item := <-ingest:
		t.Errorf("unexpected item in ingest channel: %+v", item)
	default:
		// Expected: no items
	}
}

func TestReenqueueSweepChannelFull(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	tenant := "requeue-t3"
	old := time.Now().Add(-15 * time.Minute).UnixMilli()

	// Insert 5 stalled records.
	for i := 0; i < 5; i++ {
		insertRecord(t, st, tenant, "sess-3", "branch-3", 0, old)
	}

	// Channel with capacity 2 — only 2 of 5 records should be sent.
	ingest := make(chan pipeline.Item, 2)
	profile := lifecycle.Profile{
		ReenqueueDeadline:  10 * time.Minute,
		ReenqueueBatchSize: 10,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	sent := len(ingest)
	if sent != 2 {
		t.Errorf("expected 2 items sent (channel full), got %d", sent)
	}
}

func TestReenqueueSweepProcessedRecord(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()

	tenant := "requeue-t4"
	old := time.Now().Add(-15 * time.Minute).UnixMilli()
	// processedAt != 0 → already processed → should NOT be re-enqueued.
	insertRecord(t, st, tenant, "sess-4", "branch-4", old, old)

	ingest := make(chan pipeline.Item, 16)
	profile := lifecycle.Profile{
		ReenqueueDeadline:  10 * time.Minute,
		ReenqueueBatchSize: 10,
	}
	mgr := lifecycle.New(st, testLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	select {
	case item := <-ingest:
		t.Errorf("unexpected processed record re-enqueued: %+v", item)
	default:
		// Expected: no items
	}
}
