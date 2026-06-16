package reconcile

// rollback.go — the exported reversibility core (D-070).
//
// Reconciliation reversibility (D-017/D-064) and pending-confirmation
// resolution (D-065) are single-user capabilities that must behave identically
// on every surface {embedded SDK, HTTP, MCP}. This file is the one logic core
// all three call; the surfaces are thin callers (D-067, one-core-many-surfaces).
//
// The orchestration here was lifted verbatim out of internal/api (the Phase 18
// handlers) — it is BEHAVIOR-PRESERVING: the same newest-event-inverse walk,
// the same merge all-or-nothing semantics, the same conflict guards, the same
// emitted memory.rolled_back / memory.superseded payloads. Surfaces map the
// typed errors below to their transport (HTTP 409/404, MCP tool error, SDK err).
//
// Atomicity: every mutation rides the existing single-transaction
// Memories().Commit unit (D-045). No new transactional surface is introduced.
// The functions are stateless (scope + store passed in) and safe for concurrent
// use.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
)

// invalidateScopes busts the retrieval cache for scope on every non-nil
// invalidator passed by a surface. Pushing invalidation into the reconcile core
// (rather than each surface doing it after the call) means NO surface — HTTP,
// MCP, or the embedded SDK — can forget it (D-053; D-070 Wave-B checkpoint).
// It is variadic + nil-safe so callers pass their cache (or nothing) uniformly.
func invalidateScopes(scope identity.Scope, invs []ScopeInvalidator) {
	for _, inv := range invs {
		if inv != nil {
			inv.InvalidateScope(scope)
		}
	}
}

// ─── typed conflict errors ────────────────────────────────────────────────────

// ConflictError is a typed reversibility conflict the surfaces map 1:1 onto a
// transport status (HTTP 409). Code is the stable machine code the API has
// emitted since Phase 18 (behavior-preserving); Msg is the human "error" string.
// Is() compares by Code so dynamic-message variants (incomplete_snapshots names
// the offending sibling) still match the sentinel via errors.Is.
type ConflictError struct {
	Code string
	Msg  string
}

func (e *ConflictError) Error() string { return e.Msg }

// Is reports whether target is a ConflictError with the same Code.
func (e *ConflictError) Is(target error) bool {
	t, ok := target.(*ConflictError)
	return ok && t.Code == e.Code
}

// Conflict sentinels. The Code strings are the Phase-18 wire codes (D-064/D-065)
// and must not change — existing api tests and clients key off them.
var (
	// ErrAlreadyRolledBack — a memory.rolled_back event is newer than the newest
	// restorable event (double-rollback guard).
	ErrAlreadyRolledBack = &ConflictError{
		Code: "already_rolled_back",
		Msg:  "already_rolled_back: memory has already been rolled back",
	}
	// ErrNoPriorState — no restorable event found at all.
	ErrNoPriorState = &ConflictError{
		Code: "no_prior_state",
		Msg:  "no_prior_state: no restorable prior state found in event log",
	}
	// ErrInvalidPriorState — snapshot payload fails to parse or prior.ID != id.
	ErrInvalidPriorState = &ConflictError{
		Code: "invalid_prior_state",
		Msg:  "invalid_prior_state: snapshot payload is missing or has wrong id",
	}
	// ErrDownstreamSupersede — the result/digest row has been modified downstream;
	// the chain must unwind newest-first.
	ErrDownstreamSupersede = &ConflictError{
		Code: "downstream_conflict",
		Msg:  "downstream_conflict: result row has been modified; unwind the chain newest-first",
	}
	// ErrIncompleteSnapshots — at least one merge sibling lacks a parseable snapshot.
	ErrIncompleteSnapshots = &ConflictError{
		Code: "incomplete_snapshots",
		Msg:  "incomplete_snapshots: a merge sibling lacks a parseable snapshot",
	}
	// ErrNotParked — Resolve called on a memory that is not pending_confirmation.
	ErrNotParked = &ConflictError{
		Code: "not_parked",
		Msg:  "not_parked: memory is not pending confirmation",
	}
)

// ─── results ──────────────────────────────────────────────────────────────────

