// Package integration holds cross-subsystem, real-driver tests (CLAUDE.md §17).
//
// pipeline_parity_test.go proves the Phase h1 invariant (D-068): a record
// ingested through ANY live entrypoint — `stowage serve` (HTTP), `stowage mcp`
// (in-process MCP transport), or `sdk/stowage` (embedded) — flows through the
// identical buffer→extract→reconcile pipeline that boot.StartPipeline wires, and
// becomes the SAME reconciled memory with the SAME provenance. It also covers a
// failure mode: under a degraded gateway the verbatim record is still durably
// appended and no goroutine panics during drain.
//
// Real drivers: sqlite store + the gateway mock driver (the sanctioned test
// exception, CLAUDE.md §17 — extraction is scripted so the assertions are
// deterministic). Runs under -race.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/api"
	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/boot"
	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway/mock"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/records"
	"github.com/hurtener/stowage/internal/store"
	stowage "github.com/hurtener/stowage/sdk/stowage"

	// Driver registration for boot.Open (the embedded SDK registers these too).
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	_ "github.com/hurtener/stowage/internal/store/sqlitestore"
	_ "github.com/hurtener/stowage/internal/vindex/hnsw"
)

const (
	wantContent = "The capital of France is Paris, a major European city."
	wantKind    = "fact"
	parityQuery = "capital of France Paris"
	paritySess  = "parity-sess"
	recContent  = "Paris is the capital of France and a major European city."
	// countTrigger is the assistant-profile buffer count trigger (config.D-042).
	// We ingest exactly this many records so the buffer flushes deterministically
	// without depending on the age ticker.
	countTrigger = 12
)

// retrieved is the surface-agnostic shape we compare across entrypoints.
type retrieved struct {
	ID       string
	Kind     string
	Content  string
	Citation string
}

// span is the surface-agnostic provenance span.
type span struct {
	RecordID  string
	SpanStart int
	SpanEnd   int
}

// outcome is what each entrypoint produces; parity means all three match on
// Content/Kind and the provenance span offsets.
type outcome struct {
	Content   string
	Kind      string
	SpanStart int
	SpanEnd   int
	HasProv   bool
}

// baseConfig returns a minimal sqlite+mock config pointed at a fresh temp DB.
func baseConfig(t *testing.T) config.Config {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "parity.db")
	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"
	cfg.Gateway.EmbedDims = 8
	cfg.Gateway.EmbedModel = "mock-embed"
	cfg.VIndex.Driver = "hnsw"
	cfg.Server.MaxBodyBytes = 4 << 20
	return cfg
}

// writeExtractionScript writes a single-entry mock script file (lazy-read at each
// Complete call) producing one "fact" candidate whose provenance points at recID.
func writeExtractionScript(t *testing.T, path, recID string) {
	t.Helper()
	body := fmt.Sprintf(`[{"candidates":[{"kind":%q,"content":%q,"context":"geography",`+
		`"entities":["france","paris"],"keywords":["capital","france","paris"],`+
		`"anticipated_queries":["what is the capital of france"],"importance":3,"confidence":0.9,`+
		`"provenance":[{"record_id":%q,"span_start":0,"span_end":10}]}]}]`,
		wantKind, wantContent, recID)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
}

// installTopic upserts one active topic so the extract stage does not
// short-circuit on an empty topic set (extract.go step 3).
func installTopic(t *testing.T, st store.Store, scope identity.Scope) {
	t.Helper()
	now := time.Now().UnixMilli()
	err := st.Topics().Upsert(t.Context(), scope, store.Topic{
		ID:          "parity-topic",
		TenantID:    scope.Tenant,
		Key:         "parity-topic",
		Description: "pipeline parity",
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	})
	if err != nil {
		t.Fatalf("install topic: %v", err)
	}
}

// pollRetrieve calls fn until it returns ≥1 item or the deadline passes.
func pollRetrieve(t *testing.T, fn func() ([]retrieved, error)) []retrieved {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for {
		items, err := fn()
		if err != nil {
			t.Fatalf("retrieve: %v", err)
		}
		if len(items) > 0 {
			return items
		}
		if time.Now().After(deadline) {
			t.Fatalf("no memory became retrievable within the deadline")
		}
		time.Sleep(150 * time.Millisecond)
	}
}

