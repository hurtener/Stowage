// Package api implements the Stowage HTTP surface (RFC §9.1, Phase 05–06).
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
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
)

// pipelineCap is the bounded pipeline channel capacity.
// Not a top-level config knob (D-034 guardrail).
const pipelineCap = 4096

// Server is the Stowage HTTP server.
type Server struct {
	st      store.Store
	log     *slog.Logger
	cfg     config.ServerConfig
	profile string // active config profile — selects the profile-internal playbook budget (D-072/D-042)
	httpSrv *http.Server

	// pipeline is the bounded channel signalling the buffer stage.
	// Enqueue is non-blocking: if full the enqueue is dropped and
	// pipelineDrops is incremented. The record is already durable (P2).
	pipeline chan pipeline.Item // Stage drains this after Shutdown closes it.

	// ingestSink is the channel the ingest handler enqueues onto. It defaults
	// to the server-owned pipeline channel above (the standalone/test path);
	// when the live system is wired by boot.StartPipeline (serve), SetPipelineIn
	// redirects it to the shared pipeline-owned channel (D-068). The server does
	// not own or close ingestSink when it has been injected.
	ingestSink chan<- pipeline.Item

	// stage is the buffer pipeline stage (may be nil — tests don't wire it).
	// Set via SetStage before calling ListenAndServe.
	stage *pipeline.Stage

	// topicSvc provides virtual-pack logic for GET /v1/topics (Phase 07).
	// Set via SetTopicService before calling ListenAndServe.
	topicSvc *topics.Service

	// retriever is the four-lane retrieval engine (Phase 09).
	// Set via SetRetriever before calling ListenAndServe.
	retriever *retrieval.Retriever

	// grantsSvc provides group and grant management (Phase 15).
	// Set via SetGrantsService before calling ListenAndServe.
	grantsSvc *grants.Service

	maxBodyB int64 // max request body bytes

	// Prometheus metrics.
	ingestTotal   prometheus.Counter
	pipelineDrops prometheus.Counter
}

// New creates a configured Server with all routes registered.
// The server is ready to serve after New returns; call ListenAndServe to start.
// Call SetStage to wire the buffer stage before serving (optional — tests skip it).
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

	srv := &Server{
		st:            st,
		log:           log,
		cfg:           cfg.Server,
		profile:       cfg.Profile,
		pipeline:      make(chan pipeline.Item, pipelineCap),
		maxBodyB:      cfg.Server.MaxBodyBytes,
		ingestTotal:   ingestTotal,
		pipelineDrops: pipelineDrops,
	}
	// Default the ingest sink to the server-owned channel; serve overrides it
	// with the boot.StartPipeline-owned channel via SetPipelineIn.
	srv.ingestSink = srv.pipeline

	mux := http.NewServeMux()

	// Health and metrics — no auth required.
	mux.HandleFunc("GET /healthz", srv.handleHealthz)
	mux.HandleFunc("GET /readyz", srv.handleReadyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))

	// Ingest — auth required (agent or admin role).
	mux.HandleFunc("POST /v1/records", srv.authMiddleware(srv.handleIngest, false))

	// Branches — auth required.
	mux.HandleFunc("POST /v1/branches", srv.authMiddleware(srv.handleBranches, false))

	// Buffers — explicit flush endpoint (Phase 06).
	mux.HandleFunc("POST /v1/buffers/{key}/flush", srv.authMiddleware(srv.handleFlushBuffer, false))

	// Topics — extraction magnet management (Phase 07).
	mux.HandleFunc("GET /v1/topics", srv.authMiddleware(srv.handleListTopics, false))
	mux.HandleFunc("PUT /v1/topics", srv.authMiddleware(srv.handleUpsertTopics, false))
	mux.HandleFunc("DELETE /v1/topics/{key}", srv.authMiddleware(srv.handleDeleteTopic, false))

	// Retrieval — four-lane RRF retrieval (Phase 09).
	mux.HandleFunc("POST /v1/retrieve", srv.authMiddleware(srv.handleRetrieve, false))

	// Playbook — deterministic, LLM-free playbook assembly (Phase h5, D-072).
	mux.HandleFunc("GET /v1/playbook", srv.authMiddleware(srv.handlePlaybook, false))

	// Phase 23: episodic retrieval (RFC §6b, D-080).
	mux.HandleFunc("GET /v1/episodes", srv.authMiddleware(srv.handleEpisodes, false))

	// Phase 11: drill-down, feedback, and citation resolution.
	mux.HandleFunc("POST /v1/drilldown", srv.authMiddleware(srv.handleDrilldown, false))
	mux.HandleFunc("POST /v1/feedback", srv.authMiddleware(srv.handleFeedback, false))
	mux.HandleFunc("POST /v1/citations/resolve", srv.authMiddleware(srv.handleCitationsResolve, false))

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

	// Grants: group management (admin role) — Phase 15 (RFC §5.3).
	mux.HandleFunc("POST /v1/admin/groups", srv.authMiddleware(srv.handleCreateGroup, true))
	mux.HandleFunc("GET /v1/admin/groups", srv.authMiddleware(srv.handleListGroups, true))
	mux.HandleFunc("POST /v1/admin/groups/{id}/members", srv.authMiddleware(srv.handleAddMember, true))
	mux.HandleFunc("DELETE /v1/admin/groups/{id}/members/{user_id}", srv.authMiddleware(srv.handleRemoveMember, true))

	// Grants: grant management (agent or admin) — Phase 15 (RFC §5.3).
	mux.HandleFunc("GET /v1/scopes/grants", srv.authMiddleware(srv.handleListGrants, false))
	mux.HandleFunc("PUT /v1/scopes/grants", srv.authMiddleware(srv.handleCreateGrant, false))
	mux.HandleFunc("POST /v1/grants/{id}/revoke", srv.authMiddleware(srv.handleRevokeGrant, false))

	// Memory management — Phase 18 (D-064, D-065).
	mux.HandleFunc("GET /v1/memories/{id}", srv.authMiddleware(srv.handleGetMemory, false))
	mux.HandleFunc("POST /v1/memories/{id}/rollback", srv.authMiddleware(srv.handleRollbackMemory, false))
	mux.HandleFunc("PATCH /v1/memories/{id}", srv.authMiddleware(srv.handlePatchMemory, false))

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

