package locomo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hurtener/stowage/eval/datasets"
)

const (
	defaultURL = "https://raw.githubusercontent.com/snap-research/locomo/main/data/locomo10.json"
	// DataDir is the subdirectory under eval/data/.
	DataDir = "locomo"
	// DataFile is the filename of the downloaded JSON.
	DataFile = "locomo10.json"
)

// Fetch downloads LoCoMo into dataRoot/locomo/locomo10.json
// and verifies + records the SHA-256 checksum.
func Fetch(ctx context.Context, dataRoot string) (string, error) {
	dir := filepath.Join(dataRoot, DataDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("locomo fetch: mkdir: %w", err)
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

	url := os.Getenv("STOWAGE_EVAL_LOCOMO_URL")
	if url == "" {
		url = defaultURL
	}

	data, err := datasets.FetchURL(ctx, url)
	if err != nil {
		return "", fmt.Errorf("locomo fetch: %w", err)
	}

	sum := datasets.SHA256Hex(data)
	if err := os.WriteFile(dest, data, 0o600); err != nil { //nolint:gosec // operator-controlled path
		return "", fmt.Errorf("locomo fetch: write data: %w", err)
	}
	if err := os.WriteFile(sumFile, []byte(sum), 0o600); err != nil { //nolint:gosec // operator-controlled path
		return "", fmt.Errorf("locomo fetch: write checksum: %w", err)
	}
	return dest, nil
}
