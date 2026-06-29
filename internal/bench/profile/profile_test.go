//go:build profile

// Package profile contains the P1 load+profile rig for Stowage (D-126).
//
// # Usage
//
//	make profile
//	# or:
//	CGO_ENABLED=1 go test -tags=profile -v -run TestProfile ./internal/bench/profile/ \
//	  -profile.write-baseline
//
// The rig measures resource behaviour at idle (goroutine + alloc drift) and
// under concurrent load (goroutine stability after drain). pprof artifacts are
// written to -profile.out (default: a t.TempDir() so ephemeral). Both tests are
// ADVISORY by default (log + record, never fail); pass -profile.strict to make
// the goroutine-stability gates bite.
//
// Numbers from this machine are recorded in eval/PROFILE.md when
// -profile.write-baseline is set.
package profile_test

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	stowage "github.com/hurtener/stowage/sdk/stowage"
)

// ---------------------------------------------------------------------------
// Flags
// ---------------------------------------------------------------------------

var (
	flIngest        = flag.Int("profile.ingest", 16, "concurrent ingest goroutines")
	flRetrieve      = flag.Int("profile.retrieve", 16, "concurrent retrieve goroutines")
	flDuration      = flag.Duration("profile.duration", 5*time.Second, "load duration")
	flIdle          = flag.Duration("profile.idle", 3*time.Second, "idle observation window")
	flSettle        = flag.Duration("profile.settle", 2*time.Second, "post-drain settle before final sample")
	flEps           = flag.Int("profile.eps", 50, "allowed goroutine growth before stability gate fires")
	flOut           = flag.String("profile.out", "", "dir for pprof artifacts; empty => t.TempDir() (ephemeral)")
	flStrict        = flag.Bool("profile.strict", false, "when true goroutine-stability gates FAIL the build; default false = advisory")
	flWriteBaseline = flag.Bool("profile.write-baseline", false, "when true (re)write eval/PROFILE.md with measured numbers")
)

// ---------------------------------------------------------------------------
// Package-level results (collected by tests, written by TestZZZWriteBaseline)
// ---------------------------------------------------------------------------

type idleResults struct {
	collected  bool
	g0         int
	g1         int
	gDelta     int
	allocDelta int64 // bytes allocated during the idle window
}

type loadResults struct {
	collected   bool
	s0          int
	s1          int
	s2          int
	gateDelta   int
	ingestOps   int64
	retrieveOps int64
	errorCount  int64
	artifactDir string
	stabilityOK bool // s2 <= s0+eps
}

var (
	resultsMu sync.Mutex
	idleRes   idleResults
	loadRes   loadResults
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newRigClient returns an embedded Stowage client backed by a temp SQLite DB
// and a mock gateway. The closer is NOT registered with t.Cleanup — callers
// must invoke it explicitly so they can measure the drain latency.
func newRigClient(t *testing.T, tmpDir string) (stowage.Client, func(context.Context) error) {
	t.Helper()
	dbPath := filepath.Join(tmpDir, "rig.db")

	cfg := config.Config{}
	cfg.Store.Driver = "sqlite"
	cfg.Store.DSN = dbPath
	cfg.Gateway.Driver = "mock"

	ctx := context.Background()
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID("rig"))
	if err != nil {
		t.Fatalf("newRigClient: NewEmbedded: %v", err)
	}
	return client, closer
}

// sampleGoroutines runs two GCs with a short settle and returns the goroutine count.
func sampleGoroutines() int {
	runtime.GC()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	return runtime.NumGoroutine()
}

// sampleMem runs a GC and returns a MemStats snapshot.
func sampleMem() runtime.MemStats {
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms
}

// gate either calls t.Fatalf (strict) or t.Logf (advisory).
func gate(t *testing.T, format string, args ...any) {
	t.Helper()
	if *flStrict {
		t.Fatalf(format, args...)
	} else {
		t.Logf("ADVISORY: "+format, args...)
	}
}

// ---------------------------------------------------------------------------
// TestProfileIdle — zero-traffic resource baseline
// ---------------------------------------------------------------------------

