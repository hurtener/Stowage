package harness

import (
	"sort"
	"strings"
	"time"

	"github.com/hurtener/stowage/internal/retrieval"
)

// Scores holds the aggregate metrics for one CI eval run.
//
// AnswerQuality and JudgedCount are populated only by the opt-in, full-mode
// judged-QA path (reader + LLM judge — Phase 20, D-076). They are pointer /
// omitempty so the deterministic CI run's JSON shape is byte-unchanged when
// judging is off — the committed CI baseline (eval/baselines/ci.json) and the
// gate (gate.go) are unaffected.
type Scores struct {
	AnswerContextHit float64  `json:"answer_context_hit"` // [0,1]
	HitCount         int      `json:"hit_count"`
	TotalQuestions   int      `json:"total_questions"`
	P50LatencyMs     float64  `json:"p50_latency_ms"`
	P95LatencyMs     float64  `json:"p95_latency_ms"`
	AnswerQuality    *float64 `json:"answer_quality,omitempty"` // judged QA: (correct + ½·partial)/N
	JudgedCount      int      `json:"judged_count,omitempty"`
	// ByCategory breaks the metrics out per LongMemEval question category so a
	// re-baseline can see where accuracy lives (the "open up by categories" view).
	// omitempty: deterministic CI runs (single synthetic category) leave it nil so
	// the committed CI baseline JSON shape is unchanged.
	ByCategory map[string]CategoryScore `json:"by_category,omitempty"`
}

// CategoryScore is the per-category slice of Scores. AnswerQuality/Judged are set
// only on the judged path; Correct/Partial feed the quality the same way the
// aggregate does ((correct + ½·partial)/judged).
type CategoryScore struct {
	Total            int      `json:"total"`
	Hits             int      `json:"hits"`
	AnswerContextHit float64  `json:"answer_context_hit"`
	Correct          int      `json:"correct,omitempty"`
	Partial          int      `json:"partial,omitempty"`
	Judged           int      `json:"judged,omitempty"`
	AnswerQuality    *float64 `json:"answer_quality,omitempty"`
}

// QuestionResult is the per-question result for one retrieve + score.
//
// ReaderAnswer/JudgeVerdict/JudgeJustification are populated only by the judged
// path (omitempty: the CI run never sets them, keeping its JSON unchanged).
type QuestionResult struct {
	QuestionID string        `json:"question_id"`
	Category   string        `json:"category,omitempty"`
	Query      string        `json:"query"`
	Expected   string        `json:"expected"`
	Hit        bool          `json:"hit"`
	Latency    time.Duration `json:"latency_ns"`
	Items      []string      `json:"items"` // content of retrieved items
	// RenderItems is the typed projection Items was rendered from (Stale,
	// SupersededByContent, SupersededByDate, OccurredAt) — carried alongside
	// Items so the judged-QA path (dataset.go, gain.go) can partition
	// CURRENT/SUPERSEDED via BuildReaderPrompt instead of re-deriving it from
	// the rendered strings (wave-0 fix: restores pre-ae3 partitioning on the
	// judged path, D-141). Not part of the wire JSON — it is redundant with
	// Items for any external consumer.
	RenderItems        []retrieval.RenderItem `json:"-"`
	ReaderAnswer       string                 `json:"reader_answer,omitempty"`
	JudgeVerdict       string                 `json:"judge_verdict,omitempty"` // correct|partial|incorrect
	JudgeJustification string                 `json:"judge_justification,omitempty"`
}

// ComputeScores computes aggregate metrics from per-question results.
func ComputeScores(results []QuestionResult) Scores {
	if len(results) == 0 {
		return Scores{}
	}

	hits := 0
	latencies := make([]float64, 0, len(results))
	for _, r := range results {
		if r.Hit {
			hits++
		}
		latencies = append(latencies, float64(r.Latency.Milliseconds()))
	}

	sort.Float64s(latencies)
	p50 := percentile(latencies, 50)
	p95 := percentile(latencies, 95)

	return Scores{
		AnswerContextHit: float64(hits) / float64(len(results)),
		HitCount:         hits,
		TotalQuestions:   len(results),
		P50LatencyMs:     p50,
		P95LatencyMs:     p95,
		ByCategory:       categoryScores(results),
	}
}

