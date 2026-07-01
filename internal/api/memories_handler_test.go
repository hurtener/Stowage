package api_test

// memories_handler_test.go — Phase 18 acceptance criteria for
// POST /v1/memories/{id}/rollback and PATCH /v1/memories/{id} (D-064, D-065).
//
// AC-1: Round-trip per op type (updated, superseded, merged).
// AC-2: Multi-source merge rollback restores ALL sources and tombstones digest.
// AC-3: Conflict guards: double-rollback 409, downstream conflict 409,
//        no prior state 409, incomplete snapshots 409.
// AC-4: Restored memory is retrievable (cache invalidated).
// AC-5: Composition: park → confirm-sweep → rollback (reversibility of auto-resolution).
// AC-7: PATCH confirm/reject; non-parked → 409; bad action → 400.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
)

// ─── helpers ────────────────────────────────────────────────────────────────

// seedMemory inserts a new active memory with junctions + provenance and
// returns it. The provenance references a real record row to satisfy the FK
// constraint (records.id must exist before provenance can reference it).
func seedMemory(t *testing.T, st store.Store, scope identity.Scope, content string) store.Memory {
	t.Helper()
	now := time.Now().UnixMilli()

	// Insert a record row first so the provenance FK is satisfied.
	rec := store.Record{
		ID:         ulid.Make().String(),
		TenantID:   scope.Tenant,
		Role:       "user",
		Content:    "record for " + content,
		CreatedAt:  now,
		OccurredAt: now,
	}
	if err := st.Records().Append(context.Background(), scope, []store.Record{rec}); err != nil {
		t.Fatalf("seedMemory append record: %v", err)
	}

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
		Provenance: []store.Provenance{{
			ID:        ulid.Make().String(),
			MemoryID:  mem.ID,
			RecordID:  rec.ID,
			SpanStart: 0,
			SpanEnd:   10,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		}},
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
		t.Fatalf("seedMemory commit: %v", err)
	}
	// Return the committed memory (re-fetch to get exact DB state).
	got, err := st.Memories().Get(context.Background(), scope, mem.ID)
	if err != nil {
		t.Fatalf("seedMemory get: %v", err)
	}
	return *got
}

// writeUpdatedEvent writes a memory.updated event with a full prior-state
// payload for memID, simulating what the reconciler does before an update.
func writeUpdatedEvent(t *testing.T, st store.Store, scope identity.Scope, memID string) {
	t.Helper()
	mem, err := st.Memories().Get(context.Background(), scope, memID)
	if err != nil {
		t.Fatalf("writeUpdatedEvent Get: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, memID)
	payload := reconcile.MarshalPriorState(*mem, jt)
	now := time.Now().UnixMilli()
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: memID,
		Reason:    "test: simulate update",
		Payload:   payload,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("writeUpdatedEvent Emit: %v", err)
	}
}

// writeSupersededEvent writes a memory.superseded event for targetID with a
// full prior-state payload, then marks targetID as superseded in the DB.
func writeSupersededEvent(t *testing.T, st store.Store, scope identity.Scope, targetID, supersederID string) {
	t.Helper()
	target, err := st.Memories().Get(context.Background(), scope, targetID)
	if err != nil {
		t.Fatalf("writeSupersededEvent Get target: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, targetID)
	payload := reconcile.MarshalPriorState(*target, jt)
	now := time.Now().UnixMilli()

	// Emit the prior-state event.
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.superseded",
		SubjectID: targetID,
		Reason:    "test: simulate supersede",
		Payload:   payload,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("writeSupersededEvent Emit: %v", err)
	}

	// Update target status in DB to reflect the supersede.
	if err := st.Memories().SetStatus(context.Background(), scope, targetID, "superseded", now); err != nil {
		t.Fatalf("writeSupersededEvent SetStatus: %v", err)
	}
	// Set superseded_by_id on the target.
	targetMem := *target
	targetMem.Status = "superseded"
	targetMem.SupersededByID = supersederID
	targetMem.UpdatedAt = now
	if err := st.Memories().Update(context.Background(), scope, targetMem); err != nil {
		t.Fatalf("writeSupersededEvent Update: %v", err)
	}
}

// writeMergedEvent writes a memory.merged event for sibID with a full
// prior-state payload, then marks sibID as superseded in the DB.
func writeMergedEvent(t *testing.T, st store.Store, scope identity.Scope, sibID, digestID string) {
	t.Helper()
	sib, err := st.Memories().Get(context.Background(), scope, sibID)
	if err != nil {
		t.Fatalf("writeMergedEvent Get sib: %v", err)
	}
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, sibID)
	payload := reconcile.MarshalPriorState(*sib, jt)
	now := time.Now().UnixMilli()

	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.merged",
		SubjectID: sibID,
		Reason:    "test: simulate merge",
		Payload:   payload,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("writeMergedEvent Emit: %v", err)
	}

	// Mark sibling as superseded with superseded_by_id = digestID.
	sibMem := *sib
	sibMem.Status = "superseded"
	sibMem.SupersededByID = digestID
	sibMem.UpdatedAt = now
	if err := st.Memories().Update(context.Background(), scope, sibMem); err != nil {
		t.Fatalf("writeMergedEvent Update: %v", err)
	}
}