func assertParityItem(t *testing.T, items []retrieved) retrieved {
	t.Helper()
	for _, it := range items {
		if it.Content == wantContent && it.Kind == wantKind {
			return it
		}
	}
	t.Fatalf("expected memory (content=%q kind=%q) not found in %+v", wantContent, wantKind, items)
	return retrieved{}
}

// TestPipelineParity_AllEntrypoints proves serve, mcp, and embedded each turn an
// ingested record into the same reconciled memory + provenance (AC-3, AC-7).
func TestPipelineParity_AllEntrypoints(t *testing.T) {
	// STOWAGE_MOCK_SCRIPT is process-global and read lazily per Complete call;
	// each subtest writes its own content before flushing. t.Setenv forbids
	// t.Parallel, which is exactly what we want (sequential paths).
	scriptPath := filepath.Join(t.TempDir(), "mockscript.json")
	if err := os.WriteFile(scriptPath, []byte("[]"), 0o600); err != nil {
		t.Fatalf("seed mock script: %v", err)
	}
	t.Setenv("STOWAGE_MOCK_SCRIPT", scriptPath)

	results := map[string]outcome{}

	t.Run("embedded", func(t *testing.T) {
		results["embedded"] = runEmbedded(t, scriptPath)
	})
	t.Run("serve", func(t *testing.T) {
		results["serve"] = runServe(t, scriptPath)
	})
	t.Run("mcp", func(t *testing.T) {
		results["mcp"] = runMCP(t, scriptPath)
	})

	// Cross-entrypoint parity: identical content, kind, and provenance offsets.
	want := results["embedded"]
	if !want.HasProv {
		t.Fatalf("embedded produced no provenance: %+v", want)
	}
	for name, got := range results {
		if got.Content != want.Content || got.Kind != want.Kind {
			t.Errorf("%s memory diverges: got (%q,%q) want (%q,%q)", name, got.Content, got.Kind, want.Content, want.Kind)
		}
		if !got.HasProv {
			t.Errorf("%s produced no provenance", name)
		}
		if got.SpanStart != want.SpanStart || got.SpanEnd != want.SpanEnd {
			t.Errorf("%s provenance span diverges: got [%d:%d] want [%d:%d]", name, got.SpanStart, got.SpanEnd, want.SpanStart, want.SpanEnd)
		}
	}
}

// ── embedded (sdk/stowage) ────────────────────────────────────────────────────

func runEmbedded(t *testing.T, scriptPath string) outcome {
	cfg := baseConfig(t)
	tenant := "embedded-tenant"
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenant))
	if err != nil {
		t.Fatalf("NewEmbedded: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if cerr := closer(shutCtx); cerr != nil {
			t.Logf("embedded closer: %v", cerr)
		}
	})

	// Install a topic via a side store handle (the SDK is single-user and does
	// not expose topic upsert until Wave B).
	side, err := store.Open(ctx, cfg.Store)
	if err != nil {
		t.Fatalf("side store open: %v", err)
	}
	installTopic(t, side, scope)
	_ = side.Close(ctx)

	// Ingest countTrigger-1 records (no flush yet), capture an ID for provenance,
	// write the script, then ingest the final record to fire the count trigger.
	firstID := embeddedIngest(t, client, countTrigger-1)
	writeExtractionScript(t, scriptPath, firstID)
	_ = embeddedIngest(t, client, 1)

	items := pollRetrieve(t, func() ([]retrieved, error) {
		resp, rerr := client.Retrieve(ctx, stowage.RetrieveRequest{Query: parityQuery, Limit: 5})
		if rerr != nil {
			return nil, rerr
		}
		out := make([]retrieved, 0, len(resp.Items))
		for _, it := range resp.Items {
			out = append(out, retrieved{ID: it.ID, Kind: it.Kind, Content: it.Content, Citation: it.Citation})
		}
		return out, nil
	})
	mem := assertParityItem(t, items)

	dd, err := client.Drilldown(ctx, stowage.DrilldownRequest{MemoryID: mem.ID})
	if err != nil {
		t.Fatalf("embedded drilldown: %v", err)
	}
	return spansToOutcome(t, mem, embeddedSpans(dd))
}

