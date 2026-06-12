// Package harness provides an in-process Stowage server for eval runs.
//
// The server is backed by a temp SQLite database, a mock gateway driven by
// a scripted response file, and the full buffer → extract → reconcile pipeline.
// It mirrors the boot sequence in cmd/stowage/main.go#runServe.
package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/lifecycle"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/vindex"

	// register drivers; mock imported named so PushExtractionScript can type-assert.
	gwtmock "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw"
)

// TestServer is a fully-wired in-process Stowage server for eval runs.
// It uses a temp SQLite DB, a mock gateway, and the full pipeline.
type TestServer struct {
	// URL is the base URL for HTTP calls (e.g. "http://127.0.0.1:PORT").
	URL string
	// AgentKey is the plaintext API key for eval tenant requests.
	AgentKey string
	// Store is the underlying store; used for polling (e.g. waiting for memories).
	Store store.Store
	// MockScriptPath is the path of the STOWAGE_MOCK_SCRIPT file.
	MockScriptPath string
	// TenantID is the eval tenant scope.
	TenantID string

	httpSrv        *httptest.Server
	srv            *api.Server
	bufStage       *pipeline.Stage
	extractStage   *pipeline.ExtractStage
	reconcileStage *reconcile.ReconcileStage
	gw             gateway.Gateway
	cancel         context.CancelFunc
}

