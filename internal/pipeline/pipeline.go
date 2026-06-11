// Package pipeline implements the Stowage buffer stage (Phase 06).
//
// Ingested record IDs flow from the API ingest channel into N stage workers.
// Each worker appends a durable BufferItem to the store, then evaluates count
// and token triggers under a per-buffer keyed mutex. A jittered 5-second ticker
// scans for aged items and drives crash recovery. Flushes are exactly-once at
// the store layer (BufferStore.Flush is atomic). Downstream consumers receive
// FlushedBuffer events on a typed channel.
//
// Concurrency posture: per-buffer keyed mutex serialises trigger evaluation so
// two workers cannot race on the same buffer. The global stage mutex is held
// only for map insert/delete operations.
//
// Shutdown: closing the ingest channel drains in-flight items; the ticker is
// stopped via an internal channel. Items already durable in the store are
// recovered by the age scan on next start (crash recovery).
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// Trigger reason constants (D-042).
const (
	TriggerCount         = "count"
	TriggerTokens        = "tokens"
	TriggerAge           = "age"
	TriggerExplicit      = "explicit"
	TriggerSessionEnd    = "session_end"
	TriggerBranchDiscard = "branch_discard"
)

// stageWorkers is the fixed number of worker goroutines per Stage.
// Not a top-level config knob (D-034 guardrail).
const stageWorkers = 4

// scanLimit caps the number of items returned per ScanAged tick.
const scanLimit = 1000

// downstreamCap is the FlushedBuffer channel buffer size.
const downstreamCap = 256

// Item is sent on the pipeline ingest channel for each durably-written record.
type Item struct {
	RecordID  string // ULID
	TenantID  string // tenant scope for O(1) record lookup
	BufferKey string // "" means derive from (session_id + "/" + branch_id)
	// SessionID and BranchID carry the ingest-payload values so the buffer
	// key can be derived correctly even though the records store persists
	// session_id under scope.Session (which the API writes as empty).
	SessionID string
	BranchID  string
}

// FlushedBuffer is emitted on the downstream channel when a buffer flushes.
// It is also the shape recorded in the buffer.flushed audit event payload.
type FlushedBuffer struct {
	Scope         identity.Scope `json:"scope"`
	Key           string         `json:"key"`
	BranchID      string         `json:"branch_id"`
	RecordIDs     []string       `json:"record_ids"`
	TokenEstimate int64          `json:"token_estimate"`
	Trigger       string         `json:"trigger"`
	SkipPromotion bool           `json:"skip_promotion"`
}

// bufState is the in-memory state for one buffer key.
// Fields are only accessed while the per-key keyMutex is held.
type bufState struct {
	count  int
	tokens int64
	oldest int64 // unix millis of oldest unflushed item; 0 = empty
}

// branchEntry records the relationship between a branch and a buffer key.
type branchEntry struct {
	scope     identity.Scope
	bufferKey string
}

// Stage is the buffer pipeline stage.
type Stage struct {
	st       store.Store
	log      *slog.Logger
	triggers Triggers

	in  <-chan Item
	out chan FlushedBuffer

	// per-buffer keyed mutex and state
	km      *keyMutex
	stateMu sync.Mutex
	states  map[string]*bufState // bufferKey → state; updated under km.Lock(key)

	// branch → buffer key tracking (for FlushBranch)
	branchMu   sync.Mutex
	branchKeys map[string][]branchEntry // branchID → entries

	stopCh chan struct{} // closed to stop the ticker
	wg     sync.WaitGroup
}

// New creates a new Stage. Call Start to begin processing.
func New(st store.Store, log *slog.Logger, trig Triggers, in <-chan Item) *Stage {
	return &Stage{
		st:         st,
		log:        log,
		triggers:   trig,
		in:         in,
		out:        make(chan FlushedBuffer, downstreamCap),
		km:         newKeyMutex(),
		states:     make(map[string]*bufState),
		branchKeys: make(map[string][]branchEntry),
		stopCh:     make(chan struct{}),
	}
}

// Downstream returns the read end of the FlushedBuffer channel.
// Phase 07 replaces the no-op consumer with the extraction stage.
func (s *Stage) Downstream() <-chan FlushedBuffer { return s.out }

// Start launches stageWorkers worker goroutines plus one ticker goroutine.
// ctx is used only for logging; workers drain until the ingest channel is closed.
func (s *Stage) Start(ctx context.Context) {
	for i := 0; i < stageWorkers; i++ {
		s.wg.Add(1)
		go s.runWorker(ctx)
	}
	s.wg.Add(1)
	go s.runTicker(ctx)
}

// Drain waits for all workers and the ticker to finish, then closes the
// downstream channel. Call after the ingest channel has been closed.
func (s *Stage) Drain(ctx context.Context) {
	// Signal the ticker to stop.
	close(s.stopCh)

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		s.log.WarnContext(ctx, "pipeline: drain timed out")
	}
	close(s.out)
}

