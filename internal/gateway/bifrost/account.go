// Package bifrost is the gateway driver backed by the maximhq/bifrost Go SDK
// (D-049). It provides every provider (OpenAI, Anthropic, Gemini, Mistral, …)
// natively in-process via bf.Init. No types or functions from this package may
// be imported outside internal/gateway/bifrost — CLAUDE.md §13, P5.
package bifrost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	bfschemas "github.com/maximhq/bifrost/core/schemas"

	"github.com/hurtener/stowage/internal/config"
)

// ErrMissingAPIKey is returned when the operator configured driver=bifrost but
// the key referenced by GatewayConfig.APIKey is not present at Open time.
// Fail-closed (CLAUDE.md §5); the error message names the env var so the
// operator can fix it without exposing the key value.
var ErrMissingAPIKey = errors.New("bifrost: API key missing")

// ErrInvalidProvider is returned when gateway.provider is empty or not in the
// SDK's StandardProviders set. Fails at Open time.
var ErrInvalidProvider = errors.New("bifrost: invalid provider")

// Account implements bfschemas.Account for Stowage's single-provider
// deployment shape (one provider per Stowage instance, selected by
// config.GatewayConfig.Provider).
//
// Concurrent-reuse: the struct is read-only after newAccount returns.
// Safe for N concurrent bifrost goroutines.
type Account struct {
	provider    bfschemas.ModelProvider
	apiKey      string // resolved at construction; NEVER logged
	providerCfg *bfschemas.ProviderConfig
}

// newAccount resolves the provider and API key from the GatewayConfig.
// Fails closed when:
//   - cfg.Provider is empty or unknown (ErrInvalidProvider).
//   - cfg.APIKey does not resolve to a non-empty value (ErrMissingAPIKey).
func newAccount(cfg config.GatewayConfig) (*Account, error) {
	if cfg.Provider == "" {
		return nil, fmt.Errorf("%w: gateway.provider is empty — set it to one of: %s",
			ErrInvalidProvider, knownProvidersHuman())
	}

	provider := bfschemas.ModelProvider(cfg.Provider)
	if !isKnownProvider(provider) {
		return nil, fmt.Errorf("%w: %q is not a known Bifrost provider (valid: %s)",
			ErrInvalidProvider, cfg.Provider, knownProvidersHuman())
	}

	apiKey, err := resolveAPIKey(cfg.APIKey)
	if err != nil {
		return nil, err
	}

	providerCfg := buildProviderConfig(cfg)

	return &Account{
		provider:    provider,
		apiKey:      apiKey,
		providerCfg: providerCfg,
	}, nil
}

// resolveAPIKey reads either a literal key or an env.VAR_NAME reference
// (Stowage's standard indirection, D-030). Empty input → ErrMissingAPIKey.
// The error message names the env var so the operator can fix it; the key
// VALUE is never logged or surfaced.
func resolveAPIKey(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("%w: gateway.api_key is empty", ErrMissingAPIKey)
	}
	if name, ok := strings.CutPrefix(input, "env."); ok {
		val := os.Getenv(name)
		if val == "" {
			return "", fmt.Errorf("%w: env var %q is unset (configure the API key for gateway.provider or change gateway.api_key to a literal)",
				ErrMissingAPIKey, name)
		}
		return val, nil
	}
	return input, nil
}

// isKnownProvider checks p against bifrost's enumerated StandardProviders.
func isKnownProvider(p bfschemas.ModelProvider) bool {
	for _, sp := range bfschemas.StandardProviders {
		if sp == p {
			return true
		}
	}
	return false
}

// knownProvidersHuman renders the standard providers as a comma-separated
// human-readable string for error messages. Stable order from the SDK slice.
func knownProvidersHuman() string {
	names := make([]string, 0, len(bfschemas.StandardProviders))
	for _, sp := range bfschemas.StandardProviders {
		names = append(names, string(sp))
	}
	return strings.Join(names, ", ")
}

// buildProviderConfig constructs the *ProviderConfig for this provider.
// Honours cfg.BaseURL for providers that need a custom base URL (e.g. Ollama,
// vLLM, or any self-hosted OpenAI-compatible endpoint behind bifrost).
func buildProviderConfig(cfg config.GatewayConfig) *bfschemas.ProviderConfig {
	out := &bfschemas.ProviderConfig{
		NetworkConfig: bfschemas.NetworkConfig{
			BaseURL: cfg.BaseURL,
		},
	}
	return out
}

// GetConfiguredProviders implements bfschemas.Account. Returns the single
// configured provider so bifrost knows which worker pool to start.
func (a *Account) GetConfiguredProviders() ([]bfschemas.ModelProvider, error) {
	return []bfschemas.ModelProvider{a.provider}, nil
}

// GetKeysForProvider implements bfschemas.Account. Returns the resolved API
// key for the configured provider. Per bifrost docs, Models: []string{"*"}
// means "all non-blacklisted models" — the wildcard sentinel.
func (a *Account) GetKeysForProvider(_ context.Context, providerKey bfschemas.ModelProvider) ([]bfschemas.Key, error) {
	if providerKey != a.provider {
		return nil, fmt.Errorf("bifrost: provider %q is not configured for this Stowage account", providerKey)
	}
	return []bfschemas.Key{
		{
			ID:     "stowage-default",
			Name:   "stowage-default",
			Value:  bfschemas.EnvVar{Val: a.apiKey},
			Models: bfschemas.WhiteList{"*"}, // bifrost's "all non-blacklisted" wildcard
			Weight: 1.0,
		},
	}, nil
}

// GetConfigForProvider implements bfschemas.Account. Returns the pre-built
// ProviderConfig (network settings, optional base URL).
func (a *Account) GetConfigForProvider(providerKey bfschemas.ModelProvider) (*bfschemas.ProviderConfig, error) {
	if providerKey != a.provider {
		return nil, fmt.Errorf("bifrost: provider %q is not configured for this Stowage account", providerKey)
	}
	return a.providerCfg, nil
}
