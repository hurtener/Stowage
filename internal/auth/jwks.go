package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// The JWKS-specific typed sentinels. ErrJWKSStale is the D-147 fail-closed
// signal — validator.go's keyfunc propagates it un-masked (never remapped to
// ErrUnknownKey).
var (
	// ErrJWKSStale means the cached snapshot is past auth.jwks.max_stale
	// without a successful refresh — KeyByID fails CLOSED (D-147). A
	// possibly-revoked key in a too-old snapshot is never served.
	ErrJWKSStale = errors.New("auth: jwks: cached key set is past the max-stale ceiling")
	// ErrJWKSSource means the configured source (URL or File) could not be
	// fetched or parsed as JSON.
	ErrJWKSSource = errors.New("auth: jwks: source fetch/parse failed")
	// ErrJWKSNoUsableKeys means the source parsed but yielded zero usable
	// asymmetric keys (every entry was oct, sub-floor RSA, off-curve EC, or
	// otherwise unusable).
	ErrJWKSNoUsableKeys = errors.New("auth: jwks: source has zero usable asymmetric keys")
)

const (
	// jwksMaxBodyBytes bounds both the URL response body and a File read (1
	// MiB — plan §Design).
	jwksMaxBodyBytes = 1 << 20
	// jwksMinRSABits is the RSA modulus floor (plan §Design).
	jwksMinRSABits = 2048
	// jwksDefaultTTL is the on-demand refresh cadence. Package-internal (like
	// BufferTriggers) — not an operator knob (D-034 guardrail; only
	// auth.jwks.max_stale is operator-tunable).
	jwksDefaultTTL = 5 * time.Minute
)

// JWKSSource identifies where a JWK Set is fetched from — exactly one of URL
// or File must be set (enforced by NewJWKSKeySet).
type JWKSSource struct {
	URL  string
	File string
}

// JWKSKeySet is an asymmetric-only KeySet resolving a JWT `kid` against a
// URL-published or File-local JWK Set. It holds an atomically-swappable
// staticKeySet snapshot, refreshed ON DEMAND (no background goroutine —
// nothing to leak) under a TTL cadence and single-flighted so concurrent
// callers coalesce into one fetch. A snapshot older than max_stale fails
// KeyByID CLOSED (ErrJWKSStale, D-147).
type JWKSKeySet struct {
	source   JWKSSource
	maxStale time.Duration
	ttl      time.Duration
	clock    func() time.Time
	client   *http.Client
	sf       singleflight.Group

	mu         sync.Mutex
	snapshot   *staticKeySet
	lastGoodAt time.Time
}

// JWKSOption configures a JWKSKeySet built by NewJWKSKeySet.
type JWKSOption func(*JWKSKeySet)

// WithJWKSClock injects the time source used for TTL/max-stale bookkeeping
// (L1 — pairs with a test's manually-advanced clock).
func WithJWKSClock(clock func() time.Time) JWKSOption {
	return func(ks *JWKSKeySet) { ks.clock = clock }
}

// WithJWKSHTTPClient overrides the http.Client used for a URL source
// (default: a 10s-timeout client). Tests point this at an httptest.Server.
func WithJWKSHTTPClient(c *http.Client) JWKSOption {
	return func(ks *JWKSKeySet) { ks.client = c }
}

// WithJWKSTTL overrides the internal refresh cadence (default
// jwksDefaultTTL). Test-only knob — never exposed as operator config (D-034
// guardrail); production always uses the package default.
func WithJWKSTTL(d time.Duration) JWKSOption {
	return func(ks *JWKSKeySet) { ks.ttl = d }
}

