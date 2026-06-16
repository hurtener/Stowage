package topics

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// TopicView is the API-visible representation of one active topic. Source
// distinguishes explicit (stored in TopicStore) from pack (virtual — D-043).
type TopicView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// Pack is non-empty for topics that belong to a named pack.
	Pack string `json:"pack,omitempty"`
	// Source is "explicit" for topics stored in the TopicStore, "pack" for
	// virtual topics injected from the profile default pack (D-043).
	Source string `json:"source"`
}

// Service manages topics for a scope, applying virtual default pack logic
// (D-043). It is the single access point for the extraction stage and the
// topics API handler.
type Service struct {
	ts      store.TopicStore
	log     *slog.Logger
	profile string // "assistant" | "coding-agent" | "fleet"
}

// New creates a Service backed by ts. profile selects which virtual default
// pack applies when a scope has no explicit topics (D-043).
func New(ts store.TopicStore, log *slog.Logger, profile string) *Service {
	return &Service{ts: ts, log: log, profile: profile}
}

// ActiveTopics returns the effective topic set for the scope (D-043):
//
//   - If the scope has any active explicit topics, those are returned (the
//     virtual default pack is suppressed by the presence of explicit topics).
//   - If the scope has an active topic with Key == PackOff (the opt-out
//     sentinel), nil is returned (caller must short-circuit without a gateway
//     call — AC-2).
//   - Otherwise, the virtual default pack for the profile is returned with
//     Source="pack".
//
// Deleted and paused topics are excluded.
// TopicUpsert is one topic to upsert via Upsert.
type TopicUpsert struct {
	Key         string
	Description string
	Status      string // defaults to "active"; must be "active" or "paused"
}

// Upsert upserts each topic by key within scope and returns the count written.
// It is the shared topic-write core for the embedded SDK (D-071); pack:off is a
// normal topic whose Key == PackOff, so opting out of the virtual default pack
// is an Upsert of {key: "pack:off"} (D-043). status defaults to "active".
func (s *Service) Upsert(ctx context.Context, scope identity.Scope, items []TopicUpsert) (int, error) {
	if len(items) == 0 {
		return 0, fmt.Errorf("topics: upsert: items must not be empty")
	}
	now := time.Now().UnixMilli()
	for i, item := range items {
		if item.Key == "" {
			return 0, fmt.Errorf("topics: upsert: item[%d]: key must not be empty", i)
		}
		status := item.Status
		if status == "" {
			status = "active"
		}
		if status != "active" && status != "paused" {
			return 0, fmt.Errorf("topics: upsert: item[%d]: status must be active or paused", i)
		}
		t := store.Topic{
			ID:          ulid.Make().String(),
			TenantID:    scope.Tenant,
			Key:         item.Key,
			Description: item.Description,
			Status:      status,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		if err := s.ts.Upsert(ctx, scope, t); err != nil {
			return 0, fmt.Errorf("topics: upsert item[%d]: %w", i, err)
		}
	}
	return len(items), nil
}

// Delete soft-deletes a topic by key within scope. Returns store.ErrNotFound
// (wrapped) when the key is absent.
func (s *Service) Delete(ctx context.Context, scope identity.Scope, key string) error {
	if key == "" {
		return fmt.Errorf("topics: delete: key must not be empty")
	}
	if err := s.ts.Delete(ctx, scope, key); err != nil {
		return fmt.Errorf("topics: delete: %w", err)
	}
	return nil
}

func (s *Service) ActiveTopics(ctx context.Context, scope identity.Scope) ([]TopicView, error) {
	stored, err := s.ts.List(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("topics: list: %w", err)
	}

	// Separate pack:off sentinel from other active topics.
	var active []TopicView
	packOffPresent := false
	for _, t := range stored {
		if t.Status != "active" {
			continue
		}
		if t.Key == PackOff {
			packOffPresent = true
			continue
		}
		active = append(active, TopicView{
			Key:         t.Key,
			Description: t.Description,
			Status:      t.Status,
			Pack:        t.Pack,
			Source:      "explicit",
		})
	}

	// Explicit active topics → return them; virtual pack is suppressed.
	if len(active) > 0 {
		return active, nil
	}

	// pack:off sentinel active and no other topics → opt-out; caller skips extraction.
	if packOffPresent {
		return nil, nil
	}

	// No explicit topics → virtual default pack (D-043).
	packName, entries := defaultPackForProfile(s.profile)
	views := make([]TopicView, len(entries))
	for i, e := range entries {
		views[i] = TopicView{
			Key:         e.Key,
			Description: e.Description,
			Status:      "active",
			Pack:        packName,
			Source:      "pack",
		}
	}
	return views, nil
}
