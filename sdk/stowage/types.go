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

// ---- Tier-A control verbs (D-071) -------------------------------------------

// TopicUpsert is one topic to upsert via UpsertTopics. Opting out of the
// virtual default pack is an upsert of {Key: "pack:off"} (D-043).
type TopicUpsert struct {
	Key         string `json:"key"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"` // defaults to "active"
}

// UpsertTopicsRequest is the request for UpsertTopics.
type UpsertTopicsRequest struct {
	Topics []TopicUpsert `json:"topics"`
}

// UpsertTopicsResponse is the response from UpsertTopics.
type UpsertTopicsResponse struct {
	Upserted int `json:"upserted"`
}

// DeleteTopicResponse is the response from DeleteTopic.
type DeleteTopicResponse struct {
	Deleted string `json:"deleted"`
}

// FlushRequest flushes a named buffer key (D-071). Trigger must be "explicit"
// (default) or "session_end".
type FlushRequest struct {
	Key     string `json:"-"`
	Trigger string `json:"trigger,omitempty"`
}

// FlushResponse is the response from Flush.
type FlushResponse struct {
	Key     string `json:"key"`
	Trigger string `json:"trigger"`
	Flushed bool   `json:"flushed"`
}

// ForkBranchRequest forks a new branch for a session (D-029). SessionID required.
type ForkBranchRequest struct {
	SessionID      string `json:"session_id"`
	ParentBranchID string `json:"parent_branch_id,omitempty"`
}

// ForkBranchResponse is the response from ForkBranch.
type ForkBranchResponse struct {
	BranchID string `json:"branch_id"`
}

// BranchResponse is the response from MergeBranch / DiscardBranch.
type BranchResponse struct {
	BranchID string `json:"branch_id"`
	Status   string `json:"status"`
}

// AssertRequest directly asserts a memory, bypassing the pipeline (D-071).
// Action must be "add", "update", or "delete".
type AssertRequest struct {
	Action   string `json:"action"`
	MemoryID string `json:"memory_id,omitempty"`
	Content  string `json:"content,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Context  string `json:"context,omitempty"`
	// Review (action=add) parks the memory as pending_review instead of active —
	// the uncited-claim safeguard (§6c, Phase 25); resolve it via Review.
	Review bool `json:"review,omitempty"`
}

// AssertResponse is the response from Assert.
type AssertResponse struct {
	MemoryID string `json:"memory_id"`
	Action   string `json:"action"`
	Status   string `json:"status"`
}

// ---- Verification + review queue (Phase 25, RFC §6c, D-084) ------------------

// VerifyRequest checks that Claim is entailed by the memories behind Citations
// (injection handles from a prior Retrieve).
type VerifyRequest struct {
	Claim     string   `json:"claim"`
	Citations []string `json:"citations,omitempty"`
}

// VerifyResponse is the entailment verdict. Degraded is set when the gateway was
// unreachable (verdict falls back to "unclear").
type VerifyResponse struct {
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation,omitempty"`
	Degraded    bool    `json:"degraded,omitempty"`
}

