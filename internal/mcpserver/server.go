package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/dockyard/runtime/tool"

	"github.com/hurtener/stowage/internal/auth"
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

// KeyringMiddleware authenticates HTTP MCP requests against the store
// keyring (auth.Verify — constant-time, runtime-rotatable keys, D-030) and
// injects the key's tenant scope for CtxScopeFn.
func KeyringMiddleware(kr auth.Keyring, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(hdr, prefix) {
			http.Error(w, "authorization required", http.StatusUnauthorized)
			return
		}
		key, err := auth.Verify(kr, strings.TrimPrefix(hdr, prefix))
		if err != nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		// The KEY's tenant is the request tenant (D-030/P3) — never a
		// config constant: the original static-list middleware hardcoded
		// tenant "default" for every caller (multi-tenant hole, found in
		// gate review) and kept plaintext keys in config.
		ctx := identity.WithScope(r.Context(), identity.Scope{Tenant: key.TenantID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// CtxScopeFn resolves the request scope injected by KeyringMiddleware.
func CtxScopeFn() ScopeFn {
	return func(ctx context.Context) (identity.Scope, error) {
		sc, err := identity.FromContext(ctx)
		if err != nil || sc.Tenant == "" {
			return identity.Scope{}, fmt.Errorf("mcpserver: no authenticated scope in context")
		}
		return sc, nil
	}
}
