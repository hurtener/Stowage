package auth

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/hurtener/stowage/internal/identity"
)

// fixedClock returns a clock func pinned at t.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestValidator builds a Validator around signer's key set with the given
// options, failing the test on error.
func newTestValidator(t *testing.T, signer *testSigner, opts ...Option) Validator {
	t.Helper()
	v, err := NewValidator(signer.keySet(), opts...)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// ---- AC-1: positive verify across all six asymmetric algorithms ----------

func TestValidate_Positive_AllAlgorithms(t *testing.T) {
	for _, alg := range AllowedAlgorithms {
		t.Run(alg, func(t *testing.T) {
			now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
			signer := newTestSigner(t, alg)
			v := newTestValidator(t, signer, WithIssuer("harbor"), WithAudience("stowage"), WithClock(fixedClock(now)))

			token := signer.sign(t, validClaims(now))
			got, err := v.Validate(context.Background(), token)
			if err != nil {
				t.Fatalf("Validate(%s): unexpected error: %v", alg, err)
			}
			want := identity.Scope{Tenant: "acme", User: "alice", Session: "s1"}
			if got.Scope != want {
				t.Errorf("Validate(%s): Scope = %+v, want %+v", alg, got.Scope, want)
			}
			if got.Subject != "alice" {
				t.Errorf("Validate(%s): Subject = %q, want alice", alg, got.Subject)
			}
			if got.Issuer != "harbor" {
				t.Errorf("Validate(%s): Issuer = %q, want harbor", alg, got.Issuer)
			}
			if len(got.Scopes) != 1 || got.Scopes[0] != "read" {
				t.Errorf("Validate(%s): Scopes = %v, want [read]", alg, got.Scopes)
			}
		})
	}
}

// ---- AC-1: the algorithm-confusion CVE family — the load-bearing negatives ----

// TestValidate_AlgConfusion_HS256WithRSAPublicKey pins the classic
// algorithm-confusion attack: an attacker signs a forged token with HS256,
// using the server's own RSA PUBLIC key (which is not secret) as the HMAC
// secret. jwt.WithValidMethods (the parser, NewValidator) must reject this
// BEFORE any keyfunc/key material is consulted — structurally impossible, not
// merely detected.
func TestValidate_AlgConfusion_HS256WithRSAPublicKey(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	pubDER, err := x509.MarshalPKIXPublicKey(signer.pub)
	if err != nil {
		t.Fatalf("marshal pubkey: %v", err)
	}

	forged := signHS256WithKey(t, validClaims(now), signer.kid, pubDER)
	_, err = v.Validate(context.Background(), forged)
	if !errors.Is(err, ErrAlgNotAllowed) {
		t.Fatalf("Validate(HS256-forged): err = %v, want ErrAlgNotAllowed", err)
	}
}

// TestValidate_AlgConfusion_None pins the "alg":"none" unsigned-token attack.
func TestValidate_AlgConfusion_None(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	forged := signNone(t, validClaims(now), signer.kid)
	_, err := v.Validate(context.Background(), forged)
	if !errors.Is(err, ErrAlgNotAllowed) {
		t.Fatalf("Validate(none): err = %v, want ErrAlgNotAllowed", err)
	}
}

func TestValidate_TamperedSignature(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	token := signer.sign(t, validClaims(now))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token does not have 3 segments")
	}
	// Flip a bit in the DECODED signature bytes, then re-encode — guarantees
	// valid base64url (unlike mutating the encoded text directly, which can
	// produce illegal base64 and mask the test as a malformed-token case
	// instead of a signature-invalid one).
	sigBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sigBytes[len(sigBytes)-1] ^= 0x01
	tampered := parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sigBytes)

	_, err = v.Validate(context.Background(), tampered)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Fatalf("Validate(tampered): err = %v, want ErrSignatureInvalid", err)
	}
}

func TestValidate_Expired(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	claims := validClaims(now)
	claims["exp"] = now.Add(-time.Minute).Unix()
	token := signer.sign(t, claims)

	_, err := v.Validate(context.Background(), token)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Validate(expired): err = %v, want ErrTokenExpired", err)
	}
}

func TestValidate_NotYetValid(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	claims := validClaims(now)
	claims["nbf"] = now.Add(time.Hour).Unix()
	token := signer.sign(t, claims)

	_, err := v.Validate(context.Background(), token)
	if !errors.Is(err, ErrTokenNotYetValid) {
		t.Fatalf("Validate(nbf-future): err = %v, want ErrTokenNotYetValid", err)
	}
}

func TestValidate_NoExp(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	claims := validClaims(now)
	delete(claims, "exp")
	token := signer.sign(t, claims)

	_, err := v.Validate(context.Background(), token)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("Validate(no-exp): err = %v, want ErrTokenExpired (no-exp => expired)", err)
	}
}

