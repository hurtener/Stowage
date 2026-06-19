// Package store defines the Store seam and all sub-store interfaces for the
// Stowage persistence layer (RFC §8.1, D-009, D-021, D-024).
//
// No concrete drivers live here — they register themselves in sub-packages
// (sqlitestore, pgstore) via init(). Callers use Open() to obtain a Store.
package store

import (
	"context"

	"github.com/hurtener/stowage/internal/auth"
	"github.com/hurtener/stowage/internal/identity"
)

// Store is the top-level persistence seam.
type Store interface {
	// Migrate applies pending migrations idempotently.
	Migrate(ctx context.Context) error
	// AppliedMigrations returns the versions recorded in schema_migrations,
	// ascending. Empty (not an error) before the first Migrate.
	AppliedMigrations(ctx context.Context) ([]string, error)

	// Records returns the verbatim fidelity sub-store.
	Records() RecordStore

	// Memories returns the abstraction-layer sub-store.
	Memories() MemoryStore

	// Topics returns the extraction-magnet sub-store.
	Topics() TopicStore

	// Buffers returns the multi-agent accumulation sub-store.
	Buffers() BufferStore

	// Keys returns the API-key keyring.
	Keys() auth.Keyring

	// Events returns the audit-trail sub-store.
	Events() EventStore

	// Branches returns the branch-lifecycle sub-store.
	Branches() BranchStore

	// Episodes returns the episodic sub-store (Phase 22, RFC §6b, D-079).
	Episodes() EpisodeStore

	// Ops returns the dead-letter and job-marker sub-store.
	Ops() OpsStore

	// Injections returns the injection (attribution) sub-store (Phase 11, D-051).
	Injections() InjectionStore

	// Grants returns the team-sharing sub-store (Phase 15, RFC §5.3, D-016).
	Grants() GrantStore

	// Suggestions returns the proactive-offer sub-store (Phase 27, RFC §6d, D-087).
	Suggestions() SuggestionStore

	// ScopeSettings returns the per-scope settings KV sub-store (governance;
	// Phase 27, RFC §6d, D-087).
	ScopeSettings() ScopeSettingsStore

	// Vectors returns the float32-LE BLOB vector sub-store (Phase 09, D-046).
	// Drivers implement brute-force scope-filtered scan; cosine is computed by
	// the caller (internal/vindex). No pgvector dependency; CI stays postgres:17.
	Vectors() VectorStore

	// Close flushes any pending writes and releases resources.
	Close(ctx context.Context) error

	// Tenants returns the distinct tenant IDs present in the store.
	// This is an operator-level enumeration used by sweeps to iterate
	// tenants and then operate per-scope (D-057). NOT a data read —
	// only returns tenant ID strings, not scoped data.
	Tenants(ctx context.Context) ([]string, error)
}

// SuggestionStore is the proactive-offer sub-store (RFC §6d, Phase 27, D-087). A
// suggestion is a context offer surfaced for a session; accept/dismiss tunes the
// trigger class. Backed by the day-one `suggestions` table. Scope-enforced (P3).
type SuggestionStore interface {
	// Create inserts pending suggestion rows. Duplicate IDs are ignored (idempotent).
	Create(ctx context.Context, scope identity.Scope, sugs []Suggestion) error

	// ListBySession returns a session's suggestions, optionally filtered by status
	// ("" = any), most-recent-first, capped at limit.
	ListBySession(ctx context.Context, scope identity.Scope, sessionID, status string, limit int) ([]Suggestion, error)

	// Get returns one suggestion by id within scope (ErrNotFound when absent).
	Get(ctx context.Context, scope identity.Scope, id string) (*Suggestion, error)

	// Resolve applies accept|dismiss to a PENDING suggestion (compare-and-swap on
	// status='pending'): increments the matching counter, sets status, stamps
	// updated_at. ErrNotPending when the suggestion is not pending (or absent).
	Resolve(ctx context.Context, scope identity.Scope, id, action string, now int64) (*Suggestion, error)

	// CountByTrigger returns the accepted/dismissed suggestion counts for a trigger
	// kind within scope — the per-class feedback-tuning signal.
	CountByTrigger(ctx context.Context, scope identity.Scope, triggerKind string) (accepted, dismissed int, err error)

	// ListPendingBefore returns pending suggestions created before `before` (for the
	// expiry sweep), capped at limit. Scope-enforced.
	ListPendingBefore(ctx context.Context, scope identity.Scope, before int64, limit int) ([]Suggestion, error)

	// ExpirePending sets the given pending suggestions to 'expired' (idempotent).
	ExpirePending(ctx context.Context, scope identity.Scope, ids []string, now int64) error
}

