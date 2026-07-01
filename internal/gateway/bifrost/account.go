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

// OpenRouter base URLs. The bifrost SDK has no built-in base for OpenRouter, so
// the driver supplies them when the operator left them empty (D-131): embed +
// complete go to `…/api` (the SDK appends `/v1/…`), while the auto-wired
// Cohere-shape rerank provider POSTs to `…/api/v1/rerank` (D-075). Keeping these
// in the driver — not config Defaults — preserves "empty base_url → native
// endpoint" for every other provider (P5: the driver owns provider wire details).
const (
	openRouterBaseURL       = "https://openrouter.ai/api"
	openRouterRerankBaseURL = "https://openrouter.ai/api/v1"
)

// applyProviderBaseDefaults fills provider-specific base URLs the SDK does not
// know, only when the operator left them empty. Mutates a by-value copy.
func applyProviderBaseDefaults(cfg config.GatewayConfig) config.GatewayConfig {
	if bfschemas.ModelProvider(cfg.Provider) == bfschemas.OpenRouter {
		if cfg.BaseURL == "" {
			cfg.BaseURL = openRouterBaseURL
		}
		if cfg.RerankBaseURL == "" {
			cfg.RerankBaseURL = openRouterRerankBaseURL
		}
	}
	return cfg
}

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

// effectiveRerankBase is the provider the rerank lane is based on: gateway.rerank_provider
// when set (a1b, D-134), else the primary. The native-vs-custom decision keys off this.
func effectiveRerankBase(primary bfschemas.ModelProvider, cfg config.GatewayConfig) bfschemas.ModelProvider {
	if cfg.RerankProvider != "" {
		return bfschemas.ModelProvider(cfg.RerankProvider)
	}
	return primary
}

// rerankProviderFor returns the provider a rerank request must route to: no rerank
// model → the primary (no rerank); a native-rerank effective base → that base
// (the primary, or a distinct native provider via rerank_provider); otherwise the
// auto-wired Cohere-shape customRerankProvider (D-075). Shared by newAccount and the
// Driver so the two cannot drift.
func rerankProviderFor(primary bfschemas.ModelProvider, cfg config.GatewayConfig) bfschemas.ModelProvider {
	if cfg.RerankModel == "" {
		return primary
	}
	base := effectiveRerankBase(primary, cfg)
	if isNativeRerankProvider(base) {
		return base
	}
	return customRerankProvider
}

// embedProviderFor returns the provider the embed lane routes to: gateway.embed_provider
// when set to a known provider distinct from the primary (a1b, D-134), else the primary.
func embedProviderFor(primary bfschemas.ModelProvider, cfg config.GatewayConfig) bfschemas.ModelProvider {
	if cfg.EmbedProvider != "" {
		if ep := bfschemas.ModelProvider(cfg.EmbedProvider); ep != primary && isKnownProvider(ep) {
			return ep
		}
	}
	return primary
}

// providerBaseURL returns the base URL for a provider: the explicit value when set,
// else the provider's known default the SDK lacks (OpenRouter), else empty (native
// endpoint). Mirrors applyProviderBaseDefaults for per-concern providers (D-131/134).
func providerBaseURL(p bfschemas.ModelProvider, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p == bfschemas.OpenRouter {
		return openRouterBaseURL
	}
	return ""
}

