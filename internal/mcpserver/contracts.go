// Package mcpserver implements the Stowage MCP tool surface over the Dockyard
// runtime library (RFC §9.2, D-015, D-018, D-020, D-061).
//
// Fourteen typed tools mirror the HTTP v1 surfaces: the original seven, the
// D-070 reversibility trio (memory_get, memory_rollback, memory_resolve), and the
// D-071 Tier control verbs (memory_flush, memory_branch, and the Tier-B
// memory_grants). Tool handlers share the same store/service core code the HTTP
// handlers use — no business logic is duplicated (AC-3). Schema goldens in
// testdata/ are the contract gate (D-061).
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

// IngestTargetScope is the optional contribute-mode target scope (D-059, D-071).
//
// Contribute-mode is now honored on the MCP surface: when TargetScope is set the
// records are committed into the pool-owner's scope, subject to an active
// contribute grant covering the caller (identified by ContributorUserID). The
// grant-check + scope-override is the shared grants.AuthorizeContribute core that
// the HTTP /v1/records path also uses, so the two surfaces cannot drift. Without
// a covering grant the request is rejected (never silently mis-scoped).
type IngestTargetScope struct {
	ProjectID string `json:"project_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// IngestInput is the memory_ingest tool input. Records ingest into the caller's
// own scope unless TargetScope is set (contribute-mode, D-059/D-071), in which
// case they ingest into the pool-owner's scope under a covering contribute grant.
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
	// ProjectID/UserID scope the read to a sub-tenant identity (P3, D-125); empty = tenant-wide.
	ProjectID  string `json:"project_id,omitempty"`
	UserID     string `json:"user_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Debug      bool   `json:"debug,omitempty"`
	ResponseID string `json:"response_id,omitempty"`
	Profile    string `json:"profile,omitempty"`
}

// RetrieveItem is one result in the memory_retrieve output.
type RetrieveItem struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	Content             string   `json:"content"`
	Context             string   `json:"context,omitempty"`
	Score               float64  `json:"score"`
	Citation            string   `json:"citation"`
	Lanes               []string `json:"lanes,omitempty"`
	Stale               bool     `json:"stale,omitempty"`                 // D-105: superseded value (dual-visibility, §6c)
	SupersededBy        string   `json:"superseded_by,omitempty"`         // successor memory ID
	SupersededByContent string   `json:"superseded_by_content,omitempty"` // D-114: successor's current value inline
	SupersededByDate    int64    `json:"superseded_by_date,omitempty"`    // D-114: successor's assertion date, unix millis
	OccurredAt          int64    `json:"occurred_at,omitempty"`           // D-109: assertion (conversation) date, unix millis
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

// ─── memory_playbook (D-072) ───────────────────────────────────────────────────

// PlaybookInput is the memory_playbook tool input. SessionID, when set, narrows
// assembly to a single session (session-affinity). The token budget is
// profile-internal (D-034/D-042) — there is no client-supplied limit.
type PlaybookInput struct {
	SessionID string `json:"session_id,omitempty"`
}

