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
# --delete-branch also tries to delete the LOCAL branch; when a worktree holds
# it, gh exits non-zero even though the merge + remote-branch delete succeeded
# (observed on PRs #21/#22). Verify the actual PR state before failing.
if gh pr merge "$pr" --squash --delete-branch; then
  exit 0
fi
state=$(gh pr view "$pr" --json state -q .state)
if [ "$state" = "MERGED" ]; then
  echo "merged OK (local branch cleanup failed — remove the worktree, then delete the branch)"
  exit 0
fi
echo "REFUSED: merge did not complete (state=$state)." >&2
exit 1