// ScopeSettingsStore is the per-scope settings KV (RFC §6d governance, Phase 27,
// D-087). Backed by the day-one `scope_settings` table (UNIQUE(scope,key)). Used for
// runtime-changeable, admin-set proactive governance. Scope-enforced (P3).
type ScopeSettingsStore interface {
	// Get returns the value for key at the EXACT scope (tenant/project/user/session
	// as given). found=false (not an error) when absent.
	Get(ctx context.Context, scope identity.Scope, key string) (value string, found bool, err error)

	// Set upserts the value for key at the exact scope.
	Set(ctx context.Context, scope identity.Scope, key, value string, now int64) error

	// List returns all settings at the exact scope as a key→value map.
	List(ctx context.Context, scope identity.Scope) (map[string]string, error)

	// Delete removes the key at the exact scope (no error when absent).
	Delete(ctx context.Context, scope identity.Scope, key string) error
}

// RecordStore is the verbatim fidelity layer (RFC P1, D-006, D-024).
type RecordStore interface {
	// Append stores records. Duplicate IDs are silently ignored (idempotent).
	Append(ctx context.Context, scope identity.Scope, records []Record) error

	// Get returns a single record by ID within scope.
	// Returns ErrNotFound when absent.
	Get(ctx context.Context, scope identity.Scope, id string) (*Record, error)

	// ListBySession returns records for a session/branch pair, ordered by
	// occurred_at ascending. cursor is a ULID-string opaque pagination token;
	// pass "" for the first page. Returns (records, nextCursor, error).
	ListBySession(ctx context.Context, scope identity.Scope, sessionID, branchID string, limit int, cursor string) ([]Record, string, error)

	// ListUnprocessed returns records where processed_at == 0, older than
	// olderThan unix-millis, up to limit. Used by the pipeline worker.
	ListUnprocessed(ctx context.Context, olderThan int64, limit int) ([]Record, error)

	// MarkProcessed sets processed_at for the given record IDs.
	MarkProcessed(ctx context.Context, ids []string) error

	// CountRecordsSince returns the number of records in scope whose created_at
	// is strictly greater than sinceMs (unix milliseconds). Used by the retrieval
	// layer to compute ActivityTurns for the scoring decay function (Phase 10).
	// The count is scope-indexed for efficiency; see D-008 on decay computation.
	CountRecordsSince(ctx context.Context, scope identity.Scope, sinceMs int64) (int64, error)

	// GetMany returns records for the given IDs within scope. IDs not found are
	// silently omitted. Order matches the order of ids. Used by the drill-down
	// path to batch-fetch verbatim records for provenance spans (Phase 11).
	GetMany(ctx context.Context, scope identity.Scope, ids []string) ([]Record, error)

	// ListByOutcome returns scope's records whose outcome is in outcomes and
	// whose occurred_at is strictly greater than since (unix millis), ordered by
	// (session_id, branch_id, occurred_at, id) ascending so rows group into
	// trajectories, capped at limit. Scope-parameterized (P3) — there is no
	// unscoped variant. An empty outcomes slice returns no rows. Used by the
	// reflection sweep to read outcome-tagged trajectories (Phase 19, D-077).
	ListByOutcome(ctx context.Context, scope identity.Scope, outcomes []string, since int64, limit int) ([]Record, error)

	// DistinctSessions returns scope's distinct (session_id, branch_id) groups
	// whose latest record occurred at or before idleBefore (i.e. closed sessions),
	// with the record time-range and count, ordered by last occurrence ascending,
	// capped at limit. Sessionless records (session_id empty) are excluded. Scope-
	// parameterized (P3). Used by the episode boundary-detection sweep (Phase 22).
	DistinctSessions(ctx context.Context, scope identity.Scope, idleBefore int64, limit int) ([]SessionInfo, error)
}

