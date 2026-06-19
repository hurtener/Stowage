package longmemeval

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hurtener/stowage/eval/datasets"
)

const (
	// jsonURL is the HuggingFace JSON download for longmemeval-cleaned (oracle).
	// Operators should set STOWAGE_EVAL_LONGMEMEVAL_URL to override (e.g. for the -v2 variant).
	jsonURL = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_oracle.json"
	// jsonURLS is the distractor-haystack variant (longmemeval_s, ~500 questions
	// over ~40–50 sessions each) — the harder, headline benchmark (D-096).
	jsonURLS = "https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s.json"
	// DataDir is the subdirectory under eval/data/ where the oracle data is stored.
	DataDir = "longmemeval"
	// DataFile is the filename of the downloaded oracle JSON.
	DataFile = "longmemeval.json"
	// DataDirS / DataFileS are the distractor (longmemeval_s) location.
	DataDirS  = "longmemeval_s"
	DataFileS = "longmemeval_s.json"
)

// Fetch downloads the LongMemEval oracle variant into
// dataRoot/longmemeval/longmemeval.json (checksum-verified, skip-on-match).
// Override the source with STOWAGE_EVAL_LONGMEMEVAL_URL.
func Fetch(ctx context.Context, dataRoot string) (string, error) {
	return fetchVariant(ctx, dataRoot, DataDir, DataFile, jsonURL, "STOWAGE_EVAL_LONGMEMEVAL_URL")
}

// FetchS downloads the LongMemEval distractor variant (longmemeval_s) into
// dataRoot/longmemeval_s/longmemeval_s.json. Override the source with
// STOWAGE_EVAL_LONGMEMEVAL_S_URL — a per-variant env so it never collides with the
// oracle override.
func FetchS(ctx context.Context, dataRoot string) (string, error) {
	return fetchVariant(ctx, dataRoot, DataDirS, DataFileS, jsonURLS, "STOWAGE_EVAL_LONGMEMEVAL_S_URL")
}

// fetchVariant downloads a longmemeval variant, verifies + records the SHA-256
// checksum, and skips the download when the file already matches. envURL is the
// per-variant override env var.
func fetchVariant(ctx context.Context, dataRoot, subdir, file, defaultURL, envURL string) (string, error) {
	dir := filepath.Join(dataRoot, subdir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("longmemeval fetch: mkdir: %w", err)
	}
	dest := filepath.Join(dir, file)
	sumFile := filepath.Join(dir, file+".sha256")

	// Check for existing valid download.
	if existing, err := os.ReadFile(dest); err == nil { //nolint:gosec // operator-controlled path
		if recorded, err := os.ReadFile(sumFile); err == nil { //nolint:gosec // operator-controlled path
			if datasets.SHA256Hex(existing) == string(recorded) {
				return dest, nil
			}
		}
	}

	url := os.Getenv(envURL)
	if url == "" {
		url = defaultURL
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
