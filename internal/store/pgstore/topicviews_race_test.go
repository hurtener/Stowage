package pgstore_test

// topicviews_race_test.go — proves the TOCTOU fix in pgstore's
// CreateView/UpdateView (dual-review Wave-3 BLOCKER 4): two concurrent
// CreateView calls for the SAME natural key (tenant_id, subject_kind,
// subject_id, view_name) but DISJOINT topic_key sets must not both succeed.
// Before the fix, each transaction's existence pre-check (viewRowsExist)
// would run under READ COMMITTED and see no rows (neither had committed
// yet), and the two inserts would not collide on the per-key UNIQUE index
// (different topic_key values) — silently merging two "creates" into one
// multi-writer view instead of the documented Create->ErrConflict semantics.
// The fix runs CreateView/UpdateView under SERIALIZABLE isolation so
// PostgreSQL detects the rw-antidependency and aborts one side with
// SQLSTATE 40001, mapped to store.ErrConflict.
//
// Requires STOWAGE_TEST_PG_DSN (skips otherwise, matching every other
// pgstore test — sqlite is exempt: it is single-writer by construction,
// D-022).
//
// Two complementary proofs:
//
//   - TestPgTopicViewsSerializable_ClosesNaturalKeyRace manually interleaves
//     two raw transactions step-by-step (bypassing the store abstraction, via
//     a second pgxpool against the same DSN) so the race window is
//     deterministic rather than dependent on goroutine scheduling: it first
//     reproduces the pre-fix vulnerability under READ COMMITTED (both commit
//     cleanly — the bug), then proves SERIALIZABLE aborts one side with
//     SQLSTATE 40001 (the fix).
//   - TestCreateView_ConcurrentDisjointTopicKeys_OneWins drives the real
//     store.TopicViewStore.CreateView API under actual goroutine
//     concurrency as a best-effort end-to-end confirmation.
import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// isSerializationFailure duplicates pgstore's unexported
// pgIsSerializationFailure check (SQLSTATE 40001) for this external test
// package.
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return err != nil && errors.As(err, &pgErr) && pgErr.Code == "40001"
}

// TestPgTopicViewsSerializable_ClosesNaturalKeyRace deterministically
// interleaves two raw transactions performing exactly the
// pre-check-then-insert sequence CreateView performs, against a natural key
// with disjoint topic keys, under both isolation levels.
func TestPgTopicViewsSerializable_ClosesNaturalKeyRace(t *testing.T) {
	if pgDSN() == "" {
		t.Skip("STOWAGE_TEST_PG_DSN not set — skipping postgres tests")
	}
	s, cleanup := openStore(t) // ensures the schema is migrated
	defer cleanup()
	_ = s

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgDSN())
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	tenant := "race-manual-" + ulid.Make().String()

	// interleave runs the exact CreateView sequence (existence pre-check,
	// then insert one disjoint topic_key row per transaction) for two
	// transactions under the given isolation level, with the pre-checks and
	// inserts interleaved in program order (both pre-checks BEFORE either
	// insert, both inserts BEFORE either commit) — the textbook TOCTOU
	// interleaving.
	interleave := func(t *testing.T, isoLevel pgx.TxIsoLevel, subjectID string) (commit1, commit2 error) {
		t.Helper()
		tx1, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isoLevel})
		if err != nil {
			t.Fatalf("begin tx1: %v", err)
		}
		defer func() { _ = tx1.Rollback(ctx) }()
		tx2, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isoLevel})
		if err != nil {
			t.Fatalf("begin tx2: %v", err)
		}
		defer func() { _ = tx2.Rollback(ctx) }()

		existsSQL := `SELECT count(*) FROM topic_views WHERE tenant_id=$1 AND subject_kind='agent' AND subject_id=$2 AND view_name='work'`
		var n1, n2 int
		if err := tx1.QueryRow(ctx, existsSQL, tenant, subjectID).Scan(&n1); err != nil {
			t.Fatalf("tx1 exists check: %v", err)
		}
		if err := tx2.QueryRow(ctx, existsSQL, tenant, subjectID).Scan(&n2); err != nil {
			t.Fatalf("tx2 exists check: %v", err)
		}
		if n1 != 0 || n2 != 0 {
			t.Fatalf("both pre-checks must see zero rows (neither transaction has committed yet): n1=%d n2=%d", n1, n2)
		}

		now := time.Now().UnixMilli()
		insertSQL := `INSERT INTO topic_views (id, tenant_id, subject_kind, subject_id, view_name, topic_key, effect, created_at, updated_at)
			VALUES ($1,$2,'agent',$3,'work',$4,'allow',$5,$5)`
		if _, err := tx1.Exec(ctx, insertSQL, ulid.Make().String(), tenant, subjectID, "topic-a", now); err != nil {
			t.Fatalf("tx1 insert: %v", err)
		}
		if _, err := tx2.Exec(ctx, insertSQL, ulid.Make().String(), tenant, subjectID, "topic-b", now); err != nil {
			t.Fatalf("tx2 insert: %v", err)
		}

		commit1 = tx1.Commit(ctx)
		commit2 = tx2.Commit(ctx)
		return commit1, commit2
	}

	t.Run("ReadCommitted_bothCommit_reproducesThePreFixBug", func(t *testing.T) {
		c1, c2 := interleave(t, pgx.ReadCommitted, "agent-manual-rc")
		if c1 != nil || c2 != nil {
			t.Fatalf("under READ COMMITTED both disjoint-topic-key transactions were expected to commit cleanly (the vulnerability this fix closes): c1=%v c2=%v", c1, c2)
		}
		// Confirm the silent merge: TWO topic keys under one natural key.
		got, err := s.TopicViews().GetView(ctx, identity.Scope{Tenant: tenant}, "agent", "agent-manual-rc", "work")
		if err != nil {
			t.Fatalf("GetView: %v", err)
		}
		if len(got.AllowTopics) != 2 {
			t.Fatalf("expected the READ COMMITTED race to merge 2 disjoint keys into one view (the bug), got %d: %v", len(got.AllowTopics), got.AllowTopics)
		}
	})

	t.Run("Serializable_oneAborts_provesTheFix", func(t *testing.T) {
		c1, c2 := interleave(t, pgx.Serializable, "agent-manual-ser")
		succeeded, aborted := c1, c2
		switch {
		case c1 == nil && c2 != nil:
			succeeded, aborted = c1, c2
		case c1 != nil && c2 == nil:
			succeeded, aborted = c2, c1
		default:
			t.Fatalf("expected exactly one transaction to succeed and one to abort under SERIALIZABLE: c1=%v c2=%v", c1, c2)
		}
		if succeeded != nil {
			t.Fatalf("the winning transaction must commit cleanly: %v", succeeded)
		}
		if !isSerializationFailure(aborted) {
			t.Fatalf("the losing transaction must fail with SQLSTATE 40001 (serialization_failure), got: %v", aborted)
		}
		// Confirm no merge: exactly ONE topic key survived.
		got, err := s.TopicViews().GetView(ctx, identity.Scope{Tenant: tenant}, "agent", "agent-manual-ser", "work")
		if err != nil {
			t.Fatalf("GetView: %v", err)
		}
		if len(got.AllowTopics) != 1 {
			t.Fatalf("expected SERIALIZABLE to prevent the merge (exactly 1 topic key), got %d: %v", len(got.AllowTopics), got.AllowTopics)
		}
	})
}

