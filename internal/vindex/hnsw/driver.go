package hnsw

import (
	"context"
	"sort"
	"sync"

	coderhnsw "github.com/coder/hnsw"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

// HNSW construction constants (D-034 knob guardrail: profile-internal, not
// operator-tunable in v1; revisit with Phase 13 eval benchmarks).
const (
	hnswM        = 16 // max neighbours per node (OpenAI-tuned default)
	hnswEfSearch = 64 // candidate pool size during search; higher = better recall
)

// overFetchCap is the maximum candidate count returned by graph.Search on a
// single call. For graphs with Len ≤ overFetchCap, Search requests ALL nodes
// (giving near-brute-force recall ≥ 0.99 as verified empirically with seed 42
// corpus). For larger graphs, Search requests min(k×4+16, overFetchCap)
// candidates, relying on HNSW navigability for approximate recall.
//
// Rationale for 2048: the coder/hnsw v0.6.1 graph.Search uses a strict
// "no improvement over current best" termination condition that limits recall
// for random unit vectors when fetchN << Len. Fetching ALL nodes for small
// graphs sidesteps this limitation. For production graphs (>2048 vectors)
// HNSW navigability provides adequate recall on clustered real embeddings.
//
// D-034 knob guardrail: not operator-tunable in v1.
const overFetchCap = 2048

// driver is the HNSW vindex.Index implementation.
//
// Isolation model:
//   - One tenantGraph per scope.Tenant (the outer isolation boundary).
//   - Sub-tenant isolation (project/user/session) is enforced via sidecar
//     filtering on the Search path, not by separate graphs.
//   - The per-tenantGraph RWMutex guards the coder/hnsw Graph (not
//     concurrency-safe) and the sidecar maps.
//
// Lazy build:
//   - The graph is built on first Search by scanning the full tenant's vectors
//     from the BLOB store. Rebuild-from-BLOBs is O(n·log n) inserts and
//     acceptable; no graph persistence in Phase 09b.
//
// Incremental upsert after build:
//   - scope info (project/user/session) is populated immediately.
//   - kind and createdAt are unknown; the entry is added to pendingMeta.
//   - On the next Search, if pendingMeta is non-empty, one vs.Scan refreshes
//     kind/createdAt for all pending entries before filtering. This is the
//     only store round-trip on the search path (and only when new entries
//     exist since the last search). The deviation from "zero store round-trips
//     on search path" is documented here and in D-048.
type driver struct {
	vs    store.VectorStore
	dims  int
	model string

	mu      sync.RWMutex            // guards tenants map
	tenants map[string]*tenantGraph // key = scope.Tenant
}

// tenantGraph holds the per-tenant HNSW graph and metadata sidecar.
type tenantGraph struct {
	mu          sync.RWMutex
	graph       *coderhnsw.Graph[string]
	meta        map[string]metaEntry // memoryID → metadata
	pendingMeta map[string]bool      // memoryIDs needing kind/createdAt refresh
	built       bool                 // lazy-build done flag
}

// invalidateLocked discards the in-memory graph and sidecar so the next Search
// lazy-rebuilds from the vector store. Caller must hold tg.mu for writing.
//
// This is the ONLY safe way to remove or replace a node: coder/hnsw v0.6.1
// Graph.Delete leaves dangling inbound edges (D-066), so graph.Delete must
// never be called on a graph that will be searched or added to again.
func (tg *tenantGraph) invalidateLocked() {
	tg.graph = newGraph()
	tg.meta = make(map[string]metaEntry)
	tg.pendingMeta = make(map[string]bool)
	tg.built = false
}

// New creates a new HNSW vindex driver.
// dims is the expected embedding dimensionality; 0 means unchecked.
// model is the embedding model name written to the vector store.
func New(vs store.VectorStore, dims int, model string) vindex.Index {
	return &driver{
		vs:      vs,
		dims:    dims,
		model:   model,
		tenants: make(map[string]*tenantGraph),
	}
}

func init() {
	// Register under the name "hnsw" so vindex.Open(cfg) can select it via
	// a blank import of this package.
	vindex.Register("hnsw", func(vs store.VectorStore, dims int, model string) (vindex.Index, error) {
		return New(vs, dims, model), nil
	})
}

// newGraph returns a fresh coder/hnsw graph tuned for Stowage.
func newGraph() *coderhnsw.Graph[string] {
	g := coderhnsw.NewGraph[string]()
	g.M = hnswM
	g.EfSearch = hnswEfSearch
	// Distance function: CosineDistance = 1 − cosine_similarity.
	// Smaller distance → higher similarity. Score reported to callers as
	// 1 − distance = cosine_similarity ∈ [0,1].
	g.Distance = coderhnsw.CosineDistance
	return g
}

// getOrCreate returns the tenantGraph for the given tenant key, creating it if
// absent. Uses double-checked locking for minimal contention.
func (d *driver) getOrCreate(tenant string) *tenantGraph {
	d.mu.RLock()
	tg := d.tenants[tenant]
	d.mu.RUnlock()
	if tg != nil {
		return tg
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if tg = d.tenants[tenant]; tg != nil {
		return tg
	}
	tg = &tenantGraph{
		graph:       newGraph(),
		meta:        make(map[string]metaEntry),
		pendingMeta: make(map[string]bool),
	}
	d.tenants[tenant] = tg
	return tg
}

// Upsert inserts or replaces the embedding for memoryID within scope.
func (d *driver) Upsert(ctx context.Context, scope identity.Scope, memoryID string, vec []float32) error {
	if d.dims > 0 && len(vec) != d.dims {
		return vindex.ErrDimsMismatch{Got: len(vec), Want: d.dims}
	}

	// Persist to BLOB store (source of truth, D-046; HNSW graph rebuilds from
	// here at boot). This call happens outside any tenant lock.
	if err := d.vs.Upsert(ctx, scope, store.StoredVector{
		MemoryID: memoryID,
		Vec:      vec,
		Dims:     len(vec),
		Model:    d.model,
	}); err != nil {
		return err
	}

	tg := d.getOrCreate(scope.Tenant)

	tg.mu.Lock()
	defer tg.mu.Unlock()

	if !tg.built {
		// Graph not yet built; the lazy build on first Search will pick up
		// this vector from the store. Nothing to add to the in-memory graph.
		return nil
	}

	// Incremental add for NEW keys only. coder/hnsw v0.6.1 Graph.Delete leaves
	// dangling INBOUND edges (adjacency is asymmetric; isolate() only removes
	// the edges the node itself knows about), so a graph that has seen a
	// Delete can later crash inside Add: traversal reaches the deleted node,
	// adopts its key as the inter-layer elevator, and `layer.nodes[*elevator]`
	// on the next layer is nil (SIGSEGV seen in CI, reproduced locally with
	// -count=40). Delete-then-Add is therefore forbidden: a duplicate-key
	// upsert invalidates the tenant graph and the next Search lazy-rebuilds
	// from the vector store (D-066).
	if d.dims == 0 {
		if tg.graph.Len() > 0 && len(vec) != tg.graph.Dims() {
			return vindex.ErrDimsMismatch{Got: len(vec), Want: tg.graph.Dims()}
		}
	}
	if _, exists := tg.graph.Lookup(memoryID); exists {
		tg.invalidateLocked()
		return nil
	}
	tg.graph.Add(coderhnsw.MakeNode(memoryID, vec))

	// Populate sidecar with scope info (available from the Upsert call).
	// kind and createdAt are unknown here; they require a vs.Scan. Mark as
	// pending so the next Search refreshes them before filtering.
	tg.meta[memoryID] = metaEntry{
		project: scope.Project,
		userID:  scope.User,
		session: scope.Session,
		// kind and createdAt left zero — pending refresh.
	}
	tg.pendingMeta[memoryID] = true

	return nil
}

// Delete removes the embedding for memoryID. No-op when absent.
//
// Deletion semantics (D-066, amends the D-048 hard-delete finding): the BLOB
// store row is removed immediately; the in-memory graph is invalidated and
// lazily rebuilt on the next Search rather than mutated in place, because
// coder/hnsw v0.6.1 Graph.Delete leaves dangling inbound edges that crash
// later Adds.
func (d *driver) Delete(ctx context.Context, scope identity.Scope, memoryID string) error {
	// Remove from BLOB store first.
	if err := d.vs.Delete(ctx, scope, memoryID); err != nil {
		return err
	}

	d.mu.RLock()
	tg := d.tenants[scope.Tenant]
	d.mu.RUnlock()
	if tg == nil {
		return nil // tenant graph never created; nothing to do
	}

	tg.mu.Lock()
	defer tg.mu.Unlock()

	// Never graph.Delete (dangling inbound edges, D-066): when the key is in
	// the built graph, invalidate and let the next Search lazy-rebuild from
	// the vector store (the BLOB row is already gone, so the rebuilt graph
	// excludes it). Absent key or unbuilt graph: nothing to do.
	if _, exists := tg.graph.Lookup(memoryID); exists {
		tg.invalidateLocked()
	} else {
		delete(tg.meta, memoryID)
		delete(tg.pendingMeta, memoryID)
	}

	return nil
}

// Search returns the top-k most similar memories within scope.
// Returns nil, nil when vec is nil (degraded-mode signal).
func (d *driver) Search(ctx context.Context, scope identity.Scope, vec []float32, k int, f vindex.Filter) ([]vindex.Hit, error) {
	if vec == nil {
		return nil, nil
	}
	if k <= 0 {
		return nil, nil
	}

	tg := d.getOrCreate(scope.Tenant)

	// Phase 1: ensure graph is built and sidecar is current (write lock).
	// Re-check conditions inside the write lock to handle concurrent callers.
	tg.mu.Lock()
	if !tg.built {
		if err := d.lazyBuild(ctx, scope.Tenant, tg); err != nil {
			tg.mu.Unlock()
			return nil, err
		}
	}
	if len(tg.pendingMeta) > 0 {
		if err := d.refreshSidecar(ctx, scope.Tenant, tg); err != nil {
			tg.mu.Unlock()
			return nil, err
		}
	}
	tg.mu.Unlock()

	// Phase 2: read-only search under shared lock.
	tg.mu.RLock()
	defer tg.mu.RUnlock()

	if tg.graph.Len() == 0 {
		return nil, nil
	}

	// Protect against dimension mismatch (graph.Search panics on wrong dims).
	if graphDims := tg.graph.Dims(); graphDims > 0 && len(vec) != graphDims {
		return nil, nil // degraded: return empty rather than panic
	}

	// Over-fetch: for small graphs (Len ≤ overFetchCap) request ALL nodes so
	// that sorting by cosine similarity produces near-brute-force recall.
	// For large graphs, fetch min(k×4+16, overFetchCap) candidates and rely
	// on HNSW graph navigability (suited to clustered real embeddings).
	// Under-fill (after sidecar filtering) triggers ONE refetch at 4×.
	graphLen := tg.graph.Len()
	var fetchN int
	if graphLen <= overFetchCap {
		fetchN = graphLen // fetch all: high recall for small/test graphs
	} else {
		fetchN = k*4 + 16
		if fetchN > overFetchCap {
			fetchN = overFetchCap
		}
	}

	candidates := tg.graph.Search(vec, fetchN)
	hits := d.filterCandidates(candidates, tg.meta, scope, vec, f)

	// ONE refetch if under-filled and more candidates remain in the graph.
	if len(hits) < k && fetchN < graphLen {
		fetchN2 := fetchN * 4
		if fetchN2 > overFetchCap {
			fetchN2 = overFetchCap
		}
		if fetchN2 > graphLen {
			fetchN2 = graphLen
		}
		if fetchN2 > fetchN {
			candidates2 := tg.graph.Search(vec, fetchN2)
			hits = d.filterCandidates(candidates2, tg.meta, scope, vec, f)
		}
	}

	// coder/hnsw returns candidates in heap-array order (not ranked).
	// Sort by score descending so that hits[:k] yields the true top-k.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].MemoryID < hits[j].MemoryID // stable tie-break
	})

	if k < len(hits) {
		hits = hits[:k]
	}
	return hits, nil
}

