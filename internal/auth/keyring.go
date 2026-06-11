package auth

import (
	"fmt"
	"sync"
	"time"
)

// Keyring stores and retrieves API keys.
// The store-backed driver arrives in Phase 03 against the same interface and
// conformance test.
type Keyring interface {
	// Lookup returns the Key with the given ID, or ErrKeyNotFound.
	Lookup(id string) (*Key, error)
	// Insert stores a new key. Returns an error if the ID already exists.
	Insert(key Key) error
	// Revoke marks a key as revoked at the given time.
	Revoke(id string, at time.Time) error
}

// MemKeyring is an in-memory Keyring. Safe for concurrent use.
type MemKeyring struct {
	mu   sync.RWMutex
	keys map[string]Key
}

// NewMemKeyring returns an empty MemKeyring.
func NewMemKeyring() *MemKeyring {
	return &MemKeyring{keys: make(map[string]Key)}
}

// Lookup returns the Key with the given ID.
func (k *MemKeyring) Lookup(id string) (*Key, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	key, ok := k.keys[id]
	if !ok {
		return nil, ErrKeyNotFound
	}
	// Return a copy to prevent external mutation.
	c := key
	return &c, nil
}

// Insert stores key. Returns an error if a key with the same ID already exists.
func (k *MemKeyring) Insert(key Key) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, exists := k.keys[key.ID]; exists {
		return fmt.Errorf("auth: key %q already exists", key.ID)
	}
	k.keys[key.ID] = key
	return nil
}

// Revoke marks the key with the given ID as revoked at time at.
func (k *MemKeyring) Revoke(id string, at time.Time) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	key, ok := k.keys[id]
	if !ok {
		return ErrKeyNotFound
	}
	key.RevokedAt = &at
	k.keys[id] = key
	return nil
}
