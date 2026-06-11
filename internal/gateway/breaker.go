package gateway

import (
	"sync"
	"time"
)

const (
	// BreakerThreshold is the number of consecutive failures that open the circuit.
	BreakerThreshold = 5
	// BreakerOpenPeriod is how long the circuit stays open before allowing a probe.
	BreakerOpenPeriod = 30 * time.Second
)

type breakerState int8

const (
	bsClosed breakerState = iota
	bsOpen
	bsHalfOpen
)

// CircuitBreaker is a 3-state breaker: closed → open (N consecutive failures)
// → half-open (after BreakerOpenPeriod) → closed or open depending on probe.
// Thread-safe.
type CircuitBreaker struct {
	mu         sync.Mutex
	state      breakerState
	failures   int
	openedAt   time.Time
	threshold  int
	openPeriod time.Duration
}

// NewCircuitBreaker returns a CircuitBreaker with default thresholds.
func NewCircuitBreaker() *CircuitBreaker {
	return &CircuitBreaker{
		threshold:  BreakerThreshold,
		openPeriod: BreakerOpenPeriod,
	}
}

// Allow returns ErrGatewayUnavailable when the circuit is open and the half-open
// probe window has not elapsed; returns nil otherwise (call may proceed).
func (b *CircuitBreaker) Allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case bsOpen:
		if time.Since(b.openedAt) >= b.openPeriod {
			b.state = bsHalfOpen
			return nil
		}
		return ErrGatewayUnavailable
	default:
		return nil
	}
}

// Success records a successful call and resets the breaker to closed.
func (b *CircuitBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
	b.state = bsClosed
}

// Failure records a failed call; transitions to open when threshold is reached,
// and immediately back to open from half-open.
func (b *CircuitBreaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case bsHalfOpen:
		b.state = bsOpen
		b.openedAt = time.Now()
	default:
		b.failures++
		if b.failures >= b.threshold {
			b.state = bsOpen
			b.openedAt = time.Now()
		}
	}
}
