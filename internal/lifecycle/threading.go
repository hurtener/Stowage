package lifecycle

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/oklog/ulid/v2"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/store"
	"github.com/hurtener/stowage/internal/vindex"
)

const episodeThreadLockKey int64 = 0x1409

// runThreadEpisodes is the Phase-24b episode-threading sweep (gateway-free, D-081):
// per tenant it clusters recent narrated episodes into cross-session arcs by
// (narrative word-set Jaccard OR narrative-embedding cosine, D-093) ∧ temporal proximity
// ∧ (project,user) continuity, recording each link as a relates_to edge between the two
// episodes' narrative memories. OFF BY DEFAULT (threadingOn) — enablement is eval-gated.
// Idempotent: an already-linked pair is skipped. Reversible: derived edges over immutable
// episodes/narratives.
func (m *Manager) runThreadEpisodes(ctx context.Context) {
	if !m.threadingOn() {
		return
	}
	release, err := m.st.Ops().AdvisoryLock(ctx, episodeThreadLockKey)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: advisory lock failed", "err", err)
		return
	}
	defer func() { _ = release() }()

	tenants, err := m.st.Tenants(ctx)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: list tenants failed", "err", err)
		return
	}
	windowMs := m.profile.ThreadWindow.Milliseconds()
	for _, tenant := range tenants {
		m.threadTenant(ctx, tenant, windowMs)
	}
}

// threadCand is a narrated episode prepared for pairwise comparison.
type threadCand struct {
	ep    store.Episode
	words map[string]struct{} // content-word set for the lexical overlap signal
	vec   []float32           // the narrative's stored embedding (nil when unembedded — degraded)
}

// threadMinCosine is the cosine floor for the SEMANTIC threading signal (A7, D-093).
// Narratives whose stored embeddings sit this close in vector space are threaded even
// when they share few literal words — the case word-Jaccard misses (same arc, different
// vocabulary). Conservative (0.82): narrative prose is long, so genuinely-related arcs
// embed high; a high floor guards against spurious cross-arc edges. A package const, not
// a config knob (D-034) — like the reconcile cosine floor (D-090). Reusing the SAME stored
// vectors the embed sweep already wrote keeps the threading sweep gateway-free (D-081).
const threadMinCosine = 0.82

// minThreadWords is the floor on a narrative's distinct content words before it is
// eligible to thread — guards against empty/degenerate narratives scoring spuriously
// high (an empty word set would otherwise Jaccard to 0 with everything, but a 1–2 word
// stub could match noise). Conservative against false merges.
const minThreadWords = 3

// wordSet tokenizes prose into a set of lowercased content words (alphanumeric runs of
// length ≥ 3 — drops punctuation and short stopword-ish noise). Word-set overlap is the
// topical threading signal: unlike character-bigram Jaccard (which saturates on any two
// English prose strings), distinct-word overlap reflects shared subject matter.
func wordSet(s string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, tok := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		if len([]rune(tok)) >= 3 {
			set[tok] = struct{}{}
		}
	}
	return set
}

// narrativeCosine is the cosine similarity of two stored narrative embeddings, or 0
// when either is absent (unembedded narrative / degraded ingest / vindex unwired) or
// their dims differ. 0 means "no semantic signal" — the pair then relies on the lexical
// signal alone (degraded-safe, D-036). Reuses the vindex kernel so threading and
// retrieval score vectors identically.
func narrativeCosine(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	return vindex.CosineSimilarity(a, b)
}

