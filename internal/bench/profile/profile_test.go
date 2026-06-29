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
// The rig runs a driver/store matrix — {vindex: hnsw, brute} × {store: sqlite,
// postgres} — measuring goroutine stability and memory footprint at idle and
// under concurrent load. Each cell captures pprof artifacts. All gates are
// ADVISORY by default (log + record, never fail); pass -profile.strict to make
// the goroutine-stability gates bite. Postgres cells are silently skipped when
// STOWAGE_TEST_PG_DSN (or -profile.dsn) is empty.
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
	flDSN           = flag.String("profile.dsn", os.Getenv("STOWAGE_TEST_PG_DSN"), "postgres DSN for postgres/* cells; defaults to STOWAGE_TEST_PG_DSN env var")
)

// ---------------------------------------------------------------------------
// Matrix cell definition
// ---------------------------------------------------------------------------

type cell struct {
	name         string // e.g. "sqlite/hnsw"
	storeDriver  string // "sqlite" | "postgres"
	vindexDriver string // "hnsw" | "brute"
	needsDSN     bool
}

var cells = []cell{
	{"sqlite/hnsw", "sqlite", "hnsw", false},
	{"sqlite/brute", "sqlite", "brute", false},
	{"postgres/hnsw", "postgres", "hnsw", true},
	{"postgres/brute", "postgres", "brute", true},
}

// ---------------------------------------------------------------------------
// Memory footprint snapshot
// ---------------------------------------------------------------------------

type memFootprint struct {
	HeapAllocBytes  uint64
	HeapInuseBytes  uint64
	HeapSysBytes    uint64
	StackInuseBytes uint64
	SysBytes        uint64
	NumGC           uint32
}

func sampleFootprint() memFootprint {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return memFootprint{m.HeapAlloc, m.HeapInuse, m.HeapSys, m.StackInuse, m.Sys, m.NumGC}
}

// mib formats a byte count as a MiB string with one decimal place.
func mib(b uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(b)/(1024*1024))
}

// ---------------------------------------------------------------------------
// Per-cell result
// ---------------------------------------------------------------------------

type cellResult struct {
	name string
	ran  bool
	// skipReason is non-empty when the cell was skipped.
	skipReason string
	// goroutine samples at four points
	g0    int // post-boot
	gIdle int // post-idle
	s1    int // steady-state (end of load)
	s2    int // post-drain+settle
	// footprints at the same four points
	fpBoot   memFootprint
	fpIdle   memFootprint
	fpSteady memFootprint
	fpDrain  memFootprint
	// derived
	idleDelta   int  // gIdle - g0
	drainDelta  int  // s2 - g0
	stabilityOK bool // s2 <= g0+eps
	idleGateOK  bool // gIdle <= g0+eps
	// load phase counters
	ingestOps   int64
	retrieveOps int64
	errorCount  int64
	artifactDir string
}

// ---------------------------------------------------------------------------
// Package-level results (collected by TestProfileMatrix, read by TestProfileWriteBaseline)
// ---------------------------------------------------------------------------