// EpisodeStore is the episodic sub-store (RFC §6b, Phase 22, D-079). Episodes are
// detected heuristically from closed sessions; a narration sweep attaches a
// narrative memory. Scope-parameterized except the unscoped narration scan.
type EpisodeStore interface {
	// CreateEpisode inserts a new episode. Scope sets tenant/project/user.
	CreateEpisode(ctx context.Context, scope identity.Scope, e Episode) error

	// GetEpisode returns an episode by ID within scope; ErrNotFound when absent.
	GetEpisode(ctx context.Context, scope identity.Scope, id string) (*Episode, error)

	// GetEpisodeBySession returns the episode for a session within scope, or
	// ErrNotFound when none exists — the detection idempotency gate (D-079).
	GetEpisodeBySession(ctx context.Context, scope identity.Scope, sessionID string) (*Episode, error)

	// ListEpisodesNeedingNarrative returns episodes with no narrative_memory_id,
	// oldest first, up to limit. Unscoped scan (the narration sweep iterates all
	// tenants), mirroring RecordStore.ListUnprocessed (D-057).
	ListEpisodesNeedingNarrative(ctx context.Context, limit int) ([]Episode, error)

	// SetEpisodeNarrative attaches the narrative memory + title to an episode and
	// bumps updated_at, within scope. ErrNotFound when absent.
	SetEpisodeNarrative(ctx context.Context, scope identity.Scope, episodeID, narrativeMemoryID, title string, updatedAt int64) error

	// ListEpisodes returns scope's episodes, most recent first, paginated by an
	// opaque "<started_at>:<id>" cursor ("" for the first page). Used by Phase 23.
	ListEpisodes(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]Episode, string, error)
}