// NewJWKSKeySet builds a JWKSKeySet against source, performing a SYNCHRONOUS
// initial fetch+parse. It FAILS LOUD (returns a non-nil error) when the
// source cannot be fetched/parsed, or parses to zero usable asymmetric keys —
// so a boot into auth.mode=jwt with a bad JWKS source never silently starts
// (D-147; cmd/stowage/main.go treats this as a fatal boot error).
func NewJWKSKeySet(ctx context.Context, source JWKSSource, maxStale time.Duration, opts ...JWKSOption) (*JWKSKeySet, error) {
	if (source.URL == "") == (source.File == "") {
		return nil, errors.New("auth: jwks: exactly one of URL or File must be set")
	}
	if maxStale <= 0 {
		return nil, errors.New("auth: jwks: max_stale must be > 0")
	}
	ks := &JWKSKeySet{
		source:   source,
		maxStale: maxStale,
		ttl:      jwksDefaultTTL,
		clock:    time.Now,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
	for _, opt := range opts {
		opt(ks)
	}
	if err := ks.doRefresh(ctx); err != nil {
		return nil, fmt.Errorf("auth: jwks: initial load: %w", err)
	}
	return ks, nil
}

// KeyByID implements KeySet. It refreshes the snapshot on demand — on a
// cache/kid miss, or once the TTL has elapsed — then serves the current
// snapshot if it is within max_stale, else fails closed with ErrJWKSStale.
func (ks *JWKSKeySet) KeyByID(kid string) (crypto.PublicKey, string, error) {
	ks.mu.Lock()
	snap := ks.snapshot
	lastGood := ks.lastGoodAt
	ks.mu.Unlock()

	now := ks.clock()
	needsRefresh := snap == nil || now.Sub(lastGood) >= ks.ttl
	if !needsRefresh {
		if _, _, err := snap.KeyByID(kid); err != nil {
			needsRefresh = true // bounded kid-miss refresh, single-flighted below
		}
	}

	if needsRefresh {
		_ = ks.refreshSingleflight(context.Background())
		ks.mu.Lock()
		snap = ks.snapshot
		lastGood = ks.lastGoodAt
		ks.mu.Unlock()
		now = ks.clock()
	}

	if snap == nil {
		return nil, "", fmt.Errorf("%w: no successful JWKS fetch yet", ErrJWKSSource)
	}
	if now.Sub(lastGood) > ks.maxStale {
		return nil, "", ErrJWKSStale
	}
	return snap.KeyByID(kid)
}

// refreshSingleflight bounds concurrent refreshes to one in-flight fetch;
// callers that arrive while a refresh is running wait for and share its
// result rather than issuing a redundant fetch.
func (ks *JWKSKeySet) refreshSingleflight(ctx context.Context) error {
	_, err, _ := ks.sf.Do("refresh", func() (any, error) {
		return nil, ks.doRefresh(ctx)
	})
	return err
}

// doRefresh fetches + parses the source and, on success, atomically swaps in
// the new snapshot. On failure it leaves the existing snapshot untouched
// (serve last-known-good up to max_stale — the caller's KeyByID enforces the
// ceiling).
func (ks *JWKSKeySet) doRefresh(ctx context.Context) error {
	data, err := ks.fetch(ctx)
	if err != nil {
		return err
	}
	keys, err := parseJWKSet(data)
	if err != nil {
		return err
	}
	ks.mu.Lock()
	ks.snapshot = newStaticKeySet(keys)
	ks.lastGoodAt = ks.clock()
	ks.mu.Unlock()
	return nil
}

// fetch reads the raw JWK Set bytes from the configured source, enforcing the
// 1 MiB body cap on both the URL and File paths.
func (ks *JWKSKeySet) fetch(ctx context.Context) ([]byte, error) {
	if ks.source.File != "" {
		data, err := os.ReadFile(ks.source.File) //nolint:gosec // G304: operator config path, not user input
		if err != nil {
			return nil, fmt.Errorf("%w: read file: %w", ErrJWKSSource, err)
		}
		if len(data) > jwksMaxBodyBytes {
			return nil, fmt.Errorf("%w: file exceeds %d byte cap", ErrJWKSSource, jwksMaxBodyBytes)
		}
		return data, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ks.source.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %w", ErrJWKSSource, err)
	}
	resp, err := ks.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: fetch: %w", ErrJWKSSource, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: unexpected status %d", ErrJWKSSource, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxBodyBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", ErrJWKSSource, err)
	}
	if len(data) > jwksMaxBodyBytes {
		return nil, fmt.Errorf("%w: response exceeds %d byte cap", ErrJWKSSource, jwksMaxBodyBytes)
	}
	return data, nil
}