// TestProfileIdle measures goroutine and allocation drift at idle: sweeps and
// tickers run, but no ingest or retrieve calls are made. The goroutine delta
// must not climb (advisory unless -profile.strict).
func TestProfileIdle(t *testing.T) {
	tmp := t.TempDir()
	client, closer := newRigClient(t, tmp)

	// Let boot settle before sampling.
	time.Sleep(500 * time.Millisecond)

	g0 := sampleGoroutines()
	m0 := sampleMem()

	time.Sleep(*flIdle) // idle window — NO traffic

	g1 := sampleGoroutines()
	m1 := sampleMem()

	// Drain explicitly (we don't use t.Cleanup intentionally).
	ctx := context.Background()
	if err := closer(ctx); err != nil {
		t.Logf("closer: %v", err)
	}

	gDelta := g1 - g0
	allocDelta := int64(m1.TotalAlloc) - int64(m0.TotalAlloc)

	t.Logf("=== IDLE RESULTS ===")
	t.Logf("g0 (post-boot) : %d goroutines", g0)
	t.Logf("g1 (post-idle) : %d goroutines", g1)
	t.Logf("gDelta         : %d  (eps=%d)", gDelta, *flEps)
	t.Logf("alloc during idle : %d bytes (sweeps allocate; no absolute gate)", allocDelta)

	// Record for baseline writer.
	resultsMu.Lock()
	idleRes = idleResults{
		collected:  true,
		g0:         g0,
		g1:         g1,
		gDelta:     gDelta,
		allocDelta: allocDelta,
	}
	resultsMu.Unlock()

	// Goroutines must not climb at idle.
	if gDelta > *flEps {
		gate(t, "goroutine-idle gate: post-idle G(%d) exceeds post-boot G(%d)+eps(%d) — possible ticker/sweep leak", g1, g0, *flEps)
	}

	// Silence the unused variable warning — client is the embedded system under observation.
	_ = client
}

// ---------------------------------------------------------------------------
// TestProfileLoad — concurrent load + drain + goroutine-stability gate
// ---------------------------------------------------------------------------