func embeddedIngest(t *testing.T, client stowage.Client, n int) string {
	t.Helper()
	recs := make([]stowage.RecordInput, n)
	for i := range recs {
		recs[i] = stowage.RecordInput{Role: "user", Content: recContent, SessionID: paritySess}
	}
	resp, err := client.Ingest(t.Context(), stowage.IngestRequest{Records: recs})
	if err != nil {
		t.Fatalf("embedded ingest: %v", err)
	}
	if len(resp.IDs) == 0 {
		t.Fatalf("embedded ingest: no ids returned")
	}
	return resp.IDs[0]
}

func embeddedSpans(dd stowage.DrilldownResponse) []span {
	out := make([]span, 0, len(dd.Spans))
	for _, s := range dd.Spans {
		out = append(out, span{RecordID: s.RecordID, SpanStart: s.SpanStart, SpanEnd: s.SpanEnd})
	}
	return out
}

// ── serve (HTTP via httptest) ─────────────────────────────────────────────────

func runServe(t *testing.T, scriptPath string) outcome {
	cfg := baseConfig(t)
	tenant := "serve-tenant"
	scope := identity.Scope{Tenant: tenant}
	ctx := context.Background()

	stk, p := startStack(t, cfg)
	installTopic(t, stk.Store, scope)

	srv, err := api.New(&cfg, stk.Store, stk.Log, stk.Metrics)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	srv.SetPipelineIn(p.In)
	srv.SetStage(p.Stage)
	srv.SetTopicService(stk.TopicSvc)
	srv.SetRetriever(stk.Retriever)
	srv.SetGrantsService(stk.GrantsSvc)

	ts := httptest.NewServer(srv)
	t.Cleanup(func() {
		ts.Close()
		shutCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})

	// Agent key for auth.
	key, plaintext, err := auth.Generate(tenant, auth.RoleAgent)
	if err != nil {
		t.Fatalf("auth.Generate: %v", err)
	}
	if err := stk.Store.Keys().Insert(key); err != nil {
		t.Fatalf("keys insert: %v", err)
	}

	doPost := func(path string, body any) []byte {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+plaintext)
		resp, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("POST %s: %v", path, derr)
		}
		defer func() { _ = resp.Body.Close() }()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 300 {
			t.Fatalf("POST %s: status %d body %s", path, resp.StatusCode, out)
		}
		return out
	}

	ingest := func(n int) string {
		recs := make([]map[string]any, n)
		for i := range recs {
			recs[i] = map[string]any{"role": "user", "content": recContent, "session_id": paritySess}
		}
		var ir struct {
			IDs []string `json:"ids"`
		}
		_ = json.Unmarshal(doPost("/v1/records", map[string]any{"records": recs}), &ir)
		if len(ir.IDs) == 0 {
			t.Fatalf("serve ingest: no ids")
		}
		return ir.IDs[0]
	}

	firstID := ingest(countTrigger - 1)
	writeExtractionScript(t, scriptPath, firstID)
	_ = ingest(1)

	items := pollRetrieve(t, func() ([]retrieved, error) {
		var rr struct {
			Items []struct {
				ID       string `json:"id"`
				Kind     string `json:"kind"`
				Content  string `json:"content"`
				Citation string `json:"citation"`
			} `json:"items"`
		}
		_ = json.Unmarshal(doPost("/v1/retrieve", map[string]any{"query": parityQuery, "limit": 5}), &rr)
		out := make([]retrieved, 0, len(rr.Items))
		for _, it := range rr.Items {
			out = append(out, retrieved{ID: it.ID, Kind: it.Kind, Content: it.Content, Citation: it.Citation})
		}
		return out, nil
	})
	mem := assertParityItem(t, items)

	var dd struct {
		Spans []struct {
			RecordID  string `json:"record_id"`
			SpanStart int    `json:"span_start"`
			SpanEnd   int    `json:"span_end"`
		} `json:"spans"`
	}
	_ = json.Unmarshal(doPost("/v1/drilldown", map[string]any{"memory_id": mem.ID}), &dd)
	sp := make([]span, 0, len(dd.Spans))
	for _, s := range dd.Spans {
		sp = append(sp, span{RecordID: s.RecordID, SpanStart: s.SpanStart, SpanEnd: s.SpanEnd})
	}
	return spansToOutcome(t, mem, sp)
}

