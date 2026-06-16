// Package stowage is the public Go SDK for the Stowage memory server.
// It exposes two constructors (NewHTTP, NewEmbedded) that share ONE Client
// interface and ONE test suite (AC-1). Types mirror the HTTP v1 API envelope
// exactly (citation, response_id, degraded flags).
//
// Wire-format parity: the JSON field names in this package's types are
// intentionally identical to the internal/api handler types. Any rename in
// internal/api must be mirrored here (enforced by parity_test.go).
package stowage

// ---- Ingest types -----------------------------------------------------------

// RecordInput is one record to ingest.
type RecordInput struct {
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
	OccurredAt    int64  `json:"occurred_at,omitempty"` // unix millis; 0 → now
	BufferKey     string `json:"buffer_key,omitempty"`
}

// IngestRequest is the request for Ingest.
type IngestRequest struct {
	Records []RecordInput `json:"records"`
}

// IngestResponse is the response from Ingest.
type IngestResponse struct {
	IDs      []string `json:"ids"`
	Enqueued bool     `json:"enqueued"`
}

// ---- Retrieve types ---------------------------------------------------------

// RetrieveRequest is the request for Retrieve.
type RetrieveRequest struct {
	Query        string   `json:"query"`
	Limit        int      `json:"limit,omitempty"`
	From         int64    `json:"from,omitempty"`  // unix millis; 0 = unbounded
	Until        int64    `json:"until,omitempty"` // unix millis; 0 = unbounded
	Kinds        []string `json:"kinds,omitempty"`
	IncludeLanes bool     `json:"include_lanes,omitempty"`
	SessionID    string   `json:"session_id,omitempty"`
	Debug        bool     `json:"debug,omitempty"`
	ResponseID   string   `json:"response_id,omitempty"`
	Profile      string   `json:"profile,omitempty"` // precise|balanced|broad
}

// RetrieveBreakdown is the per-item scoring breakdown (present when Debug:true).
type RetrieveBreakdown struct {
	UseBoost         float64 `json:"use_boost"`
	NoisePenalty     float64 `json:"noise_penalty"`
	PrecisionFactor  float64 `json:"precision_factor"`
	ExplorationBonus float64 `json:"exploration_bonus"`
	DecayFactor      float64 `json:"decay_factor"`
	TrustMultiplier  float64 `json:"trust_multiplier"`
	ScopeAffinity    float64 `json:"scope_affinity"`
	TemporalBoost    float64 `json:"temporal_boost"`
	HubDampening     float64 `json:"hub_dampening"`
	Cooldown         float64 `json:"cooldown"`
	ImportanceMult   float64 `json:"importance_mult"`
	FinalScore       float64 `json:"final_score"`
}

// RetrieveConflict is a pair of contradicting memory IDs.
type RetrieveConflict struct {
	A string `json:"a"`
	B string `json:"b"`
}

// RetrieveSupport is the per-response evidence summary.
type RetrieveSupport struct {
	Strength  string             `json:"strength"`
	TopScore  float64            `json:"top_score"`
	Conflicts []RetrieveConflict `json:"conflicts,omitempty"`
}

