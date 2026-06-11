package pipeline

import (
	"time"

	"github.com/hurtener/stowage/internal/config"
)

// Triggers holds the resolved buffer-flush trigger thresholds for the stage.
// Values come from config.BufferTriggersForProfile — never from top-level
// config knobs (D-034 guardrail, D-042).
type Triggers struct {
	Count  int
	Tokens int64
	MaxAge time.Duration

	// TickBase is the base interval of the due-scan ticker; the effective
	// delay is TickBase + jitter(0..TickBase/2). Zero means the production
	// default (4s). Injectable so tests are not timing-flaky.
	TickBase time.Duration
}

// TriggersFromConfig returns Triggers for the given profile name, delegating
// to config.BufferTriggersForProfile (profiles.go pattern).
func TriggersFromConfig(profile string) Triggers {
	ct := config.BufferTriggersForProfile(profile)
	return Triggers{
		Count:  ct.Count,
		Tokens: ct.Tokens,
		MaxAge: ct.MaxAge,
	}
}
