package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/reconcile"
	"github.com/hurtener/stowage/internal/store"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAgentExplicitCommandsOnTheWire(t *testing.T) {
	svc := newFullServices(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, rec := range []store.Record{
		{ID: "agent-old-source", Role: "user", Content: "Keep authentication in Pengui.", OccurredAt: 100, CreatedAt: 100},
		{ID: "agent-new-source", Role: "user", Content: "Keep authentication and authorization in Pengui.", OccurredAt: 200, CreatedAt: 200},
	} {
		if err := svc.Store.Records().Append(ctx, testScope(), []store.Record{rec}); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := NewAgent(server.Info{Name: "agent-commands", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, srv.ServeInMemory(ctx), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()
	call := func(name string, args map[string]any, out any) string {
		t.Helper()
		result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			b, _ := json.Marshal(result.Content)
			t.Fatalf("%s: %s", name, b)
		}
		if out != nil {
			b, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(b, out); err != nil {
				t.Fatal(err)
			}
		}
		b, _ := json.Marshal(result.Content)
		return string(b)
	}
	var saved reconcile.ExplicitReceipt
	text := call("memory_remember", map[string]any{"quote": "Keep authentication in Pengui.", "source_record_id": "agent-old-source", "kind": "decision"}, &saved)
	if saved.Outcome != "saved" || !strings.Contains(text, "Durably committed") {
		t.Fatalf("receipt did not reach the reader: %+v %s", saved, text)
	}
	var inspected InspectOutput
	text = call("memory_inspect", map[string]any{"memory_id": saved.MemoryID}, &inspected)
	if len(inspected.Sources) != 1 || inspected.Revision == "" || !strings.Contains(text, "agent-old-source") {
		t.Fatalf("inspection lost source evidence: %+v", inspected)
	}
	var corrected reconcile.ExplicitReceipt
	call("memory_correct", map[string]any{"memory_id": saved.MemoryID, "expected_revision": inspected.Revision, "quote": "Keep authentication and authorization in Pengui.", "source_record_id": "agent-new-source"}, &corrected)
	if corrected.Outcome != "corrected" || corrected.ReplacesMemoryID != saved.MemoryID {
		t.Fatalf("correction not committed: %+v", corrected)
	}
	var old InspectOutput
	call("memory_inspect", map[string]any{"memory_id": saved.MemoryID}, &old)
	if old.Status != "superseded" || old.SupersededByID != corrected.MemoryID {
		t.Fatal("replacement history lost")
	}
	var recall RetrieveOutput
	call("memory_retrieve", map[string]any{"query": "authentication Pengui"}, &recall)
	for _, item := range recall.Items {
		if item.Citation != "" {
			var evidence InspectOutput
			call("memory_inspect", map[string]any{"citation": item.Citation}, &evidence)
		}
	}
	call("memory_playbook", map[string]any{}, nil)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"memory_remember", map[string]any{"quote": "invented quotation", "source_record_id": "agent-old-source"}},
		{"memory_correct", map[string]any{"memory_id": corrected.MemoryID, "expected_revision": strings.Repeat("0", 64), "quote": "Keep authentication in Pengui.", "source_record_id": "agent-old-source"}},
		{"memory_inspect", map[string]any{"memory_id": "missing"}},
	} {
		r, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && !r.IsError {
			t.Errorf("invalid command was accepted: %s", tc.name)
		}
	}
}

func TestReaderJSONFailureIsExplicit(t *testing.T) {
	if text := readerJSON(make(chan int)); !strings.Contains(text, "unavailable") {
		t.Fatal(text)
	}
}
