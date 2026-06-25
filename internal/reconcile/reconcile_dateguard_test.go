package reconcile_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// TestCandidateOlderThanTarget covers the deterministic predicate behind the D-127
// date-direction guard: it fires ONLY when both assertion dates are known and the
// candidate is strictly older. Unknown (zero) dates and equal dates disable it.
func TestCandidateOlderThanTarget(t *testing.T) {
	const base = int64(1_696_000_000_000) // ~2023-09-29
	cases := []struct {
		name      string
		candDate  int64
		validFrom int64
		want      bool
	}{
		{"candidate strictly older", base - 1, base, true},
		{"candidate strictly newer", base + 1, base, false},
		{"equal dates", base, base, false},
		{"candidate date unknown", 0, base, false},
		{"target date unknown", base, 0, false},
		{"both unknown", 0, 0, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := pipeline.Candidate{OccurredAt: tc.candDate}
			target := store.Memory{ValidFrom: tc.validFrom}
			if got := reconcile.ExportCandidateOlderThanTarget(c, target); got != tc.want {
				t.Errorf("candidateOlderThanTarget(cand=%d, target=%d) = %v, want %v",
					tc.candDate, tc.validFrom, got, tc.want)
			}
		})
	}
}

// TestStageDateDirectionGuard is the end-to-end guard test: an OLDER-dated candidate
// must NOT supersede a NEWER-dated memory (the inversion bug, handoff sub-case a), while
// a NEWER-dated candidate supersedes as normal — and the normal supersede still
// round-trips through Rollback (reversibility, D-017). The candidate differs from the
// target only by a numeral so it travels the realistic path: near-dup pre-filter →
// NumeralsDiverge (D-104) → LLM supersede verdict → the date guard adjudicates direction.
func TestStageDateDirectionGuard(t *testing.T) {
	const entity = "painting-projects"
	const kw = "projects"
	const targetContent = "The user has completed 5 projects since starting painting classes"
	const candContent = "The user has completed 4 projects since starting painting classes"

	targetValidFrom := time.Date(2023, 10, 9, 0, 0, 0, 0, time.UTC).UnixMilli()
	olderDate := time.Date(2023, 8, 16, 0, 0, 0, 0, time.UTC).UnixMilli()
	newerDate := time.Date(2023, 12, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

	t.Run("older candidate does not bury newer memory (inverted)", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		ctx := context.Background()
		scope := tenantScope("t-dateguard-inverted")

		target := store.Memory{
			ID:          ulid.Make().String(),
			Kind:        "fact",
			Content:     targetContent,
			Status:      "active",
			Importance:  3,
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
			ValidFrom:   targetValidFrom,
			CreatedAt:   time.Now().UnixMilli(),
			UpdatedAt:   time.Now().UnixMilli(),
		}
		insertTestMemory(t, st, scope, target, []string{entity}, []string{kw})

		gw := &stubGateway{responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"correction"}`)},
		}}

		cand := newCandidate("fact", candContent, 3, 0.9, entity)
		cand.Keywords = []string{kw}
		cand.OccurredAt = olderDate // OLDER than target → guard must invert
		batch := pipeline.CandidateBatch{Scope: scope, Candidates: []pipeline.Candidate{cand}}
		runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

		// The newer target must remain the sole ACTIVE memory.
		active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
		if len(active) != 1 || active[0].ID != target.ID {
			t.Fatalf("inverted: want target as the only active memory, got %d active", len(active))
		}
		got, err := st.Memories().Get(ctx, scope, target.ID)
		if err != nil {
			t.Fatalf("get target: %v", err)
		}
		if got.Status != "active" {
			t.Errorf("inverted: target status = %q, want active (newer fact must not be buried)", got.Status)
		}
		if got.Content != targetContent {
			t.Errorf("inverted: target content = %q, want unchanged %q", got.Content, targetContent)
		}

		// The older candidate must be recorded as superseded-by the target.
		superseded, _, _ := st.Memories().ListByStatus(ctx, scope, "superseded", 10, "")
		if len(superseded) != 1 {
			t.Fatalf("inverted: want 1 superseded (older candidate), got %d", len(superseded))
		}
		if superseded[0].SupersededByID != target.ID {
			t.Errorf("inverted: candidate superseded_by_id = %q, want target %q",
				superseded[0].SupersededByID, target.ID)
		}

		// No memory.superseded event — that type is restorable and rollback would flip
		// the direction back, re-burying the newer fact. A reconcile.warned must record it.
		if evs := eventsByType(t, st, scope, "memory.superseded"); len(evs) != 0 {
			t.Errorf("inverted: emitted %d memory.superseded events, want 0 (non-restorable add)", len(evs))
		}
		if evs := eventsByType(t, st, scope, "reconcile.warned"); len(evs) == 0 {
			t.Error("inverted: no reconcile.warned event recording the date-guard inversion")
		}
	})

	t.Run("newer candidate supersedes normally and round-trips", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		ctx := context.Background()
		scope := tenantScope("t-dateguard-normal")

		target := store.Memory{
			ID:          ulid.Make().String(),
			Kind:        "fact",
			Content:     targetContent,
			Status:      "active",
			Importance:  3,
			Confidence:  0.9,
			TrustSource: "llm_extracted",
			Stability:   1.0,
			ContentHash: reconcile.ContentHash(reconcile.NormalizeContent(targetContent)),
			ValidFrom:   targetValidFrom,
			CreatedAt:   time.Now().UnixMilli(),
			UpdatedAt:   time.Now().UnixMilli(),
		}
		insertTestMemory(t, st, scope, target, []string{entity}, []string{kw})

		gw := &stubGateway{responses: []gateway.CompleteResponse{
			{JSON: json.RawMessage(`{"action":"supersede","target_ids":["` + target.ID + `"],"reason":"correction"}`)},
		}}

		cand := newCandidate("fact", candContent, 3, 0.9, entity)
		cand.Keywords = []string{kw}
		cand.OccurredAt = newerDate // NEWER than target → normal supersede
		batch := pipeline.CandidateBatch{Scope: scope, Candidates: []pipeline.Candidate{cand}}
		runStage(t, st.Memories(), st.Ops(), st.Events(), gw, batch)

		got, err := st.Memories().Get(ctx, scope, target.ID)
		if err != nil {
			t.Fatalf("get target: %v", err)
		}
		if got.Status != "superseded" {
			t.Errorf("normal: target status = %q, want superseded", got.Status)
		}
		active, _, _ := st.Memories().ListByStatus(ctx, scope, "active", 10, "")
		if len(active) != 1 || active[0].Content != candContent {
			t.Fatalf("normal: want the newer candidate as the only active memory, got %d active", len(active))
		}

		// Reversibility round-trip (D-017): rolling back the supersede restores the
		// target to active and tombstones the superseding row.
		if _, err := reconcile.Rollback(ctx, st, scope, target.ID); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		restored, err := st.Memories().Get(ctx, scope, target.ID)
		if err != nil {
			t.Fatalf("get target after rollback: %v", err)
		}
		if restored.Status != "active" {
			t.Errorf("rollback: target status = %q, want active", restored.Status)
		}
	})
}