// NewTestServer creates a fully-wired in-process server for eval.
// The caller must call Close() when done (or rely on t.Cleanup).
//
// STOWAGE_MOCK_SCRIPT is set to a temp file within dir; the caller writes
// scripted extraction responses to MockScriptPath before each flush.
func NewTestServer(t testing.TB, tenantID string) *TestServer {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "eval.db")
	mockScript := filepath.Join(dir, "mock-script.json")

	// Start with an empty script file.
	if err := os.WriteFile(mockScript, []byte("[]"), 0o600); err != nil {
		t.Fatalf("harness: write mock script: %v", err)
	}

	t.Setenv("STOWAGE_MOCK_SCRIPT", mockScript)

	cfg := config.Defaults()
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.Gateway.EmbedDims = 4
	// Full-mode override (build tag fullmode / operator runs): a real gateway
	// driver + models via env. ci mode never sets these.
	if d := os.Getenv("STOWAGE_EVAL_GATEWAY"); d != "" {
		cfg.Gateway.Driver = d
		cfg.Gateway.BaseURL = os.Getenv("STOWAGE_EVAL_BASE_URL")
		cfg.Gateway.APIKey = os.Getenv("STOWAGE_EVAL_API_KEY_REF") // env.VAR form
		cfg.Gateway.Model = os.Getenv("STOWAGE_EVAL_MODEL")
		cfg.Gateway.EmbedModel = os.Getenv("STOWAGE_EVAL_EMBED_MODEL")
		if v := os.Getenv("STOWAGE_EVAL_EMBED_DIMS"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				cfg.Gateway.EmbedDims = n
			}
		}
	}
	cfg.Server.Listen = ":0"
	cfg.Profile = "assistant"

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()

	ctx, cancel := context.WithCancel(context.Background())

	st, err := store.Open(ctx, cfg.Store)
	if err != nil {
		cancel()
		t.Fatalf("harness: open store: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close(ctx)
		cancel()
		t.Fatalf("harness: migrate: %v", err)
	}

	gw, err := gateway.Open(ctx, cfg.Gateway, log, reg)
	if err != nil {
		_ = st.Close(ctx)
		cancel()
		t.Fatalf("harness: gateway open: %v", err)
	}

	srv, err := api.New(cfg, st, log, reg)
	if err != nil {
		_ = gw.Close(ctx)
		_ = st.Close(ctx)
		cancel()
		t.Fatalf("harness: api.New: %v", err)
	}

	// Use eval-safe triggers: high count/token thresholds + long age so that only
	// explicit flushes (called by the runner after writing the mock script) fire.
	// Auto-flushes would consume the mock script before it is written, returning
	// empty candidates and causing non-deterministic scores.
	trig := pipeline.Triggers{
		Count:    10000,
		Tokens:   10_000_000,
		MaxAge:   10 * time.Minute,
		TickBase: 10 * time.Minute,
	}
	bufStage := pipeline.New(st, log, trig, srv.Pipeline())
	srv.SetStage(bufStage)
	bufStage.Start(ctx)

	topicSvc := topics.New(st.Topics(), log, cfg.Profile)
	srv.SetTopicService(topicSvc)
	extractStage := pipeline.NewExtractStage(st, gw, topicSvc, log, cfg.Profile, bufStage.Downstream())
	extractStage.Start(ctx)

	vi, err := vindex.Open(cfg.VIndex, st.Vectors(), cfg.Gateway.EmbedDims, cfg.Gateway.EmbedModel)
	if err != nil {
		_ = gw.Close(ctx)
		_ = st.Close(ctx)
		cancel()
		t.Fatalf("harness: vindex open: %v", err)
	}
	embedder := reconcile.NewEmbedder(st.Vectors(), vi, gw, log)
	embedder.Start(ctx)

	retriever := retrieval.NewWithInjections(st.Memories(), st.Records(), vi, gw, st.Injections(), log)
	srv.SetRetriever(retriever)

	reconcileStage := reconcile.New(
		st.Memories(),
		st.Ops(),
		st.Events(),
		gw,
		log,
		extractStage.Downstream(),
	)
	reconcileStage.SetEmbedder(embedder)
	reconcileStage.SetScopeInvalidator(retriever.Cache())
	reconcileStage.Start(ctx)

	// Phase 14 re-enqueue sweep. Ingest is fire-and-forget over a bounded
	// channel: under burst, flush events drop and ONLY the re-enqueue sweep
	// recovers the stalled records — production serve runs the full manager.
	// Without it, full-mode runs stall with unprocessed records and score
	// against a partial store (2026-06-12 finding). Mutating sweeps (decay/
	// dedupe/rollup) are parked at long intervals so CI stays deterministic.
	lcProfile := lifecycle.DefaultProfile()
	lcProfile.DecayInterval = 24 * time.Hour
	lcProfile.DedupeInterval = 24 * time.Hour
	lcProfile.RollupInterval = 24 * time.Hour
	lcProfile.ReenqueueInterval = 3 * time.Second
	lcProfile.ReenqueueDeadline = 5 * time.Second
	lcMgr := lifecycle.New(st, log, lcProfile, srv.PipelineIn())
	lcMgr.Start(ctx)

	httpSrv := httptest.NewServer(srv)

	// Create admin + agent keys for the eval tenant.
	adminKey, _, err := auth.Generate(tenantID, auth.RoleAdmin)
	if err != nil {
		cancel()
		t.Fatalf("harness: generate admin key: %v", err)
	}
	if err := st.Keys().Insert(adminKey); err != nil {
		cancel()
		t.Fatalf("harness: insert admin key: %v", err)
	}

	agentK, agentPT, err := auth.Generate(tenantID, auth.RoleAgent)
	if err != nil {
		cancel()
		t.Fatalf("harness: generate agent key: %v", err)
	}
	if err := st.Keys().Insert(agentK); err != nil {
		cancel()
		t.Fatalf("harness: insert agent key: %v", err)
	}

	ts := &TestServer{
		URL:            httpSrv.URL,
		AgentKey:       agentPT,
		Store:          st,
		MockScriptPath: mockScript,
		TenantID:       tenantID,
		httpSrv:        httpSrv,
		srv:            srv,
		bufStage:       bufStage,
		extractStage:   extractStage,
		reconcileStage: reconcileStage,
		gw:             gw,
		cancel:         cancel,
	}

	t.Cleanup(func() { ts.Close() })

	return ts
}

