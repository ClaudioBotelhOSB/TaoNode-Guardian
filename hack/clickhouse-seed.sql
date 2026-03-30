-- =============================================================================
-- TaoNode Guardian — ClickHouse Seed Data
-- =============================================================================
-- Run this in ClickHouse Play UI (http://<IP>:8123/play) or via:
--   cat hack/clickhouse-seed.sql | kubectl exec -i -n clickhouse chi-clickhouse-taonode-taonode-0-0-0 -- clickhouse-client -mn
--
-- Creates the taonode_guardian database, all 6 operator tables, and inserts
-- ~48h of realistic demo data for 2 nodes (validator + miner).
-- =============================================================================

-- ── Database ─────────────────────────────────────────────────────────────────
CREATE DATABASE IF NOT EXISTS taonode_guardian;

-- ── Tables (from internal/analytics/schema.go) ──────────────────────────────

CREATE TABLE IF NOT EXISTS taonode_guardian.chain_telemetry (
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
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS taonode_guardian.reconcile_audit (
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
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS taonode_guardian.anomaly_scores (
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
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS taonode_guardian.finops_metrics (
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
SETTINGS index_granularity = 8192;

CREATE TABLE IF NOT EXISTS taonode_guardian.dr_events (
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
SETTINGS index_granularity = 8192;

-- ── Seed: chain_telemetry (48h, every 30s, 2 nodes = ~11520 rows) ───────────
-- Validator node: mainnet, subnet 1, synced, ~25 peers
INSERT INTO taonode_guardian.chain_telemetry
SELECT
    now() - toIntervalSecond(number * 30) AS timestamp,
    'taonode-guardian-system' AS namespace,
    'validator-mainnet-01' AS node_name,
    'mainnet' AS network,
    1 AS subnet_id,
    'validator' AS role,
    4200000 + intDiv(number, 2) AS current_block,
    4200000 + intDiv(number, 2) + toUInt64(if(rand() % 100 < 85, 0, rand() % 3)) AS network_block,
    toInt64(network_block) - toInt64(current_block) AS block_lag,
    current_block - 12 AS finalized_block,
    toUInt16(20 + rand() % 12) AS peer_count,
    '1.0.0-tao' AS runtime_version,
    if(rand() % 100 < 95, 'synced', 'catching-up') AS sync_state,
    toFloat32(1.8 + (rand() % 100) / 200.0) AS blocks_per_second,
    toUInt32(45 + rand() % 60) AS probe_latency_ms,
    toUInt8(if(rand() % 100 < 98, 1, 0)) AS probe_success,
    toUInt8(32 + intDiv(number, 200)) AS disk_usage_percent,
    toUInt64((32 + intDiv(number, 200)) * 214748364) AS disk_used_bytes,
    toUInt64(20 * 1073741824) AS disk_total_bytes,
    0 AS gpu_utilization_percent,
    0 AS gpu_memory_used_bytes,
    0 AS gpu_temperature_celsius
FROM numbers(5760);

-- Miner node: testnet, subnet 18, GPU active
INSERT INTO taonode_guardian.chain_telemetry
SELECT
    now() - toIntervalSecond(number * 30) AS timestamp,
    'taonode-guardian-system' AS namespace,
    'miner-testnet-01' AS node_name,
    'testnet' AS network,
    18 AS subnet_id,
    'miner' AS role,
    1850000 + intDiv(number, 2) AS current_block,
    1850000 + intDiv(number, 2) + toUInt64(if(rand() % 100 < 80, 0, rand() % 5)) AS network_block,
    toInt64(network_block) - toInt64(current_block) AS block_lag,
    current_block - 12 AS finalized_block,
    toUInt16(15 + rand() % 8) AS peer_count,
    '1.0.0-tao' AS runtime_version,
    if(rand() % 100 < 90, 'synced', 'catching-up') AS sync_state,
    toFloat32(1.5 + (rand() % 100) / 250.0) AS blocks_per_second,
    toUInt32(55 + rand() % 80) AS probe_latency_ms,
    toUInt8(if(rand() % 100 < 96, 1, 0)) AS probe_success,
    toUInt8(45 + intDiv(number, 300)) AS disk_usage_percent,
    toUInt64((45 + intDiv(number, 300)) * 214748364) AS disk_used_bytes,
    toUInt64(20 * 1073741824) AS disk_total_bytes,
    toUInt8(60 + rand() % 35) AS gpu_utilization_percent,
    toUInt64((6 + rand() % 4) * 1073741824) AS gpu_memory_used_bytes,
    toUInt16(55 + rand() % 25) AS gpu_temperature_celsius
FROM numbers(5760);

-- ── Seed: reconcile_audit (48h, every 30s, 2 nodes) ────────────────────────
INSERT INTO taonode_guardian.reconcile_audit
SELECT
    now() - toIntervalSecond(number * 30) AS timestamp,
    'taonode-guardian-system' AS namespace,
    arrayJoin(['validator-mainnet-01', 'miner-testnet-01']) AS node_name,
    if(rand() % 100 < 85, 'periodic', if(rand() % 100 < 50, 'watch', 'forced')) AS trigger_type,
    42 + intDiv(number, 100) AS generation,
    toString(1000 + number) AS resource_version,
    if(rand() % 100 < 90, 'Running', if(rand() % 100 < 50, 'Recovering', 'Degraded')) AS phase_before,
    if(rand() % 100 < 93, 'synced', 'catching-up') AS sync_state_before,
    toInt64(if(rand() % 100 < 85, 0, rand() % 8)) AS block_lag_before
FROM numbers(5760);

-- ── Seed: anomaly_scores (sporadic events, ~200 over 48h) ──────────────────
INSERT INTO taonode_guardian.anomaly_scores
SELECT
    now() - toIntervalSecond(number * 900 + rand() % 300) AS timestamp,
    'taonode-guardian-system' AS namespace,
    if(rand() % 2 = 0, 'validator-mainnet-01', 'miner-testnet-01') AS node_name,
    if(node_name = 'validator-mainnet-01', 'mainnet', 'testnet') AS network,
    if(node_name = 'validator-mainnet-01', 1, 18) AS subnet_id,
    arrayElement(
        ['block-lag', 'peer-churn', 'disk-exhaustion', 'recovery-freq', 'probe-latency'],
        1 + rand() % 5
    ) AS anomaly_type,
    toFloat32(0.15 + (rand() % 70) / 100.0) AS score,
    multiIf(
        anomaly_type = 'block-lag', concat('Block lag trending up: ', toString(2 + rand() % 10), ' blocks behind'),
        anomaly_type = 'peer-churn', concat('Peer churn velocity: ', toString(1 + rand() % 6), ' peers/min'),
        anomaly_type = 'disk-exhaustion', concat('Disk full in ~', toString(24 + rand() % 72), ' hours at current rate'),
        anomaly_type = 'recovery-freq', concat(toString(2 + rand() % 4), ' recovery events in last hour'),
        concat('Probe p99 latency: ', toString(80 + rand() % 120), 'ms')
    ) AS detail
FROM numbers(200);

-- ── Seed: finops_metrics (48h, every 30s, 2 nodes) ─────────────────────────
-- Validator: no GPU, ~$145/mo on-demand
INSERT INTO taonode_guardian.finops_metrics
SELECT
    now() - toIntervalSecond(number * 30) AS timestamp,
    'taonode-guardian-system' AS namespace,
    'validator-mainnet-01' AS node_name,
    'mainnet' AS network,
    'validator' AS role,
    144.50 + (rand() % 1000) / 100.0 AS estimated_monthly_cost_usd,
    4.0 AS cpu_cores,
    16.0 AS memory_gib,
    20.0 AS storage_gib,
    0 AS gpu_count,
    false AS is_spot,
    0.0 AS tao_per_gpu_hour,
    0.0 AS roi_percent
FROM numbers(5760);

-- Miner: 1 GPU, spot, ~$850/mo, ~180% ROI
INSERT INTO taonode_guardian.finops_metrics
SELECT
    now() - toIntervalSecond(number * 30) AS timestamp,
    'taonode-guardian-system' AS namespace,
    'miner-testnet-01' AS node_name,
    'testnet' AS network,
    'miner' AS role,
    845.0 + (rand() % 5000) / 100.0 AS estimated_monthly_cost_usd,
    8.0 AS cpu_cores,
    32.0 AS memory_gib,
    50.0 AS storage_gib,
    1 AS gpu_count,
    true AS is_spot,
    toFloat64(0.08 + (rand() % 40) / 1000.0) AS tao_per_gpu_hour,
    toFloat64(160.0 + (rand() % 6000) / 100.0) AS roi_percent
FROM numbers(5760);

-- ── Seed: dr_events (1 backup per 6h = 8 events over 48h, per node) ────────
INSERT INTO taonode_guardian.dr_events
SELECT
    now() - toIntervalSecond(number * 21600) AS timestamp,
    'taonode-guardian-system' AS namespace,
    arrayJoin(['validator-mainnet-01', 'miner-testnet-01']) AS node_name,
    'backup' AS event_type,
    toInt64(2048000 + rand() % 512000) AS backup_size_bytes,
    toFloat64(12.5 + (rand() % 100) / 10.0) AS duration_seconds,
    if(rand() % 100 < 94, 'success', 'failed') AS status,
    if(status = 'success',
        concat('Compressed ', toString(intDiv(backup_size_bytes, 1024)), ' KiB to object storage'),
        'Timeout waiting for object storage upload')
    AS detail
FROM numbers(8);

-- ── Verification ─────────────────────────────────────────────────────────────
SELECT 'chain_telemetry' AS table, count() AS rows FROM taonode_guardian.chain_telemetry
UNION ALL
SELECT 'reconcile_audit', count() FROM taonode_guardian.reconcile_audit
UNION ALL
SELECT 'anomaly_scores', count() FROM taonode_guardian.anomaly_scores
UNION ALL
SELECT 'finops_metrics', count() FROM taonode_guardian.finops_metrics
UNION ALL
SELECT 'dr_events', count() FROM taonode_guardian.dr_events;
