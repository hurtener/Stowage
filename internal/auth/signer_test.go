// signer_test.go is the test-only JWT signer (L1 — verify-never-mint, AC-2).
// It mints golden fixtures for validator_test.go and jwks_test.go. Stowage
// itself NEVER signs a token in shipped code — no non-_test.go file in
// internal/auth references a private key type or SignedString (see
// TestNoMintingOutsideTestFiles in validator_test.go, the AC-2 grep gate).
package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testSigner mints tokens for one (kid, alg) asymmetric key pair.
type testSigner struct {
	kid    string
	alg    string
	method jwt.SigningMethod
	priv   any // *rsa.PrivateKey or *ecdsa.PrivateKey
	pub    any // *rsa.PublicKey or *ecdsa.PublicKey
}

var testSigningMethods = map[string]jwt.SigningMethod{
	"RS256": jwt.SigningMethodRS256,
	"RS384": jwt.SigningMethodRS384,
	"RS512": jwt.SigningMethodRS512,
	"ES256": jwt.SigningMethodES256,
	"ES384": jwt.SigningMethodES384,
	"ES512": jwt.SigningMethodES512,
}

var testECCurves = map[string]elliptic.Curve{
	"ES256": elliptic.P256(),
	"ES384": elliptic.P384(),
	"ES512": elliptic.P521(),
}

// testSignerSeq gives each testSigner a unique kid even when two signers
// share the same alg (e.g. a key-rotation test with two RS256 signers) —
// kid must be a per-KEY identifier, not a per-algorithm one.
var testSignerSeq atomic.Int64

// newTestSigner generates a fresh asymmetric key pair for alg (one of
// AllowedAlgorithms) and wraps it in a testSigner.
func newTestSigner(t testing.TB, alg string) *testSigner {
	t.Helper()
	method, ok := testSigningMethods[alg]
	if !ok {
		t.Fatalf("newTestSigner: unsupported alg %q", alg)
	}
	kid := fmt.Sprintf("kid-%s-%d", alg, testSignerSeq.Add(1))

	switch alg {
	case "RS256", "RS384", "RS512":
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("generate rsa key: %v", err)
		}
		return &testSigner{kid: kid, alg: alg, method: method, priv: priv, pub: &priv.PublicKey}
	default: // ES256/384/512
		priv, err := ecdsa.GenerateKey(testECCurves[alg], rand.Reader)
		if err != nil {
			t.Fatalf("generate ecdsa key: %v", err)
		}
		return &testSigner{kid: kid, alg: alg, method: method, priv: priv, pub: &priv.PublicKey}
	}
}

// keySet returns a staticKeySet exposing ONLY s's public key.
func (s *testSigner) keySet() *staticKeySet {
	return newStaticKeySet(map[string]jwkKey{s.kid: {key: s.pub, alg: s.alg}})
}

// sign mints a token with the given claims, signed with s's private key.
func (s *testSigner) sign(t testing.TB, claims jwt.MapClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(s.method, claims)
	tok.Header["kid"] = s.kid
	signed, err := tok.SignedString(s.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

// validClaims returns a Harbor-shaped claim set that verifies cleanly against
// the default test issuer/audience, anchored at now.
func validClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"tenant":  "acme",
		"user":    "alice",
		"session": "s1",
		"iss":     "harbor",
		"aud":     "stowage",
		"sub":     "alice",
		"scopes":  []string{"read"},
		"iat":     now.Unix(),
		"exp":     now.Add(time.Hour).Unix(),
	}
}

// signNone mints an unsigned ("alg":"none") token — the classic
// algorithm-confusion vector (AC-1).
func signNone(t testing.TB, claims jwt.MapClaims, kid string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none: %v", err)
	}
	return signed
}

// signHS256WithKey mints an HS256 token using key as the raw HMAC secret —
// the "RSA public key used as HMAC secret" algorithm-confusion attack (AC-1).
func signHS256WithKey(t testing.TB, claims jwt.MapClaims, kid string, key []byte) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(key)
	if err != nil {
		t.Fatalf("sign hs256: %v", err)
	}
	return signed
}
