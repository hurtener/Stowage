#!/usr/bin/env bash
# Gate-enforced squash-merge: refuses unless EVERY check on the PR has
# concluded successfully. Branch protection is unavailable on this private
# plan, so the CLAUDE.md §12 "CI green is mandatory" rule is mechanized here.
# Usage: scripts/merge-pr.sh <pr-number>
set -euo pipefail
pr="${1:?usage: merge-pr.sh <pr-number>}"
checks=$(gh pr checks "$pr" 2>&1 || true)
echo "$checks"
total=$(echo "$checks" | grep -cE $'\t(pass|fail|pending|skipping)\t' || true)
passing=$(echo "$checks" | grep -cE $'\tpass\t' || true)
if [ "$total" -eq 0 ] || [ "$passing" -ne "$total" ]; then
  echo "REFUSED: $passing/$total checks passing — merge blocked." >&2
  exit 1
fi
exec gh pr merge "$pr" --squash --delete-branch