// categoryScores breaks per-question results out by category. Returns nil when every
// result shares one (or no) category — keeps the deterministic CI run's JSON unchanged.
func categoryScores(results []QuestionResult) map[string]CategoryScore {
	type agg struct{ total, hits, correct, partial, judged int }
	by := map[string]*agg{}
	cats := map[string]bool{}
	for _, r := range results {
		cats[r.Category] = true
		a := by[r.Category]
		if a == nil {
			a = &agg{}
			by[r.Category] = a
		}
		a.total++
		if r.Hit {
			a.hits++
		}
		switch r.JudgeVerdict {
		case "correct":
			a.correct++
			a.judged++
		case "partial":
			a.partial++
			a.judged++
		case "incorrect":
			a.judged++
		}
	}
	if len(cats) <= 1 {
		return nil // single/empty category — nothing to break out
	}
	out := make(map[string]CategoryScore, len(by))
	for cat, a := range by {
		cs := CategoryScore{
			Total: a.total, Hits: a.hits, Correct: a.correct, Partial: a.partial, Judged: a.judged,
		}
		if a.total > 0 {
			cs.AnswerContextHit = float64(a.hits) / float64(a.total)
		}
		if a.judged > 0 {
			q := (float64(a.correct) + 0.5*float64(a.partial)) / float64(a.judged)
			cs.AnswerQuality = &q
		}
		out[cat] = cs
	}
	return out
}

// AnswerContextHit returns true if expectedAnswer appears (case-insensitive)
// in any of the provided content strings. It is the deterministic, LLM-free CI
// metric — never calls a model.
//
// Matching has three layers, all deterministic (Phase 20, D-076):
//
//  1. Short answers (< 4 runes, e.g. "2", "$12", "NYC") match on token
//     boundaries: a bare substring check lets "2" match inside "f/2.8 lens",
//     a spurious hit in the 2026-06-12 baseline. Longer answers keep substring
//     semantics (distinctive enough that anchoring only adds false negatives).
//  2. Number-word equivalence: the gold answer is expanded to its number-word
//     forms both directions ("five"↔"5") over a small exact table, so a memory
//     phrasing the count the other way still counts. This fixes a FORM mismatch,
//     not a reasoning gap — "25" never matches "17"+"8" (that is the judge's job).
//  3. Either-direction phrase containment for short gold phrases (≥2 non-stopword
//     tokens, ≤6 tokens total): after dropping a tiny stopword set from both
//     sides, the gold's non-stopword tokens must appear as a contiguous run in
//     the content. This credits "under my bed" against "under the bed". The
//     ≥2-non-stopword-token guard is load-bearing: it keeps single-number golds
//     like "2" OUT of this path so the f/2.8 anchoring (layer 1) is never bypassed.
func AnswerContextHit(contents []string, expectedAnswer string) bool {
	lower := strings.ToLower(strings.TrimSpace(expectedAnswer))
	if lower == "" {
		return false
	}
	variants := answerVariants(lower)
	for _, c := range contents {
		lc := strings.ToLower(c)
		for _, v := range variants {
			if matchOne(lc, v) {
				return true
			}
		}
		if phraseMatchIgnoringStopwords(lc, lower) {
			return true
		}
	}
	return false
}

// matchOne matches a single variant against content. A NUMERIC variant (a bare
// digit string or a known number word) is ALWAYS matched on token boundaries,
// regardless of length: number words are tokens, so "eight" (gold "8") must not
// substring-match inside "weight"/"eighteen"/"eighty", and "five" (gold "5")
// must not match inside the compound "twenty-five" (=25). Short non-numeric
// answers (< 4 runes) also use token boundaries (the f/2.8 guard); longer
// non-numeric answers keep distinctive substring semantics.
func matchOne(lc, variant string) bool {
	if variant == "" {
		return false
	}
	if isNumericVariant(variant) || len([]rune(variant)) < 4 {
		return containsToken(lc, variant)
	}
	return strings.Contains(lc, variant)
}

