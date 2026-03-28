// internal/analytics/finops_calculator.go
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

// NodeCostSummary aggregates cost data for a single TaoNode over a time period.
type NodeCostSummary struct {
	Namespace      string
	NodeName       string
	Network        string
	Role           string
	Period         time.Duration
	TotalCostUSD   float64 // estimated cost for the period
	AvgMonthlyCost float64 // avg estimated_monthly_cost_usd over the period
	TotalGPUHours  float64 // gpu_count * period_hours (summed)
	AvgROIPercent  float64
	AvgTaoPerGPUHr float64
}

// FleetCostReport aggregates cost data for all nodes observed in a period.
type FleetCostReport struct {
	Period         time.Duration
	Nodes          []NodeCostSummary // sorted DESC by TotalCostUSD
	TotalCostUSD   float64
	CostByNetwork  map[string]float64
	CostByRole     map[string]float64
	TopCostlyNodes []NodeCostSummary // top 5 by TotalCostUSD
	GeneratedAt    time.Time
}

// FinOpsCalculator computes cost analytics from the finops_metrics ClickHouse table.
type FinOpsCalculator struct {
	conn driver.Conn
}

// NewFinOpsCalculator creates a FinOpsCalculator.
func NewFinOpsCalculator(conn driver.Conn) *FinOpsCalculator {
	return &FinOpsCalculator{conn: conn}
}

// NodeCostSummaryForPeriod returns aggregated cost metrics for a single node
// over the specified duration. Uses 730 hours/month as the billing denominator.
func (fc *FinOpsCalculator) NodeCostSummaryForPeriod(
	ctx context.Context,
	namespace, nodeName string,
	period time.Duration,
) (NodeCostSummary, error) {
	hours := period.Hours()

	row := fc.conn.QueryRow(ctx, `
		SELECT
			namespace,
			node_name,
			any(network)                                        AS network,
			any(role)                                           AS role,
			avg(estimated_monthly_cost_usd)                     AS avg_monthly_cost,
			avg(estimated_monthly_cost_usd) * ? / 730           AS total_cost,
			sum(gpu_count) * ? / 730                            AS total_gpu_hours,
			avg(roi_percent)                                    AS avg_roi,
			avg(tao_per_gpu_hour)                               AS avg_tao_per_gpu_hr
		FROM finops_metrics
		WHERE namespace = ? AND node_name = ?
		  AND timestamp >= now() - toIntervalHour(?)
		GROUP BY namespace, node_name
	`, hours, hours, namespace, nodeName, int(hours))

	var s NodeCostSummary
	s.Period = period
	if err := row.Scan(
		&s.Namespace,
		&s.NodeName,
		&s.Network,
		&s.Role,
		&s.AvgMonthlyCost,
		&s.TotalCostUSD,
		&s.TotalGPUHours,
		&s.AvgROIPercent,
		&s.AvgTaoPerGPUHr,
	); err != nil {
		return NodeCostSummary{}, fmt.Errorf(
			"node cost summary for %s/%s: %w", namespace, nodeName, err)
	}
	return s, nil
}

// FleetCostReportForPeriod generates a full fleet cost report across all nodes
// that reported finops_metrics in the given period.
func (fc *FinOpsCalculator) FleetCostReportForPeriod(
	ctx context.Context,
	period time.Duration,
) (FleetCostReport, error) {
	hours := period.Hours()

	rows, err := fc.conn.Query(ctx, `
		SELECT
			namespace,
			node_name,
			any(network)                                        AS network,
			any(role)                                           AS role,
			avg(estimated_monthly_cost_usd)                     AS avg_monthly_cost,
			avg(estimated_monthly_cost_usd) * ? / 730           AS total_cost,
			sum(gpu_count) * ? / 730                            AS total_gpu_hours,
			avg(roi_percent)                                    AS avg_roi,
			avg(tao_per_gpu_hour)                               AS avg_tao_per_gpu_hr
		FROM finops_metrics
		WHERE timestamp >= now() - toIntervalHour(?)
		GROUP BY namespace, node_name
		ORDER BY total_cost DESC
	`, hours, hours, int(hours))
	if err != nil {
		return FleetCostReport{}, fmt.Errorf("fleet cost report query: %w", err)
	}
	defer rows.Close()

	report := FleetCostReport{
		Period:        period,
		CostByNetwork: make(map[string]float64),
		CostByRole:    make(map[string]float64),
		GeneratedAt:   time.Now().UTC(),
	}

	for rows.Next() {
		var s NodeCostSummary
		s.Period = period
		if err := rows.Scan(
			&s.Namespace,
			&s.NodeName,
			&s.Network,
			&s.Role,
			&s.AvgMonthlyCost,
			&s.TotalCostUSD,
			&s.TotalGPUHours,
			&s.AvgROIPercent,
			&s.AvgTaoPerGPUHr,
		); err != nil {
			return FleetCostReport{}, fmt.Errorf("scan fleet cost row: %w", err)
		}
		report.Nodes = append(report.Nodes, s)
		report.TotalCostUSD += s.TotalCostUSD
		report.CostByNetwork[s.Network] += s.TotalCostUSD
		report.CostByRole[s.Role] += s.TotalCostUSD
	}
	if err := rows.Err(); err != nil {
		return FleetCostReport{}, fmt.Errorf("iterate fleet cost rows: %w", err)
	}

	// Results are already sorted DESC by total_cost from the query.
	// Capture top 5 before returning.
	top := 5
	if len(report.Nodes) < top {
		top = len(report.Nodes)
	}
	report.TopCostlyNodes = report.Nodes[:top]

	return report, nil
}
