//go:build fullmode

package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hurtener/stowage/internal/config"
	"github.com/hurtener/stowage/internal/gateway"
	_ "github.com/hurtener/stowage/internal/gateway/bifrost"
	_ "github.com/hurtener/stowage/internal/gateway/openaicompat"

	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// TestReaderJudgeSweep reuses a FROZEN learn-phase result JSONL (each line carries a
// question's query, gold answer, and the retrieved context items) and sweeps the
// reader × judge model grid over it — measuring the real answer_quality without
// re-extracting or re-retrieving. This is the cost/quality experiment: learning is
// done once (cheaply); readers and judges are swept here.
//
//	STOWAGE_EVAL_GATEWAY=bifrost STOWAGE_EVAL_PROVIDER=openrouter \
//	STOWAGE_EVAL_BASE_URL=https://openrouter.ai/api STOWAGE_EVAL_API_KEY_REF=env.OPENROUTER_API_KEY \
//	STOWAGE_EVAL_EMBED_MODEL=perplexity/pplx-embed-v1-0.6b STOWAGE_EVAL_EMBED_DIMS=1024 \
//	STOWAGE_EVAL_SWEEP_INPUT=eval/results/longmemeval-n50-<ts>.jsonl \
//	STOWAGE_EVAL_SWEEP_READERS="openai/gpt-5.4-nano,openai/gpt-4o-mini,anthropic/claude-sonnet-4.6" \
//	STOWAGE_EVAL_SWEEP_JUDGES="anthropic/claude-sonnet-4.6" \
//	STOWAGE_EVAL_READER_EFFORT=medium \
//	go test -tags=fullmode -run TestReaderJudgeSweep -timeout 120m ./eval/harness/
func TestReaderJudgeSweep(t *testing.T) {
	if os.Getenv("STOWAGE_EVAL_GATEWAY") == "" {
		t.Skip("STOWAGE_EVAL_GATEWAY not set — sweep is operator-triggered")
	}
	input := os.Getenv("STOWAGE_EVAL_SWEEP_INPUT")
	if input == "" {
		t.Skip("STOWAGE_EVAL_SWEEP_INPUT not set — point it at a learn-phase result JSONL")
	}

	questions, err := loadSweepQuestions(input)
	if err != nil {
		t.Fatalf("load sweep input: %v", err)
	}
	if lim := os.Getenv("STOWAGE_EVAL_LIMIT"); lim != "" {
		if n, e := strconv.Atoi(lim); e == nil && n > 0 && n < len(questions) {
			questions = questions[:n]
		}
	}
	t.Logf("sweep input: %d questions from %s", len(questions), input)

	readers := splitCSV(os.Getenv("STOWAGE_EVAL_SWEEP_READERS"))
	judges := splitCSV(os.Getenv("STOWAGE_EVAL_SWEEP_JUDGES"))
	if len(readers) == 0 {
		readers = []string{os.Getenv("STOWAGE_EVAL_READER_MODEL")}
	}
	if len(judges) == 0 {
		judges = readers // judge follows reader unless explicitly varied
	}
	readerEffort := os.Getenv("STOWAGE_EVAL_READER_EFFORT")

	gw := openSweepGateway(t)
	defer gw.Close(context.Background()) //nolint:errcheck
	ctx := context.Background()

	outDir := "../../eval/results"
	out, _ := os.Create(fmt.Sprintf("%s/sweep-%s.jsonl", outDir, time.Now().UTC().Format("20060102T150405Z"))) //nolint:gosec
	enc := json.NewEncoder(out)
	defer out.Close() //nolint:errcheck

	fmt.Printf("\n%-34s %-34s %-8s %-10s %s\n", "READER", "JUDGE", "QUALITY", "judged/n", "abstain")
	for _, reader := range readers {
		for _, judge := range judges {
			opts := ReaderOpts{Model: reader, JudgeModel: judge, ReasoningEffort: readerEffort}
			var verdicts []string
			abstain := 0
			errs := 0
			for i, q := range questions {
				jr, jerr := JudgeQuestionWith(ctx, gw, opts, q.Query, q.Expected, q.Items)
				if jerr != nil {
					errs++
					if errs >= 8 {
						t.Fatalf("sweep aborted (reader=%s judge=%s) after %d errors: %v", reader, judge, errs, jerr)
					}
					continue
				}
				verdicts = append(verdicts, jr.Verdict)
				if isAbstention(jr.Answer) {
					abstain++
				}
				if (i+1)%10 == 0 {
					t.Logf("  [%s | %s] %d/%d", reader, judge, i+1, len(questions))
				}
			}
			quality, n := JudgedQuality(verdicts)
			fmt.Printf("%-34s %-34s %-8.4f %-10s %d/%d\n", reader, judge, quality, fmt.Sprintf("%d/%d", n, len(questions)), abstain, len(questions))
			_ = enc.Encode(map[string]any{
				"reader": reader, "judge": judge, "reader_effort": readerEffort,
				"answer_quality": quality, "judged": n, "n": len(questions), "abstain": abstain, "errors": errs,
			})
		}
	}
}

type sweepQ struct {
	Query    string
	Expected string
	Items    []string
}

func loadSweepQuestions(path string) ([]sweepQ, error) {
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		return nil, err
	}
	defer f.Close() //nolint:errcheck
	var out []sweepQ
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24) // large lines (many context items)
	for sc.Scan() {
		var row struct {
			QuestionID string   `json:"question_id"`
			Query      string   `json:"query"`
			Expected   string   `json:"expected"`
			Items      []string `json:"items"`
		}
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			continue // skip the summary line / malformed
		}
		if row.QuestionID == "" {
			continue
		}
		out = append(out, sweepQ{Query: row.Query, Expected: row.Expected, Items: row.Items})
	}
	return out, sc.Err()
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isAbstention is a loose check that the reader declined to answer (for reporting
// only — the judge already credits gold-abstention matches).
func isAbstention(ans string) bool {
	a := strings.ToLower(ans)
	return strings.Contains(a, "not enough") || strings.Contains(a, "insufficient") ||
		strings.Contains(a, "does not contain") || strings.Contains(a, "no information") ||
		strings.Contains(a, "cannot determine") || strings.Contains(a, "not provided")
}

func openSweepGateway(t *testing.T) gateway.Gateway {
	cfg := config.Defaults()
	cfg.Gateway.Driver = os.Getenv("STOWAGE_EVAL_GATEWAY")
	cfg.Gateway.Provider = os.Getenv("STOWAGE_EVAL_PROVIDER")
	cfg.Gateway.BaseURL = os.Getenv("STOWAGE_EVAL_BASE_URL")
	cfg.Gateway.RerankBaseURL = os.Getenv("STOWAGE_EVAL_RERANK_BASE_URL")
	cfg.Gateway.APIKey = os.Getenv("STOWAGE_EVAL_API_KEY_REF")
	cfg.Gateway.Model = os.Getenv("STOWAGE_EVAL_READER_MODEL") // default; overridden per-request
	if cfg.Gateway.Model == "" {
		cfg.Gateway.Model = os.Getenv("STOWAGE_EVAL_MODEL")
	}
	cfg.Gateway.EmbedModel = os.Getenv("STOWAGE_EVAL_EMBED_MODEL")
	if v := os.Getenv("STOWAGE_EVAL_EMBED_DIMS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Gateway.EmbedDims = n
		}
	}
	gw, err := gateway.Open(context.Background(), cfg.Gateway, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("open sweep gateway: %v", err)
	}
	return gw
}
