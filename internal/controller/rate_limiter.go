// internal/controller/rate_limiter.go
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

package controller

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// RecoveryRateLimiter prevents thundering-herd during fleet-wide events.
//
// If 50 nodes go out-of-sync simultaneously (e.g., chain fork, upstream
// outage), we must NOT restart all 50 at once — that causes:
//  1. API server overload from 50 concurrent StatefulSet updates
//  2. Bandwidth saturation from 50 nodes re-syncing simultaneously
//  3. Cascading peer loss (restarting nodes lose their peer connections)
//
// The limiter uses a sliding window with a concurrent-recovery cap and
// a per-node cooldown to prevent rapid restart loops.
type RecoveryRateLimiter struct {
	mu                 sync.Mutex
	activeRecoveries   map[string]time.Time // nodeKey → recovery start time
	lastRecoveryByNode map[string]time.Time // nodeKey → last recovery time
	maxConcurrent      int
	cooldownPerNode    time.Duration

	recoveryThrottled     prometheus.Counter
	activeRecoveriesGauge prometheus.Gauge
}

var (
	recoveryThrottledMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "taonode_recovery_throttled_total",
		Help: "Recovery actions delayed by the rate limiter (thundering herd protection).",
	})
	activeRecoveriesGaugeMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "taonode_recovery_active",
		Help: "Number of currently active (in-flight) recovery actions.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(recoveryThrottledMetric, activeRecoveriesGaugeMetric)
}

// NewRecoveryRateLimiter creates a rate limiter that allows at most
// maxConcurrent simultaneous recovery actions and enforces a cooldown
// period between attempts on the same node.
func NewRecoveryRateLimiter(maxConcurrent int, cooldown time.Duration) *RecoveryRateLimiter {
	return &RecoveryRateLimiter{
		activeRecoveries:      make(map[string]time.Time),
		lastRecoveryByNode:    make(map[string]time.Time),
		maxConcurrent:         maxConcurrent,
		cooldownPerNode:       cooldown,
		recoveryThrottled:     recoveryThrottledMetric,
		activeRecoveriesGauge: activeRecoveriesGaugeMetric,
	}
}

// TryAcquire returns true if recovery is permitted for this node.
// If false, the caller should requeue with a delay (not return an error).
func (rl *RecoveryRateLimiter) TryAcquire(nodeKey string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Evict stale entries (recoveries older than 5 minutes are assumed complete).
	for k, started := range rl.activeRecoveries {
		if time.Since(started) > 5*time.Minute {
			delete(rl.activeRecoveries, k)
		}
	}

	// Enforce fleet-wide concurrency cap.
	if len(rl.activeRecoveries) >= rl.maxConcurrent {
		rl.recoveryThrottled.Inc()
		return false
	}

	// Enforce per-node cooldown.
	if last, exists := rl.lastRecoveryByNode[nodeKey]; exists {
		if time.Since(last) < rl.cooldownPerNode {
			rl.recoveryThrottled.Inc()
			return false
		}
	}

	rl.activeRecoveries[nodeKey] = time.Now()
	rl.lastRecoveryByNode[nodeKey] = time.Now()
	rl.activeRecoveriesGauge.Set(float64(len(rl.activeRecoveries)))
	return true
}

// Release marks a recovery as complete and decrements the active counter.
func (rl *RecoveryRateLimiter) Release(nodeKey string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.activeRecoveries, nodeKey)
	rl.activeRecoveriesGauge.Set(float64(len(rl.activeRecoveries)))
}

// ActiveCount returns the current number of in-flight recoveries.
// Used in event messages for observability.
func (rl *RecoveryRateLimiter) ActiveCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.activeRecoveries)
}