var (
	matrixMu    sync.Mutex
	matrixCells []cellResult
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// sanitized replaces '/' with '-' so a cell name can be used as a path component or tenant ID.
func sanitized(s string) string {
	return strings.ReplaceAll(s, "/", "-")
}

// sampleGoroutines runs two GCs with a short settle and returns the goroutine count.
func sampleGoroutines() int {
	runtime.GC()
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	return runtime.NumGoroutine()
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
// TestProfileMatrix — driver/store matrix: idle + load + drain per cell
// ---------------------------------------------------------------------------

// TestProfileMatrix runs each combination of {store: sqlite, postgres} ×
// {vindex: hnsw, brute} sequentially (no t.Parallel — block/mutex profile
// rates and goroutine counts must not be cross-contaminated). For each cell it:
//  1. Boots an embedded Stowage stack with the cell's drivers.
//  2. Runs an idle phase: goroutine + footprint baseline.
//  3. Runs a concurrent load phase (ingest + retrieve) with pprof capture.
//  4. Drains the stack and measures post-drain goroutine stability.
//
// Postgres cells are skipped (not failed) when -profile.dsn / STOWAGE_TEST_PG_DSN
// is empty or the backend is unreachable. SQLite cells always run and fail the
// test on boot errors (those are real bugs). All stability gates are advisory
// unless -profile.strict is set.
func TestProfileMatrix(t *testing.T) {
	outDir := *flOut
	if outDir == "" {
		outDir = t.TempDir()
	}

	var results []cellResult
	for _, c := range cells {
		res := runCell(t, c, outDir)
		results = append(results, res)
	}

	matrixMu.Lock()
	matrixCells = results
	matrixMu.Unlock()
}

// runCell executes one matrix cell sequentially and returns its result.
func runCell(t *testing.T, c cell, outDir string) cellResult {
	t.Helper()
	t.Logf("=== CELL: %s ===", c.name)

	res := cellResult{name: c.name}

	// Skip postgres cells when no DSN is available.
	if c.needsDSN && *flDSN == "" {
		res.skipReason = "no STOWAGE_TEST_PG_DSN"
		t.Logf("SKIP cell %s: no -profile.dsn / STOWAGE_TEST_PG_DSN", c.name)
		return res
	}

	// Build config for this cell.
	cfg := config.Config{}
	cfg.Gateway.Driver = "mock"
	cfg.VIndex.Driver = c.vindexDriver
	cfg.Store.Driver = c.storeDriver
	if c.storeDriver == "sqlite" {
		cfg.Store.DSN = filepath.Join(t.TempDir(), "rig.db")
	} else {
		cfg.Store.DSN = *flDSN
	}

	ctx := context.Background()
	tenantID := "rig-" + sanitized(c.name)
	client, closer, err := stowage.NewEmbedded(ctx, cfg, stowage.WithTenantID(tenantID))
	if err != nil {
		if c.storeDriver == "sqlite" {
			// SQLite boot errors are real bugs — fail the cell (and the test).
			t.Fatalf("cell %s: NewEmbedded: %v (sqlite cells must work)", c.name, err)
		}
		// Postgres cell: backend may be unavailable — log and skip without failing.
		t.Logf("SKIP cell %s: NewEmbedded error (backend may be unavailable): %v", c.name, err)
		res.skipReason = fmt.Sprintf("NewEmbedded error: %v", err)
		return res
	}

	res.ran = true

	// Per-cell artifact directory.
	cellOut := filepath.Join(outDir, sanitized(c.name))
	if mkErr := os.MkdirAll(cellOut, 0o755); mkErr != nil {
		t.Logf("cell %s: mkdir artifact dir: %v", c.name, mkErr)
	}
	res.artifactDir = cellOut

	// Post-boot settle + baseline.
	time.Sleep(500 * time.Millisecond)
	g0 := sampleGoroutines()
	fpBoot := sampleFootprint()
	res.g0 = g0
	res.fpBoot = fpBoot

	// Idle window — no traffic.
	time.Sleep(*flIdle)
	gIdle := sampleGoroutines()
	fpIdle := sampleFootprint()
	res.gIdle = gIdle
	res.fpIdle = fpIdle
	res.idleDelta = gIdle - g0
	res.idleGateOK = (gIdle - g0) <= *flEps

	if !res.idleGateOK {
		gate(t, "cell %s: goroutine-idle gate: post-idle G(%d) exceeds post-boot G(%d)+eps(%d) — possible ticker/sweep leak",
			c.name, gIdle, g0, *flEps)
	}

	// Enable block + mutex profiling for the load window.
	// Reset explicitly AFTER profile write — do NOT defer inside the loop body;
	// that would stack defers across iterations. Reset is called explicitly below.
	runtime.SetBlockProfileRate(1)
	runtime.SetMutexProfileFraction(1)

	// CPU profile for this cell.
	cpuPath := filepath.Join(cellOut, "cpu.pprof")
	cpuF, cpuErr := os.Create(cpuPath)
	if cpuErr != nil {
		t.Logf("cell %s: create cpu.pprof: %v", c.name, cpuErr)
		cpuF = nil
	} else if startErr := pprof.StartCPUProfile(cpuF); startErr != nil {
		t.Logf("cell %s: start cpu profile: %v", c.name, startErr)
		cpuF.Close()
		cpuF = nil
	}

	// Drive concurrent ingest + retrieve for flDuration.
	loadCtx, cancel := context.WithTimeout(context.Background(), *flDuration)
	defer cancel()

	var wg sync.WaitGroup
	var ingestOps, retrieveOps, errCount atomic.Int64

	queries := []string{
		"profile load test query",
		"goroutine stability measurement",
		"memory resource behaviour",
		"concurrent load baseline",
		"stowage embedded client",
	}

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
				content := fmt.Sprintf("profile-load record %s g%d i%d", sanitized(c.name), gIdx, counter)
				sessionID := fmt.Sprintf("session-%s-%d", sanitized(c.name), gIdx)
				_, ingErr := client.Ingest(loadCtx, stowage.IngestRequest{
					Records: []stowage.RecordInput{
						{Content: content, SessionID: sessionID, Role: "user"},
					},
				})
				if ingErr != nil {
					errCount.Add(1)
				} else {
					ingestOps.Add(1)
				}
				counter++
			}
		}(i)
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
				_, retErr := client.Retrieve(loadCtx, stowage.RetrieveRequest{
					Query:   query,
					Limit:   10,
					Profile: "balanced",
				})
				if retErr != nil {
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

	// Steady-state goroutines immediately after load ends.
	s1 := sampleGoroutines()
	fpSteady := sampleFootprint()
	res.s1 = s1
	res.fpSteady = fpSteady

	// Stop CPU profile and write heap/goroutine/block/mutex profiles.
	if cpuF != nil {
		pprof.StopCPUProfile()
		cpuF.Close()
	}

	for _, pname := range []string{"heap", "goroutine", "block", "mutex"} {
		path := filepath.Join(cellOut, pname+".pprof")
		f, ferr := os.Create(path)
		if ferr != nil {
			t.Logf("cell %s: create %s.pprof: %v", c.name, pname, ferr)
			continue
		}
		if p := pprof.Lookup(pname); p != nil {
			if werr := p.WriteTo(f, 0); werr != nil {
				t.Logf("cell %s: write %s.pprof: %v", c.name, pname, werr)
			}
		}
		f.Close()
	}

	// Reset block/mutex profiling rates after writing profiles for this cell.
	// Explicit reset (not defer) so the next cell starts with a clean state.
	runtime.SetBlockProfileRate(0)
	runtime.SetMutexProfileFraction(0)

	// Drain the stack.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if dErr := closer(drainCtx); dErr != nil {
		t.Logf("cell %s: closer (drain): %v", c.name, dErr)
	}
	drainCancel()

	// Settle then final goroutine + footprint sample.
	time.Sleep(*flSettle)
	s2 := sampleGoroutines()
	fpDrain := sampleFootprint()
	res.s2 = s2
	res.fpDrain = fpDrain
	res.drainDelta = s2 - g0
	res.stabilityOK = (s2 - g0) <= *flEps

	res.ingestOps = ingestOps.Load()
	res.retrieveOps = retrieveOps.Load()
	res.errorCount = errCount.Load()

	// Per-cell log summary.
	t.Logf("--- cell %s summary ---", c.name)
	t.Logf("g0 (post-boot)         : %d goroutines", g0)
	t.Logf("gIdle (post-idle)      : %d  delta=%d  idleGateOK=%v  (eps=%d)", gIdle, res.idleDelta, res.idleGateOK, *flEps)
	t.Logf("s1 (end-of-load)       : %d goroutines", s1)
	t.Logf("s2 (post-drain+settle) : %d  delta=%d  stabilityOK=%v  (eps=%d)", s2, res.drainDelta, res.stabilityOK, *flEps)
	t.Logf("ingest ops             : %d", res.ingestOps)
	t.Logf("retrieve ops           : %d", res.retrieveOps)
	t.Logf("errors (tolerated)     : %d", res.errorCount)
	t.Logf("heap alloc : boot=%s  idle=%s  steady=%s  drain=%s",
		mib(fpBoot.HeapAllocBytes), mib(fpIdle.HeapAllocBytes),
		mib(fpSteady.HeapAllocBytes), mib(fpDrain.HeapAllocBytes))
	t.Logf("pprof artifacts        : %s", cellOut)

	// Sanity: a ran cell that drove zero successful ops is misconfigured.
	if res.ingestOps+res.retrieveOps == 0 {
		t.Errorf("cell %s drove zero successful operations (all %d calls errored) — check driver config",
			c.name, res.errorCount)
	}

	// Stability gate (advisory unless -profile.strict).
	if !res.stabilityOK {
		gate(t, "cell %s: goroutine-stability gate: post-drain S2(%d) exceeds S0(%d)+eps(%d) — possible leak",
			c.name, s2, g0, *flEps)
	}

	return res
}

