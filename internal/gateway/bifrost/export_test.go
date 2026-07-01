package bifrost

import (
	"log/slog"

	bfschemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/hurtener/stowage/internal/config"
)

// NewDriverWithClient is a test-only constructor that injects a fake
// bifrostClient instead of spinning up the real SDK infrastructure.
// Exported through the package under test so driver_test.go can use it.
func NewDriverWithClient(
	client bifrostClient,
	provider bfschemas.ModelProvider,
	cfg config.GatewayConfig,
	log *slog.Logger,
	prom *prometheus.Registry,
) *Driver {
	return newDriverWithClient(client, provider, cfg, log, prom)
}

// KnownProvidersHuman exports the provider list for test assertions.
func KnownProvidersHuman() string { return knownProvidersHuman() }

// NewAccount exposes the internal newAccount constructor for tests so that
// the Account interface methods (GetConfiguredProviders, GetKeysForProvider,
// GetConfigForProvider) and buildProviderConfig can be covered.
func NewAccount(cfg config.GatewayConfig) (*Account, error) {
	return newAccount(cfg)
}

// CustomRerankProviderName exports the synthetic rerank provider name for tests.
func CustomRerankProviderName() bfschemas.ModelProvider { return customRerankProvider }

// CustomRerank reports whether the account auto-wired the Cohere-shape rerank
// provider (test accessor for the unexported field).
func (a *Account) CustomRerank() bool { return a.customRerank }

// RerankProviderName returns the provider rerank routes to (Account accessor).
func (a *Account) RerankProviderName() bfschemas.ModelProvider { return a.rerankProvider }

// RerankBaseURL returns the base URL of the auto-wired rerank provider (test
// accessor); empty unless a custom rerank provider is wired.
func (a *Account) RerankBaseURL() string { return a.rerankBaseURL }

// RerankProviderName returns the provider the Driver routes rerank to.
func (d *Driver) RerankProviderName() bfschemas.ModelProvider { return d.rerankProvider }

// IsNativeRerankProvider exports the native-rerank gate for tests.
func IsNativeRerankProvider(p bfschemas.ModelProvider) bool { return isNativeRerankProvider(p) }

// EmbedProviderName returns the provider the embed lane routes to (a1b accessor).
func (a *Account) EmbedProviderName() bfschemas.ModelProvider { return a.embedProvider }

// DistinctEmbed reports whether embed routes to a distinct provider entry (a1b).
func (a *Account) DistinctEmbed() bool { return a.distinctEmbed }

// EmbedProviderName returns the provider the Driver routes embed to (a1b accessor).
func (d *Driver) EmbedProviderName() bfschemas.ModelProvider { return d.embedProvider }
