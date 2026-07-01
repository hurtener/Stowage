package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- JWK JSON doc builders (mirror a real JWKS endpoint's shape) ---------

func rsaJWKDocFrom(kid, alg string, pub *rsa.PublicKey) jwkDoc {
	return jwkDoc{
		Kty: "RSA", Kid: kid, Alg: alg,
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func ecJWKDocFrom(t testing.TB, kid, alg, crv string, pub *ecdsa.PublicKey) jwkDoc {
	t.Helper()
	// Use the modern uncompressed-point encoder rather than the deprecated
	// direct X/Y big.Int field access (Go 1.26, crypto/ecdsa) — mirrors
	// parseECJWK's own decode-side choice in jwks.go.
	raw, err := pub.Bytes()
	if err != nil {
		t.Fatalf("encode EC public key: %v", err)
	}
	size := (len(raw) - 1) / 2
	return jwkDoc{
		Kty: "EC", Kid: kid, Alg: alg, Crv: crv,
		X: base64.RawURLEncoding.EncodeToString(raw[1 : 1+size]),
		Y: base64.RawURLEncoding.EncodeToString(raw[1+size:]),
	}
}

// jwkDocFromSigner builds the JWK doc that publishes s's public key.
func jwkDocFromSigner(t testing.TB, s *testSigner) jwkDoc {
	t.Helper()
	switch pub := s.pub.(type) {
	case *rsa.PublicKey:
		return rsaJWKDocFrom(s.kid, s.alg, pub)
	case *ecdsa.PublicKey:
		crv := map[string]string{"ES256": "P-256", "ES384": "P-384", "ES512": "P-521"}[s.alg]
		return ecJWKDocFrom(t, s.kid, s.alg, crv, pub)
	default:
		t.Fatalf("jwkDocFromSigner: unsupported pub type %T", s.pub)
		return jwkDoc{}
	}
}

func jwkSetJSON(t testing.TB, docs ...jwkDoc) []byte {
	t.Helper()
	data, err := json.Marshal(jwkSetDoc{Keys: docs})
	if err != nil {
		t.Fatalf("marshal jwk set: %v", err)
	}
	return data
}

// ---- URL source ------------------------------------------------------------

func TestJWKS_URLSource(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(jwkSetJSON(t, jwkDocFromSigner(t, signer)))
	}))
	defer ts.Close()

	ks, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	key, alg, err := ks.KeyByID(signer.kid)
	if err != nil {
		t.Fatalf("KeyByID: %v", err)
	}
	if alg != "RS256" {
		t.Errorf("alg = %q, want RS256", alg)
	}
	if _, ok := key.(*rsa.PublicKey); !ok {
		t.Errorf("key type = %T, want *rsa.PublicKey", key)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("hits = %d, want 1 (initial load only)", hits)
	}
}

// ---- File source ------------------------------------------------------------

func TestJWKS_FileSource(t *testing.T) {
	signer := newTestSigner(t, "ES256")
	path := filepath.Join(t.TempDir(), "jwks.json")
	if err := os.WriteFile(path, jwkSetJSON(t, jwkDocFromSigner(t, signer)), 0o600); err != nil {
		t.Fatalf("write jwks file: %v", err)
	}

	ks, err := NewJWKSKeySet(context.Background(), JWKSSource{File: path}, time.Hour)
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	key, alg, err := ks.KeyByID(signer.kid)
	if err != nil {
		t.Fatalf("KeyByID: %v", err)
	}
	if alg != "ES256" {
		t.Errorf("alg = %q, want ES256", alg)
	}
	if _, ok := key.(*ecdsa.PublicKey); !ok {
		t.Errorf("key type = %T, want *ecdsa.PublicKey", key)
	}
}

// ---- exactly-one-source / positive max_stale validation --------------------

func TestJWKS_ExactlyOneSourceRequired(t *testing.T) {
	if _, err := NewJWKSKeySet(context.Background(), JWKSSource{}, time.Hour); err == nil {
		t.Error("NewJWKSKeySet(neither URL nor File) = nil error, want rejection")
	}
	if _, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: "http://x", File: "/y"}, time.Hour); err == nil {
		t.Error("NewJWKSKeySet(both URL and File) = nil error, want rejection")
	}
}

func TestJWKS_MaxStaleMustBePositive(t *testing.T) {
	if _, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: "http://x"}, 0); err == nil {
		t.Error("NewJWKSKeySet(max_stale=0) = nil error, want rejection")
	}
	if _, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: "http://x"}, -time.Second); err == nil {
		t.Error("NewJWKSKeySet(max_stale<0) = nil error, want rejection")
	}
}

