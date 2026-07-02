// resolve.go — Phase ae8 (D-148): the ONE pure function that merges every
// active identity source (the credential tenant, verified JWT claims — ae7,
// host-injected _meta — ae2, and the legacy D-125 args) into the effective
// READ identity.Scope, under the D-137 precedence (verified JWT claims >
// _meta > args) and resolution rule (a dimension the credential PINS rejects
// a disagreeing assertion; a dimension it lets the connection ASSERT accepts
// one). See docs/plans/phase-ae8-effective-scope.md and docs/decisions.md
// D-148 (the resolver + the two knobs) and D-150 (session never scopes a
// read).
//
// ResolveReadScope does NO I/O — no gateway, no store — so it is pure,
// concurrency-safe, and works unchanged in the D-036 degraded path. Every
// single-user read surface (SDK, HTTP, MCP) builds its read scope through
// this one function via a thin, surface-specific source-gathering adapter
// (D-067/D-073 one logic core; D-140 sanctions the adapters differing in
// which fields they populate).
package identity

import "errors"

// ReadPosture governs the empty-identity fallback on the read path
// (retrieval.read_posture, D-137 knob 2). It is NOT a store distinction —
// buildScopeWhere treats an empty Scope.User as tenant-wide either way; the
// posture decides whether the resolver ALLOWS that empty-user read or
// refuses it BEFORE any store call.
type ReadPosture int

const (
	// PostureCompatible is the default: an omitted user AND agent falls back
	// to a tenant-wide read — today's behaviour, byte-identical.
	PostureCompatible ReadPosture = iota
	// PostureStrict refuses a read that resolves to neither a user nor an
	// agent (ErrIdentityRequired), before any store call.
	PostureStrict
)

// ParsePosture maps the retrieval.read_posture config value ("compatible" |
// "strict") onto a ReadPosture. Any value other than the literal "strict"
// resolves to PostureCompatible — config.Validate is the fail-loud enum gate
// (D-034); this mapping is deliberately permissive so a not-yet-validated
// zero-value Config (e.g. in a unit test) still resolves to the safe,
// byte-identical default.
func ParsePosture(s string) ReadPosture {
	if s == "strict" {
		return PostureStrict
	}
	return PostureCompatible
}

// IdentitySources is the raw, per-source identity gathered at a read entry
// point, BEFORE precedence is applied. ResolveReadScope applies the
// precedence (verified JWT claims > _meta > D-125 args — JWT-over-assertion
// tiering per D-137, _meta-over-arg sub-order per the charter identity-model
// table / ae2's metaElseArg) and the D-137 resolution rule internally, so
// every surface resolves identity identically (D-067 one logic core). A
// surface adapter fills only the fields its transport carries and leaves the
// rest zero: MCP fills Meta* from server.RequestMeta (ae2) plus Arg* from
// tool args; HTTP/SDK have no _meta channel (D-140) and fill Claim* from the
// verified JWT (ae7, HTTP only) plus Arg* from request/call args, stamping
// Scope.Agent from their own D-140-sanctioned agent arg AFTER resolution
// instead of through MetaAgent (see the HTTP/SDK adapters).
type IdentitySources struct {
	// Credential — always trustworthy. Tenant is the P3 authorization boundary.
	Tenant string // credential/JWT-verified tenant; PINNED always
	// CredUser is the user bound to the credential (ae7 JWT `user` claim);
	// "" for a bare keyring key — the pre-ae7 world where nothing is pinned.
	CredUser string
	// CanAssertUser is the per-credential capability to override a pinned
	// user (JWT scope memory:assert-user / a keyring flag), populated by a
	// post-ae7 phase. Always false today.
	CanAssertUser bool

	// Verified JWT claims (ae7) — highest-precedence connection assertions.
	ClaimTenant  string
	ClaimUser    string
	ClaimSession string

	// Host-injected _meta (ae2) — connection assertions. MCP-only (D-140).
	MetaTenant  string
	MetaUser    string
	MetaSession string
	MetaAgent   string // read-time identity only; never persisted, never a WHERE
	MetaProject string

	// Legacy D-125 args — omittable model/caller args, lowest precedence.
	ArgUser    string
	ArgSession string
	ArgProject string
}

// ResolveOptions carries the two D-137 knobs, threaded from config at wiring
// time (never a per-request argument — nothing new to fuzz on the wire).
type ResolveOptions struct {
	Posture      ReadPosture // retrieval.read_posture
	Multiplexing bool        // identity.multiplexing (global interim flag; per-credential via CanAssertUser post-ae7)
}

