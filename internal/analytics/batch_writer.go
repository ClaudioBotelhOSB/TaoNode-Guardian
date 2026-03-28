// internal/analytics/batch_writer.go
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
	"sync"
	"time"

	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

const (
	// defaultChannelSize is the depth of each row channel.
	// At 4096 entries the memory footprint per channel is bounded even for
	// the largest row type (ChainTelemetryRow ≈ 120 bytes → ~480 KiB max).
	defaultChannelSize = 4096

	// defaultFlushInterval controls how often buffered rows are sent to
	// ClickHouse. 5 s is a good balance between latency and batch efficiency.
	defaultFlushInterval = 5 * time.Second
)

// BatchWriter is a non-blocking, asynchronous writer to ClickHouse.
// Each row type has its own buffered channel and a dedicated flushLoop goroutine,
// so a slow or failing table never stalls ingestion for other tables.
//
// BatchWriter implements manager.Runnable — register it with mgr.Add() in
// cmd/main.go so the manager manages its lifecycle.
//
// All Push* methods are non-blocking: if the channel is full the row is
// dropped and a FlushErrors counter is incremented.
type BatchWriter struct {
	conn    driver.Conn
	metrics *WriterMetrics

	// One buffered channel per row type.
	telemetry      chan ChainTelemetryRow
	reconcileAudit chan ReconcileAuditRow
	anomalies      chan AnomalyRow
	finops         chan FinOpsRow
	dr             chan DRRow
}

// NewBatchWriter creates a BatchWriter using the DefaultWriterMetrics() singleton
// (v5 I1 — callers MUST NOT pass a custom WriterMetrics; use the package singleton).
func NewBatchWriter(conn driver.Conn) *BatchWriter {
	return &BatchWriter{
		conn:           conn,
		metrics:        DefaultWriterMetrics(),
		telemetry:      make(chan ChainTelemetryRow, defaultChannelSize),
		reconcileAudit: make(chan ReconcileAuditRow, defaultChannelSize),
		anomalies:      make(chan AnomalyRow, defaultChannelSize),
		finops:         make(chan FinOpsRow, defaultChannelSize),
		dr:             make(chan DRRow, defaultChannelSize),
	}
}

// Start launches all flush goroutines and blocks until ctx is cancelled.
// It implements manager.Runnable. The manager calls Start in a dedicated
// goroutine; Start returns only after all goroutines have completed their
// final drain-and-flush sequence.
func (w *BatchWriter) Start(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(5)

	go func() {
		defer wg.Done()
		flushLoop(ctx, w.telemetry, defaultFlushInterval,
			"chain_telemetry", flushTelemetryBatch, w.conn, w.metrics)
	}()
	go func() {
		defer wg.Done()
		flushLoop(ctx, w.reconcileAudit, defaultFlushInterval,
			"reconcile_audit", flushReconcileAuditBatch, w.conn, w.metrics)
	}()
	go func() {
		defer wg.Done()
		flushLoop(ctx, w.anomalies, defaultFlushInterval,
			"anomaly_scores", flushAnomalyBatch, w.conn, w.metrics)
	}()
	go func() {
		defer wg.Done()
		flushLoop(ctx, w.finops, defaultFlushInterval,
			"finops_metrics", flushFinOpsBatch, w.conn, w.metrics)
	}()
	go func() {
		defer wg.Done()
		flushLoop(ctx, w.dr, defaultFlushInterval,
			"dr_events", flushDRBatch, w.conn, w.metrics)
	}()

	// Block until the manager cancels the context (graceful shutdown).
	<-ctx.Done()

	// Wait for all goroutines to complete their final flush before returning.
	// This ensures no rows are dropped during operator shutdown.
	wg.Wait()
	return nil
}

// PushTelemetry enqueues a ChainTelemetryRow for async flush.
// Non-blocking: drops the row and increments FlushErrors if the channel is full.
func (w *BatchWriter) PushTelemetry(row ChainTelemetryRow) {
	select {
	case w.telemetry <- row:
	default:
		w.metrics.FlushErrors.WithLabelValues("chain_telemetry").Inc()
	}
}

// PushReconcileAudit enqueues a ReconcileAuditRow for async flush.
func (w *BatchWriter) PushReconcileAudit(row ReconcileAuditRow) {
	select {
	case w.reconcileAudit <- row:
	default:
		w.metrics.FlushErrors.WithLabelValues("reconcile_audit").Inc()
	}
}

// PushAnomaly enqueues an AnomalyRow for async flush.
// Called by the reconciler after EvaluateNode returns scores > 0.
func (w *BatchWriter) PushAnomaly(row AnomalyRow) {
	select {
	case w.anomalies <- row:
	default:
		w.metrics.FlushErrors.WithLabelValues("anomaly_scores").Inc()
	}
}

// PushFinOps enqueues a FinOpsRow for async flush.
// Called by finops_estimator.go each reconcile cycle.
func (w *BatchWriter) PushFinOps(row FinOpsRow) {
	select {
	case w.finops <- row:
	default:
		w.metrics.FlushErrors.WithLabelValues("finops_metrics").Inc()
	}
}

// PushDREvent enqueues a DRRow for async flush.
// Called by DRRunner on backup/restore completion.
func (w *BatchWriter) PushDREvent(row DRRow) {
	select {
	case w.dr <- row:
	default:
		w.metrics.FlushErrors.WithLabelValues("dr_events").Inc()
	}
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
// Returns false: the BatchWriter runs on every replica independently.
func (w *BatchWriter) NeedLeaderElection() bool {
	return false
}
