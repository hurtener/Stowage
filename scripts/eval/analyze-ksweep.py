#!/usr/bin/env python3
"""Aggregate LongMemEval full-mode result JSONLs into a per-category, per-K table.

For each result file (one per K from ksweep.sh), reports per LongMemEval category and
overall:
  - quality        = (correct + 0.5*partial) / n   (the meaningful judge metric)
  - avg_ctx_tokens = mean over questions of sum(len(item)//4) — the context fed to the
                     reader (~4 chars/token, the project's roughTokens heuristic)
  - avg_ret_ms     = mean retrieval-pipeline latency (request -> context provided; the
                     reader/judge time is NOT included — latency_ns is the /v1/retrieve
                     round-trip only)

K is read from each file's trailing summary record (retrieve_limit). Usage:

    python3 scripts/eval/analyze-ksweep.py eval/results/longmemeval-n50-*.jsonl
"""
import sys
import json
import glob
from collections import defaultdict


def load(path):
    """Return (retrieve_limit, [question_records])."""
    qs, k = [], None
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            obj = json.loads(line)
            if "summary" in obj:  # trailing summary record
                k = obj.get("retrieve_limit")
                continue
            qs.append(obj)
    return k, qs


def quality(records):
    if not records:
        return None
    score = 0.0
    judged = 0
    for r in records:
        v = r.get("judge_verdict", "")
        if not v:
            continue
        judged += 1
        if v == "correct":
            score += 1.0
        elif v == "partial":
            score += 0.5
    if judged == 0:
        return None
    return score / judged, judged


def ctx_tokens(r):
    return sum(len(it) // 4 for it in r.get("items", []))


def summarize(path):
    k, qs = load(path)
    by_cat = defaultdict(list)
    for r in qs:
        by_cat[r.get("category", "(none)")].append(r)

    rows = []
    cats = sorted(by_cat) + ["__ALL__"]
    for cat in cats:
        recs = qs if cat == "__ALL__" else by_cat[cat]
        if not recs:
            continue
        q = quality(recs)
        qstr = f"{q[0]:.3f} (n={q[1]})" if q else "n/a"
        avg_tok = sum(ctx_tokens(r) for r in recs) / len(recs)
        avg_ms = sum(r.get("latency_ns", 0) for r in recs) / len(recs) / 1e6
        rows.append((cat, len(recs), qstr, avg_tok, avg_ms))
    return k, rows


def main():
    args = sys.argv[1:]
    if not args:
        print(__doc__)
        sys.exit(1)
    files = []
    for a in args:
        files.extend(sorted(glob.glob(a)))
    if not files:
        print("no files matched", file=sys.stderr)
        sys.exit(1)

    parsed = []
    for path in files:
        k, rows = summarize(path)
        parsed.append((k, path, rows))
    # Order by K when known.
    parsed.sort(key=lambda x: (x[0] is None, x[0] if x[0] is not None else 0))

    for k, path, rows in parsed:
        print(f"\n=== K={k}  ({path.split('/')[-1]}) ===")
        print(f"{'category':24} {'n':>4} {'quality':>16} {'avg_ctx_tok':>12} {'avg_ret_ms':>11}")
        print("-" * 72)
        for cat, n, qstr, tok, ms in rows:
            label = "ALL" if cat == "__ALL__" else cat
            print(f"{label:24} {n:>4} {qstr:>16} {tok:>12.0f} {ms:>11.1f}")

    # Compact cross-K plateau view (overall quality + tokens vs K).
    print("\n=== plateau view (overall) ===")
    print(f"{'K':>5} {'quality':>16} {'avg_ctx_tok':>12} {'avg_ret_ms':>11}")
    print("-" * 48)
    for k, _, rows in parsed:
        allrow = next((r for r in rows if r[0] == "__ALL__"), None)
        if allrow:
            _, _, qstr, tok, ms = allrow
            print(f"{str(k):>5} {qstr:>16} {tok:>12.0f} {ms:>11.1f}")


if __name__ == "__main__":
    main()
