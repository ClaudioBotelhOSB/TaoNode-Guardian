// internal/controller/sync_state_machine.go
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
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/analytics"
)

// evaluateSyncState is the core state machine that transitions TaoNode phases
// based on the current chain health and any detected anomalies.
//
// State transitions:
//
//	Pending    → Syncing  : pod is running and probing successfully
//	Syncing    → Synced   : block lag drops below maxBlockLag
//	Synced     → Degraded : block lag exceeds maxBlockLag OR anomaly score > 0.7
//	Degraded   → Recovering: recovery is triggered via executeRecovery
//	Recovering → Syncing  : recovery action completed, node restarted
//	Any        → Failed   : maxRestartAttempts exceeded
func (r *TaoNodeReconciler) evaluateSyncState(
	ctx context.Context,
	tn *taov1alpha1.TaoNode,
	health *ChainHealthResult,
	anomalies []analytics.AnomalyScore,
) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	blockLagVal := health.NetworkBlock - health.CurrentBlock
	maxLag := tn.Spec.SyncPolicy.MaxBlockLag
	isSynced := blockLagVal <= maxLag && health.SyncState == "in-sync"

	// Update Prometheus gauge.
	blockLag.WithLabelValues(tn.Name, tn.Namespace, tn.Spec.Network).Set(float64(blockLagVal))
	peerCount.WithLabelValues(tn.Name, tn.Namespace, tn.Spec.Network).Set(float64(health.PeerCount))
	if isSynced {
		syncState.WithLabelValues(tn.Name, tn.Namespace, tn.Spec.Network).Set(1)
	} else {
		syncState.WithLabelValues(tn.Name, tn.Namespace, tn.Spec.Network).Set(0)
	}

	// Update anomaly scores in status and Prometheus.
	for _, a := range anomalies {
		anomalyScore.WithLabelValues(tn.Name, tn.Namespace, a.Type).Set(float64(a.Score))
	}

	// Determine if a critical anomaly (score > 0.7) warrants proactive recovery.
	hasCriticalAnomaly := false
	for _, a := range anomalies {
		if a.Score >= 0.7 {
			hasCriticalAnomaly = true
			log.Info("Critical anomaly detected",
				"type", a.Type,
				"score", a.Score,
				"detail", a.Detail,
			)
		}
	}

	switch {
	case tn.Status.Phase == taov1alpha1.PhasePending:
		// First reconcile — transition to Syncing now that the pod is responsive.
		tn.Status.Phase = taov1alpha1.PhaseSyncing
		r.setCondition(tn, "Synced", metav1.ConditionFalse, "InitialSync", "Node is syncing from genesis or snapshot")
		return ctrl.Result{RequeueAfter: time.Duration(tn.Spec.SyncPolicy.ProbeIntervalSeconds) * time.Second}, nil

	case isSynced && !hasCriticalAnomaly:
		// Happy path.
		if tn.Status.Phase != taov1alpha1.PhaseSynced {
			log.Info("TaoNode reached sync", "blockLag", blockLagVal)
			r.Recorder.Eventf(tn, corev1.EventTypeNormal, "NodeSynced",
				"Node is in-sync (lag: %d blocks, peers: %d)", blockLagVal, health.PeerCount)
		}
		tn.Status.Phase = taov1alpha1.PhaseSynced
		now := metav1.Now()
		tn.Status.SyncState.LastSyncedAt = &now
		tn.Status.SyncState.SyncPercentage = "100%"
		r.setCondition(tn, "Synced", metav1.ConditionTrue, "InSync", fmt.Sprintf("Block lag: %d", blockLagVal))
		return ctrl.Result{}, nil

	case !isSynced && tn.Status.Phase == taov1alpha1.PhaseSynced:
		// Synced → Degraded transition.
		log.Info("TaoNode fell out of sync",
			"blockLag", blockLagVal,
			"maxBlockLag", maxLag,
			"syncState", health.SyncState,
		)
		r.Recorder.Eventf(tn, corev1.EventTypeWarning, "NodeDegraded",
			"Sync lost: block lag %d exceeds threshold %d", blockLagVal, maxLag)
		tn.Status.Phase = taov1alpha1.PhaseDegraded
		r.setCondition(tn, "Synced", metav1.ConditionFalse, "SyncLost",
			fmt.Sprintf("Block lag %d > maxBlockLag %d", blockLagVal, maxLag))
		fallthrough

	case tn.Status.Phase == taov1alpha1.PhaseDegraded || hasCriticalAnomaly:
		// Check restart budget before attempting recovery.
		if tn.Status.RestartCount >= tn.Spec.SyncPolicy.MaxRestartAttempts {
			log.Error(nil, "Max restart attempts exceeded, transitioning to Failed",
				"restartCount", tn.Status.RestartCount,
				"maxRestartAttempts", tn.Spec.SyncPolicy.MaxRestartAttempts,
			)
			tn.Status.Phase = taov1alpha1.PhaseFailed
			r.setCondition(tn, "Ready", metav1.ConditionFalse, "MaxRestartsExceeded",
				fmt.Sprintf("Restart count %d exceeded max %d",
					tn.Status.RestartCount, tn.Spec.SyncPolicy.MaxRestartAttempts))
			r.Recorder.Eventf(tn, corev1.EventTypeWarning, "NodeFailed",
				"TaoNode failed: restart budget exhausted after %d attempts",
				tn.Status.RestartCount)
			return ctrl.Result{}, nil
		}
		return r.executeRecovery(ctx, tn)

	case tn.Status.Phase == taov1alpha1.PhaseRecovering:
		// Still recovering — check if recovery has timed out.
		syncTimeoutDuration := time.Duration(tn.Spec.SyncPolicy.SyncTimeoutMinutes) * time.Minute
		if tn.Status.LastReconcileTime != nil &&
			time.Since(tn.Status.LastReconcileTime.Time) > syncTimeoutDuration {
			log.Error(nil, "Recovery timed out", "timeout", syncTimeoutDuration)
			tn.Status.Phase = taov1alpha1.PhaseFailed
			r.setCondition(tn, "Ready", metav1.ConditionFalse, "RecoveryTimeout",
				fmt.Sprintf("Recovery exceeded %s timeout", syncTimeoutDuration))
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

	default:
		// Syncing phase — report progress.
		if health.NetworkBlock > 0 {
			progress := float64(health.CurrentBlock) / float64(health.NetworkBlock) * 100
			tn.Status.SyncState.SyncPercentage = fmt.Sprintf("%.1f%%", progress)
		}
		r.setCondition(tn, "Synced", metav1.ConditionFalse, "Syncing",
			fmt.Sprintf("Block %d / %d (lag: %d)", health.CurrentBlock, health.NetworkBlock, blockLagVal))
		return ctrl.Result{RequeueAfter: time.Duration(tn.Spec.SyncPolicy.ProbeIntervalSeconds) * time.Second}, nil
	}
}

