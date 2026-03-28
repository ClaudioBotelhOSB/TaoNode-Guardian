// internal/analytics/anomaly_detector.go
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
	"context"
	"fmt"
	"math"
	"time"

	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// AnomalyDetector queries ClickHouse telemetry to produce per-node anomaly scores.
// It is safe for concurrent use: all state is read-only after construction.
type AnomalyDetector struct {
	conn driver.Conn
}

// NewAnomalyDetector creates an AnomalyDetector backed by the given connection.
func NewAnomalyDetector(conn driver.Conn) *AnomalyDetector {
	return &AnomalyDetector{conn: conn}
}

// EvaluateNode runs five anomaly-detection queries concurrently (v5 J1).
// A shared context timeout of 10 s is applied; individual query failures are
// non-fatal and do not prevent returning the results of the other queries.
//
// Returns only AnomalyScores where Score > 0.1 (noise floor).
// DetectedAt is stamped to time.Now() on all returned scores.
func (d *AnomalyDetector) EvaluateNode(
	ctx context.Context,
	namespace, nodeName, network string,
	subnetID uint16,
) ([]AnomalyScore, error) {
	// Each query shares a single 10 s deadline (v5 J1).
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	type queryResult struct {
		score AnomalyScore
		err   error
	}

	queries := []func(context.Context) queryResult{
		func(c context.Context) queryResult {
			s, err := d.blockLagAnomaly(c, namespace, nodeName)
			return queryResult{s, err}
		},
		func(c context.Context) queryResult {
			s, err := d.peerCountAnomaly(c, namespace, nodeName)
			return queryResult{s, err}
		},
		func(c context.Context) queryResult {
			s, err := d.diskUsageAnomaly(c, namespace, nodeName)
			return queryResult{s, err}
		},
		func(c context.Context) queryResult {
			s, err := d.recoveryFrequencyAnomaly(c, namespace, nodeName)
			return queryResult{s, err}
		},
		func(c context.Context) queryResult {
			s, err := d.probeLatencyAnomaly(c, namespace, nodeName)
			return queryResult{s, err}
		},
	}

	now := time.Now().UTC()
	resultsCh := make(chan queryResult, len(queries))
	for _, q := range queries {
		q := q
		go func() {
			result := q(qctx)
			select {
			case resultsCh <- result:
			case <-qctx.Done():
			}
		}()
	}

	var scores []AnomalyScore
	for received := 0; received < len(queries); received++ {
		select {
		case <-qctx.Done():
			return scores, nil
		case r := <-resultsCh:
			if r.err != nil {
				continue
			}
			if r.score.Score > 0.1 {
				r.score.DetectedAt = now
				scores = append(scores, r.score)
			}
		}
	}
	return scores, nil
}

// blockLagAnomaly detects abnormally high block lag using a z-score computed
// over the rolling 30-minute window of chain_telemetry.
func (d *AnomalyDetector) blockLagAnomaly(ctx context.Context, namespace, nodeName string) (AnomalyScore, error) {
	row := d.conn.QueryRow(ctx, `
		SELECT
			avg(block_lag)          AS mean_lag,
			stddevPop(block_lag)    AS std_lag,
			max(block_lag)          AS current_lag
		FROM chain_telemetry
		WHERE namespace = ? AND node_name = ?
		  AND timestamp >= now() - INTERVAL 30 MINUTE
	`, namespace, nodeName)

	var mean, std, current float64
	if err := row.Scan(&mean, &std, &current); err != nil {
		return AnomalyScore{}, fmt.Errorf("block-lag anomaly query: %w", err)
	}

	score := 0.0
	if std > 0 {
		z := (current - mean) / std
		score = math.Min(1.0, math.Max(0.0, z/3.0)) // z=3σ → score=1.0
	} else if current > 10 {
		score = 0.5 // no variance yet but already lagging
	}

	return AnomalyScore{
		Type:   "block-lag",
		Score:  float32(score),
		Detail: fmt.Sprintf("current=%.0f mean=%.1f std=%.1f", current, mean, std),
	}, nil
}