// TestProfileLoad drives concurrent ingest + retrieve for flDuration, captures
// pprof artifacts, drains the pipeline, settles, and asserts s2 <= s0+eps.
func TestProfileLoad(t *testing.T) {
	outDir := *flOut
	if outDir == "" {
		outDir = t.TempDir()
	}

	tmp := t.TempDir()
	client, closer := newRigClient(t, tmp)

	// Post-boot baseline — no traffic yet.
	time.Sleep(500 * time.Millisecond)
	s0 := sampleGoroutines()

	// Enable block + mutex profiling for the load window.
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)
	defer runtime.SetBlockProfileRate(0)
	defer runtime.SetMutexProfileFraction(0)

	cpuPath := filepath.Join(outDir, "cpu.pprof")
	cpuF, err := os.Create(cpuPath)
	if err != nil {
		t.Fatalf("create cpu.pprof: %v", err)
	}
	if err := pprof.StartCPUProfile(cpuF); err != nil {
		cpuF.Close()
		t.Fatalf("start cpu profile: %v", err)
	}

	// Drive concurrent load.
	loadCtx, cancel := context.WithTimeout(context.Background(), *flDuration)
	defer cancel()

	var wg sync.WaitGroup
	var ingestOps, retrieveOps, errCount atomic.Int64

	// Ingest goroutines.
	for i := range *flIngest {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-loadCtx.Done():
					return
				default:
				}
				content := fmt.Sprintf("profile-load record g%d i%d", gIdx, counter)
				sessionID := fmt.Sprintf("session-%d", gIdx)
				_, err := client.Ingest(loadCtx, stowage.IngestRequest{
					Records: []stowage.RecordInput{
						{Content: content, SessionID: sessionID, Role: "user"},
					},
				})
				if err != nil {
					errCount.Add(1)
				} else {
					ingestOps.Add(1)
				}
				counter++
			}
		}(i)
	}

	// Retrieve goroutines.
	queries := []string{
		"profile load test query",
		"goroutine stability measurement",
		"memory resource behaviour",
		"concurrent load baseline",
		"stowage embedded client",
	}
	for i := range *flRetrieve {
		wg.Add(1)
		go func(gIdx int) {
			defer wg.Done()
			counter := 0
			for {
				select {
				case <-loadCtx.Done():
					return
				default:
				}
				query := queries[(gIdx+counter)%len(queries)]
				_, err := client.Retrieve(loadCtx, stowage.RetrieveRequest{
					Query:   query,
					Limit:   10,
					Profile: "balanced",
				})
				if err != nil {
					errCount.Add(1)
				} else {
					retrieveOps.Add(1)
				}
				counter++
			}
		}(i)
	}

	wg.Wait()
	cancel()

	// Steady-state goroutines immediately after load.
	s1 := sampleGoroutines()

	pprof.StopCPUProfile()
	cpuF.Close()

	// Write heap, goroutine, block, mutex profiles.
	for _, name := range []string{"heap", "goroutine", "block", "mutex"} {
		path := filepath.Join(outDir, name+".pprof")
		f, ferr := os.Create(path)
		if ferr != nil {
			t.Logf("create %s.pprof: %v", name, ferr)
			continue
		}
		if p := pprof.Lookup(name); p != nil {
			if werr := p.WriteTo(f, 0); werr != nil {
				t.Logf("write %s.pprof: %v", name, werr)
			}
		}
		f.Close()
	}

	// DRAIN — Stack.Close + pipeline.Drain (synchronous).
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer drainCancel()
	if err := closer(drainCtx); err != nil {
		t.Logf("closer (drain): %v", err)
	}
	drainCancel()

	// Settle before final sample.
	time.Sleep(*flSettle)
	s2 := sampleGoroutines()

	gateDelta := s2 - s0
	iOps := ingestOps.Load()
	rOps := retrieveOps.Load()
	eCount := errCount.Load()
	stabilityOK := gateDelta <= *flEps

	t.Logf("=== LOAD RESULTS ===")
	t.Logf("s0 (post-boot)        : %d goroutines", s0)
	t.Logf("s1 (end-of-load)      : %d goroutines", s1)
	t.Logf("s2 (post-drain+settle): %d goroutines", s2)
	t.Logf("post-drain delta s2-s0: %d  (eps=%d, stable=%v)", gateDelta, *flEps, stabilityOK)
	t.Logf("ingest ops            : %d", iOps)
	t.Logf("retrieve ops          : %d", rOps)
	t.Logf("errors (tolerated)    : %d", eCount)
	t.Logf("pprof artifacts       : %s", outDir)

	// Sanity: if every call errored the rig is misconfigured.
	if iOps+rOps == 0 {
		t.Fatalf("rig drove no successful operations (all %d calls errored) — check driver config", eCount)
	}

	// Record for baseline writer.
	resultsMu.Lock()
	loadRes = loadResults{
		collected:   true,
		s0:          s0,
		s1:          s1,
		s2:          s2,
		gateDelta:   gateDelta,
		ingestOps:   iOps,
		retrieveOps: rOps,
		errorCount:  eCount,
		artifactDir: outDir,
		stabilityOK: stabilityOK,
	}
	resultsMu.Unlock()

	// Goroutine-stability gate.
	if !stabilityOK {
		gate(t, "goroutine-stability gate: post-drain S2(%d) exceeds S0(%d)+eps(%d) — possible leak", s2, s0, *flEps)
	}
}

// ---------------------------------------------------------------------------
// TestZZZWriteBaseline — writes eval/PROFILE.md when -profile.write-baseline
// ---------------------------------------------------------------------------

