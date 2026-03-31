// internal/controller/taonode_controller.go
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
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/ai"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/analytics"
)

const (
	finalizerName = "tao.guardian.io/finalizer"
)

// TaoNodeReconciler reconciles TaoNode objects.
// All dependencies are injected at manager setup time in cmd/main.go.
//
// Analytics and AI fields are nil-safe: the controller operates in
// reactive-only mode when these are nil (no ClickHouse or Ollama configured).
//
// +kubebuilder:rbac:groups=tao.guardian.io,resources=taonodes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=tao.guardian.io,resources=taonodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=tao.guardian.io,resources=taonodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.coreos.com,resources=servicemonitors,verbs=get;list;watch;create;update;patch
type TaoNodeReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	// APIReader provides direct API-server reads bypassing the informer cache.
	// Used for reads that must be strongly consistent (e.g., PVC status after update).
	APIReader client.Reader

	// Analytics plane — all nil-safe. nil when analytics disabled globally.
	AnalyticsWriter   *analytics.BatchWriter
	AnomalyDetector   *analytics.AnomalyDetector
	ClickHouseCircuit *analytics.CircuitBreaker

	// AI advisory plane — nil-safe. nil when Ollama endpoint not configured.
	AIAdvisor *ai.Advisor

	// Resilience — prevents thundering-herd during fleet-wide events.
	RecoveryLimiter *RecoveryRateLimiter

	// Configuration
	DefaultImage    string       // fallback image when spec.image is empty
	ProbeHTTPClient *http.Client // shared HTTP client for chain-probe calls
}

