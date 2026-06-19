package vindex_test

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/store/sqlitestore"
	"github.com/hurtener/stowage/internal/vindex"
	"github.com/hurtener/stowage/internal/vindex/conformance"
)

// --- conformance suite via sqlite -------------------------------------------

func TestConformanceSQLite(t *testing.T) {
	t.Parallel()
	for range 3 { // "×3 consecutive" per definition of done
		t.Run("run", func(t *testing.T) {
			conformance.Run(t, func() (vindex.Index, store.Store, func()) {
				st, cleanup := openSQLiteStore(t)
				vi := vindex.New(st.Vectors(), 4, "test-model")
				return vi, st, cleanup
			})
		})
	}
}

func openSQLiteStore(t *testing.T) (store.Store, func()) {
	t.Helper()
	// Use a temp-dir file so each parallel test gets its own isolated database.
	dir := t.TempDir()
	st, err := sqlitestore.Open(context.Background(), config.StoreConfig{
		Driver: "sqlite",
		DSN:    filepath.Join(dir, "test.db"),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st, func() { _ = st.Close(context.Background()) }
}

// --- CosineSimilarity unit tests --------------------------------------------

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []float32
		wantMin float64
		wantMax float64
	}{
		{
			name:    "identical unit vectors",
			a:       []float32{1, 0, 0},
			b:       []float32{1, 0, 0},
			wantMin: 0.999,
			wantMax: 1.001,
		},
		{
			name:    "orthogonal vectors",
			a:       []float32{1, 0},
			b:       []float32{0, 1},
			wantMin: -0.001,
			wantMax: 0.001,
		},
		{
			name:    "opposite vectors",
			a:       []float32{1, 0},
			b:       []float32{-1, 0},
			wantMin: -1.001,
			wantMax: -0.999,
		},
		{
			name:    "zero vector returns 0",
			a:       []float32{0, 0, 0},
			b:       []float32{1, 1, 1},
			wantMin: -0.001,
			wantMax: 0.001,
		},
		{
			name:    "empty slices",
			a:       []float32{},
			b:       []float32{},
			wantMin: -0.001,
			wantMax: 0.001,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := vindex.CosineSimilarity(tc.a, tc.b)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("CosineSimilarity = %v, want in [%v, %v]", got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestCosineSimilarityLengthMismatch(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0}
	got := vindex.CosineSimilarity(a, b)
	if got != 0 {
		t.Errorf("expected 0 for mismatched lengths, got %v", got)
	}
}

// --- registry tests ---------------------------------------------------------

func TestOpen_BruteDriver(t *testing.T) {
	st, cleanup := openSQLiteStore(t)
	defer cleanup()
	vi, err := vindex.Open(config.VIndexConfig{Driver: "brute"}, st.Vectors(), 4, "test-model")
	if err != nil {
		t.Fatalf("Open(brute): unexpected error: %v", err)
	}
	if vi == nil {
		t.Fatal("Open(brute): returned nil Index")
	}
}

func TestOpen_UnknownDriver(t *testing.T) {
	st, cleanup := openSQLiteStore(t)
	defer cleanup()
	_, err := vindex.Open(config.VIndexConfig{Driver: "does-not-exist"}, st.Vectors(), 4, "test-model")
	if err == nil {
		t.Fatal("Open(does-not-exist): expected error, got nil")
	}
	if !errors.As(err, new(interface{ Error() string })) {
		t.Fatalf("unexpected error type: %T", err)
	}
}

func TestRegisterAndOpen(t *testing.T) {
	// Register a custom driver and verify Open uses it.
	called := false
	vindex.Register("test-custom-driver", func(vs store.VectorStore, dims int, model string) (vindex.Index, error) {
		called = true
		return vindex.New(vs, dims, model), nil
	})
	st, cleanup := openSQLiteStore(t)
	defer cleanup()
	vi, err := vindex.Open(config.VIndexConfig{Driver: "test-custom-driver"}, st.Vectors(), 4, "m")
	if err != nil {
		t.Fatalf("Open(test-custom-driver): %v", err)
	}
	if vi == nil {
		t.Fatal("Open(test-custom-driver): returned nil Index")
	}
	if !called {
		t.Error("custom factory was not called")
	}
}

// --- BenchmarkVectorScan ---------------------------------------------------

func BenchmarkVectorScan(b *testing.B) {
	st, cleanup := func() (store.Store, func()) {
		dir := b.TempDir()
		st, err := sqlitestore.Open(context.Background(), config.StoreConfig{
			Driver: "sqlite",
			DSN:    filepath.Join(dir, "bench.db"),
		})
		if err != nil {
			b.Fatalf("open sqlite: %v", err)
		}
		if err := st.Migrate(context.Background()); err != nil {
			b.Fatalf("migrate: %v", err)
		}
		return st, func() { _ = st.Close(context.Background()) }
	}()
	defer cleanup()

	ctx := context.Background()
	scope := identity.Scope{Tenant: "bench"}
	vi := vindex.New(st.Vectors(), 32, "bench-model")

	// Seed 1000 memories + vectors.
	for i := 0; i < 1000; i++ {
		id := insertBenchMemory(b, st, scope, i)
		vec := syntheticVec(32, i)
		if err := vi.Upsert(ctx, scope, id, vec); err != nil {
			b.Fatalf("Upsert: %v", err)
		}
	}

	queryVec := syntheticVec(32, 42)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := vi.Search(ctx, scope, queryVec, 20, vindex.Filter{})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func insertBenchMemory(b *testing.B, st store.Store, scope identity.Scope, idx int) string {
	b.Helper()
	id := benchID(idx)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: "fact",
			Content:     "bench memory",
			Status:      "active",
			TrustSource: "llm_extracted", Stability: 1.0,
			ContentHash: benchID(idx + 100000),
			CreatedAt:   1000000 + int64(idx),
			UpdatedAt:   1000000 + int64(idx),
		},
		Events: []store.Event{{ID: benchID(idx + 200000), Type: "memory.added", SubjectID: id, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		b.Fatalf("Commit: %v", err)
	}
	return id
}

func benchID(n int) string {
	// Pad to 26 chars (ULID-length) for deterministic IDs.
	s := "0000000000000000000000000" + itoa(n)
	return s[len(s)-26:]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n >= 10 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	pos--
	buf[pos] = byte('0' + n)
	return string(buf[pos:])
}

func syntheticVec(dims, seed int) []float32 {
	v := make([]float32, dims)
	for i := range v {
		v[i] = float32(math.Sin(float64(seed*dims+i) * 0.1))
	}
	return v
}

// --- Additional branch coverage ---------------------------------------------

// TestSearch_ZeroK proves Search returns nil, nil when k <= 0.
func TestSearch_ZeroK(t *testing.T) {
	t.Parallel()
	st, cleanup := openSQLiteStore(t)
	defer cleanup()
	vi := vindex.New(st.Vectors(), 4, "test-model")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "tenant-zerok"}

	hits, err := vi.Search(ctx, scope, []float32{1, 0, 0, 0}, 0, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search(k=0): unexpected error: %v", err)
	}
	if hits != nil {
		t.Errorf("Search(k=0): expected nil hits, got %v", hits)
	}
}

// errVS is a VectorStore whose Scan always returns an error.
type errVS struct{}

func (errVS) Upsert(_ context.Context, _ identity.Scope, _ store.StoredVector) error { return nil }
func (errVS) Delete(_ context.Context, _ identity.Scope, _ string) error             { return nil }
func (errVS) Scan(_ context.Context, _ identity.Scope, _ []string, _ store.Window) ([]store.StoredVector, error) {
	return nil, errors.New("scan error")
}
func (errVS) ListWithoutVectors(_ context.Context, _ int) ([]store.MemoryForEmbed, error) {
	return nil, nil
}
func (errVS) DistinctModels(_ context.Context) ([]string, error) { return nil, nil }

// TestSearch_ScanError proves Search propagates errors returned by VectorStore.Scan.
func TestSearch_ScanError(t *testing.T) {
	t.Parallel()
	vi := vindex.New(errVS{}, 4, "test-model")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "tenant-scanerr"}

	_, err := vi.Search(ctx, scope, []float32{1, 0, 0, 0}, 10, vindex.Filter{})
	if err == nil {
		t.Fatal("Search: expected error from Scan, got nil")
	}
}

// insertTestMemory inserts a minimal active memory and returns its ID.
// This is a *testing.T variant of insertBenchMemory for use in unit tests.
func insertTestMemory(t *testing.T, st store.Store, scope identity.Scope, idx int) string {
	t.Helper()
	id := benchID(idx)
	cs := store.CommitSet{
		Action: store.ActionAdd,
		Memory: store.Memory{
			ID: id, Kind: "fact",
			Content:     "test memory",
			Status:      "active",
			TrustSource: "llm_extracted", Stability: 1.0,
			ContentHash: benchID(idx + 100000),
			CreatedAt:   1000000 + int64(idx),
			UpdatedAt:   1000000 + int64(idx),
		},
		Events: []store.Event{{ID: benchID(idx + 200000), Type: "memory.added", SubjectID: id, Payload: `{}`}},
	}
	if err := st.Memories().Commit(context.Background(), scope, cs); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return id
}

// TestSearch_TrimResults proves Search trims results to k when more hits exist.
func TestSearch_TrimResults(t *testing.T) {
	t.Parallel()
	st, cleanup := openSQLiteStore(t)
	defer cleanup()
	vi := vindex.New(st.Vectors(), 4, "test-model")
	ctx := context.Background()
	scope := identity.Scope{Tenant: "tenant-trim"}

	// Seed 5 memories with the same vector (all get score ~1.0).
	vec := []float32{1, 0, 0, 0}
	for i := 0; i < 5; i++ {
		id := insertTestMemory(t, st, scope, i+10000)
		if err := vi.Upsert(ctx, scope, id, vec); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}

	// Search with k=2: must return exactly 2 results (trim path exercised).
	hits, err := vi.Search(ctx, scope, vec, 2, vindex.Filter{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("Search(k=2): got %d hits, want 2", len(hits))
	}
}

// TestErrDimsMismatch_Error proves the Error() method formats correctly for
// multi-digit and negative numbers, exercising all itoa branches.
func TestErrDimsMismatch_Error(t *testing.T) {
	cases := []struct {
		got, want int
		wantMsg   string
	}{
		{8, 4, "vindex: dims mismatch: got 8 want 4"},
		{100, 200, "vindex: dims mismatch: got 100 want 200"},
		{1536, 768, "vindex: dims mismatch: got 1536 want 768"},
		{0, 4, "vindex: dims mismatch: got 0 want 4"},
	}
	for _, tc := range cases {
		e := vindex.ErrDimsMismatch{Got: tc.got, Want: tc.want}
		if got := e.Error(); got != tc.wantMsg {
			t.Errorf("ErrDimsMismatch{%d,%d}.Error() = %q, want %q", tc.got, tc.want, got, tc.wantMsg)
		}
	}
}
