package locomo_test

import (
	"os"
	"testing"

	"github.com/hurtener/stowage/eval/datasets/locomo"
)

// TestNormalize_Mini golden-tests the normalizer on the committed mini-fixture.
func TestNormalize_Mini(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/mini.json")
	if err != nil {
		t.Fatalf("open testdata/mini.json: %v", err)
	}
	defer f.Close() //nolint:errcheck

	convs, qs, err := locomo.Normalize(f)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(convs) < 1 {
		t.Errorf("convs: got 0 want >= 1")
	}
	if len(qs) < 1 {
		t.Errorf("questions: got 0 want >= 1")
	}
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
	for i, c := range convs {
		if len(c.Sessions) == 0 {
			t.Errorf("conv[%d].Sessions empty", i)
		}
	}
}
