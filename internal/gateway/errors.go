package gateway

import "errors"

// ErrDriverNotRegistered is returned by Open when no factory is registered for
// the requested driver name.
var ErrDriverNotRegistered = errors.New("gateway: driver not registered")

// ErrGatewayUnavailable is returned when the circuit breaker is open. Phase 09
// degrades retrieval to lexical/structured lanes on this error (D-036).
var ErrGatewayUnavailable = errors.New("gateway: provider unavailable")

// ErrSchemaValidation is returned when the model response fails JSON schema
// validation after the seam-level retry (CLAUDE.md §10, AC-2).
var ErrSchemaValidation = errors.New("gateway: schema validation failed")

// ErrProbeFailed is returned by Probe when the provider is reachable but the
// response does not match the configured expectations (e.g. wrong embed dims).
var ErrProbeFailed = errors.New("gateway: probe failed")
