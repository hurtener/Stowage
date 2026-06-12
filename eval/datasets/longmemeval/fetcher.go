package longmemeval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hurtener/stowage/eval/datasets"
)

const (
	// jsonURL is the HuggingFace JSON download for longmemeval-cleaned.
	// Operators should set STOWAGE_EVAL_LONGMEMEVAL_URL to override (e.g. for the -v2 variant).
	jsonURL = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_oracle.json"
	// DataDir is the subdirectory under eval/data/ where data is stored.
	DataDir = "longmemeval"
	// DataFile is the filename of the downloaded JSON.
	DataFile = "longmemeval.json"
)

// Fetch downloads LongMemEval into dataRoot/longmemeval/longmemeval.json
// and verifies + records the SHA-256 checksum.
// If the file already exists and its checksum matches a recorded value, the
// download is skipped.
func Fetch(ctx context.Context, dataRoot string) (string, error) {
	dir := filepath.Join(dataRoot, DataDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("longmemeval fetch: mkdir: %w", err)
	}
	dest := filepath.Join(dir, DataFile)
	sumFile := filepath.Join(dir, DataFile+".sha256")

	// Check for existing valid download.
	if existing, err := os.ReadFile(dest); err == nil { //nolint:gosec // operator-controlled path
		if recorded, err := os.ReadFile(sumFile); err == nil { //nolint:gosec // operator-controlled path
			actual := datasets.SHA256Hex(existing)
			if actual == string(recorded) {
				return dest, nil
			}
		}
	}

	url := os.Getenv("STOWAGE_EVAL_LONGMEMEVAL_URL")
	if url == "" {
		url = jsonURL
	}

	data, err := datasets.FetchURL(ctx, url)
	if err != nil {
		return "", fmt.Errorf("longmemeval fetch: %w", err)
	}

	sum := datasets.SHA256Hex(data)
	if err := os.WriteFile(dest, data, 0o600); err != nil { //nolint:gosec // operator-controlled path
		return "", fmt.Errorf("longmemeval fetch: write data: %w", err)
	}
	if err := os.WriteFile(sumFile, []byte(sum), 0o600); err != nil { //nolint:gosec // operator-controlled path
		return "", fmt.Errorf("longmemeval fetch: write checksum: %w", err)
	}
	return dest, nil
}