func TestValidate_MissingIdentityTriple(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	for _, missing := range []string{"tenant", "user", "session"} {
		t.Run(missing, func(t *testing.T) {
			claims := validClaims(now)
			delete(claims, missing)
			token := signer.sign(t, claims)

			_, err := v.Validate(context.Background(), token)
			if !errors.Is(err, ErrIdentityClaimMissing) {
				t.Fatalf("Validate(missing %s): err = %v, want ErrIdentityClaimMissing", missing, err)
			}
		})
	}
}

func TestValidate_EmptyIdentityTriple(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	for _, empty := range []string{"tenant", "user", "session"} {
		t.Run(empty, func(t *testing.T) {
			claims := validClaims(now)
			claims[empty] = ""
			token := signer.sign(t, claims)

			_, err := v.Validate(context.Background(), token)
			if !errors.Is(err, ErrIdentityClaimMissing) {
				t.Fatalf("Validate(empty %s): err = %v, want ErrIdentityClaimMissing", empty, err)
			}
		})
	}
}

func TestValidate_IssuerMismatch(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithIssuer("harbor"), WithClock(fixedClock(now)))

	claims := validClaims(now)
	claims["iss"] = "someone-else"
	token := signer.sign(t, claims)

	_, err := v.Validate(context.Background(), token)
	if !errors.Is(err, ErrIssuerMismatch) {
		t.Fatalf("Validate(bad iss): err = %v, want ErrIssuerMismatch", err)
	}
}

func TestValidate_IssuerCheckDisabledWhenEmpty(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now))) // no WithIssuer

	claims := validClaims(now)
	claims["iss"] = "anything"
	token := signer.sign(t, claims)

	if _, err := v.Validate(context.Background(), token); err != nil {
		t.Fatalf("Validate(no issuer configured): unexpected error: %v", err)
	}
}

// TestValidate_AudienceContainment covers AC-5: aud may be a string or
// []string; containment (not equality) decides; empty configured disables.
func TestValidate_AudienceContainment(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")

	cases := []struct {
		name       string
		configured string
		aud        any
		wantErr    error
	}{
		{"string exact match", "stowage", "stowage", nil},
		{"string mismatch", "stowage", "other", ErrAudienceMismatch},
		{"array contains", "stowage", []string{"harbor", "stowage"}, nil},
		{"array does not contain", "stowage", []string{"harbor", "other"}, ErrAudienceMismatch},
		{"empty configured disables check", "", "anything-or-nothing", nil},
		{"missing aud claim, check enabled", "stowage", nil, ErrAudienceMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestValidator(t, signer, WithAudience(tc.configured), WithClock(fixedClock(now)))
			claims := validClaims(now)
			if tc.aud == nil {
				delete(claims, "aud")
			} else {
				claims["aud"] = tc.aud
			}
			token := signer.sign(t, claims)

			_, err := v.Validate(context.Background(), token)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate: unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate: err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_UnknownKid(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithClock(fixedClock(now)))

	claims := validClaims(now)
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "not-a-real-kid"
	signed, err := tok.SignedString(signer.priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, err = v.Validate(context.Background(), signed)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Validate(unknown kid): err = %v, want ErrUnknownKey", err)
	}
}

// kidAlgDisagreeKeySet always resolves to signer's key but reports a
// DIFFERENT alg than what actually signed the token — simulating a
// JWKS/staticKeySet entry whose declared alg disagrees with the key that
// verifies it.
type kidAlgDisagreeKeySet struct {
	inner   KeySet
	fakeAlg string
}

func (k kidAlgDisagreeKeySet) KeyByID(kid string) (crypto.PublicKey, string, error) {
	key, _, err := k.inner.KeyByID(kid)
	if err != nil {
		return nil, "", err
	}
	return key, k.fakeAlg, nil
}

func TestValidate_KidAlgDisagreement(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	ks := kidAlgDisagreeKeySet{inner: signer.keySet(), fakeAlg: "RS384"} // real token header says RS256

	v, err := NewValidator(ks, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	token := signer.sign(t, validClaims(now))
	_, err = v.Validate(context.Background(), token)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Validate(kid/alg disagreement): err = %v, want ErrUnknownKey", err)
	}
}

// symmetricKeySet resolves any kid to a raw byte-slice "key" — simulating a
// misbehaving KeySet driver that returns symmetric key material. The
// validator's belt-and-braces type-switch must reject it (defense in depth;
// jwks.go's own JWK parser already refuses to ever produce such an entry).
type symmetricKeySet struct{ alg string }

func (s symmetricKeySet) KeyByID(string) (crypto.PublicKey, string, error) {
	return []byte("not-an-asymmetric-key"), s.alg, nil
}

func TestValidate_SymmetricKeyRejected(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v, err := NewValidator(symmetricKeySet{alg: "RS256"}, WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	token := signer.sign(t, validClaims(now))
	_, err = v.Validate(context.Background(), token)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("Validate(symmetric key from KeySet): err = %v, want ErrUnknownKey", err)
	}
}

func TestValidate_TokenMissing(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer)

	_, err := v.Validate(context.Background(), "")
	if !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("Validate(empty): err = %v, want ErrTokenMissing", err)
	}
}

