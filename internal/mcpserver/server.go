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
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/grants"
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
	Store     store.Store
	Retriever *retrieval.Retriever
	TopicSvc  *topics.Service
	GrantsSvc *grants.Service
	// Gateway is the intelligence seam, used by memory_verify (claim entailment,
	// Phase 25). May be nil — verify then degrades to unclear (D-036).
	Gateway    gateway.Gateway
	PipelineIn chan<- pipeline.Item
	// PipelineStage is the buffer stage, used by memory_flush and memory_branch
	// (discard) — the shared control-verb core (D-071). May be nil in tests.
	PipelineStage *pipeline.Stage
	Log           *slog.Logger
	ScopeFn       ScopeFn
	// Profile is the active config profile — selects the profile-internal
	// playbook token budget for memory_playbook (D-072/D-042).
	Profile string
}

// StdioScopeFn returns a ScopeFn that always resolves to a tenant-only scope
// with the given tenant ID. This is the correct posture for stdio mode where
// there is no per-request auth (AC-4 / D-020).
func StdioScopeFn(tenant string) ScopeFn {
	return func(_ context.Context) (identity.Scope, error) {
		return identity.Scope{Tenant: tenant}, nil
	}
}

// New creates a Dockyard *server.Server with all 17 Stowage MCP tools registered:
// the original seven, the D-070 reversibility trio (memory_get, memory_rollback,
// memory_resolve), the D-071 Tier control verbs (memory_flush, memory_branch, and the
// Tier-B memory_grants), the episodic reads (memory_episodes, memory_causal), and the
// §6c trust verbs (memory_verify, memory_review).
// It returns an error when any tool fails to register (type mismatch, missing
// handler) — the caller must handle the error and exit non-zero (AGENTS.md §5).
func New(info server.Info, svc *Services) (*server.Server, error) {
	srv, err := server.New(info, &server.Options{Logger: svc.Log})
	if err != nil {
		return nil, err
	}

	if err := tool.New[IngestInput, IngestOutput]("memory_ingest").
		Describe("Ingest one or more verbatim interaction records into the caller's own Stowage memory scope. Contribute-mode (target_scope + contributor_user_id) writes into a pool-owner's scope when a covering contribute grant exists; without one the request is rejected (D-071).").
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
		Describe("Assemble the deterministic, sectioned, utility-ranked, budget-packed memory playbook for the caller's scope: strategy and failure_mode memories first, then decision/gotcha/pattern building blocks, with provenance. LLM-free (mirrors GET /v1/playbook; D-072). session_id narrows to one session.").
		Handler(makePlaybookHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[EpisodesInput, EpisodesOutput]("memory_episodes").
		Describe("Read the caller's episodes + their narratives (mirrors GET /v1/episodes; RFC §6b, D-080): most-recent-first list, or one episode when id is set, narrowed by session_id and the [from,until] time window. Deterministic + LLM-free.").
		Handler(makeEpisodesHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[CausalInput, CausalOutput]("memory_causal").
		Describe("Walk the causal graph from a memory (mirrors GET /v1/causal; RFC §5.6/§6b, D-083): backward to its causes ('why did this happen'), forward to its effects, or both, with provenance at every hop. Deterministic + LLM-free.").
		Handler(makeCausalHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[VerifyInput, VerifyOutput]("memory_verify").
		Describe("Verify that a claim is entailed by its cited memories (mirrors POST /v1/verify; RFC §6c, D-084): a schema-constrained gateway entailment check. Returns verdict (entailed|not_entailed|unclear) + confidence + explanation; degrades to unclear when the gateway is unreachable.").
		Handler(makeVerifyHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[ReviewInput, ReviewOutput]("memory_review").
		Describe("List the scope's pending_review memories (uncited agent assertions) and approve (→active) or reject (→quarantined) them (RFC §6c, D-084). action: list | approve | reject.").
		Handler(makeReviewHandler(svc)).
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

	// Reversibility tools (D-070) — single-purpose, mirroring the HTTP verbs.
	if err := tool.New[GetInput, GetOutput]("memory_get").
		Describe("Read a memory by id with its junctions and supersedes chain (mirrors GET /v1/memories/{id}).").
		Handler(makeGetHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[RollbackInput, RollbackOutput]("memory_rollback").
		Describe("Roll back (invert) the newest reconciliation event for a memory, restoring its prior state (mirrors POST /v1/memories/{id}/rollback; D-064).").
		Handler(makeRollbackHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[ResolveInput, ResolveOutput]("memory_resolve").
		Describe("Resolve a pending_confirmation memory: action=confirm promotes it to active (superseding any target); action=reject expires it (mirrors PATCH /v1/memories/{id}; D-065).").
		Handler(makeResolveHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Tier-A control verbs (D-071) — single-user, mirroring the HTTP routes.
	if err := tool.New[FlushInput, FlushOutput]("memory_flush").
		Describe("Flush a named buffer key with trigger explicit|session_end (mirrors POST /v1/buffers/{key}/flush; D-071).").
		Handler(makeFlushHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := tool.New[BranchInput, BranchOutput]("memory_branch").
		Describe("Manage session branches: action=fork creates a branch; merge marks it merged; discard marks it discarded and flushes its buffered turns without promoting them (mirrors POST /v1/branches; D-029).").
		Handler(makeBranchHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Tier-B admin verb (D-071) — multi-user; matches the HTTP admin routes,
	// deliberately ABSENT from the single-user SDK (D-067).
	if err := tool.New[GrantsInput, GrantsOutput]("memory_grants").
		Describe("Manage team-sharing groups and grants: create_group, list_groups, add_member, remove_member, list_members, create_grant, list_grants, revoke_grant (mirrors the HTTP /v1/admin/groups + /v1/scopes/grants routes; D-016).").
		Handler(makeGrantsHandler(svc)).
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
