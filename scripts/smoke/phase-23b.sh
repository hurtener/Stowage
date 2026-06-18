#!/usr/bin/env bash
# Phase 23b smoke: similar-episode contrast (RFC §6b). D-082.
#
#   AC-1  Retriever.SimilarNarratives present + degraded-safe (embed via gateway seam,
#         kind=narrative vindex; gateway down ⇒ degraded, no error).
#   AC-2  episodes.Similar is the single core; memory_episodes exposes similar_to on
#         HTTP + MCP + SDK with a per-episode score + degraded flag.
#   AC-3  similar_to cross-surface parity test + Similar unit tests pass.
#   AC-4  episodes view core stays gateway-free (P5) — Similar takes a NarrativeSearcher.
#   AC    MCP schema goldens stable; eval-ci unaffected.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if ! grep -q 'func (r \*Retriever) SimilarNarratives' internal/retrieval/retrieval.go 2>/dev/null; then
  skip "AC-1..4: similar-episode contrast not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-1: SimilarNarratives present, embeds via the gateway seam ──────────────────
if grep -q 'r.gw.Embed' internal/retrieval/retrieval.go 2>/dev/null \
   && grep -q 'Kinds: \[\]string{"narrative"}' internal/retrieval/retrieval.go 2>/dev/null; then
  ok "AC-1: SimilarNarratives embeds via the gateway seam + kind=narrative vindex"
else
  failc "AC-1: SimilarNarratives embed/kind-filter wiring missing"
fi

# ── AC-2: similar_to on all three single-user surfaces (D-067) ────────────────────
if grep -q 'similar_to' internal/api/episodes_handler.go 2>/dev/null; then
  ok "AC-2: HTTP ?similar_to= branch present"
else
  failc "AC-2: HTTP similar_to branch missing"
fi
if grep -q 'SimilarTo' internal/mcpserver/contracts.go 2>/dev/null \
   && grep -q 'episodes.Similar' internal/mcpserver/handlers.go 2>/dev/null; then
  ok "AC-2: MCP EpisodesInput.SimilarTo + handler branch present"
else
  failc "AC-2: MCP similar_to wiring missing"
fi
if grep -q 'SimilarTo' sdk/stowage/types.go 2>/dev/null \
   && grep -q 'episodes.Similar' sdk/stowage/embedded.go 2>/dev/null \
   && grep -q 'similar_to' sdk/stowage/http.go 2>/dev/null; then
  ok "AC-2: SDK EpisodesRequest.SimilarTo on embedded + HTTP"
else
  failc "AC-2: SDK similar_to wiring missing"
fi

# ── AC-4: view core stays gateway-free (P5) ──────────────────────────────────────
if grep -q 'internal/gateway' internal/episodes/view.go 2>/dev/null; then
  failc "AC-4: episodes view core imports the gateway (must stay LLM-free)"
else
  ok "AC-4: episodes view core gateway-free (Similar takes a NarrativeSearcher)"
fi

# ── AC-1/3: unit + parity tests ──────────────────────────────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestSimilarNarratives' ./internal/retrieval/ >/tmp/p23b-rt.log 2>&1 \
   && CGO_ENABLED=1 go test -count=1 -timeout=180s -run 'TestSimilar$' ./internal/episodes/ >/tmp/p23b-ep.log 2>&1; then
  ok "AC-1/3: SimilarNarratives + episodes.Similar unit tests pass"
else
  failc "AC-1/3: similar unit tests failed"; tail -25 /tmp/p23b-rt.log /tmp/p23b-ep.log >&2
fi
if CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestEpisodesParity_Similar' ./test/integration/ >/tmp/p23b-parity.log 2>&1; then
  ok "AC-3: similar_to all-surfaces parity test passes"
else
  failc "AC-3: similar_to parity test failed"; tail -25 /tmp/p23b-parity.log >&2
fi

# ── AC: MCP schema goldens stable + CI eval gate unaffected ──────────────────────
if CGO_ENABLED=1 go test -count=1 -run 'TestSchemaGoldens' ./internal/mcpserver/ >/tmp/p23b-golden.log 2>&1; then
  ok "AC: MCP schema goldens stable (incl. memory_episodes similar_to/score)"
else
  failc "AC: MCP schema goldens drifted"; cat /tmp/p23b-golden.log >&2
fi
if make eval-ci >/tmp/p23b-evalci.log 2>&1; then
  ok "AC: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC: make eval-ci failed"; cat /tmp/p23b-evalci.log >&2
fi

exit "$fails"