// MemoryStore is the abstraction layer (RFC §5, D-006, D-008, D-024).
type MemoryStore interface {
	// Insert stores a new memory.
	Insert(ctx context.Context, scope identity.Scope, m Memory) error

	// Get returns a single memory by ID within scope.
	// Returns ErrNotFound when absent.
	Get(ctx context.Context, scope identity.Scope, id string) (*Memory, error)

	// Update replaces the mutable fields of a memory.
	Update(ctx context.Context, scope identity.Scope, m Memory) error

	// SetStatus updates only the status + updated_at fields.
	SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error

	// ListByStatus returns memories with the given status ordered by
	// created_at ascending. cursor is an opaque pagination token.
	ListByStatus(ctx context.Context, scope identity.Scope, status string, limit int, cursor string) ([]Memory, string, error)

	// InsertLinks stores typed directed edges between memories.
	InsertLinks(ctx context.Context, scope identity.Scope, links []Link) error

	// ListLinks returns edges matching fromMemoryID or toMemoryID (either can
	// be "" to omit that filter).
	ListLinks(ctx context.Context, scope identity.Scope, fromMemoryID, toMemoryID string) ([]Link, error)

	// AddProvenance records which verbatim record spans produced a memory.
	AddProvenance(ctx context.Context, scope identity.Scope, rows []Provenance) error

	// GetByContentHash returns the active memory matching the given SHA-256
	// content hash within scope. Returns ErrNotFound when absent.
	// Used by the exact-dedup pre-filter (D-044).
	GetByContentHash(ctx context.Context, scope identity.Scope, hash string) (*Memory, error)

	// GetByContentHashStatus returns the memory with the given content hash AND
	// status within scope. Returns ErrNotFound when absent. Unlike
	// GetByContentHash (which is active-only), this method accepts any status
	// value — it is used by the parked-duplicate pre-commit check (D-065):
	// the active-only unique index never fires for pending_confirmation rows,
	// so re-extracted parked content is caught here instead.
	GetByContentHashStatus(ctx context.Context, scope identity.Scope, hash, status string) (*Memory, error)

	// FindNeighbors returns active memories that share entities or keywords
	// with the candidate, ranked by overlap count descending then recency.
	// The search is bounded by q.Limit (default 8 when 0). Scope-parameterized
	// per P3; cross-tenant/cross-user isolation proven by conformance.
	// This is the interim structural neighbor lookup (Phase 09 adds semantic lanes).
	FindNeighbors(ctx context.Context, scope identity.Scope, q NeighborQuery) ([]Memory, error)

	// IncrementCounter atomically increments one of the six utility counters
	// on a memory. counter must be one of: "match", "inject", "use", "save",
	// "fail", "noise". Returns an error for unrecognised counter names.
	IncrementCounter(ctx context.Context, scope identity.Scope, id, counter string) error

	// ApplyFeedback atomically increments the counter named by signal and touches
	// last_accessed_at. signal must be one of: "use", "save", "fail", "noise".
	// Returns an error for unrecognised signals. No-op (not ErrNotFound) when the
	// memory does not exist in scope (feedback is best-effort).
	ApplyFeedback(ctx context.Context, scope identity.Scope, memoryID, signal string) error

	// GetJunctions returns the junction rows (entities, keywords, anticipated
	// queries) and provenance spans for a memory. Used to build complete
	// prior-state snapshots (D-017, Phase 15 rollback).
	// Returns an empty MemoryJunctions (not ErrNotFound) when the memory
	// exists but has no junction rows.
	GetJunctions(ctx context.Context, scope identity.Scope, id string) (MemoryJunctions, error)

	// LexicalSearch returns the top-k memories matching query via full-text
	// search on content+context (FTS5/tsvector, Phase 09). Results are ordered
	// by relevance score descending. scope-isolated per P3.
	LexicalSearch(ctx context.Context, scope identity.Scope, query string, k int, w Window, kinds []string) ([]LexicalHit, error)

	// QuerySearch returns the top-k memories whose anticipated queries match
	// the given query text (FTS over memory_queries, Phase 09). Results are
	// ordered by relevance score descending. scope-isolated per P3.
	QuerySearch(ctx context.Context, scope identity.Scope, query string, k int, w Window) ([]LexicalHit, error)

	// GetMany returns memories for the given IDs within scope. IDs not found
	// are silently omitted. Order matches the order of ids.
	GetMany(ctx context.Context, scope identity.Scope, ids []string) ([]Memory, error)

	// Commit executes one reconciliation outcome as a single atomic transaction:
	// memory insert/update, junction rows, provenance rows, link rows, status
	// transitions on targets, and event rows — all in one DB transaction.
	//   SQLite driver:  ONE exec closure = ONE sql.Tx (D-045).
	//   Postgres driver: pool.Begin → pgx.Tx (D-045).
	// Events are written directly into the transaction; not via EventStore.Emit.
	// CommitSet.FaultHook is a test-only mid-commit failure injection.
	// Commit returns ErrDuplicateContent for ActionAdd/ActionPark when the
	// content_hash unique index fires (m7 TOCTOU guard).
	Commit(ctx context.Context, scope identity.Scope, cs CommitSet) error

	// ListActiveForDecay returns at most limit active memories for the scope,
	// ordered by created_at, id ascending. cursor is an opaque pagination token.
	// Used by the lifecycle decay sweep to batch-scan active memories per
	// tenant-scope (Phase 14, D-058).
	ListActiveForDecay(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]Memory, string, error)

	// SetValidUntil sets the valid_until field of a memory (unix millis).
	// Used by the decay sweep to record the first-below-floor observation.
	// A value of 0 clears the field (D-058).
	SetValidUntil(ctx context.Context, scope identity.Scope, id string, validUntil int64) error

	// ListSupersededBy returns all memories whose superseded_by_id equals
	// supersederID within scope. Used by the merge-rollback path to discover
	// all siblings that were merged into a common digest (Phase 18, D-064).
	// Returns an empty slice (not ErrNotFound) when none exist.
	ListSupersededBy(ctx context.Context, scope identity.Scope, supersederID string) ([]Memory, error)

	// ListByKinds returns the active memories in scope whose kind is one of
	// kinds, ordered by (created_at, id) ascending for a stable, reproducible
	// result. It is the store view backing deterministic playbook assembly
	// (internal/playbook, D-072): ranking happens in the playbook layer from the
	// utility counters, so this method ranks nothing — it just returns the
	// candidate set. Active-only and scope-enforced (P3); there is no unscoped
	// variant. An empty kinds slice returns an empty slice (not an error). Not
	// paginated: the playbook kinds are a bounded per-scope set the caller
	// budget-packs.
	ListByKinds(ctx context.Context, scope identity.Scope, kinds []string) ([]Memory, error)

	// ListMemoriesByRecords returns the active memories in scope whose provenance
	// references any of recordIDs, optionally narrowed to kinds (empty = any kind).
	// It is the reverse of GetJunctions (record → memories) and backs Phase-24
	// causal inference: gathering an episode's decision-class memories from the
	// episode's records. Results are DISTINCT by memory id, ordered by (created_at,
	// id) ascending for a stable result. Active-only and scope-enforced (P3); there
	// is no unscoped variant. An empty recordIDs slice returns an empty slice (not an
	// error). Not paginated: the per-episode record set is bounded.
	ListMemoriesByRecords(ctx context.Context, scope identity.Scope, recordIDs []string, kinds []string) ([]Memory, error)
}

