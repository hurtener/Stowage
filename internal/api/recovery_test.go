package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRecoveryMiddleware proves AC-7: a handler panic is converted to a 500
// response without process exit. Tests from within the package to access the
// unexported recoveryMiddleware method.
func TestRecoveryMiddleware(t *testing.T) {
	t.Parallel()

	// Stub server — only the recovery middleware is exercised.
	s := &Server{
		log:      noopLogger(),
		maxBodyB: 1 << 20,
	}

	// Build a handler that panics and wrap it with recovery middleware.
	panicHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("intentional test panic — must not propagate")
	})

	wrapped := s.recoveryMiddleware(panicHandler)

	w := httptest.NewRecorder()
	r, _ := http.NewRequest("GET", "/test", nil)
	wrapped.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("recovery: got status %d want 500", w.Code)
	}
}
