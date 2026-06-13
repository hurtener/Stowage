package harness

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestHarnessStageParity is the in-sync guard for the sanctioned
// post-boot-wiring exception (D-067 Wave-A checkpoint). The eval harness
// hand-wires the pipeline instead of routing through boot.StartPipeline (see the
// package doc for why); this would be an invisible benchmark-gate drift surface
// because the Phase-h1 AC-1 grep only scans cmd/stowage and sdk/stowage, never
// eval/.
//
// This test closes that gap mechanically: it reads boot/pipeline.go and asserts
// that every core stage constructor boot.StartPipeline wires is ALSO wired in
// this package's server.go. If a future change adds a new stage to the shared
// seam, this test fails — forcing the harness owner to wire it too or record a
// new documented exception, rather than silently shipping an out-of-date gate.
func TestHarnessStageParity(t *testing.T) {
	bootSrc := readRepoFile(t, "internal", "boot", "pipeline.go")
	harnessSrc := readRepoFile(t, "eval", "harness", "server.go")

	// Core stage constructors that turn the static stack into a live system.
	// BackfillSweep is the one documented exception: the harness drives mock
	// embeddings synchronously via the embedder worker (embedder.Start) and does
	// not exercise the degraded-gateway embed-recovery sweep deterministically.
	constructors := []string{
		"pipeline.New(",
		"pipeline.NewExtractStage(",
		"reconcile.New(",
		"lifecycle.New(",
	}

	for _, c := range constructors {
		if !strings.Contains(bootSrc, c) {
			t.Errorf("boot.StartPipeline no longer wires %q — update this in-sync test "+
				"and re-evaluate the eval-harness exception", c)
			continue
		}
		if !strings.Contains(harnessSrc, c) {
			t.Errorf("eval harness server.go is missing %q which boot.StartPipeline wires: "+
				"the benchmark-gate harness has drifted from the shared post-boot seam. "+
				"Wire it in NewTestServer or record a new documented exception (D-067).", c)
		}
	}

	// Sanity-check the documented exception still holds: boot wires BackfillSweep,
	// the harness intentionally does not. If boot ever drops BackfillSweep, the
	// exception note in server.go is stale and must be revisited.
	if !strings.Contains(bootSrc, "BackfillSweep") {
		t.Errorf("boot.StartPipeline no longer wires BackfillSweep — the harness " +
			"exception note in server.go is now stale; revisit it")
	}
}

// readRepoFile reads a file relative to the repo root (resolved from this test's
// own path: eval/harness/ → repo root is two dirs up).
func readRepoFile(t *testing.T, parts ...string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(filename))) // eval/harness/<file> → repo
	full := filepath.Join(append([]string{repoRoot}, parts...)...)
	b, err := os.ReadFile(full) //nolint:gosec // test reads a fixed in-repo source path
	if err != nil {
		t.Fatalf("read %s: %v", full, err)
	}
	return string(b)
}
