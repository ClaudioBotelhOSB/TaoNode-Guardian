// internal/analytics/schema.go
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

	driver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SchemaVersion is stamped into log messages so ops teams can correlate DDL changes.
const SchemaVersion = "v001"

// tableSchemas holds the CREATE TABLE DDL for all analytics tables.
// All tables use MergeTree with TTL-based retention.
// Column names MUST match the `ch:` struct tags in types.go.
var tableSchemas = map[string]string{
	"chain_telemetry": `
CREATE TABLE IF NOT EXISTS chain_telemetry (
    timestamp               DateTime64(3, 'UTC'),
    namespace               LowCardinality(String),
    node_name               LowCardinality(String),
    network                 LowCardinality(String),
    subnet_id               UInt16,
    role                    LowCardinality(String),
    current_block           UInt64,
    network_block           UInt64,
    block_lag               Int64,
    finalized_block         UInt64,
    peer_count              UInt16,
    runtime_version         LowCardinality(String),
    sync_state              LowCardinality(String),
    blocks_per_second       Float32,
    probe_latency_ms        UInt32,
    probe_success           UInt8,
    disk_usage_percent      UInt8,
    disk_used_bytes         UInt64,
    disk_total_bytes        UInt64,
    gpu_utilization_percent UInt8,
    gpu_memory_used_bytes   UInt64,
    gpu_temperature_celsius UInt16
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (namespace, node_name, timestamp)
TTL timestamp + INTERVAL 30 DAY
SETTINGS index_granularity = 8192`,

	"reconcile_audit": `
CREATE TABLE IF NOT EXISTS reconcile_audit (
    timestamp        DateTime64(3, 'UTC'),
    namespace        LowCardinality(String),
    node_name        LowCardinality(String),
    trigger_type     LowCardinality(String),
    generation       UInt64,
    resource_version String,
    phase_before     LowCardinality(String),
    sync_state_before LowCardinality(String),
    block_lag_before Int64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (namespace, node_name, timestamp)
TTL timestamp + INTERVAL 90 DAY
SETTINGS index_granularity = 8192`,

	"anomaly_scores": `
CREATE TABLE IF NOT EXISTS anomaly_scores (
    timestamp    DateTime64(3, 'UTC'),
    namespace    LowCardinality(String),
    node_name    LowCardinality(String),
    network      LowCardinality(String),
    subnet_id    UInt16,
    anomaly_type LowCardinality(String),
    score        Float32,
    detail       String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (namespace, node_name, anomaly_type, timestamp)
TTL timestamp + INTERVAL 30 DAY
SETTINGS index_granularity = 8192`,

	"finops_metrics": `
CREATE TABLE IF NOT EXISTS finops_metrics (
    timestamp                  DateTime64(3, 'UTC'),
    namespace                  LowCardinality(String),
    node_name                  LowCardinality(String),
    network                    LowCardinality(String),
    role                       LowCardinality(String),
    estimated_monthly_cost_usd Float64,
    cpu_cores                  Float64,
    memory_gib                 Float64,
    storage_gib                Float64,
    gpu_count                  Int32,
    is_spot                    Bool,
    tao_per_gpu_hour           Float64,
    roi_percent                Float64
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (namespace, node_name, timestamp)
TTL timestamp + INTERVAL 365 DAY
SETTINGS index_granularity = 8192`,

	"dr_events": `
CREATE TABLE IF NOT EXISTS dr_events (
    timestamp        DateTime64(3, 'UTC'),
    namespace        LowCardinality(String),
    node_name        LowCardinality(String),
    event_type       LowCardinality(String),
    backup_size_bytes Int64,
    duration_seconds Float64,
    status           LowCardinality(String),
    detail           String
) ENGINE = MergeTree()
PARTITION BY toYYYYMM(timestamp)
ORDER BY (namespace, node_name, timestamp)
TTL timestamp + INTERVAL 365 DAY
SETTINGS index_granularity = 8192`,
}

// ApplySchema creates all analytics tables if they do not already exist.
// It is idempotent — safe to call on every operator startup.
// Tables use IF NOT EXISTS so re-runs are a no-op unless a new table is added.
func ApplySchema(ctx context.Context, conn driver.Conn) error {
	log := logf.FromContext(ctx)

	for table, ddl := range tableSchemas {
		log.Info("Applying ClickHouse DDL",
			"table", table,
			"schemaVersion", SchemaVersion,
		)
		if err := conn.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("apply schema for table %q (version %s): %w",
				table, SchemaVersion, err)
		}
		log.Info("DDL applied", "table", table)
	}
	return nil
}