// Reconcile is the core event loop. It is called whenever a TaoNode is created,
// updated, or when a watched resource (StatefulSet, Service, SA) changes.
//
// The 10-step loop (canonical from v3 Section 12, corrected by v5 I2):
//  1. Fetch TaoNode — return immediately if deleted
//  2. Finalizer management — add on creation, run cleanup on deletion
//  3. Ensure owned resources — SA, headless Service, StatefulSet, ServiceMonitor, Validator Lease
//  4. Probe chain health — HTTP GET to sidecar :9616/health
//  5. Analytics + anomaly detection — async telemetry push, sync anomaly queries
//  6. Update observed status — copy probe results into status fields
//  7. Evaluate sync state — state machine, triggers recovery if degraded
//  8. Disk pressure check — auto-expand PVC if threshold exceeded
//  9. AI advisory — async goroutine, non-blocking
//
// 10. Persist status + requeue — retry on conflict, requeue with probe interval
func (r *TaoNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	start := time.Now()

	// ── STEP 1: Fetch ──────────────────────────────────────────────────────
	var tn taov1alpha1.TaoNode
	if err := r.Get(ctx, req.NamespacedName, &tn); err != nil {
		// Object deleted before we could reconcile — not an error.
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ── STEP 2: Finalization ───────────────────────────────────────────────
	if !tn.DeletionTimestamp.IsZero() {
		return r.handleFinalization(ctx, &tn)
	}
	if !controllerutil.ContainsFinalizer(&tn, finalizerName) {
		controllerutil.AddFinalizer(&tn, finalizerName)
		if err := r.Update(ctx, &tn); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{}, nil // requeue immediately after update
	}
	
	// Set initial status phase so it appears immediately in kubectl get tn
	if tn.Status.Phase == "" {
		tn.Status.Phase = taov1alpha1.PhasePending
	}

	// ── STEP 3: Ensure owned resources ────────────────────────────────────
	if err := r.ensureServiceAccount(ctx, &tn); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure ServiceAccount: %w", err)
	}
	if err := r.ensureHeadlessService(ctx, &tn); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure headless Service: %w", err)
	}
	if err := r.ensureNodeWorkload(ctx, &tn); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensure StatefulSet: %w", err)
	}
	if tn.Spec.Monitoring != nil && tn.Spec.Monitoring.Enabled {
		if err := r.ensureServiceMonitor(ctx, &tn); err != nil {
			// Non-fatal: prometheus-operator CRD may not be installed.
			log.Error(err, "ServiceMonitor creation failed (non-fatal — prometheus-operator may not be installed)")
		}
	}
	if tn.Spec.Role == taov1alpha1.RoleValidator {
		if err := r.ensureValidatorSingleton(ctx, &tn); err != nil {
			return ctrl.Result{}, fmt.Errorf("ensure validator singleton: %w", err)
		}
	}

	// ── STEP 4: Probe chain health ─────────────────────────────────────────
	health, err := r.probeChainHealth(ctx, &tn)
	if err != nil {
		log.Error(err, "Chain health probe failed")
		r.setCondition(&tn, "ProbeSucceeded", metav1.ConditionFalse, "ProbeFailed", err.Error())
		// If pod is not yet running, requeue with a longer delay.
		if tn.Status.Phase == taov1alpha1.PhasePending {
			return r.updateStatusAndRequeue(ctx, &tn, 15*time.Second)
		}
		return r.updateStatusAndRequeue(ctx, &tn, 30*time.Second)
	}
	r.setCondition(&tn, "ProbeSucceeded", metav1.ConditionTrue, "ProbeOK", "Chain probe succeeded")

	// ── STEP 5: Analytics + anomaly detection (optional, nil-safe) ─────────
	anomalyScores := r.reconcileWithAnalytics(ctx, &tn, health)

	// ── STEP 6: Update observed status ─────────────────────────────────────
	tn.Status.SyncState.CurrentBlock = health.CurrentBlock
	tn.Status.SyncState.NetworkBlock = health.NetworkBlock
	tn.Status.SyncState.BlockLag = health.NetworkBlock - health.CurrentBlock
	tn.Status.SyncState.State = health.SyncState
	tn.Status.Chain.PeerCount = health.PeerCount
	tn.Status.Chain.FinalizedBlock = health.FinalizedBlock
	tn.Status.Chain.RuntimeVersion = health.RuntimeVersion
	tn.Status.Chain.Epoch = health.Epoch
	tn.Status.Resources.DiskUsagePercent = health.DiskUsagePercent

	// GPU status (nil-safe).
	updateGPUStatus(&tn, health)

	// FinOps estimate.
	updateFinOpsStatus(&tn)

	// Update anomaly scores in status.
	if len(anomalyScores) > 0 && tn.Status.Analytics != nil {
		scores := make([]taov1alpha1.AnomalyScoreStatus, 0, len(anomalyScores))
		for _, a := range anomalyScores {
			now := metav1.NewTime(a.DetectedAt)
			scores = append(scores, taov1alpha1.AnomalyScoreStatus{
				Type:       a.Type,
				Score:      strconv.FormatFloat(float64(a.Score), 'f', 3, 32),
				Detail:     a.Detail,
				DetectedAt: &now,
			})
		}
		tn.Status.Analytics.AnomalyScores = scores
	}

	// ── STEP 7: Evaluate sync state + trigger recovery ─────────────────────
	result, err := r.evaluateSyncState(ctx, &tn, health, anomalyScores)
	if err != nil {
		return ctrl.Result{}, err
	}

	// ── STEP 8: Disk pressure check (non-fatal) ────────────────────────────
	if err := r.evaluateDiskPressure(ctx, &tn); err != nil {
		log.Error(err, "Disk pressure evaluation failed (non-fatal)")
	}

	// ── STEP 9: AI advisory (async, non-blocking) ──────────────────────────
	r.maybeRequestAIAdvisory(ctx, &tn, anomalyScores)

	// ── STEP 10: Persist status + requeue ──────────────────────────────────
	tn.Status.ObservedGeneration = tn.Generation
	now := metav1.Now()
	tn.Status.LastReconcileTime = &now

	// Record reconcile duration.
	reconcileDuration.WithLabelValues(tn.Name, tn.Namespace, "success").
		Observe(time.Since(start).Seconds())

	if result.RequeueAfter > 0 {
		return r.updateStatusAndRequeue(ctx, &tn, result.RequeueAfter)
	}
	probeInterval := time.Duration(tn.Spec.SyncPolicy.ProbeIntervalSeconds) * time.Second
	return r.updateStatusAndRequeue(ctx, &tn, probeInterval)
}