// ── mcp (in-process MCP transport) ────────────────────────────────────────────

func runMCP(t *testing.T, scriptPath string) outcome {
	cfg := baseConfig(t)
	tenant := "mcp-tenant"
	scope := identity.Scope{Tenant: tenant}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stk, p := startStack(t, cfg)
	installTopic(t, stk.Store, scope)

	srv, err := mcpserver.New(server.Info{Name: "stowage", Version: "test"}, &mcpserver.Services{
		Store:      stk.Store,
		Retriever:  stk.Retriever,
		TopicSvc:   stk.TopicSvc,
		PipelineIn: p.In,
		Log:        stk.Log,
		ScopeFn:    mcpserver.StdioScopeFn(tenant),
	})
	if err != nil {
		t.Fatalf("mcpserver.New: %v", err)
	}
	t.Cleanup(func() {
		shutCtx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		_ = p.Drain(shutCtx)
		_ = stk.Close(shutCtx)
	})
	_ = scope

	clientT := srv.ServeInMemory(ctx)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "parity-client", Version: "0.0.0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	callTool := func(name string, args any, out any) {
		res, cerr := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if cerr != nil {
			t.Fatalf("CallTool %s: %v", name, cerr)
		}
		if res.IsError {
			t.Fatalf("CallTool %s returned IsError: %+v", name, res.Content)
		}
		if out != nil {
			b, _ := json.Marshal(res.StructuredContent)
			if uerr := json.Unmarshal(b, out); uerr != nil {
				t.Fatalf("decode %s result: %v", name, uerr)
			}
		}
	}

	ingest := func(n int) string {
		recs := make([]mcpserver.IngestRecord, n)
		for i := range recs {
			recs[i] = mcpserver.IngestRecord{Role: "user", Content: recContent, SessionID: paritySess}
		}
		var out mcpserver.IngestOutput
		callTool("memory_ingest", mcpserver.IngestInput{Records: recs}, &out)
		if len(out.IDs) == 0 {
			t.Fatalf("mcp ingest: no ids")
		}
		return out.IDs[0]
	}

	firstID := ingest(countTrigger - 1)
	writeExtractionScript(t, scriptPath, firstID)
	_ = ingest(1)

	items := pollRetrieve(t, func() ([]retrieved, error) {
		var out mcpserver.RetrieveOutput
		callTool("memory_retrieve", mcpserver.RetrieveInput{Query: parityQuery, Limit: 5}, &out)
		res := make([]retrieved, 0, len(out.Items))
		for _, it := range out.Items {
			res = append(res, retrieved{ID: it.ID, Kind: it.Kind, Content: it.Content, Citation: it.Citation})
		}
		return res, nil
	})
	mem := assertParityItem(t, items)

	var dd mcpserver.DrilldownOutput
	callTool("memory_drilldown", mcpserver.DrilldownInput{MemoryID: mem.ID}, &dd)
	sp := make([]span, 0, len(dd.Spans))
	for _, s := range dd.Spans {
		sp = append(sp, span{RecordID: s.RecordID, SpanStart: s.SpanStart, SpanEnd: s.SpanEnd})
	}
	return spansToOutcome(t, mem, sp)
}

// ── shared helpers ────────────────────────────────────────────────────────────