// jwkSetDoc / jwkDoc mirror the standard JWK Set JSON shape (RFC 7517),
// parsed with the stdlib encoding/json only (no third-party JWK library).
type jwkSetDoc struct {
	Keys []jwkDoc `json:"keys"`
}

type jwkDoc struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	// RSA
	N string `json:"n"`
	E string `json:"e"`
	// EC
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// parseJWKSet decodes a JWK Set document into a map[kid]jwkKey, skipping any
// entry that is unusable (no kid, oct/symmetric, sub-floor RSA, off-curve EC,
// missing/non-asymmetric alg, undecodable) rather than failing the whole set.
// Returns ErrJWKSNoUsableKeys when the result is empty (fail loud, D-147).
func parseJWKSet(data []byte) (map[string]jwkKey, error) {
	var doc jwkSetDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%w: decode JSON: %w", ErrJWKSSource, err)
	}
	out := make(map[string]jwkKey)
	for _, k := range doc.Keys {
		if k.Kid == "" {
			continue // unusable without a kid to resolve against
		}
		switch k.Kty {
		case "RSA":
			pub, alg, err := parseRSAJWK(k)
			if err != nil {
				continue
			}
			out[k.Kid] = jwkKey{key: pub, alg: alg}
		case "EC":
			pub, alg, err := parseECJWK(k)
			if err != nil {
				continue
			}
			out[k.Kid] = jwkKey{key: pub, alg: alg}
		default:
			// "oct" (symmetric) and any other kty are REJECTED — never added
			// to the usable set (asymmetric-only, AC-1/AC-3).
			continue
		}
	}
	if len(out) == 0 {
		return nil, ErrJWKSNoUsableKeys
	}
	return out, nil
}

// parseRSAJWK decodes an RSA JWK entry's n/e into an *rsa.PublicKey, enforcing
// the >=2048-bit floor and that alg is present and in the asymmetric
// allowlist.
func parseRSAJWK(k jwkDoc) (*rsa.PublicKey, string, error) {
	if k.Alg == "" {
		return nil, "", errors.New("RSA JWK missing alg")
	}
	if !isAllowedAlgString(k.Alg) {
		return nil, "", fmt.Errorf("unsupported alg %q for RSA key", k.Alg)
	}
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, "", fmt.Errorf("decode n: %w", err)
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, "", fmt.Errorf("decode e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if n.BitLen() < jwksMinRSABits {
		return nil, "", fmt.Errorf("rsa modulus (%d bits) below the %d-bit floor", n.BitLen(), jwksMinRSABits)
	}
	return &rsa.PublicKey{N: n, E: int(e.Int64())}, k.Alg, nil
}

// parseECJWK decodes an EC JWK entry's crv/x/y into an *ecdsa.PublicKey,
// checking the point is on the named curve and that alg is present and in
// the asymmetric allowlist.
func parseECJWK(k jwkDoc) (*ecdsa.PublicKey, string, error) {
	if k.Alg == "" {
		return nil, "", errors.New("EC JWK missing alg")
	}
	if !isAllowedAlgString(k.Alg) {
		return nil, "", fmt.Errorf("unsupported alg %q for EC key", k.Alg)
	}
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, "", fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	xBytes, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, "", fmt.Errorf("decode x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, "", fmt.Errorf("decode y: %w", err)
	}

	// Build the SEC1 uncompressed-point encoding (0x04 || X || Y, each
	// right-aligned to the curve's field byte-length — a JWK's base64url x/y
	// may omit leading zero bytes) and hand it to ParseUncompressedPublicKey,
	// which performs the on-curve check itself. This avoids the deprecated
	// direct big.Int X/Y field construction (Go 1.26, crypto/ecdsa).
	size := (curve.Params().BitSize + 7) / 8
	if len(xBytes) > size || len(yBytes) > size {
		return nil, "", fmt.Errorf("EC coordinate exceeds curve %s field size", k.Crv)
	}
	point := make([]byte, 1+2*size)
	point[0] = 0x04
	copy(point[1+size-len(xBytes):1+size], xBytes)
	copy(point[1+2*size-len(yBytes):], yBytes)

	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, "", fmt.Errorf("EC point is not on curve %s: %w", k.Crv, err)
	}
	return pub, k.Alg, nil
}
