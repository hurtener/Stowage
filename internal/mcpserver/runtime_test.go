package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/identity"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func runtimeTestSession(t *testing.T, srv *server.Server) *mcpsdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "runtime-profile-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, srv.ServeInMemory(ctx), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestRuntimeCatalogPreservesAgentAndSinkContracts(t *testing.T) {
	svc := newHandlerServices(t)
	catalogs := map[string]map[string][]byte{}
	for name, constructor := range map[string]func(server.Info, *Services) (*server.Server, error){
		"agent": NewAgent, "runtime": NewRuntime, "full": New,
	} {
		srv, err := constructor(server.Info{Name: name, Version: "1"}, svc)
		if err != nil {
			t.Fatal(err)
		}
		list, err := runtimeTestSession(t, srv).ListTools(context.Background(), &mcpsdk.ListToolsParams{})
		if err != nil {
			t.Fatal(err)
		}
		catalogs[name] = map[string][]byte{}
		for _, tool := range list.Tools {
			b, err := json.Marshal(tool)
			if err != nil {
				t.Fatal(err)
			}
			catalogs[name][tool.Name] = b
		}
	}
	if len(catalogs["agent"]) != 5 || len(catalogs["runtime"]) != 6 || len(catalogs["full"]) != 24 {
		t.Fatalf("catalog sizes changed unexpectedly: agent=%d runtime=%d full=%d", len(catalogs["agent"]), len(catalogs["runtime"]), len(catalogs["full"]))
	}
	for name, descriptor := range catalogs["agent"] {
		if !bytes.Equal(descriptor, catalogs["runtime"][name]) {
			t.Errorf("runtime profile changed ordinary tool %s", name)
		}
	}
	if !bytes.Equal(catalogs["full"]["memory_ingest_run"], catalogs["runtime"]["memory_ingest_run"]) {
		t.Fatal("runtime sink differs from the existing full-catalog contract")
	}
	if _, present := catalogs["agent"]["memory_ingest_run"]; present {
		t.Fatal("pure agent profile advertises the runtime sink")
	}
}

func TestRuntimeSinkAcceptsHarborPayloadAndRejectsForeignIdentity(t *testing.T) {
	svc := newHandlerServices(t)
	scope := identity.Scope{Tenant: "acme", User: "alice"}
	svc.ScopeFn = fixedScopeFn(scope)
	srv, err := NewRuntime(server.Info{Name: "runtime", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	session := runtimeTestSession(t, srv)
	ctx := context.Background()
	in := validRunInput()
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_ingest_run", Arguments: in})
	if err != nil || result.IsError {
		t.Fatalf("run-end ingestion failed: %+v %v", result, err)
	}
	var receipt IngestRunOutput
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		t.Fatal(err)
	}
	if len(receipt.IDs) != len(in.Conversation) {
		t.Fatalf("wrong durable receipt: %+v", receipt)
	}
	assertRecordCount(t, svc, scope, "s1", len(in.Conversation))
	for _, rec := range listRecords(t, svc, scope, "s1") {
		if rec.UserID != "alice" || rec.TenantID != "acme" || rec.OutcomeDetail != "goal" {
			t.Fatalf("runtime scope/outcome lost: %+v", rec)
		}
	}
	in.UserID = "mallory"
	in.RunID = "foreign-run"
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_ingest_run", Arguments: in})
	if err == nil && !result.IsError {
		t.Fatal("runtime profile accepted a foreign user in the payload")
	}
	assertRecordCount(t, svc, scope, "s1", len(in.Conversation))
	for _, name := range []string{"memory_assert", "memory_ingest", "memory_grants", "memory_flush"} {
		result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !result.IsError {
			t.Errorf("runtime profile exposed unrelated operational tool %s", name)
		}
	}
}
