#!/usr/bin/env bash
# Mechanical design-coherence checks (CLAUDE.md §"Drift-hygiene artifacts").
# Exit non-zero on any drift finding.
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0

# 1. Mirror rule (§18)
if ! diff -q AGENTS.md CLAUDE.md >/dev/null; then
  echo "DRIFT: AGENTS.md and CLAUDE.md differ"
  fail=1
fi

# 2. Forbidden names (D-001, D-003). eval/data/ (gitignored third-party
#    datasets) is excluded — the constraint governs repo content. Patterns are assembled from fragments so
#    this script itself never trips the check.
p1="$(printf 'ice%s' 'berg')"
p2="$(printf 'yes%s' 'mem')"
if grep -riIl --exclude-dir=.git --exclude-dir=data -e "$p1" -e "$p2" . | grep -v '^\./scripts/drift-audit.sh$' >/tmp/stowage-forbidden.txt; then
  echo "DRIFT: forbidden predecessor names found in:"
  cat /tmp/stowage-forbidden.txt
  fail=1
fi

# 3. Required drift-hygiene artifacts exist
for f in RFC-001-Stowage.md docs/decisions.md docs/glossary.md \
         docs/research/INDEX.md docs/plans/README.md docs/plans/_template.md; do
  if [ ! -f "$f" ]; then
    echo "DRIFT: missing required artifact $f"
    fail=1
  fi
done

# 4. Phase plans must cite at least one informing brief (§16)
for plan in docs/plans/phase-*.md; do
  [ -e "$plan" ] || continue
  if ! grep -qE 'briefs?|research/0[0-9]' "$plan"; then
    echo "DRIFT: $plan cites no informing brief"
    fail=1
  fi
done

# 5. Decision log is append-only in spirit: IDs must be unique
dups=$(grep -oE '^## D-[0-9]{3}' docs/decisions.md | sort | uniq -d || true)
if [ -n "$dups" ]; then
  echo "DRIFT: duplicate decision IDs: $dups"
  fail=1
fi

[ "$fail" -eq 0 ] && echo "drift-audit OK"
exit "$fail"
