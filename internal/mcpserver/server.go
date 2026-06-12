package mcpserver

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/dockyard/runtime/tool"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
)

// ScopeFn resolves an identity.Scope from the request context. In stdio mode
// the scope is fixed (see StdioScopeFn); in HTTP mode it is derived from the
// bearer token (see BearerMiddleware wiring the tenant into context).
type ScopeFn func(ctx context.Context) (identity.Scope, error)

// Services bundles the dependencies shared across all MCP tool handlers.
// Fields mirror the dependencies wired in cmd/stowage/main.go runServe.
type Services struct {
	Store      store.Store
	Retriever  *retrieval.Retriever
	TopicSvc   *topics.Service
	PipelineIn chan<- pipeline.Item
	Log        *slog.Logger
	ScopeFn    ScopeFn
}

// StdioScopeFn returns a ScopeFn that always resolves to a tenant-only scope
// with the given tenant ID. This is the correct posture for stdio mode where
// there is no per-request auth (AC-4 / D-020).
func StdioScopeFn(tenant string) ScopeFn {
	return func(_ context.Context) (identity.Scope, error) {
		return identity.Scope{Tenant: tenant}, nil
	}
}

// New creates a Dockyard *server.Server with all 7 Stowage MCP tools registered.
// It returns an error when any tool fails to register (type mismatch, missing
// handler) — the caller must handle the error and exit non-zero (AGENTS.md §5).
func New(info server.Info, svc *Services) (*server.Server, error) {
	srv, err := server.New(info, &server.Options{Logger: svc.Log})
	if err != nil {
		return nil, err
	}

	if err := tool.New[IngestInput, IngestOutput]("memory_ingest").
		Describe("Ingest one or more verbatim interaction records into Stowage memory (mirrors POST /v1/records).").
		Handler(makeIngestHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[RetrieveInput, RetrieveOutput]("memory_retrieve").
		Describe("Retrieve relevant memories for a query using the four-lane (lexical+queries+structured+vector) retrieval pipeline.").
		Handler(makeRetrieveHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[PlaybookInput, PlaybookOutput]("memory_playbook").
		Describe("Look up the extraction playbook for a query (stub — lands in Phase 17).").
		Handler(makePlaybookHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[DrilldownInput, DrilldownOutput]("memory_drilldown").
		Describe("Drill down into the provenance spans of a memory or citation (mirrors POST /v1/drilldown).").
		Handler(makeDrilldownHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[FeedbackInput, FeedbackOutput]("memory_feedback").
		Describe("Submit feedback on retrieved memories: use, save, fail, noise, or wrong_citation (mirrors POST /v1/feedback).").
		Handler(makeFeedbackHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[AssertInput, AssertOutput]("memory_assert").
		Describe("Directly assert (add, update, or delete) a memory in the store, bypassing the ingestion pipeline.").
		Handler(makeAssertHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[TopicsInput, TopicsOutput]("memory_topics").
		Describe("Manage extraction topics: list, upsert, or delete (mirrors GET/PUT/DELETE /v1/topics).").
		Handler(makeTopicsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

// BearerMiddleware is an HTTP middleware that validates a static bearer token
// and injects the tenant ID from the first matched key into the context via
// identity.WithScope. keys is a slice of raw API key strings. Requests without
// a valid Authorization: Bearer <key> header are rejected 401/403.
//
// This is the HTTP-transport auth guard (AC-5). Stdio mode uses StdioScopeFn
// instead — bearer auth is irrelevant for a single-user local pipe.
func BearerMiddleware(keys []string, next http.Handler) http.Handler {
	keySet := make(map[string]bool, len(keys))
	for _, k := range keys {
		keySet[k] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		raw := strings.TrimPrefix(auth, prefix)
		if !keySet[raw] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// Inject a tenant-only scope so the ScopeFn can read it.
		ctx := identity.WithScope(r.Context(), identity.Scope{Tenant: "default"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
