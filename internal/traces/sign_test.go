package traces

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func testKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return key
}

func sampleTrace() Trace {
	return Trace{ResponseID: "r1", Query: "q", Items: []TraceItem{{MemoryID: "m1", Rank: 0, Score: 0.9}}, GeneratedAt: 100}
}

func TestSign_VerifyRoundTrip(t *testing.T) {
	key := testKey(t)
	b, err := Sign(sampleTrace(), key)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if !b.Signed || b.Algorithm != SignAlgorithm || b.Signature == "" || b.PublicKey == "" {
		t.Fatalf("expected a signed bundle, got %+v", b)
	}
	if !Verify(b) {
		t.Error("Verify should accept a freshly signed bundle")
	}
}

func TestSign_TamperRejected(t *testing.T) {
	key := testKey(t)
	b, _ := Sign(sampleTrace(), key)
	// Tamper the trace after signing.
	b.Trace.Query = "tampered"
	if Verify(b) {
		t.Error("Verify must reject a tampered trace")
	}
	// Tamper the signature.
	b2, _ := Sign(sampleTrace(), key)
	b2.Signature = base64.StdEncoding.EncodeToString([]byte("not a real signature padding padding padding padding padding!!"))
	if Verify(b2) {
		t.Error("Verify must reject a bad signature")
	}
}

func TestSign_Unkeyed(t *testing.T) {
	b, err := Sign(sampleTrace(), nil)
	if err != nil {
		t.Fatalf("Sign(nil): %v", err)
	}
	if b.Signed || b.Signature != "" {
		t.Errorf("nil key ⇒ unsigned bundle, got %+v", b)
	}
	if Verify(b) {
		t.Error("Verify must return false for an unsigned bundle")
	}
}

func TestParseSigningKey(t *testing.T) {
	// Empty ⇒ (nil, nil).
	if k, err := ParseSigningKey(""); err != nil || k != nil {
		t.Errorf("empty ⇒ (nil,nil), got %v / %v", k, err)
	}
	// Valid 32-byte seed.
	seed := make([]byte, ed25519.SeedSize)
	seed[0] = 1
	k, err := ParseSigningKey(base64.StdEncoding.EncodeToString(seed))
	if err != nil || len(k) != ed25519.PrivateKeySize {
		t.Errorf("valid seed parse failed: %v / %d", err, len(k))
	}
	// Non-base64.
	if _, err := ParseSigningKey("@@notbase64@@"); err == nil {
		t.Error("expected error on non-base64 seed")
	}
	// Wrong length.
	if _, err := ParseSigningKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("expected error on wrong-length seed")
	}
	// A parsed key actually signs+verifies.
	b, _ := Sign(sampleTrace(), k)
	if !Verify(b) {
		t.Error("a bundle signed with a parsed key should verify")
	}
}
