package stowage_test

// reversibility_test.go covers the success paths of the D-070 SDK verbs
// (GetMemory / Rollback / ResolveMemory) on BOTH impls: the embedded client
// against a real sqlite stack (with a side store to seed a restorable state)
// and the http client against an httptest server returning canned envelopes.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"

	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
)

// ── embedded success paths ────────────────────────────────────────────────────

func TestEmbedded_Reversibility_Success(t *testing.T) {
	tenant := "rev-sdk-embedded"
	scope := identity.Scope{Tenant: tenant}
	dbPath := filepath.Join(t.TempDir(), "rev.db")

	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = "hnsw"

	ctx, cancel := context.WithCancel(context.Background())
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
	if err != nil {
		cancel()
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		shutCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = closer(shutCtx)
	})

	// Seed via a side store on the same DSN.
	side, err := store.Open(context.Background(), cfg.Store)
	if err != nil {
		t.Fatalf("side open: %v", err)
	}
	defer func() { _ = side.Close(context.Background()) }()

	now := time.Now().UnixMilli()
	memID := ulid.Make().String()
	mem := store.Memory{
		ID: memID, TenantID: tenant, Kind: "fact", Content: "original sdk content",
		Status: "active", Importance: 3, Confidence: 0.8, TrustSource: "llm_extracted",
		Stability: 1.0, ContentHash: ulid.Make().String(), CreatedAt: now, UpdatedAt: now,
	}
	if err := side.Memories().Commit(context.Background(), scope, store.CommitSet{
		Action: store.ActionAdd, Memory: mem, Entities: []string{"e"}, Keywords: []string{"k"},
		Events: []store.Event{{ID: ulid.Make().String(), Type: "memory.added", SubjectID: memID, Payload: "{}", CreatedAt: now}},
		Scope:  scope,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// GetMemory success.
	view, err := client.GetMemory(ctx, memID)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if view.Memory.ID != memID || len(view.Entities) == 0 {
		t.Errorf("GetMemory view unexpected: %+v", view)
	}

	// Write a prior-state event + mutate, then Rollback success (covers
	// memoryToSDK + invalidateScope + restored.Memory!=nil branch).
	jt, _ := side.Memories().GetJunctions(context.Background(), scope, memID)
	if err := side.Events().Emit(context.Background(), scope, store.Event{
		ID: ulid.Make().String(), Type: "memory.updated", SubjectID: memID,
		Payload: reconcile.MarshalPriorState(mem, jt), CreatedAt: time.Now().UnixMilli(),
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	upd := mem
	upd.Content = "mutated"
	upd.UpdatedAt = time.Now().UnixMilli()
	if err := side.Memories().Update(context.Background(), scope, upd); err != nil {
		t.Fatalf("update: %v", err)
	}

	restored, err := client.Rollback(ctx, stowage.RollbackRequest{MemoryID: memID})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored.Content != "original sdk content" || restored.Status != "active" {
		t.Errorf("Rollback restored unexpected: %+v", restored)
	}

	// ResolveMemory success (confirm) on a parked memory.
	parkedID := ulid.Make().String()
	parked := store.Memory{
		ID: parkedID, TenantID: tenant, Kind: "fact", Content: "parked sdk",
		Status: "pending_confirmation", Importance: 3, Confidence: 0.8,
		TrustSource: "llm_extracted", Stability: 1.0, ContentHash: ulid.Make().String(),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := side.Memories().Insert(context.Background(), scope, parked); err != nil {
		t.Fatalf("insert parked: %v", err)
	}
	res, err := client.ResolveMemory(ctx, stowage.ResolveRequest{MemoryID: parkedID, Action: "confirm"})
	if err != nil {
		t.Fatalf("ResolveMemory: %v", err)
	}
	if res.Status != "active" {
		t.Errorf("ResolveMemory status: got %q want active", res.Status)
	}
}

// ── http success paths ────────────────────────────────────────────────────────

func TestHTTP_Reversibility_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"memory":   map[string]any{"id": r.PathValue("id"), "status": "active", "content": "c"},
			"entities": []string{"e"}, "keywords": []string{}, "queries": []string{},
		})
	})
	mux.HandleFunc("POST /v1/memories/{id}/rollback", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "status": "active", "content": "restored"})
	})
	mux.HandleFunc("PATCH /v1/memories/{id}", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "status": "active"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := stowage.NewHTTP(ts.URL, "k")
	ctx := context.Background()

	view, err := client.GetMemory(ctx, "m1")
	if err != nil || view.Memory.ID != "m1" {
		t.Fatalf("GetMemory: %v view=%+v", err, view)
	}
	rb, err := client.Rollback(ctx, stowage.RollbackRequest{MemoryID: "m1"})
	if err != nil || rb.Content != "restored" {
		t.Fatalf("Rollback: %v rb=%+v", err, rb)
	}
	res, err := client.ResolveMemory(ctx, stowage.ResolveRequest{MemoryID: "m1", Action: "reject"})
	if err != nil || res.Status != "active" {
		t.Fatalf("ResolveMemory: %v res=%+v", err, res)
	}
}
