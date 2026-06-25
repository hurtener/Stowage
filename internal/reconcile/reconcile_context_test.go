package reconcile_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// TestBuildReconcileContext_PerNeighborBudget verifies the D-129 per-neighbor turn
// allocation: the candidate is capped at maxCandidateContextTurns and EACH neighbor gets
// its own up-to-maxNeighborContextTurns turns — so a later neighbor is not starved by the
// candidate (or earlier neighbors) consuming a single shared budget, as the old 12-turn
// shared budget did.
func TestBuildReconcileContext_PerNeighborBudget(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()
	scope := tenantScope("t-rc-perneighbor")
	now := time.Now().UnixMilli()

	// Helper: append n records and return their IDs.
	mkRecords := func(prefix string, n int) []string {
		recs := make([]store.Record, n)
		ids := make([]string, n)
		for i := 0; i < n; i++ {
			id := ulid.Make().String()
			ids[i] = id
			recs[i] = store.Record{
				ID: id, TenantID: scope.Tenant, Role: "user",
				Content: fmt.Sprintf("%s turn %d", prefix, i), OccurredAt: now, CreatedAt: now,
			}
		}
		if err := st.Records().Append(ctx, scope, recs); err != nil {
			t.Fatalf("append %s records: %v", prefix, err)
		}
		return ids
	}

	// Helper: commit a neighbor memory with provenance to the given record IDs.
	mkNeighbor := func(content string, recIDs []string) store.Memory {
		m := store.Memory{
			ID: ulid.Make().String(), Kind: "fact", Content: content, Status: "active",
			Confidence: 0.8, TrustSource: "llm_extracted", Stability: 1.0, CreatedAt: now, UpdatedAt: now,
		}
		prov := make([]store.Provenance, len(recIDs))
		for i, rid := range recIDs {
			prov[i] = store.Provenance{ID: ulid.Make().String(), MemoryID: m.ID, RecordID: rid, TenantID: scope.Tenant, CreatedAt: now}
		}
		if err := st.Memories().Commit(ctx, scope, store.CommitSet{Action: store.ActionAdd, Memory: m, Scope: scope, Provenance: prov}); err != nil {
			t.Fatalf("commit neighbor: %v", err)
		}
		return m
	}

	// Candidate with 6 source turns; three neighbors with 5 source turns each.
	candIDs := mkRecords("cand", 6)
	n1 := mkNeighbor("neighbor one", mkRecords("n1", 5))
	n2 := mkNeighbor("neighbor two", mkRecords("n2", 5))
	n3 := mkNeighbor("neighbor three", mkRecords("n3", 5))

	cand := pipeline.Candidate{Content: "candidate fact"}
	for _, id := range candIDs {
		cand.Provenance = append(cand.Provenance, pipeline.ProvSpan{RecordID: id})
	}

	ch := make(chan pipeline.CandidateBatch)
	stage := reconcile.New(st.Memories(), st.Ops(), st.Events(), &stubGateway{}, discardLogger(), ch)
	stage.SetRecordStore(st.Records())

	rc := stage.ExportBuildReconcileContext(ctx, scope, cand, []store.Memory{n1, n2, n3})

	if got := len(rc.CandidateTurns); got != reconcile.ExportMaxCandidateContextTurns {
		t.Errorf("CandidateTurns = %d, want %d (capped)", got, reconcile.ExportMaxCandidateContextTurns)
	}
	// The key property: EVERY neighbor gets its own guaranteed turns — none starved.
	for _, n := range []store.Memory{n1, n2, n3} {
		got := len(rc.NeighborTurns[n.ID])
		if got != reconcile.ExportMaxNeighborContextTurns {
			t.Errorf("NeighborTurns[%s] = %d, want %d (per-neighbor guarantee)", n.ID, got, reconcile.ExportMaxNeighborContextTurns)
		}
	}
}
