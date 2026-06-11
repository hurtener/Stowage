package store

import "github.com/hurtener/stowage/internal/identity"

// Record is a verbatim immutable interaction record (RFC P1, D-006).
// Records are append-only; no Update method exists on RecordStore.
// All timestamps are unix milliseconds (D-037).
type Record struct {
	ID            string
	TenantID      string
	ProjectID     string
	UserID        string
	SessionID     string
	BranchID      string
	Role          string // "user" | "assistant" | "tool"
	Content       string
	SourceAgent   string
	ResponseID    string
	Outcome       string // "success" | "failure" | ""
	OutcomeDetail string
	TokenEstimate int64
	OccurredAt    int64 // unix millis
	CreatedAt     int64 // unix millis
	ProcessedAt   int64 // unix millis; 0 = unprocessed
}

// Memory is a derived abstraction produced by the engram pipeline (RFC §5).
// All timestamps are unix milliseconds (D-037).
type Memory struct {
	ID             string
	TenantID       string
	ProjectID      string
	UserID         string
	SessionID      string
	Kind           string // "fact"|"preference"|"decision"|"gotcha"|"pattern"|"task"|"narrative"|"strategy"|"failure_mode"
	Content        string
	Context        string
	Status         string // "active"|"pending_confirmation"|"pending_review"|"superseded"|"quarantined"|"expired"|"deleted"
	Importance     int
	Confidence     float64
	TrustSource    string
	MatchCount     int64
	InjectCount    int64
	UseCount       int64
	SaveCount      int64
	FailCount      int64
	NoiseCount     int64
	Stability      float64
	LastAccessedAt int64
	ValidFrom      int64
	ValidUntil     int64
	EpisodeID      string
	SupersedesID   string
	SupersededByID string
	PrivacyZone    string
	CreatedAt      int64
	UpdatedAt      int64
	ContentHash    string // SHA-256 hex of NormalizeContent(content); "" for pre-Phase-08 rows
}

// Link is a typed directed edge between memories (RFC §5.6, D-026).
type Link struct {
	ID         string
	TenantID   string
	FromMemory string
	ToMemory   string
	Type       string // "supports"|"contradicts"|"depends_on"|"caused_by"|"led_to"|"relates_to"
	Source     string // "explicit"|"reconciler"|"inferred"
	Confidence float64
	CreatedAt  int64
}

// Provenance links a memory to a verbatim record span (RFC §5, D-006).
type Provenance struct {
	ID        string
	MemoryID  string
	RecordID  string
	SpanStart int
	SpanEnd   int
	TenantID  string
	CreatedAt int64
}

// Topic is an extraction magnet (RFC §4.1, D-007).
type Topic struct {
	ID          string
	TenantID    string
	ProjectID   string
	UserID      string
	SessionID   string
	Key         string
	Description string
	Status      string // "active"|"paused"|"deleted"
	Pack        string
	CreatedAt   int64
	UpdatedAt   int64
}

// BufferItem is an item in a multi-agent accumulation buffer (RFC §4.1, D-007).
type BufferItem struct {
	ID            string
	TenantID      string
	ProjectID     string
	UserID        string
	SessionID     string
	BufferKey     string
	BranchID      string
	RecordID      string
	TokenEstimate int64
	FlushedAt     int64 // unix millis; 0 = not flushed
	CreatedAt     int64
}

// Event is an audit trail entry (RFC §5.8, D-024).
type Event struct {
	ID        string
	TenantID  string
	ProjectID string
	UserID    string
	SessionID string
	Type      string
	SubjectID string
	Reason    string
	Payload   string // JSON
	CreatedAt int64
}

// Branch is a session fork for exploration (RFC §5.5, D-029).
// Records on a discarded branch remain readable; status reflects lifecycle.
// All timestamps are unix milliseconds (D-037).
type Branch struct {
	ID             string
	TenantID       string
	ProjectID      string
	UserID         string
	SessionID      string
	ParentBranchID string
	Status         string // "open"|"merged"|"discarded"
	CreatedAt      int64  // unix millis
	UpdatedAt      int64  // unix millis
}

// DeadLetter is a failed pipeline item that requires operator attention.
type DeadLetter struct {
	ID         string
	Stage      string
	ItemID     string
	Error      string
	Attempts   int
	ResolvedAt int64 // unix millis; 0 = unresolved
	CreatedAt  int64
}

// MemoryJunctions holds the junction rows for a memory, used in prior-state
// snapshots (D-017 reversibility contract, Phase 15 rollback).
// GetJunctions returns an empty MemoryJunctions (not ErrNotFound) when the
// memory has no junction rows.
type MemoryJunctions struct {
	Entities   []string
	Keywords   []string
	Queries    []string
	Provenance []Provenance
}

// ReconcileAction is the outcome of a reconciliation decision (Phase 08).
type ReconcileAction string