// RollbackResult is the outcome of Rollback. Memory is the restored row
// re-fetched after commit (nil only if the post-commit re-read failed, in which
// case ID still identifies the rolled-back memory).
type RollbackResult struct {
	ID     string
	Memory *store.Memory
}

// ConfirmAction is the resolution applied to a pending_confirmation memory.
type ConfirmAction string

const (
	// ConfirmActionConfirm promotes the parked memory to active (supersede path).
	ConfirmActionConfirm ConfirmAction = "confirm"
	// ConfirmActionReject expires the parked memory (status → 'expired').
	ConfirmActionReject ConfirmAction = "reject"
)

// ResolveResult is the outcome of Resolve. Invalidate is true when the caller
// should bust its retrieval cache (confirm promotes a row into active set);
// reject only expires the parked row and does not change the active set.
type ResolveResult struct {
	ID         string
	Status     string
	Invalidate bool
}

// ProvRef is a compact provenance reference in a MemoryView.
type ProvRef struct {
	RecordID  string
	SpanStart int
	SpanEnd   int
}

// MemoryView is the full read of a memory: the row, its junction rows, its
// provenance references, and up to 10 supersedes-chain ancestors (parent-first).
// Surfaces map it to their wire format (HTTP memoryResponse, SDK GetMemoryResponse).
type MemoryView struct {
	Memory          store.Memory
	Entities        []string
	Keywords        []string
	Queries         []string
	Provenance      []ProvRef
	SupersedesChain []string
}

// ─── GetMemory ────────────────────────────────────────────────────────────────

// GetMemory reads a memory, its junctions, and its supersedes chain within
// scope. Returns store.ErrNotFound (wrapped) when the memory is absent. A
// junction-read error is non-fatal: the view is returned with empty junctions
// (matches the pre-h3 HTTP handler behavior).
func GetMemory(ctx context.Context, st store.Store, scope identity.Scope, id string) (*MemoryView, error) {
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	view := &MemoryView{Memory: *mem}

	jt, jerr := st.Memories().GetJunctions(ctx, scope, id)
	if jerr == nil {
		view.Entities = jt.Entities
		view.Keywords = jt.Keywords
		view.Queries = jt.Queries
		for _, p := range jt.Provenance {
			view.Provenance = append(view.Provenance, ProvRef{
				RecordID:  p.RecordID,
				SpanStart: p.SpanStart,
				SpanEnd:   p.SpanEnd,
			})
		}
	} else {
		// Non-fatal: the view is still returned with empty junctions, but the
		// error is surfaced rather than silently swallowed (matches the Phase-18
		// HTTP handler's slog warning; Wave-B checkpoint NIT).
		slog.Default().WarnContext(ctx, "reconcile: get_memory junctions read failed",
			"memory_id", id, "err", jerr)
	}

	view.SupersedesChain = walkSupersedesChain(ctx, st, scope, mem.SupersedesID, 10)
	return view, nil
}

// walkSupersedesChain follows supersedes_id links from startID up to maxDepth
// hops, returning the collected IDs in walk order (parent-first). Stops on a
// cycle, a missing ancestor, or a scope mismatch.
func walkSupersedesChain(ctx context.Context, st store.Store, scope identity.Scope, startID string, maxDepth int) []string {
	if startID == "" || maxDepth <= 0 {
		return nil
	}
	seen := make(map[string]bool)
	var chain []string
	cur := startID
	for i := 0; i < maxDepth && cur != ""; i++ {
		if seen[cur] {
			break // cycle guard
		}
		seen[cur] = true
		chain = append(chain, cur)
		ancestor, err := st.Memories().Get(ctx, scope, cur)
		if err != nil {
			break // ancestor gone or scope mismatch — stop walking
		}
		cur = ancestor.SupersedesID
	}
	return chain
}

// ─── Rollback ─────────────────────────────────────────────────────────────────

