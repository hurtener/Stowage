package lifecycle

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/scoring"
	"github.com/hurtener/stowage/internal/store"
)

// decaySweepLockKey is the advisory lock key for the decay sweep (D-057).
const decaySweepLockKey int64 = 0x1401

// runDecay executes one pass of the decay sweep.
// For each active memory, computes decay factor and:
//   - if >= floor: clear valid_until if set (reset the grace timer)
//   - if < floor and valid_until == 0: set valid_until = now + grace period
//   - if < floor and valid_until set and we're past it: expire the memory
func (m *Manager) runDecay(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, decaySweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/decay: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/decay: list tenants failed", "err", err)
		return
	}

	nowMs := time.Now().UnixMilli()
	for _, tenant := range tenants {
		m.decayTenant(ctx, tenant, nowMs)
	}
}

func (m *Manager) decayTenant(ctx context.Context, tenant string, nowMs int64) {
	scope := identity.Scope{Tenant: tenant}
	processed := 0
	cursor := ""
	for processed < m.profile.DecayBatchSize {
		remaining := m.profile.DecayBatchSize - processed
		if remaining <= 0 {
			break
		}
		batch, next, err := m.st.Memories().ListActiveForDecay(ctx, scope, remaining, cursor)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/decay: list failed", "tenant", tenant, "err", err)
			return
		}
		// Approximate activity turns as 0 — conservative, uses only wall-clock delta.
		const activityTurns int64 = 0

		for _, mem := range batch {
			m.processDecayMemory(ctx, scope, mem, nowMs, activityTurns)
		}
		processed += len(batch)
		if next == "" || len(batch) == 0 {
			break
		}
		cursor = next
	}
}

func (m *Manager) processDecayMemory(ctx context.Context, scope identity.Scope, mem store.Memory, nowMs int64, activityTurns int64) {
	facts := scoring.MemoryFacts{
		UseCount:       mem.UseCount,
		SaveCount:      mem.SaveCount,
		Stability:      mem.Stability,
		LastAccessedAt: mem.LastAccessedAt,
		TrustSource:    mem.TrustSource,
		Importance:     mem.Importance,
	}
	df := scoring.DecayFactor(facts, nowMs, activityTurns)
	floor := scoring.DecayFloorFor(mem.TrustSource)

	// DecayFactor returns values in [floor, 1.0]; df == floor means the raw
	// decay was below floor (clamped). df > floor means the memory is healthy.
	if df > floor {
		// Strictly above floor: clear valid_until if we had previously set it.
		if mem.ValidUntil > 0 {
			if err := m.st.Memories().SetValidUntil(ctx, scope, mem.ID, 0); err != nil {
				m.log.WarnContext(ctx, "lifecycle/decay: clear valid_until failed",
					"id", mem.ID, "err", err)
			}
		}
		return
	}

	// At floor (df == floor): raw decay was at or below floor.
	graceMs := int64(m.profile.DecayGraceSweeps) * int64(m.profile.DecayInterval)

	if mem.ValidUntil == 0 {
		// First below-floor observation: set valid_until = now + grace (D-058).
		graceExpiry := nowMs + graceMs
		if err := m.st.Memories().SetValidUntil(ctx, scope, mem.ID, graceExpiry); err != nil {
			m.log.WarnContext(ctx, "lifecycle/decay: set valid_until failed",
				"id", mem.ID, "err", err)
		}
		return
	}

	// Already observed below floor. Check if grace has elapsed.
	if nowMs < mem.ValidUntil {
		return // still within grace period
	}

	// Grace elapsed — expire.
	m.expireMemory(ctx, scope, mem, df, "decay: below-floor for grace period")
}

func (m *Manager) expireMemory(ctx context.Context, scope identity.Scope, mem store.Memory, decayFactor float64, reason string) {
	// Fetch junctions for prior-state snapshot (D-017).
	jt, _ := m.st.Memories().GetJunctions(ctx, scope, mem.ID)

	priorJSON := marshalDecayPriorState(mem, jt, decayFactor)
	now := time.Now().UnixMilli()

	cs := store.CommitSet{
		Action: store.ActionDiscard,
		Events: []store.Event{
			{
				ID:        ulid.Make().String(),
				Type:      "memory.expired",
				SubjectID: mem.ID,
				Reason:    reason,
				Payload:   priorJSON,
				CreatedAt: now,
			},
		},
		Scope: scope,
	}
	if err := m.st.Memories().Commit(ctx, scope, cs); err != nil {
		m.log.WarnContext(ctx, "lifecycle/decay: expire commit failed",
			"id", mem.ID, "err", err)
		return
	}

	// SetStatus to expired (Commit(discard) only writes events, doesn't change status).
	if err := m.st.Memories().SetStatus(ctx, scope, mem.ID, "expired", now); err != nil {
		m.log.WarnContext(ctx, "lifecycle/decay: SetStatus expired failed",
			"id", mem.ID, "err", err)
	}

	m.log.InfoContext(ctx, "lifecycle/decay: memory expired",
		"tenant", scope.Tenant,
		"id", mem.ID,
		"decay_factor", decayFactor,
	)
}

// decayPriorState is the JSON payload for memory.expired events.
type decayPriorState struct {
	Memory      store.Memory          `json:"memory"`
	Junctions   store.MemoryJunctions `json:"junctions"`
	DecayFactor float64               `json:"decay_factor"`
}

func marshalDecayPriorState(mem store.Memory, jt store.MemoryJunctions, df float64) string {
	b, err := json.Marshal(decayPriorState{Memory: mem, Junctions: jt, DecayFactor: df})
	if err != nil {
		return "{}"
	}
	return string(b)
}

// SweepDecayOnce is the test-hook entry point for the decay sweep.
func (m *Manager) SweepDecayOnce(ctx context.Context) {
	m.runDecay(ctx)
}
