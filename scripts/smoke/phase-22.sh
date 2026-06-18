#!/usr/bin/env bash
# Phase 22 smoke: episodes & narratives (RFC §6b). D-079.
#
#   AC-1  EpisodeStore + RecordStore.DistinctSessions on both drivers.
#   AC-2  boundary detection is gateway-free (no gateway import in detect.go).
#   AC-3  narration routes through the gateway seam (P5) + schema-constrained (§10).
#   AC-6  detect + narrate sweeps registered; episodes profile-gated.
#   AC-7  episodes unit + integration tests pass; eval-ci unaffected.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()    { printf 'OK   %s\n' "$*"; }
failc() { printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }
skip()  { printf 'SKIP %s\n' "$*"; }

if [ ! -d internal/episodes ]; then
  skip "AC-1..7: episodes not yet implemented (plan skeleton)"
  exit "$fails"
fi

# ── AC-2: boundary detection is gateway-free ─────────────────────────────────────
if grep -q 'internal/gateway' internal/episodes/detect.go 2>/dev/null; then
  failc "AC-2: detect.go imports the gateway (boundary detection must be heuristic/gateway-free)"
else
  ok "AC-2: boundary detection is gateway-free (heuristic, OQ-8)"
fi

# ── AC-3/P5: narration routes through the gateway seam, schema-constrained ───────
if grep -rnE '"github.com/(sashabaranov|openai|cohere-ai)|bifrost/core|google.golang.org/genai' internal/episodes/ 2>/dev/null; then
  failc "AC-3/P5: internal/episodes imports a provider SDK"
else
  ok "AC-3/P5: internal/episodes routes through the gateway seam"
fi
if grep -q 'narrativeSchema' internal/episodes/narrate.go 2>/dev/null && grep -q 'Schema:' internal/episodes/narrate.go 2>/dev/null; then
  ok "AC-3/§10: narration Complete call is schema-constrained"
else
  failc "AC-3/§10: narration not schema-constrained"
fi

# ── AC-1: EpisodeStore + DistinctSessions on both drivers ────────────────────────
if grep -q 'episodeStore' internal/store/sqlitestore/episodes.go 2>/dev/null \
   && grep -q 'episodeStore' internal/store/pgstore/episodes.go 2>/dev/null \
   && grep -rq 'DistinctSessions' internal/store/sqlitestore/ 2>/dev/null \
   && grep -rq 'DistinctSessions' internal/store/pgstore/ 2>/dev/null; then
  ok "AC-1: EpisodeStore + DistinctSessions implemented on sqlite + pgx"
else
  failc "AC-1: EpisodeStore/DistinctSessions missing on a driver"
fi

# ── AC-6: detect + narrate sweeps registered; episodes profile-gated ─────────────
if grep -q 'runDetectEpisodes' internal/lifecycle/episodes.go 2>/dev/null \
   && grep -q 'runNarrateEpisodes' internal/lifecycle/episodes.go 2>/dev/null \
   && grep -q 'func EpisodeConfigForProfile' internal/config/profiles.go 2>/dev/null; then
  ok "AC-6: detect/narrate sweeps registered; episodes profile-gated"
else
  failc "AC-6: episode sweeps or profile gating missing"
fi

# ── AC-7: episodes unit + conformance + integration tests ────────────────────────
if CGO_ENABLED=1 go test -count=1 -timeout=300s \
     ./internal/episodes/ \
     -run '.' >/tmp/p22-ep.log 2>&1 \
   && CGO_ENABLED=1 go test -count=1 -timeout=300s -run 'TestEpisodeSweeps|TestEpisodesLoop' \
     ./internal/lifecycle/ ./test/integration/ >/tmp/p22-sweep.log 2>&1; then
  ok "AC-7: episodes unit + sweep + integration tests pass"
else
  failc "AC-7: episode tests failed"; tail -25 /tmp/p22-ep.log /tmp/p22-sweep.log >&2
fi

# ── AC-7: CI eval gate unaffected ────────────────────────────────────────────────
if make eval-ci >/tmp/p22-evalci.log 2>&1; then
  ok "AC-7: make eval-ci green (deterministic CI unaffected)"
else
  failc "AC-7: make eval-ci failed"; cat /tmp/p22-evalci.log >&2
fi

exit "$fails"
