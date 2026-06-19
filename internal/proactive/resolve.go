package proactive

// resolve.go — the single logic core behind accept/dismiss (D-067). Every surface
// (HTTP/MCP/SDK) calls ResolveOffer so the feedback CAS and its audit event
// (suggestion.accepted | suggestion.dismissed) can never be omitted by one surface.

import (
	"context"
	"fmt"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ResolveOffer applies accept|dismiss to a pending offer (compare-and-swap on
// status='pending') and emits the matching lifecycle event (§8 audit trail). The
// store CAS is the durable record; emission is best-effort (the row is already
// resolved). Returns store.ErrNotPending / store.ErrNotFound unchanged so the
// surfaces map them to a uniform 404.
func ResolveOffer(ctx context.Context, st store.Store, scope identity.Scope, id, action string, now int64) (*store.Suggestion, error) {
	sug, err := st.Suggestions().Resolve(ctx, scope, id, action, now)
	if err != nil {
		return nil, err
	}
	evtType := "suggestion.accepted"
	if action == "dismiss" {
		evtType = "suggestion.dismissed"
	}
	_ = st.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), SessionID: sug.SessionID,
		Type: evtType, SubjectID: sug.ID,
		Reason:  fmt.Sprintf("proactive offer %s (%s)", action, sug.TriggerKind),
		Payload: "{}", CreatedAt: now,
	})
	return sug, nil
}