// executeRecovery dispatches to the appropriate recovery strategy based on
// spec.syncPolicy.recoveryStrategy. It is protected by the RecoveryRateLimiter
// to prevent simultaneous fleet-wide restarts.
func (r *TaoNodeReconciler) executeRecovery(ctx context.Context, tn *taov1alpha1.TaoNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	nodeKey := tn.Namespace + "/" + tn.Name

	if !r.RecoveryLimiter.TryAcquire(nodeKey) {
		r.Recorder.Eventf(tn, corev1.EventTypeNormal, "RecoveryThrottled",
			"Recovery rate-limited (%d/%d active), requeuing in 30s",
			r.RecoveryLimiter.ActiveCount(), 5)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	tn.Status.Phase = taov1alpha1.PhaseRecovering
	tn.Status.RestartCount++

	var result ctrl.Result
	var err error
	switch tn.Spec.SyncPolicy.RecoveryStrategy {
	case "restart":
		result, err = r.recoveryRestart(ctx, tn)
	case "snapshot-restore":
		result, err = r.recoverySnapshotRestore(ctx, tn)
	case "cordon-and-alert":
		result, err = r.recoveryCordonAndAlert(ctx, tn)
	default:
		log.Info("Unknown recovery strategy, defaulting to restart",
			"strategy", tn.Spec.SyncPolicy.RecoveryStrategy)
		result, err = r.recoveryRestart(ctx, tn)
	}
	if err != nil || tn.Status.Phase == taov1alpha1.PhaseFailed {
		r.RecoveryLimiter.Release(nodeKey)
	}
	return result, err
}

// recoveryRestart deletes pod-0 of the TaoNode's StatefulSet, causing the
// StatefulSet controller to recreate it. The node will re-sync from its
// existing chain data on the PVC.
func (r *TaoNodeReconciler) recoveryRestart(ctx context.Context, tn *taov1alpha1.TaoNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	podName := fmt.Sprintf("%s-0", tn.Name)
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: tn.Namespace}, pod); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.Delete(ctx, pod); err != nil {
		return ctrl.Result{}, fmt.Errorf("delete pod for restart recovery: %w", err)
	}

	recoveryTotal.WithLabelValues(tn.Name, tn.Namespace, "restart").Inc()
	r.Recorder.Eventf(tn, corev1.EventTypeNormal, "RecoveryRestart",
		"Pod %s deleted for restart recovery (attempt %d/%d)",
		podName, tn.Status.RestartCount, tn.Spec.SyncPolicy.MaxRestartAttempts)
	log.Info("Recovery restart executed",
		"pod", podName,
		"attempt", tn.Status.RestartCount,
	)

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// recoveryCordonAndAlert marks the TaoNode as Failed, emits an event, and
// does NOT attempt automatic recovery. Used when automatic recovery would
// be too risky (e.g., validator on mainnet with unclear cause).
func (r *TaoNodeReconciler) recoveryCordonAndAlert(ctx context.Context, tn *taov1alpha1.TaoNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tn.Status.Phase = taov1alpha1.PhaseFailed
	r.setCondition(tn, "Ready", metav1.ConditionFalse, "CordonAndAlert",
		"Node cordon-and-alert strategy invoked — manual intervention required")

	r.Recorder.Eventf(tn, corev1.EventTypeWarning, "NodeCordoned",
		"TaoNode cordoned: automatic recovery disabled (strategy: cordon-and-alert). Manual intervention required.")

	recoveryTotal.WithLabelValues(tn.Name, tn.Namespace, "cordon-and-alert").Inc()
	log.Error(nil, "TaoNode cordoned, manual intervention required",
		"node", tn.Name,
		"namespace", tn.Namespace,
	)

	// Requeue slowly — keep emitting events until operator intervenes.
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}
