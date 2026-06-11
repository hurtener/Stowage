package gateway

import "errors"

// ErrDriverNotRegistered is returned by Open when no factory is registered for
// the requested driver name.
var ErrDriverNotRegistered = errors.New("gateway: driver not registered")

// ErrGatewayUnavailable is returned when the circuit breaker is open. Phase 09
// degrades retrieval to lexical/structured lanes on this error (D-036).
var ErrGatewayUnavailable = errors.New("gateway: provider unavailable")

// ErrTruncated is returned when the provider stopped generation on the token
// limit (finish_reason "length") — the output is incomplete; callers should
// raise MaxTokens. Surfaced before schema validation so truncation is not
// misreported as a schema failure (found by live validation: thinking models
// burn small budgets on reasoning and emit truncated prose).
var ErrTruncated = errors.New("gateway: response truncated at max_tokens")

// ErrSchemaValidation is returned when the model response fails JSON schema
// validation after the seam-level retry (CLAUDE.md §10, AC-2).
var ErrSchemaValidation = errors.New("gateway: schema validation failed")

// ErrProbeFailed is returned by Probe when the provider is reachable but the
// response does not match the configured expectations (e.g. wrong embed dims).
var ErrProbeFailed = errors.New("gateway: probe failed")