// TopicStore manages extraction magnets (RFC §4.1, D-007).
type TopicStore interface {
	// Upsert inserts or updates a topic keyed by (scope, key).
	Upsert(ctx context.Context, scope identity.Scope, t Topic) error

	// Get returns a topic by key within scope. Returns ErrNotFound when absent.
	Get(ctx context.Context, scope identity.Scope, key string) (*Topic, error)

	// List returns all topics for the scope ordered by created_at ascending.
	List(ctx context.Context, scope identity.Scope) ([]Topic, error)

	// Delete soft-deletes a topic (sets status = "deleted").
	Delete(ctx context.Context, scope identity.Scope, key string) error
}

// BufferStore manages multi-agent accumulation buffers (RFC §4.1, D-007).
type BufferStore interface {
	// AppendItem adds an item to a named buffer.
	AppendItem(ctx context.Context, scope identity.Scope, item BufferItem) error

	// ListDue returns unflushed items for a buffer key, ordered by created_at.
	ListDue(ctx context.Context, scope identity.Scope, bufferKey string, limit int) ([]BufferItem, error)

	// Flush atomically marks all unflushed items for bufferKey as flushed and
	// returns them. Returns empty slice (not error) if none are pending.
	Flush(ctx context.Context, scope identity.Scope, bufferKey string) ([]BufferItem, error)

	// ScanAged returns unflushed items created before olderThanMs (unix millis),
	// up to limit, ordered by created_at ascending. Scans all tenants — used by
	// the pipeline ticker for age-triggered flush and crash recovery, following
	// the same unscoped-scan pattern as RecordStore.ListUnprocessed.
	ScanAged(ctx context.Context, olderThanMs int64, limit int) ([]BufferItem, error)
}