const (
	ActionAdd       ReconcileAction = "add"
	ActionUpdate    ReconcileAction = "update"
	ActionMerge     ReconcileAction = "merge"
	ActionSupersede ReconcileAction = "supersede"
	ActionDiscard   ReconcileAction = "discard"
	ActionPark      ReconcileAction = "park" // pending_confirmation; target stays active
)

// NeighborQuery specifies structural overlap parameters for FindNeighbors.
// Junction-overlap search: memories sharing entities or keywords with the
// candidate, ranked by overlap count then recency. Status is always 'active'.
// This is the interim neighbor lookup until Phase 09's semantic lanes.
type NeighborQuery struct {
	Entities []string
	Keywords []string
	Kinds    []string // optional kind filter (empty = all kinds)
	Limit    int      // 0 defaults to 8
}

// Window is an optional time-range filter for retrieval lanes (Phase 09).
// Timestamps are unix milliseconds; zero means unbounded.
type Window struct {
	From  int64 // inclusive lower bound on created_at; 0 = no lower bound
	Until int64 // inclusive upper bound on created_at; 0 = no upper bound
}

// LexicalHit is a single result from a FTS retrieval lane.
type LexicalHit struct {
	MemoryID string
	Rank     float64 // bm25/ts_rank score; higher = more relevant
}

// StoredVector is a raw vector entry returned by VectorStore.Scan (D-046).
// Vec is already decoded from float32-LE bytes; Dims is the declared length.
type StoredVector struct {
	MemoryID  string
	TenantID  string
	ProjectID string
	UserID    string
	SessionID string
	Vec       []float32
	Dims      int
	Model     string
	// Metadata from the memories table — used for window/kind filtering in Scan.
	Kind      string
	CreatedAt int64 // unix ms
}

// MemoryForEmbed is a lightweight memory entry for the backfill embed sweep.
// It carries the content and junction rows needed to build enriched text (D-047).
type MemoryForEmbed struct {
	MemoryID  string
	TenantID  string
	ProjectID string
	UserID    string
	SessionID string
	Content   string
	Entities  []string
	Keywords  []string
	Queries   []string
}

// Injection records one retrieved memory that was injected into a response
// (D-025, D-051). The ID is the citation handle exposed in the v1 envelope.
// Lane is a CSV of contributing lane names (e.g. "lexical,vector").
// Feedback holds any wrong-citation signal; "" means no feedback yet.
// All timestamps are unix milliseconds (D-037).
type Injection struct {
	ID         string // ULID = citation handle (D-051)
	TenantID   string
	ProjectID  string
	UserID     string
	SessionID  string
	ResponseID string
	MemoryID   string
	Rank       int
	Score      float64
	Lane       string // CSV of lane names
	WasCited   bool
	Feedback   string // "" | "wrong_citation"
	CreatedAt  int64  // unix millis
}

// CommitSet is the transactional unit for one reconciliation outcome.
// All writes (memory row, junction rows, provenance rows, link rows, event rows)
// happen in a single DB transaction — the D-017/D-045 reversibility contract.
//
// The reconcile package pre-populates Events with prior-state snapshots before
// calling Commit; the driver writes them directly into the transaction
// (EventStore.Emit cannot join an existing tx — see D-045).
type CommitSet struct {
	// Action is the reconciliation outcome.
	Action ReconcileAction

	// Memory is the new or updated memory to persist.
	// Zero-value for ActionDiscard (nothing is persisted).
	Memory Memory

	// Entities, Keywords, Queries are junction rows for Memory.
	// For ActionUpdate these replace the existing junctions for Memory.ID.
	Entities []string
	Keywords []string
	Queries  []string

	// Provenance rows to insert.
	Provenance []Provenance

	// Links to insert (source = "reconciler" for reconciler-written links).
	Links []Link

	// Targets are the prior states of memories being modified (for D-017
	// reversibility). Populate before calling Commit so events carry the
	// full pre-mutation snapshot.
	//   update:    one entry (memory before content rewrite)
	//   merge:     all source memories before supersede
	//   supersede: the superseded memory before status change
	//   park:      the target memory (target stays active; snapshot for audit)
	Targets []Memory

	// Events to write within the same transaction. The reconcile package
	// builds these (with prior-state JSON payloads) before calling Commit.
	// The driver writes the rows directly — not via EventStore.Emit — to
	// ensure they participate in the same tx.
	Events []Event

	// Scope is the identity scope used when writing events in the same tx.
	// Must match the scope passed to Commit.
	Scope identity.Scope

	// FaultHook is a TEST-ONLY injection point. If non-nil, it is called
	// after inserting the main memory row but before writing junction,
	// provenance, link, and event rows. Returning a non-nil error causes the
	// entire transaction to roll back — proving no partial rows remain.
	// MUST be nil in all production code paths.
	FaultHook func() error
}
