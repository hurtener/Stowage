package mcpserver

// metaintake.go — Phase ae2 (D-137/D-138): the ONE shared `_meta` identity intake
// seam every MCP handler calls, instead of open-coding a server.RequestMeta read
// (the CC-memory predecessor's surface-sprawl failure mode, brief 02). It is the
// single place `server.RequestMeta` is called from internal/mcpserver (AC-9).

import (
	"context"
	"fmt"

	"github.com/hurtener/dockyard/runtime/server"

	"github.com/hurtener/stowage/internal/identity"
)

// metaIdentity is the non-authorizing identity carried by the inbound _meta.
// Tenant is deliberately absent — it is NEVER sourced from _meta (D-138).
type metaIdentity struct {
	User    string // _meta.user
	Session string // _meta.session
	Agent   string // _meta.agent_id (read-path only; ae1's Scope.Agent)
}

// readMetaIdentity reads the host-injected _meta (dockyard v1.8 server.RequestMeta)
// and (1) enforces the D-138 tenant guard against the authenticated credTenant,
// (2) extracts the non-authorizing dimensions. It is called by EVERY handler right
// after svc.ScopeFn(ctx): read handlers use the returned identity; write/admin
// handlers call it only for the guard and discard the identity. The returned map
// is dockyard's per-call shallow copy — read-only, never retained past the call.
//
// Guard semantics (fail closed, D-138):
//   - _meta.tenant absent               -> fine (the common case; identical to today).
//   - _meta.tenant present and equal    -> fine, no-op.
//   - _meta.tenant present and different, or non-string -> reject. The error
//     carries no tenant values (neither the injected nor the real one) — a
//     redacted reason.
//
// requestMeta is the SINGLE place internal/mcpserver calls dockyard's inbound
// server.RequestMeta (ae2 AC-9: no per-handler open-coding). Both the write/admin
// guard path (readMetaIdentity, below) and the ae8 read-scope adapter
// (resolveScope, scope.go) read the host-injected _meta through this one wrapper.
func requestMeta(ctx context.Context) map[string]any {
	return server.RequestMeta(ctx) // nil when no _meta was sent
}

func readMetaIdentity(ctx context.Context, credTenant string) (metaIdentity, error) {
	m := requestMeta(ctx) // nil when no _meta was sent
	if v, ok := m["tenant"]; ok {
		s, isStr := v.(string)
		if !isStr || s != credTenant {
			// present-but-mismatched (or malformed) tenant -> reject, no values leaked.
			return metaIdentity{}, fmt.Errorf("mcpserver: %w", identity.ErrTenantMismatch)
		}
	}
	return metaIdentity{
		User:    metaString(m, "user"),
		Session: metaString(m, "session"),
		Agent:   metaString(m, "agent_id"),
	}, nil
}

// metaString returns the string value of key in m. A missing key, a nil map, or
// a non-string value all return "" — a malformed non-authorizing _meta key is
// simply ignored (fail-open on the non-authorizing dimensions; only tenant fails
// closed, in readMetaIdentity above).
func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// metaElseArg returns the host-injected _meta value when set, else the in-band
// arg fallback. The documented precedence: _meta is the host's trusted,
// out-of-band channel and WINS; an in-band arg is model-filled and untrustworthy
// for identity, so it is only the fallback. Equal to today when meta=="" (no
// caller sends _meta yet), i.e. metaElseArg("", arg) == arg.
func metaElseArg(meta, arg string) string {
	if meta != "" {
		return meta
	}
	return arg
}
