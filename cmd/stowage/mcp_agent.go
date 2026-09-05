package main

import (
	"fmt"
	"net/http"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/version"
)

// withAgentMCP mounts static agent and runtime profiles beside the full legacy
// catalog. ALL routes share the caller's existing authentication middleware.
// The runtime profile includes the completion sink on the same connection as
// ordinary tools; its HOST must exclude that sink from the planner, not detach it.
func withAgentMCP(full http.Handler, svc *mcpserver.Services, authMode string, trustProxy bool) (http.Handler, error) {
	mux := http.NewServeMux()
	for _, profile := range []string{"agent", "runtime"} {
		srv, err := newMCPCatalog(profile, server.Info{
			Name: "stowage-" + profile, Title: "Stowage Memory", Version: version.Version,
		}, svc)
		if err != nil {
			return nil, err
		}
		handler, err := srv.HTTPHandler(mcpHTTPOptions(authMode, trustProxy))
		if err != nil {
			return nil, err
		}
		for _, path := range []string{"/" + profile, "/" + profile + "/", "/mcp/" + profile, "/mcp/" + profile + "/"} {
			mux.Handle(path, mcpRootRewrite(handler))
		}
	}
	mux.Handle("/", mcpRootRewrite(full))
	return mux, nil
}

// newMCPCatalog is shared by stdio selection and HTTP mounting; unknown names
// fail loudly rather than silently choosing a catalog that loses the sink.
func newMCPCatalog(profile string, info server.Info, svc *mcpserver.Services) (*server.Server, error) {
	switch profile {
	case "agent":
		return mcpserver.NewAgent(info, svc)
	case "runtime":
		return mcpserver.NewRuntime(info, svc)
	case "full":
		return mcpserver.New(info, svc)
	default:
		return nil, fmt.Errorf("unknown MCP catalog %q: expected agent, runtime, or full", profile)
	}
}