// postRollback calls POST /v1/memories/{id}/rollback and returns the response.
func postRollback(t *testing.T, tsURL, authHeader, id string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s/v1/memories/%s/rollback", tsURL, id), nil)
	req.Header.Set("Authorization", authHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST rollback: %v", err)
	}
	return resp
}

// patchMemory calls PATCH /v1/memories/{id} with the given action body.
func patchMemory(t *testing.T, tsURL, authHeader, id, action string) *http.Response {
	t.Helper()
	body := jsonBody(t, map[string]string{"action": action})
	req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/v1/memories/%s", tsURL, id), body)
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH memory: %v", err)
	}
	return resp
}

// getMemoryHTTP fetches a memory via GET /v1/memories/{id}.
func getMemoryHTTP(t *testing.T, tsURL, authHeader, id string) (int, map[string]interface{}) {
	t.Helper()
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/v1/memories/%s", tsURL, id), nil)
	req.Header.Set("Authorization", authHeader)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET memory: %v", err)
	}
	defer drainClose(resp.Body)
	var body map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// assertMemoryStatus fetches a memory from the store and asserts status.
func assertMemoryStatus(t *testing.T, st store.Store, scope identity.Scope, id, wantStatus string) {
	t.Helper()
	mem, err := st.Memories().Get(context.Background(), scope, id)
	if err != nil {
		t.Fatalf("assertMemoryStatus Get %q: %v", id, err)
	}
	if mem.Status != wantStatus {
		t.Errorf("memory %q status: got %q want %q", id, mem.Status, wantStatus)
	}
}

// ─── AC-1: round-trip per op type ──────────────────────────────────────────

// TestRollback_UpdatedRoundTrip seeds an active memory, simulates an
// memory.updated event (as the reconciler would emit), then calls rollback and
// asserts the memory is restored to its pre-update golden state.
func TestRollback_UpdatedRoundTrip(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-upd")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-upd"}

	// Seed active memory with junctions.
	mem := seedMemory(t, st, scope, "original content updated-rt")
	goldenJT, _ := st.Memories().GetJunctions(context.Background(), scope, mem.ID)

	// Simulate an update: write updated event with prior-state payload.
	writeUpdatedEvent(t, st, scope, mem.ID)

	// Mutate the memory content (simulating the post-update state).
	updated := mem
	updated.Content = "content after update"
	updated.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, updated); err != nil {
		t.Fatalf("update memory: %v", err)
	}

	// Call rollback.
	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d want 200", resp.StatusCode)
	}

	// Verify restored state matches golden.
	restored, err := st.Memories().Get(context.Background(), scope, mem.ID)
	if err != nil {
		t.Fatalf("Get restored: %v", err)
	}
	if restored.Content != mem.Content {
		t.Errorf("content: got %q want %q", restored.Content, mem.Content)
	}
	if restored.Status != "active" {
		t.Errorf("status: got %q want active", restored.Status)
	}

	// Verify junctions restored.
	jt, _ := st.Memories().GetJunctions(context.Background(), scope, mem.ID)
	if len(jt.Entities) == 0 || jt.Entities[0] != goldenJT.Entities[0] {
		t.Errorf("entities: got %v want %v", jt.Entities, goldenJT.Entities)
	}
}

