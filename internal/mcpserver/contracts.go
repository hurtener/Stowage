// Package mcpserver implements the Stowage MCP tool surface over the Dockyard
// runtime library (RFC §9.2, D-015, D-018, D-020, D-061).
//
// Ten typed tools mirror the HTTP v1 surfaces (the original seven plus the
// D-070 reversibility trio: memory_get, memory_rollback, memory_resolve). Tool
// handlers share the
// same store/service code the HTTP handlers use — no business logic is
// duplicated (AC-3). Schema goldens in testdata/ are the contract gate (D-061).
package mcpserver

// ─── memory_ingest ────────────────────────────────────────────────────────────

// IngestRecord is one verbatim record to ingest (mirrors HTTP POST /v1/records).
type IngestRecord struct {
	TenantID      string `json:"tenant_id,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`
	UserID        string `json:"user_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	BranchID      string `json:"branch_id,omitempty"`
	Role          string `json:"role,omitempty"`
	Content       string `json:"content"`
	SourceAgent   string `json:"source_agent,omitempty"`
	ResponseID    string `json:"response_id,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	OutcomeDetail string `json:"outcome_detail,omitempty"`
	OccurredAt    int64  `json:"occurred_at,omitempty"`
	BufferKey     string `json:"buffer_key,omitempty"`
}

// IngestTargetScope is the optional contribute-mode target scope.
//
// NOTE (D-069, parity-lens BUG-2): contribute-mode is a multi-user, grant-gated
// write that the MCP surface does NOT yet honor. Setting this field (or
// ContributorUserID) on a memory_ingest call is rejected with an error rather
// than silently mis-scoping into the caller's own pool. Full HTTP↔MCP honoring
// lands in Wave B; for now use HTTP POST /v1/records for contribute writes.
type IngestTargetScope struct {
	ProjectID string `json:"project_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// IngestInput is the memory_ingest tool input. Records ingest into the caller's
// own scope. TargetScope/ContributorUserID (contribute-mode) are declared for
// forward-compatibility but are NOT yet honored on MCP — setting either fails
// loud (see IngestTargetScope; D-069).
type IngestInput struct {
	Records           []IngestRecord     `json:"records"`
	TargetScope       *IngestTargetScope `json:"target_scope,omitempty"`
	ContributorUserID string             `json:"contributor_user_id,omitempty"`
}

// IngestOutput is the memory_ingest tool output.
type IngestOutput struct {
	IDs      []string `json:"ids"`
	Enqueued bool     `json:"enqueued"`
}

// ─── memory_retrieve ──────────────────────────────────────────────────────────

// RetrieveInput is the memory_retrieve tool input (mirrors HTTP POST /v1/retrieve).
type RetrieveInput struct {
	Query        string   `json:"query"`
	Limit        int      `json:"limit,omitempty"`
	From         int64    `json:"from,omitempty"`
	Until        int64    `json:"until,omitempty"`
	Kinds        []string `json:"kinds,omitempty"`
	IncludeLanes bool     `json:"include_lanes,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	Debug        bool     `json:"debug,omitempty"`
	ResponseID   string   `json:"response_id,omitempty"`
	Profile      string   `json:"profile,omitempty"`
}

// RetrieveItem is one result in the memory_retrieve output.
type RetrieveItem struct {
	ID       string   `json:"id"`
	Kind     string   `json:"kind"`
	Content  string   `json:"content"`
	Context  string   `json:"context,omitempty"`
	Score    float64  `json:"score"`
	Citation string   `json:"citation"`
	Lanes    []string `json:"lanes,omitempty"`
}

// ConflictPair is a pair of memory IDs connected by a contradicts link.
type ConflictPair struct {
	A string `json:"a"`
	B string `json:"b"`
}

// RetrieveSupport is the per-response evidence summary.
type RetrieveSupport struct {
	Strength  string         `json:"strength"`
	TopScore  float64        `json:"top_score"`
	Conflicts []ConflictPair `json:"conflicts,omitempty"`
}

// RetrieveOutput is the memory_retrieve tool output.
type RetrieveOutput struct {
	ResponseID     string          `json:"response_id"`
	Items          []RetrieveItem  `json:"items"`
	Support        RetrieveSupport `json:"support"`
	Degraded       bool            `json:"degraded"`
	DegradedRerank bool            `json:"degraded_rerank,omitempty"`
	CacheHit       bool            `json:"cache_hit,omitempty"`
	API            string          `json:"api"`
}

// ─── memory_playbook ─────────────────────────────────────────────────────────

// PlaybookInput is the memory_playbook tool input.
type PlaybookInput struct {
	Query string `json:"query"`
}

// PlaybookOutput is the memory_playbook tool output.
// This tool is a stub placeholder for Phase 17.
type PlaybookOutput struct {
	Error string `json:"error"`
}

// ─── memory_drilldown ────────────────────────────────────────────────────────

// DrilldownInput is the memory_drilldown tool input (mirrors HTTP POST /v1/drilldown).
type DrilldownInput struct {
	MemoryID string `json:"memory_id,omitempty"`
	Citation string `json:"citation,omitempty"`
}