// handleFinalization runs cleanup logic when a TaoNode is being deleted.
// It removes the finalizer after cleanup to allow garbage collection.
func (r *TaoNodeReconciler) handleFinalization(ctx context.Context, tn *taov1alpha1.TaoNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(tn, finalizerName) {
		return ctrl.Result{}, nil
	}

	log.Info("Running TaoNode finalization", "node", tn.Name)

	// Emit a final snapshot before deletion if snapshot policy is configured.
	if tn.Spec.SnapshotPolicy != nil && tn.Spec.SnapshotPolicy.OnSyncLoss {
		if err := r.triggerSnapshot(ctx, tn, "pre-deletion"); err != nil {
			log.Error(err, "Pre-deletion snapshot failed (continuing finalization)")
		}
	}

	// Release the validator singleton Lease if held.
	if tn.Spec.Role == taov1alpha1.RoleValidator {
		leaseName := fmt.Sprintf("%s-validator-singleton", tn.Name)
		if err := r.deleteLeaseIfExists(ctx, tn.Namespace, leaseName); err != nil {
			log.Error(err, "Failed to delete validator Lease during finalization")
		}
	}

	r.Recorder.Eventf(tn, corev1.EventTypeNormal, "Finalized",
		"TaoNode resources cleaned up, removing finalizer")

	controllerutil.RemoveFinalizer(tn, finalizerName)
	if err := r.Update(ctx, tn); err != nil {
		return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

// deleteLeaseIfExists deletes a Lease by name, ignoring NotFound errors.
func (r *TaoNodeReconciler) deleteLeaseIfExists(ctx context.Context, namespace, name string) error {
	lease := &coordinationv1.Lease{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, lease); err != nil {
		return client.IgnoreNotFound(err)
	}
	return r.Delete(ctx, lease)
}

// updateStatusAndRequeue persists the TaoNode status subresource and returns
// a Result with the specified requeue delay.
//
// Uses retry.RetryOnConflict to handle optimistic concurrency conflicts (409):
//  1. Attempt r.Status().Update(ctx, tn)
//  2. On 409 Conflict: re-GET the latest TaoNode, re-apply the
//     reconciler's status mutations onto the fresh object, retry
//  3. Max 5 retries with exponential backoff (client-go default)
//
// This is canonical from v5 J3.
func (r *TaoNodeReconciler) updateStatusAndRequeue(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	after time.Duration,
) (ctrl.Result, error) {
	// Capture the status we want to persist before the retry loop.
	desiredStatus := tn.Status

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &taov1alpha1.TaoNode{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      tn.Name,
			Namespace: tn.Namespace,
		}, latest); err != nil {
			return err
		}
		// Re-apply the reconciler's status mutations onto the fresh object.
		latest.Status = desiredStatus
		return r.Status().Update(ctx, latest)
	})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("update TaoNode status: %w", err)
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// reconcileWithAnalytics wraps all analytics-plane interactions with graceful
// degradation. If the analytics plane is unavailable or disabled, the controller
// falls back to purely reactive reconciliation.
//
// All analytics calls are nil-safe: this method returns nil if any required
// component is not initialized.
func (r *TaoNodeReconciler) reconcileWithAnalytics(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	health *ChainHealthResult,
) []analytics.AnomalyScore {
	log := logf.FromContext(ctx)

	if tn.Spec.Analytics == nil || !tn.Spec.Analytics.Enabled {
		return nil
	}
	if r.AnalyticsWriter == nil || r.ClickHouseCircuit == nil {
		return nil
	}

	// Circuit breaker check — skip if ClickHouse is known-down.
	if !r.ClickHouseCircuit.Allow() {
		r.setCondition(tn, "AnalyticsAvailable", metav1.ConditionFalse,
			"CircuitBreakerOpen",
			"ClickHouse circuit open, operating in reactive-only mode")
		analyticsGracefulDegradations.Inc()
		return nil
	}

	// Push telemetry asynchronously (non-blocking channel send).
	r.AnalyticsWriter.PushTelemetry(r.buildTelemetryRow(tn, health))

	// Push reconcile audit record.
	if tn.Spec.Analytics.Ingestion.ReconcileAudit {
		r.AnalyticsWriter.PushReconcileAudit(r.buildReconcileAuditRow(tn))
	}

	// Update analytics status block.
	if tn.Status.Analytics == nil {
		tn.Status.Analytics = &taov1alpha1.AnalyticsStatusBlock{}
	}
	tn.Status.Analytics.Connected = true
	now := metav1.Now()
	tn.Status.Analytics.LastIngestionTime = &now

	// Anomaly detection (with 10s timeout — don't block reconcile).
	if r.AnomalyDetector == nil {
		return nil
	}
	if tn.Spec.Analytics.AnomalyDetection == nil || !tn.Spec.Analytics.AnomalyDetection.Enabled {
		return nil
	}

	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	queryStart := time.Now()
	scores, err := r.AnomalyDetector.EvaluateNode(
		queryCtx,
		tn.Namespace,
		tn.Name,
		tn.Spec.Network,
		uint16(tn.Spec.SubnetID),
	)
	anomalyEvaluationDuration.WithLabelValues(tn.Name, tn.Namespace).
		Observe(time.Since(queryStart).Seconds())

	if err != nil {
		r.ClickHouseCircuit.RecordFailure()
		log.Error(err, "Anomaly detection query failed, falling back to reactive mode")
		r.setCondition(tn, "AnalyticsAvailable", metav1.ConditionFalse, "QueryFailed", err.Error())
		if tn.Status.Analytics != nil {
			tn.Status.Analytics.Connected = false
		}
		return nil
	}

	r.ClickHouseCircuit.RecordSuccess()
	r.setCondition(tn, "AnalyticsAvailable", metav1.ConditionTrue, "Connected", "Analytics plane healthy")
	return scores
}

