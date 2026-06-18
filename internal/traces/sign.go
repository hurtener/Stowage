package traces

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// SignAlgorithm is the only supported trace-signing algorithm (CGo-free stdlib).
const SignAlgorithm = "ed25519"

// Bundle is the exported trace plus an optional detached signature over the canonical
// JSON of the Trace. A verifier recomputes the trace bytes and checks Signature against
// PublicKey. When no signing key is configured, Signed is false and the signature
// fields are empty (the bundle is still a valid, unsigned export).
type Bundle struct {
	Trace     Trace  `json:"trace"`
	Signed    bool   `json:"signed"`
	Algorithm string `json:"algorithm,omitempty"`
	PublicKey string `json:"public_key,omitempty"` // base64 ed25519 public key
	Signature string `json:"signature,omitempty"`  // base64 detached signature over canonicalTrace(Trace)
}

// canonicalTrace returns the deterministic JSON bytes the signature covers. Trace
// marshals deterministically (no maps; stable struct field order), so this is stable
// across processes — a third party recomputes it to verify.
func canonicalTrace(t Trace) ([]byte, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("traces: canonicalize: %w", err)
	}
	return b, nil
}

// Sign returns a signed bundle. A nil/empty key returns an unsigned bundle (Signed
// false) — never an error, so unkeyed deployments still export traces.
func Sign(t Trace, key ed25519.PrivateKey) (Bundle, error) {
	if len(key) == 0 {
		return Bundle{Trace: t, Signed: false}, nil
	}
	if len(key) != ed25519.PrivateKeySize {
		return Bundle{}, fmt.Errorf("traces: sign: bad ed25519 private key size %d", len(key))
	}
	msg, err := canonicalTrace(t)
	if err != nil {
		return Bundle{}, err
	}
	sig := ed25519.Sign(key, msg)
	pub := key.Public().(ed25519.PublicKey)
	return Bundle{
		Trace:     t,
		Signed:    true,
		Algorithm: SignAlgorithm,
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}, nil
}

// Verify checks a signed bundle's detached signature against its embedded public key.
// Returns false for an unsigned bundle, a malformed key/signature, or a tampered
// trace. It never panics on arbitrary input (audit-tooling-safe).
func Verify(b Bundle) bool {
	if !b.Signed || b.Algorithm != SignAlgorithm {
		return false
	}
	pub, err := base64.StdEncoding.DecodeString(b.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(b.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg, err := canonicalTrace(b.Trace)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), msg, sig)
}

// ParseSigningKey decodes a base64-encoded 32-byte ed25519 seed into a private key.
// Empty input returns (nil, nil) — the unsigned mode. Used at boot to validate the
// trace.signing_key config (fail-loud on a malformed key).
func ParseSigningKey(b64seed string) (ed25519.PrivateKey, error) {
	if b64seed == "" {
		return nil, nil
	}
	seed, err := base64.StdEncoding.DecodeString(b64seed)
	if err != nil {
		return nil, fmt.Errorf("traces: signing key is not valid base64: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("traces: signing key must decode to %d bytes (an ed25519 seed), got %d", ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
