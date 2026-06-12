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

	// Playbook returns a stub response in Phase 17; full assembly lands in
	// a future phase once playbook storage is wired. Returns Stub:true always
	// until that phase ships.
	Playbook(ctx context.Context, req PlaybookRequest) (PlaybookResponse, error)
}
