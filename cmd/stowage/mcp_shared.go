package main

import "net/http"

// sharedMCPMux preserves the requested profile path until withAgentMCP has
// selected its handler. Rewriting /mcp/runtime to / before that selection would
// silently expose the full catalog instead. Only the selected leaf normalizes
// its path for Dockyard. REST's independent deadline/auth wrapper stays intact.
func sharedMCPMux(mcp, rest http.Handler) *http.ServeMux {
	root := http.NewServeMux()
	root.Handle("/mcp", mcp)
	root.Handle("/mcp/", mcp)
	root.Handle("/", rest)
	return root
}
