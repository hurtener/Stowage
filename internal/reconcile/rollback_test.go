package reconcile_test

// rollback_test.go — unit coverage for the exported reversibility core (D-070):
// reconcile.Rollback / reconcile.Resolve / reconcile.GetMemory and the typed
// conflict guards. These run the SAME orchestration the HTTP handler used to own
// (behavior-preserving re-homing) against a real sqlite store.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// ─── seed helpers ─────────────────────────────────────────────────────────────

// seedRBMemory inserts an active memory with junctions and a distinct content
// hash, returning the committed row.
func seedRBMemory(t *testing.T, st store.Store, scope identity.Scope, content string) store.Memory {
	t.Helper()
	now := time.Now().UnixMilli()
	mem := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     content,
		Status:      "active",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	cs := store.CommitSet{
		Action:   store.ActionAdd,
		Memory:   mem,
		Entities: []string{"ent-" + mem.ID[:8]},
		Keywords: []string{"kw-" + mem.ID[:8]},
		Queries:  []string{"q-" + mem.ID[:8]},
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.added",
			SubjectID: mem.ID,
			Payload:   "{}",
			CreatedAt: now,
		}},
		Scope: scope,
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("seedRBMemory commit: %v", err)
	}
	got, err := st.Memories().Get(context.Background(), scope, mem.ID)
	if err != nil {
		t.Fatalf("seedRBMemory get: %v", err)
	}
	return *got
}

