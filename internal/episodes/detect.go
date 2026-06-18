// Package episodes implements the Phase 22 episodic layer (RFC §6b, D-079):
// heuristic boundary detection (gateway-free) groups a session's records into
// episodes, and a narration pass (gateway, schema-constrained) constructs a
// narrative memory per episode. Boundary detection never calls the gateway
// (OQ-8 heuristic-first); only narration does.
package episodes

import "github.com/hurtener/stowage/internal/store"

// EpisodeDraft is a detected episode boundary over a session's records: the time
// range, the terminal outcome, and the member record IDs (provenance source).
type EpisodeDraft struct {
	StartedAt int64
	EndedAt   int64
	Outcome   string // last non-empty outcome among the member records
	RecordIDs []string
}

// DetectEpisodes splits a session's records (assumed ordered by occurred_at
// ascending) into episodes on intra-session temporal gaps greater than gapMs.
// gapMs <= 0 disables splitting (one episode per session — the v1 default). Pure
// and deterministic; the gateway is never involved (OQ-8 heuristic-first).
func DetectEpisodes(records []store.Record, gapMs int64) []EpisodeDraft {
	if len(records) == 0 {
		return nil
	}
	var out []EpisodeDraft
	cur := EpisodeDraft{StartedAt: records[0].OccurredAt, EndedAt: records[0].OccurredAt}
	prevAt := records[0].OccurredAt
	flush := func() {
		if len(cur.RecordIDs) > 0 {
			out = append(out, cur)
		}
	}
	for _, r := range records {
		if gapMs > 0 && r.OccurredAt-prevAt > gapMs {
			flush()
			cur = EpisodeDraft{StartedAt: r.OccurredAt, EndedAt: r.OccurredAt}
		}
		cur.RecordIDs = append(cur.RecordIDs, r.ID)
		cur.EndedAt = r.OccurredAt
		if r.Outcome != "" {
			cur.Outcome = r.Outcome
		}
		prevAt = r.OccurredAt
	}
	flush()
	return out
}
