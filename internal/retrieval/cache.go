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
	"sort"
	"strings"
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

// scopeCacheKey is the NON-LOSSY scope identity used for cache keying and the per-scope
// generation map. It encodes all four dimensions with a NUL delimiter — NOT scope.String(),
// which omits User when Project is empty and would collapse {Tenant,User:alice} and
// {Tenant,User:bob} to the same key, serving one user another's cached items (Phase 30 B2).
func scopeCacheKey(s identity.Scope) string {
	return s.Tenant + "\x00" + s.Project + "\x00" + s.User + "\x00" + s.Session
}

// scopeGen returns the effective generation for a read scope: the SUM of the generation
// counters of the scope AND its ancestors (tenant → +project → +user → +session). This makes
// invalidation hierarchical — a tenant-wide bump (the lifecycle sweeps invalidate at {Tenant})
// still busts a per-user cached read ({Tenant,User}), while a per-user invalidate (a reconcile
// commit at the memory's scope) busts only that user. Without this, B2's per-user cache key would
// leave per-user reads served stale after a tenant-scoped sweep (Phase 30 B2 follow-on). Caller
// holds c.mu.
func (c *ResultCache) scopeGen(s identity.Scope) uint64 {
	sum := c.gens[scopeCacheKey(identity.Scope{Tenant: s.Tenant})]
	if s.Project != "" {
		sum += c.gens[scopeCacheKey(identity.Scope{Tenant: s.Tenant, Project: s.Project})]
	}
	if s.User != "" {
		sum += c.gens[scopeCacheKey(identity.Scope{Tenant: s.Tenant, Project: s.Project, User: s.User})]
	}
	if s.Session != "" {
		sum += c.gens[scopeCacheKey(s)]
	}
	return sum
}

// cacheKey returns the canonical string key for a retrieve call.
// sessionID is included because it affects the utility score (write-echo cooldown).
func cacheKey(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64, kinds []string, includeLanes bool, limit int) string {
	// Kinds and IncludeLanes change the RESULT SET / item payload, so they MUST be in the key —
	// otherwise a kind-filtered (or lanes-on) request collides with a plain one within the TTL
	// and returns the wrong items (D-115, audit #8). Kinds is sorted for order-independence.
	// limit is the EFFECTIVE (post-default/post-cap) limit: the cached value is the post-trim
	// items[:limit] slice returned verbatim, so two requests differing only by limit must NOT
	// collide (29d S3). Passing the effective value collapses req.Limit=0 and req.Limit=DefaultLimit.
	ks := append([]string(nil), kinds...)
	sort.Strings(ks)
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s\x00%t\x00%d",
		scopeCacheKey(scope), querySig, profile, sessionID, windowFrom, windowTo, strings.Join(ks, ","), includeLanes, limit)
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
func (c *ResultCache) Get(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64, kinds []string, includeLanes bool, limit int) ([]MemoryItem, Support, bool) {
	if cacheOff() {
		return nil, Support{}, false
	}
	key := cacheKey(scope, querySig, profile, sessionID, windowFrom, windowTo, kinds, includeLanes, limit)
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
	// was stored. scopeGen sums the scope's own gen and its ancestors' so a
	// tenant-wide sweep (InvalidateScope at {Tenant}) still busts per-user reads.
	if e.generation != c.scopeGen(scope) {
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
func (c *ResultCache) Put(scope identity.Scope, querySig, profile, sessionID string, windowFrom, windowTo int64, kinds []string, includeLanes bool, limit int, items []MemoryItem, support Support) {
	if cacheOff() {
		return
	}
	key := cacheKey(scope, querySig, profile, sessionID, windowFrom, windowTo, kinds, includeLanes, limit)
	scopeStr := scopeCacheKey(scope)
	now := c.nowFunc()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Stamp the entry with the effective (ancestor-summed) generation so a later
	// tenant-wide InvalidateScope busts it via the same scopeGen sum on read.
	gen := c.scopeGen(scope)

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
	scopeStr := scopeCacheKey(scope)
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