// DrilldownSpan is one provenance span in the drilldown output.
type DrilldownSpan struct {
	RecordID   string `json:"record_id"`
	SpanStart  int    `json:"span_start"`
	SpanEnd    int    `json:"span_end"`
	Excerpt    string `json:"excerpt"`
	OccurredAt int64  `json:"occurred_at"`
	Role       string `json:"role"`
}

// DrilldownOutput is the memory_drilldown tool output.
type DrilldownOutput struct {
	MemoryID string          `json:"memory_id"`
	Spans    []DrilldownSpan `json:"spans"`
}

// ─── memory_feedback ──────────────────────────────────────────────────────────

// FeedbackInput is the memory_feedback tool input (mirrors HTTP POST /v1/feedback).
type FeedbackInput struct {
	ResponseID string `json:"response_id,omitempty"`
	MemoryID   string `json:"memory_id,omitempty"`
	Citation   string `json:"citation,omitempty"`
	Signal     string `json:"signal"`
}

// FeedbackOutput is the memory_feedback tool output.
type FeedbackOutput struct {
	Applied int    `json:"applied"`
	Signal  string `json:"signal"`
}

// ─── memory_assert ────────────────────────────────────────────────────────────

// AssertInput is the memory_assert tool input. action must be "add", "update", or "delete".
type AssertInput struct {
	Action   string `json:"action"`
	MemoryID string `json:"memory_id,omitempty"`
	Content  string `json:"content,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Context  string `json:"context,omitempty"`
}

// AssertOutput is the memory_assert tool output.
type AssertOutput struct {
	MemoryID string `json:"memory_id"`
	Action   string `json:"action"`
	Status   string `json:"status"`
}

// ─── memory_topics ───────────────────────────────────────────────────────────

// TopicItem is one topic in a upsert/delete request.
type TopicItem struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
}

// TopicView is the API-visible representation of one topic.
type TopicView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Pack        string `json:"pack,omitempty"`
	Source      string `json:"source"`
}

// TopicsInput is the memory_topics tool input. action must be "list", "upsert", or "delete".
type TopicsInput struct {
	Action string      `json:"action"`
	Topics []TopicItem `json:"topics,omitempty"`
	Key    string      `json:"key,omitempty"`
}

// TopicsOutput is the memory_topics tool output.
type TopicsOutput struct {
	Topics   []TopicView `json:"topics,omitempty"`
	Upserted int         `json:"upserted,omitempty"`
	Deleted  string      `json:"deleted,omitempty"`
}

// ─── memory reversibility tools (D-070) ───────────────────────────────────────

// MemoryRecord mirrors the HTTP memoryJSON / SDK Memory wire shape. Returned by
// memory_get and memory_rollback.
type MemoryRecord struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Content        string  `json:"content"`
	Context        string  `json:"context,omitempty"`
	Status         string  `json:"status"`
	Importance     int     `json:"importance"`
	Confidence     float64 `json:"confidence"`
	TrustSource    string  `json:"trust_source"`
	MatchCount     int64   `json:"match_count"`
	InjectCount    int64   `json:"inject_count"`
	UseCount       int64   `json:"use_count"`
	SaveCount      int64   `json:"save_count"`
	FailCount      int64   `json:"fail_count,omitempty"`
	NoiseCount     int64   `json:"noise_count,omitempty"`
	Stability      float64 `json:"stability"`
	ValidFrom      int64   `json:"valid_from,omitempty"`
	ValidUntil     int64   `json:"valid_until,omitempty"`
	EpisodeID      string  `json:"episode_id,omitempty"`
	SupersedesID   string  `json:"supersedes_id,omitempty"`
	SupersededByID string  `json:"superseded_by_id,omitempty"`
	PrivacyZone    string  `json:"privacy_zone,omitempty"`
	ContentHash    string  `json:"content_hash,omitempty"`
	CreatedAt      int64   `json:"created_at"`
	UpdatedAt      int64   `json:"updated_at"`
}

// MemoryProvRef is a compact provenance reference in memory_get output.
type MemoryProvRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// ─── memory_get ───────────────────────────────────────────────────────────────

// GetInput is the memory_get tool input (mirrors HTTP GET /v1/memories/{id}).
type GetInput struct {
	MemoryID string `json:"memory_id"`
}

// GetOutput is the memory_get tool output.
type GetOutput struct {
	Memory          MemoryRecord    `json:"memory"`
	Entities        []string        `json:"entities"`
	Keywords        []string        `json:"keywords"`
	Queries         []string        `json:"queries"`
	Provenance      []MemoryProvRef `json:"provenance,omitempty"`
	SupersedesChain []string        `json:"supersedes_chain,omitempty"`
}

// ─── memory_rollback ──────────────────────────────────────────────────────────

// RollbackInput is the memory_rollback tool input (mirrors POST /v1/memories/{id}/rollback).
type RollbackInput struct {
	MemoryID string `json:"memory_id"`
}

// RollbackOutput is the memory_rollback tool output: the restored memory.
type RollbackOutput struct {
	Memory MemoryRecord `json:"memory"`
}

// ─── memory_resolve ───────────────────────────────────────────────────────────

// ResolveInput is the memory_resolve tool input (mirrors PATCH /v1/memories/{id}).
// Action must be "confirm" or "reject".
type ResolveInput struct {
	MemoryID string `json:"memory_id"`
	Action   string `json:"action"`
}

// ResolveOutput is the memory_resolve tool output.
type ResolveOutput struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