// MemoryItem is one retrieval result (envelope v1).
type MemoryItem struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`
	Content   string             `json:"content"`
	Context   string             `json:"context,omitempty"`
	Score     float64            `json:"score"`
	Citation  string             `json:"citation"` // injection ULID = citation handle (D-051)
	Lanes     []string           `json:"lanes,omitempty"`
	Breakdown *RetrieveBreakdown `json:"breakdown,omitempty"`
}

// RetrieveResponse is the response from Retrieve.
type RetrieveResponse struct {
	ResponseID     string          `json:"response_id"`
	Items          []MemoryItem    `json:"items"`
	Support        RetrieveSupport `json:"support"`
	Degraded       bool            `json:"degraded"`
	DegradedRerank bool            `json:"degraded_rerank,omitempty"`
	CacheHit       bool            `json:"cache_hit,omitempty"`
	API            string          `json:"api"`
}

// ---- Drilldown types --------------------------------------------------------

// DrilldownRequest is the request for Drilldown.
// Exactly one of MemoryID or Citation must be set.
type DrilldownRequest struct {
	MemoryID string `json:"memory_id,omitempty"`
	Citation string `json:"citation,omitempty"`
}

// DrilldownSpan is one provenance span in the drilldown response.
type DrilldownSpan struct {
	RecordID   string `json:"record_id"`
	SpanStart  int    `json:"span_start"`
	SpanEnd    int    `json:"span_end"`
	Excerpt    string `json:"excerpt"`
	OccurredAt int64  `json:"occurred_at"`
	Role       string `json:"role"`
}

// DrilldownResponse is the response from Drilldown.
type DrilldownResponse struct {
	MemoryID string          `json:"memory_id"`
	Spans    []DrilldownSpan `json:"spans"`
}

// ---- Feedback types ---------------------------------------------------------

// FeedbackRequest is the request for Feedback.
// Exactly one of ResponseID, MemoryID, or Citation must be set.
type FeedbackRequest struct {
	ResponseID string `json:"response_id,omitempty"`
	MemoryID   string `json:"memory_id,omitempty"`
	Citation   string `json:"citation,omitempty"`
	Signal     string `json:"signal"` // use|save|fail|noise|wrong_citation
}

// FeedbackResponse is the response from Feedback.
type FeedbackResponse struct {
	Applied int    `json:"applied"`
	Signal  string `json:"signal"`
}

// ---- ResolveCitations types -------------------------------------------------

// ResolveCitationsRequest is the request for ResolveCitations.
type ResolveCitationsRequest struct {
	Citations []string `json:"citations"`
}

// ResolveProvenanceRef is a single provenance span reference.
type ResolveProvenanceRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start"`
	SpanEnd   int    `json:"span_end"`
}

// ResolveMemory is the memory summary in a resolved citation.
type ResolveMemory struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Content    string  `json:"content"`
	Context    string  `json:"context,omitempty"`
	Importance int     `json:"importance"`
	Confidence float64 `json:"confidence"`
	CreatedAt  int64   `json:"created_at"`
}

// ResolveItem is the per-citation result.
type ResolveItem struct {
	Citation   string                 `json:"citation"`
	Found      bool                   `json:"found"`
	Memory     *ResolveMemory         `json:"memory,omitempty"`
	Provenance []ResolveProvenanceRef `json:"provenance,omitempty"`
	Rank       int                    `json:"rank,omitempty"`
	Score      float64                `json:"score,omitempty"`
	Lanes      []string               `json:"lanes,omitempty"`
}

// ResolveCitationsResponse is the response from ResolveCitations.
type ResolveCitationsResponse struct {
	Items []ResolveItem `json:"items"`
}

// ---- Topics types -----------------------------------------------------------

// TopicView is one topic in the topics list.
type TopicView struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Pack        string `json:"pack,omitempty"`
	Source      string `json:"source"`
}

// TopicsResponse is the response from Topics (list).
type TopicsResponse struct {
	Topics []TopicView `json:"topics"`
}

// ---- Memory / reversibility types (D-070) -----------------------------------

// Memory mirrors the HTTP memoryJSON wire type. It is returned by Rollback and
// embedded in GetMemoryResponse. Field names are byte-identical to the v1 API.
type Memory struct {
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

// MemoryProvenanceRef is a compact provenance reference (mirrors provRefJSON).
type MemoryProvenanceRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// GetMemoryResponse mirrors the HTTP memoryResponse for GET /v1/memories/{id}.
type GetMemoryResponse struct {
	Memory          Memory                `json:"memory"`
	Entities        []string              `json:"entities"`
	Keywords        []string              `json:"keywords"`
	Queries         []string              `json:"queries"`
	Provenance      []MemoryProvenanceRef `json:"provenance,omitempty"`
	SupersedesChain []string              `json:"supersedes_chain,omitempty"`
}

// RollbackRequest identifies the memory to roll back (D-064).
type RollbackRequest struct {
	MemoryID string `json:"-"`
}

// ResolveRequest confirms or rejects a pending_confirmation memory (D-065).
// Action must be "confirm" or "reject".
type ResolveRequest struct {
	MemoryID string `json:"-"`
	Action   string `json:"action"`
}

// ResolveResponse is the result of ResolveMemory (mirrors the PATCH response).
type ResolveResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// ---- Playbook types ---------------------------------------------------------

// PlaybookRequest is the request for Playbook. Stub in Phase 17.
type PlaybookRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// PlaybookResponse is the response from Playbook. Stub in Phase 17.
type PlaybookResponse struct {
	// Entries will be populated in a future phase.
	Entries []any `json:"entries"`
	Stub    bool  `json:"stub"` // true in Phase 17 — full assembly lands later
}