// EventStore is the audit trail (RFC §5.8, D-024).
type EventStore interface {
	// Emit records an audit event.
	Emit(ctx context.Context, scope identity.Scope, e Event) error

	// List returns events ordered by created_at ascending.
	// cursor is an opaque pagination token; pass "" for first page.
	List(ctx context.Context, scope identity.Scope, limit int, cursor string) ([]Event, string, error)

	// ListBySubject returns at most limit events whose subject_id matches
	// subjectID within scope, ordered by created_at DESCENDING (newest first).
	// Used by the rollback endpoint to find the invertible reconcile event
	// (D-064, Phase 18). Migration 0006 adds idx_events_subject for efficiency.
	ListBySubject(ctx context.Context, scope identity.Scope, subjectID string, limit int) ([]Event, error)
}

// BranchStore manages branch lifecycle (RFC §5.5, D-029).
type BranchStore interface {
	// Create inserts a new branch record.
	Create(ctx context.Context, scope identity.Scope, b Branch) error

	// Get returns a single branch by ID within scope.
	// Returns ErrNotFound when absent.
	Get(ctx context.Context, scope identity.Scope, id string) (*Branch, error)

	// SetStatus updates the status and updated_at fields of a branch.
	SetStatus(ctx context.Context, scope identity.Scope, id string, status string, updatedAt int64) error

	// ListBySession returns all branches for a session within scope.
	ListBySession(ctx context.Context, scope identity.Scope, sessionID string) ([]Branch, error)
}

// VectorStore manages float32-LE BLOB vector embeddings (Phase 09, D-046).
// All vectors are scope-isolated; brute-force cosine is performed by the
// caller (internal/vindex) after Scan returns the raw float32 slices.
// No pgvector extension; CI stays postgres:17 (D-046).
type VectorStore interface {
	// Upsert inserts or replaces the vector for v.MemoryID within scope.
	Upsert(ctx context.Context, scope identity.Scope, v StoredVector) error

	// Delete removes the vector for memoryID. No-op when absent.
	Delete(ctx context.Context, scope identity.Scope, memoryID string) error

	// Scan returns all vectors for the scope, optionally filtered by kinds and
	// time window. Used by the brute-force vindex driver to compute cosine.
	Scan(ctx context.Context, scope identity.Scope, kinds []string, w Window) ([]StoredVector, error)

	// ListWithoutVectors returns at most limit active memories with no vector
	// entry, including junction rows needed to build enriched embed text (D-047).
	// Unscoped — scans all tenants like RecordStore.ListUnprocessed.
	ListWithoutVectors(ctx context.Context, limit int) ([]MemoryForEmbed, error)
}

// InjectionStore records retrieval injections for attribution (Phase 11, D-025, D-051).
// Every retrieved memory is persisted here; the row ID is the citation handle.
type InjectionStore interface {
	// Append batch-inserts injection rows. Duplicate IDs are silently ignored.
	// Called asynchronously from the injection writer goroutine.
	Append(ctx context.Context, scope identity.Scope, rows []Injection) error

	// ListByResponse returns all injections for responseID within scope,
	// ordered by rank ascending.
	ListByResponse(ctx context.Context, scope identity.Scope, responseID string) ([]Injection, error)

	// Get returns a single injection by ID within scope.
	// Returns ErrNotFound when absent or owned by a different tenant.
	Get(ctx context.Context, scope identity.Scope, id string) (*Injection, error)

	// MarkWrongCitation atomically marks the injection as wrong_citation AND
	// increments noise_count + fail_count on the associated memory AND touches
	// last_accessed_at — all in a single transaction (D-027 groundwork).
	// Returns ErrNotFound if the injection does not exist within scope.
	MarkWrongCitation(ctx context.Context, scope identity.Scope, injectionID string) error
}

