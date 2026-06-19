//go:build slo

// Package slo contains the SLO measurement harness for Stowage retrieval (Phase 12, D-031).
//
// # Usage
//
//	STOWAGE_TEST_PG_DSN=postgres://... make slo
//
// The rig:
//   - Seeds seedCount (default 10 000) memories into a postgres-backed Stowage instance.
//   - Fires numSessions (default 1 000) concurrent goroutines each making numQueries
//     (default 5) retrieval calls through a lightweight in-process HTTP stack.
//   - Collects round-trip latencies.
//   - Reports p50/p95/p99 and the cache hit rate.
//
// The test is skipped when STOWAGE_TEST_PG_DSN is unset.
// Numbers from the reference machine are recorded in eval/SLO.md.
package slo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/mock"
	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/retrieval"
	"github.com/hurtener/stowage/internal/store"
	_ "github.com/hurtener/stowage/internal/store/pgstore"
	"github.com/hurtener/stowage/internal/vindex"
)

// Flags let the gate reviewer tune the rig without recompilation.
var (
	flDSN      = flag.String("slo.dsn", os.Getenv("STOWAGE_TEST_PG_DSN"), "postgres DSN for the SLO rig")
	flSeeds    = flag.Int("slo.seeds", 10_000, "number of memories to seed")
	flSessions = flag.Int("slo.sessions", 1_000, "number of concurrent sessions")
	flQueries  = flag.Int("slo.queries", 5, "retrieve calls per session")
	flLimit    = flag.Int("slo.limit", 10, "items per retrieve call")
	flMaxP99   = flag.Int("slo.maxp99", sloTargetP99MS, "p99 budget in ms; the rig FAILS the build when exceeded (the gate). Defaults to the binding D-031 target; a slower-than-reference environment may raise it deliberately.")
)

// sloTargetP99MS is the binding p99 target in milliseconds (D-031, reference hardware).
// The rig FAILS (t.Fatalf) when the measured p99 exceeds the budget (-slo.maxp99,
// default = this target) — the SLO gate bites on a regression (D-095). The binding
// reference-hardware numbers are operator-recorded in eval/SLO.md.
const sloTargetP99MS = 150

// latencyBag collects round-trip durations.
type latencyBag struct {
	mu      sync.Mutex
	samples []int64
}

func (lb *latencyBag) Add(d time.Duration) {
	lb.mu.Lock()
	lb.samples = append(lb.samples, d.Milliseconds())
	lb.mu.Unlock()
}

func (lb *latencyBag) Percentile(p float64) int64 {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if len(lb.samples) == 0 {
		return 0
	}
	cp := make([]int64, len(lb.samples))
	copy(cp, lb.samples)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := int(float64(len(cp)-1) * p)
	return cp[idx]
}

func (lb *latencyBag) Len() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return len(lb.samples)
}

