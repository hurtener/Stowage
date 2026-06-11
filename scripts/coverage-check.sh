#!/usr/bin/env bash
# scripts/coverage-check.sh
# Reads coverage.out per package against scripts/coverage.json thresholds.
# Exits non-zero listing failures.
# An internal/* package with statements but no configured threshold also fails
# (CLAUDE.md §11 — "a new package with no configured threshold fails the build").
set -uo pipefail
cd "$(dirname "$0")/.."

COVERAGE_FILE="${1:-coverage.out}"
THRESHOLDS_FILE="scripts/coverage.json"

if [ ! -f "$COVERAGE_FILE" ]; then
    echo "coverage-check: $COVERAGE_FILE not found" >&2
    exit 1
fi

if [ ! -f "$THRESHOLDS_FILE" ]; then
    echo "coverage-check: $THRESHOLDS_FILE not found" >&2
    exit 1
fi

fails=0

# Compute per-package statement/covered counts from coverage.out.
# Line format: filepath:startline.col,endline.col numstmts execcount
# Outputs:     pkg<TAB>totalstmts<TAB>coveredstmts
pkg_coverage=$(awk '
    /^mode:/ { next }
    {
        file = $1
        # Strip the :line.col,line.col suffix.
        sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file)
        # Compute package path = directory of the file.
        n = split(file, parts, "/")
        pkg = ""
        for (i = 1; i < n; i++) {
            if (i > 1) pkg = pkg "/"
            pkg = pkg parts[i]
        }
        numstmts = $2
        count = $3
        total[pkg] += numstmts
        if (count > 0) covered[pkg] += numstmts
    }
    END {
        for (pkg in total) {
            printf "%s\t%d\t%d\n", pkg, total[pkg], (covered[pkg] ? covered[pkg] : 0)
        }
    }
' "$COVERAGE_FILE")

if [ -z "$pkg_coverage" ]; then
    echo "coverage-check: no packages found in $COVERAGE_FILE" >&2
    exit 0
fi

# Look up the threshold for a package from coverage.json.
# The JSON format is one-package-per-line: "pkg/path": number
get_threshold() {
    local pkg="$1"
    awk -F'"' -v pkg="$pkg" '$2 == pkg { gsub(/[^0-9]/, "", $3); if ($3 != "") print $3; exit }' "$THRESHOLDS_FILE"
}

while IFS=$'\t' read -r pkg stmts covered; do
    # Only enforce thresholds for internal/* packages.
    if [[ "$pkg" != *"/internal/"* ]]; then
        continue
    fi

    if [ "$stmts" -eq 0 ]; then
        continue
    fi

    pct=$(awk "BEGIN { printf \"%.1f\", ${covered} * 100.0 / ${stmts} }")
    threshold=$(get_threshold "$pkg")

    if [ -z "$threshold" ]; then
        echo "FAIL $pkg: no threshold configured (coverage: ${pct}%)"
        fails=$((fails + 1))
        continue
    fi

    ok=$(awk "BEGIN { print (${pct} + 0 >= ${threshold} + 0) ? \"yes\" : \"no\" }")
    if [ "$ok" = "yes" ]; then
        echo "OK   $pkg: coverage ${pct}% >= threshold ${threshold}%"
    else
        echo "FAIL $pkg: coverage ${pct}% < threshold ${threshold}%"
        fails=$((fails + 1))
    fi
done <<< "$pkg_coverage"

exit "$fails"
