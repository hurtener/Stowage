package pipeline

import "sync"

// keyMutex provides per-key mutual exclusion with automatic map compaction.
// Goroutines waiting on the same key are serialised; goroutines waiting on
// different keys proceed concurrently. The global mu is held only briefly
// during ref-count bookkeeping.
type keyMutex struct {
	mu    sync.Mutex
	locks map[string]*keyEntry
}

type keyEntry struct {
	mu   sync.Mutex
	refs int // number of goroutines that hold or are waiting for this entry
}

func newKeyMutex() *keyMutex {
	return &keyMutex{locks: make(map[string]*keyEntry)}
}

// Lock acquires the per-key mutex for key.
func (km *keyMutex) Lock(key string) {
	km.mu.Lock()
	e, ok := km.locks[key]
	if !ok {
		e = &keyEntry{}
		km.locks[key] = e
	}
	e.refs++
	km.mu.Unlock()
	e.mu.Lock()
}

// Unlock releases the per-key mutex for key and compacts the map when idle.
func (km *keyMutex) Unlock(key string) {
	km.mu.Lock()
	e := km.locks[key]
	e.refs--
	if e.refs == 0 {
		delete(km.locks, key)
	}
	km.mu.Unlock()
	e.mu.Unlock()
}
