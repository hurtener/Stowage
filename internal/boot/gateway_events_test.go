package boot

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func TestCheckEmbedModel(t *testing.T) {
	if err := checkEmbedModel(nil, "m-1"); err != nil {
		t.Errorf("empty persisted: %v", err)
	}
	if err := checkEmbedModel([]string{"m-1"}, "m-1"); err != nil {
		t.Errorf("match: %v", err)
	}
	if err := checkEmbedModel([]string{"m-1", "m-2"}, "m-1"); err == nil {
		t.Error("a persisted model differing from config must error (reindex guard)")
	}
	if err := checkEmbedModel([]string{"old-model"}, "new-model"); err == nil {
		t.Error("model swap must be rejected")
	}
}

// recordingEvents is a minimal EventStore capturing emitted events (mutex-guarded:
// the async emitter writes from its drain goroutine).
type recordingEvents struct {
	store.EventStore
	mu      sync.Mutex
	emitted []store.Event
	scopes  []identity.Scope
}

func (r *recordingEvents) Emit(_ context.Context, scope identity.Scope, e store.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scopes = append(r.scopes, scope)
	r.emitted = append(r.emitted, e)
	return nil
}

func TestGatewayUsageEmitter_ScopedAndSkipped(t *testing.T) {
	rec := &recordingEvents{}
	em := newGatewayUsageEmitter(rec, slog.New(slog.NewTextHandler(io.Discard, nil)))

	// Scope-less ctx ⇒ skipped (no tenant to attribute).
	em.EmitUsage(context.Background(), "embed", "m-1", 10, 0, 0.001)

	// Scoped ctx ⇒ a gateway.call + gateway.rerank event attributed to the tenant.
	ctx := identity.WithScope(context.Background(), identity.Scope{Tenant: "acme"})
	em.EmitUsage(ctx, "complete", "m-2", 100, 50, 0.02)
	em.EmitRerankUsage(ctx, "rerank-m", 7, 0.005)

	_ = em.Close(context.Background()) // drains the async buffer

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.emitted) != 2 {
		t.Fatalf("expected 2 emitted events (scope-less skipped), got %d", len(rec.emitted))
	}
	if rec.emitted[0].Type != "gateway.call" || rec.scopes[0].Tenant != "acme" {
		t.Errorf("call event wrong: type=%s tenant=%s", rec.emitted[0].Type, rec.scopes[0].Tenant)
	}
	if rec.emitted[1].Type != "gateway.rerank" {
		t.Errorf("rerank event type = %s", rec.emitted[1].Type)
	}
}
