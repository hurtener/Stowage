package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/mcpserver"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAgentHTTPMountKeepsStaticCatalogsSeparate(t *testing.T) {
	svc := &mcpserver.Services{ScopeFn: mcpserver.StdioScopeFn("test")}
	full, err := mcpserver.New(server.Info{Name: "full", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	h, err := full.HTTPHandler(mcpHTTPOptions("keyring", false))
	if err != nil {
		t.Fatal(err)
	}
	h, err = withAgentMCP(h, svc, "keyring", false)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()
	for _, tc := range []struct {
		path  string
		count int
	}{
		{"/", 24}, {"/mcp", 24},
		{"/agent", 5}, {"/mcp/agent", 5}, {"/mcp/agent/", 5},
		{"/runtime", 6}, {"/runtime/", 6}, {"/mcp/runtime", 6}, {"/mcp/runtime/", 6},
	} {
		t.Run(tc.path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "paths", Version: "1"}, nil)
			session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: ts.URL + tc.path}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			list, err := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Tools) != tc.count {
				t.Fatalf("%s: wanted %d tools, got %d", tc.path, tc.count, len(list.Tools))
			}
			if tc.count == 6 {
				found := false
				for _, tool := range list.Tools {
					found = found || tool.Name == "memory_ingest_run"
				}
				if !found {
					t.Fatal("runtime connection lost the auto-save sink")
				}
			}
			if tc.count == 5 {
				r, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_ingest_run", Arguments: map[string]any{}})
				if err == nil && !r.IsError {
					t.Fatal("runtime sink callable on pure agent endpoint")
				}
			}
		})
	}
}

func TestMCPCatalogSelection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
	}{{"agent", 5}, {"runtime", 6}, {"full", 24}} {
		srv, err := newMCPCatalog(tc.name, server.Info{Name: "selector", Version: "1"}, &mcpserver.Services{})
		if err != nil {
			t.Fatal(err)
		}
		if len(srv.Tools()) != tc.count {
			t.Fatalf("%s: got %d tools, want %d", tc.name, len(srv.Tools()), tc.count)
		}
	}
	if _, err := newMCPCatalog("deferred", server.Info{Name: "invalid", Version: "1"}, &mcpserver.Services{}); err == nil {
		t.Fatal("invalid catalog silently accepted")
	}
}
