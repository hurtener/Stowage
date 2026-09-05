package mcpserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/gateway"
	"github.com/hurtener/stowage/internal/grants"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/topics"
	"github.com/hurtener/stowage/internal/traces"
	"github.com/hurtener/stowage/internal/views"
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
	// ViewsSvc is the ae9 (D-149/D-151) named-view admin core. May be nil in
	// tests; the makeViewsHandler thin caller returns an error when unwired.
	ViewsSvc *views.Service
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
	// ResolveOpts carries the ae8 (D-148/D-137) read-scope resolution knobs
	// (retrieval.read_posture, identity.multiplexing), threaded from config at
	// construction (main.go) — never a per-request argument. The zero value
	// (PostureCompatible, Multiplexing:false) is the byte-identical default.
	ResolveOpts identity.ResolveOptions
}

// StdioScopeFn returns a ScopeFn that always resolves to a tenant-only scope
// with the given tenant ID. This is the correct posture for stdio mode where
// there is no per-request auth (AC-4 / D-020).
func StdioScopeFn(tenant string) ScopeFn {
	return func(_ context.Context) (identity.Scope, error) {
		return identity.Scope{Tenant: tenant}, nil
	}
}

// New creates a Dockyard *server.Server with all 24 Stowage MCP tools registered:
// the original seven, the D-070 reversibility trio (memory_get, memory_rollback,
// memory_resolve), the D-071 Tier control verbs (memory_flush, memory_branch, and the
// Tier-B memory_grants), the episodic reads (memory_episodes, memory_causal), the
// deterministic scope walk (memory_browse, ae5/D-143), the
// §6c trust verbs (memory_verify, memory_review), the §6c trace export (memory_trace),
// the §6d proactive verbs (memory_suggestions, memory_proactive_config), the
// read-time agent-policy admin (memory_agent_policy, ae1, D-135/D-146/D-151), the
// named per-agent/per-key topic-view admin (memory_views, ae9, D-149/D-151), and the
// Harbor run-completion sink (memory_ingest_run, ae12, D-153).
// It returns an error when any tool fails to register (type mismatch, missing
// handler) — the caller must handle the error and exit non-zero (AGENTS.md §5).
func New(info server.Info, svc *Services) (*server.Server, error) {
	srv, err := server.New(info, &server.Options{Logger: svc.Log})
	if err != nil {
		return nil, err
	}

	if err := declare[IngestInput, IngestOutput]("memory_ingest").
		Describe(toolDescription("memory_ingest")).
		Handler(makeIngestHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Harbor run-completion sink (ae12, D-153): accepts Harbor's pinned
	// RunCompletionPayload (format_version 1) as a tools/call and adapts it
	// internally to verbatim records. Identity is from the verified per-call
	// bearer (D-152) + _meta; the payload's tenant_id/user_id are cross-checked
	// and fail closed on a mismatch (D-138 analog), never scope-authoritative
	// (D-140/D-124). One extraction buffer per run (buffer_key = run_id) with an
	// eager best-effort flush. MCP-only tiering (the auto-save-target pattern is
	// an MCP-host contract — D-153 §4).
	if err := declare[IngestRunInput, IngestRunOutput]("memory_ingest_run").
		Describe(toolDescription("memory_ingest_run")).
		Handler(makeIngestRunHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[RetrieveInput, RetrieveOutput]("memory_retrieve").
		Describe(toolDescription("memory_retrieve")).
		Handler(makeRetrieveHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[PlaybookInput, PlaybookOutput]("memory_playbook").
		Describe(toolDescription("memory_playbook")).
		Handler(makePlaybookHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[EpisodesInput, EpisodesOutput]("memory_episodes").
		Describe(toolDescription("memory_episodes")).
		Handler(makeEpisodesHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[BrowseInput, BrowseOutput]("memory_browse").
		Describe(toolDescription("memory_browse")).
		Handler(makeBrowseHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[CausalInput, CausalOutput]("memory_causal").
		Describe(toolDescription("memory_causal")).
		Handler(makeCausalHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[VerifyInput, VerifyOutput]("memory_verify").
		Describe(toolDescription("memory_verify")).
		Handler(makeVerifyHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[ReviewInput, ReviewOutput]("memory_review").
		Describe(toolDescription("memory_review")).
		Handler(makeReviewHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[TraceInput, traces.Bundle]("memory_trace").
		Describe(toolDescription("memory_trace")).
		Handler(makeTraceHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[DrilldownInput, DrilldownOutput]("memory_drilldown").
		Describe(toolDescription("memory_drilldown")).
		Handler(makeDrilldownHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[FeedbackInput, FeedbackOutput]("memory_feedback").
		Describe(toolDescription("memory_feedback")).
		Handler(makeFeedbackHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[AssertInput, AssertOutput]("memory_assert").
		Describe(toolDescription("memory_assert")).
		Handler(makeAssertHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[TopicsInput, TopicsOutput]("memory_topics").
		Describe(toolDescription("memory_topics")).
		Handler(makeTopicsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Reversibility tools (D-070) — single-purpose, mirroring the HTTP verbs.
	if err := declare[GetInput, GetOutput]("memory_get").
		Describe(toolDescription("memory_get")).
		Handler(makeGetHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[RollbackInput, RollbackOutput]("memory_rollback").
		Describe(toolDescription("memory_rollback")).
		Handler(makeRollbackHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[ResolveInput, ResolveOutput]("memory_resolve").
		Describe(toolDescription("memory_resolve")).
		Handler(makeResolveHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Tier-A control verbs (D-071) — single-user, mirroring the HTTP routes.
	if err := declare[FlushInput, FlushOutput]("memory_flush").
		Describe(toolDescription("memory_flush")).
		Handler(makeFlushHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	if err := declare[BranchInput, BranchOutput]("memory_branch").
		Describe(toolDescription("memory_branch")).
		Handler(makeBranchHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Phase 27: proactive suggestions (RFC §6d, D-087) — single-user tier.
	if err := declare[SuggestionsInput, SuggestionsOutput]("memory_suggestions").
		Describe(toolDescription("memory_suggestions")).
		Handler(makeSuggestionsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Phase 27: proactive governance (RFC §6d, D-087) — admin tier; deliberately
	// ABSENT from the single-user SDK (D-067).
	if err := declare[ProactiveConfigInput, ProactiveConfigOutput]("memory_proactive_config").
		Describe(toolDescription("memory_proactive_config")).
		Handler(makeProactiveConfigHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Tier-B admin verb (D-071) — multi-user; matches the HTTP admin routes,
	// deliberately ABSENT from the single-user SDK (D-067).
	if err := declare[GrantsInput, GrantsOutput]("memory_grants").
		Describe(toolDescription("memory_grants")).
		Handler(makeGrantsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Phase ae1: read-time agent-policy admin (D-135/D-146/D-151) — policy-admin
	// tier; deliberately ABSENT from the single-user SDK (D-067), matching
	// memory_grants' tiering.
	if err := declare[AgentPolicyInput, AgentPolicyOutput]("memory_agent_policy").
		Describe(toolDescription("memory_agent_policy")).
		Handler(makeAgentPolicyHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	// Phase ae9: named per-agent/per-key topic-view admin (D-149/D-151) — view-admin
	// tier; deliberately ABSENT from the single-user SDK (D-067), matching
	// memory_grants/memory_agent_policy's tiering.
	if err := declare[ViewsInput, ViewsOutput]("memory_views").
		Describe(toolDescription("memory_views")).
		Handler(makeViewsHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}

	return srv, nil
}

// AuthMiddleware authenticates HTTP MCP requests via a (D-067)
// *auth.Authenticator — keyring (Verify) or JWT (Validator), depending on how
// a was constructed — and injects the resolved Scope for CtxScopeFn, plus the
// verified credential's key id (ae9, D-149 — stashed via keyIDContextKey so
// KeyIDFromContext can expose it to the retrieve handler; empty in ModeJWT,
// where there is no stored *auth.Key). Never logs credentials (CLAUDE.md §7).
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
		scope, _, keyID, err := a.Authenticate(r.Context(), hdr, r.Header.Get(auth.SessionHeader))
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
		ctx = withKeyID(ctx, keyID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// KeyringMiddleware authenticates HTTP MCP requests against the store
// keyring (auth.Verify — constant-time, runtime-rotatable keys, D-030). A
// thin back-compat wrapper around AuthMiddleware with a keyring-only
// Authenticator (ae7, D-067) — there is no second verify implementation. Also
// stashes the verified key id on the request context (ae9, D-149) via
// AuthMiddleware — see KeyIDFromContext.
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

// keyIDContextKey is the context key AuthMiddleware stashes the verified
// credential's key id under (ae9, D-149).
type keyIDContextKey struct{}

// withKeyID returns a new context carrying keyID (may be "").
func withKeyID(ctx context.Context, keyID string) context.Context {
	return context.WithValue(ctx, keyIDContextKey{}, keyID)
}

// KeyIDFromContext resolves the verified credential's key id injected by
// AuthMiddleware/KeyringMiddleware (ae9, D-149) — the "key" topic-view subject
// fallback used when a request carries no scope.Agent. Returns "" when unset
// (stdio mode, where there is no per-request key, or ModeJWT, where there is
// no stored *auth.Key) — the retrieve handler treats an empty key id exactly
// like an absent one (the key view subject simply does not resolve).
func KeyIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(keyIDContextKey{}).(string)
	return id
}
