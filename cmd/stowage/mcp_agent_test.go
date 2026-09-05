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
	}{{"/", 24}, {"/mcp", 24}, {"/agent", 5}, {"/mcp/agent", 5}} {
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
			if tc.count == 5 {
				r, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "memory_ingest_run", Arguments: map[string]any{}})
				if err == nil && !r.IsError {
					t.Fatal("runtime sink callable on agent endpoint")
				}
			}
		})
	}
}
