package auth

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestGenerateAndVerify tests the happy path: generate a key and verify it.
func TestGenerateAndVerify(t *testing.T) {
	kr := NewMemKeyring()
	key, plaintext, err := Generate("tenant-1", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := Verify(kr, plaintext)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got.ID != key.ID {
		t.Errorf("Verify returned key ID %q, want %q", got.ID, key.ID)
	}
	if got.TenantID != "tenant-1" {
		t.Errorf("TenantID = %q, want %q", got.TenantID, "tenant-1")
	}
}

// TestPlaintextFormat verifies the generated plaintext key format.
func TestPlaintextFormat(t *testing.T) {
	_, plaintext, err := Generate("t", RoleAdmin)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(plaintext) != keyTotalLen {
		t.Errorf("plaintext len = %d, want %d: %q", len(plaintext), keyTotalLen, plaintext)
	}
	if !strings.HasPrefix(plaintext, "sk_") {
		t.Errorf("plaintext %q does not start with sk_", plaintext)
	}
	if plaintext[keySepPos] != '_' {
		t.Errorf("plaintext[%d] = %q, want '_'", keySepPos, plaintext[keySepPos])
	}
}

// TestVerifyWrongSecret verifies AC-6: wrong secret fails.
func TestVerifyWrongSecret(t *testing.T) {
	kr := NewMemKeyring()
	key, plaintext, err := Generate("t", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// Corrupt a character in the MIDDLE of the 43-char secret part so that the
	// decoded bytes definitely differ. The last base64url character carries only
	// 4 bits of payload (the lower 2 are padding zeros), so corrupting it can be
	// a no-op for certain original values.
	secretStart := keySepPos + 1   // first char of the secret segment
	corruptIdx := secretStart + 20 // well within the middle; all 6 bits count
	orig := plaintext[corruptIdx]
	// Pick any valid base64url character that differs from orig.
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var corruptChar byte
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] != orig {
			corruptChar = alphabet[i]
			break
		}
	}
	bad := plaintext[:corruptIdx] + string(corruptChar) + plaintext[corruptIdx+1:]

	_, err = Verify(kr, bad)
	if !errors.Is(err, ErrBadCredential) && !errors.Is(err, ErrInvalidKey) {
		t.Errorf("Verify(bad secret) = %v, want ErrBadCredential or ErrInvalidKey", err)
	}
}

// TestVerifyRevoked verifies AC-6: revoked key fails.
func TestVerifyRevoked(t *testing.T) {
	kr := NewMemKeyring()
	key, plaintext, err := Generate("t", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := kr.Insert(key); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := kr.Revoke(key.ID, time.Now()); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err = Verify(kr, plaintext)
	if !errors.Is(err, ErrRevokedKey) {
		t.Errorf("Verify(revoked) = %v, want ErrRevokedKey", err)
	}
}

// TestVerifyNotFound verifies ErrKeyNotFound on unknown key.
func TestVerifyNotFound(t *testing.T) {
	kr := NewMemKeyring()
	_, plaintext, err := Generate("t", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Do not Insert.
	_, err = Verify(kr, plaintext)
	if !errors.Is(err, ErrKeyNotFound) {
		t.Errorf("Verify(not inserted) = %v, want ErrKeyNotFound", err)
	}
}

// TestVerifyMalformed tests various malformed key strings.
var malformedTests = []struct {
	name      string
	plaintext string
}{
	{"empty", ""},
	{"too short", "sk_abc"},
	{"wrong prefix", "xx_" + strings.Repeat("a", keyIDSuffixLen) + "_" + strings.Repeat("b", keySecretLen)},
	{"no separator", "sk_" + strings.Repeat("a", keyIDSuffixLen) + strings.Repeat("b", keySecretLen+1)},
}

func TestVerifyMalformed(t *testing.T) {
	kr := NewMemKeyring()
	for _, tt := range malformedTests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, err := Verify(kr, tt.plaintext)
			if !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Verify(%q) = %v, want ErrInvalidKey", tt.name, err)
			}
		})
	}
}