// Rollback inverts the NEWEST reconciliation event for memory id within scope
// (D-064: newest-event-only, atomic, tombstone=deleted, merge all-or-nothing).
//
// Returns store.ErrNotFound when id is absent, or a *ConflictError
// (ErrAlreadyRolledBack, ErrNoPriorState, ErrInvalidPriorState,
// ErrDownstreamSupersede, ErrIncompleteSnapshots) on a guard violation. The
// emitted memory.rolled_back event(s) carry the full pre-rollback prior state
// so the inverse is itself reversible.
//
// A rollback always changes the active memory set, so on success the optional
// ScopeInvalidator(s) are invalidated in the core (D-053) — every surface passes
// its retrieval cache (or nothing) and none invalidates separately.
func Rollback(ctx context.Context, st store.Store, scope identity.Scope, id string, inv ...ScopeInvalidator) (*RollbackResult, error) {
	res, err := rollback(ctx, st, scope, id)
	if err != nil {
		return nil, err
	}
	invalidateScopes(scope, inv)
	return res, nil
}

// rollback is the unexported reversibility core (invalidation-free); the
// exported Rollback wraps it to invalidate the retrieval cache once on success.
func rollback(ctx context.Context, st store.Store, scope identity.Scope, id string) (*RollbackResult, error) {
	// Fetch current memory state (404 guard + pre-rollback snapshot source).
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}

	// Scan event log (newest-first, limit 50).
	events, err := st.Events().ListBySubject(ctx, scope, id, 50)
	if err != nil {
		return nil, fmt.Errorf("reconcile: rollback list events: %w", err)
	}

	// Walk newest-first. The first restorable event wins; if a rolled_back
	// event appears before any restorable event the call is a double-rollback.
	var invertibleEvent *store.Event
	for i := range events {
		ev := &events[i]
		if ev.Type == "memory.rolled_back" {
			return nil, ErrAlreadyRolledBack
		}
		if isRestorable(ev.Type) {
			invertibleEvent = ev
			break
		}
	}
	if invertibleEvent == nil {
		return nil, ErrNoPriorState
	}

	// Parse the prior-state snapshot.
	prior, parseErr := parsePriorState(invertibleEvent.Payload)
	if parseErr != nil || prior.ID != id {
		return nil, ErrInvalidPriorState
	}

	now := time.Now().UnixMilli()

	switch invertibleEvent.Type {
	case "memory.updated":
		// Restore {id} in place from snapshot. No tombstone.
		return commitSimpleRollback(ctx, st, scope, id, mem, prior, now, "reconcile: rollback updated op")

	case "memory.superseded":
		return rollbackSuperseded(ctx, st, scope, id, mem, prior, now)

	case "memory.merged":
		return rollbackMerged(ctx, st, scope, id, mem, prior, now)
	}

	// Unreachable: isRestorable gates the switch.
	return nil, ErrNoPriorState
}

// isRestorable reports whether evType is an invertible reconciliation event (D-064).
func isRestorable(evType string) bool {
	return evType == "memory.updated" || evType == "memory.merged" || evType == "memory.superseded"
}

// commitSimpleRollback handles the in-place restore (updated event, or a
// superseded/merged event with no recorded result row). No tombstone.
func commitSimpleRollback(ctx context.Context, st store.Store, scope identity.Scope, id string, mem *store.Memory, prior *priorStateJSON, now int64, reason string) (*RollbackResult, error) {
	preJT, _ := st.Memories().GetJunctions(ctx, scope, id)
	cs := store.CommitSet{
		Action:     store.ActionRollback,
		Memory:     prior.toMemory(now),
		Entities:   prior.Entities,
		Keywords:   prior.Keywords,
		Queries:    prior.Queries,
		Provenance: prior.provenanceRows(scope, now),
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: id,
			Reason:    reason,
			Payload:   MarshalPriorState(*mem, preJT),
			CreatedAt: now,
		}},
		Scope: scope,
	}
	return doCommitRollback(ctx, st, scope, id, cs)
}

