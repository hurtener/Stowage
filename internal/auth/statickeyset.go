package auth

import (
	"crypto"
	"fmt"
)

// jwkKey is one resolved (public key, alg) pair, produced by parsing a JWK
// Set entry (jwks.go) or minted directly by the test signer
// (validator_test.go). Never holds a private key — staticKeySet only ever
// verifies (L1).
type jwkKey struct {
	key crypto.PublicKey
	alg string
}

// staticKeySet is a fixed map[kid]{key,alg} KeySet — a pure lookup with no
// fetch or refresh of its own. It backs a JWKSKeySet's current parsed
// snapshot (jwks.go — both the jwks.url and jwks.file sources produce one
// after each successful parse) and, directly, the test signer.
type staticKeySet struct {
	keys map[string]jwkKey
}

// newStaticKeySet builds a staticKeySet from resolved (kid -> key,alg) pairs.
func newStaticKeySet(keys map[string]jwkKey) *staticKeySet {
	return &staticKeySet{keys: keys}
}

// KeyByID implements KeySet.
func (s *staticKeySet) KeyByID(kid string) (crypto.PublicKey, string, error) {
	k, ok := s.keys[kid]
	if !ok {
		return nil, "", fmt.Errorf("auth: static keyset: unknown kid %q", kid)
	}
	return k.key, k.alg, nil
}
