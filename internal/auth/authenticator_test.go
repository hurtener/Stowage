package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/identity"
)

// ---- ModeKeyring ------------------------------------------------------------

func TestAuthenticator_Keyring_Valid(t *testing.T) {
	kr := NewMemKeyring()
	key, plaintext, err := Generate("tenant-a", RoleAdmin)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	a := NewKeyringAuthenticator(kr)
	scope, role, err := a.Authenticate(context.Background(), "Bearer "+plaintext, "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if scope != (identity.Scope{Tenant: "tenant-a"}) {
		t.Errorf("Scope = %+v, want {Tenant: tenant-a}", scope)
	}
	if role != RoleAdmin {
		t.Errorf("Role = %q, want admin", role)
	}
}

func TestAuthenticator_Keyring_MissingBearer(t *testing.T) {
	a := NewKeyringAuthenticator(NewMemKeyring())

	for _, hdr := range []string{"", "Token abc", "Bearer "} {
		_, _, err := a.Authenticate(context.Background(), hdr, "")
		if !errors.Is(err, ErrTokenMissing) {
			t.Errorf("Authenticate(%q): err = %v, want ErrTokenMissing", hdr, err)
		}
	}
}

func TestAuthenticator_Keyring_BadCredential(t *testing.T) {
	a := NewKeyringAuthenticator(NewMemKeyring())
	_, _, err := a.Authenticate(context.Background(), "Bearer sk_bogus", "")
	if err == nil {
		t.Fatal("Authenticate(bogus key): err = nil, want a rejection")
	}
}

// ---- ModeJWT ------------------------------------------------------------

func jwtAuthenticatorFixture(t *testing.T) (*Authenticator, *testSigner, time.Time) {
	t.Helper()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "RS256")
	v, err := NewValidator(signer.keySet(), WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return NewJWTAuthenticator(v), signer, now
}

func TestAuthenticator_JWT_Valid(t *testing.T) {
	a, signer, now := jwtAuthenticatorFixture(t)
	token := signer.sign(t, validClaims(now))

	scope, role, err := a.Authenticate(context.Background(), "Bearer "+token, "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	want := identity.Scope{Tenant: "acme", User: "alice", Session: "s1"}
	if scope != want {
		t.Errorf("Scope = %+v, want %+v", scope, want)
	}
	if role != RoleAgent {
		t.Errorf("Role = %q, want agent (scopes=[read] has no admin)", role)
	}
}

// TestAuthenticator_JWT_SessionHeaderReplace pins D-137: a non-empty
// X-Harbor-Session REPLACES the token's session claim; Tenant/User stay
// token-verified — a header can never widen them.
func TestAuthenticator_JWT_SessionHeaderReplace(t *testing.T) {
	a, signer, now := jwtAuthenticatorFixture(t)
	token := signer.sign(t, validClaims(now)) // session claim = "s1"

	scope, _, err := a.Authenticate(context.Background(), "Bearer "+token, "s2-per-call")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if scope.Session != "s2-per-call" {
		t.Errorf("Session = %q, want s2-per-call (header must replace the token claim)", scope.Session)
	}
	if scope.Tenant != "acme" || scope.User != "alice" {
		t.Errorf("Tenant/User = %q/%q, want acme/alice (unaffected by the session header)", scope.Tenant, scope.User)
	}
}

func TestAuthenticator_JWT_SessionHeaderEmpty_KeepsTokenClaim(t *testing.T) {
	a, signer, now := jwtAuthenticatorFixture(t)
	token := signer.sign(t, validClaims(now))

	scope, _, err := a.Authenticate(context.Background(), "Bearer "+token, "")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if scope.Session != "s1" {
		t.Errorf("Session = %q, want s1 (empty header must not override the token claim)", scope.Session)
	}
}

// TestAuthenticator_JWT_RoleMapping pins departure #4: scopes containing
// "admin" -> RoleAdmin, else RoleAgent.
func TestAuthenticator_JWT_RoleMapping(t *testing.T) {
	cases := []struct {
		name   string
		scopes []string
		want   Role
	}{
		{"admin present", []string{"read", "admin"}, RoleAdmin},
		{"admin only", []string{"admin"}, RoleAdmin},
		{"no admin", []string{"read", "write"}, RoleAgent},
		{"empty scopes", []string{}, RoleAgent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, signer, now := jwtAuthenticatorFixture(t)
			claims := validClaims(now)
			claims["scopes"] = tc.scopes
			token := signer.sign(t, claims)

			_, role, err := a.Authenticate(context.Background(), "Bearer "+token, "")
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			if role != tc.want {
				t.Errorf("Role = %q, want %q", role, tc.want)
			}
		})
	}
}

func TestAuthenticator_JWT_InvalidTokenPropagates(t *testing.T) {
	a, signer, now := jwtAuthenticatorFixture(t)
	claims := validClaims(now)
	claims["exp"] = now.Add(-time.Hour).Unix()
	token := signer.sign(t, claims)

	_, _, err := a.Authenticate(context.Background(), "Bearer "+token, "")
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("Authenticate(expired): err = %v, want ErrTokenExpired", err)
	}
}

func TestAuthenticator_JWT_MissingBearer(t *testing.T) {
	a, _, _ := jwtAuthenticatorFixture(t)
	_, _, err := a.Authenticate(context.Background(), "", "")
	if !errors.Is(err, ErrTokenMissing) {
		t.Errorf("Authenticate(no header): err = %v, want ErrTokenMissing", err)
	}
}

// TestAuthenticator_ConcurrentReuse proves one shared Authenticator is safe
// under concurrent use across both modes (§5).
func TestAuthenticator_ConcurrentReuse(t *testing.T) {
	kr := NewMemKeyring()
	key, plaintext, err := Generate("tenant-a", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	keyringAuth := NewKeyringAuthenticator(kr)

	jwtAuth, signer, now := jwtAuthenticatorFixture(t)
	token := signer.sign(t, validClaims(now))

	done := make(chan struct{})
	const n = 16
	for i := 0; i < n; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			if i%2 == 0 {
				_, _, _ = keyringAuth.Authenticate(context.Background(), "Bearer "+plaintext, "")
			} else {
				_, _, _ = jwtAuth.Authenticate(context.Background(), "Bearer "+token, "")
			}
		}(i)
	}
	for i := 0; i < n; i++ {
		<-done
	}
}