// buildTelemetryRow constructs a ChainTelemetryRow from the current TaoNode
// status and health probe result.
func (r *TaoNodeReconciler) buildTelemetryRow(
	tn *taov1alpha1.TaoNode,
	health *ChainHealthResult,
) analytics.ChainTelemetryRow {
	var blocksPerSecond float32
	if health.ProbeLatency > 0 {
		blocksPerSecond = float32(health.CurrentBlock) / float32(health.ProbeLatency.Seconds())
	}

	var probeSuccess uint8 = 1 // we only build this row on successful probes

	return analytics.ChainTelemetryRow{
		Timestamp:             time.Now().UTC(),
		Namespace:             tn.Namespace,
		NodeName:              tn.Name,
		Network:               tn.Spec.Network,
		SubnetID:              uint16(tn.Spec.SubnetID),
		Role:                  string(tn.Spec.Role),
		CurrentBlock:          uint64(health.CurrentBlock),
		NetworkBlock:          uint64(health.NetworkBlock),
		BlockLag:              health.NetworkBlock - health.CurrentBlock,
		FinalizedBlock:        uint64(health.FinalizedBlock),
		PeerCount:             uint16(health.PeerCount),
		RuntimeVersion:        health.RuntimeVersion,
		SyncState:             health.SyncState,
		BlocksPerSecond:       blocksPerSecond,
		ProbeLatencyMs:        uint32(health.ProbeLatency.Milliseconds()),
		ProbeSuccess:          probeSuccess,
		DiskUsagePercent:      uint8(health.DiskUsagePercent),
		DiskUsedBytes:         health.DiskUsedBytes,
		DiskTotalBytes:        health.DiskTotalBytes,
		GPUUtilizationPercent: health.GPUUtilPercent,
		GPUMemoryUsedBytes:    health.GPUMemUsedBytes,
		GPUTemperatureCelsius: health.GPUTempCelsius,
	}
}

// buildReconcileAuditRow constructs a ReconcileAuditRow for audit logging.
func (r *TaoNodeReconciler) buildReconcileAuditRow(tn *taov1alpha1.TaoNode) analytics.ReconcileAuditRow {
	return analytics.ReconcileAuditRow{
		Timestamp:       time.Now().UTC(),
		Namespace:       tn.Namespace,
		NodeName:        tn.Name,
		TriggerType:     "periodic",
		Generation:      uint64(tn.Generation),
		ResourceVersion: tn.ResourceVersion,
		PhaseBefore:     string(tn.Status.Phase),
		SyncStateBefore: tn.Status.SyncState.State,
		BlockLagBefore:  tn.Status.SyncState.BlockLag,
	}
}

// maybeRequestAIAdvisory launches an AI incident analysis in a background
// goroutine when the anomaly score exceeds the configured threshold.
//
// This is intentionally async and non-blocking: AI inference can take 10-30s
// and must NOT delay the reconcile loop or block status updates.
func (r *TaoNodeReconciler) maybeRequestAIAdvisory(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	anomalies []analytics.AnomalyScore,
) {
	if r.AIAdvisor == nil {
		return
	}
	if tn.Spec.AIAdvisor == nil || !tn.Spec.AIAdvisor.Enabled {
		return
	}
	if len(anomalies) == 0 {
		return
	}

	// Check if any anomaly exceeds the minimum score threshold.
	minScore := float32(0.6)
	if tn.Spec.AIAdvisor.MinAnomalyScoreForAdvisory != "" {
		if parsed, err := strconv.ParseFloat(tn.Spec.AIAdvisor.MinAnomalyScoreForAdvisory, 32); err == nil {
			minScore = float32(parsed)
		}
	}

	shouldAdvise := false
	for _, a := range anomalies {
		if a.Score >= minScore {
			shouldAdvise = true
			break
		}
	}
	if !shouldAdvise {
		return
	}

	// Launch in background goroutine — copy required data to avoid data races.
	incidentCtx := r.buildIncidentContext(ctx, tn, anomalies)
	tnCopy := tn.DeepCopy()
	advisor := r.AIAdvisor

	go func() {
		// Use a detached context with a timeout for the async AI call.
		aiCtx, cancel := context.WithTimeout(context.Background(),
			time.Duration(tnCopy.Spec.AIAdvisor.TimeoutSeconds)*time.Second)
		defer cancel()

		report, err := advisor.AnalyzeIncident(aiCtx, incidentCtx)
		if err != nil {
			logf.FromContext(ctx).Error(err, "AI advisory analysis failed (non-fatal)")
			return
		}
		if report == nil {
			return
		}

		// Update AI advisory status (non-blocking update, separate reconcile context).
		updateCtx, updateCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer updateCancel()
		if err := r.updateAIAdvisoryStatus(updateCtx, tnCopy, report); err != nil {
			logf.FromContext(ctx).Error(err, "Failed to update AI advisory status")
		}

		// Send notifications (Slack, PagerDuty, K8s Events).
		r.sendNotifications(updateCtx, tnCopy, report)
	}()
}