// wordJaccard is the Jaccard overlap of two content-word sets (0 when either is empty).
func wordJaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for w := range a {
		if _, ok := b[w]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func (m *Manager) threadTenant(ctx context.Context, tenant string, windowMs int64) {
	scope := identity.Scope{Tenant: tenant}
	eps, _, err := m.st.Episodes().ListEpisodes(ctx, scope, m.profile.ThreadBatchSize, "")
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: list episodes failed", "tenant", tenant, "err", err)
		return
	}
	// Keep only narrated episodes with a substantive narrative; precompute word sets.
	// Track the oldest episode start to bound the vector scan below.
	cands := make([]threadCand, 0, len(eps))
	minStart := int64(0)
	for _, ep := range eps {
		if ep.NarrativeMemoryID == "" {
			continue
		}
		epScope := identity.Scope{Tenant: tenant, Project: ep.ProjectID, User: ep.UserID}
		mem, gerr := m.st.Memories().Get(ctx, epScope, ep.NarrativeMemoryID)
		if gerr != nil || mem == nil || mem.Status != "active" {
			continue
		}
		ws := wordSet(mem.Content)
		if len(ws) < minThreadWords { // degenerate/empty narrative — never threads (M1 guard)
			continue
		}
		if minStart == 0 || ep.StartedAt < minStart {
			minStart = ep.StartedAt
		}
		cands = append(cands, threadCand{ep: ep, words: ws})
	}
	if len(cands) < 2 {
		return // nothing to pair — skip the vector scan entirely
	}

	// Attach stored narrative embeddings for the SEMANTIC threading signal (A7, D-093).
	// Gateway-free (these vectors were written by the embed sweep). The scan is bounded
	// to [minStart, ∞): a narrative's created_at is always ≥ its episode's StartedAt
	// (narration happens at/after episode close), so this never drops a candidate's
	// vector, while keeping the scan off a full-table read on large tenants.
	// Degraded-safe (D-036): on a scan error (or no vindex/vectors) the embeddings stay
	// nil and threading falls back to the lexical word-Jaccard signal alone.
	if svs, verr := m.st.Vectors().Scan(ctx, scope, []string{"narrative"}, store.Window{From: minStart}); verr != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: narrative vector scan failed — lexical-only", "tenant", tenant, "err", verr)
	} else {
		byID := make(map[string][]float32, len(svs))
		for _, sv := range svs {
			byID[sv.MemoryID] = sv.Vec
		}
		for i := range cands {
			cands[i].vec = byID[cands[i].ep.NarrativeMemoryID]
		}
	}

	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			a, b := cands[i], cands[j]
			// Same owner only (P3): two users sharing a tenant never thread.
			if a.ep.ProjectID != b.ep.ProjectID || a.ep.UserID != b.ep.UserID {
				continue
			}
			// Two episodes that SHARE one narrative memory (D-079 content-dedup of
			// identical narratives) must not be threaded — the relates_to edge would
			// be self-referential (M→M). Skip; there is no meaningful arc edge between
			// an episode and itself-via-a-shared-narrative (checkpoint finding).
			if a.ep.NarrativeMemoryID == b.ep.NarrativeMemoryID {
				continue
			}
			if !withinWindow(a.ep, b.ep, windowMs) {
				continue
			}
			// Thread on EITHER the lexical signal (shared words) OR the semantic
			// signal (close narrative embeddings) — an OR so vectors WIDEN recall to
			// same-arc episodes that share few words (A7, D-093), never narrow it.
			// The recorded confidence is the stronger of the two qualifying signals.
			word := wordJaccard(a.words, b.words)
			cos := narrativeCosine(a.vec, b.vec)
			lexOK := word >= m.profile.ThreadMinOverlap
			semOK := cos >= threadMinCosine
			if !lexOK && !semOK {
				continue
			}
			score := word
			if cos > score {
				score = cos
			}
			// Discriminate which signal(s) fired so the event consumer can tell whether
			// `score` is a word-overlap or a cosine (events/v1 contract hygiene, §8).
			signal := "lexical"
			switch {
			case lexOK && semOK:
				signal = "both"
			case semOK:
				signal = "semantic"
			}
			m.linkNarratives(ctx, identity.Scope{Tenant: tenant, Project: a.ep.ProjectID, User: a.ep.UserID}, a, b, score, signal)
		}
	}
}

// withinWindow reports whether two episodes are within windowMs of each other (by the
// gap between one's end and the other's start, in either order).
func withinWindow(a, b store.Episode, windowMs int64) bool {
	if windowMs <= 0 {
		return true
	}
	// Order by start so the gap is end(earlier) → start(later).
	earlier, later := a, b
	if b.StartedAt < a.StartedAt {
		earlier, later = b, a
	}
	gap := later.StartedAt - earlier.EndedAt
	if gap < 0 {
		gap = 0 // overlapping in time
	}
	return gap <= windowMs
}

// linkNarratives writes a canonical relates_to edge between two episodes' narrative
// memories (idempotent + order-independent), with an episode.threaded audit event.
// score is the stronger qualifying signal; signal ∈ {lexical, semantic, both} names
// which signal(s) fired so the event consumer can interpret score (§8).
func (m *Manager) linkNarratives(ctx context.Context, scope identity.Scope, a, b threadCand, score float64, signal string) {
	from, to := a.ep.NarrativeMemoryID, b.ep.NarrativeMemoryID
	fromEp, toEp := a.ep, b.ep
	if to < from { // canonical: smaller id is `from` so (a,b) and (b,a) collapse
		from, to = to, from
		fromEp, toEp = b.ep, a.ep
	}
	existing, err := m.st.Memories().ListLinks(ctx, scope, from, to)
	if err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: list links failed", "err", err)
		return
	}
	for _, l := range existing {
		if l.Type == "relates_to" && l.FromMemory == from && l.ToMemory == to {
			return // already threaded — idempotent
		}
	}
	now := time.Now().UnixMilli()
	link := store.Link{
		ID: ulid.Make().String(), TenantID: scope.Tenant,
		FromMemory: from, ToMemory: to, Type: "relates_to", Source: "inferred",
		Confidence: score, CreatedAt: now,
	}
	if err := m.st.Memories().InsertLinks(ctx, scope, []store.Link{link}); err != nil {
		m.log.WarnContext(ctx, "lifecycle/episode-thread: insert link failed", "err", err)
		return
	}
	payload, _ := json.Marshal(struct {
		FromEpisode string  `json:"from_episode"`
		ToEpisode   string  `json:"to_episode"`
		Overlap     float64 `json:"overlap"` // the winning signal's score (cosine when signal=semantic)
		Signal      string  `json:"signal"`  // "lexical" | "semantic" | "both" (D-093)
	}{FromEpisode: fromEp.ID, ToEpisode: toEp.ID, Overlap: score, Signal: signal})
	_ = m.st.Events().Emit(ctx, scope, store.Event{
		ID: ulid.Make().String(), TenantID: scope.Tenant, ProjectID: scope.Project, UserID: scope.User,
		Type: "episode.threaded", SubjectID: fromEp.ID, Reason: "threaded into a cross-session arc",
		Payload: string(payload), CreatedAt: now,
	})
}