// SetStage wires the buffer pipeline stage. Must be called before
// ListenAndServe. The stage receives from the server's internal pipeline
// channel; the server closes the channel on Shutdown to signal the stage.
func (s *Server) SetStage(st *pipeline.Stage) {
	s.stage = st
}

// SetTopicService wires the topics.Service used by GET /v1/topics (Phase 07).
// Must be called before ListenAndServe.
func (s *Server) SetTopicService(svc *topics.Service) {
	s.topicSvc = svc
}

// SetRetriever wires the retrieval.Retriever used by POST /v1/retrieve (Phase 09).
// Must be called before ListenAndServe.
func (s *Server) SetRetriever(r *retrieval.Retriever) {
	s.retriever = r
}

// SetGrantsService wires the grants.Service used by the grants and groups
// endpoints (Phase 15). Must be called before ListenAndServe.
func (s *Server) SetGrantsService(svc *grants.Service) {
	s.grantsSvc = svc
}

// SetPipelineIn redirects the ingest handler's enqueue target to an
// externally-owned channel — the one created and consumed by boot.StartPipeline
// (D-068). Must be called before ListenAndServe. The server does NOT close an
// injected channel on Shutdown; the Pipeline owner (boot.Pipeline.Drain) does.
func (s *Server) SetPipelineIn(ch chan<- pipeline.Item) {
	s.ingestSink = ch
}

// Pipeline returns the read end of the server-owned ingest pipeline channel.
// Pass this to pipeline.New so the stage can consume ingest items. Used by the
// standalone/test wiring path; serve uses boot.StartPipeline instead.
func (s *Server) Pipeline() <-chan pipeline.Item {
	return s.pipeline
}

// PipelineIn returns the write end of the ingest pipeline channel.
// Used by the lifecycle re-enqueue sweep to re-submit stalled records (Phase 14).
func (s *Server) PipelineIn() chan<- pipeline.Item {
	return s.pipeline
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

// Shutdown gracefully stops the HTTP server then closes the pipeline channel,
// signalling the buffer stage to drain. The caller is responsible for waiting
// on the stage drain (cmd/stowage does this after Shutdown returns).
// If a retriever with an injection writer is wired, it is closed after all
// HTTP handlers have exited to drain any pending injection batches.
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.httpSrv.Shutdown(ctx); err != nil {
		return fmt.Errorf("api: shutdown: %w", err)
	}
	// All HTTP handlers have exited — no more pipeline sends possible. This
	// closes the server-owned channel only; when boot.StartPipeline injected an
	// external ingest sink via SetPipelineIn, that channel is owned and closed by
	// boot.Pipeline.Drain, and this server-owned channel is simply unused.
	close(s.pipeline)
	// Drain the injection writer goroutine (Phase 11, P2 graceful drain).
	if s.retriever != nil {
		s.retriever.Close()
	}
	return nil
}
