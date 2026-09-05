package mcpserver

import "github.com/hurtener/dockyard/runtime/server"

// NewRuntime exposes the five agent operations plus the existing run-completion
// sink on ONE connection. Pengui discovers the sink on its attached capability,
// not on a second, unattached endpoint. Reusing NewAgent and the existing sink
// handler keeps schemas, source-backed writes and ingestion semantics identical.
//
// This is a HOST-facing catalog, not a six-tool planner catalog. The host must
// exclude memory_ingest_run from both planner discovery and execution while
// retaining it in its full executor catalog. Harbor's DisabledTools projection
// plus trusted run-completion hook provides that separation; deferred loading
// does not. No caller-provided metadata is treated as permission to bypass auth.
func NewRuntime(info server.Info, svc *Services) (*server.Server, error) {
	srv, err := NewAgent(info, svc)
	if err != nil {
		return nil, err
	}
	if err := declare[IngestRunInput, IngestRunOutput]("memory_ingest_run").
		Describe(toolDescription("memory_ingest_run")).
		Handler(makeIngestRunHandler(svc)).
		Register(srv); err != nil {
		return nil, err
	}
	return srv, nil
}