// startStack opens a Stack and starts the live pipeline via the seam under test.
func startStack(t *testing.T, cfg config.Config) (*boot.Stack, *boot.Pipeline) {
	t.Helper()
	cfgPtr := cfg
	stk, err := boot.Open(context.Background(), &cfgPtr)
	if err != nil {
		t.Fatalf("boot.Open: %v", err)
	}
	p, err := boot.StartPipeline(context.Background(), stk, cfg)
	if err != nil {
		_ = stk.Close(context.Background())
		t.Fatalf("boot.StartPipeline: %v", err)
	}
	return stk, p
}

func spansToOutcome(t *testing.T, mem retrieved, spans []span) outcome {
	t.Helper()
	o := outcome{Content: mem.Content, Kind: mem.Kind}
	if len(spans) == 0 {
		return o
	}
	o.HasProv = true
	o.SpanStart = spans[0].SpanStart
	o.SpanEnd = spans[0].SpanEnd
	if spans[0].RecordID == "" {
		t.Errorf("provenance span has empty record_id")
	}
	return o
}

// TestPipelineParity_GatewayDegraded covers the failure mode: under a gateway
// that errors on every Complete call, the verbatim record is still durably
// appended (P1), no memory is derived, and Drain is panic-free and idempotent
// (AC-6). Re-enqueue eligibility (the record stays unprocessed) is preserved.
func TestPipelineParity_GatewayDegraded(t *testing.T) {
	cfg := baseConfig(t)
	scope := identity.Scope{Tenant: "degraded-tenant"}
	ctx := context.Background()

	stk, p := startStack(t, cfg)

	// Force the gateway into a degraded state: every extraction Complete errors.
	drv, ok := stk.Gateway.(*mock.Driver)
	if !ok {
		t.Fatalf("expected *mock.Driver, got %T", stk.Gateway)
	}
	for i := 0; i < countTrigger; i++ {
		drv.PushScript(mock.Script{Err: errors.New("gateway unavailable")})
	}

	installTopic(t, stk.Store, scope)

	// Durably append records and enqueue them; the 12th fires the count trigger,
	// driving the (degraded) extraction path.
	ids := make([]string, countTrigger)
	for i := 0; i < countTrigger; i++ {
		rec, err := records.New(records.Input{
			TenantID:  scope.Tenant,
			Role:      "user",
			Content:   recContent,
			SessionID: paritySess,
		})
		if err != nil {
			t.Fatalf("records.New: %v", err)
		}
		sr := store.Record{
			ID: rec.ID, TenantID: rec.TenantID, SessionID: rec.SessionID,
			Role: rec.Role, Content: rec.Content, TokenEstimate: rec.TokenEstimate,
			OccurredAt: rec.OccurredAt, CreatedAt: rec.CreatedAt,
		}
		if err := stk.Store.Records().Append(ctx, scope, []store.Record{sr}); err != nil {
			t.Fatalf("records append: %v", err)
		}
		ids[i] = rec.ID
		p.In <- pipeline.Item{RecordID: rec.ID, TenantID: rec.TenantID, SessionID: rec.SessionID}
	}

	// Give the degraded extraction path time to run and dead-letter.
	time.Sleep(2 * time.Second)

	// P1: every verbatim record is still durably present.
	got, err := stk.Store.Records().GetMany(ctx, scope, ids)
	if err != nil {
		t.Fatalf("records GetMany: %v", err)
	}
	if len(got) != countTrigger {
		t.Errorf("durable records: got %d want %d", len(got), countTrigger)
	}

	// No memory derived (extraction was degraded) — the records remain
	// unprocessed and re-enqueue-eligible.
	mems, _, err := stk.Store.Memories().ListByStatus(ctx, scope, "active", 100, "")
	if err != nil {
		t.Fatalf("memories ListByStatus: %v", err)
	}
	if len(mems) != 0 {
		t.Errorf("expected no memories under degraded gateway, got %d", len(mems))
	}

	// AC-6: Drain is panic-free and idempotent.
	shutCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := p.Drain(shutCtx); err != nil {
		t.Errorf("first drain: %v", err)
	}
	if err := p.Drain(shutCtx); err != nil {
		t.Errorf("second drain (idempotent): %v", err)
	}
	if err := stk.Close(shutCtx); err != nil {
		t.Logf("stack close: %v", err)
	}
}
