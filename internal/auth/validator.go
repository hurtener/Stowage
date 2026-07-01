// validator.go: Stowage's verify-never-mint JWT verifier (phase ae7, RFC
// §5.5, §9.5). It reimplements the shape of Harbor's on-disk verifier
// (repos/Harbor/internal/protocol/auth/auth.go — cross-module internal/, so
// Go forbids importing it; the shape is ported, not linked) on
// golang-jwt/jwt/v5. Stowage never signs a token — see validator_test.go's
// test-only signer (AC-2, L1).

package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hurtener/stowage/internal/identity"
)

// AllowedAlgorithms is the asymmetric-only signing-method allowlist. HS*/none
// are structurally excluded — never added here — which is what makes
// algorithm-confusion (an HS256 token "signed" with an RSA public key used as
// the HMAC secret) structurally impossible: jwt.WithValidMethods rejects any
// method outside this list at the PARSER, before a keyfunc or key material is
// ever consulted (AC-1).
var AllowedAlgorithms = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}

// The typed sentinels a Validate call can return. mapParserError translates
// golang-jwt's own errors onto these; ErrJWKSStale (jwks.go) propagates
// un-masked from a KeySet through the same path (fail-closed, D-147).
var (
	ErrTokenMissing         = errors.New("auth: token missing")
	ErrTokenMalformed       = errors.New("auth: token malformed")
	ErrAlgNotAllowed        = errors.New("auth: signing algorithm not allowed")
	ErrSignatureInvalid     = errors.New("auth: token signature invalid")
	ErrTokenExpired         = errors.New("auth: token expired")
	ErrTokenNotYetValid     = errors.New("auth: token not yet valid")
	ErrUnknownKey           = errors.New("auth: unknown signing key")
	ErrIdentityClaimMissing = errors.New("auth: identity claim missing")
	ErrAudienceMismatch     = errors.New("auth: audience mismatch")
	ErrIssuerMismatch       = errors.New("auth: issuer mismatch")
)

// KeySet resolves a JWT `kid` header to the (public key, alg) pair that
// should have signed it. JWKSKeySet (jwks.go) and staticKeySet
// (statickeyset.go) are the two drivers; the test signer (validator_test.go)
// also builds a staticKeySet.
type KeySet interface {
	KeyByID(kid string) (key crypto.PublicKey, alg string, err error)
}

// Verified is the trustworthy result of a successful Validate call.
type Verified struct {
	// Scope carries Tenant/User/Session from the mandatory triple (Project is
	// always empty — the JWT carries no project claim; project is a
	// host-routing dimension, not an auth claim).
	Scope identity.Scope
	// Scopes is the verified `scopes` claim (may be empty). The Authenticator
	// maps scopes containing "admin" to RoleAdmin (departure #4, plan §Findings
	// I'm departing from).
	Scopes []string
	// Subject is the audited `sub` claim (sub==user per Harbor convention).
	Subject string
	// Issuer is the audited `iss` claim.
	Issuer string
}

// Validator verifies a raw bearer JWT and extracts its verified identity.
// Never mints a token (L1) — see NewValidator.
type Validator interface {
	Validate(ctx context.Context, rawToken string) (Verified, error)
}

// validator is the Validator implementation. Immutable after NewValidator
// returns — safe for concurrent use (§5 reusable-artifact discipline); both
// HTTP seams' shared Authenticator calls one instance.
type validator struct {
	keys       KeySet
	issuer     string   // exact-match; empty disables the check
	audience   string   // containment-match (D-136); empty disables the check
	algorithms []string // the configured subset of AllowedAlgorithms this instance accepts
	clock      func() time.Time
	log        *slog.Logger
	parser     *jwt.Parser
}

// Option configures a Validator built by NewValidator.
type Option func(*validator)

// WithIssuer sets the expected `iss` claim (exact-match). The default ("")
// disables the check.
func WithIssuer(iss string) Option { return func(v *validator) { v.issuer = iss } }

// WithAudience sets the expected `aud` claim (containment-match, D-136). The
// default ("") disables the check.
func WithAudience(aud string) Option { return func(v *validator) { v.audience = aud } }

// WithAlgorithms narrows the accepted signing methods to the given subset of
// AllowedAlgorithms (the `auth.algorithms` config knob). The default (nil) is
// the full six-algorithm allowlist. NewValidator rejects a non-asymmetric
// entry.
func WithAlgorithms(algs []string) Option {
	return func(v *validator) {
		if len(algs) > 0 {
			v.algorithms = algs
		}
	}
}

// WithClock injects the time source used for exp/nbf checks (L1 — the test
// signer pairs with this for deterministic golden tests). The default is
// time.Now.
func WithClock(clock func() time.Time) Option { return func(v *validator) { v.clock = clock } }

