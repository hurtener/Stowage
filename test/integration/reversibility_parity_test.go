// reversibility_parity_test.go proves the Phase h3 (D-070) both-paths-identical
// bar: reconciliation rollback drives to the SAME restored memory and the SAME
// emitted events whether invoked through the embedded SDK or the HTTP server,
// both over real sqlite. It also covers a conflict path — a double rollback
// returns the typed conflict (SDK) / 409 (HTTP) on both surfaces.
//
// The supersede state is seeded deterministically (the exact shape a reconcile
// supersede leaves: a memory.superseded prior-state event + status/link on the
// target). Fixed IDs/timestamps/hashes make the two independent stores produce
// byte-identical restored memories and rolled_back payloads. Runs under -race.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// Deterministic seed constants — identical in both stores so the restored
// memory + rolled_back payload are byte-identical across surfaces.
const (
	revTargetID     = "rev-parity-target-0000000001"
	revSupersederID = "rev-parity-superseder-000001"
	revTargetHash   = "rev-parity-target-hash"
	revSupHash      = "rev-parity-superseder-hash"
	revContent      = "the project kickoff was in Q1 2024"
	revCreatedAt    = int64(1_700_000_000_000)
	revSupersededAt = int64(1_700_000_500_000)
)

// rollbackOutcome captures what each surface produced for cross-comparison.
type rollbackOutcome struct {
	restored          store.Memory // normalized (tenant + updated_at zeroed)
	rolledBackPayload string
	supersederStatus  string
	doubleConflict    bool
}

// seedSupersededScenario writes the deterministic superseded state into st.
func seedSupersededScenario(t *testing.T, st store.Store, scope identity.Scope) {
	t.Helper()
	ctx := context.Background()

	target := store.Memory{
		ID: revTargetID, TenantID: scope.Tenant, Kind: "fact", Content: revContent,
		Status: "active", Importance: 4, Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: revTargetHash, CreatedAt: revCreatedAt, UpdatedAt: revCreatedAt,
	}
	if err := st.Memories().Commit(ctx, scope, store.CommitSet{
		Action: store.ActionAdd, Memory: target,
		Entities: []string{"project"}, Keywords: []string{"kickoff", "q1"},
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: target.ID, Payload: "{}", CreatedAt: revCreatedAt}},
		Scope:  scope,
	}); err != nil {
		t.Fatalf("seed target: %v", err)
	}

	superseder := store.Memory{
		ID: revSupersederID, TenantID: scope.Tenant, Kind: "fact", Content: "kickoff moved to Q2 2024",
		Status: "active", Importance: 4, Confidence: 0.9, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: revSupHash, CreatedAt: revCreatedAt, UpdatedAt: revCreatedAt,
	}
	if err := st.Memories().Commit(ctx, scope, store.CommitSet{
		Action: store.ActionAdd, Memory: superseder,
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: superseder.ID, Payload: "{}", CreatedAt: revCreatedAt}},
		Scope:  scope,
	}); err != nil {
		t.Fatalf("seed superseder: %v", err)
	}

	// Emit the prior-state supersede event (what the reconciler writes), then
	// transition the target to superseded — fixed timestamp for payload parity.
	tgt, _ := st.Memories().Get(ctx, scope, revTargetID)
	jt, _ := st.Memories().GetJunctions(ctx, scope, revTargetID)
	if err := st.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), Type: "memory.superseded", SubjectID: revTargetID,
		Reason: "reconcile supersede (seed)", Payload: reconcile.MarshalPriorState(*tgt, jt), CreatedAt: revSupersededAt,
	}); err != nil {
		t.Fatalf("emit superseded: %v", err)
	}
	sup := *tgt
	sup.Status = "superseded"
	sup.SupersededByID = revSupersederID
	sup.UpdatedAt = revSupersededAt
	if err := st.Memories().Update(ctx, scope, sup); err != nil {
		t.Fatalf("transition target: %v", err)
	}
}

// normalizeMem zeroes the surface-specific fields (tenant, mutation time) so two
// independent stores can be compared for structural identity.
func normalizeMem(m store.Memory) store.Memory {
	m.TenantID = ""
	m.UpdatedAt = 0
	return m
}

// readRolledBackPayload returns the payload of the newest memory.rolled_back
// event for id.
func readRolledBackPayload(t *testing.T, st store.Store, scope identity.Scope, id string) string {
	t.Helper()
	evs, err := st.Events().ListBySubject(context.Background(), scope, id, 50)
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, e := range evs {
		if e.Type == "memory.rolled_back" {
			return e.Payload
		}
	}
	t.Fatalf("no memory.rolled_back event for %s", id)
	return ""
}

