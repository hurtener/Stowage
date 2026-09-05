package main

import (
	"net/http"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/mcpserver"
	"github.com/hurtener/stowage/internal/version"
)

// withAgentMCP mounts a separate, static ordinary-agent catalog. Both paths are
// wrapped by the SAME existing auth middleware at the caller. Runtime sinks stay
// on the compatibility root, never in the ordinary planner's tools/list.
func withAgentMCP(full http.Handler, svc *mcpserver.Services, authMode string, trustProxy bool) (http.Handler, error) {
	agent, err := mcpserver.NewAgent(server.Info{Name: "stowage-agent", Title: "Stowage Agent Memory", Version: version.Version}, svc)
	if err != nil { return nil, err }
	handler, err := agent.HTTPHandler(mcpHTTPOptions(authMode, trustProxy))
	if err != nil { return nil, err }
	mux := http.NewServeMux()
	for _, path := range []string{"/agent", "/agent/", "/mcp/agent", "/mcp/agent/"} {
		mux.Handle(path, mcpRootRewrite(handler))
	}
	mux.Handle("/", mcpRootRewrite(full))
	return mux, nil
}
