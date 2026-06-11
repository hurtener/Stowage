package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

const defaultCacheSize = 50_000

// EmbedCache is a thread-safe LRU cache for embedding vectors keyed by
// (model, sha256(input)). Default capacity is 50 000 entries.
type EmbedCache struct {
	mu    sync.Mutex
	cap   int
	items map[embedCacheKey][]float32
	order []embedCacheKey // index 0 = LRU, last = MRU
}

type embedCacheKey struct {
	model string
	hash  string // hex-encoded sha256 of input
}

// NewEmbedCache returns an EmbedCache with the given capacity.
// capacity <= 0 uses the default (50 000).
func NewEmbedCache(capacity int) *EmbedCache {
	if capacity <= 0 {
		capacity = defaultCacheSize
	}
	return &EmbedCache{
		cap:   capacity,
		items: make(map[embedCacheKey][]float32, capacity),
		order: make([]embedCacheKey, 0, capacity),
	}
}

// Get returns the cached vector for (model, input), or (nil, false) if absent.
func (c *EmbedCache) Get(model, input string) ([]float32, bool) {
	k := embedCacheKey{model: model, hash: hashInput(input)}
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[k]
	if ok {
		c.promote(k)
	}
	return v, ok
}

// Put stores a vector, evicting the LRU entry when at capacity.
func (c *EmbedCache) Put(model, input string, vec []float32) {
	k := embedCacheKey{model: model, hash: hashInput(input)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[k]; exists {
		c.items[k] = vec
		c.promote(k)
		return
	}
	if len(c.items) >= c.cap {
		lru := c.order[0]
		c.order = c.order[1:]
		delete(c.items, lru)
	}
	c.items[k] = vec
	c.order = append(c.order, k)
}

// promote moves k to the MRU position. Must be called with mu held.
func (c *EmbedCache) promote(k embedCacheKey) {
	for i, ck := range c.order {
		if ck == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, k)
			return
		}
	}
}

func hashInput(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
