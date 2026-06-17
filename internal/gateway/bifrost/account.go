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

// customRerankProvider is the synthetic Bifrost custom-provider name Stowage
// auto-wires so a non-native-rerank primary (e.g. OpenRouter) can serve the
// cross-encoder rerank over a Cohere-shape `/rerank` endpoint (D-075). It is
// purely internal — never a config value an operator sets.
const customRerankProvider bfschemas.ModelProvider = "stowage-rerank"

// customRerankPath is the request path the auto-wired Cohere-shape provider
// POSTs rerank requests to. Combined with a `…/api/v1` base URL this yields
// OpenRouter's working `…/api/v1/rerank` (verified live 2026-06-17).
const customRerankPath = "/rerank"

// isNativeRerankProvider reports whether p implements rerank natively in
// bifrost (so no custom provider is needed). The set is {cohere, vllm,
// bedrock, vertex} (D-075). Any other primary provider with a configured
// rerank model gets the auto-wired Cohere-shape custom provider.
func isNativeRerankProvider(p bfschemas.ModelProvider) bool {
	switch p {
	case bfschemas.Cohere, bfschemas.VLLM, bfschemas.Bedrock, bfschemas.Vertex:
		return true
	default:
		return false
	}
}

// rerankProviderFor returns the provider a rerank request must route to for the
// given primary provider + config: the auto-wired customRerankProvider when a
// rerank model is configured AND the primary is not native-rerank, else the
// primary itself. Shared by newAccount (to expose the routing) and the Driver
// (to set bfReq.Provider) so the two cannot drift.
func rerankProviderFor(primary bfschemas.ModelProvider, cfg config.GatewayConfig) bfschemas.ModelProvider {
	if cfg.RerankModel != "" && !isNativeRerankProvider(primary) {
		return customRerankProvider
	}
	return primary
}

// Account implements bfschemas.Account for Stowage's deployment shape: one
// primary provider (embed + complete + native rerank), selected by
// config.GatewayConfig.Provider, OPTIONALLY plus a synthetic Cohere-shape
// custom provider (customRerankProvider) auto-wired for rerank when the primary
// is not native-rerank and a rerank model is configured (D-075). One Bifrost
// Account legitimately exposes multiple providers via GetConfiguredProviders.
//
// Concurrent-reuse: the struct is read-only after newAccount returns.
// Safe for N concurrent bifrost goroutines.
type Account struct {
	provider    bfschemas.ModelProvider
	apiKey      string // resolved at construction; NEVER logged
	providerCfg *bfschemas.ProviderConfig

	// rerankProvider is the provider rerank requests route to: == provider for a
	// native-rerank primary (or no rerank model), == customRerankProvider when
	// the Cohere-shape custom provider is auto-wired.
	rerankProvider bfschemas.ModelProvider
	// rerankCfg is the ProviderConfig for the auto-wired custom rerank provider;
	// nil unless customRerank is true.
	rerankCfg *bfschemas.ProviderConfig
	// customRerank is true iff the synthetic Cohere-shape rerank provider is wired.
	customRerank bool
	// rerankBaseURL is the base URL the custom rerank provider POSTs to (for the
	// boot log only); empty unless customRerank.
	rerankBaseURL string
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

	a := &Account{
		provider:       provider,
		apiKey:         apiKey,
		providerCfg:    providerCfg,
		rerankProvider: provider,
	}

	// Auto-wire a Cohere-shape custom rerank provider when the primary cannot
	// rerank natively but a rerank model is configured (D-075). rerank_base_url
	// overrides base_url for the rare case rerank lives on a different host.
	if rerankProviderFor(provider, cfg) == customRerankProvider {
		baseURL := cfg.BaseURL
		if cfg.RerankBaseURL != "" {
			baseURL = cfg.RerankBaseURL
		}
		a.customRerank = true
		a.rerankProvider = customRerankProvider
		a.rerankBaseURL = baseURL
		a.rerankCfg = buildRerankProviderConfig(baseURL)
	}

	return a, nil
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

// buildRerankProviderConfig constructs the *ProviderConfig for the auto-wired
// Cohere-shape custom rerank provider (D-075): a Cohere base type that only
// allows rerank, with the request path overridden to `/rerank` so it POSTs to
// `<baseURL>/rerank` (OpenRouter's working `…/api/v1/rerank`).
func buildRerankProviderConfig(baseURL string) *bfschemas.ProviderConfig {
	return &bfschemas.ProviderConfig{
		NetworkConfig: bfschemas.NetworkConfig{
			BaseURL: baseURL,
		},
		CustomProviderConfig: &bfschemas.CustomProviderConfig{
			BaseProviderType: bfschemas.Cohere,
			AllowedRequests:  &bfschemas.AllowedRequests{Rerank: true},
			RequestPathOverrides: map[bfschemas.RequestType]string{
				bfschemas.RerankRequest: customRerankPath,
			},
		},
	}
}

// GetConfiguredProviders implements bfschemas.Account. Returns the primary
// provider, plus the auto-wired custom rerank provider when one is configured
// (D-075), so bifrost starts a worker pool for each.
func (a *Account) GetConfiguredProviders() ([]bfschemas.ModelProvider, error) {
	if a.customRerank {
		return []bfschemas.ModelProvider{a.provider, a.rerankProvider}, nil
	}
	return []bfschemas.ModelProvider{a.provider}, nil
}

// GetKeysForProvider implements bfschemas.Account. Returns the resolved API
// key for either the primary provider or the auto-wired rerank provider (they
// share one key/credential, D-075). Per bifrost docs, Models: []string{"*"}
// means "all non-blacklisted models" — the wildcard sentinel; the custom rerank
// provider REQUIRES the wildcard (an empty Models yields "no keys found that
// support model").
func (a *Account) GetKeysForProvider(_ context.Context, providerKey bfschemas.ModelProvider) ([]bfschemas.Key, error) {
	if providerKey != a.provider && (!a.customRerank || providerKey != a.rerankProvider) {
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
// ProviderConfig for the primary provider, or the Cohere-shape custom config
// for the auto-wired rerank provider (D-075).
func (a *Account) GetConfigForProvider(providerKey bfschemas.ModelProvider) (*bfschemas.ProviderConfig, error) {
	if a.customRerank && providerKey == a.rerankProvider {
		return a.rerankCfg, nil
	}
	if providerKey != a.provider {
		return nil, fmt.Errorf("bifrost: provider %q is not configured for this Stowage account", providerKey)
	}
	return a.providerCfg, nil
}
