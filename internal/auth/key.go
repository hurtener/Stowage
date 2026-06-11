// Package auth implements the API-key model with constant-time verification.
// Key format: sk_<22-char base64url id>_<43-char base64url 256-bit secret>
// Secrets are stored as SHA-256 hashes only; plaintexts are shown once (AC-6).
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Role is the authorization level of an API key.
type Role string

const (
	RoleAgent Role = "agent"
	RoleAdmin Role = "admin"
)

// Key is a stored API key record.
type Key struct {
	ID        string // "sk_" + 22-char base64url public identifier
	TenantID  string
	Role      Role
	Hash      [32]byte // SHA-256 of the 32-byte secret part
	CreatedAt time.Time
	RevokedAt *time.Time // nil means active
}

// ErrInvalidKey is returned for malformed key strings.
var ErrInvalidKey = errors.New("auth: invalid key format")

// ErrRevokedKey is returned when a key has been revoked.
var ErrRevokedKey = errors.New("auth: key is revoked")

// ErrKeyNotFound is returned when the key ID is not in the keyring.
var ErrKeyNotFound = errors.New("auth: key not found")

// ErrBadCredential is returned when the secret does not match.
var ErrBadCredential = errors.New("auth: bad credential")

// Key format constants:
//
//	"sk_" (3) + base64url(16 bytes id) (22) + "_" (1) + base64url(32 bytes secret) (43) = 69
const (
	keyIDSuffixLen = 22                            // base64url-encoded 16-byte ID
	keySecretLen   = 43                            // base64url-encoded 32-byte secret
	keyPrefixLen   = 3                             // "sk_"
	keySepPos      = keyPrefixLen + keyIDSuffixLen // position of the underscore separator
	keyTotalLen    = keyPrefixLen + keyIDSuffixLen + 1 + keySecretLen
)

// Generate creates a new Key and returns (key, plaintext).
// The plaintext is shown once; it must not be logged or stored (AC-6,
// CLAUDE.md §7).
func Generate(tenantID string, role Role) (Key, string, error) {
	// 16 bytes → 22 base64url chars (public ID suffix).
	idBuf := make([]byte, 16)
	if _, err := rand.Read(idBuf); err != nil {
		return Key{}, "", fmt.Errorf("auth: generate id: %w", err)
	}

	// 32 bytes → 43 base64url chars (256-bit secret).
	secretBuf := make([]byte, 32)
	if _, err := rand.Read(secretBuf); err != nil {
		return Key{}, "", fmt.Errorf("auth: generate secret: %w", err)
	}

	id := "sk_" + base64.RawURLEncoding.EncodeToString(idBuf)
	secretPart := base64.RawURLEncoding.EncodeToString(secretBuf)
	plaintext := id + "_" + secretPart

	hash := sha256.Sum256(secretBuf)

	key := Key{
		ID:        id,
		TenantID:  tenantID,
		Role:      role,
		Hash:      hash,
		CreatedAt: time.Now().UTC(),
	}
	return key, plaintext, nil
}

// Verify checks plaintext against the keyring.
// It uses crypto/subtle for constant-time hash comparison to prevent timing
// attacks (AC-6). Revocation is checked after the comparison.
func Verify(kr Keyring, plaintext string) (*Key, error) {
	id, secretBytes, err := parseKey(plaintext)
	if err != nil {
		// Still hash to avoid early-exit timing difference.
		dummy := make([]byte, 32)
		_ = sha256.Sum256(dummy)
		return nil, err
	}

	hash := sha256.Sum256(secretBytes)

	key, lookupErr := kr.Lookup(id)

	if lookupErr != nil {
		// Constant-time dummy compare to avoid timing oracle.
		var zero [32]byte
		subtle.ConstantTimeCompare(hash[:], zero[:])
		return nil, lookupErr
	}

	if subtle.ConstantTimeCompare(hash[:], key.Hash[:]) != 1 {
		return nil, ErrBadCredential
	}

	if key.RevokedAt != nil {
		return nil, ErrRevokedKey
	}

	return key, nil
}

// parseKey extracts the public ID and secret bytes from a plaintext key.
// The format is fixed-length (69 chars): sk_<22>_<43>.
func parseKey(plaintext string) (id string, secretBytes []byte, err error) {
	if len(plaintext) != keyTotalLen {
		return "", nil, ErrInvalidKey
	}
	if !strings.HasPrefix(plaintext, "sk_") {
		return "", nil, ErrInvalidKey
	}
	if plaintext[keySepPos] != '_' {
		return "", nil, ErrInvalidKey
	}

	id = plaintext[:keySepPos] // "sk_" + 22 chars
	secretPart := plaintext[keySepPos+1:]

	secretBytes, err = base64.RawURLEncoding.DecodeString(secretPart)
	if err != nil {
		return "", nil, fmt.Errorf("%w: decode secret: %v", ErrInvalidKey, err)
	}
	if len(secretBytes) != 32 {
		return "", nil, ErrInvalidKey
	}

	return id, secretBytes, nil
}
