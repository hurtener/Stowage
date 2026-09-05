package mcpserver

import (
	"context"
	"testing"

	"github.com/hurtener/dockyard/runtime/server"
	"github.com/hurtener/stowage/internal/reconcile"
)

func TestExplicitHostBindingIsOutsideArguments(t *testing.T) {
	ctx := server.WithRequestMeta(context.Background(), map[string]any{"stowage": map[string]any{"source_record_id": "host-source", "idempotency_key": "host-command", "operation": "remember"}})
	req, err := boundExplicit(ctx, "remember", reconcile.ExplicitRequest{Quote: "an actual quotation"})
	if err != nil || req.SourceRecordID != "host-source" || req.IdempotencyKey != "host-command" {
		t.Fatalf("binding lost: %+v %v", req, err)
	}
	if _, err := boundExplicit(ctx, "remember", reconcile.ExplicitRequest{SourceRecordID: "different"}); err == nil {
		t.Fatal("model source overrode host")
	}
	if _, err := boundExplicit(ctx, "correct", reconcile.ExplicitRequest{}); err == nil {
		t.Fatal("operation binding ignored")
	}
	for _, binding := range []any{"invalid", map[string]any{"source_record_id": 12}, map[string]any{"idempotency_key": true}, map[string]any{"operation": []string{"remember"}}} {
		ctx := server.WithRequestMeta(context.Background(), map[string]any{"stowage": binding})
		if _, err := boundExplicit(ctx, "remember", reconcile.ExplicitRequest{SourceRecordID: "source"}); err == nil {
			t.Errorf("accepted malformed binding: %v", binding)
		}
	}
}