func TestValidate_Malformed(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer)

	for _, bad := range []string{"not-a-jwt", "a.b", "a.b.c.d", "..", "not base64!.also not.valid"} {
		_, err := v.Validate(context.Background(), bad)
		if !errors.Is(err, ErrTokenMalformed) {
			t.Errorf("Validate(%q): err = %v, want ErrTokenMalformed", bad, err)
		}
	}
}

// TestValidate_WithAlgorithmsNarrowsSubset covers auth.algorithms (config
// knob): a Validator configured with only RS256 rejects a validly-signed
// ES256 token as alg-not-allowed, even though ES256 IS in the global
// AllowedAlgorithms list.
func TestValidate_WithAlgorithmsNarrowsSubset(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "ES256")
	v, err := NewValidator(signer.keySet(), WithAlgorithms([]string{"RS256"}), WithClock(fixedClock(now)))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	token := signer.sign(t, validClaims(now))
	_, err = v.Validate(context.Background(), token)
	if !errors.Is(err, ErrAlgNotAllowed) {
		t.Fatalf("Validate(ES256 outside configured RS256-only subset): err = %v, want ErrAlgNotAllowed", err)
	}
}

func TestNewValidator_RejectsNonAsymmetricAlgorithm(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	_, err := NewValidator(signer.keySet(), WithAlgorithms([]string{"HS256"}))
	if err == nil {
		t.Fatal("NewValidator(HS256 in WithAlgorithms) = nil error, want rejection")
	}
}

func TestNewValidator_NilKeys(t *testing.T) {
	if _, err := NewValidator(nil); err == nil {
		t.Fatal("NewValidator(nil keys) = nil error, want rejection")
	}
}

// ---- AC-2: the test signer is test-only (L1) — Stowage never mints -------

// TestNoMintingOutsideTestFiles is the AC-2 grep gate: no non-_test.go file
// in internal/auth may reference a private-key type or SignedString (the
// two symbols that are exclusively meaningful for MINTING a token — unlike
// jwt.MapClaims, which internal/auth's validator.go legitimately uses on the
// PARSE side and is therefore not grepped here). Stowage verifies; it never
// signs.
func TestNoMintingOutsideTestFiles(t *testing.T) {
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`rsa\.PrivateKey`),
		regexp.MustCompile(`ecdsa\.PrivateKey`),
		regexp.MustCompile(`\.SignedString\(`),
		regexp.MustCompile(`jwt\.NewWithClaims`),
	}

	dir := "."
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, re := range forbidden {
			if re.Match(data) {
				t.Errorf("%s: contains %q — minting is test-only (AC-2, L1); Stowage never signs", name, re.String())
			}
		}
	}
}

// ---- FuzzValidate: prime parse surface (§11) ------------------------------

func FuzzValidate(f *testing.F) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// testing.F satisfies testing.TB, so the shared signer helpers work
	// unmodified for seed-corpus setup (only *testing.T is usable inside
	// f.Fuzz's callback itself, per the testing package's documented rule).
	signer := newTestSigner(f, "RS256")
	v, err := NewValidator(signer.keySet(), WithClock(fixedClock(now)))
	if err != nil {
		f.Fatalf("NewValidator: %v", err)
	}

	valid := signer.sign(f, validClaims(now))
	f.Add(valid)
	f.Add(valid[:len(valid)/2]) // truncated
	f.Add(signNone(f, validClaims(now), signer.kid))
	f.Add(strings.Repeat("A", 100000) + "." + strings.Repeat("B", 100000) + "." + strings.Repeat("C", 100000)) // oversized

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := v.Validate(context.Background(), raw)
		if err == nil {
			if got.Scope.Tenant == "" || got.Scope.User == "" || got.Scope.Session == "" {
				t.Errorf("Validate(%q): non-nil Verified with an empty identity triple: %+v", raw, got.Scope)
			}
		}
	})
}

// ---- concurrent-reuse (§5: a reusable artifact must be safe under
// concurrent use, proven under -race) --------------------------------------

func TestValidator_ConcurrentReuse(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v := newTestValidator(t, signer, WithIssuer("harbor"), WithAudience("stowage"), WithClock(fixedClock(now)))

	validToken := signer.sign(t, validClaims(now))
	expiredClaims := validClaims(now)
	expiredClaims["exp"] = now.Add(-time.Minute).Unix()
	expiredToken := signer.sign(t, expiredClaims)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			token := validToken
			if i%2 == 0 {
				token = expiredToken
			}
			_, _ = v.Validate(context.Background(), token)
		}(i)
	}
	wg.Wait()
}