// WithLogger sets the audit logger. The default is slog.Default(). Audit
// records ONLY structural, non-secret fields (kid, iss, reason) — never the
// raw token or claims body (CLAUDE.md §7).
func WithLogger(log *slog.Logger) Option { return func(v *validator) { v.log = log } }

// NewValidator builds a Validator against keys. The parser is constructed
// with jwt.WithValidMethods(the configured algorithm subset) +
// jwt.WithoutClaimsValidation() — the library's own claims checks are
// disabled because Validate owns exp/nbf/iss/aud against an injectable clock
// (AC-1).
func NewValidator(keys KeySet, opts ...Option) (Validator, error) {
	if keys == nil {
		return nil, errors.New("auth: NewValidator: keys is required")
	}
	v := &validator{
		keys:       keys,
		algorithms: AllowedAlgorithms,
		clock:      time.Now,
		log:        slog.Default(),
	}
	for _, opt := range opts {
		opt(v)
	}
	for _, a := range v.algorithms {
		if !isAllowedAlgString(a) {
			return nil, fmt.Errorf("auth: NewValidator: algorithm %q is not in the asymmetric allowlist", a)
		}
	}
	v.parser = jwt.NewParser(jwt.WithValidMethods(v.algorithms), jwt.WithoutClaimsValidation())
	return v, nil
}

// Validate implements Validator.
func (v *validator) Validate(ctx context.Context, rawToken string) (Verified, error) {
	_ = ctx // reserved: a future KeySet driver may honor cancellation on refresh
	if rawToken == "" {
		return Verified{}, ErrTokenMissing
	}

	alg, algErr := peekAlg(rawToken)
	if algErr != nil {
		v.audit(peekKid(rawToken), "", reasonForWire(ErrTokenMalformed))
		return Verified{}, fmt.Errorf("%w: %w", ErrTokenMalformed, algErr)
	}

	token, err := v.parser.ParseWithClaims(rawToken, jwt.MapClaims{}, v.keyfunc)
	if err != nil {
		mapped := v.mapParserError(err, alg)
		v.audit(peekKid(rawToken), "", reasonForWire(mapped))
		return Verified{}, mapped
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return Verified{}, fmt.Errorf("%w: unexpected claims type", ErrTokenMalformed)
	}

	now := v.clock()

	// exp is MANDATORY: no-exp is treated as expired (plan §Design).
	exp, expErr := claims.GetExpirationTime()
	if expErr != nil || exp == nil || exp.Before(now) {
		v.audit(peekKid(rawToken), "", reasonForWire(ErrTokenExpired))
		return Verified{}, ErrTokenExpired
	}

	// nbf is optional.
	if nbf, nbfErr := claims.GetNotBefore(); nbfErr == nil && nbf != nil && nbf.After(now) {
		v.audit(peekKid(rawToken), "", reasonForWire(ErrTokenNotYetValid))
		return Verified{}, ErrTokenNotYetValid
	}

	iss, _ := claims.GetIssuer()
	if v.issuer != "" && iss != v.issuer {
		v.audit(peekKid(rawToken), iss, reasonForWire(ErrIssuerMismatch))
		return Verified{}, ErrIssuerMismatch
	}

	if !audienceContains(claims, v.audience) {
		v.audit(peekKid(rawToken), iss, reasonForWire(ErrAudienceMismatch))
		return Verified{}, ErrAudienceMismatch
	}

	scope, err := extractScope(claims)
	if err != nil {
		v.audit(peekKid(rawToken), iss, reasonForWire(ErrIdentityClaimMissing))
		return Verified{}, err
	}

	sub, _ := claims.GetSubject()

	return Verified{
		Scope:   scope,
		Scopes:  extractScopes(claims),
		Subject: sub,
		Issuer:  iss,
	}, nil
}