// FlushBranch flushes all in-memory-tracked buffers associated with branchID
// using trigger "branch_discard" and SkipPromotion=true. Called by the branch
// discard handler (Phase 05 hook).
func (s *Stage) FlushBranch(ctx context.Context, branchID string) {
	s.branchMu.Lock()
	entries := make([]branchEntry, len(s.branchKeys[branchID]))
	copy(entries, s.branchKeys[branchID])
	s.branchMu.Unlock()

	for _, e := range entries {
		if err := s.flushKey(ctx, e.scope, e.bufferKey, TriggerBranchDiscard); err != nil {
			s.log.WarnContext(ctx, "pipeline: branch flush error",
				"branch_id", branchID, "key", e.bufferKey, "err", err)
		}
	}
}

// FlushKey explicitly flushes a named buffer key for the given scope and trigger.
// Returns nil if the buffer is empty.
func (s *Stage) FlushKey(ctx context.Context, scope identity.Scope, bufferKey, trigger string) error {
	return s.flushKey(ctx, scope, bufferKey, trigger)
}

// ----------------------------------------------------------------------------
// internal workers
// ----------------------------------------------------------------------------

func (s *Stage) runWorker(ctx context.Context) {
	defer s.wg.Done()
	for item := range s.in {
		if err := s.processItem(ctx, item); err != nil {
			s.log.WarnContext(ctx, "pipeline: worker error", "record_id", item.RecordID, "err", err)
		}
	}
}

func (s *Stage) processItem(ctx context.Context, item Item) error {
	// Look up the record to get scope and session/branch info.
	rec, err := s.lookupRecord(ctx, item.TenantID, item.RecordID)
	if err != nil {
		return fmt.Errorf("lookup record %q: %w", item.RecordID, err)
	}
	scope := identity.Scope{Tenant: rec.TenantID}

	// Resolve buffer key: explicit hint, then derive from (session_id, branch_id).
	// Prefer values carried on the Item (forwarded from the ingest payload) over
	// the store-scanned values, because records are written with a tenant-only
	// scope so session_id is stored as NULL in the records table.
	bufferKey := item.BufferKey
	if bufferKey == "" {
		sessID := item.SessionID
		if sessID == "" {
			sessID = rec.SessionID
		}
		branchID := item.BranchID
		if branchID == "" {
			branchID = rec.BranchID
		}
		bufferKey = sessID + "/" + branchID
	}

	// Build and append the BufferItem (durable write outside the per-key mutex).
	bi := store.BufferItem{
		ID:            ulid.Make().String(),
		TenantID:      rec.TenantID,
		SessionID:     rec.SessionID,
		BranchID:      rec.BranchID,
		BufferKey:     bufferKey,
		RecordID:      rec.ID,
		TokenEstimate: rec.TokenEstimate,
		CreatedAt:     time.Now().UnixMilli(),
	}
	if err := s.st.Buffers().AppendItem(ctx, scope, bi); err != nil {
		return fmt.Errorf("append buffer item: %w", err)
	}

	// Track branch → bufferKey for FlushBranch.
	if rec.BranchID != "" {
		s.branchMu.Lock()
		entries := s.branchKeys[rec.BranchID]
		found := false
		for _, e := range entries {
			if e.bufferKey == bufferKey {
				found = true
				break
			}
		}
		if !found {
			s.branchKeys[rec.BranchID] = append(entries, branchEntry{scope: scope, bufferKey: bufferKey})
		}
		s.branchMu.Unlock()
	}

	// Acquire per-buffer keyed mutex to evaluate triggers.
	s.km.Lock(bufferKey)
	defer s.km.Unlock(bufferKey)

	st := s.getOrCreateState(bufferKey)
	st.count++
	st.tokens += rec.TokenEstimate
	if st.oldest == 0 || bi.CreatedAt < st.oldest {
		st.oldest = bi.CreatedAt
	}

	// Evaluate count trigger.
	if st.count >= s.triggers.Count {
		return s.doFlushLocked(ctx, scope, bufferKey, rec.BranchID, TriggerCount)
	}
	// Evaluate token trigger.
	if st.tokens >= s.triggers.Tokens {
		return s.doFlushLocked(ctx, scope, bufferKey, rec.BranchID, TriggerTokens)
	}
	return nil
}

func (s *Stage) runTicker(ctx context.Context) {
	defer s.wg.Done()
	for {
		base := s.triggers.TickBase
		if base <= 0 {
			base = 4 * time.Second
		}
		// Jitter up to base/2 so concurrent instances don't scan in lockstep.
		jitter := time.Duration(rand.Int64N(int64(base/2)/1e6+1)) * time.Millisecond //nolint:gosec // non-crypto jitter
		delay := base + jitter

		select {
		case <-s.stopCh:
			return
		case <-time.After(delay):
		}

		s.tickScan(ctx)
	}
}