// New ae8 sentinels. ErrTenantMismatch is NOT redefined here — it already
// exists (ae2, identity.go) and is reused verbatim (D-148 departure #... the
// resolver realizes D-138's tenant guard, it does not reinvent it).
var (
	// ErrUserConflict is returned when the credential pins a user and a
	// disagreeing assertion is not authorized (multiplexing off and no
	// per-credential CanAssertUser capability).
	ErrUserConflict = errors.New("identity: assertion disagrees with the credential-pinned user")
	// ErrIdentityRequired is returned in strict posture when neither a user
	// nor an agent resolved — the resolver refuses the silent tenant-wide
	// fallback before any store call.
	ErrIdentityRequired = errors.New("identity: strict read posture requires a resolved user or agent")
)

// ResolveReadScope merges every active identity source into the effective
// READ Scope, honoring the D-137 resolution rule and read posture. It does NO
// I/O (pure, gateway-free, store-free) and is safe for concurrent reuse. It
// NEVER returns a Scope with an empty Tenant on a nil error (P3).
//
// Deliberate signature departure from the plan's literal `(Scope, error)`:
// resolution step 2 requires the effective SESSION to be resolved under the
// same D-137 precedence yet NEVER placed on the returned Scope (D-150 —
// Scope.Session must stay empty on every read, or buildScopeWhere silently
// narrows to one session and cross-session recall breaks). A two-return
// signature cannot carry that value out-of-band, so this returns
// (Scope, session string, error); callers route session to their own
// relevance sink (retrieval.Request.SessionID / playbook.Options.SessionID)
// and MUST NOT assign it to Scope.Session.
//
// Errors are sentinels:
//   - ErrTenantMismatch   — a claim/_meta tenant disagrees with the credential (D-138)
//   - ErrUserConflict     — a disagreeing user assertion is not authorized
//   - ErrIdentityRequired — strict posture and neither user nor agent resolved
//   - a wrapped ErrInvalidScope — the resolved Tenant is empty (P3; should not
//     occur past a real authentication boundary)
func ResolveReadScope(src IdentitySources, opts ResolveOptions) (Scope, string, error) {
	// Step 1 — tenant PINNED always (P3 boundary). A present-and-disagreeing
	// claim/_meta tenant fails closed (D-138); _meta/a claim may never widen
	// or override the authorization boundary.
	if src.ClaimTenant != "" && src.ClaimTenant != src.Tenant {
		return Scope{}, "", ErrTenantMismatch
	}
	if src.MetaTenant != "" && src.MetaTenant != src.Tenant {
		return Scope{}, "", ErrTenantMismatch
	}

	scope := Scope{Tenant: src.Tenant}

	// Step 2 — session: ALWAYS assertable (Harbor parity; never gated by
	// either knob), but NEVER placed on the read Scope (D-150). Resolved here
	// under the same precedence and returned out-of-band.
	session := firstNonEmpty(src.ClaimSession, src.MetaSession, src.ArgSession)

	// Step 3 — project: assertable, no JWT claim (project is host-routing,
	// not an auth claim).
	scope.Project = firstNonEmpty(src.MetaProject, src.ArgProject)

	// Step 4 — agent: _meta only, read-time (requires ae1's Scope.Agent). Set
	// on the read Scope only; never a write INSERT or a scope WHERE.
	scope.Agent = src.MetaAgent

	// Step 5 — user: PINNED (default) / ASSERTABLE (multiplexing).
	asserted := firstNonEmpty(src.ClaimUser, src.MetaUser, src.ArgUser)
	assertable := opts.Multiplexing || src.CanAssertUser
	switch {
	case src.CredUser == "":
		// Bare keyring (pre-ae7): nothing is pinned — fully back-compat, the
		// args set the user freely, exactly as today.
		scope.User = asserted
	case asserted == "" || asserted == src.CredUser:
		scope.User = src.CredUser
	case assertable:
		scope.User = asserted
	default:
		return Scope{}, "", ErrUserConflict
	}

	// Step 6 — strict refusal (posture, orthogonal to user-pinning). Refuse
	// the tenant-wide read BEFORE any store call.
	if opts.Posture == PostureStrict && scope.User == "" && scope.Agent == "" {
		return Scope{}, "", ErrIdentityRequired
	}

	// Step 7 — validate; Session is deliberately left empty on this read
	// Scope (D-150) — the effective session resolved in step 2 is the
	// caller's responsibility to route to its relevance sink.
	if err := scope.Validate(); err != nil {
		return Scope{}, "", err
	}
	return scope, session, nil
}

// firstNonEmpty returns the first non-empty string in vals, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