// TestRollback_SupersededRoundTrip seeds an active target, creates a superseder,
// writes the memory.superseded event for the target, then rolls back and asserts
// target is restored (active) and superseder is tombstoned (deleted).
func TestRollback_SupersededRoundTrip(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-sup")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-sup"}

	// Seed target memory.
	target := seedMemory(t, st, scope, "target to be superseded")

	// Seed superseder memory.
	superseder := seedMemory(t, st, scope, "superseder content")

	// Write memory.superseded event for the target (simulating reconciler).
	writeSupersededEvent(t, st, scope, target.ID, superseder.ID)

	// Call rollback on the target.
	resp := postRollback(t, ts.URL, auth, target.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d want 200", resp.StatusCode)
	}

	// Target should be restored to active.
	assertMemoryStatus(t, st, scope, target.ID, "active")

	// Superseder should be tombstoned (deleted).
	assertMemoryStatus(t, st, scope, superseder.ID, "deleted")
}

// TestRollback_MergedRoundTrip seeds two source memories, creates a digest,
// writes memory.merged events for both sources, then rolls back one source and
// asserts both sources are restored (active) and the digest is tombstoned.
func TestRollback_MergedRoundTrip(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-mrg")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-mrg"}

	// Seed two source memories.
	src1 := seedMemory(t, st, scope, "source 1 merged")
	src2 := seedMemory(t, st, scope, "source 2 merged")

	// Seed digest memory.
	digest := seedMemory(t, st, scope, "merged digest")

	// Write memory.merged events for both sources.
	writeMergedEvent(t, st, scope, src1.ID, digest.ID)
	writeMergedEvent(t, st, scope, src2.ID, digest.ID)

	// Roll back from src1 (rollback of any one source undoes the whole merge).
	resp := postRollback(t, ts.URL, auth, src1.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback src1: got %d want 200", resp.StatusCode)
	}

	// Both sources should be restored to active.
	assertMemoryStatus(t, st, scope, src1.ID, "active")
	assertMemoryStatus(t, st, scope, src2.ID, "active")

	// Digest should be tombstoned (deleted).
	assertMemoryStatus(t, st, scope, digest.ID, "deleted")
}

// ─── AC-2: 3-source merge rollback ──────────────────────────────────────────

// TestRollback_MergeThreeSources seeds 3 source memories, creates a digest,
// writes memory.merged events for all 3, then rolls back from one source
// and asserts all 3 are restored and the digest is tombstoned.
func TestRollback_MergeThreeSources(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-m3")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-m3"}

	src1 := seedMemory(t, st, scope, "source-A")
	src2 := seedMemory(t, st, scope, "source-B")
	src3 := seedMemory(t, st, scope, "source-C")
	digest := seedMemory(t, st, scope, "three-way digest")

	writeMergedEvent(t, st, scope, src1.ID, digest.ID)
	writeMergedEvent(t, st, scope, src2.ID, digest.ID)
	writeMergedEvent(t, st, scope, src3.ID, digest.ID)

	// Rollback from src1 — must restore all 3 and tombstone digest.
	resp := postRollback(t, ts.URL, auth, src1.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d want 200", resp.StatusCode)
	}

	for _, id := range []string{src1.ID, src2.ID, src3.ID} {
		assertMemoryStatus(t, st, scope, id, "active")
	}
	assertMemoryStatus(t, st, scope, digest.ID, "deleted")
}

// ─── AC-3: conflict guards ──────────────────────────────────────────────────

