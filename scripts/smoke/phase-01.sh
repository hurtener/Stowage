#!/usr/bin/env bash
# Phase 01 smoke — scaffold & CI gates.
set -uo pipefail
cd "$(dirname "$0")/../.."

fails=0
ok()   { echo "OK   $*"; }
failc(){ echo "FAIL $*"; fails=$((fails+1)); }

# 1. CGo-free build + version
CGO_ENABLED=0 go build -o /tmp/stowage-smoke ./cmd/stowage 2>/dev/null \
  && ok "cgo-free build" || failc "cgo-free build"
[ -n "$(/tmp/stowage-smoke version)" ] && ok "version prints" || failc "version prints"

# 2. Unknown subcommand exits 2
/tmp/stowage-smoke bogus >/dev/null 2>&1
[ $? -eq 2 ] && ok "unknown command exits 2" || failc "unknown command exits 2"

# 3. Mirror + drift-audit pass on the worktree
diff -q AGENTS.md CLAUDE.md >/dev/null && ok "mirror identical" || failc "mirror identical"
scripts/drift-audit.sh >/dev/null && ok "drift-audit passes" || failc "drift-audit passes"

# 4. Required artifacts
for f in RFC-001-Stowage.md docs/decisions.md docs/glossary.md \
         docs/plans/README.md docs/research/INDEX.md Makefile .golangci.yml; do
  [ -f "$f" ] && ok "artifact $f" || failc "artifact $f"
done

# 5. Negative test: mirror gate catches divergence (in a temp copy)
tmp=$(mktemp -d)
cp -R . "$tmp/repo" 2>/dev/null
echo "drift" >> "$tmp/repo/AGENTS.md"
if (cd "$tmp/repo" && scripts/drift-audit.sh >/dev/null 2>&1); then
  failc "drift-audit catches mirror divergence"
else
  ok "drift-audit catches mirror divergence"
fi

# 6. Negative test: forbidden-name gate (assemble the name from fragments)
tmp2=$(mktemp -d)
cp -R . "$tmp2/repo" 2>/dev/null
printf 'the %s%s system\n' 'ice' 'berg' > "$tmp2/repo/docs/forbidden-test.md"
if (cd "$tmp2/repo" && scripts/drift-audit.sh >/dev/null 2>&1); then
  failc "drift-audit catches forbidden name"
else
  ok "drift-audit catches forbidden name"
fi
rm -rf "$tmp" "$tmp2" /tmp/stowage-smoke

exit "$fails"
