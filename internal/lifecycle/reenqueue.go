package lifecycle

import (
	"context"
	"time"

	"github.com/hurtener/stowage/internal/pipeline"
)

// reenqueueSweepLockKey is the advisory lock key for the re-enqueue sweep (D-057).
const reenqueueSweepLockKey int64 = 0x1404

func (m *Manager) runReenqueue(ctx context.Context) {
	release, err := m.st.Ops().AdvisoryLock(ctx, reenqueueSweepLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/reenqueue: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	olderThan := time.Now().Add(-m.profile.ReenqueueDeadline).UnixMilli()
	records, err := m.st.Records().ListUnprocessed(ctx, olderThan, m.profile.ReenqueueBatchSize)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/reenqueue: list unprocessed failed", "err", err)
		return
	}

	requeued := 0
	for _, rec := range records {
		item := pipeline.Item{
			RecordID:  rec.ID,
			TenantID:  rec.TenantID,
			SessionID: rec.SessionID,
			BranchID:  rec.BranchID,
		}
		select {
		case m.ingest <- item:
			requeued++
		default:
			// Channel full — leave remaining records for the next pass.
			m.log.WarnContext(ctx, "lifecycle/reenqueue: ingest channel full; leaving for next pass",
				"remaining", len(records)-requeued)
			goto done
		}
	}
done:
	if requeued > 0 {
		m.log.InfoContext(ctx, "lifecycle/reenqueue: re-enqueued stalled records",
			"count", requeued)
	}
}
