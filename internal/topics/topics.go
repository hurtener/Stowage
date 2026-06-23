package topics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// ErrInvalidTopic is the sentinel wrapping an Upsert/Delete validation failure
// (empty key, bad status, empty item set). Surfaces map errors.Is(err,
// ErrInvalidTopic) to a 400/bad-request response; anything else from the service
// is a store error (500). Routing all surfaces through the service (D-071,
// Wave-B checkpoint) means active|paused validation is enforced once, everywhere.
var ErrInvalidTopic = errors.New("topics: invalid topic")

// TopicView is the API-visible representation of one active topic. Source
// distinguishes explicit (stored in TopicStore) from pack (virtual — D-043).
type TopicView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Status      string `json:"status"`
	// Pack is non-empty for topics that belong to a named pack.
	Pack string `json:"pack,omitempty"`
	// Source is "explicit" for topics stored in the TopicStore, or the pack name
	// (e.g. "pack:project") for virtual topics injected from an enabled pack
	// (D-043 introduced "pack"; D-099 widens it to the specific pack name so a
	// composed set shows each entry's origin). The Pack field carries the same
	// name and is retained for back-compat.
	Source string `json:"source"`
}

// Resolution is the composed effective topic set for a scope plus composition
// metadata (D-099). Returned by Resolve; ActiveTopics is the thin read-path wrapper.
type Resolution struct {
	// Topics is the deduped, capped topic set the extractor should use.
	Topics []TopicView
	// DroppedKeys lists pack-entry keys dropped by the MaxActiveTopics cap, in drop
	// order. Explicit topics are never dropped; this is empty unless enabled packs
	// pushed the set past the cap.
	DroppedKeys []string
}

// Service manages topics for a scope, applying virtual default pack logic
// (D-043). It is the single access point for the extraction stage and the
// topics API handler.
type Service struct {
	ts      store.TopicStore
	log     *slog.Logger
	profile string // "assistant" | "coding-agent" | "fleet"
}

// New creates a Service backed by ts. profile selects the ordered list of default
// packs applied when a scope has expressed no intent (D-043, D-099).
func New(ts store.TopicStore, log *slog.Logger, profile string) *Service {
	return &Service{ts: ts, log: log, profile: profile}
}

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
		return 0, fmt.Errorf("topics: upsert: items must not be empty: %w", ErrInvalidTopic)
	}
	now := time.Now().UnixMilli()
	for i, item := range items {
		if item.Key == "" {
			return 0, fmt.Errorf("topics: upsert: item[%d]: key must not be empty: %w", i, ErrInvalidTopic)
		}
		// The "pack:" namespace is reserved for sentinels (D-099). Allow the two
		// control sentinels — pack:off and pack:on:<name> — but reject a bare pack
		// name (e.g. "pack:project") used as an explicit topic, which would silently
		// behave as an ordinary topic instead of enabling the pack (the footgun).
		if strings.HasPrefix(item.Key, "pack:") && item.Key != PackOff && !strings.HasPrefix(item.Key, packOnPrefix) {
			return 0, fmt.Errorf("topics: upsert: item[%d]: key %q uses the reserved pack: namespace (use pack:on:<name> to enable a pack): %w", i, item.Key, ErrInvalidTopic)
		}
		status := item.Status
		if status == "" {
			status = "active"
		}
		if status != "active" && status != "paused" {
			return 0, fmt.Errorf("topics: upsert: item[%d]: status must be active or paused: %w", i, ErrInvalidTopic)
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
		return fmt.Errorf("topics: delete: key must not be empty: %w", ErrInvalidTopic)
	}
	if err := s.ts.Delete(ctx, scope, key); err != nil {
		return fmt.Errorf("topics: delete: %w", err)
	}
	return nil
}