// rollbackSuperseded inverts a memory.superseded event: restore {id}, tombstone
// the result row (mem.SupersededByID) when present and still active.
func rollbackSuperseded(ctx context.Context, st store.Store, scope identity.Scope, id string, mem *store.Memory, prior *priorStateJSON, now int64) (*RollbackResult, error) {
	if mem.SupersededByID == "" {
		// No result row to tombstone; plain restore of {id}.
		return commitSimpleRollback(ctx, st, scope, id, mem, prior, now, "reconcile: rollback superseded op")
	}
	resultRow, fetchErr := st.Memories().Get(ctx, scope, mem.SupersededByID)
	if fetchErr != nil && !errors.Is(fetchErr, store.ErrNotFound) {
		return nil, fmt.Errorf("reconcile: rollback fetch result row: %w", fetchErr)
	}
	if fetchErr == nil && resultRow.Status != "active" {
		return nil, ErrDownstreamSupersede
	}

	preJT, _ := st.Memories().GetJunctions(ctx, scope, id)
	cs := store.CommitSet{
		Action:     store.ActionRollback,
		Memory:     prior.toMemory(now),
		Entities:   prior.Entities,
		Keywords:   prior.Keywords,
		Queries:    prior.Queries,
		Provenance: prior.provenanceRows(scope, now),
		Events: []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: id,
			Reason:    "reconcile: rollback superseded op",
			Payload:   MarshalPriorState(*mem, preJT),
			CreatedAt: now,
		}},
		Scope: scope,
	}
	if fetchErr == nil {
		cs.Targets = []store.Memory{*resultRow}
	}
	return doCommitRollback(ctx, st, scope, id, cs)
}

// rollbackMerged inverts a memory.merged event: restore ALL siblings (same
// superseded_by_id) atomically in ONE CommitSet and tombstone the digest. Every
// sibling must carry its own parseable snapshot (all-or-nothing).
func rollbackMerged(ctx context.Context, st store.Store, scope identity.Scope, id string, mem *store.Memory, prior *priorStateJSON, now int64) (*RollbackResult, error) {
	supersederID := mem.SupersededByID
	if supersederID == "" {
		// No superseder recorded; restore {id} alone.
		return commitSimpleRollback(ctx, st, scope, id, mem, prior, now, "reconcile: rollback merged op")
	}

	// Downstream conflict guard on the digest.
	digestRow, fetchErr := st.Memories().Get(ctx, scope, supersederID)
	if fetchErr != nil && !errors.Is(fetchErr, store.ErrNotFound) {
		return nil, fmt.Errorf("reconcile: rollback fetch digest: %w", fetchErr)
	}
	if fetchErr == nil && digestRow.Status != "active" {
		return nil, &ConflictError{
			Code: "downstream_conflict",
			Msg:  "downstream_conflict: digest has been modified; unwind newest-first",
		}
	}

	// Discover ALL siblings (including {id}).
	allSiblings, listErr := st.Memories().ListSupersededBy(ctx, scope, supersederID)
	if listErr != nil {
		return nil, fmt.Errorf("reconcile: rollback list siblings: %w", listErr)
	}
	if len(allSiblings) == 0 {
		allSiblings = []store.Memory{*mem}
	}

	// Each sibling needs its own prior-state snapshot. Collect them.
	type siblingSnap struct {
		mem   store.Memory
		prior *priorStateJSON
		jt    store.MemoryJunctions
	}
	var snaps []siblingSnap
	for _, sib := range allSiblings {
		sibEvents, evErr := st.Events().ListBySubject(ctx, scope, sib.ID, 50)
		if evErr != nil {
			return nil, fmt.Errorf("reconcile: rollback list sibling events: %w", evErr)
		}
		var sibPrior *priorStateJSON
		for _, ev := range sibEvents {
			if ev.Type == "memory.merged" {
				p, pErr := parsePriorState(ev.Payload)
				if pErr == nil && p.ID == sib.ID {
					sibPrior = p
					break
				}
			}
		}
		if sibPrior == nil {
			return nil, &ConflictError{
				Code: "incomplete_snapshots",
				Msg:  "incomplete_snapshots: sibling " + sib.ID + " lacks a parseable snapshot",
			}
		}
		jt, _ := st.Memories().GetJunctions(ctx, scope, sib.ID)
		snaps = append(snaps, siblingSnap{mem: sib, prior: sibPrior, jt: jt})
	}

	// Build CommitSet. Primary = first sibling; extras = the rest.
	primary := snaps[0]
	events := []store.Event{{
		ID:        ulid.Make().String(),
		Type:      "memory.rolled_back",
		SubjectID: primary.mem.ID,
		Reason:    "reconcile: rollback merged op",
		Payload:   MarshalPriorState(primary.mem, primary.jt),
		CreatedAt: now,
	}}
	var extras []store.RollbackMemory
	for _, snap := range snaps[1:] {
		extras = append(extras, store.RollbackMemory{
			Memory:     snap.prior.toMemory(now),
			Entities:   snap.prior.Entities,
			Keywords:   snap.prior.Keywords,
			Queries:    snap.prior.Queries,
			Provenance: snap.prior.provenanceRows(scope, now),
		})
		events = append(events, store.Event{
			ID:        ulid.Make().String(),
			Type:      "memory.rolled_back",
			SubjectID: snap.mem.ID,
			Reason:    "reconcile: rollback merged op (sibling)",
			Payload:   MarshalPriorState(snap.mem, snap.jt),
			CreatedAt: now,
		})
	}

	cs := store.CommitSet{
		Action:        store.ActionRollback,
		Memory:        primary.prior.toMemory(now),
		Entities:      primary.prior.Entities,
		Keywords:      primary.prior.Keywords,
		Queries:       primary.prior.Queries,
		Provenance:    primary.prior.provenanceRows(scope, now),
		ExtraMemories: extras,
		Events:        events,
		Scope:         scope,
	}
	if fetchErr == nil {
		cs.Targets = []store.Memory{*digestRow}
	}
	return doCommitRollback(ctx, st, scope, id, cs)
}