// lazyBuild populates the HNSW graph from the BLOB store for the given tenant.
// Must be called with tg.mu held for writing.
// Scans with a tenant-only scope so ALL users' vectors are loaded (sub-tenant
// isolation is enforced by sidecar filtering at Search time, not by separate
// graphs per user).
func (d *driver) lazyBuild(ctx context.Context, tenant string, tg *tenantGraph) error {
	if tg.built {
		return nil
	}

	vectors, err := d.vs.Scan(ctx, identity.Scope{Tenant: tenant}, nil, store.Window{})
	if err != nil {
		return err
	}

	for _, sv := range vectors {
		if d.dims > 0 && len(sv.Vec) != d.dims {
			continue // skip dimension-mismatched entries (should not occur in practice)
		}
		if tg.graph.Len() > 0 && len(sv.Vec) != tg.graph.Dims() {
			continue // inconsistent dims in store; skip silently
		}
		tg.graph.Add(coderhnsw.MakeNode(sv.MemoryID, sv.Vec))
		tg.meta[sv.MemoryID] = metaEntry{
			project:   sv.ProjectID,
			userID:    sv.UserID,
			session:   sv.SessionID,
			kind:      sv.Kind,
			createdAt: sv.CreatedAt,
		}
	}

	tg.built = true
	return nil
}

