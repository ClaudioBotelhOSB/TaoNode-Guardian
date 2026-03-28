// internal/analytics/circuit_breaker.go
/*
Copyright 2026 Claudio Botelho.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package analytics

import (
	"sync"
	"sync/atomic"
	"time"
)

// cbState enumerates the three states of the circuit breaker.
type cbState uint32

const (
	cbStateClosed   cbState = iota // requests flow normally
	cbStateOpen                    // requests are rejected
	cbStateHalfOpen                // a probe is allowed through to test recovery
)

// CircuitBreaker is a lock-minimised circuit breaker for ClickHouse connections.
//
// Deadlock-free design (v4 §8):
//   - state is an atomic.Uint32; no mutex required to *read* it.
//   - Allow() uses only sync.RWMutex.RLock for reading time-based fields
//     (lastFailureAt), then a single atomic.CompareAndSwap for state transition.
//     There is no nested lock acquisition anywhere in Allow().
//   - RecordFailure / RecordSuccess use a single sync.Mutex.Lock() for
//     mutation of counters; state transitions are done with atomic.Store
//     *after* the mutex is released.
type CircuitBreaker struct {
	// state is the authoritative circuit state. Transitions use CAS.
	state atomic.Uint32
	// halfOpenPermit ensures only one probe is allowed while half-open.
	halfOpenPermit atomic.Bool

	// mu protects the mutable time/counter fields below.
	mu            sync.RWMutex
	failures      int
	successes     int
	lastFailureAt time.Time

	// configuration (immutable after construction)
	maxFailures    int
	resetTimeout   time.Duration
	halfOpenProbes int // consecutive successes needed to re-close
}

// NewCircuitBreaker creates a CircuitBreaker in Closed state.
//
//   - maxFailures: consecutive failures required to open the circuit.
//   - resetTimeout: time to wait in Open before probing.
//   - halfOpenProbes: successful probes required to close from HalfOpen.
func NewCircuitBreaker(maxFailures int, resetTimeout time.Duration, halfOpenProbes int) *CircuitBreaker {
	cb := &CircuitBreaker{
		maxFailures:    maxFailures,
		resetTimeout:   resetTimeout,
		halfOpenProbes: halfOpenProbes,
	}
	cb.state.Store(uint32(cbStateClosed))
	return cb
}

// Allow reports whether a request should be passed to ClickHouse.
//
// Implementation contract (v4 §8 — no nested lock acquisition):
//  1. Atomically load state (no lock).
//  2. For Open: acquire RLock, read lastFailureAt, release RLock, then CAS
//     Open → HalfOpen (no lock held during CAS).
//  3. Lock is only re-acquired after the CAS completes, never while held.
func (cb *CircuitBreaker) Allow() bool {
	switch cbState(cb.state.Load()) {
	case cbStateClosed:
		return true

	case cbStateOpen:
		// Read the time field under a read-lock, then release before CAS.
		cb.mu.RLock()
		elapsed := time.Since(cb.lastFailureAt)
		cb.mu.RUnlock()

		if elapsed < cb.resetTimeout {
			return false // still within the blackout window
		}

		if cb.halfOpenPermit.CompareAndSwap(false, true) {
			if cb.state.CompareAndSwap(uint32(cbStateOpen), uint32(cbStateHalfOpen)) {
				cb.mu.Lock()
				cb.successes = 0
				cb.mu.Unlock()
				return true
			}
			cb.halfOpenPermit.Store(false)
		}
		return false

	case cbStateHalfOpen:
		return false

	default:
		return true
	}
}

// RecordSuccess registers a successful ClickHouse call.
// In HalfOpen, accumulates probe successes and closes the circuit when the
// halfOpenProbes threshold is reached.
func (cb *CircuitBreaker) RecordSuccess() {
	if cbState(cb.state.Load()) == cbStateHalfOpen {
		cb.mu.Lock()
		cb.successes++
		shouldClose := cb.successes >= cb.halfOpenProbes
		if shouldClose {
			cb.failures = 0
			cb.successes = 0
		}
		cb.mu.Unlock()

		if shouldClose {
			cb.state.Store(uint32(cbStateClosed))
			cb.halfOpenPermit.Store(false)
		}
		return
	}

	// In Closed state reset the failure streak on any success.
	if cbState(cb.state.Load()) == cbStateClosed {
		cb.mu.Lock()
		cb.failures = 0
		cb.mu.Unlock()
	}
}

// RecordFailure registers a failed ClickHouse call.
// Opens the circuit when maxFailures consecutive failures are reached,
// or immediately if the circuit is already HalfOpen (any failure re-opens).
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	cb.failures++
	cb.lastFailureAt = time.Now()
	currentState := cbState(cb.state.Load())
	shouldOpen := cb.failures >= cb.maxFailures || currentState == cbStateHalfOpen
	cb.mu.Unlock()

	if shouldOpen {
		cb.state.Store(uint32(cbStateOpen))
		cb.halfOpenPermit.Store(false)
	}
}

// IsOpen returns true when the circuit is open (rejecting requests).
func (cb *CircuitBreaker) IsOpen() bool {
	return cbState(cb.state.Load()) == cbStateOpen
}

// State returns the current circuit state as a human-readable string.
func (cb *CircuitBreaker) State() string {
	switch cbState(cb.state.Load()) {
	case cbStateClosed:
		return "closed"
	case cbStateOpen:
		return "open"
	case cbStateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}