// buildIncidentContext constructs the structured context sent to the LLM.
func (r *TaoNodeReconciler) buildIncidentContext(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	anomalies []analytics.AnomalyScore,
) ai.IncidentContext {
	return ai.IncidentContext{
		NodeName:      tn.Name,
		Namespace:     tn.Namespace,
		Network:       tn.Spec.Network,
		SubnetID:      tn.Spec.SubnetID,
		Role:          string(tn.Spec.Role),
		CurrentPhase:  string(tn.Status.Phase),
		AnomalyScores: anomalies,
		// RecentTelemetry and PastIncidents are populated by Phase 3
		// context_builder when the analytics package is available.
		RecentTelemetry: nil,
		PastIncidents:   nil,
		RecoveryHistory: nil,
	}
}

// updateAIAdvisoryStatus persists an AI incident report into the TaoNode status.
func (r *TaoNodeReconciler) updateAIAdvisoryStatus(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	report *ai.IncidentReport,
) error {
	confidence := strconv.FormatFloat(float64(report.Confidence), 'f', 2, 32)
	now := metav1.Now()

	desiredAIStatus := &taov1alpha1.AIAdvisoryStatus{
		ModelAvailable: true,
		LastReport: &taov1alpha1.AIIncidentReportStatus{
			Timestamp:         &now,
			Severity:          report.Severity,
			Summary:           report.Summary,
			RootCauseCategory: report.RootCauseCategory,
			RecommendedAction: report.RecommendedAction,
			Confidence:        confidence,
		},
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &taov1alpha1.TaoNode{}
		if err := r.Get(ctx, types.NamespacedName{
			Name:      tn.Name,
			Namespace: tn.Namespace,
		}, latest); err != nil {
			return err
		}
		latest.Status.AIAdvisory = desiredAIStatus
		return r.Status().Update(ctx, latest)
	})
}

// sendNotifications dispatches an AI incident report to all configured channels.
// Failures are logged but do not affect the reconcile result.
func (r *TaoNodeReconciler) sendNotifications(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	report *ai.IncidentReport,
) {
	log := logf.FromContext(ctx)

	if tn.Spec.AIAdvisor == nil || tn.Spec.AIAdvisor.NotificationChannels == nil {
		return
	}
	channels := tn.Spec.AIAdvisor.NotificationChannels

	// Kubernetes Events are always emitted (default: true).
	if channels.KubernetesEvents {
		r.Recorder.Eventf(tn, corev1.EventTypeWarning, "AIIncidentReport",
			"[%s] %s — %s (confidence: %.0f%%)",
			report.Severity,
			report.Summary,
			report.RecommendedAction,
			report.Confidence*100,
		)
	}

	// External notifications are handled by the ai.Notifier in Phase 3.
	// For now, log at Info level so the report is not lost.
	if channels.SlackWebhookSecret != "" || channels.PagerDutyKeySecret != "" {
		log.Info("AI incident report ready for external notification",
			"severity", report.Severity,
			"summary", report.Summary,
			"action", report.RecommendedAction,
			"confidence", report.Confidence,
		)
	}
}

// SetupWithManager registers the TaoNodeReconciler with the controller-runtime
// manager. It watches TaoNode CRs and the owned StatefulSets, Services, and
// ServiceAccounts. Any change to these owned resources re-triggers reconciliation.
//
// MaxConcurrentReconciles: 10 — allows parallel reconciliation of 10 TaoNodes.
// This is safe because each reconcile targets a specific TaoNode by name/namespace.
func (r *TaoNodeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&taov1alpha1.TaoNode{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		Complete(r)
}