// Close shuts down the server and releases all resources.
func (s *TestServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
	s.bufStage.Drain(ctx)
	s.extractStage.Drain(ctx)
	s.reconcileStage.Drain(ctx)
	_ = s.gw.Close(ctx)
	_ = s.Store.Close(ctx)
	s.httpSrv.Close()
	s.cancel()
}

// Scope returns the identity.Scope for the eval tenant.
func (s *TestServer) Scope() identity.Scope {
	return identity.Scope{Tenant: s.TenantID}
}

// PushExtractionScript queues a single JSON extraction response entry into the
// mock gateway's in-process script queue. The extraction stage's Complete() call
// consumes from this queue before falling back to the lazy file, so calling
// PushExtractionScript immediately before a buffer flush guarantees the flush
// consumes exactly this entry — eliminating the global file-offset race
// (bbd134d diagnosis). Test-only boot-infrastructure; lives in server.go.
func (s *TestServer) PushExtractionScript(entry json.RawMessage) {
	drv := s.gw.(*gwtmock.Driver)
	drv.PushScript(gwtmock.Script{JSON: entry})
}

// ActiveMemoryCount returns the number of active memories currently stored in
// the eval tenant scope. Used by the runner's per-conversation fixture integrity
// check to detect when a conversation produced zero committed memories.
func (s *TestServer) ActiveMemoryCount(ctx context.Context) int {
	scope := s.Scope()
	total := 0
	cursor := ""
	for {
		mems, next, err := s.Store.Memories().ListByStatus(ctx, scope, "active", 500, cursor)
		if err != nil {
			return total
		}
		total += len(mems)
		if next == "" || len(mems) == 0 {
			return total
		}
		cursor = next
	}
}

// WaitForMemories polls until at least minCount active memories exist in scope,
// or until the deadline. Returns error on timeout.
func (s *TestServer) WaitForMemories(ctx context.Context, minCount int) error {
	deadline := time.Now().Add(30 * time.Second)
	scope := s.Scope()
	for time.Now().Before(deadline) {
		mems, _, err := s.Store.Memories().ListByStatus(ctx, scope, "active", 200, "")
		if err == nil && len(mems) >= minCount {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	mems, _, _ := s.Store.Memories().ListByStatus(ctx, scope, "active", 200, "")
	return fmt.Errorf("timeout waiting for %d memories: have %d", minCount, len(mems))
}

// WaitForQuiescence polls until the ingest pipeline has settled: zero
// unprocessed records AND a stable active-memory count across stablePolls
// consecutive polls (pollInterval apart), or until the deadline elapses.
//
// This is the full-mode settle barrier. WaitForMemories(minCount) is NOT
// sufficient for full mode: real extraction is async and a too-small minCount
// lets scoring start against a near-empty store (the 2026-06-12 n=10 baseline
// scored against ~10 memories because the harness waited for len(conversations)
// memories and then warned-and-continued — see eval/REPORT.md).
func (s *TestServer) WaitForQuiescence(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	const (
		pollInterval = 2 * time.Second
		stablePolls  = 3
	)
	lastCount := -1
	stable := 0
	for time.Now().Before(deadline) {
		unprocessed, err := s.Store.Records().ListUnprocessed(ctx, time.Now().UnixMilli(), 1)
		if err == nil && len(unprocessed) == 0 {
			n := s.ActiveMemoryCount(ctx)
			if n == lastCount {
				stable++
				if stable >= stablePolls {
					return nil
				}
			} else {
				stable = 0
				lastCount = n
			}
		} else {
			stable = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	unprocessed, _ := s.Store.Records().ListUnprocessed(ctx, time.Now().UnixMilli(), 50)
	return fmt.Errorf("pipeline not quiescent after %s: %d unprocessed records, %d active memories",
		timeout, len(unprocessed), s.ActiveMemoryCount(ctx))
}

// DoJSON makes a JSON HTTP request and returns the status code and response body.
func (s *TestServer) DoJSON(ctx context.Context, method, path string, body interface{}) (int, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.URL+path, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.AgentKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read body: %w", err)
	}
	return resp.StatusCode, data, nil
}