// TestRollback_DoubleRollback verifies that calling rollback twice returns
// 409 already_rolled_back on the second call.
func TestRollback_DoubleRollback(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-dbl")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-dbl"}

	mem := seedMemory(t, st, scope, "double rollback target")
	writeUpdatedEvent(t, st, scope, mem.ID)

	// First rollback — should succeed.
	r1 := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(r1.Body)
	if r1.StatusCode != http.StatusOK {
		t.Fatalf("first rollback: got %d want 200", r1.StatusCode)
	}

	// Second rollback — should 409 already_rolled_back.
	r2 := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(r2.Body)
	if r2.StatusCode != http.StatusConflict {
		t.Errorf("double rollback: got %d want 409", r2.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(r2.Body).Decode(&body)
	if body["code"] != "already_rolled_back" {
		t.Errorf("code: got %q want already_rolled_back", body["code"])
	}
}

// TestRollback_DownstreamConflict verifies that if a superseder has itself
// been superseded (chain extended downstream), rollback returns
// 409 downstream_conflict.
func TestRollback_DownstreamConflict(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-ds")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-ds"}

	target := seedMemory(t, st, scope, "downstream target")
	superseder := seedMemory(t, st, scope, "downstream superseder")
	writeSupersededEvent(t, st, scope, target.ID, superseder.ID)

	// Now supersede the superseder as well (extend chain).
	if err := st.Memories().SetStatus(context.Background(), scope, superseder.ID, "superseded", time.Now().UnixMilli()); err != nil {
		t.Fatalf("SetStatus superseder: %v", err)
	}
	supSup := superseder
	supSup.Status = "superseded"
	supSup.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, supSup); err != nil {
		t.Fatalf("Update superseder: %v", err)
	}

	resp := postRollback(t, ts.URL, auth, target.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("downstream conflict: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "downstream_conflict" {
		t.Errorf("code: got %q want downstream_conflict", body["code"])
	}
}

// TestRollback_NoPriorState verifies that rollback of a memory with no
// restorable event returns 409 no_prior_state.
func TestRollback_NoPriorState(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-nps")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-nps"}

	// Seed a memory with only a memory.added event (no restorable event).
	mem := seedMemory(t, st, scope, "no prior state memory")

	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("no prior state: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "no_prior_state" {
		t.Errorf("code: got %q want no_prior_state, got %q", body["code"], body["code"])
	}
}

// TestRollback_IncompleteSnapshots verifies that merge rollback returns
// 409 incomplete_snapshots when a sibling lacks a snapshot.
func TestRollback_IncompleteSnapshots(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-inc")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-inc"}

	src1 := seedMemory(t, st, scope, "source with snapshot")
	src2 := seedMemory(t, st, scope, "source WITHOUT snapshot")
	digest := seedMemory(t, st, scope, "incomplete merge digest")

	// Write merged event only for src1, not src2.
	writeMergedEvent(t, st, scope, src1.ID, digest.ID)
	// Mark src2 as superseded without writing a memory.merged event.
	if err := st.Memories().SetStatus(context.Background(), scope, src2.ID, "superseded", time.Now().UnixMilli()); err != nil {
		t.Fatalf("SetStatus src2: %v", err)
	}
	src2Mem := src2
	src2Mem.Status = "superseded"
	src2Mem.SupersededByID = digest.ID
	src2Mem.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, src2Mem); err != nil {
		t.Fatalf("Update src2: %v", err)
	}

	resp := postRollback(t, ts.URL, auth, src1.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("incomplete snapshots: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "incomplete_snapshots" {
		t.Errorf("code: got %q want incomplete_snapshots", body["code"])
	}
}

// TestRollback_NotFound verifies that rollback of a non-existent memory
// returns 404.
func TestRollback_NotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-404")
	auth := bearerHeader(pt)

	resp := postRollback(t, ts.URL, auth, "no-such-id")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("not found: got %d want 404", resp.StatusCode)
	}
}

// ─── AC-4: retrievability after rollback ────────────────────────────────────

// TestRollback_CacheInvalidated verifies that after rollback the restored
// memory is retrievable via GET /v1/memories/{id} (cache invalidated).
func TestRollback_CacheInvalidated(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-cache")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-cache"}

	mem := seedMemory(t, st, scope, "cache test content")
	writeUpdatedEvent(t, st, scope, mem.ID)

	// Modify content.
	updated := mem
	updated.Content = "modified content"
	updated.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, updated); err != nil {
		t.Fatalf("update: %v", err)
	}

	// Rollback.
	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d want 200", resp.StatusCode)
	}

	// After rollback, GET should return the original content.
	status, body := getMemoryHTTP(t, ts.URL, auth, mem.ID)
	if status != http.StatusOK {
		t.Fatalf("GET after rollback: got %d want 200", status)
	}
	memBody, _ := body["memory"].(map[string]interface{})
	if memBody == nil {
		t.Fatal("GET response missing memory field")
	}
	if memBody["content"] != mem.Content {
		t.Errorf("content after rollback: got %v want %q", memBody["content"], mem.Content)
	}
}

