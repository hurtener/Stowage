// Package retrieval — result cache (hot-warm, Phase 12, D-053).
//
// Key: (scope.String(), querySig, profile, windowFrom, windowTo)
// Invalidation: per-scope generation counter; O(1) bump on write, O(1) check on read.
// TTL: 60 s (injectable clock for tests).
// Cap: 8192 entries.
// Escape hatch: STOWAGE_CACHE_OFF=1 disables the cache entirely.
package retrieval

import (
	"container/list"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hurtener/stowage/internal/identity"
)

const (
	cacheDefaultCap = 8192
	cacheTTL        = 60 * time.Second
)

// ResultCache is an LRU result cache with per-scope generation-based
// invalidation. It is safe for concurrent use.
type ResultCache struct {
	mu      sync.Mutex
	lru     *list.List
	entries map[string]*list.Element
	cap     int

	// gens maps scope string → monotonic generation counter.
	// A cached entry is only valid when its stored generation equals the
	// current scope gen.
	gens map[string]uint64

	// now is an injectable clock for tests; nil = time.Now.
	now func() time.Time

	hits   atomic.Int64
	misses atomic.Int64
}

type cacheEntry struct {
	key        string
	scopeStr   string
	generation uint64
	items      []MemoryItem
	support    Support
	expiresAt  time.Time
}

// cacheKey returns the canonical string key for a retrieve call.
// sessionID is included because it affects the utility score (write-echo cooldown).
func cacheKey(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d",
		scope.String(), querySig, profile, sessionID, windowFrom, windowTo)
}

// NewResultCache creates a ResultCache with the given capacity.
// If cap <= 0, cacheDefaultCap is used.
func NewResultCache(cap int) *ResultCache {
	if cap <= 0 {
		cap = cacheDefaultCap
	}
	return &ResultCache{
		lru:     list.New(),
		entries: make(map[string]*list.Element, cap),
		gens:    make(map[string]uint64),
		cap:     cap,
	}
}

// cacheOff reports whether STOWAGE_CACHE_OFF=1 is set.
func cacheOff() bool {
	return os.Getenv("STOWAGE_CACHE_OFF") == "1"
}

func (c *ResultCache) nowFunc() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Get returns the cached items and support for the given key, if the entry
// exists, has not expired, and belongs to the current scope generation.
func (c *ResultCache) Get(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64) ([]MemoryItem, Support, bool) {
	if cacheOff() {
		return nil, Support{}, false
	}
	key := cacheKey(scope, querySig, profile, sessionID, windowFrom, windowTo)
	scopeStr := scope.String()
	now := c.nowFunc()

	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		return nil, Support{}, false
	}
	e := el.Value.(*cacheEntry)

	// TTL check.
	if now.After(e.expiresAt) {
		c.lru.Remove(el)
		delete(c.entries, key)
		c.misses.Add(1)
		return nil, Support{}, false
	}

	// Generation check: scope must not have been written to since this entry
	// was stored.
	if e.generation != c.gens[scopeStr] {
		c.lru.Remove(el)
		delete(c.entries, key)
		c.misses.Add(1)
		return nil, Support{}, false
	}

	c.lru.MoveToFront(el)
	c.hits.Add(1)
	return e.items, e.support, true
}

// Put stores a result under the given key. Evicts the LRU tail when at
// capacity. Updating an existing key refreshes the TTL and moves it to front.
func (c *ResultCache) Put(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64, items []MemoryItem, support Support) {
	if cacheOff() {
		return
	}
	key := cacheKey(scope, querySig, profile, sessionID, windowFrom, windowTo)
	scopeStr := scope.String()
	now := c.nowFunc()

	c.mu.Lock()
	defer c.mu.Unlock()

	gen := c.gens[scopeStr]

	if el, ok := c.entries[key]; ok {
		c.lru.MoveToFront(el)
		e := el.Value.(*cacheEntry)
		e.items = items
		e.support = support
		e.generation = gen
		e.expiresAt = now.Add(cacheTTL)
		return
	}

	// Evict LRU tail if at capacity.
	for c.lru.Len() >= c.cap {
		tail := c.lru.Back()
		if tail == nil {
			break
		}
		old := tail.Value.(*cacheEntry)
		delete(c.entries, old.key)
		c.lru.Remove(tail)
	}

	e := &cacheEntry{
		key:        key,
		scopeStr:   scopeStr,
		generation: gen,
		items:      items,
		support:    support,
		expiresAt:  now.Add(cacheTTL),
	}
	el := c.lru.PushFront(e)
	c.entries[key] = el
}

// InvalidateScope bumps the per-scope generation counter, logically
// invalidating all cached entries for the given scope without a scan. O(1).
func (c *ResultCache) InvalidateScope(scope identity.Scope) {
	scopeStr := scope.String()
	c.mu.Lock()
	c.gens[scopeStr]++
	c.mu.Unlock()
}

// Stats returns (hits, misses) since creation.
func (c *ResultCache) Stats() (hits, misses int64) {
	return c.hits.Load(), c.misses.Load()
}

// ScopeInvalidator is the narrow interface the reconcile stage uses to
// invalidate the result cache after a content-changing commit.
// Defined here to avoid a retrieval→reconcile circular import (D-053).
type ScopeInvalidator interface {
	InvalidateScope(scope identity.Scope)
}
