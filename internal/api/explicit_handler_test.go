package api_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/reconcile"
	stowage "github.com/hurtener/stowage/sdk/stowage"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSourceBackedCommandsHTTPAndMCP(t *testing.T) {
	_, ts, st := newTestServer(t)
	_, key := mustCreateAgentKey(t, st, "explicit-tenant")
	client := stowage.NewHTTP(ts.URL, key, stowage.WithUser("u"), stowage.WithProject("p"))
	ctx := context.Background()
	appendUser := func(text, session string, at int64) string {
		t.Helper()
		r, err := client.Ingest(ctx, stowage.IngestRequest{Records: []stowage.RecordInput{{Role: "user", Content: text, ProjectID: "p", UserID: "u", SessionID: session, OccurredAt: at}}})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.IDs) != 1 {
			t.Fatal(r)
		}
		return r.IDs[0]
	}
	source := appendUser("Prefer concise responses.", "s1", 100)
	req := stowage.RememberRequest{SourceRecordID: source, Quote: "Prefer concise responses.", Kind: "preference", SessionID: "s1", IdempotencyKey: "save-one"}
	saved, err := client.Remember(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Outcome != "saved" || !saved.RetrievalEligible {
		t.Fatalf("bad receipt: %+v", saved)
	}
	inspected, err := client.GetMemory(ctx, saved.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Revision != saved.Revision || len(inspected.Provenance) != 1 {
		t.Fatalf("inspection not usable for correction: %+v", inspected)
	}

	// MCP uses the very same store and verified owner context, not a second
	// write implementation. New source evidence is persisted by the HOST.
	scope := identity.Scope{Tenant: "explicit-tenant", Project: "p", User: "u"}
	srv, err := mcpserver.NewAgent(server.Info{Name: "explicit-test", Version: "1"}, &mcpserver.Services{Store: st, ScopeFn: func(context.Context) (identity.Scope, error) { return scope, nil }})
	if err != nil {
		t.Fatal(err)
	}
	mcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mcClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := mcClient.Connect(mcCtx, srv.ServeInMemory(mcCtx), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	newSource := appendUser("Prefer detailed responses now.", "s2", 200)
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_correct", Arguments: map[string]any{"memory_id": saved.MemoryID, "expected_revision": inspected.Revision, "source_record_id": newSource, "quote": "Prefer detailed responses now."}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("MCP correction failed: %+v", result)
	}
	var receipt reconcile.ExplicitReceipt
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Outcome != "corrected" || receipt.ReplacesMemoryID != saved.MemoryID {
		t.Fatalf("bad correction receipt: %+v", receipt)
	}
	updated, err := client.GetMemory(ctx, receipt.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != receipt.Revision || updated.Memory.Content != "Prefer detailed responses now." {
		t.Fatalf("HTTP/MCP divergence: %+v", updated)
	}
	old, err := client.GetMemory(ctx, saved.MemoryID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Memory.Status != "superseded" {
		t.Fatal("old history was not retained")
	}
	again, err := client.Remember(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.CurrentStatus != "superseded" || again.RetrievalEligible {
		t.Fatalf("retry revived old value: %+v", again)
	}
	other := stowage.NewHTTP(ts.URL, key, stowage.WithUser("other"), stowage.WithProject("p"))
	if _, err := other.Remember(ctx, stowage.RememberRequest{SourceRecordID: source, Quote: req.Quote}); err == nil {
		t.Fatal("foreign source accepted")
	}
	if _, err := client.Correct(ctx, stowage.CorrectRequest{MemoryID: receipt.MemoryID, SourceRecordID: newSource, Quote: "Fabricated statement", ExpectedRevision: receipt.Revision}); err == nil {
		t.Fatal("fabricated correction accepted")
	}
}