// ─── AC-5: reversibility composition ────────────────────────────────────────

// TestRollback_CompositionParkConfirmRollback parks a memory, runs the
// confirm sweep (which supersedes the target), then rolls back the target to
// verify the whole auto-resolution is reversible.
func TestRollback_CompositionParkConfirmRollback(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-comp")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-comp"}

	// Seed the target (will be superseded on confirm promotion).
	target := seedMemory(t, st, scope, "composition target")

	// Seed a parked memory (pending_confirmation) pointing at the target.
	now := time.Now().UnixMilli()
	parked := store.Memory{
		ID:           ulid.Make().String(),
		TenantID:     scope.Tenant,
		Kind:         "fact",
		Content:      "composition parked",
		Status:       "pending_confirmation",
		SupersedesID: target.ID,
		Importance:   3,
		Confidence:   0.8,
		TrustSource:  "llm_extracted",
		Stability:    1.0,
		ContentHash:  ulid.Make().String(),
		// Make it old enough for TTL.
		CreatedAt: time.Now().Add(-5 * time.Minute).UnixMilli(),
		UpdatedAt: now,
	}
	if err := st.Memories().Insert(context.Background(), scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}

	// Run the confirm sweep (very short TTL so the parked memory is eligible).
	ingest := make(chan pipeline.Item, 8)
	profile := lifecycle.Profile{
		ConfirmTTL:       2 * time.Minute, // parked is 5 min old
		ConfirmRepeats:   100,
		ConfirmInterval:  24 * time.Hour,
		ConfirmBatchSize: 50,
	}
	mgr := lifecycle.New(st, memHandlerTestLogger(), profile, ingest)
	mgr.RunForce(context.Background())

	// Verify promotion: parked → active, target → superseded.
	promoted, _ := st.Memories().Get(context.Background(), scope, parked.ID)
	if promoted.Status != "active" {
		t.Fatalf("parked not promoted: status %q", promoted.Status)
	}
	assertMemoryStatus(t, st, scope, target.ID, "superseded")

	// Now roll back the target (which was superseded by the sweep).
	resp := postRollback(t, ts.URL, auth, target.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback target: got %d want 200", resp.StatusCode)
	}

	// Target should be restored to active.
	assertMemoryStatus(t, st, scope, target.ID, "active")
	// Promoted row should be tombstoned.
	assertMemoryStatus(t, st, scope, parked.ID, "deleted")
}

// ─── AC-7: PATCH confirm / reject ────────────────────────────────────────────

// TestPatch_Confirm verifies that PATCH /v1/memories/{id} with action=confirm
// promotes a parked memory to active and supersedes its target. Also verifies
// the memory.superseded event carries a prior-state snapshot (D-065).
func TestPatch_Confirm(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-c")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-patch-c"}

	target := seedMemory(t, st, scope, "target to supersede on confirm")

	// Seed parked memory with junctions and provenance.
	now := time.Now().UnixMilli()
	parked := store.Memory{
		ID:           ulid.Make().String(),
		TenantID:     scope.Tenant,
		Kind:         "fact",
		Content:      "new fact from confirm",
		Status:       "pending_confirmation",
		SupersedesID: target.ID,
		Importance:   4,
		Confidence:   0.9,
		TrustSource:  "llm_extracted",
		Stability:    1.0,
		ContentHash:  ulid.Make().String(),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := st.Memories().Insert(context.Background(), scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}

	resp := patchMemory(t, ts.URL, auth, parked.ID, "confirm")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH confirm: got %d want 200", resp.StatusCode)
	}

	// Parked should now be active.
	assertMemoryStatus(t, st, scope, parked.ID, "active")
	// Target should be superseded.
	assertMemoryStatus(t, st, scope, target.ID, "superseded")

	// The memory.superseded event for the target must carry a prior-state snapshot.
	events, err := st.Events().ListBySubject(context.Background(), scope, target.ID, 20)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	var supersededEvent *store.Event
	for i := range events {
		if events[i].Type == "memory.superseded" {
			supersededEvent = &events[i]
			break
		}
	}
	if supersededEvent == nil {
		t.Fatal("no memory.superseded event found for target after confirm")
		return // unreachable; makes non-nil provable to staticcheck (SA5011)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(supersededEvent.Payload), &payload); err != nil {
		t.Fatalf("superseded event payload: %v", err)
	}
	if payload["id"] != target.ID {
		t.Errorf("superseded event payload id: got %v want %q", payload["id"], target.ID)
	}
	// content field must be present (prior-state snapshot complete).
	if payload["content"] == "" || payload["content"] == nil {
		t.Errorf("superseded event payload missing content field")
	}
}

