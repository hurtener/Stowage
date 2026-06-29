package api

import (
	"net/http"
	"net/http/pprof"
)

// pprofMux builds a ServeMux serving the net/http/pprof endpoints. It is mounted
// ONLY on the dedicated, opt-in pprof listener (server.pprof_listen) — never on
// the public API mux (CLAUDE.md §7; D-126).
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // also serves /heap, /goroutine, /block, /mutex, ...
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// PprofAdminHandler returns the admin-gated handler for the dedicated pprof
// listener. requireAdmin=true: pprof exposes PROCESS-GLOBAL profile data (heap,
// goroutines) that is not tenant-scoped, so a non-admin tenant key must not reach
// it (D-126).
func (s *Server) PprofAdminHandler() http.Handler {
	return s.authMiddleware(pprofMux().ServeHTTP, true)
}
