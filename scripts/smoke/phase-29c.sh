#!/usr/bin/env bash
# Smoke for Phase 29c (D-109): memories capture the assertion date and retrieval surfaces it.
set -uo pipefail
cd "$(dirname "$0")/../.."
fails=0
ok(){ echo "OK   $*"; }; failc(){ echo "FAIL $*"; fails=$((fails+1)); }

grep -q "OccurredAt int64" internal/pipeline/candidates.go && ok "Candidate carries OccurredAt" || failc "Candidate.OccurredAt missing"
grep -q "ValidFrom:   c.OccurredAt" internal/reconcile/reconcile.go && ok "candidateToMemory sets ValidFrom from OccurredAt" || failc "ValidFrom not set from OccurredAt"
grep -q "ri.OccurredAt = item.Memory.ValidFrom" internal/api/retrieve_handler.go && ok "HTTP retrieve surfaces occurred_at" || failc "HTTP retrieve missing occurred_at"
grep -q "occurred_at" internal/mcpserver/contracts.go && ok "MCP retrieve item has occurred_at" || failc "MCP missing occurred_at"
grep -q "OccurredAt int64" sdk/stowage/types.go && ok "SDK retrieve item has occurred_at" || failc "SDK missing occurred_at"
grep -q "When: " internal/retrieval/render.go && ok "eval reader renders | When: date (retrieval.Render, ae3/D-141)" || failc "eval reader missing When rendering"
go build ./... >/dev/null 2>&1 && ok "build green" || failc "build"
exit "$fails"
