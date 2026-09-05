package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/mcpserver"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func agentSession(t *testing.T, svc *mcpserver.Services) *mcpsdk.ClientSession {
	t.Helper()
	srv, err := mcpserver.NewAgent(server.Info{Name: "agent-test", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cl := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "agent-test", Version: "1"}, nil)
	session, err := cl.Connect(ctx, srv.ServeInMemory(ctx), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestAgentCatalogGoldens(t *testing.T) {
	session := agentSession(t, newTestServices(t))
	list, err := session.ListTools(context.Background(), &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"memory_retrieve": true, "memory_inspect": true, "memory_remember": true, "memory_correct": true, "memory_playbook": true}
	if len(list.Tools) != len(want) {
		t.Fatalf("agent catalog has %d tools", len(list.Tools))
	}
	for _, tl := range list.Tools {
		if !want[tl.Name] {
			t.Fatalf("unexpected planner tool: %s", tl.Name)
		}
		delete(want, tl.Name)
		data, err := json.MarshalIndent(tl, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", "agent", tl.Name+".json")
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
				t.Fatal(err)
			}
		} else {
			checkGolden(t, path, data)
		}
		var in map[string]any
		raw, _ := json.Marshal(tl.InputSchema)
		if err := json.Unmarshal(raw, &in); err != nil {
			t.Fatal(err)
		}
		props, _ := in["properties"].(map[string]any)
		for key, value := range props {
			p, _ := value.(map[string]any)
			if p["description"] == nil {
				t.Errorf("%s.%s lacks guidance", tl.Name, key)
			}
		}
		if tl.Name == "memory_retrieve" && len(props) != 2 {
			t.Errorf("runtime fields leaked into recall: %v", props)
		}
	}
}

func TestAgentCatalogRejectsRuntimeAndAdminCalls(t *testing.T) {
	session := agentSession(t, newTestServices(t))
	for _, name := range []string{"memory_ingest", "memory_ingest_run", "memory_assert", "memory_grants", "memory_views", "memory_flush", "memory_forget"} {
		r, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !r.IsError {
			t.Errorf("hidden operation %s was callable", name)
		}
	}
}

func TestAgentSchemaRejectsInvalidValuesBeforeHandlers(t *testing.T) {
	session := agentSession(t, newTestServices(t))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"memory_retrieve", map[string]any{"query": "test", "limit": 101}},
		{"memory_retrieve", map[string]any{"query": "test", "user_id": "someone"}},
		{"memory_remember", map[string]any{"quote": "test", "kind": "invented_kind"}},
		{"memory_correct", map[string]any{"memory_id": "m", "quote": "test", "expected_revision": "bad"}},
		{"memory_inspect", map[string]any{"memory_id": "m", "citation": "c"}},
	} {
		r, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tc.name, Arguments: tc.args})
		if err == nil && !r.IsError {
			t.Errorf("accepted invalid %s: %v", tc.name, tc.args)
		}
	}
}

func TestAgentMissingSourceDoesNotClaimSave(t *testing.T) {
	session := agentSession(t, newTestServices(t))
	r, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "memory_remember", Arguments: map[string]any{"quote": "Keep auth in Pengui."}})
	if err != nil {
		if !strings.Contains(err.Error(), "source_required") {
			t.Fatal(err)
		}
		return
	}
	if !r.IsError {
		t.Fatal("missing source was accepted")
	}
	b, _ := json.Marshal(r.Content)
	if !strings.Contains(string(b), "source_required") {
		t.Fatalf("unhelpful error: %s", b)
	}
}
