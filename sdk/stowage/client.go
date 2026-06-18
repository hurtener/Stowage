package stowage

import "context"

// Client is the Stowage SDK interface. Both the HTTP and embedded constructors
// return a Client; the same test suite runs against both (AC-1).
//
// Concurrency: implementations must be safe for concurrent use by multiple
// goroutines (D-025 discipline).
type Client interface {
	// Ingest durably appends records and enqueues them for processing.
	// Implements P2 fire-and-forget: ACKs after the durable append; pipeline
	// processing is asynchronous.
	Ingest(ctx context.Context, req IngestRequest) (IngestResponse, error)

	// Retrieve performs four-lane fusion retrieval (lexical, queries, structured,
	// vector) and returns fused + scored results. Returns a degraded response
	// (Degraded:true) when the gateway is unreachable (D-036).
	Retrieve(ctx context.Context, req RetrieveRequest) (RetrieveResponse, error)

	// Drilldown resolves a memory or citation handle to its verbatim provenance
	// spans (RFC P1, D-006).
	Drilldown(ctx context.Context, req DrilldownRequest) (DrilldownResponse, error)

	// Feedback applies a signal (use|save|fail|noise|wrong_citation) to a
	// response, memory, or citation.
	Feedback(ctx context.Context, req FeedbackRequest) (FeedbackResponse, error)

	// ResolveCitations resolves citation handles (injection ULIDs) to their
	// memory summaries and provenance. Per-handle misses are reported as
	// Found:false without failing the batch (AC-5).
	ResolveCitations(ctx context.Context, req ResolveCitationsRequest) (ResolveCitationsResponse, error)

	// Topics lists the effective topics for the client's scope.
	Topics(ctx context.Context) (TopicsResponse, error)

	// Playbook returns the deterministic, sectioned, utility-ranked,
	// budget-packed playbook for the client's scope (RFC §6a.3, D-072). It is
	// LLM-free: assembly reads stored memories and the pure scoring functions
	// only. SessionID, when set, narrows assembly to one session.
	Playbook(ctx context.Context, req PlaybookRequest) (PlaybookResponse, error)

	// Episodes reads the client's episodes + their narratives (RFC §6b, D-080):
	// most-recent-first list, or one episode when ID is set, optionally narrowed by
	// SessionID and the [From,Until] time window. Deterministic and LLM-free.
	Episodes(ctx context.Context, req EpisodesRequest) (EpisodesResponse, error)

	// Causal walks the causal graph from a memory (RFC §5.6/§6b, D-083): backward
	// to its causes ("why did this happen"), forward to its effects, or both, with
	// provenance at every hop. Deterministic and LLM-free.
	Causal(ctx context.Context, req CausalRequest) (CausalResponse, error)

	// GetMemory reads a memory, its junctions, and its supersedes chain within
	// the client's scope (D-070). Returns an error when the memory is absent.
	GetMemory(ctx context.Context, id string) (GetMemoryResponse, error)

	// Rollback inverts the newest reconciliation event for a memory (D-064),
	// restoring its prior state. Returns the restored memory. A reversibility
	// conflict (double-rollback, downstream supersede, missing snapshot) is
	// returned as an error identical across surfaces.
	Rollback(ctx context.Context, req RollbackRequest) (Memory, error)

	// ResolveMemory confirms or rejects a pending_confirmation memory (D-065).
	// confirm promotes it to active (superseding any target); reject expires it.
	ResolveMemory(ctx context.Context, req ResolveRequest) (ResolveResponse, error)

	// ── Tier-A control verbs (D-071): single-user verbs on {SDK, MCP, HTTP} ──

	// UpsertTopics upserts extraction topics in the client's scope (D-043).
	// Opting out of the virtual default pack is an upsert of {Key: "pack:off"}.
	UpsertTopics(ctx context.Context, req UpsertTopicsRequest) (UpsertTopicsResponse, error)

	// DeleteTopic soft-deletes a topic by key in the client's scope (D-043).
	DeleteTopic(ctx context.Context, key string) (DeleteTopicResponse, error)

	// Flush flushes a named buffer key with trigger explicit|session_end (D-071).
	Flush(ctx context.Context, req FlushRequest) (FlushResponse, error)

	// ForkBranch forks a new branch for a session (D-029).
	ForkBranch(ctx context.Context, req ForkBranchRequest) (ForkBranchResponse, error)

	// MergeBranch transitions a branch to merged (D-029).
	MergeBranch(ctx context.Context, branchID string) (BranchResponse, error)

	// DiscardBranch transitions a branch to discarded; its buffered turns are
	// flushed with SkipPromotion (never promoted to memories) (D-029).
	DiscardBranch(ctx context.Context, branchID string) (BranchResponse, error)

	// Assert directly adds, updates, or deletes a memory, bypassing the ingest
	// pipeline (D-071). This is an embedded-host escape hatch; the HTTP surface
	// deliberately omits it (writes stay routed through the pipeline there).
	Assert(ctx context.Context, req AssertRequest) (AssertResponse, error)
}