// TestPatch_Reject verifies that PATCH /v1/memories/{id} with action=reject
// expires a parked memory (status → 'expired', per D-065).
func TestPatch_Reject(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-r")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-patch-r"}

	now := time.Now().UnixMilli()
	parked := store.Memory{
		ID:          ulid.Make().String(),
		TenantID:    scope.Tenant,
		Kind:        "fact",
		Content:     "parked to reject",
		Status:      "pending_confirmation",
		Importance:  3,
		Confidence:  0.8,
		TrustSource: "llm_extracted",
		Stability:   1.0,
		ContentHash: ulid.Make().String(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := st.Memories().Insert(context.Background(), scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}

	resp := patchMemory(t, ts.URL, auth, parked.ID, "reject")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH reject: got %d want 200", resp.StatusCode)
	}

	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "expired" {
		t.Errorf("reject response status: got %q want expired", body["status"])
	}

	// Verify status in store.
	assertMemoryStatus(t, st, scope, parked.ID, "expired")
}

// TestPatch_NonParked verifies that PATCH on a non-parked memory returns
// 409 not_parked.
func TestPatch_NonParked(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-np")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-patch-np"}

	active := seedMemory(t, st, scope, "active, not parked")

	resp := patchMemory(t, ts.URL, auth, active.ID, "confirm")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("non-parked: got %d want 409", resp.StatusCode)
	}
}

// TestPatch_BadAction verifies that PATCH with an unknown action returns 400.
func TestPatch_BadAction(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-ba")
	auth := bearerHeader(pt)

	resp := patchMemory(t, ts.URL, auth, "some-id", "explode")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad action: got %d want 400", resp.StatusCode)
	}
}

// TestPatch_NotFound verifies that PATCH on a non-existent memory returns 404.
func TestPatch_NotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-nf")
	auth := bearerHeader(pt)

	resp := patchMemory(t, ts.URL, auth, "no-such-id", "confirm")
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("not found: got %d want 404", resp.StatusCode)
	}
}

// TestGetMemory_JunctionsProvenance verifies GET /v1/memories/{id} returns
// memory, junctions, provenance, and supersedes chain (AC-7).
func TestGetMemory_JunctionsProvenance(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-get-mem")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-get-mem"}

	mem := seedMemory(t, st, scope, "junction test content")

	status, body := getMemoryHTTP(t, ts.URL, auth, mem.ID)
	if status != http.StatusOK {
		t.Fatalf("GET: got %d want 200", status)
	}

	memBody, _ := body["memory"].(map[string]interface{})
	if memBody == nil {
		t.Fatal("GET response missing memory field")
	}
	if memBody["id"] != mem.ID {
		t.Errorf("id: got %v want %q", memBody["id"], mem.ID)
	}

	// entities and keywords should be present (seeded in seedMemory).
	entities, _ := body["entities"].([]interface{})
	if len(entities) == 0 {
		t.Error("entities should not be empty")
	}
	keywords, _ := body["keywords"].([]interface{})
	if len(keywords) == 0 {
		t.Error("keywords should not be empty")
	}
	provenance, _ := body["provenance"].([]interface{})
	if len(provenance) == 0 {
		t.Error("provenance should not be empty")
	}
}

