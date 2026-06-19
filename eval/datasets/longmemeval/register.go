package longmemeval

import (
	"path/filepath"

	"github.com/hurtener/stowage/eval/datasets"
)

// init registers both LongMemEval variants in the dataset registry (D-096): the
// oracle variant (each question's haystack = only its evidence sessions) and the
// distractor variant (longmemeval_s) — the harder headline benchmark. Both share
// the same normalizer; only the source URL and on-disk location differ.
func init() {
	datasets.Register(datasets.Spec{
		Name:      "longmemeval",
		DataFile:  filepath.Join(DataDir, DataFile),
		Fetch:     Fetch,
		Normalize: Normalize,
	})
	datasets.Register(datasets.Spec{
		Name:      "longmemeval_s",
		DataFile:  filepath.Join(DataDirS, DataFileS),
		Fetch:     FetchS,
		Normalize: Normalize,
	})
}
