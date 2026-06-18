package lifecycle

import (
	"context"
	"fmt"
	"time"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/reflect"
)

// reflectSweepLockKey is the advisory lock key for the reflection sweep (D-077).
const reflectSweepLockKey int64 = 0x1406

// reflectOutcomes are the outcome tags a trajectory must terminate in to be
// reflected (ACE §6a.1 — success/failure execution feedback).
var reflectOutcomes = []string{"success", "failure"}

// runReflect is the Phase 19 reflection sweep (ACE §6a.2, D-077). Per scope it
// reads recently outcome-tagged records, assembles trajectories, distills
// strategy/failure_mode candidates via the gateway, and emits them into the
// reconcile stage (one reconcile core, fed by both extract and reflection).
//
// Idempotency + multi-epoch re-reflection (D-077 #6): a per-(scope, epoch,
// trajectory) job marker reflects each trajectory once per epoch; when the epoch
// bucket rolls over (every ReflectEpochEvery intervals) trajectories are
// re-reflected and reconcile's content-hash/near-dup pre-filters dedupe or
// supersede them rather than duplicate. The query window matches the epoch window
// so a trajectory is visible across the epoch it belongs to.
// runReflect is invoked only via the reflectionEnabled()-gated paths in Start and
// RunForce, so m.gw and m.reflectOut are always non-nil here.
func (m *Manager) runReflect(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, reflectSweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/reflect: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/reflect: list tenants failed", "err", err)
		return
	}

	now := time.Now().UnixMilli()
	// New() guarantees ReflectInterval and ReflectEpochEvery are positive, so the
	// epoch window is always > 0.
	epochWindowMs := (m.profile.ReflectInterval * time.Duration(m.profile.ReflectEpochEvery)).Milliseconds()
	epoch := now / epochWindowMs
	since := now - epochWindowMs

	for _, tenant := range tenants {
		if m.reflectTenant(ctx, tenant, since, epoch, now) {
			return // ctx cancelled / stopping
		}
	}
}

// reflectTenant reflects one tenant scope's trajectories. Returns true if the
// caller should stop (ctx done / sweep stopping).
func (m *Manager) reflectTenant(ctx context.Context, tenant string, since, epoch, now int64) (stop bool) {
	scope := identity.Scope{Tenant: tenant}
	recs, err := m.st.Records().ListByOutcome(ctx, scope, reflectOutcomes, since, m.profile.ReflectBatchSize)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/reflect: list by outcome failed", "tenant", tenant, "err", err)
		return false
	}
	if len(recs) == 0 {
		return false
	}
	// Every record from ListByOutcome is outcome-tagged, so each assembled
	// trajectory has a terminal outcome.
	trajectories := reflect.AssembleTrajectories(recs)
	for _, traj := range trajectories {
		marker := fmt.Sprintf("%s|%d|%s", scope.String(), epoch, traj.Key())
		fresh, err := m.st.Ops().CheckAndSetJobMarker(ctx, "reflect", marker, now)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/reflect: job marker failed", "tenant", tenant, "err", err)
			continue
		}
		if !fresh {
			continue // already reflected this trajectory this epoch
		}
		cands, err := reflect.Reflect(ctx, m.gw, scope, traj)
		if err != nil {
			m.log.WarnContext(ctx, "lifecycle/reflect: reflect failed", "tenant", tenant, "session", traj.SessionID, "err", err)
			continue
		}
		if len(cands) == 0 {
			continue
		}
		batch := pipeline.CandidateBatch{
			Scope:      scope,
			BranchID:   traj.BranchID,
			BufferKey:  fmt.Sprintf("reflect:%s:%s:%s", traj.UserID, traj.SessionID, traj.BranchID),
			Candidates: cands,
		}
		select {
		case m.reflectOut <- batch:
			m.log.InfoContext(ctx, "lifecycle/reflect: emitted reflection candidates",
				"tenant", tenant, "session", traj.SessionID, "outcome", traj.Outcome, "count", len(cands))
		case <-ctx.Done():
			return true
		case <-m.stopCh:
			return true
		}
	}
	return false
}
