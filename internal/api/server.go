// Package api implements the Stowage HTTP surface (RFC §9.1, Phase 05).
//
// Routing uses stdlib net/http Go 1.22+ ServeMux patterns only (D-041).
// No router dependency is introduced; the API surface is small by design.
//
// Middleware chain (outermost to innermost per request):
//
//	recovery → request-log → body-limit → auth → handler
//
// HTTP hardening (CLAUDE.md §7): Read/Write/Idle timeouts and MaxHeaderBytes
// are set on the http.Server, never inherited from SDK defaults. Body limits
// are enforced per-request by the body-limit middleware.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/store"
)

// pipelineCap is the bounded pipeline channel capacity.
// Not a top-level config knob (per phase plan); Phase 06 replaces the drainer.
const pipelineCap = 4096

// Server is the Stowage HTTP server.
type Server struct {
	st      store.Store
	log     *slog.Logger
	cfg     config.ServerConfig
	httpSrv *http.Server

	// pipeline is the bounded channel for signalling the downstream pipeline
	// stage (Phase 06 replaces the stub drainer). Enqueue is non-blocking:
	// if full, the enqueue is dropped and pipelineDrops is incremented.
	// The record is already durable at this point (P2, CLAUDE.md §6).
	pipeline  chan string // ULID record IDs
	drainDone chan struct{}
	maxBodyB  int64 // max request body bytes

	// Prometheus metrics.
	ingestTotal   prometheus.Counter
	pipelineDrops prometheus.Counter
}

// New creates a configured Server with all routes registered.
// The server is ready to serve after New returns; call ListenAndServe to start.
func New(cfg *config.Config, st store.Store, log *slog.Logger, reg *prometheus.Registry) (*Server, error) {
	ingestTotal := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stowage_ingest_records_total",
		Help: "Total number of records durably ingested.",
	})
	pipelineDrops := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "stowage_pipeline_drops_total",
		Help: "Total non-blocking pipeline enqueue drops (record durable; future sweep recovers).",
	})
	reg.MustRegister(ingestTotal, pipelineDrops)

	pipeline := make(chan string, pipelineCap)
	drainDone := make(chan struct{})

	srv := &Server{
		st:            st,
		log:           log,
		cfg:           cfg.Server,
		pipeline:      pipeline,
		drainDone:     drainDone,
		maxBodyB:      cfg.Server.MaxBodyBytes,
		ingestTotal:   ingestTotal,
		pipelineDrops: pipelineDrops,
	}

	// Stub drainer goroutine — no-op until Phase 06 replaces it with the
	// buffer stage. Drains the channel so it never fills during tests.
	go func() {
		defer close(drainDone)
		for range pipeline {
			// Phase 06: replace with buffer-stage dispatch.
		}
	}()

	mux := http.NewServeMux()

	// Health and metrics — no auth required.
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /readyz", srv.handleReadyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))

	// Ingest — auth required (agent or admin role).
	mux.HandleFunc("POST /v1/records", srv.authMiddleware(srv.handleIngest, false))

	// Branches — auth required.
	mux.HandleFunc("POST /v1/branches", srv.authMiddleware(srv.handleBranches, false))

	// Admin key management — admin role required.
	// POST /v1/admin/keys is registered without the auth wrapper so that the
	// handler can implement bootstrap mode (first-key creation when the keyring
	// is empty — no auth required). The handler enforces auth for all
	// subsequent calls.
	mux.HandleFunc("GET /v1/admin/keys", srv.authMiddleware(srv.handleListKeys, true))
	mux.HandleFunc("POST /v1/admin/keys", srv.handleCreateKey)
	mux.HandleFunc("POST /v1/admin/keys/{id}/revoke", srv.authMiddleware(srv.handleRevokeKey, true))
	mux.HandleFunc("POST /v1/admin/keys/revoke-tenant", srv.authMiddleware(srv.handleRevokeTenantKeys, true))

	// DSAR stub — returns 501 (Phase 21 retention work implements the cascade).
	mux.HandleFunc("DELETE /v1/admin/users/{user}", srv.authMiddleware(srv.handleDSARStub, true))

	readTimeout := time.Duration(cfg.Server.ReadTimeout) * time.Second
	writeTimeout := time.Duration(cfg.Server.WriteTimeout) * time.Second
	idleTimeout := time.Duration(cfg.Server.IdleTimeout) * time.Second

	srv.httpSrv = &http.Server{
		Addr:           cfg.Server.Listen,
		Handler:        srv.recoveryMiddleware(srv.requestLogMiddleware(srv.bodyLimitMiddleware(mux))),
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
		IdleTimeout:    idleTimeout,
		MaxHeaderBytes: 1 << 20, // 1 MiB header limit
	}

	return srv, nil
}

// ServeHTTP implements http.Handler so *Server can be used directly with
// httptest.NewServer in tests.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.httpSrv.Handler.ServeHTTP(w, r)
}

// ListenAndServe starts the HTTP server. It returns http.ErrServerClosed after
// Shutdown is called.
func (s *Server) ListenAndServe() error {
	s.log.Info("api: listening", "addr", s.cfg.Listen)
	err := s.httpSrv.ListenAndServe()
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the HTTP server, then closes the pipeline channel
// and waits for the drainer goroutine to finish.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		return fmt.Errorf("api: shutdown: %w", err)
	}
	// All HTTP handlers have exited — no more pipeline sends possible.
	close(s.pipeline)
	select {
	case <-s.drainDone:
	case <-ctx.Done():
		s.log.Warn("api: shutdown: drain timed out")
	}
	return nil
}
