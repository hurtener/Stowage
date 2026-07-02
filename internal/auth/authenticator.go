package auth

import (
	"context"
	"strings"

	"github.com/hurtener/stowage/internal/identity"
)

// SessionHeader is the per-request session header ported from Harbor
// (middleware.go:143, D-137): in ModeJWT a non-empty value REPLACES the
// token's session claim on the resolved Scope, so session stays per-call even
// over one long-lived connection. Tenant/User always stay token-verified — a
// header can never widen them.
const SessionHeader = "X-Harbor-Session"

// Mode selects the verify path. Keyring is the zero-config default.
type Mode string

const (
	// ModeKeyring is the pre-existing static store-backed API keys (D-030).
	ModeKeyring Mode = "keyring"
	// ModeJWT is the Harbor-aligned JWT verification this phase adds.
	ModeJWT Mode = "jwt"
)

// Authenticator resolves a request's bearer credential into an identity Scope
// + Role, by whichever mode it was built with. Immutable after construction —
// safe for concurrent use (§5 reusable-artifact discipline); this is the
// D-067 core BOTH the internal/api and internal/mcpserver middlewares call —
// there is no second verify implementation.
type Authenticator struct {
	mode      Mode
	keyring   Keyring   // set when mode == ModeKeyring
	validator Validator // set when mode == ModeJWT
}

// NewKeyringAuthenticator builds the zero-config default: static
// store-backed API keys (D-030), verified via Verify.
func NewKeyringAuthenticator(kr Keyring) *Authenticator {
	return &Authenticator{mode: ModeKeyring, keyring: kr}
}

// NewJWTAuthenticator builds a Harbor-aligned JWT verifier authenticator
// (this phase) around an already-constructed Validator (cmd/stowage/main.go
// builds the Validator's KeySet — JWKS or static — at boot).
func NewJWTAuthenticator(v Validator) *Authenticator {
	return &Authenticator{mode: ModeJWT, validator: v}
}

// Authenticate turns the raw Authorization header value and the optional
// X-Harbor-Session header value into the request Scope + Role + the verified
// credential's key id (ae9, D-149 — the "key" topic-view subject fallback;
// added alongside Role rather than as a separate lookup, since both modes
// already resolve the full credential in one pass here). Never logs
// credentials (CLAUDE.md §7).
//
//   - ModeKeyring: Verify(keyring, token) -> Scope{Tenant}, Role from Key.Role,
//     keyID from Key.ID.
//   - ModeJWT: validator.Validate(ctx, token) -> Scope{Tenant,User,Session};
//     a non-empty sessionHdr REPLACES the token's session claim (D-137);
//     Role = RoleAdmin iff the verified scopes contain "admin", else RoleAgent
//     (plan §Findings I'm departing from, departure #4). keyID is always ""
//     in ModeJWT — a verified JWT is not a stored *auth.Key, so the "key" view
//     subject simply never resolves for a JWT-mode caller (the "agent"
//     subject, sourced from _meta/claims independent of auth mode, still
//     works).
func (a *Authenticator) Authenticate(ctx context.Context, authz, sessionHdr string) (identity.Scope, Role, string, error) {
	token, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok || token == "" {
		return identity.Scope{}, "", "", ErrTokenMissing
	}

	if a.mode == ModeJWT {
		scope, role, err := a.authenticateJWT(ctx, token, sessionHdr)
		return scope, role, "", err
	}
	return a.authenticateKeyring(token)
}

func (a *Authenticator) authenticateKeyring(token string) (identity.Scope, Role, string, error) {
	key, err := Verify(a.keyring, token)
	if err != nil {
		return identity.Scope{}, "", "", err
	}
	return identity.Scope{Tenant: key.TenantID}, key.Role, key.ID, nil
}

func (a *Authenticator) authenticateJWT(ctx context.Context, token, sessionHdr string) (identity.Scope, Role, error) {
	verified, err := a.validator.Validate(ctx, token)
	if err != nil {
		return identity.Scope{}, "", err
	}

	scope := verified.Scope
	if sessionHdr != "" {
		scope.Session = sessionHdr
	}

	role := RoleAgent
	for _, s := range verified.Scopes {
		if s == "admin" {
			role = RoleAdmin
			break
		}
	}

	return scope, role, nil
}
