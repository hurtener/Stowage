package store

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