// ReviewRequest drives the review queue. Action ∈ {list, approve, reject}; list
// paginates pending_review memories, approve/reject resolve one by MemoryID.
type ReviewRequest struct {
	Action   string `json:"action"`
	MemoryID string `json:"memory_id,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Cursor   string `json:"cursor,omitempty"`
}

// ReviewItem is one pending_review memory.
type ReviewItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	Context   string `json:"context,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ReviewResponse is the review-queue envelope. For list: Items + NextCursor. For
// approve/reject: ID + Status.
type ReviewResponse struct {
	Items      []ReviewItem `json:"items,omitempty"`
	NextCursor string       `json:"next_cursor,omitempty"`
	ID         string       `json:"id,omitempty"`
	Status     string       `json:"status,omitempty"`
}

// ---- Reasoning traces (Phase 26, RFC §6c, D-086) -----------------------------

// TraceRequest exports the reasoning trace for a response_id.
type TraceRequest struct {
	ResponseID string `json:"response_id"`
}

// TraceSpan is one drill-down provenance span.
type TraceSpan struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

// TraceLink is one typed edge out of an injected memory.
type TraceLink struct {
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence,omitempty"`
}

// TraceItem is one injected memory and its chain.
type TraceItem struct {
	MemoryID   string      `json:"memory_id"`
	Kind       string      `json:"kind"`
	Content    string      `json:"content"`
	Status     string      `json:"status"`
	Rank       int         `json:"rank"`
	Score      float64     `json:"score"`
	Lane       string      `json:"lane,omitempty"`
	WasCited   bool        `json:"was_cited,omitempty"`
	Feedback   string      `json:"feedback,omitempty"`
	Provenance []TraceSpan `json:"provenance,omitempty"`
	Links      []TraceLink `json:"links,omitempty"`
}

// TraceVerdict is one verification verdict run against the response.
type TraceVerdict struct {
	Claim      string  `json:"claim"`
	Verdict    string  `json:"verdict"`
	Confidence float64 `json:"confidence,omitempty"`
	Degraded   bool    `json:"degraded,omitempty"`
}

// Trace is the full reasoning chain for one response_id.
type Trace struct {
	ResponseID  string         `json:"response_id"`
	Query       string         `json:"query,omitempty"`
	Support     string         `json:"support,omitempty"`
	Degraded    bool           `json:"degraded,omitempty"`
	Items       []TraceItem    `json:"items"`
	Verdicts    []TraceVerdict `json:"verdicts,omitempty"`
	GeneratedAt int64          `json:"generated_at"`
}

// TraceResponse is the exported bundle: the trace plus an optional ed25519 detached
// signature + public key for third-party audit verification (signed:false when the
// server has no signing key configured).
type TraceResponse struct {
	Trace     Trace  `json:"trace"`
	Signed    bool   `json:"signed"`
	Algorithm string `json:"algorithm,omitempty"`
	PublicKey string `json:"public_key,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// ---- Proactive suggestions (Phase 27, RFC §6d, D-087) ------------------------

// SuggestionsRequest drives the proactive pull. Action ∈ {list (default), accept,
// dismiss}. list evaluates+offers for SessionID (with the optional Query context);
// accept/dismiss resolve the offer ID with feedback.
type SuggestionsRequest struct {
	Action    string // "list" | "accept" | "dismiss"
	SessionID string
	Query     string
	ID        string // the suggestion id (accept/dismiss)
}

// Suggestion is one proactive offer (byte-identical to the HTTP/MCP shape). Content
// carries the offered memory's text inline so the agent can act without a round-trip.
type Suggestion struct {
	ID          string  `json:"id"`
	TriggerKind string  `json:"trigger_kind"`
	MemoryID    string  `json:"memory_id"`
	EpisodeID   string  `json:"episode_id,omitempty"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Score       float64 `json:"score"`
}

// SuggestionsResponse carries the offers (list) or the resolved ID+Status
// (accept/dismiss).
type SuggestionsResponse struct {
	Suggestions []Suggestion `json:"suggestions"`
	Degraded    bool         `json:"degraded,omitempty"`
	ID          string       `json:"id,omitempty"`
	Status      string       `json:"status,omitempty"`
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

// ---- Playbook types (D-072) -------------------------------------------------

// PlaybookRequest is the request for Playbook. SessionID, when set, narrows
// assembly to a single session (session-affinity). The token budget is
// profile-internal (D-034/D-042) — there is no client-supplied limit.
type PlaybookRequest struct {
	SessionID string `json:"session_id,omitempty"`
}

// PlaybookProvenanceRef is a compact provenance span reference for P1 drill-down.
type PlaybookProvenanceRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// PlaybookItem is one ranked memory in a playbook section.
type PlaybookItem struct {
	MemoryID   string                  `json:"memory_id"`
	Kind       string                  `json:"kind"`
	Content    string                  `json:"content"`
	Score      float64                 `json:"score"`
	Provenance []PlaybookProvenanceRef `json:"provenance,omitempty"`
}

// PlaybookSection groups the packed items of a single kind.
type PlaybookSection struct {
	Title string         `json:"title"`
	Kind  string         `json:"kind"`
	Items []PlaybookItem `json:"items"`
}

// PlaybookBudget reports how the token budget was spent.
type PlaybookBudget struct {
	TokenBudget int `json:"token_budget"`
	TokensUsed  int `json:"tokens_used"`
	ItemsTotal  int `json:"items_total"`
	ItemsPacked int `json:"items_packed"`
}

// PlaybookResponse is the response from Playbook: the deterministic, sectioned,
// utility-ranked, budget-packed view over the scope's strategy/failure_mode/
// building-block memories (RFC §6a.3, D-072).
type PlaybookResponse struct {
	Sections []PlaybookSection `json:"sections"`
	Budget   PlaybookBudget    `json:"budget"`
}

// EpisodesRequest reads episodes (RFC §6b, D-080). ID returns one episode; else a
// most-recent-first list, narrowed by SessionID and the [From,Until] window (unix
// millis; 0 = unbounded). Limit/Cursor paginate the unfiltered list.
type EpisodesRequest struct {
	ID        string
	Limit     int
	Cursor    string
	SessionID string
	From      int64
	Until     int64
	// SimilarTo, when set, vector-ranks the scope's episodes by narrative
	// similarity to this text (§6b contrast, Phase 23b); K caps the results.
	SimilarTo string
	K         int
	// ArcOf, when set, returns the cross-session arc of the given episode id —
	// the episodes threaded to it via relates_to edges (§6b threading, Phase 24b).
	ArcOf string
}

// Episode is one episode + its narrative.
type Episode struct {
	ID                string  `json:"id"`
	SessionID         string  `json:"session_id"`
	Title             string  `json:"title"`
	Status            string  `json:"status"`
	Outcome           string  `json:"outcome,omitempty"`
	StartedAt         int64   `json:"started_at"`
	EndedAt           int64   `json:"ended_at"`
	NarrativeMemoryID string  `json:"narrative_memory_id,omitempty"`
	Narrative         string  `json:"narrative,omitempty"`
	Score             float64 `json:"score,omitempty"`
}

// EpisodesResponse is the episodic-retrieval envelope.
type EpisodesResponse struct {
	Episodes   []Episode `json:"episodes"`
	NextCursor string    `json:"next_cursor,omitempty"`
	Degraded   bool      `json:"degraded,omitempty"`
}

// CausalRequest walks the causal graph from MemoryID (RFC §5.6/§6b, D-083).
// Direction is "backward" (causes — the default), "forward" (effects), or "both".
// Depth bounds the hops (default 3, capped server-side).
type CausalRequest struct {
	MemoryID  string
	Direction string
	Depth     int
}

// CausalProvRef is a compact provenance span for P1 drill-down at a node.
type CausalProvRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// CausalNode is one memory in the causal graph.
type CausalNode struct {
	MemoryID   string          `json:"memory_id"`
	Kind       string          `json:"kind"`
	Content    string          `json:"content"`
	Context    string          `json:"context,omitempty"`
	EpisodeID  string          `json:"episode_id,omitempty"`
	Provenance []CausalProvRef `json:"provenance,omitempty"`
}

// CausalEdge is a canonical cause→effect edge.
type CausalEdge struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// CausalResponse is the why-traversal envelope.
type CausalResponse struct {
	Root      string       `json:"root"`
	Nodes     []CausalNode `json:"nodes"`
	Edges     []CausalEdge `json:"edges"`
	Truncated bool         `json:"truncated,omitempty"`
}