// ---- AC-3: fail LOUD on first load ------------------------------------------

func TestJWKS_FailsLoud_ZeroUsableKeys(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwkSetJSON(t)) // empty keys array
	}))
	defer ts.Close()

	_, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err == nil || !errors.Is(err, ErrJWKSNoUsableKeys) {
		t.Fatalf("NewJWKSKeySet(empty key set): err = %v, want ErrJWKSNoUsableKeys", err)
	}
}

// TestJWKS_FailsLoud_OctRejected pins that a symmetric (oct) key is never
// added to the usable set — a JWKS containing ONLY an oct entry construction-fails.
func TestJWKS_FailsLoud_OctRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwkSetJSON(t, jwkDoc{Kty: "oct", Kid: "oct-1", Alg: "HS256", N: "irrelevant"}))
	}))
	defer ts.Close()

	_, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err == nil || !errors.Is(err, ErrJWKSNoUsableKeys) {
		t.Fatalf("NewJWKSKeySet(oct-only key set): err = %v, want ErrJWKSNoUsableKeys (oct must be rejected)", err)
	}
}

// TestJWKS_FailsLoud_SubStandardRSARejected pins the >=2048-bit RSA floor: a
// 1024-bit RSA key is skipped as unusable, so a set containing only one
// fails construction.
func TestJWKS_FailsLoud_SubStandardRSARejected(t *testing.T) {
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate weak rsa key: %v", err)
	}
	doc := rsaJWKDocFrom("weak-1", "RS256", &weak.PublicKey)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(jwkSetJSON(t, doc))
	}))
	defer ts.Close()

	_, err = NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err == nil || !errors.Is(err, ErrJWKSNoUsableKeys) {
		t.Fatalf("NewJWKSKeySet(1024-bit RSA only): err = %v, want ErrJWKSNoUsableKeys", err)
	}
}

func TestJWKS_FailsLoud_SourceUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err == nil || !errors.Is(err, ErrJWKSSource) {
		t.Fatalf("NewJWKSKeySet(500 response): err = %v, want ErrJWKSSource", err)
	}
}

func TestJWKS_BodyCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, jwksMaxBodyBytes+1))
	}))
	defer ts.Close()

	_, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour)
	if err == nil || !errors.Is(err, ErrJWKSSource) {
		t.Fatalf("NewJWKSKeySet(oversized body): err = %v, want ErrJWKSSource", err)
	}
}

// ---- TTL refresh + single-flight -------------------------------------------

func TestJWKS_TTLRefresh(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	var hits int32
	clockVal := &atomicTime{}
	clockVal.set(time.Unix(1_700_000_000, 0))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(jwkSetJSON(t, jwkDocFromSigner(t, signer)))
	}))
	defer ts.Close()

	ks, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour,
		WithJWKSClock(clockVal.now), WithJWKSTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	if hits := atomic.LoadInt32(&hits); hits != 1 {
		t.Fatalf("hits after construction = %d, want 1", hits)
	}

	// Within TTL: no refresh.
	if _, _, err := ks.KeyByID(signer.kid); err != nil {
		t.Fatalf("KeyByID (within TTL): %v", err)
	}
	if hits := atomic.LoadInt32(&hits); hits != 1 {
		t.Fatalf("hits after within-TTL call = %d, want 1 (no refresh)", hits)
	}

	// Advance past TTL: next call refreshes.
	clockVal.set(clockVal.now().Add(2 * time.Minute))
	if _, _, err := ks.KeyByID(signer.kid); err != nil {
		t.Fatalf("KeyByID (past TTL): %v", err)
	}
	if hits := atomic.LoadInt32(&hits); hits != 2 {
		t.Fatalf("hits after past-TTL call = %d, want 2 (one refresh)", hits)
	}
}

