package longmemeval_test

import (
	"os"
	"testing"

	"github.com/hurtener/stowage/eval/datasets/longmemeval"
)

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