// doCommitRollback executes the commit and re-reads the restored memory.
func doCommitRollback(ctx context.Context, st store.Store, scope identity.Scope, id string, cs store.CommitSet) (*RollbackResult, error) {
	if err := st.Memories().Commit(ctx, scope, cs); err != nil {
		return nil, fmt.Errorf("reconcile: rollback commit: %w", err)
	}
	res := &RollbackResult{ID: id}
	if mem, err := st.Memories().Get(ctx, scope, id); err == nil {
		res.Memory = mem
	}
	return res, nil
}

// ─── Resolve (confirm / reject) ───────────────────────────────────────────────

// Resolve applies confirm|reject to a pending_confirmation memory (D-065).
// Confirm promotes the parked memory to active and supersedes its target,
// riding the supersede path so the resolution is itself reversible via Rollback;
// reject expires the parked memory. Returns store.ErrNotFound when id is absent,
// ErrNotParked when the memory is not pending_confirmation, or a plain error for
// an unrecognised action.
//
// Confirm promotes a row into the active set, so on a confirm (res.Invalidate)
// the optional ScopeInvalidator(s) are invalidated in the core (D-053); reject
// only expires the parked row and never invalidates. Every surface passes its
// cache (or nothing) and none invalidates separately.
func Resolve(ctx context.Context, st store.Store, scope identity.Scope, id string, action ConfirmAction, inv ...ScopeInvalidator) (*ResolveResult, error) {
	res, err := resolve(ctx, st, scope, id, action)
	if err != nil {
		return nil, err
	}
	if res.Invalidate {
		invalidateScopes(scope, inv)
	}
	return res, nil
}