// tickScan calls ScanAged and flushes any aged buffers.
func (s *Stage) tickScan(ctx context.Context) {
	threshold := time.Now().Add(-s.triggers.MaxAge).UnixMilli()
	items, err := s.st.Buffers().ScanAged(ctx, threshold, scanLimit)
	if err != nil {
		s.log.WarnContext(ctx, "pipeline: tick scan error", "err", err)
		return
	}

	// Group by (scope-key, bufferKey) — a bufferKey is already unique per tenant.
	type flushTarget struct {
		scope     identity.Scope
		bufferKey string
		branchID  string
	}
	seen := make(map[string]flushTarget)
	for _, item := range items {
		k := item.TenantID + "|" + item.BufferKey
		if _, ok := seen[k]; !ok {
			seen[k] = flushTarget{
				scope:     identity.Scope{Tenant: item.TenantID},
				bufferKey: item.BufferKey,
				branchID:  item.BranchID,
			}
		}
	}

	for _, t := range seen {
		if err := s.flushKeyWithBranch(ctx, t.scope, t.bufferKey, t.branchID, TriggerAge); err != nil {
			s.log.WarnContext(ctx, "pipeline: age flush error",
				"key", t.bufferKey, "err", err)
		}
	}
}

// ----------------------------------------------------------------------------
// flush helpers
// ----------------------------------------------------------------------------

// flushKey acquires the per-buffer keyed mutex then calls doFlushLocked.
func (s *Stage) flushKey(ctx context.Context, scope identity.Scope, bufferKey, trigger string) error {
	s.km.Lock(bufferKey)
	defer s.km.Unlock(bufferKey)
	return s.doFlushLocked(ctx, scope, bufferKey, "", trigger)
}

// flushKeyWithBranch acquires the per-buffer keyed mutex then flushes with branch hint.
func (s *Stage) flushKeyWithBranch(ctx context.Context, scope identity.Scope, bufferKey, branchID, trigger string) error {
	s.km.Lock(bufferKey)
	defer s.km.Unlock(bufferKey)
	return s.doFlushLocked(ctx, scope, bufferKey, branchID, trigger)
}

// doFlushLocked performs a store Flush, emits FlushedBuffer, and clears the
// in-memory state. Must be called with the per-buffer keyed mutex held.
func (s *Stage) doFlushLocked(ctx context.Context, scope identity.Scope, bufferKey, branchHint, trigger string) error {
	items, err := s.st.Buffers().Flush(ctx, scope, bufferKey)
	if err != nil {
		return fmt.Errorf("store flush: %w", err)
	}
	if len(items) == 0 {
		s.deleteState(bufferKey)
		return nil
	}

	// Derive branchID from first item if not provided.
	branchID := branchHint
	if branchID == "" && len(items) > 0 {
		branchID = items[0].BranchID
	}

	recordIDs := make([]string, len(items))
	var tokenTotal int64
	for i, it := range items {
		recordIDs[i] = it.RecordID
		tokenTotal += it.TokenEstimate
	}

	fb := FlushedBuffer{
		Scope:         scope,
		Key:           bufferKey,
		BranchID:      branchID,
		RecordIDs:     recordIDs,
		TokenEstimate: tokenTotal,
		Trigger:       trigger,
		SkipPromotion: trigger == TriggerBranchDiscard,
	}

	// Emit audit event.
	payload, _ := json.Marshal(struct {
		Trigger string `json:"trigger"`
		Count   int    `json:"count"`
		Tokens  int64  `json:"tokens"`
	}{Trigger: trigger, Count: len(recordIDs), Tokens: tokenTotal})

	ev := store.Event{
		ID:        ulid.Make().String(),
		Type:      "buffer.flushed",
		SubjectID: bufferKey,
		Reason:    trigger,
		Payload:   string(payload),
		CreatedAt: time.Now().UnixMilli(),
	}
	if evErr := s.st.Events().Emit(ctx, scope, ev); evErr != nil {
		s.log.WarnContext(ctx, "pipeline: emit event error", "err", evErr)
	}

	// Send downstream (non-blocking; drop if consumer is behind).
	select {
	case s.out <- fb:
	default:
		s.log.WarnContext(ctx, "pipeline: downstream channel full; flush event dropped",
			"key", bufferKey, "trigger", trigger)
	}

	// Clear in-memory state.
	s.deleteState(bufferKey)

	return nil
}

// ----------------------------------------------------------------------------
// state map helpers (protected by stateMu; only called while km.Lock(key) held)
// ----------------------------------------------------------------------------

func (s *Stage) getOrCreateState(key string) *bufState {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	st, ok := s.states[key]
	if !ok {
		st = &bufState{}
		s.states[key] = st
	}
	return st
}

func (s *Stage) deleteState(key string) {
	s.stateMu.Lock()
	delete(s.states, key)
	s.stateMu.Unlock()
}

// ----------------------------------------------------------------------------
// record lookup
// ----------------------------------------------------------------------------

// lookupRecord retrieves the store Record for the given ID and tenant.
func (s *Stage) lookupRecord(ctx context.Context, tenantID, recordID string) (*store.Record, error) {
	scope := identity.Scope{Tenant: tenantID}
	rec, err := s.st.Records().Get(ctx, scope, recordID)
	if err != nil {
		return nil, fmt.Errorf("get record %q: %w", recordID, err)
	}
	return rec, nil
}