// TestGetMemory_NotFound verifies GET /v1/memories/{id} returns 404 for
// unknown IDs.
func TestGetMemory_NotFound(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-get-404")
	auth := bearerHeader(pt)

	status, _ := getMemoryHTTP(t, ts.URL, auth, "no-such-id")
	if status != http.StatusNotFound {
		t.Errorf("not found: got %d want 404", status)
	}
}

// TestRollback_RolledBackEventPayload verifies that after rollback the
// memory.rolled_back event carries a prior-state snapshot of the pre-rollback
// memory (the audit-trail completeness requirement from D-064).
func TestRollback_RolledBackEventPayload(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-evpl")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-evpl"}

	mem := seedMemory(t, st, scope, "event payload test")
	writeUpdatedEvent(t, st, scope, mem.ID)

	// Change content to something new.
	preRollbackContent := "pre-rollback content"
	updated := mem
	updated.Content = preRollbackContent
	updated.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, updated); err != nil {
		t.Fatalf("update: %v", err)
	}

	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rollback: got %d want 200", resp.StatusCode)
	}

	// The memory.rolled_back event should carry the pre-rollback state.
	events, err := st.Events().ListBySubject(context.Background(), scope, mem.ID, 50)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	var rolledBackEvent *store.Event
	for i := range events {
		if events[i].Type == "memory.rolled_back" {
			rolledBackEvent = &events[i]
			break
		}
	}
	if rolledBackEvent == nil {
		t.Fatal("no memory.rolled_back event found after rollback")
		return // unreachable; makes non-nil provable to staticcheck (SA5011)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(rolledBackEvent.Payload), &payload); err != nil {
		t.Fatalf("rolled_back event payload: %v", err)
	}
	if payload["id"] != mem.ID {
		t.Errorf("payload id: got %v want %q", payload["id"], mem.ID)
	}
}

// memHandlerTestLogger returns a slog.Logger that discards all output for tests.
func memHandlerTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestGetMemory_SupersedesChain verifies walkSupersedesChain by creating a
// two-level supersedes chain and checking the response includes both ancestors.
func TestGetMemory_SupersedesChain(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-get-chain")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-get-chain"}

	// grandparent ← parent ← child (child supersedes parent, parent supersedes grandparent).
	grandparent := seedMemory(t, st, scope, "grandparent content")
	parent := seedMemory(t, st, scope, "parent content")
	child := seedMemory(t, st, scope, "child content")

	// Wire supersedes links: parent supersedes grandparent, child supersedes parent.
	pMem, _ := st.Memories().Get(context.Background(), scope, parent.ID)
	pMem.SupersedesID = grandparent.ID
	pMem.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, *pMem); err != nil {
		t.Fatalf("update parent supersedes: %v", err)
	}
	cMem, _ := st.Memories().Get(context.Background(), scope, child.ID)
	cMem.SupersedesID = parent.ID
	cMem.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, *cMem); err != nil {
		t.Fatalf("update child supersedes: %v", err)
	}

	status, body := getMemoryHTTP(t, ts.URL, auth, child.ID)
	if status != http.StatusOK {
		t.Fatalf("GET child: got %d want 200", status)
	}
	chain, _ := body["supersedes_chain"].([]interface{})
	if len(chain) < 2 {
		t.Errorf("supersedes_chain: got %d entries want >= 2", len(chain))
	}
}

// TestPatch_WrongContentType verifies that PATCH with wrong content-type
// returns 415.
func TestPatch_WrongContentType(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-wct")
	auth := bearerHeader(pt)

	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/memories/some-id",
		strings.NewReader(`{"action":"confirm"}`))
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH wrong content-type: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content-type: got %d want 415", resp.StatusCode)
	}
}

// TestPatch_MalformedJSON verifies that PATCH with bad JSON returns 400.
func TestPatch_MalformedJSON(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-patch-mjson")
	auth := bearerHeader(pt)

	req, _ := http.NewRequest("PATCH", ts.URL+"/v1/memories/some-id",
		strings.NewReader(`{not json`))
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH bad json: %v", err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad json: got %d want 400", resp.StatusCode)
	}
}

