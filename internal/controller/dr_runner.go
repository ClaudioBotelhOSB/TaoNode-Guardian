// internal/controller/dr_runner.go
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
	"bytes"
	gziplib "compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// DRRunner implements manager.Runnable — it is NOT a Reconciler.
// It runs as a periodic background task registered with mgr.Add()
// and backed by leader-election (only the leader runs DR backups).
//
// Responsibilities:
//  1. List all TaoNode CRs in the cluster
//  2. Serialize them to JSON (stripping server-side metadata)
//  3. gzip compress the payload
//  4. Upload to object storage with a timestamped key
type DRRunner struct {
	Client      client.Client
	ObjectStore ObjectStoreClient
	ClusterID   string
	Interval    time.Duration
	Metrics     *DRMetrics
}

// DRMetrics tracks disaster recovery backup operations.
type DRMetrics struct {
	BackupSuccess  prometheus.Counter
	BackupFailures prometheus.Counter
	LastBackupTime prometheus.Gauge
}

// DRBackup is the JSON payload written to object storage.
type DRBackup struct {
	Timestamp     time.Time             `json:"timestamp"`
	ClusterID     string                `json:"clusterID"`
	SchemaVersion string                `json:"schemaVersion"`
	Nodes         []taov1alpha1.TaoNode `json:"nodes"`
}

// ObjectStoreClient is the interface for S3/GCS/MinIO upload operations.
// Concrete implementations live in cmd/main.go or a dedicated pkg/objectstore package.
type ObjectStoreClient interface {
	Upload(ctx context.Context, key string, data []byte) error
}

var (
	drBackupSuccessMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "taonode_dr_backup_success_total",
		Help: "Total successful DR backup uploads.",
	})
	drBackupFailureMetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "taonode_dr_backup_failures_total",
		Help: "Total failed DR backup uploads.",
	})
	drLastBackupTimeMetric = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "taonode_dr_last_backup_timestamp_seconds",
		Help: "Unix timestamp of the most recent successful DR backup.",
	})
)

func init() {
	ctrlmetrics.Registry.MustRegister(
		drBackupSuccessMetric,
		drBackupFailureMetric,
		drLastBackupTimeMetric,
	)
}

// NewDRMetrics returns the registered singleton DR metrics.
func NewDRMetrics() *DRMetrics {
	return &DRMetrics{
		BackupSuccess:  drBackupSuccessMetric,
		BackupFailures: drBackupFailureMetric,
		LastBackupTime: drLastBackupTimeMetric,
	}
}

// Start implements manager.Runnable.
// Called by the manager after leader election is acquired.
// Runs an immediate backup on startup, then on the configured Interval.
func (dr *DRRunner) Start(ctx context.Context) error {
	log := logf.FromContext(ctx)
	log.Info("DR runner started", "clusterID", dr.ClusterID, "interval", dr.Interval)

	ticker := time.NewTicker(dr.Interval)
	defer ticker.Stop()

	// Run immediately on startup before waiting for the first tick.
	if err := dr.runBackup(ctx); err != nil {
		log.Error(err, "Initial DR backup failed")
		dr.Metrics.BackupFailures.Inc()
	}

	for {
		select {
		case <-ticker.C:
			if err := dr.runBackup(ctx); err != nil {
				log.Error(err, "Periodic DR backup failed")
				dr.Metrics.BackupFailures.Inc()
			}
		case <-ctx.Done():
			log.Info("DR runner stopping", "reason", ctx.Err())
			return nil
		}
	}
}

// NeedLeaderElection ensures only the elected leader runs periodic DR backups.
func (dr *DRRunner) NeedLeaderElection() bool {
	return true
}

// runBackup executes a single DR backup cycle:
//  1. List all TaoNode CRs
//  2. Strip server-side-only fields
//  3. Marshal to JSON
//  4. gzip compress
//  5. Upload to object storage
func (dr *DRRunner) runBackup(ctx context.Context) error {
	log := logf.FromContext(ctx)

	var nodeList taov1alpha1.TaoNodeList
	if err := dr.Client.List(ctx, &nodeList); err != nil {
		return fmt.Errorf("list TaoNodes: %w", err)
	}

	backup := DRBackup{
		Timestamp:     time.Now().UTC(),
		ClusterID:     dr.ClusterID,
		SchemaVersion: "v1alpha1",
		Nodes:         make([]taov1alpha1.TaoNode, len(nodeList.Items)),
	}

	for i, node := range nodeList.Items {
		// Strip server-managed fields that should not be restored verbatim.
		node.ResourceVersion = ""
		node.UID = ""
		node.ManagedFields = nil
		node.Finalizers = nil
		node.DeletionTimestamp = nil
		backup.Nodes[i] = node
	}

	data, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshal DR backup: %w", err)
	}

	// gzip compress the JSON payload.
	var buf bytes.Buffer
	gz := gziplib.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}

	key := fmt.Sprintf("dr-backups/%s/%s.json.gz",
		dr.ClusterID,
		time.Now().UTC().Format("2006-01-02T15-04-05"),
	)

	if err := dr.ObjectStore.Upload(ctx, key, buf.Bytes()); err != nil {
		return fmt.Errorf("upload DR backup to %s: %w", key, err)
	}

	dr.Metrics.BackupSuccess.Inc()
	dr.Metrics.LastBackupTime.SetToCurrentTime()

	log.Info("DR backup completed",
		"key", key,
		"nodes", len(backup.Nodes),
		"compressedBytes", buf.Len(),
	)
	return nil
}