// TestCreateView_ConcurrentDisjointTopicKeys_OneWins races the real
// store.TopicViewStore.CreateView API (actual goroutines, no manual
// interleaving) for the same natural key with disjoint topic keys, as a
// best-effort end-to-end confirmation alongside the deterministic proof
// above.
func TestCreateView_ConcurrentDisjointTopicKeys_OneWins(t *testing.T) {
	if pgDSN() == "" {
		t.Skip("STOWAGE_TEST_PG_DSN not set — skipping postgres tests")
	}
	s, cleanup := openStore(t)
	defer cleanup()

	ctx := context.Background()
	scope := identity.Scope{Tenant: "race-tenant-" + ulid.Make().String()}
	subjectID := "agent-race-1"

	const attempts = 8
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	var wgStart sync.WaitGroup
	wgStart.Add(1)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			wgStart.Wait() // maximize actual overlap
			errs[i] = s.TopicViews().CreateView(ctx, scope, store.TopicView{
				SubjectKind: "agent", SubjectID: subjectID, ViewName: "work",
				AllowTopics: []string{ulid.Make().String()}, // disjoint per goroutine
			})
		}(i)
	}
	wgStart.Done()
	wg.Wait()

	successes, conflicts, other := 0, 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, store.ErrConflict):
			conflicts++
		default:
			other++
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 {
		t.Errorf("successes: got %d want exactly 1 (TOCTOU: concurrent disjoint-topic-key creates must not both win)", successes)
	}
	if conflicts != attempts-1 {
		t.Errorf("conflicts: got %d want %d", conflicts, attempts-1)
	}
	if other != 0 {
		t.Errorf("unexpected non-conflict errors: %d", other)
	}

	// The view must carry exactly ONE topic key (the winner's), never a
	// silent merge of every goroutine's disjoint AllowTopics.
	got, err := s.TopicViews().GetView(ctx, scope, "agent", subjectID, "work")
	if err != nil {
		t.Fatalf("GetView after race: %v", err)
	}
	if len(got.AllowTopics) != 1 {
		t.Errorf("AllowTopics after race: got %d keys want 1 (no silent multi-writer merge): %v", len(got.AllowTopics), got.AllowTopics)
	}
}
