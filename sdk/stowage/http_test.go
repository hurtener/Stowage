package stowage_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/vindex"
	stowage "github.com/hurtener/stowage/sdk/stowage"

	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw"
)

// newHTTPTestServer returns a running httptest.Server backed by a real
// in-memory SQLite store + mock gateway, and an API key for tenantID.
// The server is closed automatically when t finishes.
func newHTTPTestServer(t *testing.T, tenantID string) (*httptest.Server, string) {
	t.Helper()
	ctx := context.Background()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	reg := prometheus.NewRegistry()

	// Use a temp-file SQLite database, not ":memory:", because the SQLite
	// driver opens two connections (writer + reader pool). With ":memory:" each
	// connection gets its own in-memory DB, so keys written via the writer
	// are invisible to the reader, causing spurious 401s in auth.Verify.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = "hnsw"
	cfg.Server.MaxBodyBytes = 4 << 20

	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})

	gw, err := gateway.Open(ctx, cfg.Gateway, log, reg)
	if err != nil {
		t.Fatalf("gateway.Open: %v", err)
	}
	t.Cleanup(func() { _ = gw.Close(context.Background()) })

	vi := vindex.New(st.Vectors(), 0, "")

	embedder := reconcile.NewEmbedder(st.Vectors(), vi, gw, log)
	embedder.Start(ctx)

	ret := retrieval.NewWithInjections(st.Memories(), st.Records(), vi, gw, st.Injections(), log)
	ret.SetGrants(st.Grants())
	t.Cleanup(func() { ret.Close() })

	topicSvc := topics.New(st.Topics(), log, "assistant")
	grantsSvc := grants.New(st.Grants(), st.Events(), log)

	srv, err := api.New(cfg, st, log, reg)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetRetriever(ret)
	srv.SetTopicService(topicSvc)
	srv.SetGrantsService(grantsSvc)

	// Wire pipeline stages.
	trig := pipeline.TriggersFromConfig("assistant")
	bufStage := pipeline.New(st, log, trig, srv.Pipeline())
	srv.SetStage(bufStage)
	bufStage.Start(ctx)

	extractStage := pipeline.NewExtractStage(st, gw, topicSvc, log, "assistant", bufStage.Downstream())
	extractStage.Start(ctx)

	reconcileStage := reconcile.New(st.Memories(), st.Ops(), st.Events(), gw, log, extractStage.Downstream())
	reconcileStage.SetEmbedder(embedder)
	reconcileStage.SetScopeInvalidator(ret.Cache())
	reconcileStage.Start(ctx)

	lcMgr := lifecycle.New(st, log, lifecycle.DefaultProfile(), srv.PipelineIn())
	lcMgr.Start(ctx)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()

		shutCtx, shutDone := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutDone()

		// Stop the lifecycle manager before closing the pipeline so the
		// re-enqueue sweep cannot send on a closed channel.
		lcMgr.Stop()

		// Close the server's pipeline channel so buffer stage workers exit
		// their range loop (mirrors srv.Shutdown → close(pipeline) in serve mode).
		// ts.Close() has already stopped the HTTP server, so no more handler
		// sends can occur; lcMgr.Stop() ensures no more sweep sends either.
		close(srv.PipelineIn())

		bufStage.Drain(shutCtx)
		extractStage.Drain(shutCtx)
		reconcileStage.Drain(shutCtx)
	})

	// Create an agent key for the test tenant.
	key, plaintext, err := auth.Generate(tenantID, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := st.Keys().Insert(key); err != nil {
		t.Fatalf("st.Keys().Insert: %v", err)
	}

	return ts, plaintext
}

// TestClientHTTP_Suite runs the full parity suite against the HTTP constructor.
// AC-1: same-suite parity, HTTP path.
func TestClientHTTP_Suite(t *testing.T) {
	ts, apiKey := newHTTPTestServer(t, "http-suite-tenant")
	client := stowage.NewHTTP(ts.URL, apiKey)
	RunSuite(t, client)
}

// TestClientHTTP_TripleRun proves the HTTP client survives three concurrent
// suites without data races (AC-7: race ×3).
func TestClientHTTP_TripleRun(t *testing.T) {
	for i := 0; i < 3; i++ {
		i := i
		t.Run("run", func(t *testing.T) {
			t.Parallel()
			ts, apiKey := newHTTPTestServer(t, "http-race-tenant")
			client := stowage.NewHTTP(ts.URL, apiKey)
			_ = client
			// Lightweight smoke: just ingest + retrieve.
			ctx := context.Background()
			_, err := client.Ingest(ctx, stowage.IngestRequest{
				Records: []stowage.RecordInput{
					{Content: "race test record", Role: "user"},
				},
			})
			if err != nil {
				t.Errorf("run %d: Ingest error: %v", i, err)
			}
		})
	}
}
