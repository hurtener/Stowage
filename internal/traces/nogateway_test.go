package traces_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNoGatewayDependency enforces the D-086 invariant that internal/traces stays
// gateway-free — TRANSITIVELY, not just by a per-file grep (the playbook D-072 pattern).
// A trace is a deterministic read-assembly + ed25519 sign; it must never pull the
// intelligence seam. If this fails, something in the package (or a new dep) imported
// internal/gateway transitively.
func TestNoGatewayDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", "github.com/hurtener/stowage/internal/traces").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == "github.com/hurtener/stowage/internal/gateway" {
			t.Fatalf("internal/traces transitively imports internal/gateway — the trace core must stay gateway-free (D-086)")
		}
	}
}
