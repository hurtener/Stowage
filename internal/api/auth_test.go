package api

// auth_test.go — in-package unit tests for the ae8 resolver adapters in
// auth.go (respondScopeError, SetAuthenticator) that are hard or impossible
// to drive end-to-end over a real HTTP request:
//
//   - respondScopeError maps every identity.ResolveReadScope sentinel to its
//     HTTP status directly. ErrTenantMismatch/ErrUserConflict require a
//     ModeJWT credential (CredUser/ClaimTenant populated) to occur via a real
//     request; ModeKeyring (this package's HTTP test harness) never
//     populates those fields, so the sentinel branches are exercised as pure
//     unit tests here instead (see docs/plans/phase-ae8-effective-scope.md,
//     "As-built deviations").
//   - SetAuthenticator is a trivial setter exercised directly since it needs
//     no HTTP round trip to prove correct.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
)

// TestKeyFromContext_PanicsWithoutMiddleware proves keyFromContext panics
// (rather than returning a nil/zero key that would silently bypass scoping)
// when called on a context that authMiddleware never touched.
func TestKeyFromContext_PanicsWithoutMiddleware(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected keyFromContext to panic without authMiddleware")
		}
	}()
	keyFromContext(context.Background())
}

// TestRespondScopeError proves every identity.ResolveReadScope sentinel maps
// to its documented HTTP status, and any other error falls back to 400.
func TestRespondScopeError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"tenant mismatch", identity.ErrTenantMismatch, http.StatusForbidden},
		{"user conflict", identity.ErrUserConflict, http.StatusForbidden},
		{"identity required (strict posture)", identity.ErrIdentityRequired, http.StatusForbidden},
		{"wrapped tenant mismatch", errors.Join(errors.New("ctx"), identity.ErrTenantMismatch), http.StatusForbidden},
		{"other/unrecognized error", errors.New("boom"), http.StatusBadRequest},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			respondScopeError(w, tc.err)
			if w.Code != tc.wantStatus {
				t.Errorf("respondScopeError(%v): got status %d want %d", tc.err, w.Code, tc.wantStatus)
			}
			if ct := w.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("respondScopeError: Content-Type = %q want application/json", ct)
			}
		})
	}
}

// TestSetAuthenticator proves SetAuthenticator overrides the server's
// authenticator (used by cmd/stowage/main.go to install the boot-built
// ModeJWT authenticator over the zero-config keyring default, ae7).
func TestSetAuthenticator(t *testing.T) {
	t.Parallel()

	original := auth.NewKeyringAuthenticator(auth.NewMemKeyring())
	s := &Server{authn: original}

	replacement := auth.NewKeyringAuthenticator(auth.NewMemKeyring())
	s.SetAuthenticator(replacement)

	if s.authn != replacement {
		t.Fatal("SetAuthenticator did not replace the server's authenticator")
	}
	if s.authn == original {
		t.Fatal("SetAuthenticator left the original authenticator in place")
	}
}
