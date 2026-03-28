// internal/analytics/fleet_correlator.go
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
	"time"

	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// FleetAnomaly represents a correlated anomaly that affects a significant
// fraction of nodes within a network simultaneously.
type FleetAnomaly struct {
	// Network is the Bittensor network (e.g. "mainnet", "testnet").
	Network string

	// AnomalyType matches the AnomalyScore.Type that was detected fleet-wide.
	AnomalyType string

	// AffectedNodes is the number of distinct nodes reporting this anomaly type.
	AffectedNodes int

	// TotalNodes is the total number of active nodes seen in the last 15 minutes.
	TotalNodes int

	// AvgScore is the average anomaly score across all affected nodes.
	AvgScore float64

	// DetectedAt is when the correlation query ran.
	DetectedAt time.Time

	// Detail is a human-readable summary (e.g. "42% of nodes affected").
	Detail string
}

// FleetCorrelator identifies anomaly patterns that affect a configurable
// fraction of the fleet within a network simultaneously.
//
// It queries anomaly_scores grouped by anomaly_type and flags patterns where
// the affected-node ratio exceeds the threshold. A threshold of 0.3 means
// "alert when ≥ 30% of active nodes show this anomaly type".
type FleetCorrelator struct {
	conn      driver.Conn
	threshold float64 // 0.0–1.0 fraction of active nodes required to trigger
}

// NewFleetCorrelator creates a FleetCorrelator.
// threshold is a fraction between 0 and 1 (e.g. 0.3 = 30% of active nodes).
func NewFleetCorrelator(conn driver.Conn, threshold float64) *FleetCorrelator {
	return &FleetCorrelator{conn: conn, threshold: threshold}
}

// CorrelateFleet queries the last 15 minutes of anomaly_scores for the given
// network and returns FleetAnomalies where the fraction of affected nodes
// meets or exceeds the configured threshold.
func (fc *FleetCorrelator) CorrelateFleet(ctx context.Context, network string) ([]FleetAnomaly, error) {
	totalNodes, err := fc.countActiveNodes(ctx, network)
	if err != nil {
		return nil, err
	}
	if totalNodes == 0 {
		return nil, nil
	}

	rows, err := fc.conn.Query(ctx, `
		SELECT
			anomaly_type,
			countDistinct(node_name) AS affected_nodes,
			avg(score)               AS avg_score
		FROM anomaly_scores
		WHERE network = ?
		  AND timestamp >= now() - INTERVAL 15 MINUTE
		  AND score > 0.3
		GROUP BY anomaly_type
		HAVING countDistinct(node_name) >= 2
		ORDER BY avg_score DESC
	`, network)
	if err != nil {
		return nil, fmt.Errorf("fleet correlation query: %w", err)
	}
	defer rows.Close()

	var anomalies []FleetAnomaly
	for rows.Next() {
		var anomalyType string
		var affectedNodes int
		var avgScore float64
		if err := rows.Scan(&anomalyType, &affectedNodes, &avgScore); err != nil {
			return nil, fmt.Errorf("scan fleet anomaly row: %w", err)
		}

		fraction := float64(affectedNodes) / float64(totalNodes)
		if fraction < fc.threshold {
			continue
		}

		anomalies = append(anomalies, FleetAnomaly{
			Network:       network,
			AnomalyType:   anomalyType,
			AffectedNodes: affectedNodes,
			TotalNodes:    totalNodes,
			AvgScore:      avgScore,
			DetectedAt:    time.Now().UTC(),
			Detail: fmt.Sprintf(
				"%.0f%% of nodes affected (threshold %.0f%%)",
				fraction*100, fc.threshold*100,
			),
		})
	}
	return anomalies, rows.Err()
}

// countActiveNodes returns the number of distinct nodes that have submitted
// telemetry for the given network in the last 15 minutes.
func (fc *FleetCorrelator) countActiveNodes(ctx context.Context, network string) (int, error) {
	row := fc.conn.QueryRow(ctx, `
		SELECT countDistinct(node_name)
		FROM chain_telemetry
		WHERE network = ?
		  AND timestamp >= now() - INTERVAL 15 MINUTE
	`, network)

	var count int
	if err := row.Scan(&count); err != nil {
		return 0, fmt.Errorf("count active nodes for network %q: %w", network, err)
	}
	return count, nil
}
