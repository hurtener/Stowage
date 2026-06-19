package mcpserver

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

func seedExpiringMem(t *testing.T, svc *Services, scope identity.Scope) string {
	t.Helper()
	now := time.Now().UnixMilli()
	id := ulid.Make().String()
	if err := svc.Store.Memories().Insert(context.Background(), scope, store.Memory{
		ID: id, Kind: "fact", Content: "rotate the cert", Status: "active",
		Importance: 8, Confidence: 0.9, TrustSource: "user_stated", Stability: 5.0,
		ValidUntil: now + int64(time.Hour/time.Millisecond), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}
	return id
}

func TestHandlerSuggestions_ListAcceptDismiss(t *testing.T) {
	svc := newHandlerServices(t)
	svc.Profile = "assistant"
	h := makeSuggestionsHandler(svc)
	ctx := context.Background()
	memID := seedExpiringMem(t, svc, testScope())

	// list (default action).
	res, err := h(ctx, SuggestionsInput{SessionID: "s1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(res.Structured.Suggestions) != 1 || res.Structured.Suggestions[0].MemoryID != memID {
		t.Fatalf("expected 1 offer for %s, got %+v", memID, res.Structured.Suggestions)
	}
	offerID := res.Structured.Suggestions[0].ID

	// accept.
	acc, err := h(ctx, SuggestionsInput{Action: "accept", ID: offerID})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if acc.Structured.Status != "accepted" || acc.Structured.ID != offerID {
		t.Fatalf("accept result: %+v", acc.Structured)
	}

	// double-resolve ⇒ error (not pending).
	if _, err := h(ctx, SuggestionsInput{Action: "dismiss", ID: offerID}); err == nil {
		t.Error("double-resolve should error")
	}
}

func TestHandlerSuggestions_MissingIDOnResolve(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeSuggestionsHandler(svc)
	if _, err := h(context.Background(), SuggestionsInput{Action: "accept"}); err == nil {
		t.Error("accept without id should error")
	}
}

func TestHandlerSuggestions_BadAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeSuggestionsHandler(svc)
	if _, err := h(context.Background(), SuggestionsInput{Action: "frobnicate"}); err == nil {
		t.Error("bad action should error")
	}
}

func TestHandlerProactiveConfig_GetSet(t *testing.T) {
	svc := newHandlerServices(t)
	svc.Profile = "assistant"
	h := makeProactiveConfigHandler(svc)
	ctx := context.Background()

	// get (default) returns the profile default (enabled).
	got, err := h(ctx, ProactiveConfigInput{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Structured.Enabled {
		t.Errorf("assistant default should be enabled, got %+v", got.Structured)
	}

	// set a full override and confirm the resolved echo reflects it.
	no := false
	half := 0.5
	one := 1
	set, err := h(ctx, ProactiveConfigInput{Action: "set", Enabled: &no, Threshold: &half, Budget: &one,
		Classes: map[string]bool{"expiring": true}})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if set.Structured.Enabled {
		t.Errorf("override should disable, got %+v", set.Structured)
	}

	// get again reflects the stored override.
	again, _ := h(ctx, ProactiveConfigInput{Action: "get"})
	if again.Structured.Enabled {
		t.Errorf("stored override not reflected: %+v", again.Structured)
	}
}

// TestHandlerProactiveConfig_PartialPatch proves a one-field set does NOT zero-wipe
// the rest of the config (the patch-semantics fix).
func TestHandlerProactiveConfig_PartialPatch(t *testing.T) {
	svc := newHandlerServices(t)
	svc.Profile = "assistant" // default: enabled, threshold 0.45, budget 2, {recent_episode, expiring}
	h := makeProactiveConfigHandler(svc)
	ctx := context.Background()

	// Patch ONLY the threshold.
	newThresh := 0.8
	res, err := h(ctx, ProactiveConfigInput{Action: "set", Threshold: &newThresh})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	// enabled/budget/classes must be preserved from the profile default, not wiped.
	if !res.Structured.Enabled {
		t.Errorf("partial set wiped enabled: %+v", res.Structured)
	}
	if res.Structured.Budget != 2 {
		t.Errorf("partial set wiped budget (want 2): %+v", res.Structured)
	}
	if res.Structured.Threshold != 0.8 {
		t.Errorf("threshold not applied: %+v", res.Structured)
	}
	if !res.Structured.Classes["expiring"] {
		t.Errorf("partial set wiped classes: %+v", res.Structured)
	}
}

func TestHandlerProactiveConfig_BadAction(t *testing.T) {
	svc := newHandlerServices(t)
	h := makeProactiveConfigHandler(svc)
	if _, err := h(context.Background(), ProactiveConfigInput{Action: "frobnicate"}); err == nil {
		t.Error("bad action should error")
	}
}
