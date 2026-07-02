package retrieval

// views.go — the read-time NAMED topic-VIEW apply path (Phase ae9, D-149,
// generalized by D-151). This file defines resolveAndApplyView, the wiring
// method SetTopicViews, and the cache-bypass predicate hasViewApply. It does
// NOT define a second topic-filter function — resolveAndApplyView feeds ae6's
// EXISTING filterByTopicOwnScope (topicfilter.go) a THIRD time over the
// caller's own-scope candidates, exactly like agentfilter.go's
// resolveAgentTopics does a second time. Do not add filtering logic here; add
// it to topicfilter.go if the shared filter itself needs to change.
//
// Fails OPEN by default (D-139/D-036): a views-STORE error degrades to
// unfiltered own-scope results with degraded=true, unless
// retrieval.agent_views.on_policy_error=closed (an explicit operator
// override), in which case it degrades to NO results (still degraded=true). A
// view NAME with no matching row (store.ErrNotFound) is an UNBOUND subject —
// unfiltered but NOT degraded, exactly like ae1's unbound-agent case.
//
// The subject is ALWAYS identity-derived (identity.ResolveViewSubject) —
// scope.Agent (read-time only, from _meta via ae1) or, when absent, the
// SERVER-INJECTED CredentialKeyID (never a wire field, see Request docs) —
// never a caller-supplied argument, so a caller can only ever apply its own
// subject's views (P3-honest, AC-5).

import (
	"context"
	"errors"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// SetTopicViews wires the named-view read-path resolver (ae9, D-149) plus its
// two apply-time knobs (retrieval.agent_views.on_policy_error/
// subject_precedence). A nil st makes resolveAndApplyView fully inert
// (zero-config off, byte-identical to pre-ae9 behaviour) — same shape as
// SetGrants (grants.go) / WithAgentPolicy (agentfilter.go). Reuses the SAME
// retrieval.agent_views.enabled master switch WithAgentPolicy sets (D-151: one
// shared enable knob for both ae1's default binding and ae9's named views).
// Call after New, before serving; not safe to call concurrently with Retrieve.
func (r *Retriever) SetTopicViews(st store.TopicViewStore, onPolicyErrorClosed bool, subjectPrecedence string) *Retriever {
	r.viewSt = st
	r.onPolicyErrorClosed = onPolicyErrorClosed
	r.subjectPrecedence = subjectPrecedence
	return r
}

// hasViewApply reports whether the CURRENT request could actually resolve and
// apply a named view — i.e. whether the result cache must be bypassed for it
// (D-149, see the cache-bypass note in retrieval.go). True only when the
// feature is enabled, a TopicViewStore is wired, AND the request carries a
// resolvable subject (scope.Agent or req.CredentialKeyID). An unbound request
// (agentFilterOn but no subject at all) is cache-safe exactly like ae1's
// unbound-agent case, since no view can ever apply to it.
func (r *Retriever) hasViewApply(scope identity.Scope, req Request) bool {
	if !r.agentFilterOn || r.viewSt == nil {
		return false
	}
	return scope.Agent != "" || req.CredentialKeyID != ""
}

// resolveAndApplyView narrows the caller's OWN-scope candidate IDs through the
// named view bound to the request's subject. It reuses ae6's
// filterByTopicOwnScope (built once, in ae6) with the resolved view's
// allow/deny keys — ae9 defines NO second filter. FAILS OPEN by default
// (D-139): a views-store error returns the input IDs unchanged with
// degraded=true, unless on_policy_error=closed (return nil, degraded=true).
func (r *Retriever) resolveAndApplyView(
	ctx context.Context, scope identity.Scope, req Request, ids []string,
) (kept []string, degraded bool) {
	if !r.agentFilterOn || r.viewSt == nil {
		return ids, false
	}

	kind, subjectID, ok := identity.ResolveViewSubject(scope.Agent, req.CredentialKeyID, r.subjectPrecedence)
	if !ok {
		return ids, false // no subject at all — an unbound caller, not an error
	}

	viewName := req.ViewName
	if viewName == "" {
		viewName = "default"
	}

	view, err := r.viewSt.GetView(ctx, scope, kind, subjectID, viewName)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return ids, false // UNBOUND for this view name — unfiltered, not degraded
		}
		r.log.WarnContext(ctx, "retrieval: GetView failed — applying agent_views.on_policy_error posture (D-139/D-149)",
			"scope", scope.String(), "subject_kind", kind, "subject_id", subjectID, "view_name", viewName, "err", err)
		if r.onPolicyErrorClosed {
			return nil, true // operator override: fail CLOSED (drop results)
		}
		return ids, true // default: fail OPEN (D-139-aligned)
	}

	kept, deg := r.filterByTopicOwnScope(ctx, scope, ids, view.AllowTopics, view.DenyTopics)
	return kept, deg
}
