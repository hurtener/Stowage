// Package vindex is the vector-index seam for Stowage (Phase 09, D-046).
//
// The Index interface abstracts over vector search backends:
//   - v1: brute-force float32-LE BLOB scan + cosine in Go (no pgvector required)
//   - future: gohnsw, pgvector-native (same seam, new driver)
//
// Vectors are stored via the store.VectorStore seam; vindex wraps it and
// never owns a DB connection (D-046 "vindex wraps the store").
//
// Usage:
//
//	vi := vindex.New(st.Vectors(), cfg.Gateway.EmbedDims, cfg.Gateway.EmbedModel)
//	hits, err := vi.Search(ctx, scope, queryVec, 20, vindex.Filter{})
package vindex

import (
	"context"
	"math"
	"sort"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Index is the vector-search seam (RFC §8, D-046).
// Implementations must be safe for concurrent use after construction.
type Index interface {
	// Upsert inserts or replaces the embedding for memoryID within scope.
	// Returns ErrDimsMismatch when len(vec) != the configured dims.
	Upsert(ctx context.Context, scope identity.Scope, memoryID string, vec []float32) error

	// Search returns the top-k most similar memories within scope, optionally
	// filtered by kind and time window. Results are ordered by score descending.
	// SKIPPED (returns nil, nil) when vec is nil — allows callers to omit the
	// vector lane gracefully in degraded mode.
	Search(ctx context.Context, scope identity.Scope, vec []float32, k int, f Filter) ([]Hit, error)

	// Delete removes the embedding for memoryID. No-op when absent.
	Delete(ctx context.Context, scope identity.Scope, memoryID string) error
}

// Filter restricts the vector search to a subset of memories.
type Filter struct {
	Kinds  []string     // empty = all kinds
	Window store.Window // time window on created_at; zero values = unbounded
}

// Hit is a single result from Index.Search.
type Hit struct {
	MemoryID string
	Score    float64 // cosine similarity in [0, 1]; higher = more similar
}

// New creates a brute-force vector index wrapping the given VectorStore.
// dims is the expected embedding dimensionality; mismatches are rejected at Upsert.
// model is the embedding model name persisted with each vector.
func New(vs store.VectorStore, dims int, model string) Index {
	return &bruteIndex{vs: vs, dims: dims, model: model}
}

// bruteIndex implements Index via brute-force cosine scan (D-046 v1 plan).
// This is correct to ~100k vectors/scope; HNSW arrives as a new driver.
type bruteIndex struct {
	vs    store.VectorStore
	dims  int
	model string
}

// ErrDimsMismatch is returned when the vector length does not match the index dims.
type ErrDimsMismatch struct {
	Got  int
	Want int
}

func (e ErrDimsMismatch) Error() string {
	return "vindex: dims mismatch: got " + itoa(e.Got) + " want " + itoa(e.Want)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n >= 10 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	pos--
	buf[pos] = byte('0' + n) //nolint:gosec // n < 10 guaranteed by loop exit condition
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

func (b *bruteIndex) Upsert(ctx context.Context, scope identity.Scope, memoryID string, vec []float32) error {
	if b.dims > 0 && len(vec) != b.dims {
		return ErrDimsMismatch{Got: len(vec), Want: b.dims}
	}
	return b.vs.Upsert(ctx, scope, store.StoredVector{
		MemoryID: memoryID,
		Vec:      vec,
		Dims:     len(vec),
		Model:    b.model,
	})
}

func (b *bruteIndex) Delete(ctx context.Context, scope identity.Scope, memoryID string) error {
	return b.vs.Delete(ctx, scope, memoryID)
}

// Search scans all vectors for the scope, computes cosine similarity against
// vec, and returns the top-k hits ordered by score descending.
// Returns nil, nil when vec is nil (degraded-mode signal from caller).
func (b *bruteIndex) Search(ctx context.Context, scope identity.Scope, vec []float32, k int, f Filter) ([]Hit, error) {
	if vec == nil {
		return nil, nil
	}
	if k <= 0 {
		return nil, nil
	}

	stored, err := b.vs.Scan(ctx, scope, f.Kinds, f.Window)
	if err != nil {
		return nil, err
	}

	hits := make([]Hit, 0, len(stored))
	for _, sv := range stored {
		if len(sv.Vec) != len(vec) {
			continue // dims mismatch for this vector — skip silently
		}
		score := CosineSimilarity(vec, sv.Vec)
		hits = append(hits, Hit{MemoryID: sv.MemoryID, Score: score})
	}

	// Sort by score descending, then by ID for stability.
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].MemoryID < hits[j].MemoryID
	})

	if k < len(hits) {
		hits = hits[:k]
	}
	return hits, nil
}

// CosineSimilarity computes the cosine similarity of two float32 vectors.
// Returns 0 when either vector has zero norm or lengths differ.
// Exported so the live-test and eval harness can use the same kernel.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai := float64(a[i])
		bi := float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