// TestMemKeyringConcurrent verifies safe concurrent use under -race.
// It does not assert on verify results because concurrent revocations make
// success/failure non-deterministic; the race detector is the actual check.
func TestMemKeyringConcurrent(t *testing.T) {
	kr := NewMemKeyring()

	const n = 50
	keys := make([]Key, n)
	plaintexts := make([]string, n)
	for i := 0; i < n; i++ {
		k, p, err := Generate("tenant", RoleAgent)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		keys[i] = k
		plaintexts[i] = p
		if err := kr.Insert(k); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}

	var wg sync.WaitGroup
	// Concurrent verifications — only the non-revoked half guarantees success.
	for i := n / 2; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Verify(kr, plaintexts[i]); err != nil {
				t.Errorf("Verify[%d] should succeed: %v", i, err)
			}
		}()
	}
	// Concurrent revocations on the first half.
	for i := 0; i < n/2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = kr.Revoke(keys[i].ID, time.Now())
		}()
	}
	// Concurrent reads on the revoked half — race detector checks for data races.
	for i := 0; i < n/2; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = Verify(kr, plaintexts[i]) // result is non-deterministic; race is what we test
		}()
	}
	wg.Wait()
}

// TestMemKeyringList verifies MemKeyring.List returns keys filtered by tenant.
func TestMemKeyringList(t *testing.T) {
	kr := NewMemKeyring()

	k1, _, _ := Generate("tenant-a", RoleAgent)
	k2, _, _ := Generate("tenant-a", RoleAdmin)
	k3, _, _ := Generate("tenant-b", RoleAgent)
	_ = kr.Insert(k1)
	_ = kr.Insert(k2)
	_ = kr.Insert(k3)

	// Filter by tenant-a: should return k1 and k2.
	got, err := kr.List("tenant-a")
	if err != nil {
		t.Fatalf("List(tenant-a): %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List(tenant-a) = %d keys, want 2", len(got))
	}

	// Filter by tenant-b: should return k3.
	got, err = kr.List("tenant-b")
	if err != nil {
		t.Fatalf("List(tenant-b): %v", err)
	}
	if len(got) != 1 {
		t.Errorf("List(tenant-b) = %d keys, want 1", len(got))
	}

	// List all (empty string): should return all 3.
	got, err = kr.List("")
	if err != nil {
		t.Fatalf("List(): %v", err)
	}
	if len(got) != 3 {
		t.Errorf("List() = %d keys, want 3", len(got))
	}
}

// TestParseKeyRoundTrip verifies generate → parseKey round-trip.
func TestParseKeyRoundTrip(t *testing.T) {
	_, plaintext, err := Generate("t", RoleAgent)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	id, secret, err := parseKey(plaintext)
	if err != nil {
		t.Fatalf("parseKey: %v", err)
	}
	if !strings.HasPrefix(id, "sk_") {
		t.Errorf("id %q does not start with sk_", id)
	}
	if len(secret) != 32 {
		t.Errorf("secret len = %d, want 32", len(secret))
	}
}

// FuzzParseKey is a fuzz target for parseKey. It asserts:
//   - parseKey never panics.
//   - On success, the ID starts with "sk_" and secret is 32 bytes.
func FuzzParseKey(f *testing.F) {
	// Seed corpus: valid key.
	_, plaintext, err := Generate("fuzz-tenant", RoleAgent)
	if err != nil {
		f.Fatalf("Generate: %v", err)
	}
	f.Add(plaintext)
	// Seed: various malformed inputs.
	f.Add("")
	f.Add("sk_")
	f.Add("not-a-key")
	f.Add("sk_" + strings.Repeat("a", keyIDSuffixLen) + "_" + strings.Repeat("b", keySecretLen))

	f.Fuzz(func(t *testing.T, s string) {
		id, secret, err := parseKey(s)
		if err != nil {
			return // expected for malformed input; no panic = OK
		}
		if !strings.HasPrefix(id, "sk_") {
			t.Errorf("parseKey: id %q does not start with sk_", id)
		}
		if len(secret) != 32 {
			t.Errorf("parseKey: secret len = %d, want 32", len(secret))
		}
	})
}