// keyfunc resolves the verification key for a parsed-but-unverified token.
// Belt-and-braces (ported posture, plan §Design): re-checks the method is
// allowed (WithValidMethods already rejected a disallowed method at the
// parser BEFORE this ever runs — this is pure defense in depth), rejects a
// kid whose resolved alg disagrees with the header alg, and final
// type-switches that the resolved key is *rsa.PublicKey/*ecdsa.PublicKey (a
// symmetric key returned by a misbehaving KeySet is rejected here too).
func (v *validator) keyfunc(t *jwt.Token) (any, error) {
	if !isAllowedMethod(t.Method, v.algorithms) {
		return nil, ErrAlgNotAllowed
	}

	kidRaw, ok := t.Header["kid"]
	if !ok {
		return nil, fmt.Errorf("%w: token has no kid header", ErrUnknownKey)
	}
	kid, ok := kidRaw.(string)
	if !ok || kid == "" {
		return nil, fmt.Errorf("%w: kid header is not a string", ErrUnknownKey)
	}

	key, alg, err := v.keys.KeyByID(kid)
	if err != nil {
		if errors.Is(err, ErrJWKSStale) {
			return nil, err // propagate un-masked — fail closed (D-147)
		}
		return nil, fmt.Errorf("%w: %w", ErrUnknownKey, err)
	}
	if alg != t.Method.Alg() {
		return nil, fmt.Errorf("%w: kid %q resolves to alg %q, header says %q", ErrUnknownKey, kid, alg, t.Method.Alg())
	}

	switch key.(type) {
	case *rsa.PublicKey, *ecdsa.PublicKey:
		return key, nil
	default:
		return nil, fmt.Errorf("%w: kid %q resolved to non-asymmetric key material", ErrUnknownKey, kid)
	}
}

// mapParserError translates a golang-jwt ParseWithClaims error onto our typed
// sentinels. golang-jwt's own validMethods rejection and a genuine bad
// signature both surface as the SAME jwt.ErrTokenSignatureInvalid (the
// library deliberately does not leak which failure occurred at the wire
// level — https://auth0.com/blog/critical-vulnerabilities-in-json-web-token-libraries/).
// alg is the pre-parse header peek (peekAlg), used ONLY to recover a
// diagnostic distinction between the two for audit/wire purposes; it is never
// used as an enforcement gate — jwt.WithValidMethods (NewValidator) remains
// the sole enforcement point (AC-1).
func (v *validator) mapParserError(err error, alg string) error {
	switch {
	case errors.Is(err, ErrJWKSStale):
		return ErrJWKSStale
	case errors.Is(err, ErrUnknownKey):
		return ErrUnknownKey
	case errors.Is(err, ErrAlgNotAllowed):
		return ErrAlgNotAllowed
	case errors.Is(err, jwt.ErrTokenMalformed):
		return fmt.Errorf("%w: %w", ErrTokenMalformed, err)
	case errors.Is(err, jwt.ErrTokenSignatureInvalid):
		if !isAllowedAlgIn(alg, v.algorithms) {
			return ErrAlgNotAllowed
		}
		return ErrSignatureInvalid
	case errors.Is(err, jwt.ErrTokenUnverifiable):
		return ErrUnknownKey
	default:
		return ErrSignatureInvalid // conservative: fail closed on an unrecognized parser error
	}
}

// audit logs a rejection with ONLY structural, non-secret fields — never the
// raw token or the claims body (CLAUDE.md §7).
func (v *validator) audit(kid, iss, reason string) {
	if v.log == nil {
		return
	}
	v.log.Warn("auth: jwt verification rejected", "kid", kid, "iss", iss, "reason", reason)
}

// extractScope enforces the mandatory tenant/user/session triple (the
// identity-type departure, plan §Findings I'm departing from): each must be a
// non-empty string claim, else ErrIdentityClaimMissing. Project is always
// left empty — the JWT carries no project claim.
func extractScope(claims jwt.MapClaims) (identity.Scope, error) {
	tenant, tenantOK := stringClaim(claims, "tenant")
	if !tenantOK || tenant == "" {
		return identity.Scope{}, fmt.Errorf("%w: tenant", ErrIdentityClaimMissing)
	}
	user, userOK := stringClaim(claims, "user")
	if !userOK || user == "" {
		return identity.Scope{}, fmt.Errorf("%w: user", ErrIdentityClaimMissing)
	}
	session, sessionOK := stringClaim(claims, "session")
	if !sessionOK || session == "" {
		return identity.Scope{}, fmt.Errorf("%w: session", ErrIdentityClaimMissing)
	}
	return identity.Scope{Tenant: tenant, User: user, Session: session}, nil
}

// stringClaim returns claims[key] as a string and whether it was present AND
// string-typed (a present-but-wrong-typed claim reports ok=false, matching a
// missing claim — both are unusable identity material).
func stringClaim(claims jwt.MapClaims, key string) (string, bool) {
	raw, ok := claims[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	return s, ok
}

// extractScopes reads the optional `scopes` claim, accepting a string,
// []string, or []any-of-strings (the same permissive shapes RFC 7519 allows
// for `aud`). Returns nil when absent or unusable.
func extractScopes(claims jwt.MapClaims) []string {
	raw, ok := claims["scopes"]
	if !ok {
		return nil
	}
	switch v := raw.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// audienceContains implements D-136: the `aud` claim may be a string OR an
// array of strings per RFC 7519 §4.1.3; the check passes iff configured is
// contained in it. An empty configured audience disables the check entirely.
func audienceContains(claims jwt.MapClaims, configured string) bool {
	if configured == "" {
		return true
	}
	raw, ok := claims["aud"]
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case string:
		return v == configured
	case []string:
		for _, s := range v {
			if s == configured {
				return true
			}
		}
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == configured {
				return true
			}
		}
	}
	return false
}