// Resolve composes a scope's effective extraction topics (D-099, amending D-043):
//
//   - effective = union(explicit topics, entries of every ENABLED pack), deduped by
//     key with explicit winning collisions and, among packs, first-enabled winning;
//   - a pack is enabled by a `pack:on:<name>` sentinel topic; an unknown pack name is
//     logged and ignored (not treated as an explicit topic);
//   - the profile's default pack list applies ONLY when the scope has no explicit
//     topics and no enabled packs (the zero-config path);
//   - `pack:off` dominates the pack layer: it suppresses every pack (default and
//     pack:on), leaving only explicit topics; when none remain the result is empty and
//     the caller short-circuits extraction without a gateway call (AC-2);
//   - the set is capped at MaxActiveTopics by dropping PACK entries (by enable order,
//     into DroppedKeys, never silently); explicit topics are never dropped, so the
//     result may exceed the cap in the rare case where explicit topics alone do.
//
// Deleted and paused topics are excluded.
func (s *Service) Resolve(ctx context.Context, scope identity.Scope) (Resolution, error) {
	stored, err := s.ts.List(ctx, scope)
	if err != nil {
		return Resolution{}, fmt.Errorf("topics: list: %w", err)
	}

	var explicit []TopicView
	var enabledPacks []string
	seenPack := make(map[string]bool)
	packOffPresent := false
	for _, t := range stored {
		if t.Status != "active" {
			continue
		}
		switch {
		case t.Key == PackOff:
			packOffPresent = true
		case strings.HasPrefix(t.Key, packOnPrefix):
			name, ok := packNameFromOnSentinel(t.Key)
			if !ok {
				s.log.WarnContext(ctx, "topics: unknown pack in pack:on sentinel; ignoring", "key", t.Key)
				continue
			}
			if !seenPack[name] {
				seenPack[name] = true
				enabledPacks = append(enabledPacks, name)
			}
		default:
			explicit = append(explicit, TopicView{
				Key:         t.Key,
				Description: t.Description,
				Status:      t.Status,
				Pack:        t.Pack,
				Source:      "explicit",
			})
		}
	}

	// pack:off dominates the PACK layer: it suppresses every pack (the profile
	// default AND any pack:on), leaving only the scope's explicit topics (D-099,
	// preserving D-043 — pack:off was always ignored when explicit topics were
	// present, and short-circuits when it stands alone). It does not erase explicit
	// topics; the short-circuit happens below only if the composed set is empty.
	switch {
	case packOffPresent:
		enabledPacks = nil
	case len(explicit) == 0 && len(enabledPacks) == 0:
		// Zero expressed intent → the profile's default pack list (zero-config path).
		enabledPacks = defaultPacksForProfile(s.profile)
	}

	// Explicit topics first — they win key collisions and are never capped.
	out := make([]TopicView, 0, len(explicit))
	seenKey := make(map[string]bool, len(explicit))
	for _, v := range explicit {
		if seenKey[v.Key] { // store UNIQUE makes this defensive, not load-bearing
			continue
		}
		seenKey[v.Key] = true
		out = append(out, v)
	}

	// Pack entries, in enable order, deduped against explicit and earlier packs.
	var packViews []TopicView
	for _, name := range enabledPacks {
		entries, ok := packEntriesByName(name)
		if !ok {
			continue
		}
		for _, e := range entries {
			if seenKey[e.Key] {
				continue
			}
			seenKey[e.Key] = true
			packViews = append(packViews, TopicView{
				Key:         e.Key,
				Description: e.Description,
				Status:      "active",
				Pack:        name,
				Source:      name,
			})
		}
	}

	// Cap: fill packs up to the budget remaining after explicit; drop the overflow.
	remaining := MaxActiveTopics - len(out)
	if remaining < 0 {
		remaining = 0
	}
	var dropped []string
	for i, pv := range packViews {
		if i < remaining {
			out = append(out, pv)
		} else {
			dropped = append(dropped, pv.Key)
		}
	}
	if len(dropped) > 0 {
		s.log.WarnContext(ctx, "topics: composed set exceeded MaxActiveTopics; dropped pack entries",
			"cap", MaxActiveTopics, "kept", len(out), "dropped_count", len(dropped))
	}

	// Normalize empty → nil so the opt-out / no-topics contract is a nil slice
	// (callers short-circuit on len==0 either way).
	if len(out) == 0 {
		out = nil
	}

	return Resolution{Topics: out, DroppedKeys: dropped}, nil
}

// ActiveTopics returns the effective composed topic set for the scope — the
// read-path wrapper over Resolve, discarding cap metadata. Callers that must react
// to capping (the extract stage) call Resolve directly.
func (s *Service) ActiveTopics(ctx context.Context, scope identity.Scope) ([]TopicView, error) {
	r, err := s.Resolve(ctx, scope)
	return r.Topics, err
}
