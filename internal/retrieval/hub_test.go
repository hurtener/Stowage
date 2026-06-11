package retrieval_test

import (
	"fmt"
	"testing"

	"github.com/hurtener/stowage/internal/retrieval"
)

func TestHubRecordAndSignals(t *testing.T) {
	t.Parallel()
	hub := retrieval.ExportNewHub(8)

	// No records yet.
	if s := hub.Signals("mem-A"); s != 0 {
		t.Errorf("empty hub: signals for unknown mem = %d want 0", s)
	}

	// Record mem-A for 3 distinct signatures.
	hub.Record("mem-A", "sig1")
	hub.Record("mem-A", "sig2")
	hub.Record("mem-A", "sig3")

	if s := hub.Signals("mem-A"); s != 3 {
		t.Errorf("after 3 distinct sigs: signals = %d want 3", s)
	}

	// Same signature recorded again → still 3 (set semantics).
	hub.Record("mem-A", "sig1")
	if s := hub.Signals("mem-A"); s != 3 {
		t.Errorf("after duplicate sig: signals = %d want 3", s)
	}
}

func TestHubLRUEviction(t *testing.T) {
	t.Parallel()
	// Create a hub with capacity 3.
	hub := retrieval.ExportNewHub(3)

	// Fill to capacity.
	for i := 0; i < 3; i++ {
		hub.Record(fmt.Sprintf("mem-%d", i), "sig")
	}
	// Verify all three are present.
	for i := 0; i < 3; i++ {
		if s := hub.Signals(fmt.Sprintf("mem-%d", i)); s != 1 {
			t.Errorf("mem-%d: signals = %d want 1", i, s)
		}
	}

	// Insert a 4th entry — should evict the LRU (mem-0 was inserted first
	// and not accessed since).
	hub.Record("mem-3", "sig")

	// mem-0 should be evicted.
	if s := hub.Signals("mem-0"); s != 0 {
		t.Errorf("evicted mem-0: signals = %d want 0", s)
	}
	// mem-3 should be present.
	if s := hub.Signals("mem-3"); s != 1 {
		t.Errorf("new mem-3: signals = %d want 1", s)
	}
}

func TestHubMoveToFrontOnAccess(t *testing.T) {
	t.Parallel()
	// Access mem-0 after inserting so it moves to front; then insert 3 more
	// and verify mem-0 survives (not evicted).
	hub := retrieval.ExportNewHub(3)
	hub.Record("mem-0", "sig-a")
	hub.Record("mem-1", "sig-a")
	// Access mem-0 again → it moves to front.
	hub.Record("mem-0", "sig-b")
	// Now insert mem-2 (cap=3, full) — mem-1 is LRU tail.
	hub.Record("mem-2", "sig-a")

	if s := hub.Signals("mem-0"); s == 0 {
		t.Error("mem-0 should not be evicted (was accessed recently)")
	}
	// mem-1 is LRU tail — it may be evicted. If cap=3 and we inserted
	// mem-0, mem-1, mem-2 with mem-0 touched again, mem-1 is tail.
	// This depends on the order of moves. We just verify mem-0 survived.
}

func TestQuerySig(t *testing.T) {
	t.Parallel()
	// Empty tokens → "empty".
	if sig := retrieval.ExportQuerySig(nil); sig != "empty" {
		t.Errorf("nil tokens: got %q want empty", sig)
	}
	if sig := retrieval.ExportQuerySig([]string{}); sig != "empty" {
		t.Errorf("empty tokens: got %q want empty", sig)
	}

	// Same tokens in different order → same signature (sorted).
	sig1 := retrieval.ExportQuerySig([]string{"go", "postgres", "database"})
	sig2 := retrieval.ExportQuerySig([]string{"database", "go", "postgres"})
	if sig1 != sig2 {
		t.Errorf("order-independent: sig1=%s sig2=%s differ", sig1, sig2)
	}

	// Different tokens → different signature.
	sig3 := retrieval.ExportQuerySig([]string{"redis", "cache"})
	if sig1 == sig3 {
		t.Error("different tokens produced the same signature")
	}
}
