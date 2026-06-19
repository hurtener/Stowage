package locomo

import (
	"path/filepath"

	"github.com/hurtener/stowage/eval/datasets"
)

// init registers LoCoMo in the dataset registry so it flows through the shared
// public-benchmark runner (harness.RunDataset) like LongMemEval — previously the
// LoCoMo fetcher + normalizer existed but no runner consumed them (D-096).
func init() {
	datasets.Register(datasets.Spec{
		Name:      "locomo",
		DataFile:  filepath.Join(DataDir, DataFile),
		Fetch:     Fetch,
		Normalize: Normalize,
	})
}
