package store

import "errors"

// ErrNotFound is returned when a requested entity does not exist in the store.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when an insert would violate a uniqueness constraint.
var ErrConflict = errors.New("store: conflict")

// ErrChecksumMismatch is returned when an already-applied migration's SQL
// content has changed since it was applied.
var ErrChecksumMismatch = errors.New("store: migration checksum mismatch")

// ErrDriverNotRegistered is returned by Open when no factory has been
// registered for the requested driver name.
var ErrDriverNotRegistered = errors.New("store: driver not registered")

// ErrScopeRequired is returned when a scoped store method is called with an
// empty Tenant field. The store layer fails closed (P3): no query is issued.
var ErrScopeRequired = errors.New("store: scope tenant is required")

// ErrClosed is returned when a write is attempted on a store that has already
// been closed (W1 guard).
var ErrClosed = errors.New("store: store is closed")

// ErrBadCursor is returned when a pagination cursor cannot be parsed.
var ErrBadCursor = errors.New("store: bad cursor")

// ErrDuplicateContent is returned by Commit (ActionAdd/ActionPark) when the
// content_hash unique constraint fires — i.e., a concurrent goroutine already
// committed an identical-hash memory. Reconcile handles this as exact-dedup:
// IncrementCounter("match") on the existing row, candidate discarded (m7).
var ErrDuplicateContent = errors.New("store: duplicate content hash")

// ErrNotPending is returned by SuggestionStore.Resolve when the suggestion is not in
// the 'pending' state (already accepted/dismissed/expired, or absent) — the
// compare-and-swap found no pending row (Phase 27, D-087).
var ErrNotPending = errors.New("store: suggestion is not pending")

// ErrEmptyPolicy is returned by TopicViewStore.PutAgentPolicy when both
// AllowTopics and DenyTopics are empty. Such a binding carries no constraint and
// is indistinguishable from an unbound agent, so it is rejected BEFORE the
// delete-then-insert replace runs — otherwise an empty Put would silently wipe an
// existing binding and then read back as ErrNotFound (ae1, D-146). To remove a
// binding, use DeleteAgentPolicy.
var ErrEmptyPolicy = errors.New("store: agent policy must have at least one allow or deny topic")

// ErrInvalidSubjectKind is returned by (TopicView).Validate when SubjectKind is
// neither "agent" nor "key" (ae9, D-149/D-151).
var ErrInvalidSubjectKind = errors.New(`store: topic view subject_kind must be "agent" or "key"`)

// ErrSubjectIDRequired is returned by (TopicView).Validate when SubjectID is
// empty (ae9, D-149/D-151).
var ErrSubjectIDRequired = errors.New("store: topic view subject_id is required")