// GrantStore manages groups, group membership, and grants (Phase 15, RFC §5.3, D-016).
// All writes are tenant-scoped (P3); cross-tenant grants are unconstructible.
type GrantStore interface {
	// --- Groups ---

	// CreateGroup inserts a new group. Returns ErrConflict on duplicate ID.
	CreateGroup(ctx context.Context, scope identity.Scope, g Group) error

	// GetGroup returns a group by ID within the tenant.
	// Returns ErrNotFound when absent.
	GetGroup(ctx context.Context, scope identity.Scope, id string) (*Group, error)

	// ListGroups returns all groups for the tenant, ordered by created_at ascending.
	ListGroups(ctx context.Context, scope identity.Scope) ([]Group, error)

	// DeleteGroup removes a group and its membership rows (cascade).
	// Returns ErrNotFound when absent.
	DeleteGroup(ctx context.Context, scope identity.Scope, id string) error

	// --- Members ---

	// AddMember adds a user to a group. Duplicate inserts are silently ignored.
	AddMember(ctx context.Context, scope identity.Scope, m GroupMember) error

	// RemoveMember removes a user from a group.
	// Returns ErrNotFound when the membership does not exist.
	RemoveMember(ctx context.Context, scope identity.Scope, groupID, userID string) error

	// ListMembers returns all members of a group, ordered by created_at ascending.
	ListMembers(ctx context.Context, scope identity.Scope, groupID string) ([]GroupMember, error)

	// --- Grants ---

	// CreateGrant inserts a new grant. The TenantID on g must match scope.Tenant
	// (cross-tenant grants are unconstructible — driver enforces via FK + check).
	CreateGrant(ctx context.Context, scope identity.Scope, g Grant) error

	// GetGrant returns a grant by ID within the tenant.
	// Returns ErrNotFound when absent.
	GetGrant(ctx context.Context, scope identity.Scope, id string) (*Grant, error)

	// ListGrants returns all grants for the tenant (including revoked), ordered
	// by created_at ascending.
	ListGrants(ctx context.Context, scope identity.Scope) ([]Grant, error)

	// RevokeGrant sets revoked_at on the grant; effective immediately.
	// Returns ErrNotFound when absent.
	RevokeGrant(ctx context.Context, scope identity.Scope, id string, revokedAt int64) error

	// --- Resolution ---

	// EffectiveScopes resolves the set of scopes the caller may read.
	// The first element is always the caller's own scope (ZoneCeiling="").
	// Subsequent elements are scopes granted via active (non-revoked) grants where
	// the caller's user_id appears in the grant's group, capped by zone_ceiling.
	// If callerScope.User is empty (no user in scope), only the caller's own scope
	// is returned (no group memberships can exist without a user).
	// The query is a single SQL join (≤1 extra query per retrieve, D-060).
	// Cross-tenant isolation: only grants in callerScope.Tenant are considered.
	EffectiveScopes(ctx context.Context, callerScope identity.Scope) ([]ScopedQuery, error)
}

// OpsStore manages dead letters and job markers (RFC §11, D-024).
type OpsStore interface {
	// PutDeadLetter records a failed pipeline item.
	PutDeadLetter(ctx context.Context, d DeadLetter) error

	// ListDeadLetters returns unresolved dead letters for a stage.
	ListDeadLetters(ctx context.Context, stage string, limit int) ([]DeadLetter, error)

	// ResolveDeadLetter marks a dead letter as resolved.
	ResolveDeadLetter(ctx context.Context, id string, resolvedAt int64) error

	// CheckAndSetJobMarker atomically checks whether (job, marker) has run and
	// sets it if not. Returns true if the marker was newly set (i.e. job should
	// run), false if it was already present.
	CheckAndSetJobMarker(ctx context.Context, job, marker string, ranAt int64) (bool, error)

	// AdvisoryLock acquires a PostgreSQL advisory lock identified by key.
	// The returned func releases the lock. No-op on sqlite (returns a no-op
	// release func and nil error).
	AdvisoryLock(ctx context.Context, key int64) (func() error, error)
}