// ---------------------------------------------------------------------------
// TestProfileWriteBaseline — writes eval/PROFILE.md when -profile.write-baseline
// ---------------------------------------------------------------------------

// TestProfileWriteBaseline runs last (W > M alphabetically) and writes
// eval/PROFILE.md with the measured numbers from TestProfileMatrix. It is a
// no-op when -profile.write-baseline is false.
func TestProfileWriteBaseline(t *testing.T) {
	if !*flWriteBaseline {
		t.Skip("-profile.write-baseline not set; skipping baseline write")
	}

	matrixMu.Lock()
	results := matrixCells
	matrixMu.Unlock()

	bt := "`"
	var sb strings.Builder
	ln := func(format string, args ...any) {
		fmt.Fprintf(&sb, format+"\n", args...)
	}

	ln("# Stowage P1 Resource Baseline (D-126)")
	ln("")
	ln("This file is generated by the profile rig (%smake profile%s /", bt, bt)
	ln("%sgo test -tags=profile -run TestProfile ./internal/bench/profile/ -profile.write-baseline%s).", bt, bt)
	ln("It records the goroutine-stability and memory-footprint baselines from the")
	ln("P1 profiling phase across the full driver/store matrix. Re-run %smake profile%s", bt, bt)
	ln("(with -profile.write-baseline) after any change to the pipeline, lifecycle")
	ln("sweeps, boot sequence, or store/vindex drivers to update these numbers.")
	ln("")
	ln("**The goroutine-stability deltas (gIdle-g0, s2-g0) are environment-independent.**")
	ln("Absolute footprint/MiB numbers are local-machine-specific and listed here for")
	ln("orientation only — they will vary across machines and Go runtime versions.")
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
	ln("STOWAGE_TEST_PG_DSN='postgres://...' \\")
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
	ln("1. **TestProfileMatrix** — iterates the {vindex: hnsw, brute} × {store: sqlite,")
	ln("   postgres} matrix sequentially. For each cell:")
	ln("   - Boots a full embedded Stowage instance with the cell's drivers.")
	ln("   - Idles for %s-profile.idle%s: samples goroutines + footprint before and after.", bt, bt)
	ln("   - Drives %s-profile.ingest%s + %s-profile.retrieve%s concurrent goroutines for", bt, bt, bt, bt)
	ln("     %s-profile.duration%s with block+mutex profiling enabled.", bt, bt)
	ln("   - Captures CPU / heap / goroutine / block / mutex pprof artifacts.")
	ln("   - Drains the stack; settles for %s-profile.settle%s; samples post-drain.", bt, bt)
	ln("   - Asserts post-drain goroutine count S2 <= post-boot g0+eps (advisory gate).")
	ln("   Postgres cells are skipped (not failed) when STOWAGE_TEST_PG_DSN is unset.")
	ln("2. **TestProfileWriteBaseline** — collects results and writes this file.")
	ln("")
	ln("**Flags:**")
	ln("")
	ln("| Flag                     | Default | Description                                              |")
	ln("|--------------------------|---------|----------------------------------------------------------|")
	ln("| -profile.ingest          | 16      | Concurrent ingest goroutines                             |")
	ln("| -profile.retrieve        | 16      | Concurrent retrieve goroutines                           |")
	ln("| -profile.duration        | 5s      | Load duration                                            |")
	ln("| -profile.idle            | 3s      | Idle observation window                                  |")
	ln("| -profile.settle          | 2s      | Post-drain settle before final sample                    |")
	ln("| -profile.eps             | 50      | Allowed goroutine growth before stability gate fires     |")
	ln("| -profile.out             | \"\"      | pprof artifact dir; empty = t.TempDir() (ephemeral)     |")
	ln("| -profile.dsn             | env     | Postgres DSN; defaults to STOWAGE_TEST_PG_DSN            |")
	ln("| -profile.strict          | false   | Make gates fail the build (default: advisory log)        |")
	ln("| -profile.write-baseline  | false   | (Re)write this file with measured numbers                |")
	ln("")
	ln("---")
	ln("")
	ln("## Matrix Results")
	ln("")
	ln("Cells run sequentially. The goroutine-stability delta is the primary leak signal.")
	ln("Memory footprint MiB values are environment-specific; the delta pattern matters.")
	ln("")

	for _, res := range results {
		ln("---")
		ln("")
		ln("### Cell: %s", res.name)
		ln("")

		if !res.ran {
			reason := res.skipReason
			if reason == "" {
				reason = "unknown"
			}
			ln("skipped (%s)", reason)
			ln("")
			continue
		}

		// Goroutine stability table.
		idleGateStr := "PASS"
		if !res.idleGateOK {
			idleGateStr = "ADVISORY"
		}
		stabilityStr := "PASS"
		if !res.stabilityOK {
			stabilityStr = "ADVISORY"
		}

		ln("**Goroutine Stability**")
		ln("")
		ln("| Metric                          | Value                     |")
		ln("|---------------------------------|---------------------------|")
		ln("| g0 (post-boot)                  | %-25d |", res.g0)
		ln("| gIdle (post-idle)               | %-25d |", res.gIdle)
		ln("| s1 (end-of-load)                | %-25d |", res.s1)
		ln("| s2 (post-drain+settle)          | %-25d |", res.s2)
		ln("| idle delta (gIdle-g0)           | %-25d |", res.idleDelta)
		ln("| post-drain delta (s2-g0)        | %-25d |", res.drainDelta)
		ln("| eps                             | %-25d |", *flEps)
		ln("| idle gate                       | %-25s |", idleGateStr)
		ln("| stability gate                  | %-25s |", stabilityStr)
		ln("| ingest ops (successful)         | %-25d |", res.ingestOps)
		ln("| retrieve ops (successful)       | %-25d |", res.retrieveOps)
		ln("| errors (tolerated)              | %-25d |", res.errorCount)
		ln("")

		// Memory footprint table.
		ln("**Memory Footprint** *(environment-specific — MiB values vary by machine)*")
		ln("")
		ln("| Metric       | post-boot   | post-idle   | steady      | post-drain  |")
		ln("|--------------|-------------|-------------|-------------|-------------|")
		ln("| HeapAlloc    | %-11s | %-11s | %-11s | %-11s |",
			mib(res.fpBoot.HeapAllocBytes), mib(res.fpIdle.HeapAllocBytes),
			mib(res.fpSteady.HeapAllocBytes), mib(res.fpDrain.HeapAllocBytes))
		ln("| HeapInuse    | %-11s | %-11s | %-11s | %-11s |",
			mib(res.fpBoot.HeapInuseBytes), mib(res.fpIdle.HeapInuseBytes),
			mib(res.fpSteady.HeapInuseBytes), mib(res.fpDrain.HeapInuseBytes))
		ln("| HeapSys      | %-11s | %-11s | %-11s | %-11s |",
			mib(res.fpBoot.HeapSysBytes), mib(res.fpIdle.HeapSysBytes),
			mib(res.fpSteady.HeapSysBytes), mib(res.fpDrain.HeapSysBytes))
		ln("| StackInuse   | %-11s | %-11s | %-11s | %-11s |",
			mib(res.fpBoot.StackInuseBytes), mib(res.fpIdle.StackInuseBytes),
			mib(res.fpSteady.StackInuseBytes), mib(res.fpDrain.StackInuseBytes))
		ln("| Sys          | %-11s | %-11s | %-11s | %-11s |",
			mib(res.fpBoot.SysBytes), mib(res.fpIdle.SysBytes),
			mib(res.fpSteady.SysBytes), mib(res.fpDrain.SysBytes))
		ln("| NumGC        | %-11d | %-11d | %-11d | %-11d |",
			res.fpBoot.NumGC, res.fpIdle.NumGC, res.fpSteady.NumGC, res.fpDrain.NumGC)
		ln("")
		ln("pprof artifacts: %s", res.artifactDir)
		ln("")
	}

	ln("---")
	ln("")
	ln("## Notes")
	ln("")
	ln("- The **goroutine-stability gate** (S2 <= g0+eps) is the primary leak signal.")
	ln("  \"PASS\" means all goroutines launched by the pipeline, sweeps, and lifecycle")
	ln("  stages were collected after drain + settle.")
	ln("- The **idle gate** (gIdle <= g0+eps) checks for goroutine growth at zero traffic.")
	ln("  A leak here points to tickers or sweeps that accumulate at rest.")
	ln("- Errors during load are tolerated — the mock gateway under concurrent load")
	ln("  may return transient errors; the rig measures resources, not correctness.")
	ln("- pprof artifacts are ephemeral by default (t.TempDir()). Set -profile.out")
	ln("  to a persistent path and inspect with %sgo tool pprof%s.", bt, bt)
	ln("- The rig does NOT run under the default %sgo test ./...%s (build tag %sprofile%s", bt, bt, bt, bt)
	ln("  guards it). It is a deliberate explicit gate (%smake profile%s), like %smake slo%s.", bt, bt, bt, bt)
	ln("- Cells run **sequentially** to avoid cross-contamination of block/mutex")
	ln("  profile rates and goroutine counts between backends.")

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
