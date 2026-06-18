package integration

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// narrateGW is a deterministic stand-in for the narration model.
type narrateGW struct{}

func (narrateGW) Complete(_ context.Context, _ gateway.CompleteRequest) (gateway.CompleteResponse, error) {
	return gateway.CompleteResponse{JSON: json.RawMessage(`{"title":"Episode","narrative":"The concrete path of what happened in this session."}`)}, nil
}
func (narrateGW) Embed(context.Context, gateway.EmbedRequest) (gateway.EmbedResponse, error) {
	return gateway.EmbedResponse{}, nil
}
func (narrateGW) Probe(context.Context) error { return nil }
func (narrateGW) Close(context.Context) error { return nil }
func (narrateGW) Rerank(context.Context, gateway.RerankRequest) (gateway.RerankResponse, error) {
	return gateway.RerankResponse{}, nil
}

// TestEpisodesLoop is the Phase-22 integration test (§17, D-079): real sqlite store
// + the detect/narrate sweeps over the gateway seam → episodes with linked
// narrative memories, scope-isolated across tenants.
func TestEpisodesLoop(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 4}))
	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = filepath.Join(t.TempDir(), "ep.db")
	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close(ctx) }()
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	old := time.Now().Add(-time.Minute).UnixMilli()
	// Tenant A: a closed session. Tenant B: a different closed session.
	a := identity.Scope{Tenant: "ten-a", Project: "p", User: "u", Session: "sa"}
	b := identity.Scope{Tenant: "ten-b", Project: "p", User: "u", Session: "sb"}
	if err := st.Records().Append(ctx, a, []store.Record{
		{ID: "a1", BranchID: "main", Role: "user", Content: "plan the launch", OccurredAt: old, CreatedAt: old},
		{ID: "a2", BranchID: "main", Role: "tool", Content: "launch shipped", Outcome: "success", OccurredAt: old + 50, CreatedAt: old + 50},
	}); err != nil {
		t.Fatalf("append a: %v", err)
	}
	if err := st.Records().Append(ctx, b, []store.Record{
		{ID: "b1", BranchID: "main", Role: "user", Content: "debug the outage", OccurredAt: old, CreatedAt: old},
	}); err != nil {
		t.Fatalf("append b: %v", err)
	}

	mgr := lifecycle.New(st, log, lifecycle.Profile{
		EpisodeDetectInterval: 15 * time.Minute, EpisodeNarrateInterval: 15 * time.Minute,
		EpisodeIdleWindow: time.Second, EpisodeBatchSize: 100,
	}, make(chan pipeline.Item, 8))
	mgr.SetEpisodes(narrateGW{})
	mgr.RunForce(ctx)

	tenA, tenB := identity.Scope{Tenant: "ten-a"}, identity.Scope{Tenant: "ten-b"}
	epA, err := st.Episodes().GetEpisodeBySession(ctx, tenA, "sa")
	if err != nil {
		t.Fatalf("episode A: %v", err)
	}
	if epA.NarrativeMemoryID == "" || epA.Outcome != "success" {
		t.Errorf("episode A not narrated/outcome: %+v", epA)
	}
	narrA, err := st.Memories().Get(ctx, tenA, epA.NarrativeMemoryID)
	if err != nil || narrA.Kind != "narrative" || narrA.EpisodeID != epA.ID {
		t.Errorf("narrative A wrong: %+v / %v", narrA, err)
	}

	// Scope isolation: tenant A's episodes never appear under tenant B.
	bEps, _, _ := st.Episodes().ListEpisodes(ctx, tenB, 10, "")
	for _, e := range bEps {
		if e.SessionID == "sa" {
			t.Error("tenant A's episode leaked into tenant B")
		}
	}
	if epB, err := st.Episodes().GetEpisodeBySession(ctx, tenB, "sb"); err != nil || epB.NarrativeMemoryID == "" {
		t.Errorf("tenant B episode should exist + be narrated: %+v / %v", epB, err)
	}
	if _, err := st.Episodes().GetEpisodeBySession(ctx, tenB, "sa"); err == nil {
		t.Error("tenant B must not see session sa")
	}
}
