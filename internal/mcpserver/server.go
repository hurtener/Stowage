package mcpserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

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
	"github.com/hurtener/stowage/internal/traces"
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
	Gateway gateway.Gateway
	// TraceSigner signs memory_trace exports (Phase 26, D-086). nil ⇒ unsigned.
	TraceSigner ed25519.PrivateKey
	PipelineIn  chan<- pipeline.Item
	// PipelineStage is the buffer stage, used by memory_flush and memory_branch
	// (discard) — the shared control-verb core (D-071). May be nil in tests.
	PipelineStage *pipeline.Stage
	Log           *slog.Logger
	ScopeFn       ScopeFn
	// Profile is the active config profile — selects the profile-internal
	// playbook token budget for memory_playbook (D-072/D-042).
	Profile string
	// BrowseDefaultLimit is the configured retrieval.browse_default_limit
	// (ae5, D-143) — the memory_browse page size used when the caller omits
	// limit. Threaded from cfg.Retrieval.BrowseDefaultLimit at construction
	// (main.go); 0 is safe (retrieval.Browse clamps to its hard page cap).
	BrowseDefaultLimit int
}

// StdioScopeFn returns a ScopeFn that always resolves to a tenant-only scope
// with the given tenant ID. This is the correct posture for stdio mode where
// there is no per-request auth (AC-4 / D-020).
func StdioScopeFn(tenant string) ScopeFn {
	return func(_ context.Context) (identity.Scope, error) {
		return identity.Scope{Tenant: tenant}, nil
	}
}

