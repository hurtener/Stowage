// export_test.go exposes internal functions for use in reconcile_test package.
package reconcile

import (
	"context"
	"encoding/json"

	"github.com/hurtener/stowage/internal/identity"
	"github.com/hurtener/stowage/internal/pipeline"
	"github.com/hurtener/stowage/internal/store"
)

// ExportBuildReconcileContext exposes buildReconcileContext for the D-108 context test.
func (r *ReconcileStage) ExportBuildReconcileContext(ctx context.Context, scope identity.Scope, c pipeline.Candidate, neighbors []store.Memory) ReconcileContext {
	return r.buildReconcileContext(ctx, scope, c, neighbors)
}

// ExportTrustRank exposes trustRank for the D-111 survivor-selection test.
func ExportTrustRank(src string) float64 { return trustRank(src) }

// ExportCandidateAssertionKey exposes candidateAssertionKey for the D-106 ordering test.
func ExportCandidateAssertionKey(c pipeline.Candidate) string {
	return candidateAssertionKey(c)
}

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

// ExportCandidateOlderThanTarget exposes candidateOlderThanTarget for the D-127
// date-direction-guard test.
func ExportCandidateOlderThanTarget(c pipeline.Candidate, target store.Memory) bool {
	return candidateOlderThanTarget(c, target)
}

// ExportValidateDecision exposes validateDecision for external tests.
// It operates on a copy so callers observe the deduplicated target_ids.
func ExportValidateDecision(d DecisionOutput) error {
	return validateDecision(&d)
}

// ExportParseDecision exposes parseDecision for fuzz testing.
func ExportParseDecision(raw json.RawMessage) (DecisionOutput, error) {
	return parseDecision(raw)
}
