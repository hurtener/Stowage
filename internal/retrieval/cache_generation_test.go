package retrieval

import (
	"testing"

	"github.com/hurtener/stowage/internal/identity"
)

// TestResultCache_RejectsStaleConcurrentFill models the ordering where a
// retrieval misses and begins reading, memory_assert commits and invalidates,
// then the older retrieval tries to publish. The stale fill must be rejected.
func TestResultCache_RejectsStaleConcurrentFill(t *testing.T) {
	t.Parallel()

	c := NewResultCache(16)
	readScope := identity.Scope{
		Tenant: "tenant-a", Project: "project-a", User: "user-a",
		Session: "session-a", Agent: "agent-a",
	}
	startGeneration := c.generation(readScope)

	// Assert invalidation is tenant-wide so every descendant/agent cache shape
	// observes the bump.
	c.InvalidateScope(identity.Scope{Tenant: readScope.Tenant})
	if c.putIfGeneration(readScope, "sig", "balanced", "session-a", 0, 0, nil, false, 5, nil, Support{}, startGeneration) {
		t.Fatal("stale in-flight result was cached after generation invalidation")
	}
	if _, _, ok := c.Get(readScope, "sig", "balanced", "session-a", 0, 0, nil, false, 5); ok {
		t.Fatal("stale in-flight cache entry remained observable")
	}
}