// TestRollback_InvalidPayload verifies that a memory.updated event with a
// payload whose ID doesn't match the memory ID returns 409 invalid_prior_state.
func TestRollback_InvalidPayload(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-inv")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-inv"}

	mem := seedMemory(t, st, scope, "invalid payload test")

	// Write a memory.updated event with a payload whose ID doesn't match.
	now := time.Now().UnixMilli()
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: mem.ID,
		Reason:    "test: wrong id in payload",
		Payload:   `{"id":"wrong-id","content":"something","status":"active","created_at":1,"updated_at":1}`,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}

	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("invalid payload: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "invalid_prior_state" {
		t.Errorf("code: got %q want invalid_prior_state", body["code"])
	}
}

// TestRollback_EmptyBracesPayload verifies that a memory.updated event with
// payload "{}" (no prior state) returns 409 invalid_prior_state.
// Covers parsePriorState "{}" fast-path return.
func TestRollback_EmptyBracesPayload(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-ebp")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-ebp"}

	mem := seedMemory(t, st, scope, "empty braces payload test")
	now := time.Now().UnixMilli()
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: mem.ID,
		Reason:    "test: empty braces payload",
		Payload:   "{}",
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("empty braces: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "invalid_prior_state" {
		t.Errorf("code: got %q want invalid_prior_state", body["code"])
	}
}

// TestRollback_MalformedJSONPayload verifies that a memory.updated event with
// malformed JSON payload returns 409 invalid_prior_state.
// Covers parsePriorState json.Unmarshal error path.
func TestRollback_MalformedJSONPayload(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-mjp")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-mjp"}

	mem := seedMemory(t, st, scope, "malformed json payload test")
	now := time.Now().UnixMilli()
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: mem.ID,
		Reason:    "test: malformed json payload",
		Payload:   `{not valid json`,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("malformed json: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "invalid_prior_state" {
		t.Errorf("code: got %q want invalid_prior_state", body["code"])
	}
}

// TestRollback_MissingContentPayload verifies that a memory.updated event
// with valid JSON but an empty content field returns 409 invalid_prior_state.
// Covers parsePriorState p.Content == "" path.
func TestRollback_MissingContentPayload(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-rb-mcp")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-rb-mcp"}

	mem := seedMemory(t, st, scope, "missing content payload test")
	now := time.Now().UnixMilli()
	// Valid JSON, has id matching the memory, but no content field.
	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "memory.updated",
		SubjectID: mem.ID,
		Reason:    "test: missing content",
		Payload:   `{"id":"` + mem.ID + `","status":"active"}`,
		CreatedAt: now,
	}
	if err := st.Events().Emit(context.Background(), scope, ev); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	resp := postRollback(t, ts.URL, auth, mem.ID)
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("missing content: got %d want 409", resp.StatusCode)
	}
	var body map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != "invalid_prior_state" {
		t.Errorf("code: got %q want invalid_prior_state", body["code"])
	}
}

// TestGetMemory_DanglingSupersedes verifies that walkSupersedesChain stops
// gracefully when a SupersedesID points to a non-existent memory.
// Covers the break-on-Get-error path in walkSupersedesChain.
func TestGetMemory_DanglingSupersedes(t *testing.T) {
	t.Parallel()
	_, ts, st := newTestServer(t)
	_, pt := mustCreateAgentKey(t, st, "t-get-dangle")
	auth := bearerHeader(pt)
	scope := identity.Scope{Tenant: "t-get-dangle"}

	mem := seedMemory(t, st, scope, "dangling supersedes test")

	// Set SupersedesID to a non-existent memory ID.
	mMem, _ := st.Memories().Get(context.Background(), scope, mem.ID)
	mMem.SupersedesID = "non-existent-memory-id"
	mMem.UpdatedAt = time.Now().UnixMilli()
	if err := st.Memories().Update(context.Background(), scope, *mMem); err != nil {
		t.Fatalf("update supersedes: %v", err)
	}

	status, body := getMemoryHTTP(t, ts.URL, auth, mem.ID)
	if status != http.StatusOK {
		t.Fatalf("GET: got %d want 200", status)
	}
	// Chain walk hits non-existent ancestor → break; chain length should be 1
	// (just the non-existent ID before the break).
	chain, _ := body["supersedes_chain"].([]interface{})
	if len(chain) != 1 {
		t.Errorf("supersedes_chain len: got %d want 1", len(chain))
	}
}
