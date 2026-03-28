// api/v1alpha1/taonode_types.go
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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ══════════════════════════════════════════════════════════════════
// ENUM TYPES — Role and Phase
// ══════════════════════════════════════════════════════════════════

// TaoNodeRole defines the role of the TaoNode in the Bittensor network.
// +kubebuilder:validation:Enum=subtensor;miner;validator
type TaoNodeRole string

const (
	RoleSubtensor TaoNodeRole = "subtensor"
	RoleMiner     TaoNodeRole = "miner"
	RoleValidator TaoNodeRole = "validator"
)

// TaoNodePhase represents the current lifecycle phase of the TaoNode.
// +kubebuilder:validation:Enum=Pending;Syncing;Synced;Degraded;Recovering;Failed
type TaoNodePhase string

const (
	PhasePending    TaoNodePhase = "Pending"
	PhaseSyncing    TaoNodePhase = "Syncing"
	PhaseSynced     TaoNodePhase = "Synced"
	PhaseDegraded   TaoNodePhase = "Degraded"
	PhaseRecovering TaoNodePhase = "Recovering"
	PhaseFailed     TaoNodePhase = "Failed"
)

// ══════════════════════════════════════════════════════════════════
// SPEC SUB-TYPES — v4 R1 (14 core types)
// ══════════════════════════════════════════════════════════════════

// ChainStorageSpec configures the persistent volume for chain data.
type ChainStorageSpec struct {
	// StorageClass is the Kubernetes StorageClass to use for the chain data PVC.
	StorageClass string `json:"storageClass"`

	// Size is the initial requested size for the chain data PVC.
	Size resource.Quantity `json:"size"`

	// AutoExpand configures automatic PVC expansion when disk usage exceeds a threshold.
	// +optional
	AutoExpand *AutoExpandSpec `json:"autoExpand,omitempty"`
}

// AutoExpandSpec configures automatic PVC expansion.
type AutoExpandSpec struct {
	// ThresholdPercent is the disk usage percentage that triggers expansion.
	// +kubebuilder:validation:Minimum=50
	// +kubebuilder:validation:Maximum=95
	ThresholdPercent int32 `json:"thresholdPercent"`

	// MaxSize is the upper bound for automatic expansion.
	MaxSize resource.Quantity `json:"maxSize"`

	// IncrementPercent is how much to grow the PVC on each expansion (percentage of current size).
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=100
	IncrementPercent int32 `json:"incrementPercent"`
}

// SyncPolicySpec defines how the operator detects and recovers from sync failures.
type SyncPolicySpec struct {
	// MaxBlockLag is the maximum tolerated block lag before recovery is triggered.
	// +kubebuilder:validation:Minimum=1
	MaxBlockLag int64 `json:"maxBlockLag"`

	// RecoveryStrategy defines the action to take when MaxBlockLag is exceeded.
	// +kubebuilder:validation:Enum=restart;snapshot-restore;cordon-and-alert
	RecoveryStrategy string `json:"recoveryStrategy"`

	// MaxRestartAttempts is the maximum number of restart attempts before escalation.
	// +kubebuilder:default=3
	MaxRestartAttempts int32 `json:"maxRestartAttempts,omitempty"`

	// ProbeIntervalSeconds is how often the chain health probe is invoked (in seconds).
	// +kubebuilder:default=30
	ProbeIntervalSeconds int32 `json:"probeIntervalSeconds,omitempty"`

	// SyncTimeoutMinutes is the maximum time to wait for re-sync before declaring failure.
	// +kubebuilder:default=120
	SyncTimeoutMinutes int32 `json:"syncTimeoutMinutes,omitempty"`
}

// SnapshotPolicySpec defines the snapshot schedule and retention policy.
type SnapshotPolicySpec struct {
	// Schedule is a cron expression for snapshot timing.
	Schedule string `json:"schedule"`

	// Destination configures where snapshots are stored.
	Destination SnapshotDestination `json:"destination"`

	// RetentionCount is the number of snapshots to retain.
	// +kubebuilder:validation:Minimum=1
	RetentionCount int32 `json:"retentionCount"`

	// OnSyncLoss triggers an emergency snapshot when sync is lost.
	// +kubebuilder:default=true
	OnSyncLoss bool `json:"onSyncLoss,omitempty"`
}