// isAllowedMethod reports whether m's alg identifier is in algorithms.
func isAllowedMethod(m jwt.SigningMethod, algorithms []string) bool {
	if m == nil {
		return false
	}
	return isAllowedAlgIn(m.Alg(), algorithms)
}

// isAllowedAlgIn reports whether alg is present in algorithms.
func isAllowedAlgIn(alg string, algorithms []string) bool {
	for _, a := range algorithms {
		if a == alg {
			return true
		}
	}
	return false
}

// isAllowedAlgString reports whether alg is one of the six AllowedAlgorithms
// (the full asymmetric allowlist, independent of any instance's configured
// subset) — used to validate a WithAlgorithms([]string) input and by JWK
// parsing (jwks.go) to reject a non-asymmetric/unsupported `alg` entry.
func isAllowedAlgString(alg string) bool {
	return isAllowedAlgIn(alg, AllowedAlgorithms)
}

// peekAlg decodes ONLY the JWT header segment (no signature verification) to
// read the `alg` field. Used to recover a diagnostic alg-not-allowed vs
// bad-signature distinction (mapParserError) — it never gates verification;
// jwt.WithValidMethods (the parser built in NewValidator) is the sole
// enforcement point (AC-1).
func peekAlg(rawToken string) (string, error) {
	header, err := peekHeader(rawToken)
	if err != nil {
		return "", err
	}
	if header.Alg == "" {
		return "", errors.New("token header has no alg")
	}
	return header.Alg, nil
}

// peekKid decodes ONLY the JWT header segment to read `kid`, for audit
// logging when full parsing fails before the keyfunc ever ran. Returns ""
// on any decode failure — never the raw token or claims.
func peekKid(rawToken string) string {
	header, err := peekHeader(rawToken)
	if err != nil {
		return ""
	}
	return header.Kid
}

type jwtHeaderPeek struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

func peekHeader(rawToken string) (jwtHeaderPeek, error) {
	parts := strings.SplitN(rawToken, ".", 3)
	if len(parts) != 3 {
		return jwtHeaderPeek{}, errors.New("token does not have three segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return jwtHeaderPeek{}, fmt.Errorf("decode header: %w", err)
	}
	var header jwtHeaderPeek
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return jwtHeaderPeek{}, fmt.Errorf("decode header json: %w", err)
	}
	return header, nil
}

// reasonForWire derives a wire-safe, sentinel-name-only reason string for a
// Validate/Authenticate error — NEVER the wrapped kid/detail (CLAUDE.md §7).
// Both HTTP surfaces (internal/api, internal/mcpserver) use it to keep their
// existing surface-specific error-body style while sharing one reason
// vocabulary (plan §"Error responses stay surface-specific").
func reasonForWire(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrTokenMissing):
		return "token_missing"
	case errors.Is(err, ErrTokenMalformed):
		return "token_malformed"
	case errors.Is(err, ErrAlgNotAllowed):
		return "alg_not_allowed"
	case errors.Is(err, ErrSignatureInvalid):
		return "signature_invalid"
	case errors.Is(err, ErrTokenExpired):
		return "token_expired"
	case errors.Is(err, ErrTokenNotYetValid):
		return "token_not_yet_valid"
	case errors.Is(err, ErrUnknownKey):
		return "unknown_key"
	case errors.Is(err, ErrIdentityClaimMissing):
		return "identity_claim_missing"
	case errors.Is(err, ErrAudienceMismatch):
		return "audience_mismatch"
	case errors.Is(err, ErrIssuerMismatch):
		return "issuer_mismatch"
	case errors.Is(err, ErrJWKSStale):
		return "jwks_stale"
	case errors.Is(err, ErrBadCredential), errors.Is(err, ErrRevokedKey), errors.Is(err, ErrKeyNotFound), errors.Is(err, ErrInvalidKey):
		return "invalid_credential"
	default:
		return "auth_failed"
	}
}

// ReasonForWire is the exported form of reasonForWire — internal/api and
// internal/mcpserver call it to embed a sentinel-name-only reason in their
// own error-body style without importing validator internals or re-deriving
// the mapping (CLAUDE.md §7: never the raw token/claims/detail).
func ReasonForWire(err error) string { return reasonForWire(err) }