// ── embedded surface ──────────────────────────────────────────────────────────

func runEmbeddedRollbackParity(t *testing.T) rollbackOutcome {
	t.Helper()
	cfg := baseConfig(t)
	tenant := "rev-embedded"
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = closer(shutCtx)
	})

	// Seed via a side store on the same DSN (the SDK does not expose raw writes).
	side, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("side store open: %v", err)
	}
	defer func() { _ = side.Close(ctx) }()
	seedSupersededScenario(t, side, scope)

	restored, err := client.Rollback(ctx, stowage.RollbackRequest{MemoryID: revTargetID})
	if err != nil {
		t.Fatalf("embedded rollback: %v", err)
	}
	if restored.ID != revTargetID || restored.Status != "active" {
		t.Fatalf("embedded restored unexpected: %+v", restored)
	}

	out := rollbackOutcome{rolledBackPayload: readRolledBackPayload(t, side, scope, revTargetID)}
	tgt, _ := side.Memories().Get(ctx, scope, revTargetID)
	out.restored = normalizeMem(*tgt)
	sup, _ := side.Memories().Get(ctx, scope, revSupersederID)
	out.supersederStatus = sup.Status

	// Conflict path: a second rollback returns the typed conflict.
	_, err2 := client.Rollback(ctx, stowage.RollbackRequest{MemoryID: revTargetID})
	out.doubleConflict = errors.Is(err2, reconcile.ErrAlreadyRolledBack)
	if !out.doubleConflict {
		t.Errorf("embedded double rollback: got %v want ErrAlreadyRolledBack", err2)
	}
	return out
}

// ── HTTP surface ──────────────────────────────────────────────────────────────

func runServeRollbackParity(t *testing.T) rollbackOutcome {
	t.Helper()
	cfg := baseConfig(t)
	tenant := "rev-serve"
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	stk, p := startStack(t, cfg)
	seedSupersededScenario(t, stk.Store, scope)

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetRetriever(stk.Retriever)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	rollback := func() (int, []byte) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+"/v1/memories/"+revTargetID+"/rollback", bytes.NewReader(nil))
		req.Header.Set("Authorization", "Bearer "+plaintext)
		resp, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("POST rollback: %v", derr)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	status, body := rollback()
	if status != http.StatusOK {
		t.Fatalf("serve rollback: status %d body %s", status, body)
	}
	var restored struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(body, &restored)
	if restored.ID != revTargetID || restored.Status != "active" {
		t.Fatalf("serve restored unexpected: %s", body)
	}

	out := rollbackOutcome{rolledBackPayload: readRolledBackPayload(t, stk.Store, scope, revTargetID)}
	tgt, _ := stk.Store.Memories().Get(ctx, scope, revTargetID)
	out.restored = normalizeMem(*tgt)
	sup, _ := stk.Store.Memories().Get(ctx, scope, revSupersederID)
	out.supersederStatus = sup.Status

	// Conflict path: a second rollback returns 409.
	status2, _ := rollback()
	out.doubleConflict = status2 == http.StatusConflict
	if !out.doubleConflict {
		t.Errorf("serve double rollback: got %d want 409", status2)
	}
	return out
}

// ── parity assertion ──────────────────────────────────────────────────────────

func TestReversibilityParity_EmbeddedVsServe(t *testing.T) {
	emb := runEmbeddedRollbackParity(t)
	srv := runServeRollbackParity(t)

	if emb.restored != srv.restored {
		t.Errorf("restored memory diverges:\n embedded=%+v\n    serve=%+v", emb.restored, srv.restored)
	}
	if emb.rolledBackPayload != srv.rolledBackPayload {
		t.Errorf("rolled_back payload diverges:\n embedded=%s\n    serve=%s", emb.rolledBackPayload, srv.rolledBackPayload)
	}
	if emb.supersederStatus != "deleted" || srv.supersederStatus != "deleted" {
		t.Errorf("superseder not tombstoned: embedded=%q serve=%q", emb.supersederStatus, srv.supersederStatus)
	}
	if !emb.doubleConflict || !srv.doubleConflict {
		t.Errorf("double-rollback conflict not raised on both: embedded=%v serve=%v", emb.doubleConflict, srv.doubleConflict)
	}
}
