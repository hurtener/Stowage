// Package proactive implements the Phase-27 proactive trigger engine (RFC §6d, D-087):
// the memory service offers context (recent/similar episodes, expiring memories) for a
// session, scored with the same machinery as retrieval, gated by a per-scope governance
// threshold + budget, and tuned by accept/dismiss feedback. The package is gateway-free
// except the similar_episode rule, which embeds via the injected NarrativeSearcher
// (degraded-safe).
package proactive

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Trigger-class names (also the suggestions.trigger_kind values).
const (
	ClassRecentEpisode  = "recent_episode"
	ClassSimilarEpisode = "similar_episode"
	ClassExpiring       = "expiring"
)

// settingKey is the scope-settings key holding the governance JSON.
const settingKey = "proactive"

// Config is the effective proactive governance for a scope. It is the profile default
// overlaid by the scope's stored override (RFC §6d "stored scope settings, not config
// files"). Opt-out is Enabled=false.
type Config struct {
	Enabled   bool            `json:"enabled"`
	Threshold float64         `json:"threshold"` // min final score to surface
	Budget    int             `json:"budget"`    // max offers per request (strict per-turn budget)
	Classes   map[string]bool `json:"classes"`   // enabled trigger classes
}

// classEnabled reports whether a trigger class may fire (absent ⇒ disabled).
func (c Config) classEnabled(name string) bool { return c.Classes != nil && c.Classes[name] }

// clamp bounds a decoded config into a sane range (defensive against bad stored JSON).
func (c Config) clamp() Config {
	if c.Threshold < 0 {
		c.Threshold = 0
	}
	if c.Budget < 0 {
		c.Budget = 0
	}
	if c.Budget > 20 {
		c.Budget = 20 // hard ceiling — "silence over spam"
	}
	if c.Classes == nil {
		c.Classes = map[string]bool{}
	}
	return c
}

// Resolve merges the profile default with the scope's stored "proactive" setting (the
// override wins for any field present; an absent setting ⇒ the profile default). The
// resolution reads the EXACT scope given — callers pass the most-specific scope they
// hold (user/tenant); a future enhancement may walk scope precedence.
func Resolve(ctx context.Context, ss store.ScopeSettingsStore, scope identity.Scope, profileDefault Config) (Config, error) {
	cfg := profileDefault
	raw, found, err := ss.Get(ctx, scope, settingKey)
	if err != nil {
		return Config{}, fmt.Errorf("proactive: load governance: %w", err)
	}
	if found {
		var override Config
		if uerr := json.Unmarshal([]byte(raw), &override); uerr != nil {
			// A malformed stored setting must not crash or silently re-enable —
			// fail safe to OFF (the conservative choice for a governance gate).
			// Still clamp so the returned Config honours every invariant (non-nil
			// classes, bounded budget) just like the happy path.
			return Config{Enabled: false}.clamp(), nil
		}
		cfg = override
	}
	return cfg.clamp(), nil
}

// MarshalConfig is the canonical JSON for storing a governance override.
func MarshalConfig(c Config) (string, error) {
	b, err := json.Marshal(c.clamp())
	if err != nil {
		return "", fmt.Errorf("proactive: marshal config: %w", err)
	}
	return string(b), nil
}