// SnapshotDestination specifies the backend and credentials for snapshot storage.
type SnapshotDestination struct {
	// Type is the storage backend type.
	// +kubebuilder:validation:Enum=s3;gcs;minio;volume-snapshot
	Type string `json:"type"`

	// Bucket is the object-storage bucket name (not used for volume-snapshot type).
	// +optional
	Bucket string `json:"bucket,omitempty"`

	// CredentialsSecret is the name of the Kubernetes Secret containing storage credentials.
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
}

// GPUSpec configures GPU resources for mining/inference workloads.
type GPUSpec struct {
	// Count is the number of GPUs to request.
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`

	// Type is the GPU type label (e.g., "nvidia-a100", "nvidia-t4").
	Type string `json:"type"`

	// SpotTolerant indicates the workload can tolerate spot/preemptible GPU instances.
	// +kubebuilder:default=false
	SpotTolerant bool `json:"spotTolerant,omitempty"`

	// FallbackToCPU allows the workload to fall back to CPU if no GPUs are available.
	// +kubebuilder:default=false
	FallbackToCPU bool `json:"fallbackToCPU,omitempty"`
}

// MonitoringSpec configures Prometheus metrics exposure.
type MonitoringSpec struct {
	// Enabled controls whether the metrics sidecar and ServiceMonitor are created.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Port is the port on which the metrics sidecar listens.
	// +kubebuilder:default=9615
	Port int32 `json:"port,omitempty"`

	// AlertRules is a list of custom Prometheus alerting rules for this node.
	// +optional
	AlertRules []AlertRule `json:"alertRules,omitempty"`
}

// AlertRule defines a single Prometheus alerting rule.
type AlertRule struct {
	// Name is the alert rule name.
	Name string `json:"name"`

	// Expression is the PromQL expression that triggers the alert.
	Expression string `json:"expression"`

	// Duration is how long the condition must be true before alerting (e.g., "5m").
	Duration string `json:"duration"`

	// Severity is the alert severity label (e.g., "critical", "warning").
	Severity string `json:"severity"`
}

// ValidatorSpec configures validator-specific settings and key management.
// This is the v2 canonical version with External Secrets Operator (ESO) support.
type ValidatorSpec struct {
	// HotKeySecret is the name of the Kubernetes Secret containing the validator hot key.
	HotKeySecret string `json:"hotKeySecret"`

	// ColdKeyVaultRef is the vault reference path for the cold key (ESO integration).
	// +optional
	ColdKeyVaultRef string `json:"coldKeyVaultRef,omitempty"`

	// ExternalSecretProvider specifies which external secrets backend to use.
	// +kubebuilder:validation:Enum=none;vault;aws-secrets-manager;gcp-secret-manager
	// +kubebuilder:default="none"
	ExternalSecretProvider string `json:"externalSecretProvider,omitempty"`

	// SlashingProtection enables double-sign protection.
	// +kubebuilder:default=true
	SlashingProtection bool `json:"slashingProtection,omitempty"`

	// MaxConcurrentValidators limits how many validator instances can run simultaneously.
	// +kubebuilder:default=1
	MaxConcurrentValidators int32 `json:"maxConcurrentValidators,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// ANALYTICS SPEC TYPES — v4 R2
// ══════════════════════════════════════════════════════════════════

// AnalyticsSpec configures the ClickHouse analytics plane.
type AnalyticsSpec struct {
	// Enabled controls whether analytics ingestion is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// ClickHouseRef is the connection configuration for the ClickHouse cluster.
	ClickHouseRef ClickHouseRef `json:"clickhouseRef"`

	// Ingestion controls batch sizes, flush intervals, and which event types to ingest.
	Ingestion IngestionSpec `json:"ingestion"`

	// AnomalyDetection enables ML-based anomaly detection queries.
	// +optional
	AnomalyDetection *AnomalyDetectionSpec `json:"anomalyDetection,omitempty"`

	// Retention controls TTL policies for raw and aggregated data.
	Retention RetentionSpec `json:"retention"`
}

// ClickHouseRef holds the connection details for a ClickHouse cluster.
type ClickHouseRef struct {
	// Endpoint is the ClickHouse connection string (e.g., "clickhouse://host:9000").
	Endpoint string `json:"endpoint"`

	// Database is the target ClickHouse database name.
	// +kubebuilder:default="taonode_guardian"
	Database string `json:"database,omitempty"`

	// CredentialsSecret is the name of the Kubernetes Secret with username/password keys.
	CredentialsSecret string `json:"credentialsSecret"`

	// TLS enables TLS for the ClickHouse connection.
	// +kubebuilder:default=false
	TLS bool `json:"tls,omitempty"`

	// Cluster is the ClickHouse cluster name for ON CLUSTER DDL.
	// +optional
	Cluster string `json:"cluster,omitempty"`
}

// IngestionSpec controls what telemetry is ingested and at what rate.
type IngestionSpec struct {
	// BatchSize is the number of rows to accumulate before flushing to ClickHouse.
	// +kubebuilder:validation:Minimum=100
	// +kubebuilder:default=5000
	BatchSize int32 `json:"batchSize,omitempty"`

	// FlushIntervalSeconds is the maximum time between flushes regardless of batch fullness.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=10
	FlushIntervalSeconds int32 `json:"flushIntervalSeconds,omitempty"`

	// ChainEvents enables ingestion of on-chain events (transfers, staking, etc.).
	// +kubebuilder:default=true
	ChainEvents bool `json:"chainEvents,omitempty"`

	// MinerTelemetry enables ingestion of miner performance metrics.
	// +kubebuilder:default=true
	MinerTelemetry bool `json:"minerTelemetry,omitempty"`

	// ReconcileAudit enables ingestion of reconcile loop audit records.
	// +kubebuilder:default=true
	ReconcileAudit bool `json:"reconcileAudit,omitempty"`
}

// AnomalyDetectionSpec configures the thresholds for ClickHouse-based anomaly detection.
type AnomalyDetectionSpec struct {
	// Enabled controls whether anomaly detection queries run during each reconcile.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// EvaluationIntervalSeconds is how often anomaly queries run (independent of probe interval).
	// +kubebuilder:default=60
	EvaluationIntervalSeconds int32 `json:"evaluationIntervalSeconds,omitempty"`

	// SyncDriftThreshold is the block lag delta (blocks/minute) that signals sync drift.
	// +kubebuilder:default=50
	SyncDriftThreshold int64 `json:"syncDriftThreshold,omitempty"`

	// PeerChurnVelocityThreshold is the peer count delta/minute that signals network instability.
	// +kubebuilder:default=5
	PeerChurnVelocityThreshold int32 `json:"peerChurnVelocityThreshold,omitempty"`

	// DiskExhaustionHorizonHours is the projected hours-to-full that triggers a warning.
	// +kubebuilder:default=48
	DiskExhaustionHorizonHours int32 `json:"diskExhaustionHorizonHours,omitempty"`

	// RewardDecayPercent is the percentage drop in rewards (vs 7-day avg) that signals degradation.
	// +kubebuilder:default=30
	RewardDecayPercent int32 `json:"rewardDecayPercent,omitempty"`
}

// RetentionSpec defines TTL policies for ClickHouse data tiers.
type RetentionSpec struct {
	// RawDataDays is the retention period for raw telemetry rows.
	// +kubebuilder:default=30
	RawDataDays int32 `json:"rawDataDays,omitempty"`

	// AggregatedDataDays is the retention period for hourly rollup tables.
	// +kubebuilder:default=365
	AggregatedDataDays int32 `json:"aggregatedDataDays,omitempty"`

	// AuditDataDays is the retention period for reconcile audit records.
	// +kubebuilder:default=90
	AuditDataDays int32 `json:"auditDataDays,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// AI ADVISOR SPEC TYPES — v4 R2
// ══════════════════════════════════════════════════════════════════

// AIAdvisorSpec configures the Ollama-backed AI incident analysis engine.
type AIAdvisorSpec struct {
	// Enabled controls whether AI advisory is active.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// Endpoint is the Ollama API base URL (e.g., "http://ollama:11434").
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Model is the Ollama model tag to use for inference.
	// +kubebuilder:default="llama3.1:8b-instruct-q4_K_M"
	Model string `json:"model,omitempty"`

	// TimeoutSeconds is the maximum time to wait for an Ollama response.
	// +kubebuilder:default=30
	TimeoutSeconds int32 `json:"timeoutSeconds,omitempty"`

	// MinAnomalyScoreForAdvisory is the minimum anomaly score (0.0–1.0) that triggers AI analysis.
	// +kubebuilder:default="0.6"
	MinAnomalyScoreForAdvisory string `json:"minAnomalyScoreForAdvisory,omitempty"`

	// NotificationChannels configures where AI incident reports are sent.
	// +optional
	NotificationChannels *NotificationChannels `json:"notificationChannels,omitempty"`

	// ContextWindowTokens limits the token budget for the incident context JSON.
	// +kubebuilder:default=4096
	ContextWindowTokens int32 `json:"contextWindowTokens,omitempty"`
}

// NotificationChannels configures external alerting integrations.
type NotificationChannels struct {
	// SlackWebhookSecret is the name of the Secret containing the Slack incoming webhook URL.
	// +optional
	SlackWebhookSecret string `json:"slackWebhookSecret,omitempty"`

	// PagerDutyKeySecret is the name of the Secret containing the PagerDuty Events API key.
	// +optional
	PagerDutyKeySecret string `json:"pagerDutyKeySecret,omitempty"`

	// DiscordWebhookSecret is the name of the Secret containing the Discord incoming webhook URL
	// (key: "url"). When set, the AI advisor POSTs a colour-coded Embed to the Discord channel
	// for every IncidentReport whose severity meets MinAnomalyScoreForAdvisory.
	// See config/discord-webhook-example.yaml for the expected Secret shape.
	// +optional
	DiscordWebhookSecret string `json:"discordWebhookSecret,omitempty"`

	// KubernetesEvents controls whether AI reports are also emitted as Kubernetes Events.
	// +kubebuilder:default=true
	KubernetesEvents bool `json:"kubernetesEvents,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// DISASTER RECOVERY SPEC TYPES — v4 R2
// ══════════════════════════════════════════════════════════════════

// DisasterRecoverySpec configures multi-layer backup and recovery.
type DisasterRecoverySpec struct {
	// Enabled controls whether DR backups are active.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// CRDBackup configures periodic CRD state backups to object storage.
	// +optional
	CRDBackup CRDBackupSpec `json:"crdBackup,omitempty"`

	// ChainDataDR configures cross-region replication of chain data snapshots.
	// +optional
	ChainDataDR ChainDataDRSpec `json:"chainDataDR,omitempty"`

	// ClickHouseDR configures backup of ClickHouse analytics data.
	// +optional
	ClickHouseDR *ClickHouseDRSpec `json:"clickhouseDR,omitempty"`

	// RPO is the Recovery Point Objective (maximum tolerable data loss window).
	// +kubebuilder:default="4h"
	RPO string `json:"rpo,omitempty"`

	// RTO is the Recovery Time Objective (maximum tolerable downtime).
	// +kubebuilder:default="30m"
	RTO string `json:"rto,omitempty"`
}

// CRDBackupSpec configures automatic backup of TaoNode CRD manifests.
type CRDBackupSpec struct {
	// Schedule is the cron expression for CRD backup frequency.
	// +kubebuilder:default="0 */2 * * *"
	Schedule string `json:"schedule,omitempty"`

	// Destination specifies where to store the CRD backup archives.
	Destination SnapshotDestination `json:"destination"`
}

// ChainDataDRSpec configures cross-region replication for chain data snapshots.
type ChainDataDRSpec struct {
	// CrossRegionReplication enables replication of snapshots to a secondary region.
	// +kubebuilder:default=false
	CrossRegionReplication bool `json:"crossRegionReplication,omitempty"`

	// SecondaryBucket is the object-storage bucket in the secondary region.
	// +optional
	SecondaryBucket string `json:"secondaryBucket,omitempty"`

	// SecondaryRegion is the target region for cross-region replication.
	// +optional
	SecondaryRegion string `json:"secondaryRegion,omitempty"`
}

// ClickHouseDRSpec configures backup of ClickHouse analytics data.
type ClickHouseDRSpec struct {
	// BackupSchedule is the cron expression for ClickHouse backup frequency.
	// +kubebuilder:default="0 3 * * *"
	BackupSchedule string `json:"backupSchedule,omitempty"`

	// Destination specifies where to store ClickHouse backup archives.
	Destination SnapshotDestination `json:"destination"`

	// RetentionDays is how long to keep ClickHouse backups.
	// +kubebuilder:default=30
	RetentionDays int32 `json:"retentionDays,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// STATUS SUB-TYPES — v4 R1 (core) + v4 R2 (analytics/AI)
// ══════════════════════════════════════════════════════════════════

// SyncStateStatus holds the current blockchain synchronization state.
type SyncStateStatus struct {
	// State is the human-readable sync state (e.g., "syncing", "in-sync").
	State string `json:"state,omitempty"`

	// CurrentBlock is the latest block processed by this node.
	CurrentBlock int64 `json:"currentBlock,omitempty"`

	// NetworkBlock is the latest known block on the network.
	NetworkBlock int64 `json:"networkBlock,omitempty"`

	// BlockLag is the difference between NetworkBlock and CurrentBlock.
	BlockLag int64 `json:"blockLag,omitempty"`

	// SyncPercentage is the human-readable sync completion percentage.
	SyncPercentage string `json:"syncPercentage,omitempty"`

	// LastSyncedAt is the timestamp of the last successful sync event.
	// +optional
	LastSyncedAt *metav1.Time `json:"lastSyncedAt,omitempty"`
}

// ChainStatus holds chain-level runtime information.
type ChainStatus struct {
	// PeerCount is the current number of connected peers.
	PeerCount int32 `json:"peerCount,omitempty"`

	// FinalizedBlock is the latest finalized (irreversible) block number.
	FinalizedBlock int64 `json:"finalizedBlock,omitempty"`

	// RuntimeVersion is the Subtensor runtime spec version string.
	RuntimeVersion string `json:"runtimeVersion,omitempty"`

	// Epoch is the current consensus epoch number.
	Epoch int64 `json:"epoch,omitempty"`
}

// ResourceStatus holds real-time resource utilization metrics.
type ResourceStatus struct {
	// DiskUsagePercent is the percentage of the chain data PVC currently in use.
	DiskUsagePercent int32 `json:"diskUsagePercent,omitempty"`

	// GPUUtilizationPercent is the current GPU utilization (nil if no GPU configured).
	// +optional
	GPUUtilizationPercent *int32 `json:"gpuUtilizationPercent,omitempty"`

	// EstimatedMonthlyUSD is the projected monthly infrastructure cost for this node.
	// +optional
	EstimatedMonthlyUSD string `json:"estimatedMonthlyUSD,omitempty"`
}

// SnapshotStatus tracks the most recent snapshot operation.
type SnapshotStatus struct {
	// LastSnapshotTime is the timestamp of the most recently completed snapshot.
	// +optional
	LastSnapshotTime *metav1.Time `json:"lastSnapshotTime,omitempty"`

	// LastSnapshotSize is the compressed size in bytes of the most recent snapshot.
	LastSnapshotSize int64 `json:"lastSnapshotSize,omitempty"`

	// AvailableSnapshots is the number of snapshots currently retained in storage.
	AvailableSnapshots int32 `json:"availableSnapshots,omitempty"`
}

// AnalyticsStatusBlock reflects the health of the analytics ingestion pipeline.
type AnalyticsStatusBlock struct {
	// Connected indicates whether the ClickHouse connection is healthy.
	Connected bool `json:"connected,omitempty"`

	// LastIngestionTime is the timestamp of the last successful ClickHouse flush.
	// +optional
	LastIngestionTime *metav1.Time `json:"lastIngestionTime,omitempty"`

	// RowsIngested24h is the total number of rows ingested in the last 24 hours.
	RowsIngested24h int64 `json:"rowsIngested24h,omitempty"`

	// AnomalyScores is the list of active anomalies detected in the most recent evaluation.
	// +optional
	AnomalyScores []AnomalyScoreStatus `json:"anomalyScores,omitempty"`

	// IngestionLagMs is the current end-to-end ingestion lag in milliseconds.
	IngestionLagMs int32 `json:"ingestionLagMs,omitempty"`
}

// AnomalyScoreStatus is the status representation of a detected anomaly.
type AnomalyScoreStatus struct {
	// Type identifies the anomaly detector (e.g., "sync_drift", "disk_exhaustion").
	Type string `json:"type"`

	// Score is the anomaly severity as a string-encoded float (0.0=normal, 1.0=critical).
	Score string `json:"score"`

	// Detail is a human-readable description of the detected anomaly.
	Detail string `json:"detail,omitempty"`

	// DetectedAt is when this anomaly was first detected.
	// +optional
	DetectedAt *metav1.Time `json:"detectedAt,omitempty"`
}

// AIAdvisoryStatus reflects the health and output of the AI advisor.
type AIAdvisoryStatus struct {
	// LastReport is the most recent AI-generated incident report.
	// +optional
	LastReport *AIIncidentReportStatus `json:"lastReport,omitempty"`

	// ModelAvailable indicates whether the Ollama model is reachable.
	ModelAvailable bool `json:"modelAvailable,omitempty"`

	// InferencesTotal24h is the count of AI inferences in the last 24 hours.
	InferencesTotal24h int32 `json:"inferencesTotal24h,omitempty"`

	// AvgInferenceLatencyMs is the average inference latency in milliseconds.
	AvgInferenceLatencyMs int32 `json:"avgInferenceLatencyMs,omitempty"`
}

// AIIncidentReportStatus is the status representation of an AI incident analysis.
type AIIncidentReportStatus struct {
	// Timestamp is when the AI analysis was produced.
	// +optional
	Timestamp *metav1.Time `json:"timestamp,omitempty"`

	// Severity is the AI-assessed incident severity (e.g., "critical", "warning").
	Severity string `json:"severity,omitempty"`

	// Summary is the AI-generated 2-3 sentence incident summary.
	Summary string `json:"summary,omitempty"`

	// RootCauseCategory is one of the 8 canonical root cause categories.
	RootCauseCategory string `json:"rootCauseCategory,omitempty"`

	// RecommendedAction is the AI's top recommended remediation action.
	RecommendedAction string `json:"recommendedAction,omitempty"`

	// Confidence is the AI's confidence score as a string-encoded float (0.0–1.0).
	Confidence string `json:"confidence,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// SPEC — user declares desired state (v3 Section 3, canonical)
// ══════════════════════════════════════════════════════════════════

// TaoNodeSpec defines the desired state of TaoNode.
type TaoNodeSpec struct {
	// Network is the Bittensor network this node participates in.
	// +kubebuilder:validation:Enum=mainnet;testnet;devnet
	Network string `json:"network"`

	// SubnetID is the Bittensor subnet identifier (0 for subtensor, 1-256 for subnets).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=256
	SubnetID int32 `json:"subnetID"`

	// Role defines this node's role in the network.
	// +kubebuilder:validation:Enum=subtensor;miner;validator
	Role TaoNodeRole `json:"role"`

	// Image overrides the container image. If empty, DefaultImage from the operator is used.
	// +optional
	Image string `json:"image,omitempty"`

	// Version is the Subtensor software version tag (used for display and annotations).
	// +optional
	Version string `json:"version,omitempty"`

	// ChainStorage configures the persistent volume for chain data.
	ChainStorage ChainStorageSpec `json:"chainStorage"`

	// SyncPolicy defines sync monitoring and recovery behavior.
	SyncPolicy SyncPolicySpec `json:"syncPolicy"`

	// SnapshotPolicy configures scheduled chain data snapshots.
	// +optional
	SnapshotPolicy *SnapshotPolicySpec `json:"snapshotPolicy,omitempty"`

	// GPU configures GPU resource requests (for miner inference workloads).
	// +optional
	GPU *GPUSpec `json:"gpu,omitempty"`

	// Validator contains validator-specific configuration (only meaningful when Role=validator).
	// +optional
	Validator *ValidatorSpec `json:"validator,omitempty"`

	// Resources defines CPU/memory resource requests and limits for the node container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Monitoring configures Prometheus metrics exposure and alerting rules.
	// +optional
	Monitoring *MonitoringSpec `json:"monitoring,omitempty"`

	// Analytics configures the ClickHouse analytics ingestion plane.
	// +optional
	Analytics *AnalyticsSpec `json:"analytics,omitempty"`

	// AIAdvisor configures the Ollama-backed AI incident analysis engine.
	// +optional
	AIAdvisor *AIAdvisorSpec `json:"aiAdvisor,omitempty"`

	// DisasterRecovery configures multi-layer backup and recovery.
	// +optional
	DisasterRecovery *DisasterRecoverySpec `json:"disasterRecovery,omitempty"`

	// Tolerations are applied to the TaoNode StatefulSet pods.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// NodeSelector constrains which Kubernetes nodes the TaoNode pods can run on.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// STATUS — operator writes observed state (v3 Section 4, canonical)
// ══════════════════════════════════════════════════════════════════

// TaoNodeStatus defines the observed state of TaoNode.
type TaoNodeStatus struct {
	// Phase is the high-level lifecycle state of this TaoNode.
	// +kubebuilder:validation:Enum=Pending;Syncing;Synced;Degraded;Recovering;Failed
	Phase TaoNodePhase `json:"phase,omitempty"`

	// SyncState holds detailed blockchain synchronization metrics.
	SyncState SyncStateStatus `json:"syncState,omitempty"`

	// Chain holds chain-level runtime information.
	Chain ChainStatus `json:"chain,omitempty"`

	// Resources holds current resource utilization metrics.
	Resources ResourceStatus `json:"resources,omitempty"`

	// Snapshot holds the status of the most recent snapshot operation.
	// +optional
	Snapshot *SnapshotStatus `json:"snapshot,omitempty"`

	// Analytics reflects the health of the analytics ingestion pipeline.
	// +optional
	Analytics *AnalyticsStatusBlock `json:"analytics,omitempty"`

	// AIAdvisory reflects the health and output of the AI advisor.
	// +optional
	AIAdvisory *AIAdvisoryStatus `json:"aiAdvisory,omitempty"`

	// Conditions is the standard Kubernetes condition array for this resource.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// RestartCount is the total number of recovery restarts performed by the operator.
	RestartCount int32 `json:"restartCount,omitempty"`

	// ObservedGeneration is the .metadata.generation this status was computed from.
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastReconcileTime is the timestamp of the most recent reconcile loop execution.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// ══════════════════════════════════════════════════════════════════
// ROOT TYPES — TaoNode + TaoNodeList (v5 H1, canonical)
// ══════════════════════════════════════════════════════════════════

// TaoNode is the Schema for the taonodes API.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=tn
// +kubebuilder:printcolumn:name="Network",type=string,JSONPath=`.spec.network`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="Subnet",type=integer,JSONPath=`.spec.subnetID`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Sync",type=string,JSONPath=`.status.syncState.state`
// +kubebuilder:printcolumn:name="Block",type=integer,JSONPath=`.status.syncState.currentBlock`
// +kubebuilder:printcolumn:name="Lag",type=integer,JSONPath=`.status.syncState.blockLag`
// +kubebuilder:printcolumn:name="Peers",type=integer,JSONPath=`.status.chain.peerCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type TaoNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TaoNodeSpec   `json:"spec,omitempty"`
	Status TaoNodeStatus `json:"status,omitempty"`
}

// TaoNodeList contains a list of TaoNode.
//
// +kubebuilder:object:root=true
type TaoNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TaoNode `json:"items"`
}
