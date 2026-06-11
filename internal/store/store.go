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

	// Ops returns the dead-letter and job-marker sub-store.
	Ops() OpsStore

	// Vectors returns the float32-LE BLOB vector sub-store (Phase 09, D-046).
	// Drivers implement brute-force scope-filtered scan; cosine is computed by
	// the caller (internal/vindex). No pgvector dependency; CI stays postgres:17.
	Vectors() VectorStore

	// Close flushes any pending writes and releases resources.
	Close(ctx context.Context) error
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
