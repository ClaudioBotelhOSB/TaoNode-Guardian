// internal/analytics/types.go
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

import "time"

// AnomalyScore is returned by AnomalyDetector.EvaluateNode.
// Score is float32 to align with the controller's type assertions and
// the ai.IncidentContext.AnomalyScores slice type.
type AnomalyScore struct {
	Type       string    // e.g. "block-lag", "peer-count", "disk-usage"
	Score      float32   // 0.0–1.0; higher = more anomalous
	Detail     string    // human-readable context for alerting / LLM input
	DetectedAt time.Time // set by EvaluateNode before returning
}

// ChainTelemetryRow is a ClickHouse row for per-reconcile chain health telemetry.
// Field names and types must remain in sync with the DDL in schema.go.
type ChainTelemetryRow struct {
	Timestamp             time.Time `ch:"timestamp"`
	Namespace             string    `ch:"namespace"`
	NodeName              string    `ch:"node_name"`
	Network               string    `ch:"network"`
	SubnetID              uint16    `ch:"subnet_id"`
	Role                  string    `ch:"role"`
	CurrentBlock          uint64    `ch:"current_block"`
	NetworkBlock          uint64    `ch:"network_block"`
	BlockLag              int64     `ch:"block_lag"`
	FinalizedBlock        uint64    `ch:"finalized_block"`
	PeerCount             uint16    `ch:"peer_count"`
	RuntimeVersion        string    `ch:"runtime_version"`
	SyncState             string    `ch:"sync_state"`
	BlocksPerSecond       float32   `ch:"blocks_per_second"`
	ProbeLatencyMs        uint32    `ch:"probe_latency_ms"`
	ProbeSuccess          uint8     `ch:"probe_success"`
	DiskUsagePercent      uint8     `ch:"disk_usage_percent"`
	DiskUsedBytes         uint64    `ch:"disk_used_bytes"`
	DiskTotalBytes        uint64    `ch:"disk_total_bytes"`
	GPUUtilizationPercent uint8     `ch:"gpu_utilization_percent"`
	GPUMemoryUsedBytes    uint64    `ch:"gpu_memory_used_bytes"`
	GPUTemperatureCelsius uint16    `ch:"gpu_temperature_celsius"`
}

// ReconcileAuditRow is a ClickHouse row that records what triggered a reconcile
// and the node's state at the start of that reconcile cycle.
type ReconcileAuditRow struct {
	Timestamp       time.Time `ch:"timestamp"`
	Namespace       string    `ch:"namespace"`
	NodeName        string    `ch:"node_name"`
	TriggerType     string    `ch:"trigger_type"` // "periodic", "watch", "forced"
	Generation      uint64    `ch:"generation"`
	ResourceVersion string    `ch:"resource_version"`
	PhaseBefore     string    `ch:"phase_before"`
	SyncStateBefore string    `ch:"sync_state_before"`
	BlockLagBefore  int64     `ch:"block_lag_before"`
}

// AnomalyRow is a ClickHouse row for recording a detected anomaly event.
type AnomalyRow struct {
	Timestamp   time.Time `ch:"timestamp"`
	Namespace   string    `ch:"namespace"`
	NodeName    string    `ch:"node_name"`
	Network     string    `ch:"network"`
	SubnetID    uint16    `ch:"subnet_id"`
	AnomalyType string    `ch:"anomaly_type"`
	Score       float32   `ch:"score"`
	Detail      string    `ch:"detail"`
}

// FinOpsRow is a ClickHouse row for per-reconcile financial operations metrics.
type FinOpsRow struct {
	Timestamp               time.Time `ch:"timestamp"`
	Namespace               string    `ch:"namespace"`
	NodeName                string    `ch:"node_name"`
	Network                 string    `ch:"network"`
	Role                    string    `ch:"role"`
	EstimatedMonthlyCostUSD float64   `ch:"estimated_monthly_cost_usd"`
	CPUCores                float64   `ch:"cpu_cores"`
	MemoryGiB               float64   `ch:"memory_gib"`
	StorageGiB              float64   `ch:"storage_gib"`
	GPUCount                int32     `ch:"gpu_count"`
	IsSpot                  bool      `ch:"is_spot"`
	TaoPerGPUHour           float64   `ch:"tao_per_gpu_hour"`
	ROIPercent              float64   `ch:"roi_percent"`
}

// DRRow is a ClickHouse row for disaster recovery events emitted by DRRunner.
type DRRow struct {
	Timestamp       time.Time `ch:"timestamp"`
	Namespace       string    `ch:"namespace"`
	NodeName        string    `ch:"node_name"`
	EventType       string    `ch:"event_type"` // "backup", "restore", "test"
	BackupSizeBytes int64     `ch:"backup_size_bytes"`
	DurationSeconds float64   `ch:"duration_seconds"`
	Status          string    `ch:"status"` // "success", "failed"
	Detail          string    `ch:"detail"`
}
