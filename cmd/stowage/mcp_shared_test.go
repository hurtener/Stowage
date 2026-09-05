package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/mcpserver"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSharedMCPProfilesSurviveOuterRouting(t *testing.T) {
	svc := &mcpserver.Services{ScopeFn: mcpserver.StdioScopeFn("test")}
	full, err := mcpserver.New(server.Info{Name: "shared", Version: "1"}, svc)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := full.HTTPHandler(mcpHTTPOptions("keyring", false))
	if err != nil {
		t.Fatal(err)
	}
	handler, err = withAgentMCP(handler, svc, "keyring", false)
	if err != nil {
		t.Fatal(err)
	}
	rest := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	ts := httptest.NewServer(sharedMCPMux(handler, rest))
	defer ts.Close()
	for _, tc := range []struct {
		path  string
		count int
	}{{"/mcp", 24}, {"/mcp/", 24}, {"/mcp/agent", 5}, {"/mcp/runtime", 6}} {
		t.Run(tc.path, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "shared-routing", Version: "1"}, nil)
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
				t.Fatalf("outer routing selected %d tools at %s; want %d", len(list.Tools), tc.path, tc.count)
			}
		})
	}
	for _, path := range []string{"/healthz", "/runtime", "/agent", "/v1/memories"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusTeapot {
			t.Errorf("REST route %s was stolen by the MCP profile mux", path)
		}
	}
}