func writeRBUpdatedEvent(t *testing.T, st store.Store, scope identity.Scope, memID string) {
	t.Helper()
	mem, err := st.Memories().Get(context.Background(), scope, memID)
	if err != nil {
		t.Fatalf("writeRBUpdatedEvent get: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, memID)
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: memID,
		Reason:    "test: simulate update",
		Payload:   reconcile.MarshalPriorState(*mem, jt),
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("writeRBUpdatedEvent emit: %v", err)
	}
}

func writeRBSupersededEvent(t *testing.T, st store.Store, scope identity.Scope, targetID, supersederID string) {
	t.Helper()
	target, err := st.Memories().Get(context.Background(), scope, targetID)
	if err != nil {
		t.Fatalf("writeRBSupersededEvent get: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, targetID)
	now := time.Now().UnixMilli()
	if err := st.Events().Emit(context.Background(), scope, store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.superseded",
		SubjectID: targetID,
		Reason:    "test: simulate supersede",
		Payload:   reconcile.MarshalPriorState(*target, jt),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("writeRBSupersededEvent emit: %v", err)
	}
	tm := *target
	tm.Status = "superseded"
	tm.SupersededByID = supersederID
	tm.UpdatedAt = now
	if err := st.Memories().Update(context.Background(), scope, tm); err != nil {
		t.Fatalf("writeRBSupersededEvent update: %v", err)
	}
}

func writeRBMergedEvent(t *testing.T, st store.Store, scope identity.Scope, sibID, digestID string) {
	t.Helper()
	sib, err := st.Memories().Get(context.Background(), scope, sibID)
	if err != nil {
		t.Fatalf("writeRBMergedEvent get: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, sibID)
	now := time.Now().UnixMilli()
	if err := st.Events().Emit(context.Background(), scope, store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.merged",
		SubjectID: sibID,
		Reason:    "test: simulate merge",
		Payload:   reconcile.MarshalPriorState(*sib, jt),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("writeRBMergedEvent emit: %v", err)
	}
	sm := *sib
	sm.Status = "superseded"
	sm.SupersededByID = digestID
	sm.UpdatedAt = now
	if err := st.Memories().Update(context.Background(), scope, sm); err != nil {
		t.Fatalf("writeRBMergedEvent update: %v", err)
	}
}

func mustStatus(t *testing.T, st store.Store, scope identity.Scope, id, want string) {
	t.Helper()
	m, err := st.Memories().Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("get %q: %v", id, err)
	}
	if m.Status != want {
		t.Errorf("memory %q status: got %q want %q", id, m.Status, want)
	}
}

// ─── Rollback round-trips ─────────────────────────────────────────────────────

func TestCoreRollback_Updated(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-upd")
	ctx := context.Background()

	mem := seedRBMemory(t, st, scope, "original updated")
	writeRBUpdatedEvent(t, st, scope, mem.ID)

	upd := mem
	upd.Content = "changed"
	upd.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(ctx, scope, upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	res, err := reconcile.Rollback(ctx, st, scope, mem.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if res.Memory == nil || res.Memory.Content != mem.Content {
		t.Fatalf("restored content mismatch: %+v", res.Memory)
	}
	mustStatus(t, st, scope, mem.ID, "active")
}

func TestCoreRollback_Superseded(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-sup")
	ctx := context.Background()

	target := seedRBMemory(t, st, scope, "supersede target")
	superseder := seedRBMemory(t, st, scope, "superseder")
	writeRBSupersededEvent(t, st, scope, target.ID, superseder.ID)

	if _, err := reconcile.Rollback(ctx, st, scope, target.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mustStatus(t, st, scope, target.ID, "active")
	mustStatus(t, st, scope, superseder.ID, "deleted")
}

func TestCoreRollback_SupersededNoResultRow(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-sup-norow")
	ctx := context.Background()

	target := seedRBMemory(t, st, scope, "supersede no result row")
	// superseded event but superseded_by_id left empty → plain restore path.
	writeRBSupersededEvent(t, st, scope, target.ID, "")

	if _, err := reconcile.Rollback(ctx, st, scope, target.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mustStatus(t, st, scope, target.ID, "active")
}

func TestCoreRollback_Merged(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-mrg")
	ctx := context.Background()

	src1 := seedRBMemory(t, st, scope, "merge src 1")
	src2 := seedRBMemory(t, st, scope, "merge src 2")
	digest := seedRBMemory(t, st, scope, "merge digest")
	writeRBMergedEvent(t, st, scope, src1.ID, digest.ID)
	writeRBMergedEvent(t, st, scope, src2.ID, digest.ID)

	if _, err := reconcile.Rollback(ctx, st, scope, src1.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mustStatus(t, st, scope, src1.ID, "active")
	mustStatus(t, st, scope, src2.ID, "active")
	mustStatus(t, st, scope, digest.ID, "deleted")
}

func TestCoreRollback_MergedNoSuperseder(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-mrg-nosup")
	ctx := context.Background()

	src := seedRBMemory(t, st, scope, "merge src no superseder")
	// Emit a merged event but do not set superseded_by_id.
	jt, _ := st.Memories().GetJunctions(ctx, scope, src.ID)
	if err := st.Events().Emit(ctx, scope, store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.merged",
		SubjectID: src.ID,
		Payload:   reconcile.MarshalPriorState(src, jt),
		CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if _, err := reconcile.Rollback(ctx, st, scope, src.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	mustStatus(t, st, scope, src.ID, "active")
}

// ─── Rollback conflict guards ─────────────────────────────────────────────────

func TestCoreRollback_ConflictGuards(t *testing.T) {
	t.Run("double_rollback", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-dbl")
		ctx := context.Background()
		mem := seedRBMemory(t, st, scope, "double")
		writeRBUpdatedEvent(t, st, scope, mem.ID)
		if _, err := reconcile.Rollback(ctx, st, scope, mem.ID); err != nil {
			t.Fatalf("first rollback: %v", err)
		}
		_, err := reconcile.Rollback(ctx, st, scope, mem.ID)
		if !errors.Is(err, reconcile.ErrAlreadyRolledBack) {
			t.Errorf("got %v want ErrAlreadyRolledBack", err)
		}
	})

	t.Run("no_prior_state", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-nps")
		mem := seedRBMemory(t, st, scope, "no prior")
		_, err := reconcile.Rollback(context.Background(), st, scope, mem.ID)
		if !errors.Is(err, reconcile.ErrNoPriorState) {
			t.Errorf("got %v want ErrNoPriorState", err)
		}
	})

	t.Run("invalid_prior_state_wrong_id", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-inv")
		mem := seedRBMemory(t, st, scope, "invalid")
		if err := st.Events().Emit(context.Background(), scope, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.updated",
			SubjectID: mem.ID,
			Payload:   `{"id":"wrong","content":"x","status":"active","created_at":1,"updated_at":1}`,
			CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
		_, err := reconcile.Rollback(context.Background(), st, scope, mem.ID)
		if !errors.Is(err, reconcile.ErrInvalidPriorState) {
			t.Errorf("got %v want ErrInvalidPriorState", err)
		}
	})

	t.Run("invalid_prior_state_empty_braces", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-eb")
		mem := seedRBMemory(t, st, scope, "empty braces")
		if err := st.Events().Emit(context.Background(), scope, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.updated",
			SubjectID: mem.ID,
			Payload:   "{}",
			CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
		// "{}" is not a restorable payload → treated as no restorable event found?
		// No: the event type IS restorable, but the payload fails to parse →
		// ErrInvalidPriorState.
		_, err := reconcile.Rollback(context.Background(), st, scope, mem.ID)
		if !errors.Is(err, reconcile.ErrInvalidPriorState) {
			t.Errorf("got %v want ErrInvalidPriorState", err)
		}
	})

	t.Run("invalid_prior_state_malformed", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-mal")
		mem := seedRBMemory(t, st, scope, "malformed")
		if err := st.Events().Emit(context.Background(), scope, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.updated",
			SubjectID: mem.ID,
			Payload:   `{not json`,
			CreatedAt: time.Now().UnixMilli(),
		}); err != nil {
			t.Fatalf("emit: %v", err)
		}
		_, err := reconcile.Rollback(context.Background(), st, scope, mem.ID)
		if !errors.Is(err, reconcile.ErrInvalidPriorState) {
			t.Errorf("got %v want ErrInvalidPriorState", err)
		}
	})

	t.Run("downstream_conflict_superseded", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-ds")
		ctx := context.Background()
		target := seedRBMemory(t, st, scope, "ds target")
		superseder := seedRBMemory(t, st, scope, "ds superseder")
		writeRBSupersededEvent(t, st, scope, target.ID, superseder.ID)
		// Extend the chain: supersede the superseder too.
		if err := st.Memories().SetStatus(ctx, scope, superseder.ID, "superseded", time.Now().UnixMilli()); err != nil {
			t.Fatalf("setstatus: %v", err)
		}
		_, err := reconcile.Rollback(ctx, st, scope, target.ID)
		if !errors.Is(err, reconcile.ErrDownstreamSupersede) {
			t.Errorf("got %v want ErrDownstreamSupersede", err)
		}
	})

	t.Run("downstream_conflict_merged_digest", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-dsm")
		ctx := context.Background()
		src := seedRBMemory(t, st, scope, "dsm src")
		digest := seedRBMemory(t, st, scope, "dsm digest")
		writeRBMergedEvent(t, st, scope, src.ID, digest.ID)
		if err := st.Memories().SetStatus(ctx, scope, digest.ID, "superseded", time.Now().UnixMilli()); err != nil {
			t.Fatalf("setstatus: %v", err)
		}
		_, err := reconcile.Rollback(ctx, st, scope, src.ID)
		if !errors.Is(err, reconcile.ErrDownstreamSupersede) {
			t.Errorf("got %v want ErrDownstreamSupersede (downstream_conflict)", err)
		}
	})

	t.Run("incomplete_snapshots", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-inc")
		ctx := context.Background()
		src1 := seedRBMemory(t, st, scope, "inc src1")
		src2 := seedRBMemory(t, st, scope, "inc src2")
		digest := seedRBMemory(t, st, scope, "inc digest")
		writeRBMergedEvent(t, st, scope, src1.ID, digest.ID)
		// src2 marked superseded WITHOUT a merged event.
		s2 := src2
		s2.Status = "superseded"
		s2.SupersededByID = digest.ID
		s2.UpdatedAt = time.Now().UnixMilli()
		if err := st.Memories().Update(ctx, scope, s2); err != nil {
			t.Fatalf("update: %v", err)
		}
		_, err := reconcile.Rollback(ctx, st, scope, src1.ID)
		if !errors.Is(err, reconcile.ErrIncompleteSnapshots) {
			t.Errorf("got %v want ErrIncompleteSnapshots", err)
		}
	})

	t.Run("not_found", func(t *testing.T) {
		st, cleanup := newTestStore(t)
		defer cleanup()
		scope := tenantScope("t-core-nf")
		_, err := reconcile.Rollback(context.Background(), st, scope, "no-such")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got %v want store.ErrNotFound", err)
		}
	})
}

// ─── Resolve ──────────────────────────────────────────────────────────────────

func seedParked(t *testing.T, st store.Store, scope identity.Scope, content, supersedesID string) store.Memory {
	t.Helper()
	now := time.Now().UnixMilli()
	parked := store.Memory{
		ID:           ulid.Make().String(),
		TenantID:     scope.Tenant,
		Kind:         "fact",
		Content:      content,
		Status:       "pending_confirmation",
		SupersedesID: supersedesID,
		Importance:   3,
		Confidence:   0.8,
		TrustSource:  "llm_extracted",
		Stability:    1.0,
		ContentHash:  ulid.Make().String(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := st.Memories().Insert(context.Background(), scope, parked); err != nil {
		t.Fatalf("seedParked insert: %v", err)
	}
	return parked
}

func TestCoreResolve_Confirm(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-confirm")
	ctx := context.Background()

	target := seedRBMemory(t, st, scope, "confirm target")
	parked := seedParked(t, st, scope, "confirm parked", target.ID)

	res, err := reconcile.Resolve(ctx, st, scope, parked.ID, reconcile.ConfirmActionConfirm)
	if err != nil {
		t.Fatalf("Resolve confirm: %v", err)
	}
	if res.Status != "active" || !res.Invalidate {
		t.Errorf("confirm result: got %+v", res)
	}
	mustStatus(t, st, scope, parked.ID, "active")
	mustStatus(t, st, scope, target.ID, "superseded")

	// The superseded event must carry a prior-state snapshot (reversible).
	evs, err := st.Events().ListBySubject(ctx, scope, target.ID, 20)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	var found bool
	for _, e := range evs {
		if e.Type == "memory.superseded" {
			var p map[string]any
			if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
				t.Fatalf("payload: %v", err)
			}
			if p["id"] != target.ID || p["content"] == nil {
				t.Errorf("superseded payload incomplete: %v", p)
			}
			found = true
		}
	}
	if !found {
		t.Error("no memory.superseded event after confirm")
	}
}

func TestCoreResolve_ConfirmNoTarget(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-confirm-nt")
	ctx := context.Background()

	parked := seedParked(t, st, scope, "confirm no target", "")
	res, err := reconcile.Resolve(ctx, st, scope, parked.ID, reconcile.ConfirmActionConfirm)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Status != "active" {
		t.Errorf("status: %q", res.Status)
	}
	mustStatus(t, st, scope, parked.ID, "active")
}

func TestCoreResolve_Reject(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-reject")
	ctx := context.Background()

	parked := seedParked(t, st, scope, "reject parked", "")
	res, err := reconcile.Resolve(ctx, st, scope, parked.ID, reconcile.ConfirmActionReject)
	if err != nil {
		t.Fatalf("Resolve reject: %v", err)
	}
	if res.Status != "expired" || res.Invalidate {
		t.Errorf("reject result: got %+v", res)
	}
	mustStatus(t, st, scope, parked.ID, "expired")
}

func TestCoreResolve_Errors(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-resolve-err")

	t.Run("not_found", func(t *testing.T) {
		_, err := reconcile.Resolve(context.Background(), st, scope, "no-such", reconcile.ConfirmActionConfirm)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("got %v want store.ErrNotFound", err)
		}
	})
	t.Run("not_parked", func(t *testing.T) {
		active := seedRBMemory(t, st, scope, "active not parked")
		_, err := reconcile.Resolve(context.Background(), st, scope, active.ID, reconcile.ConfirmActionConfirm)
		if !errors.Is(err, reconcile.ErrNotParked) {
			t.Errorf("got %v want ErrNotParked", err)
		}
	})
	t.Run("invalid_action", func(t *testing.T) {
		parked := seedParked(t, st, scope, "invalid action parked", "")
		_, err := reconcile.Resolve(context.Background(), st, scope, parked.ID, reconcile.ConfirmAction("explode"))
		if err == nil || errors.Is(err, reconcile.ErrNotParked) {
			t.Errorf("got %v want a plain invalid-action error", err)
		}
	})
}

// ─── GetMemory ────────────────────────────────────────────────────────────────

func TestCoreGetMemory(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-get")
	ctx := context.Background()

	mem := seedRBMemory(t, st, scope, "get memory")
	view, err := reconcile.GetMemory(ctx, st, scope, mem.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if view.Memory.ID != mem.ID {
		t.Errorf("id: got %q want %q", view.Memory.ID, mem.ID)
	}
	if len(view.Entities) == 0 || len(view.Keywords) == 0 {
		t.Errorf("junctions missing: %+v", view)
	}
}

func TestCoreGetMemory_NotFound(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-get-nf")
	_, err := reconcile.GetMemory(context.Background(), st, scope, "no-such")
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v want store.ErrNotFound", err)
	}
}

func TestCoreGetMemory_Chain(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-get-chain")
	ctx := context.Background()

	grandparent := seedRBMemory(t, st, scope, "gp")
	parent := seedRBMemory(t, st, scope, "p")
	child := seedRBMemory(t, st, scope, "c")

	pm, _ := st.Memories().Get(ctx, scope, parent.ID)
	pm.SupersedesID = grandparent.ID
	pm.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(ctx, scope, *pm); err != nil {
		t.Fatalf("update parent: %v", err)
	}
	cm, _ := st.Memories().Get(ctx, scope, child.ID)
	cm.SupersedesID = parent.ID
	cm.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(ctx, scope, *cm); err != nil {
		t.Fatalf("update child: %v", err)
	}

	view, err := reconcile.GetMemory(ctx, st, scope, child.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if len(view.SupersedesChain) < 2 {
		t.Errorf("chain: got %d want >= 2", len(view.SupersedesChain))
	}
}

func TestCoreGetMemory_DanglingChain(t *testing.T) {
	st, cleanup := newTestStore(t)
	defer cleanup()
	scope := tenantScope("t-core-get-dangle")
	ctx := context.Background()

	mem := seedRBMemory(t, st, scope, "dangle")
	mm, _ := st.Memories().Get(ctx, scope, mem.ID)
	mm.SupersedesID = "ghost"
	mm.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(ctx, scope, *mm); err != nil {
		t.Fatalf("update: %v", err)
	}
	view, err := reconcile.GetMemory(ctx, st, scope, mem.ID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if len(view.SupersedesChain) != 1 {
		t.Errorf("dangling chain len: got %d want 1", len(view.SupersedesChain))
	}
}

// ─── prior-state golden (byte-identity of the inverse, AC-2) ──────────────────

// TestMarshalPriorState_Golden pins the exact prior-state JSON shape so the
// rolled_back / superseded event payloads stay byte-identical to today.
func TestMarshalPriorState_Golden(t *testing.T) {
	m := store.Memory{
		ID:          "mem-golden",
		Kind:        "fact",
		Content:     "the sky is blue",
		Context:     "weather",
		Status:      "active",
		Importance:  4,
		Confidence:  0.91,
		TrustSource: "llm_extracted",
		MatchCount:  2,
		InjectCount: 1,
		UseCount:    3,
		SaveCount:   1,
		FailCount:   0,
		NoiseCount:  0,
		Stability:   1.5,
		ValidFrom:   100,
		ValidUntil:  0,
		EpisodeID:   "ep-1",
		PrivacyZone: "work",
		ContentHash: "hash-1",
		CreatedAt:   1000,
		UpdatedAt:   2000,
	}
	jt := store.MemoryJunctions{
		Entities: []string{"sky"},
		Keywords: []string{"blue", "color"},
		Queries:  []string{"what color is the sky"},
		Provenance: []store.Provenance{
			{RecordID: "rec-1", SpanStart: 5, SpanEnd: 14},
		},
	}
	got := reconcile.MarshalPriorState(m, jt)
	const want = `{"id":"mem-golden","kind":"fact","content":"the sky is blue","context":"weather","status":"active","importance":4,"confidence":0.91,"trust_source":"llm_extracted","match_count":2,"inject_count":1,"use_count":3,"save_count":1,"stability":1.5,"valid_from":100,"episode_id":"ep-1","privacy_zone":"work","content_hash":"hash-1","created_at":1000,"updated_at":2000,"entities":["sky"],"keywords":["blue","color"],"queries":["what color is the sky"],"provenance":[{"record_id":"rec-1","span_start":5,"span_end":14}]}`
	if got != want {
		t.Errorf("prior-state JSON drift:\n got: %s\nwant: %s", got, want)
	}
}
