// internal/analytics/metrics.go
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
	"github.com/prometheus/client_golang/prometheus"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// WriterMetrics holds Prometheus metrics for the BatchWriter flush pipelines.
// All fields are GaugeVec / CounterVec / HistogramVec with a "table" label.
type WriterMetrics struct {
	// RowsWritten tracks the total number of rows successfully sent per table.
	RowsWritten *prometheus.CounterVec

	// FlushDuration observes how long each batch flush takes, per table.
	FlushDuration *prometheus.HistogramVec

	// FlushErrors counts flush failures per table (dropped batches).
	FlushErrors *prometheus.CounterVec

	// ChannelBacklog tracks the instantaneous number of rows waiting in the
	// in-memory channel at the point a new row is enqueued.
	ChannelBacklog *prometheus.GaugeVec
}

// NewWriterMetrics allocates a new WriterMetrics with all descriptors initialised
// but NOT yet registered. Call DefaultWriterMetrics() for the package singleton.
func NewWriterMetrics() *WriterMetrics {
	return &WriterMetrics{
		RowsWritten: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "taonode_analytics_rows_written_total",
				Help: "Total rows successfully written to ClickHouse, by table.",
			},
			[]string{"table"},
		),
		FlushDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "taonode_analytics_flush_duration_seconds",
				Help:    "Duration of ClickHouse batch flush operations, by table.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"table"},
		),
		FlushErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "taonode_analytics_flush_errors_total",
				Help: "Total ClickHouse flush failures or dropped rows, by table.",
			},
			[]string{"table"},
		),
		ChannelBacklog: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "taonode_analytics_channel_backlog",
				Help: "Instantaneous number of rows pending in the flush channel, by table.",
			},
			[]string{"table"},
		),
	}
}

// defaultWriterMetrics is the package-level singleton (v4 R12).
// NewWriterMetrics() is used here, NOT in init(), so the descriptors exist
// before init() attempts registration.
var defaultWriterMetrics = NewWriterMetrics()

// DefaultWriterMetrics returns the package-level WriterMetrics singleton.
// BatchWriter always uses this — never creates its own instance (v5 I1).
func DefaultWriterMetrics() *WriterMetrics {
	return defaultWriterMetrics
}

func init() {
	// Register all analytics metrics with the controller-runtime shared registry.
	ctrlmetrics.Registry.MustRegister(
		defaultWriterMetrics.RowsWritten,
		defaultWriterMetrics.FlushDuration,
		defaultWriterMetrics.FlushErrors,
		defaultWriterMetrics.ChannelBacklog,
	)
}