// isNumericVariant reports whether v is a bare digit string or a known
// number/ordinal word — i.e. a numeric token that must be boundary-matched.
func isNumericVariant(v string) bool {
	if v == "" {
		return false
	}
	if _, ok := numberWords[v]; ok {
		return true
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// numberWords maps small cardinals/ordinals to their digit form. Kept small and
// exact (0–20, the round tens, and the common ordinals) — no fuzzy parsing.
var numberWords = map[string]string{
	"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4",
	"five": "5", "six": "6", "seven": "7", "eight": "8", "nine": "9",
	"ten": "10", "eleven": "11", "twelve": "12", "thirteen": "13",
	"fourteen": "14", "fifteen": "15", "sixteen": "16", "seventeen": "17",
	"eighteen": "18", "nineteen": "19", "twenty": "20",
	"thirty": "30", "forty": "40", "fifty": "50", "sixty": "60",
	"seventy": "70", "eighty": "80", "ninety": "90", "hundred": "100",
	"first": "1", "second": "2", "third": "3", "fourth": "4", "fifth": "5",
	"sixth": "6", "seventh": "7", "eighth": "8", "ninth": "9", "tenth": "10",
}

// digitToWord is the reverse of numberWords for the cardinals only (ordinals and
// duplicate "2"→{two,second} would be ambiguous, so only cardinals reverse-map).
var digitToWord = func() map[string]string {
	m := make(map[string]string)
	for w, d := range numberWords {
		// Only the cardinal forms reverse cleanly; skip ordinals to avoid
		// mapping a digit to an ordinal word.
		switch w {
		case "first", "second", "third", "fourth", "fifth", "sixth",
			"seventh", "eighth", "ninth", "tenth":
			continue
		}
		if _, exists := m[d]; !exists {
			m[d] = w
		}
	}
	return m
}()

// answerVariants returns the lowercased gold answer plus its number-word
// equivalents (both directions), de-duplicated. A bare number-word gold ("five")
// yields {"five","5"}; a bare digit gold ("5") yields {"5","five"}; everything
// else yields just itself.
func answerVariants(lower string) []string {
	out := []string{lower}
	seen := map[string]bool{lower: true}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if d, ok := numberWords[lower]; ok {
		add(d)
	}
	if w, ok := digitToWord[lower]; ok {
		add(w)
	}
	return out
}

// phraseStopwords is the tiny function-word set dropped on both sides of the
// either-direction phrase match. Deliberately minimal — only words whose
// presence/absence is point-of-view or article noise.
var phraseStopwords = map[string]bool{
	"the": true, "a": true, "an": true, "my": true, "your": true,
	"their": true, "his": true, "her": true, "its": true, "our": true,
	"of": true, "is": true, "are": true, "was": true, "were": true,
}

// alnumTokens splits s into lowercased alphanumeric tokens (any non-alnum rune is
// a separator).
func alnumTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isAlnum(r)
	})
}

func dropStopwords(toks []string) []string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if !phraseStopwords[t] {
			out = append(out, t)
		}
	}
	return out
}

// phraseMatchIgnoringStopwords reports whether the gold answer's non-stopword
// tokens appear as a contiguous run in the content after stopwords are dropped
// from both. Guarded to short gold phrases with ≥2 non-stopword tokens so it
// never fires on single-number/short answers (those stay on the layer-1 path).
func phraseMatchIgnoringStopwords(content, gold string) bool {
	goldToks := alnumTokens(gold)
	if len(goldToks) > 6 { // not a short phrase — layer-1 substring already covers it
		return false
	}
	goldCore := dropStopwords(goldToks)
	if len(goldCore) < 2 {
		return false // single-token golds (incl. "2", "nyc") never reach here
	}
	cToks := dropStopwords(alnumTokens(content))
	if len(cToks) < len(goldCore) {
		return false
	}
	for i := 0; i+len(goldCore) <= len(cToks); i++ {
		match := true
		for j := range goldCore {
			if cToks[i+j] != goldCore[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// JudgedQuality computes the judged-QA aggregate from per-question verdicts:
// (correct + ½·partial) / N. Returns the quality and the number of judged
// questions. Verdicts are the canonical strings "correct"/"partial"/anything
// else (treated as incorrect).
func JudgedQuality(verdicts []string) (quality float64, judged int) {
	if len(verdicts) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range verdicts {
		switch v {
		case "correct":
			sum += 1.0
		case "partial":
			sum += 0.5
		}
	}
	return sum / float64(len(verdicts)), len(verdicts)
}

// containsToken reports whether needle occurs in haystack delimited by token
// boundaries on both sides. A boundary is a non-alphanumeric rune — EXCEPT
// joining punctuation (./-:,) sandwiched between alphanumerics, which keeps
// compound tokens like "f/2.8", "24-70mm", or "3:45" intact so a short answer
// such as "2" cannot match inside them.
func containsToken(haystack, needle string) bool {
	for start := 0; ; {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(needle)
		if isBoundary(haystack, i-1, i-2) && isBoundary(haystack, end, end+1) {
			return true
		}
		start = i + 1
	}
}

// isBoundary reports whether the rune at index idx (with beyond one step
// further away from the match) terminates a token. Out-of-range counts as a
// boundary; joining punct with an alphanumeric beyond it does not.
func isBoundary(s string, idx, beyond int) bool {
	if idx < 0 || idx >= len(s) {
		return true
	}
	r := rune(s[idx])
	if isAlnum(r) {
		return false
	}
	if strings.ContainsRune("./-:,", r) && beyond >= 0 && beyond < len(s) && isAlnum(rune(s[beyond])) {
		return false // joining punct inside a compound token ("f/2.8", "24-70mm")
	}
	return true
}

func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// percentile returns the p-th percentile value from a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100.0)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
