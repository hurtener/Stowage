package harness

import (
	"context"
	"fmt"
)

// LongMemEvalTopics is the broad extraction-magnet set seeded for a full-mode
// LongMemEval run. Extraction is topic-gated (internal/topics): a candidate that
// matches no active topic is never created. The default assistant pack
// (pack:preferences) covers only personalization, so LongMemEval's facts —
// events, dates, possessions, relationships, numbers, updates — would have no
// "home" and go uncaptured, starving retrieval. These topics give each class of
// implied learning a target, tuned to the LongMemEval question taxonomy
// (single-session-user/assistant/preference, multi-session, knowledge-update,
// temporal-reasoning). Tight, natural-language, non-overlapping descriptions keep
// the gate precise. 13 topics is well under the composition cap (topics.MaxActiveTopics).
//
// Explicit topics suppress the virtual default pack (D-099), which is intended:
// the eval run wants exactly this broad set, not the 4-topic preferences pack.
var LongMemEvalTopics = []struct{ Key, Description string }{
	{"personal-facts", "The user's durable personal facts — name, age, location, identity, and background"},
	{"relationships-and-people", "People in the user's life — family, friends, colleagues, pets — their names, roles, and relationships to the user"},
	{"preferences-and-tastes", "The user's likes, dislikes, favorites, and preferences across food, brands, tools, media, and style"},
	{"activities-and-events", "Things the user did or that happened to them — outings, trips, appointments, projects, and milestones"},
	{"dates-and-timeline", "When things happened or will happen — specific dates, durations, schedules, and recurring timing"},
	{"possessions-and-purchases", "Things the user owns, bought, sold, or plans to buy — items, prices, quantities, and brands"},
	{"work-and-education", "The user's job, employer, role, studies, and professional or academic context"},
	{"health-and-lifestyle", "The user's health, diet, fitness, routines, and habits"},
	{"plans-and-goals", "The user's intentions, future plans, goals, and to-dos"},
	{"numbers-and-quantities", "Specific numeric details the user shared — counts, amounts, measurements, prices, and durations"},
	{"updates-and-corrections", "Changes, updates, or corrections to previously stated facts — what changed and when"},
	{"opinions-and-experiences", "The user's opinions, reactions, and experiences with things they tried or encountered"},
	{"assistant-provided-info", "Recommendations, answers, instructions, or facts the assistant gave the user (for recall of what was said in-conversation, not about the user)"},
}

// SeedEvalTopics installs LongMemEvalTopics at the eval tenant scope via the live
// PUT /v1/topics surface (the same path a real adopter uses), so the full-mode run
// extracts the breadth of facts LongMemEval probes rather than only preferences.
// Called by the full-mode test before ingestion; CI/mock runs do not call it.
func SeedEvalTopics(ctx context.Context, srv *TestServer) error {
	type topicInput struct {
		Key         string `json:"key"`
		Description string `json:"description"`
		Status      string `json:"status"`
	}
	body := make([]topicInput, 0, len(LongMemEvalTopics))
	for _, t := range LongMemEvalTopics {
		body = append(body, topicInput{Key: t.Key, Description: t.Description, Status: "active"})
	}
	status, respBody, err := srv.DoJSON(ctx, "PUT", "/v1/topics", body)
	if err != nil {
		return fmt.Errorf("seed topics: %w", err)
	}
	if status != 200 {
		return fmt.Errorf("seed topics: got %d: %s", status, respBody)
	}
	return nil
}
