// Package hnsw provides a pure-Go HNSW vector index driver for Stowage
// (Phase 09b, D-048). It wraps github.com/coder/hnsw v0.6.1 behind the
// vindex.Index seam with per-tenant graphs and a metadata sidecar for
// scope-aware filtering without store round-trips on the search path.
//
// Deletion semantics (coder/hnsw v0.6.1): the library supports HARD deletes
// via Graph.Delete, which removes the node from all layers and replenishes
// neighborhood connectivity. No tombstone set is required. Verified at D-048.
package hnsw

// metaEntry is the per-memory metadata carried in the sidecar map.
// It enables sub-scope, kind, and window filtering without store round-trips
// on the hot Search path.
//
// project, userID, session are populated from the identity.Scope passed to
// Upsert (available at call time). kind and createdAt require a vs.Scan to
// resolve; they are set during the lazy build and on the next search after
// an incremental upsert (via refreshSidecar — one scan per pending batch,
// not per search invocation).
type metaEntry struct {
	project   string
	userID    string
	session   string
	kind      string
	createdAt int64 // unix ms; 0 = unknown (pending refresh)
}