// New creates a Dockyard *server.Server with all 22 Stowage MCP tools registered:
// the original seven, the D-070 reversibility trio (memory_get, memory_rollback,
// memory_resolve), the D-071 Tier control verbs (memory_flush, memory_branch, and the
// Tier-B memory_grants), the episodic reads (memory_episodes, memory_causal), the
// deterministic scope walk (memory_browse, ae5/D-143), the
// §6c trust verbs (memory_verify, memory_review), the §6c trace export (memory_trace),
// the §6d proactive verbs (memory_suggestions, memory_proactive_config), and the
// read-time agent-policy admin (memory_agent_policy, ae1, D-135/D-146/D-151).
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
		Describe("Retrieve relevant memories for a query using the four-lane (lexical+queries+structured+vector) retrieval pipeline. " +
			"Returns a lean markdown reader body in the model-facing Text block (episode hooks + per-item [cite:…] drill handles for " +
			"memory_drilldown) and the full typed result in Structured (D-142). The lean body shrinks the model's context, not the wire " +
			"payload: both blocks travel, so a host reading both receives a larger payload, not a smaller one.").
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

	if err := tool.New[BrowseInput, BrowseOutput]("memory_browse").
		Describe("Walk the caller's memories deterministically and gateway-free (mirrors GET /v1/memories; ae5, D-143): mode=recent (default) returns the scope's memories most-recent-first (created_at DESC) via a new store method; mode=superseded returns the scope's superseded memories by reusing the EXISTING status query — an ordering asymmetry (superseded is OLDEST-first) accepted as the cost of not adding a new query (H4). Neither mode ranks by relevance — for that use memory_retrieve.").
		Handler(makeBrowseHandler(svc)).
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

	if err := tool.New[TraceInput, traces.Bundle]("memory_trace").
		Describe("Export the reasoning trace for a response_id (mirrors GET /v1/traces/{response_id}; RFC §6c, D-086): the memory-into-conclusion chain (query, injected memories, drill-down spans, typed links, verification verdicts) reconstructed from the day-one tables, as an optionally ed25519-signed bundle. Deterministic + LLM-free.").
		Handler(makeTraceHandler(svc)).
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

	// Phase 27: proactive suggestions (RFC §6d, D-087) — single-user tier.
	if err := tool.New[SuggestionsInput, SuggestionsOutput]("memory_suggestions").
		Describe("Proactive memory suggestions (RFC §6d, D-087): action=list evaluates the scope's trigger rules (recent/similar episodes, expiring memories) and offers the budgeted, governance-gated set for a session — each offer carries the memory's content inline (no extra fetch needed). session_id is REQUIRED (it keys the per-session dedupe). NOTE: list is a write — each offer is recorded once per session, so a second list does not re-offer the same memory. action=accept|dismiss resolves an offer id and tunes that trigger's confidence; accept is acknowledgement/feedback, NOT a memory mutation (to keep an 'expiring' memory alive, reaffirm it with memory_assert). score is a relative utility weight (higher = stronger), not a 0-1 probability. Mirrors GET /v1/suggestions + POST /v1/suggestions/{id}.").
		Handler(makeSuggestionsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Phase 27: proactive governance (RFC §6d, D-087) — admin tier; deliberately
	// ABSENT from the single-user SDK (D-067).
	if err := tool.New[ProactiveConfigInput, ProactiveConfigOutput]("memory_proactive_config").
		Describe("Read or write a scope's proactive governance (RFC §6d, D-087): action=get returns the effective config (profile default overlaid by the scope's stored override); action=set writes the override (enabled, threshold, budget, classes). Mirrors GET/PUT /v1/admin/proactive. Opt-out is enabled=false.").
		Handler(makeProactiveConfigHandler(svc)).
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

	// Phase ae1: read-time agent-policy admin (D-135/D-146/D-151) — policy-admin
	// tier; deliberately ABSENT from the single-user SDK (D-067), matching
	// memory_grants' tiering.
	if err := tool.New[AgentPolicyInput, AgentPolicyOutput]("memory_agent_policy").
		Describe("Manage the read-time agent->topic policy binding: action=create|get|list|delete a (agent_id) -> {allow_topics, deny_topics} binding that curates (never isolates, D-139) the agent's own-scope memory_retrieve results (mirrors HTTP /v1/scopes/agent-policies; ae1, D-135/D-146/D-151).").
		Handler(makeAgentPolicyHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

// AuthMiddleware authenticates HTTP MCP requests via a (D-067)
// *auth.Authenticator — keyring (Verify) or JWT (Validator), depending on how
// a was constructed — and injects the resolved Scope for CtxScopeFn. Never
// logs credentials (CLAUDE.md §7).
//
// A missing/malformed Authorization header (auth.ErrTokenMissing) is a 401;
// any other rejection (bad/revoked/unknown credential, expired token, stale
// JWKS, etc.) is a 403 — matching the pre-ae7 KeyringMiddleware status-code
// contract exactly (surfaces keep their own error-body style; only the
// underlying reason vocabulary is shared, plan §"Error responses stay
// surface-specific").
func AuthMiddleware(a *auth.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		scope, _, err := a.Authenticate(r.Context(), hdr, r.Header.Get(auth.SessionHeader))
		if err != nil {
			if errors.Is(err, auth.ErrTokenMissing) {
				http.Error(w, "authorization required", http.StatusUnauthorized)
			} else {
				http.Error(w, "forbidden", http.StatusForbidden)
			}
			return
		}
		// The credential's tenant is the request tenant (D-030/P3) — never a
		// config constant: the original static-list middleware hardcoded
		// tenant "default" for every caller (multi-tenant hole, found in
		// gate review) and kept plaintext keys in config. In ModeJWT, scope
		// also carries the verified User/Session (ae7's core deliverable).
		ctx := identity.WithScope(r.Context(), scope)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// KeyringMiddleware authenticates HTTP MCP requests against the store
// keyring (auth.Verify — constant-time, runtime-rotatable keys, D-030). A
// thin back-compat wrapper around AuthMiddleware with a keyring-only
// Authenticator (ae7, D-067) — there is no second verify implementation.
func KeyringMiddleware(kr auth.Keyring, next http.Handler) http.Handler {
	return AuthMiddleware(auth.NewKeyringAuthenticator(kr), next)
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
