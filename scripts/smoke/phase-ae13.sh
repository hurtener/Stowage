#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."
go test -count=1 ./internal/reconcile -run 'TestRemember|TestCorrect|TestExplicitReceipt|TestCompetingCorrections|TestExplicitCommandsPostgres'
go test -count=1 ./internal/mcpserver -run 'TestAgent|TestExplicitHostBinding'
go test -count=1 ./internal/retrieval -run '^TestReader'
go test -count=1 ./internal/api -run '^TestSourceBackedCommandsHTTPAndMCP$'
go test -count=1 ./cmd/stowage -run '^TestAgentHTTPMount'
(cd adapters/harbor && go test -count=1 -run '^TestAgentTools' ./...)
go test -race -count=1 ./cmd/stowage -run '^TestSharedMCPProfiles'
go test -race -count=1 ./internal/mcpserver -run '^TestRuntime'
go test -race -count=1 ./cmd/stowage -run '^TestMCPCatalogSelection$'
printf '%s\n' 'ae13: source-backed commands and agent-surface smoke passed (no live-model selection claims)'
