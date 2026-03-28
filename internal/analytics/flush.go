// internal/analytics/flush.go
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
	"time"

	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// flushLoop is a generic, standalone goroutine function that drains rows from
// ch and flushes them to ClickHouse via flushFn (v4 R8 — NOT a struct method).
//
// It accumulates rows in a local buffer and flushes on two triggers:
//  1. The ticker fires (interval-based flush).
//  2. ctx is cancelled (drain-and-exit on shutdown).
//
// On a full flush the buffer is replaced with a fresh allocation; the old
// slice is held only by the flush closure during the PrepareBatch/Send call
// and becomes eligible for GC once the call returns.
//
// Drop policy: rows sent to ch while it is full are counted as FlushErrors
// by the caller (BatchWriter.Push* methods) — flushLoop never blocks.
func flushLoop[T any](
	ctx context.Context,
	ch <-chan T,
	interval time.Duration,
	table string,
	flushFn func(context.Context, driver.Conn, []T) error,
	conn driver.Conn,
	m *WriterMetrics,
) {
	log := logf.FromContext(ctx)
	buf := make([]T, 0, 512)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	doFlush := func() {
		if len(buf) == 0 {
			return
		}
		rows := buf
		buf = make([]T, 0, 512)

		start := time.Now()
		if err := flushFn(ctx, conn, rows); err != nil {
			m.FlushErrors.WithLabelValues(table).Inc()
			log.Error(err, "Batch flush failed",
				"table", table,
				"rows", len(rows),
			)
			return
		}
		m.FlushDuration.WithLabelValues(table).Observe(time.Since(start).Seconds())
		m.RowsWritten.WithLabelValues(table).Add(float64(len(rows)))
	}

	for {
		select {
		case <-ctx.Done():
			// Drain any rows already in the channel buffer before exiting.
			for {
				select {
				case row, ok := <-ch:
					if !ok {
						doFlush()
						return
					}
					buf = append(buf, row)
				default:
					doFlush()
					return
				}
			}

		case row, ok := <-ch:
			if !ok {
				doFlush()
				return
			}
			buf = append(buf, row)
			m.ChannelBacklog.WithLabelValues(table).Set(float64(len(ch)))

		case <-ticker.C:
			doFlush()
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Concrete flush functions — one per table.
// All follow the canonical PrepareBatch → Append → Send pattern (v5 J4).
// Column order in Append must match the CREATE TABLE column order in schema.go.
// ──────────────────────────────────────────────────────────────────────────────

func flushTelemetryBatch(ctx context.Context, conn driver.Conn, rows []ChainTelemetryRow) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO chain_telemetry")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Timestamp,
			r.Namespace,
			r.NodeName,
			r.Network,
			r.SubnetID,
			r.Role,
			r.CurrentBlock,
			r.NetworkBlock,
			r.BlockLag,
			r.FinalizedBlock,
			r.PeerCount,
			r.RuntimeVersion,
			r.SyncState,
			r.BlocksPerSecond,
			r.ProbeLatencyMs,
			r.ProbeSuccess,
			r.DiskUsagePercent,
			r.DiskUsedBytes,
			r.DiskTotalBytes,
			r.GPUUtilizationPercent,
			r.GPUMemoryUsedBytes,
			r.GPUTemperatureCelsius,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func flushReconcileAuditBatch(ctx context.Context, conn driver.Conn, rows []ReconcileAuditRow) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO reconcile_audit")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Timestamp,
			r.Namespace,
			r.NodeName,
			r.TriggerType,
			r.Generation,
			r.ResourceVersion,
			r.PhaseBefore,
			r.SyncStateBefore,
			r.BlockLagBefore,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func flushAnomalyBatch(ctx context.Context, conn driver.Conn, rows []AnomalyRow) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO anomaly_scores")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Timestamp,
			r.Namespace,
			r.NodeName,
			r.Network,
			r.SubnetID,
			r.AnomalyType,
			r.Score,
			r.Detail,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func flushFinOpsBatch(ctx context.Context, conn driver.Conn, rows []FinOpsRow) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO finops_metrics")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Timestamp,
			r.Namespace,
			r.NodeName,
			r.Network,
			r.Role,
			r.EstimatedMonthlyCostUSD,
			r.CPUCores,
			r.MemoryGiB,
			r.StorageGiB,
			r.GPUCount,
			r.IsSpot,
			r.TaoPerGPUHour,
			r.ROIPercent,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}

func flushDRBatch(ctx context.Context, conn driver.Conn, rows []DRRow) error {
	batch, err := conn.PrepareBatch(ctx, "INSERT INTO dr_events")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(
			r.Timestamp,
			r.Namespace,
			r.NodeName,
			r.EventType,
			r.BackupSizeBytes,
			r.DurationSeconds,
			r.Status,
			r.Detail,
		); err != nil {
			return err
		}
	}
	return batch.Send()
}