// PlaybookProvRef is a compact provenance span reference for P1 drill-down.
type PlaybookProvRef struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// PlaybookItem is one ranked memory in a playbook section.
type PlaybookItem struct {
	MemoryID   string            `json:"memory_id"`
	Kind       string            `json:"kind"`
	Content    string            `json:"content"`
	Score      float64           `json:"score"`
	Provenance []PlaybookProvRef `json:"provenance,omitempty"`
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

// PlaybookOutput is the memory_playbook tool output: the deterministic,
// sectioned, utility-ranked, budget-packed playbook (RFC §6a.3, D-072).
type PlaybookOutput struct {
	Sections []PlaybookSection `json:"sections"`
	Budget   PlaybookBudget    `json:"budget"`
}

// EpisodesInput is the memory_episodes tool input (RFC §6b, D-080). ID returns one
// episode; otherwise a most-recent-first list narrowed by SessionID + the
// [From,Until] time window (unix millis; 0 = unbounded). Limit/Cursor paginate.
type EpisodesInput struct {
	ID        string `json:"id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	From      int64  `json:"from,omitempty"`
	Until     int64  `json:"until,omitempty"`
	// SimilarTo, when set, vector-ranks the scope's episodes by narrative
	// similarity to this situation (§6b contrast, Phase 23b/D-082); K caps the
	// result count (default 5). The deterministic list path is unaffected.
	SimilarTo string `json:"similar_to,omitempty"`
	K         int    `json:"k,omitempty"`
	// ArcOf, when set, returns the cross-session arc of the given episode id — the
	// episodes threaded to it via relates_to edges (§6b threading, Phase 24b/D-081).
	ArcOf string `json:"arc_of,omitempty"`
}

// EpisodeItem is one episode + its narrative (byte-identical to the HTTP/SDK shape).
type EpisodeItem struct {
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

// EpisodesOutput is the memory_episodes tool output.
type EpisodesOutput struct {
	Episodes   []EpisodeItem `json:"episodes"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Degraded   bool          `json:"degraded,omitempty"`
}

// ─── memory_causal (D-083) ─────────────────────────────────────────────────────

// CausalInput is the memory_causal tool input (RFC §5.6/§6b). Direction ∈
// {backward (causes; default), forward (effects), both}; Depth bounds the hops.
type CausalInput struct {
	MemoryID  string `json:"memory_id"`
	Direction string `json:"direction,omitempty"`
	Depth     int    `json:"depth,omitempty"`
}

// CausalProvRefItem is a compact provenance span for P1 drill-down at a node.
type CausalProvRefItem struct {
	RecordID  string `json:"record_id"`
	SpanStart int    `json:"span_start,omitempty"`
	SpanEnd   int    `json:"span_end,omitempty"`
}

// CausalNodeItem is one memory in the causal graph (byte-identical to HTTP/SDK).
type CausalNodeItem struct {
	MemoryID   string              `json:"memory_id"`
	Kind       string              `json:"kind"`
	Content    string              `json:"content"`
	Context    string              `json:"context,omitempty"`
	EpisodeID  string              `json:"episode_id,omitempty"`
	Provenance []CausalProvRefItem `json:"provenance,omitempty"`
}

// CausalEdgeItem is a canonical cause→effect edge.
type CausalEdgeItem struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	Type       string  `json:"type"`
	Confidence float64 `json:"confidence"`
}

// CausalOutput is the memory_causal tool output.
type CausalOutput struct {
	Root      string           `json:"root"`
	Nodes     []CausalNodeItem `json:"nodes"`
	Edges     []CausalEdgeItem `json:"edges"`
	Truncated bool             `json:"truncated,omitempty"`
}

// ─── memory_verify (D-084) ─────────────────────────────────────────────────────

// VerifyInput is the memory_verify tool input (mirrors HTTP POST /v1/verify): a claim
// + the citation handles (injection IDs) it was drafted from.
type VerifyInput struct {
	Claim     string   `json:"claim"`
	Citations []string `json:"citations,omitempty"`
}

// VerifyOutput is the memory_verify tool output: the entailment verdict.
type VerifyOutput struct {
	Verdict     string  `json:"verdict"`
	Confidence  float64 `json:"confidence"`
	Explanation string  `json:"explanation,omitempty"`
	Degraded    bool    `json:"degraded,omitempty"`
}

// ─── memory_review (D-084) ─────────────────────────────────────────────────────

