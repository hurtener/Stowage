// export_test.go exposes internal functions for use in reconcile_test package.
package reconcile

import (
	"encoding/json"

	"github.com/hurtener/stowage/internal/store"
)

// ExportTargetTrustLevel exposes targetTrustLevel for external tests.
func ExportTargetTrustLevel(m store.Memory) TrustLevel {
	return targetTrustLevel(m)
}

// ExportTrustLevelLow is the TrustLevelLow constant for external tests.
const ExportTrustLevelLow = TrustLevelLow

// ExportTrustLevelMedium is the TrustLevelMedium constant for external tests.
const ExportTrustLevelMedium = TrustLevelMedium

// ExportTrustLevelHigh is the TrustLevelHigh constant for external tests.
const ExportTrustLevelHigh = TrustLevelHigh

// ExportTrustGateWarn exposes the warn threshold for external tests.
const ExportTrustGateWarn = trustGateWarn

// ExportTrustGatePark exposes the park threshold for external tests.
const ExportTrustGatePark = trustGatePark

// ExportContradictionBoostImportanceFloor exposes the importance floor for tests.
const ExportContradictionBoostImportanceFloor = contradictionBoostImportanceFloor

// ExportContradictionBoostStabilityDelta exposes the stability delta for tests.
const ExportContradictionBoostStabilityDelta = contradictionBoostStabilityDelta

// ExportNearDupThreshold exposes the near-dup Jaccard threshold for tests.
const ExportNearDupThreshold = nearDupThreshold

// ExportValidateDecision exposes validateDecision for external tests.
// It operates on a copy so callers observe the deduplicated target_ids.
func ExportValidateDecision(d DecisionOutput) error {
	return validateDecision(&d)
}

// ExportParseDecision exposes parseDecision for fuzz testing.
func ExportParseDecision(raw json.RawMessage) (DecisionOutput, error) {
	return parseDecision(raw)
}