// TestSLO is the SLO harness entry point.
func TestSLO(t *testing.T) {
	if *flDSN == "" {
		t.Skip("STOWAGE_TEST_PG_DSN not set — skipping SLO rig (set -slo.dsn or STOWAGE_TEST_PG_DSN)")
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Open postgres store.
	st, err := store.Open(ctx, config.StoreConfig{Driver: "postgres", DSN: *flDSN})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close(ctx) //nolint:errcheck
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Open brute-force vindex (exact recall for rig reproducibility).
	// Brute driver requires no pgvector extension; rig works on any postgres DB.
	vi, err := vindex.Open(config.VIndexConfig{Driver: "brute"}, st.Vectors(), 16, "mock-embed")
	if err != nil {
		t.Fatalf("open vindex: %v", err)
	}

	// Open mock gateway (deterministic embed, no network calls).
	gw, err := gateway.Open(ctx, config.GatewayConfig{
		Driver:    "mock",
		EmbedDims: 16,
	}, log, prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open gateway: %v", err)
	}
	defer gw.Close(ctx) //nolint:errcheck

	scope := identity.Scope{Tenant: "slo-rig"}

	// Seed memories.
	t.Logf("seeding %d memories…", *flSeeds)
	seedStart := time.Now()
	rigSeedMemories(t, ctx, st, scope, *flSeeds)
	t.Logf("seeded %d memories in %v", *flSeeds, time.Since(seedStart))

	// Build retriever with injections.
	retr := retrieval.NewWithInjections(st.Memories(), st.Records(), vi, gw, st.Injections(), log)
	defer retr.Close()

	// Lightweight in-process HTTP endpoint.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /retrieve", func(w http.ResponseWriter, r *http.Request) {
		var req retrieval.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp, err := retr.Retrieve(r.Context(), scope, req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	})
	svr := httptest.NewServer(mux)
	defer svr.Close()

	// Fire concurrent sessions.
	var lb latencyBag
	var cacheHits atomic.Int64
	var totalReqs atomic.Int64

	var wg sync.WaitGroup
	for s := range *flSessions {
		wg.Add(1)
		go func(sn int) {
			defer wg.Done()
			client := &http.Client{Timeout: 30 * time.Second}
			for q := range *flQueries {
				query := rigSampleQuery(sn, q)
				body, _ := json.Marshal(retrieval.Request{
					Query:   query,
					Limit:   *flLimit,
					Profile: "balanced",
				})
				start := time.Now()
				resp, err := client.Post(svr.URL+"/retrieve", "application/json", bytes.NewReader(body)) //nolint:noctx
				if err != nil {
					continue
				}
				var result retrieval.Response
				json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
				resp.Body.Close()                          //nolint:errcheck
				lb.Add(time.Since(start))
				totalReqs.Add(1)
				if result.CacheHit {
					cacheHits.Add(1)
				}
			}
		}(s)
	}
	wg.Wait()

	total := totalReqs.Load()
	hits := cacheHits.Load()
	hitPct := 0.0
	if total > 0 {
		hitPct = float64(hits) / float64(total) * 100
	}
	p50 := lb.Percentile(0.50)
	p95 := lb.Percentile(0.95)
	p99 := lb.Percentile(0.99)

	t.Logf("=== SLO RESULTS ===")
	t.Logf("requests : %d (%d sessions × %d queries)", total, *flSessions, *flQueries)
	t.Logf("p50      : %d ms", p50)
	t.Logf("p95      : %d ms", p95)
	t.Logf("p99      : %d ms  (budget ≤ %d ms; binding target %d ms)", p99, *flMaxP99, sloTargetP99MS)
	t.Logf("cache    : %d/%d hits (%.1f%%)", hits, total, hitPct)

	// Surface a deliberately-raised budget so the deviation is visible in the run log
	// that gets pasted into eval/SLO.md (the binding number is always taken at 150 ms).
	if *flMaxP99 > sloTargetP99MS {
		t.Logf("NOTE: budget raised to %d ms above the binding D-031 target %d ms — record why in eval/SLO.md", *flMaxP99, sloTargetP99MS)
	}

	// A broken rig that measured (almost) nothing must NOT certify the SLO green: fail
	// when too few round-trips were collected (every in-process request should succeed;
	// tolerate a small transient-drop margin). Otherwise p99=0 would pass silently.
	expected := *flSessions * *flQueries
	minSamples := expected * 9 / 10
	if lb.Len() < minSamples {
		t.Fatalf("SLO rig collected only %d/%d samples — the rig is broken and cannot certify the SLO", lb.Len(), expected)
	}

	// The gate bites (D-095): a p99 over budget fails the build. The default budget is
	// the binding D-031 target; record the actual numbers in eval/SLO.md after the run.
	if int(p99) > *flMaxP99 {
		t.Fatalf("SLO REGRESSION: p99 %d ms exceeds budget %d ms (binding target %d ms, D-031) — the gate fails the build", p99, *flMaxP99, sloTargetP99MS)
	}
	t.Logf("p99 budget MET ✓  %d ms ≤ %d ms", p99, *flMaxP99)
}

// rigSeedMemories inserts n active memories into the store.
func rigSeedMemories(t *testing.T, ctx context.Context, st store.Store, scope identity.Scope, n int) {
	t.Helper()
	corpus := []string{
		"Python is a dynamically typed, high-level programming language.",
		"Go is a statically typed compiled language developed at Google.",
		"Rust is a systems programming language focused on memory safety.",
		"TypeScript adds static typing to JavaScript.",
		"The user prefers concise, direct answers without preamble.",
		"Use tabs for indentation in Go code per gofmt.",
		"Always run go vet and golangci-lint before committing.",
		"The project uses PostgreSQL as the primary database.",
		"Docker Compose is used for local development environments.",
		"The API uses snake_case for JSON field names.",
	}
	now := time.Now().UnixMilli()
	for i := range n {
		content := fmt.Sprintf("%s (seed-%d)", corpus[i%len(corpus)], i)
		id := fmt.Sprintf("slo%020d", i)
		cs := store.CommitSet{
			Action: store.ActionAdd,
			Memory: store.Memory{
				ID:          id,
				Kind:        "fact",
				Content:     content,
				Context:     "slo-rig",
				Status:      "active",
				Confidence:  0.9,
				TrustSource: "llm_extracted",
				Stability:   1.0,
				ContentHash: fmt.Sprintf("slohash%d", i),
				CreatedAt:   now,
				UpdatedAt:   now,
			},
			Events: []store.Event{{
				ID:        fmt.Sprintf("sloevt%d", i),
				Type:      "memory.added",
				SubjectID: id,
				Payload:   `{}`,
			}},
		}
		// Ignore duplicate content errors — rig may be re-run against the same DB.
		if err := st.Memories().Commit(ctx, scope, cs); err != nil {
			continue
		}
	}
}

// rigSampleQuery returns a deterministic query string for session sn, query q.
func rigSampleQuery(sn, q int) string {
	queries := []string{
		"Python programming language features",
		"Go language concurrency patterns",
		"database configuration best practices",
		"coding style and formatting rules",
		"memory management in systems languages",
		"API design and naming conventions",
		"testing and linting workflow",
		"Docker development environment setup",
		"TypeScript type safety patterns",
		"performance optimisation techniques",
	}
	return queries[(sn+q)%len(queries)]
}
