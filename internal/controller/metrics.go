// internal/controller/metrics.go
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
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// All metrics are package-level singletons, registered once in init().
// The reconciler references them directly by name.
var (
	// ── Sync & Chain ────────────────────────────────────────────────
	blockLag = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_block_lag",
		Help: "Current block lag between node and network tip.",
	}, []string{"node", "namespace", "network"})

	syncState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_sync_state",
		Help: "Sync state encoded as float: 0=syncing, 1=in-sync.",
	}, []string{"node", "namespace", "network"})

	peerCount = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_peer_count",
		Help: "Number of connected P2P peers.",
	}, []string{"node", "namespace", "network"})

	// ── Reconcile ───────────────────────────────────────────────────
	reconcileDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "taonode_reconcile_duration_seconds",
		Help:    "Duration of a single reconcile loop execution.",
		Buckets: prometheus.DefBuckets,
	}, []string{"node", "namespace", "result"})

	// ── Recovery ────────────────────────────────────────────────────
	recoveryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "taonode_recovery_total",
		Help: "Total number of recovery actions triggered.",
	}, []string{"node", "namespace", "strategy"})

	// ── Snapshots ───────────────────────────────────────────────────
	snapshotDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "taonode_snapshot_duration_seconds",
		Help:    "Duration of snapshot operations.",
		Buckets: prometheus.ExponentialBuckets(1, 2, 10),
	}, []string{"node", "namespace", "type"})

	// ── Storage ─────────────────────────────────────────────────────
	diskUsagePercent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_disk_usage_percent",
		Help: "Chain data PVC utilization as a percentage.",
	}, []string{"node", "namespace"})

	// ── FinOps ──────────────────────────────────────────────────────
	estimatedMonthlyCost = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_estimated_monthly_cost_usd",
		Help: "Estimated monthly infrastructure cost in USD.",
	}, []string{"node", "namespace", "network"})

	taoPerGPUHour = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_tao_per_gpu_hour",
		Help: "TAO emissions earned per GPU-hour (miner ROI).",
	}, []string{"node", "namespace", "subnet_id"})

	roiPercent = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_roi_percent",
		Help: "Return on investment as a percentage (TAO value / infra cost).",
	}, []string{"node", "namespace", "subnet_id"})

	// ── GPU ─────────────────────────────────────────────────────────
	gpuUtilization = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_gpu_utilization_percent",
		Help: "GPU utilization percentage reported by the chain-probe sidecar.",
	}, []string{"node", "namespace"})

	// ── Anomaly Detection ───────────────────────────────────────────
	anomalyScore = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "taonode_anomaly_score",
		Help: "Anomaly score per detector type (0.0=normal, 1.0=critical).",
	}, []string{"node", "namespace", "type"})

	anomalyEvaluationDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "taonode_anomaly_evaluation_duration_seconds",
		Help:    "Time spent evaluating anomaly queries against ClickHouse.",
		Buckets: prometheus.DefBuckets,
	}, []string{"node", "namespace"})

	predictiveActionsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "taonode_predictive_actions_total",
		Help: "Actions taken proactively based on anomaly predictions.",
	}, []string{"node", "namespace", "action"})

	// ── Analytics Pipeline ──────────────────────────────────────────
	analyticsGracefulDegradations = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "taonode_analytics_graceful_degradation_total",
		Help: "Times the analytics plane was skipped due to circuit-breaker or config.",
	})
)

func init() {
	// controller-runtime exposes metrics.Registry which is a prometheus.Registerer.
	// Registering here means all metrics appear on the /metrics endpoint automatically.
	metrics.Registry.MustRegister(
		blockLag,
		syncState,
		peerCount,
		reconcileDuration,
		recoveryTotal,
		snapshotDuration,
		diskUsagePercent,
		estimatedMonthlyCost,
		gpuUtilization,
		anomalyScore,
		anomalyEvaluationDuration,
		predictiveActionsTotal,
		taoPerGPUHour,
		roiPercent,
		analyticsGracefulDegradations,
	)
}
