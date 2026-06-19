package retrieval_test

import (
	"testing"

	"github.com/hurtener/stowage/internal/retrieval"
)

func TestQuerySig(t *testing.T) {
	t.Parallel()
	// Empty tokens → "empty".
	if sig := retrieval.ExportQuerySig(nil); sig != "empty" {
		t.Errorf("nil tokens: got %q want empty", sig)
	}
	if sig := retrieval.ExportQuerySig([]string{}); sig != "empty" {
		t.Errorf("empty tokens: got %q want empty", sig)
	}

	// Same tokens in different order → same signature (sorted). Two retrieves with
	// the same token set count as ONE query cluster for hub dampening (D-092).
	sig1 := retrieval.ExportQuerySig([]string{"go", "postgres", "database"})
	sig2 := retrieval.ExportQuerySig([]string{"database", "go", "postgres"})
	if sig1 != sig2 {
		t.Errorf("order-independent: sig1=%s sig2=%s differ", sig1, sig2)
	}

	// Different tokens → different signature (distinct cluster).
	sig3 := retrieval.ExportQuerySig([]string{"redis", "cache"})
	if sig1 == sig3 {
		t.Error("different tokens produced the same signature")
	}
}

func TestHubWindowSane(t *testing.T) {
	t.Parallel()
	// The durable hub recency window must be positive and on the order of weeks
	// (a tuning constant, not a knob — D-034/D-092).
	if w := retrieval.ExportHubWindowMs(); w <= 0 {
		t.Errorf("hub window must be positive, got %d", w)
	}
}
