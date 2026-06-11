package retrieval

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sort"
	"sync"
)

// hubMaxSize is the default maximum number of distinct memory IDs tracked by
// the Hub LRU. Each entry is ~(memoryID string + map of sigs). 4096 entries at
// ~100 bytes each ≈ 400 KB worst case. Not a config knob (D-034 guardrail).
const hubMaxSize = 4096

// Hub tracks the number of distinct query clusters that have returned each
// memory in the recent retrieve window. It is used by the scoring layer to
// detect "hub" memories — generic content returned by many unrelated queries —
// and apply the hub-dampening factor (Phase 10).
//
// Hub is safe for concurrent use (sync.Mutex-protected LRU).
// It is not persisted; signals reset on process restart. This is intentional:
// hub dampening is a per-session or per-process heuristic, not a durable signal.
type Hub struct {
	mu      sync.Mutex
	lru     *list.List
	entries map[string]*list.Element
	maxSize int
}

type hubEntry struct {
	memoryID string
	sigs     map[string]struct{}
}

// NewHub creates a Hub bounded to maxSize entries (LRU eviction).
func NewHub(maxSize int) *Hub {
	if maxSize <= 0 {
		maxSize = hubMaxSize
	}
	return &Hub{
		lru:     list.New(),
		entries: make(map[string]*list.Element, maxSize),
		maxSize: maxSize,
	}
}

// Record registers that memoryID was returned by the query identified by
// querySig. When the same querySig is recorded multiple times for the same
// memory it counts only once (set semantics).
func (h *Hub) Record(memoryID, querySig string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if el, ok := h.entries[memoryID]; ok {
		h.lru.MoveToFront(el)
		el.Value.(*hubEntry).sigs[querySig] = struct{}{}
		return
	}

	// New entry — evict LRU tail if at capacity.
	if h.lru.Len() >= h.maxSize {
		tail := h.lru.Back()
		if tail != nil {
			old := tail.Value.(*hubEntry)
			delete(h.entries, old.memoryID)
			h.lru.Remove(tail)
		}
	}

	e := &hubEntry{
		memoryID: memoryID,
		sigs:     map[string]struct{}{querySig: {}},
	}
	el := h.lru.PushFront(e)
	h.entries[memoryID] = el
}

// Signals returns the number of distinct query clusters that have returned
// memoryID. Returns 0 for unknown memory IDs.
func (h *Hub) Signals(memoryID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if el, ok := h.entries[memoryID]; ok {
		return len(el.Value.(*hubEntry).sigs)
	}
	return 0
}

// QuerySig returns a short, stable signature for a set of query tokens.
// The signature is the SHA-256 hash of the sorted, joined tokens.
// Tokens are sorted so that query-order variation does not produce distinct
// signatures for semantically equivalent queries.
func QuerySig(tokens []string) string {
	if len(tokens) == 0 {
		return "empty"
	}
	sorted := make([]string, len(tokens))
	copy(sorted, tokens)
	sort.Strings(sorted)
	joined := fmt.Sprint(sorted)
	sum := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("%x", sum[:8]) // 16 hex chars — collision-resistant for this use
}