// resolve is the unexported confirm/reject core (invalidation-free); the
// exported Resolve wraps it to invalidate the retrieval cache on a confirm.
func resolve(ctx context.Context, st store.Store, scope identity.Scope, id string, action ConfirmAction) (*ResolveResult, error) {
	mem, err := st.Memories().Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}
	if mem.Status != "pending_confirmation" {
		return nil, ErrNotParked
	}

	now := time.Now().UnixMilli()

	switch action {
	case ConfirmActionConfirm:
		// Promote parked → active via the supersede path. The target's
		// memory.superseded event carries MarshalPriorState so the promotion is
		// itself reversible via D-064 rollback.
		promoted := *mem
		promoted.Status = "active"
		promoted.UpdatedAt = now

		var targets []store.Memory
		var targetJT store.MemoryJunctions
		if mem.SupersedesID != "" {
			target, fetchErr := st.Memories().Get(ctx, scope, mem.SupersedesID)
			if fetchErr == nil {
				targets = append(targets, *target)
				targetJT, _ = st.Memories().GetJunctions(ctx, scope, target.ID)
			} else if !errors.Is(fetchErr, store.ErrNotFound) {
				return nil, fmt.Errorf("reconcile: confirm fetch target: %w", fetchErr)
			}
		}

		events := []store.Event{{
			ID:        ulid.Make().String(),
			Type:      "memory.confirmed",
			SubjectID: id,
			Reason:    "reconcile: manually confirmed",
			Payload:   "{}",
			CreatedAt: now,
		}}
		if len(targets) > 0 {
			events = append(events, store.Event{
				ID:        ulid.Make().String(),
				Type:      "memory.superseded",
				SubjectID: targets[0].ID,
				Reason:    "reconcile: superseded on confirm (D-065)",
				Payload:   MarshalPriorState(targets[0], targetJT),
				CreatedAt: now,
			})
		}

		cs := store.CommitSet{
			Action:  store.ActionConfirm,
			Memory:  promoted,
			Targets: targets,
			Events:  events,
			Scope:   scope,
		}
		if err := st.Memories().Commit(ctx, scope, cs); err != nil {
			return nil, fmt.Errorf("reconcile: confirm commit: %w", err)
		}
		return &ResolveResult{ID: id, Status: "active", Invalidate: true}, nil

	case ConfirmActionReject:
		// Expire the parked memory (status → 'expired', per D-065).
		rejected := *mem
		rejected.Status = "expired"
		rejected.UpdatedAt = now

		cs := store.CommitSet{
			Action: store.ActionConfirm,
			Memory: rejected,
			Events: []store.Event{{
				ID:        ulid.Make().String(),
				Type:      "memory.rejected",
				SubjectID: id,
				Reason:    "reconcile: manually rejected",
				Payload:   "{}",
				CreatedAt: now,
			}},
			Scope: scope,
		}
		if err := st.Memories().Commit(ctx, scope, cs); err != nil {
			return nil, fmt.Errorf("reconcile: reject commit: %w", err)
		}
		return &ResolveResult{ID: id, Status: "expired", Invalidate: false}, nil

	default:
		return nil, fmt.Errorf("reconcile: resolve: invalid action %q (want confirm|reject)", action)
	}
}

// ─── prior-state parsing (D-017) ──────────────────────────────────────────────

// errNoPriorState is the sentinel for an empty/contentless prior-state payload.
var errNoPriorState = errors.New("reconcile: no prior state")

// parsePriorState parses a D-017 prior-state JSON payload (the inverse of
// MarshalPriorState). Returns an error when the payload is empty, malformed, or
// lacks an id/content.
func parsePriorState(payload string) (*priorStateJSON, error) {
	if payload == "" || payload == "{}" {
		return nil, errNoPriorState
	}
	var p priorStateJSON
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return nil, err
	}
	if p.ID == "" || p.Content == "" {
		return nil, errNoPriorState
	}
	return &p, nil
}

// toMemory converts a parsed prior state to a store.Memory for restore,
// stamping updatedAt as the rollback time.
func (p *priorStateJSON) toMemory(updatedAt int64) store.Memory {
	return store.Memory{
		ID:          p.ID,
		Kind:        p.Kind,
		Content:     p.Content,
		Context:     p.Context,
		Status:      p.Status,
		Importance:  p.Importance,
		Confidence:  p.Confidence,
		TrustSource: p.TrustSource,
		MatchCount:  p.MatchCount,
		InjectCount: p.InjectCount,
		UseCount:    p.UseCount,
		SaveCount:   p.SaveCount,
		FailCount:   p.FailCount,
		NoiseCount:  p.NoiseCount,
		Stability:   p.Stability,
		ValidFrom:   p.ValidFrom,
		ValidUntil:  p.ValidUntil,
		EpisodeID:   p.EpisodeID,
		PrivacyZone: p.PrivacyZone,
		ContentHash: p.ContentHash,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   updatedAt,
	}
}

// provenanceRows converts the prior state's provenance references to
// store.Provenance rows for a CommitSet.
func (p *priorStateJSON) provenanceRows(scope identity.Scope, now int64) []store.Provenance {
	var rows []store.Provenance
	for _, pref := range p.Provenance {
		rows = append(rows, store.Provenance{
			ID:        ulid.Make().String(),
			MemoryID:  p.ID,
			RecordID:  pref.RecordID,
			SpanStart: pref.SpanStart,
			SpanEnd:   pref.SpanEnd,
			TenantID:  scope.Tenant,
			CreatedAt: now,
		})
	}
	return rows
}