// resolveOptionalKey resolves a per-concern key reference, falling back to the
// primary key when the reference is empty (a1b inherit-on-empty, D-134).
func resolveOptionalKey(ref, fallback string) (string, error) {
	if ref == "" {
		return fallback, nil
	}
	return resolveAPIKey(ref)
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

	// Embed lane (a1b, D-134). embedProvider == provider unless an embed override
	// routes embedding to a DISTINCT provider; then distinctEmbed is true and
	// embedAPIKey/embedCfg are that provider's own credential + config.
	embedProvider bfschemas.ModelProvider
	embedAPIKey   string // resolved; == apiKey unless embed_api_key overrides
	embedCfg      *bfschemas.ProviderConfig
	distinctEmbed bool

	// rerankProvider is the provider rerank requests route to: == provider for a
	// native-rerank primary (or no rerank model), == customRerankProvider when
	// the Cohere-shape custom provider is auto-wired, or a distinct native rerank
	// provider when rerank_provider names one (distinctRerank).
	rerankProvider bfschemas.ModelProvider
	// rerankCfg is the ProviderConfig for the rerank provider entry (auto-wired
	// custom OR a distinct native rerank provider); nil unless customRerank ||
	// distinctRerank.
	rerankCfg *bfschemas.ProviderConfig
	// rerankAPIKey is the resolved key for the rerank provider; == apiKey unless
	// rerank_api_key overrides (a1b, D-134).
	rerankAPIKey string
	// customRerank is true iff the synthetic Cohere-shape rerank provider is wired.
	customRerank bool
	// distinctRerank is true iff rerank routes to a distinct NATIVE provider entry
	// (rerank_provider names a native-rerank provider other than the primary).
	distinctRerank bool
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

	// Validate a per-concern rerank provider up front (fail loud like embed_provider,
	// §5/D-134) — an unknown value would otherwise silently fall through to the
	// Cohere-shape custom provider against the primary endpoint.
	if cfg.RerankProvider != "" && !isKnownProvider(bfschemas.ModelProvider(cfg.RerankProvider)) {
		return nil, fmt.Errorf("%w: gateway.rerank_provider %q is not a known Bifrost provider (valid: %s)",
			ErrInvalidProvider, cfg.RerankProvider, knownProvidersHuman())
	}

	// Capture the operator's LITERAL rerank base BEFORE applyProviderBaseDefaults
	// rewrites it to OpenRouter's for an openrouter primary (D-131): a DISTINCT native
	// rerank provider must NOT inherit the primary's host (D-134 review, Blocker 1).
	origRerankBaseURL := cfg.RerankBaseURL

	// Supply provider-specific base URLs the SDK lacks (OpenRouter) when empty,
	// before buildProviderConfig / rerank wiring read them (D-131).
	cfg = applyProviderBaseDefaults(cfg)

	apiKey, err := resolveAPIKey(cfg.APIKey)
	if err != nil {
		return nil, err
	}

	providerCfg := buildProviderConfig(cfg)

	a := &Account{
		provider:       provider,
		apiKey:         apiKey,
		providerCfg:    providerCfg,
		embedProvider:  provider,
		embedAPIKey:    apiKey,
		rerankProvider: provider,
		rerankAPIKey:   apiKey,
	}

	// Embed lane (a1b, D-134): route embedding to a DISTINCT provider when configured,
	// with its own key (embed_api_key, fallback primary) and base (embed_base_url,
	// fallback the provider's default). Empty → embed uses the primary entry.
	if cfg.EmbedProvider != "" {
		ep := bfschemas.ModelProvider(cfg.EmbedProvider)
		if !isKnownProvider(ep) {
			return nil, fmt.Errorf("%w: gateway.embed_provider %q is not a known Bifrost provider (valid: %s)",
				ErrInvalidProvider, cfg.EmbedProvider, knownProvidersHuman())
		}
		if ep != provider {
			ekey, kerr := resolveOptionalKey(cfg.EmbedAPIKey, apiKey)
			if kerr != nil {
				return nil, fmt.Errorf("gateway.embed_api_key: %w", kerr)
			}
			a.embedProvider = ep
			a.embedAPIKey = ekey
			a.embedCfg = &bfschemas.ProviderConfig{
				NetworkConfig: bfschemas.NetworkConfig{BaseURL: providerBaseURL(ep, cfg.EmbedBaseURL)},
			}
			a.distinctEmbed = true
		}
	}

	// Rerank key override (a1b): the active rerank provider may carry its own key.
	if cfg.RerankAPIKey != "" {
		rkey, kerr := resolveAPIKey(cfg.RerankAPIKey)
		if kerr != nil {
			return nil, fmt.Errorf("gateway.rerank_api_key: %w", kerr)
		}
		a.rerankAPIKey = rkey
	}

	// Rerank routing: auto-wired Cohere-shape custom (D-075), a distinct native
	// rerank provider (rerank_provider names one), or the primary entry.
	switch routed := rerankProviderFor(provider, cfg); {
	case routed == customRerankProvider:
		baseURL := cfg.BaseURL
		if cfg.RerankBaseURL != "" {
			baseURL = cfg.RerankBaseURL
		}
		a.customRerank = true
		a.rerankProvider = customRerankProvider
		a.rerankBaseURL = baseURL
		a.rerankCfg = buildRerankProviderConfig(baseURL)
	case routed != provider:
		// Distinct native rerank provider with its own config + key. Use the operator's
		// LITERAL rerank base (origRerankBaseURL) so it does NOT inherit the primary's
		// OpenRouter host (D-134 review, Blocker 1) — empty → that provider's native endpoint.
		a.rerankProvider = routed
		a.distinctRerank = true
		a.rerankCfg = &bfschemas.ProviderConfig{
			NetworkConfig: bfschemas.NetworkConfig{BaseURL: providerBaseURL(routed, origRerankBaseURL)},
		}
	default:
		a.rerankProvider = provider // primary handles rerank (native primary or no rerank model)
	}

	// A distinct embed and a distinct native rerank that name the SAME provider can't
	// carry two different credentials/endpoints (bifrost keys by provider name) — fail
	// loud rather than silently letting embed win (D-134 review, Finding 3). Identical
	// key+base is harmless (one entry serves both).
	if a.distinctEmbed && a.distinctRerank && a.embedProvider == a.rerankProvider {
		if a.embedAPIKey != a.rerankAPIKey || a.embedCfg.NetworkConfig.BaseURL != a.rerankCfg.NetworkConfig.BaseURL {
			return nil, fmt.Errorf("%w: gateway.embed_provider and gateway.rerank_provider both name %q but with different key/base_url — a provider name can hold only one credential; use distinct providers or matching key+base",
				ErrInvalidProvider, a.embedProvider)
		}
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

// rerankIsSeparate reports whether the rerank lane is its own provider entry
// (auto-wired custom OR a distinct native provider), vs handled by the primary.
func (a *Account) rerankIsSeparate() bool { return a.customRerank || a.distinctRerank }

// GetConfiguredProviders implements bfschemas.Account. Returns the primary
// provider plus any distinct per-concern providers (embed and/or rerank, a1b/D-134;
// the auto-wired custom rerank, D-075), deduped, so bifrost starts a worker pool
// for each.
func (a *Account) GetConfiguredProviders() ([]bfschemas.ModelProvider, error) {
	provs := []bfschemas.ModelProvider{a.provider}
	add := func(p bfschemas.ModelProvider) {
		for _, q := range provs {
			if q == p {
				return
			}
		}
		provs = append(provs, p)
	}
	if a.distinctEmbed {
		add(a.embedProvider)
	}
	if a.rerankIsSeparate() {
		add(a.rerankProvider)
	}
	return provs, nil
}

// GetKeysForProvider implements bfschemas.Account. Returns the resolved key for
// the matching configured provider — primary, distinct embed, or rerank — each
// carrying its own credential (a1b/D-134; the rerank/primary share one key unless
// rerank_api_key overrides). Per bifrost docs, Models: []string{"*"} is the
// "all non-blacklisted" wildcard the custom rerank provider REQUIRES.
func (a *Account) GetKeysForProvider(_ context.Context, providerKey bfschemas.ModelProvider) ([]bfschemas.Key, error) {
	var key string
	switch {
	case providerKey == a.provider:
		key = a.apiKey
	case a.distinctEmbed && providerKey == a.embedProvider:
		key = a.embedAPIKey
	case a.rerankIsSeparate() && providerKey == a.rerankProvider:
		key = a.rerankAPIKey
	default:
		return nil, fmt.Errorf("bifrost: provider %q is not configured for this Stowage account", providerKey)
	}
	return []bfschemas.Key{
		{
			ID:     "stowage-default",
			Name:   "stowage-default",
			Value:  bfschemas.EnvVar{Val: key},
			Models: bfschemas.WhiteList{"*"}, // bifrost's "all non-blacklisted" wildcard
			Weight: 1.0,
		},
	}, nil
}

// GetConfigForProvider implements bfschemas.Account. Returns the pre-built
// ProviderConfig for the primary, the distinct embed provider, or the rerank
// provider (custom Cohere-shape or distinct native), a1b/D-134.
func (a *Account) GetConfigForProvider(providerKey bfschemas.ModelProvider) (*bfschemas.ProviderConfig, error) {
	switch {
	case providerKey == a.provider:
		return a.providerCfg, nil
	case a.distinctEmbed && providerKey == a.embedProvider:
		return a.embedCfg, nil
	case a.rerankIsSeparate() && providerKey == a.rerankProvider:
		return a.rerankCfg, nil
	default:
		return nil, fmt.Errorf("bifrost: provider %q is not configured for this Stowage account", providerKey)
	}
}
