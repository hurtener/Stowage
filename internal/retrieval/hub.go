package retrieval

import (
	"crypto/sha256"
	"fmt"
	"sort"
)

// hubWindowMs is the recency window over which the durable hub signal is counted:
// a memory's hub signal is the number of DISTINCT query clusters that returned it
// via an injection in the last hubWindowMs. 30 days balances "recent" against
// surviving a deploy (the former per-process LRU lost everything on restart, D-092).
// Not a config knob (D-034 guardrail) — a tuning constant.
const hubWindowMs int64 = 30 * 24 * 60 * 60 * 1000 // 30 days

// QuerySig returns a short, stable signature for a set of query tokens. It
// identifies a "query cluster": two retrieves with the same token set (any order)
// share a signature, so they count as ONE cluster for hub dampening and share a
// result-cache key. The signature is the SHA-256 hash of the sorted, joined tokens.
func QuerySig(tokens []string) string {
	if len(tokens) == 0 {
		return "empty"
	}
	sorted := make([]string, len(tokens))
	copy(sorted, tokens)
	sort.Strings(sorted)
	joined := fmt.Sprint(sorted)
	sum := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("%x", sum[:8]) // 16 hex chars — collision-resistant for this use
}