// TestProfileWriteBaseline runs last (alphabetical ordering: W > L > I) and
// writes eval/PROFILE.md with the measured numbers from both prior tests.
// It is a no-op when -profile.write-baseline is false.
func TestProfileWriteBaseline(t *testing.T) {
	if !*flWriteBaseline {
		t.Skip("-profile.write-baseline not set; skipping baseline write")
	}

	resultsMu.Lock()
	idle := idleRes
	load := loadRes
	resultsMu.Unlock()

	// Helper to format "n/a" for uncollected fields.
	orNA := func(ok bool, s string) string {
		if ok {
			return s
		}
		return "n/a"
	}

	idleG0 := orNA(idle.collected, fmt.Sprintf("%d", idle.g0))
	idleG1 := orNA(idle.collected, fmt.Sprintf("%d", idle.g1))
	idleGDelta := orNA(idle.collected, fmt.Sprintf("%d", idle.gDelta))
	idleAlloc := orNA(idle.collected, fmt.Sprintf("%d bytes", idle.allocDelta))

	loadS0 := orNA(load.collected, fmt.Sprintf("%d", load.s0))
	loadS1 := orNA(load.collected, fmt.Sprintf("%d", load.s1))
	loadS2 := orNA(load.collected, fmt.Sprintf("%d", load.s2))
	loadDelta := orNA(load.collected, fmt.Sprintf("%d", load.gateDelta))
	loadIngest := orNA(load.collected, fmt.Sprintf("%d", load.ingestOps))
	loadRetrieve := orNA(load.collected, fmt.Sprintf("%d", load.retrieveOps))
	loadErrors := orNA(load.collected, fmt.Sprintf("%d", load.errorCount))
	loadArtDir := orNA(load.collected, load.artifactDir)
	stabilityStr := orNA(load.collected, func() string {
		if load.stabilityOK {
			return "PASS (s2 <= s0+eps)"
		}
		return "ADVISORY (s2 > s0+eps)"
	}())

	// Build content using a strings.Builder — avoids raw-string backtick issues
	// when embedding markdown code spans. bt is a single backtick character.
	bt := "`"
	var sb strings.Builder
	ln := func(format string, args ...any) {
		fmt.Fprintf(&sb, format+"\n", args...)
	}

	ln("# Stowage P1 Resource Baseline (D-126)")
	ln("")
	ln("This file is generated by the profile rig (%smake profile%s /", bt, bt)
	ln("%sgo test -tags=profile -run TestProfile ./internal/bench/profile/ -profile.write-baseline%s).", bt, bt)
	ln("It records the goroutine-stability and allocation-drift baselines from the")
	ln("P1 profiling phase. Re-run %smake profile%s (with -profile.write-baseline) after any", bt, bt)
	ln("change to the pipeline, lifecycle sweeps, or boot sequence to update these numbers.")
	ln("")
	ln("**The goroutine-stability delta (S2-S0) is environment-independent.**")
	ln("Absolute alloc/CPU numbers are local-machine-specific and listed here for")
	ln("orientation only.")
	ln("")
	ln("---")
	ln("")
	ln("## Rig Description")
	ln("")
	ln("The rig lives in %sinternal/bench/profile/%s (build tag %sprofile%s). Run it with:", bt, bt, bt, bt)
	ln("")
	ln("```bash")
	ln("make profile")
	ln("# or with full options:")
	ln("CGO_ENABLED=1 go test -tags=profile -v -run TestProfile ./internal/bench/profile/ \\")
	ln("  -profile.write-baseline \\")
	ln("  -profile.duration 5s \\")
	ln("  -profile.idle 3s \\")
	ln("  -profile.settle 2s \\")
	ln("  -profile.out /tmp/stowage-pprof")
	ln("```")
	ln("")
	ln("**What the rig does:**")
	ln("")
	ln("1. **TestProfileIdle** — boots a full embedded Stowage instance (SQLite + mock")
	ln("   gateway), waits for the idle window, samples goroutines and TotalAlloc before")
	ln("   and after; drains. The goroutine delta must not climb (advisory gate).")
	ln("2. **TestProfileLoad** — drives %s-profile.ingest%s + %s-profile.retrieve%s concurrent", bt, bt, bt, bt)
	ln("   goroutines for %s-profile.duration%s, then drains and settles for %s-profile.settle%s.", bt, bt, bt, bt)
	ln("   The post-drain goroutine count S2 is compared to the post-boot S0; the delta")
	ln("   must be <= %s-profile.eps%s. Captures CPU / heap / goroutine / block / mutex pprof", bt, bt)
	ln("   artifacts.")
	ln("3. **TestProfileWriteBaseline** — collects the above results and writes this file.")
	ln("")
	ln("**Flags:**")
	ln("")
	ln("| Flag                     | Default | Description                                          |")
	ln("|--------------------------|---------|------------------------------------------------------|")
	ln("| -profile.ingest          | 16      | Concurrent ingest goroutines                         |")
	ln("| -profile.retrieve        | 16      | Concurrent retrieve goroutines                       |")
	ln("| -profile.duration        | 5s      | Load duration                                        |")
	ln("| -profile.idle            | 3s      | Idle observation window                              |")
	ln("| -profile.settle          | 2s      | Post-drain settle before final sample                |")
	ln("| -profile.eps             | 50      | Allowed goroutine growth before stability gate fires |")
	ln("| -profile.out             | \"\"      | pprof artifact dir; empty = t.TempDir() (ephemeral) |")
	ln("| -profile.strict          | false   | Make gates fail the build (default: advisory log)   |")
	ln("| -profile.write-baseline  | false   | (Re)write this file with measured numbers            |")
	ln("")
	ln("---")
	ln("")
	ln("## Capture Environment")
	ln("")
	ln("- **Driver:** SQLite (pure-Go, CGo-free for the store; CGo enabled for -race)")
	ln("- **Gateway:** mock (no network calls; deterministic embeddings)")
	ln("- **Note:** Absolute alloc / CPU numbers are environment-specific (OS, Go")
	ln("  runtime, machine). The goroutine-stability delta (S2-S0) is the primary")
	ln("  signal and is environment-independent.")
	ln("")
	ln("---")
	ln("")
	ln("## Results -- TestProfileIdle")
	ln("")
	ln("| Metric                      | Value                     |")
	ln("|-----------------------------| --------------------------|")
	ln("| g0 (post-boot goroutines)   | %-25s |", idleG0)
	ln("| g1 (post-idle goroutines)   | %-25s |", idleG1)
	ln("| gDelta (g1-g0)              | %-25s |", idleGDelta)
	ln("| alloc during idle window    | %-25s |", idleAlloc)
	ln("| eps                         | %-25d |", *flEps)
	ln("")
	ln("*allocDelta is informational -- sweeps allocate a small amount at idle; no")
	ln("absolute gate is applied.*")
	ln("")
	ln("---")
	ln("")
	ln("## Results -- TestProfileLoad")
	ln("")
	ln("| Metric                            | Value                     |")
	ln("|-----------------------------------| --------------------------|")
	ln("| s0 (post-boot goroutines)         | %-25s |", loadS0)
	ln("| s1 (end-of-load goroutines)       | %-25s |", loadS1)
	ln("| s2 (post-drain+settle goroutines) | %-25s |", loadS2)
	ln("| post-drain delta (s2-s0)          | %-25s |", loadDelta)
	ln("| eps                               | %-25d |", *flEps)
	ln("| stability gate                    | %-25s |", stabilityStr)
	ln("| ingest ops (successful)           | %-25s |", loadIngest)
	ln("| retrieve ops (successful)         | %-25s |", loadRetrieve)
	ln("| errors (tolerated)                | %-25s |", loadErrors)
	ln("| pprof artifacts                   | %s |", loadArtDir)
	ln("")
	ln("---")
	ln("")
	ln("## Notes")
	ln("")
	ln("- The **goroutine-stability gate** (S2 <= S0+eps) is the primary leak signal.")
	ln("  \"PASS\" means all goroutines launched by the pipeline, sweeps, and lifecycle")
	ln("  stages were collected after drain + settle.")
	ln("- Errors during load are tolerated -- the mock gateway under concurrent load")
	ln("  may return transient errors; the rig measures resources, not correctness.")
	ln("- pprof artifacts are ephemeral by default (t.TempDir()). Set -profile.out")
	ln("  to a persistent path and inspect with %sgo tool pprof%s.", bt, bt)
	ln("- The rig does NOT run under the default %sgo test ./...%s (build tag %sprofile%s", bt, bt, bt, bt)
	ln("  guards it). It is a deliberate explicit gate (%smake profile%s), like %smake slo%s.", bt, bt, bt, bt)

	// Find eval/PROFILE.md relative to the repo root.
	repoRoot := findRepoRoot(t)
	outPath := filepath.Join(repoRoot, "eval", "PROFILE.md")

	if err := os.WriteFile(outPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write eval/PROFILE.md: %v", err)
	}
	t.Logf("baseline written to %s", outPath)
}

// findRepoRoot walks upward from the current working directory until it finds
// a go.mod file, and returns that directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root (go.mod) from %s", dir)
		}
		dir = parent
	}
}