// peerCountAnomaly detects a sudden drop in connected peers relative to the
// 30-minute rolling mean.
func (d *AnomalyDetector) peerCountAnomaly(ctx context.Context, namespace, nodeName string) (AnomalyScore, error) {
	row := d.conn.QueryRow(ctx, `
		SELECT
			avg(peer_count) AS mean_peers,
			min(peer_count) AS min_peers
		FROM chain_telemetry
		WHERE namespace = ? AND node_name = ?
		  AND timestamp >= now() - INTERVAL 30 MINUTE
	`, namespace, nodeName)

	var mean, minPeers float64
	if err := row.Scan(&mean, &minPeers); err != nil {
		return AnomalyScore{}, fmt.Errorf("peer-count anomaly query: %w", err)
	}

	score := 0.0
	switch {
	case mean > 0:
		score = math.Min(1.0, math.Max(0.0, (mean-minPeers)/mean))
	case minPeers == 0:
		score = 0.8 // no peers at all
	}

	return AnomalyScore{
		Type:   "peer-count",
		Score:  float32(score),
		Detail: fmt.Sprintf("min=%.0f rolling_mean=%.1f", minPeers, mean),
	}, nil
}

// diskUsageAnomaly detects when disk usage is approaching capacity.
// Score ramps linearly from 0 at 70% usage to 1.0 at 100% usage.
func (d *AnomalyDetector) diskUsageAnomaly(ctx context.Context, namespace, nodeName string) (AnomalyScore, error) {
	row := d.conn.QueryRow(ctx, `
		SELECT max(disk_usage_percent)
		FROM chain_telemetry
		WHERE namespace = ? AND node_name = ?
		  AND timestamp >= now() - INTERVAL 10 MINUTE
	`, namespace, nodeName)

	var pct float64
	if err := row.Scan(&pct); err != nil {
		return AnomalyScore{}, fmt.Errorf("disk-usage anomaly query: %w", err)
	}

	score := 0.0
	if pct > 70 {
		score = math.Min(1.0, (pct-70)/30)
	}

	return AnomalyScore{
		Type:   "disk-usage",
		Score:  float32(score),
		Detail: fmt.Sprintf("max_usage_pct=%.0f", pct),
	}, nil
}

// recoveryFrequencyAnomaly detects nodes that are restarting abnormally often.
// > 3 restarts/hour produces a non-zero score; 5 restarts/hour → score = 1.0.
func (d *AnomalyDetector) recoveryFrequencyAnomaly(ctx context.Context, namespace, nodeName string) (AnomalyScore, error) {
	row := d.conn.QueryRow(ctx, `
		SELECT count()
		FROM dr_events
		WHERE namespace = ? AND node_name = ?
		  AND event_type = 'recovery'
		  AND timestamp >= now() - INTERVAL 1 HOUR
	`, namespace, nodeName)

	var count uint64
	if err := row.Scan(&count); err != nil {
		return AnomalyScore{}, fmt.Errorf("recovery-frequency anomaly query: %w", err)
	}

	score := math.Min(1.0, float64(count)/5.0)

	return AnomalyScore{
		Type:   "recovery-frequency",
		Score:  float32(score),
		Detail: fmt.Sprintf("recoveries_last_hour=%d", count),
	}, nil
}

// probeLatencyAnomaly detects high chain-probe latency using the p95 value
// over the past 30 minutes. p95 > 5 000 ms → score = 1.0.
func (d *AnomalyDetector) probeLatencyAnomaly(ctx context.Context, namespace, nodeName string) (AnomalyScore, error) {
	row := d.conn.QueryRow(ctx, `
		SELECT
			avg(probe_latency_ms)            AS mean_ms,
			quantile(0.95)(probe_latency_ms) AS p95_ms
		FROM chain_telemetry
		WHERE namespace = ? AND node_name = ?
		  AND timestamp >= now() - INTERVAL 30 MINUTE
	`, namespace, nodeName)

	var mean, p95 float64
	if err := row.Scan(&mean, &p95); err != nil {
		return AnomalyScore{}, fmt.Errorf("probe-latency anomaly query: %w", err)
	}

	score := 0.0
	if p95 > 1000 {
		score = math.Min(1.0, (p95-1000)/4000)
	}

	return AnomalyScore{
		Type:   "probe-latency",
		Score:  float32(score),
		Detail: fmt.Sprintf("mean_ms=%.0f p95_ms=%.0f", mean, p95),
	}, nil
}
