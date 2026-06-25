#!/usr/bin/env bash
# Live JSON-validity / extraction-health probe for a running LEARN phase.
# Reads the persistent eval store (WAL mode → safe concurrent reads) and reports
# whether the cheap learner model is producing valid schema-constrained JSON:
#   - active memories climbing            → valid JSON, learning works
#   - extraction.completed produced > 0   → candidates parsed + validated
#   - extraction.failed / dead_letters    → bad JSON or gateway failures
#
#   bash scripts/eval/learn-monitor.sh <db-path>
#
set -uo pipefail
DB="${1:-}"
[ -n "$DB" ] && [ -f "$DB" ] || { echo "usage: learn-monitor.sh <db-path>  (db not found: ${DB:-<unset>})"; exit 1; }
q(){ sqlite3 -readonly "$DB" "$1" 2>/dev/null; }

echo "── extraction health @ $(date -u +%H:%M:%SZ) ── ${DB##*/}"
printf "  active memories      : %s\n" "$(q "SELECT COUNT(*) FROM memories WHERE status='active';")"
printf "  total memories       : %s\n" "$(q "SELECT COUNT(*) FROM memories;")"
printf "  records ingested     : %s\n" "$(q "SELECT COUNT(*) FROM records;")"
echo   "  extraction events    :"
q "SELECT '     '||type||' : '||COUNT(*) FROM events WHERE type LIKE 'extraction.%' GROUP BY type;"
# produced / dropped across all completed extractions (JSON parsed + validated counts)
PROD=$(q "SELECT COALESCE(SUM(json_extract(payload,'\$.produced')),0) FROM events WHERE type='extraction.completed';")
DROP=$(q "SELECT COALESCE(SUM(json_extract(payload,'\$.dropped')),0) FROM events WHERE type='extraction.completed';")
printf "  candidates produced  : %s   dropped(validation) : %s\n" "${PROD:-0}" "${DROP:-0}"
printf "  extraction failures  : %s\n" "$(q "SELECT COUNT(*) FROM events WHERE type='extraction.failed';")"
printf "  dead letters (extract): %s\n" "$(q "SELECT COUNT(*) FROM dead_letters WHERE stage='extract' AND resolved_at=0;")"
echo   "  sample committed memory:"
q "SELECT '     ['||kind||'] '||substr(content,1,90) FROM memories WHERE status='active' ORDER BY created_at DESC LIMIT 1;"
# Verdict
FAIL=$(q "SELECT COUNT(*) FROM events WHERE type='extraction.failed';")
DL=$(q "SELECT COUNT(*) FROM dead_letters WHERE stage='extract' AND resolved_at=0;")
ACT=$(q "SELECT COUNT(*) FROM memories WHERE status='active';")
if [ "${ACT:-0}" -gt 0 ] && [ "${FAIL:-0}" -eq 0 ] && [ "${DL:-0}" -eq 0 ]; then
  echo "  ✅ JSON looks VALID — memories committing, no extraction failures/dead-letters."
elif [ "${FAIL:-0}" -gt 0 ] || [ "${DL:-0}" -gt 0 ]; then
  echo "  ⚠️  extraction FAILURES present — the learner may be emitting invalid JSON. Inspect dead_letters."
else
  echo "  … no memories yet — extraction may still be warming up (or the first flush hasn't landed)."
fi