func TestJWKS_KidMissBoundedRefresh(t *testing.T) {
	signerA := newTestSigner(t, "RS256")
	signerB := newTestSigner(t, "RS256")
	var docs atomic.Value
	docs.Store([]jwkDoc{jwkDocFromSigner(t, signerA)}) // B not published yet

	var hits int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(jwkSetJSON(t, docs.Load().([]jwkDoc)...))
	}))
	defer ts.Close()

	clockVal := &atomicTime{}
	clockVal.set(time.Unix(1_700_000_000, 0))
	ks, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, time.Hour,
		WithJWKSTTL(time.Hour), WithJWKSClock(clockVal.now))
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Fatalf("hits after construction = %d, want 1", h)
	}

	// A kid-miss WITHIN the min-refresh window (same instant as the load attempt)
	// serves the snapshot without refetching — the amplification guard: a flood
	// of forged/unknown kids can't force one outbound fetch per request.
	for i := 0; i < 5; i++ {
		if _, _, err := ks.KeyByID(signerB.kid); err == nil {
			t.Fatal("KeyByID(B) within window = nil error, want unknown-kid rejection")
		}
	}
	if h := atomic.LoadInt32(&hits); h != 1 {
		t.Fatalf("hits after a burst of in-window kid-misses = %d, want 1 (no refetch inside the window)", h)
	}

	// Advance past the min-refresh window: now a kid-miss is allowed to refresh
	// (still a miss — B unpublished — but the refresh happens once).
	clockVal.set(clockVal.now().Add(jwksMinRefreshInterval + time.Second))
	if _, _, err := ks.KeyByID(signerB.kid); err == nil {
		t.Fatal("KeyByID(B) after window = nil error, want unknown-kid rejection")
	}
	if h := atomic.LoadInt32(&hits); h != 2 {
		t.Fatalf("hits after out-of-window kid-miss = %d, want 2 (one refresh)", h)
	}

	// Publish B and advance past the window again — B now resolves on the refresh.
	docs.Store([]jwkDoc{jwkDocFromSigner(t, signerA), jwkDocFromSigner(t, signerB)})
	clockVal.set(clockVal.now().Add(jwksMinRefreshInterval + time.Second))
	if _, _, err := ks.KeyByID(signerB.kid); err != nil {
		t.Fatalf("KeyByID(B) after publish + window: %v", err)
	}
	if h := atomic.LoadInt32(&hits); h != 3 {
		t.Fatalf("hits after publish resolve = %d, want 3", h)
	}
}

func TestJWKS_SingleFlight(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	var hits int32
	release := make(chan struct{})
	var firstIn sync.WaitGroup
	firstIn.Add(1)
	var once sync.Once

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		once.Do(firstIn.Done)
		<-release // hold every concurrent request open until released
		_, _ = w.Write(jwkSetJSON(t, jwkDocFromSigner(t, signer)))
	}))
	defer ts.Close()

	ks := &JWKSKeySet{
		source:   JWKSSource{URL: ts.URL},
		maxStale: time.Hour,
		ttl:      time.Hour,
		clock:    time.Now,
		client:   ts.Client(),
	}

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = ks.refreshSingleflight(context.Background())
		}()
	}
	firstIn.Wait()
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("concurrent refreshSingleflight calls hit the server %d times, want 1", got)
	}
}

// ---- AC-3: max-stale fail-closed (clock-driven) -----------------------------

func TestJWKS_MaxStaleFailClosed(t *testing.T) {
	signer := newTestSigner(t, "RS256")
	clockVal := &atomicTime{}
	clockVal.set(time.Unix(1_700_000_000, 0))

	var fail atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(jwkSetJSON(t, jwkDocFromSigner(t, signer)))
	}))
	defer ts.Close()

	ks, err := NewJWKSKeySet(context.Background(), JWKSSource{URL: ts.URL}, 5*time.Minute,
		WithJWKSClock(clockVal.now), WithJWKSTTL(time.Minute))
	if err != nil {
		t.Fatalf("NewJWKSKeySet: %v", err)
	}

	// The source starts failing (e.g. an outage); the cache is still within
	// max_stale, so a stale-but-not-yet-expired snapshot is served.
	fail.Store(true)
	clockVal.set(clockVal.now().Add(2 * time.Minute)) // past TTL, within max_stale
	if _, _, err := ks.KeyByID(signer.kid); err != nil {
		t.Fatalf("KeyByID (stale but within max_stale): unexpected error: %v", err)
	}

	// Advance past max_stale: KeyByID must fail CLOSED (D-147), never serve a
	// possibly-revoked key from a too-old snapshot.
	clockVal.set(clockVal.now().Add(10 * time.Minute))
	_, _, err = ks.KeyByID(signer.kid)
	if !errors.Is(err, ErrJWKSStale) {
		t.Fatalf("KeyByID (past max_stale): err = %v, want ErrJWKSStale", err)
	}
}

// ---- helpers ----------------------------------------------------------------

// atomicTime is a small race-safe mutable clock for TTL/max-stale tests.
type atomicTime struct {
	mu sync.Mutex
	t  time.Time
}

func (a *atomicTime) set(t time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.t = t
}

func (a *atomicTime) now() time.Time {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.t
}
