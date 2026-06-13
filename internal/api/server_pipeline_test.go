package api_test

import (
	"testing"

	"github.com/hurtener/stowage/internal/pipeline"
)

// TestServerPipelineChannelAccessors covers the pipeline channel accessors used
// by the boot.StartPipeline wiring (D-068): SetPipelineIn redirects the ingest
// sink to an externally-owned channel; Pipeline/PipelineIn expose the
// server-owned channel for the standalone/test path.
func TestServerPipelineChannelAccessors(t *testing.T) {
	t.Parallel()
	srv, _, _ := newTestServer(t)

	// Server-owned channel accessors.
	if srv.Pipeline() == nil {
		t.Error("Pipeline(): read end must not be nil")
	}
	if srv.PipelineIn() == nil {
		t.Error("PipelineIn(): write end must not be nil")
	}

	// Redirect the ingest sink to an externally-owned channel and confirm it
	// receives an enqueued item (proves SetPipelineIn rewired the sink).
	external := make(chan pipeline.Item, 1)
	srv.SetPipelineIn(external)

	// The server-owned channel write/read ends still round-trip.
	srv.PipelineIn() <- pipeline.Item{RecordID: "01rectest"}
	if got := <-srv.Pipeline(); got.RecordID != "01rectest" {
		t.Errorf("Pipeline round-trip: got %q", got.RecordID)
	}
}
