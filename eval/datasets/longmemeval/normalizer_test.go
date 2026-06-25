package longmemeval_test

import (
	"os"
	"strings"
	"testing"

	"github.com/hurtener/stowage/eval/datasets/longmemeval"
)

// TestNormalize_QuestionDate verifies the LongMemEval question_date is carried onto
// Question.Date, normalized to YYYY-MM-DD (the reader's temporal anchor — without it
// "how many days/months since X" is unanswerable).
func TestNormalize_QuestionDate(t *testing.T) {
	raw := `[{"question_id":"q1","question":"how many days since X?","question_type":"temporal-reasoning",
	  "question_date":"2023/05/20 (Sat) 02:21","answer":"5",
	  "haystack_dates":["2023/05/15 (Mon) 09:00"],
	  "haystack_sessions":[[{"role":"user","content":"X happened"}]],"evidence_list":[]}]`
	_, qs, err := longmemeval.Normalize(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("want 1 question, got %d", len(qs))
	}
	if qs[0].Date != "2023-05-20" {
		t.Errorf("Question.Date = %q, want 2023-05-20 (parsed from question_date)", qs[0].Date)
	}
}

// TestNormalize_Mini golden-tests the normalizer on the committed mini-fixture.
// The mini-fixture contains 5 hand-built questions with no licensed data.
func TestNormalize_Mini(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/mini.json")
	if err != nil {
		t.Fatalf("open testdata/mini.json: %v", err)
	}
	defer f.Close() //nolint:errcheck

	convs, qs, err := longmemeval.Normalize(f)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(convs) != 5 {
		t.Errorf("convs: got %d want 5", len(convs))
	}
	if len(qs) != 5 {
		t.Errorf("questions: got %d want 5", len(qs))
	}
	// Each question must have a non-empty ID, text, and expected answer.
	for i, q := range qs {
		if q.ID == "" {
			t.Errorf("q[%d].ID empty", i)
		}
		if q.Text == "" {
			t.Errorf("q[%d].Text empty", i)
		}
		if q.Expected.Answer == "" {
			t.Errorf("q[%d].Expected.Answer empty", i)
		}
		if q.ConvID == "" {
			t.Errorf("q[%d].ConvID empty", i)
		}
	}
	// Each conversation must have sessions with turns.
	for i, c := range convs {
		if c.ID == "" {
			t.Errorf("conv[%d].ID empty", i)
		}
		if len(c.Sessions) == 0 {
			t.Errorf("conv[%d].Sessions empty", i)
		}
		for si, s := range c.Sessions {
			if len(s.Turns) == 0 {
				t.Errorf("conv[%d].Sessions[%d].Turns empty", i, si)
			}
		}
	}
}

// TestParseHaystackDate covers the real LongMemEval timestamp format (D-109): minute
// granularity must survive so temporal-reasoning intervals and same-day ordering work.
func TestParseHaystackDate(t *testing.T) {
	cases := map[string]string{
		"2023/04/10 (Mon) 17:50": "2023-04-10 17:50",
		"2023/04/10 (Mon) 14:47": "2023-04-10 14:47",
		"2023-05-28":             "2023-05-28 00:00",
	}
	for in, want := range cases {
		got := longmemeval.ExportParseHaystackDate(in)
		if got.IsZero() || got.Format("2006-01-02 15:04") != want {
			t.Errorf("longmemeval.ExportParseHaystackDate(%q) = %v, want %s", in, got, want)
		}
	}
	if !longmemeval.ExportParseHaystackDate("not a date").IsZero() {
		t.Errorf("unparseable input should return zero time")
	}
}
