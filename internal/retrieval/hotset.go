package retrieval

import (
	"container/list"
	"sync"
	"sync/atomic"

	"github.com/hurtener/stowage/internal/identity"
)

const (
	// hotSetDefaultCap is the maximum number of entries tracked per scope.
	hotSetDefaultCap = 512
)

// HotSet maintains a per-scope LRU of memory IDs ranked by injection frequency.
// It is fed by the injection writer (via Record) and used to expose metrics
// about which memories are most frequently injected.
//
// v1 scope: maintain + expose metrics only; retrieval fast-path consumption
// is measured by the SLO rig before wiring (D-053).
type HotSet struct {
	mu     sync.Mutex
	scopes map[string]*hotScopeSet
	cap    int

	// injections is a monotonic count of calls to Record.
	injections atomic.Int64
}

type hotScopeSet struct {
	lru     *list.List
	entries map[string]*list.Element
	cap     int
}

type hotEntry struct {
	memoryID string
	count    int64
}

// NewHotSet creates a HotSet with the given per-scope capacity.
// If cap <= 0, hotSetDefaultCap is used.
func NewHotSet(cap int) *HotSet {
	if cap <= 0 {
		cap = hotSetDefaultCap
	}
	return &HotSet{
		scopes: make(map[string]*hotScopeSet),
		cap:    cap,
	}
}

// Record registers that memoryID was injected into a response for the given
// scope. Thread-safe.
func (h *HotSet) Record(scope identity.Scope, memoryID string) {
	// Non-lossy key (not scope.String(), which drops User when Project is empty) so
	// per-user injection sets don't conflate across users within a tenant (Phase 30 B2).
	scopeStr := scopeCacheKey(scope)
	h.injections.Add(1)

	h.mu.Lock()
	defer h.mu.Unlock()

	ss, ok := h.scopes[scopeStr]
	if !ok {
		ss = &hotScopeSet{
			lru:     list.New(),
			entries: make(map[string]*list.Element, h.cap),
			cap:     h.cap,
		}
		h.scopes[scopeStr] = ss
	}

	if el, ok := ss.entries[memoryID]; ok {
		ss.lru.MoveToFront(el)
		el.Value.(*hotEntry).count++
		return
	}

	// Evict LRU tail when at capacity.
	if ss.lru.Len() >= ss.cap {
		tail := ss.lru.Back()
		if tail != nil {
			old := tail.Value.(*hotEntry)
			delete(ss.entries, old.memoryID)
			ss.lru.Remove(tail)
		}
	}

	e := &hotEntry{memoryID: memoryID, count: 1}
	el := ss.lru.PushFront(e)
	ss.entries[memoryID] = el
}

// TopN returns the top N memory IDs by injection count for the given scope.
// Returns at most N entries; may return fewer if fewer are tracked.
func (h *HotSet) TopN(scope identity.Scope, n int) []string {
	if n <= 0 {
		return nil
	}
	scopeStr := scopeCacheKey(scope)

	h.mu.Lock()
	defer h.mu.Unlock()

	ss, ok := h.scopes[scopeStr]
	if !ok {
		return nil
	}

	type kv struct {
		id    string
		count int64
	}
	all := make([]kv, 0, ss.lru.Len())
	for el := ss.lru.Front(); el != nil; el = el.Next() {
		e := el.Value.(*hotEntry)
		all = append(all, kv{e.memoryID, e.count})
	}

	// Selection sort — set is small (≤ hotSetDefaultCap).
	limit := n
	if limit > len(all) {
		limit = len(all)
	}
	for i := range limit {
		max := i
		for j := i + 1; j < len(all); j++ {
			if all[j].count > all[max].count {
				max = j
			}
		}
		all[i], all[max] = all[max], all[i]
	}

	out := make([]string, limit)
	for i := range limit {
		out[i] = all[i].id
	}
	return out
}

// ScopeCount returns the number of distinct scopes currently tracked.
func (h *HotSet) ScopeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.scopes)
}

// TotalInjections returns total Record calls since creation.
func (h *HotSet) TotalInjections() int64 { return h.injections.Load() }