// ReviewInput is the memory_review tool input. action ∈ {list, approve, reject}.
// list paginates the scope's pending_review memories; approve/reject resolve one by id.
type ReviewInput struct {
	Action string `json:"action"`
	ID     string `json:"id,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

// ReviewItem is one pending_review memory in the queue.
type ReviewItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Content   string `json:"content"`
	Context   string `json:"context,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

// ReviewOutput is the memory_review tool output. For list: items + next_cursor. For
// approve/reject: id + status (the resolved status).
type ReviewOutput struct {
	Items      []ReviewItem `json:"items,omitempty"`
	NextCursor string       `json:"next_cursor,omitempty"`
	ID         string       `json:"id,omitempty"`
	Status     string       `json:"status,omitempty"`
}

// ─── memory_trace (D-086) ──────────────────────────────────────────────────────

// TraceInput is the memory_trace tool input: the response_id whose reasoning trace to
// export. The output is a traces.Bundle (the trace + optional ed25519 signature) —
// byte-identical to GET /v1/traces/{response_id} and the SDK TraceResponse.
type TraceInput struct {
	ResponseID string `json:"response_id"`
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
	// Review (action=add) parks the memory as pending_review instead of active — the
	// uncited-claim safeguard (§6c, D-084); resolve it via memory_review.
	Review bool `json:"review,omitempty"`
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

// ─── memory_flush (D-071) ──────────────────────────────────────────────────────

// FlushInput is the memory_flush tool input (mirrors POST /v1/buffers/{key}/flush).
// Trigger must be "explicit" (default) or "session_end".
type FlushInput struct {
	Key     string `json:"key"`
	Trigger string `json:"trigger,omitempty"`
}

// FlushOutput is the memory_flush tool output.
type FlushOutput struct {
	Key     string `json:"key"`
	Trigger string `json:"trigger"`
	Flushed bool   `json:"flushed"`
}

// ─── memory_branch (D-029, D-071) ──────────────────────────────────────────────

// BranchInput is the memory_branch tool input. action must be "fork", "merge",
// or "discard" (mirrors POST /v1/branches).
type BranchInput struct {
	Action         string `json:"action"`
	SessionID      string `json:"session_id,omitempty"`       // required for fork
	BranchID       string `json:"branch_id,omitempty"`        // required for merge/discard
	ParentBranchID string `json:"parent_branch_id,omitempty"` // optional for fork
}

// BranchOutput is the memory_branch tool output.
type BranchOutput struct {
	BranchID string `json:"branch_id"`
	Status   string `json:"status"`
}

// ─── memory_grants (Tier B — D-016, D-071) ─────────────────────────────────────

// GrantsInput is the action-tagged memory_grants tool input. action ∈
// {create_group, list_groups, add_member, remove_member, list_members,
// create_grant, list_grants, revoke_grant}. This is a multi-user/admin verb
// (Tier B): reachable on {HTTP, MCP} only, never the single-user SDK (D-067).
type GrantsInput struct {
	Action string `json:"action"`
	// Group ops.
	Name    string `json:"name,omitempty"`     // create_group
	GroupID string `json:"group_id,omitempty"` // member ops + create_grant
	UserID  string `json:"user_id,omitempty"`  // member ops + grant owner scope
	// Grant owner-scope + grant fields.
	ProjectID        string `json:"project_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	Access           string `json:"access,omitempty"` // read|contribute
	TopicFilter      string `json:"topic_filter,omitempty"`
	KindFilter       string `json:"kind_filter,omitempty"`
	ZoneCeiling      string `json:"zone_ceiling,omitempty"` // public|work
	RedactionProfile string `json:"redaction_profile,omitempty"`
	GrantID          string `json:"grant_id,omitempty"` // revoke_grant
}

// GrantGroup mirrors the HTTP groupResponse wire shape.
type GrantGroup struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

// GrantMember mirrors the HTTP memberResponse wire shape.
type GrantMember struct {
	ID        string `json:"id"`
	GroupID   string `json:"group_id"`
	UserID    string `json:"user_id"`
	TenantID  string `json:"tenant_id"`
	CreatedAt int64  `json:"created_at"`
}

// GrantRecord mirrors the HTTP grantResponse wire shape.
type GrantRecord struct {
	ID               string `json:"id"`
	TenantID         string `json:"tenant_id"`
	ProjectID        string `json:"project_id,omitempty"`
	UserID           string `json:"user_id,omitempty"`
	SessionID        string `json:"session_id,omitempty"`
	GroupID          string `json:"group_id"`
	Access           string `json:"access"`
	TopicFilter      string `json:"topic_filter,omitempty"`
	KindFilter       string `json:"kind_filter,omitempty"`
	ZoneCeiling      string `json:"zone_ceiling"`
	RedactionProfile string `json:"redaction_profile,omitempty"`
	RevokedAt        int64  `json:"revoked_at,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

// GrantsOutput is the memory_grants tool output; only the fields relevant to the
// requested action are populated.
type GrantsOutput struct {
	Group   *GrantGroup   `json:"group,omitempty"`
	Groups  []GrantGroup  `json:"groups,omitempty"`
	Member  *GrantMember  `json:"member,omitempty"`
	Members []GrantMember `json:"members,omitempty"`
	Grant   *GrantRecord  `json:"grant,omitempty"`
	Grants  []GrantRecord `json:"grants,omitempty"`
	Removed bool          `json:"removed,omitempty"`
	Revoked string        `json:"revoked,omitempty"`
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

// ─── memory_suggestions (Phase 27, D-087) ──────────────────────────────────────

// SuggestionsInput is the memory_suggestions tool input (RFC §6d). Action ∈
// {list (default), accept, dismiss}. list evaluates+offers for SessionID (+Query);
// accept/dismiss resolve the offer ID with feedback.
type SuggestionsInput struct {
	Action    string `json:"action,omitempty"` // "list" | "accept" | "dismiss"
	SessionID string `json:"session_id,omitempty"`
	Query     string `json:"query,omitempty"`
	ID        string `json:"id,omitempty"` // the suggestion id (accept/dismiss)
}

// SuggestionItem is one proactive offer (byte-identical to the HTTP/SDK shape).
type SuggestionItem struct {
	ID          string  `json:"id"`
	TriggerKind string  `json:"trigger_kind"`
	MemoryID    string  `json:"memory_id"`
	EpisodeID   string  `json:"episode_id,omitempty"`
	Title       string  `json:"title"`
	Content     string  `json:"content"`
	Score       float64 `json:"score"`
}

// SuggestionsOutput is the memory_suggestions tool output. For list it carries the
// offers (+Degraded); for accept/dismiss it carries the resolved ID + Status.
type SuggestionsOutput struct {
	Suggestions []SuggestionItem `json:"suggestions"`
	Degraded    bool             `json:"degraded,omitempty"`
	ID          string           `json:"id,omitempty"`
	Status      string           `json:"status,omitempty"`
}

// ─── memory_proactive_config (Phase 27, D-087) ──────────────────────────────────

// ProactiveConfigInput is the memory_proactive_config tool input (admin tier).
// Action ∈ {get (default), set}. set PATCHES the scope's governance: each governance
// field is optional (pointer) and only overwrites when present, so setting one field
// (e.g. threshold) never zero-wipes the rest. User/Project refine the scope.
type ProactiveConfigInput struct {
	Action    string          `json:"action,omitempty"` // "get" | "set"
	User      string          `json:"user,omitempty"`
	Project   string          `json:"project,omitempty"`
	Enabled   *bool           `json:"enabled,omitempty"`
	Threshold *float64        `json:"threshold,omitempty"`
	Budget    *int            `json:"budget,omitempty"`
	Classes   map[string]bool `json:"classes,omitempty"`
}

// ProactiveConfigOutput is the memory_proactive_config tool output: the effective
// (resolved + clamped) governance for the scope.
type ProactiveConfigOutput struct {
	Enabled   bool            `json:"enabled"`
	Threshold float64         `json:"threshold"`
	Budget    int             `json:"budget"`
	Classes   map[string]bool `json:"classes"`
}