// refreshSidecar fills in kind and createdAt for entries that were added
// incrementally (after the lazy build) whose metadata was not yet available.
// One vs.Scan call per pending batch — not per Search invocation.
// Must be called with tg.mu held for writing.
func (d *driver) refreshSidecar(ctx context.Context, tenant string, tg *tenantGraph) error {
	vectors, err := d.vs.Scan(ctx, identity.Scope{Tenant: tenant}, nil, store.Window{})
	if err != nil {
		return err
	}
	for _, sv := range vectors {
		if tg.pendingMeta[sv.MemoryID] {
			tg.meta[sv.MemoryID] = metaEntry{
				project:   sv.ProjectID,
				userID:    sv.UserID,
				session:   sv.SessionID,
				kind:      sv.Kind,
				createdAt: sv.CreatedAt,
			}
		}
	}
	tg.pendingMeta = make(map[string]bool) // clear pending set
	return nil
}

// filterCandidates applies sub-scope, kind, and window filters to the HNSW
// candidate list and computes cosine-similarity scores.
// Must be called with tg.mu held for at least reading.
func (d *driver) filterCandidates(
	candidates []coderhnsw.Node[string],
	meta map[string]metaEntry,
	scope identity.Scope,
	query []float32,
	f vindex.Filter,
) []vindex.Hit {
	// Build kind lookup set once.
	kindSet := make(map[string]bool, len(f.Kinds))
	for _, kd := range f.Kinds {
		kindSet[kd] = true
	}

	var hits []vindex.Hit
	for _, node := range candidates {
		m, hasMeta := meta[node.Key]
		if !hasMeta {
			// Fail CLOSED for isolation (P3, Phase 30): a node with no sidecar provenance has
			// unknown scope, so it cannot be proven to belong to a sub-scoped query — drop it
			// rather than emit it to whatever user is querying. (Every Upsert/lazyBuild sets
			// meta alongside graph.Add, so this is defensive, not a reachable path.) When the
			// query carries no sub-scope (tenant-only), there is nothing to isolate → keep it.
			if scope.Project != "" || scope.User != "" || scope.Session != "" {
				continue
			}
		}
		if hasMeta {
			// Sub-scope filter (project/user/session).
			if scope.Project != "" && m.project != scope.Project {
				continue
			}
			if scope.User != "" && m.userID != scope.User {
				continue
			}
			if scope.Session != "" && m.session != scope.Session {
				continue
			}
			// Kind filter: only skip when both sides are known.
			if len(f.Kinds) > 0 && m.kind != "" && !kindSet[m.kind] {
				continue
			}
			// Window filter: only skip when createdAt is known and out of range.
			if f.Window.From > 0 && m.createdAt != 0 && m.createdAt < f.Window.From {
				continue
			}
			if f.Window.Until > 0 && m.createdAt != 0 && m.createdAt > f.Window.Until {
				continue
			}
		}
		// Score = cosine similarity ∈ [0,1]; coder/hnsw uses CosineDistance
		// = 1 − cosine_similarity, so score = 1 − distance. We recompute here
		// using vindex.CosineSimilarity to keep the same kernel as the brute driver.
		score := vindex.CosineSimilarity(query, node.Value)
		hits = append(hits, vindex.Hit{MemoryID: node.Key, Score: score})
	}

	return hits
}
