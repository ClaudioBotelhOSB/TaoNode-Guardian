// internal/controller/snapshot_manager.go
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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// volumeSnapshotGVR is the GroupVersionResource for the VolumeSnapshot CRD.
// Using unstructured avoids adding the volumesnapshots API as a dependency.
// var volumeSnapshotGVR = schema.GroupVersionResource{
// 	Group:    "snapshot.storage.k8s.io",
// 	Version:  "v1",
// 	Resource: "volumesnapshots",
// }

// triggerSnapshot creates a VolumeSnapshot for the TaoNode's chain-data PVC.
//
// The snapshot is created in the same namespace as the TaoNode.
// Snapshot names are timestamped to ensure uniqueness and easy correlation.
// The reason parameter is included in the VolumeSnapshot annotations for audit.
//
// If spec.snapshotPolicy is nil, the call is a no-op (snapshot is only
// triggered if policy is configured).
func (r *TaoNodeReconciler) triggerSnapshot(ctx context.Context, tn *taov1alpha1.TaoNode, reason string) error {
	log := logf.FromContext(ctx)

	if tn.Spec.SnapshotPolicy == nil {
		return nil
	}

	start := time.Now()
	pvcName := fmt.Sprintf("chain-data-%s-0", tn.Name)
	snapshotName := fmt.Sprintf("%s-%s", tn.Name, time.Now().Format("20060102-150405"))

	// Determine VolumeSnapshotClass from the snapshot destination type.
	snapClass := snapshotClassForDestination(tn.Spec.SnapshotPolicy.Destination)

	snap := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "snapshot.storage.k8s.io/v1",
			"kind":       "VolumeSnapshot",
			"metadata": map[string]interface{}{
				"name":      snapshotName,
				"namespace": tn.Namespace,
				"labels":    r.labelsForNode(tn),
				"annotations": map[string]interface{}{
					"tao.guardian.io/snapshot-reason":    reason,
					"tao.guardian.io/node-name":          tn.Name,
					"tao.guardian.io/snapshot-timestamp": time.Now().UTC().Format(time.RFC3339),
				},
				"ownerReferences": []interface{}{
					map[string]interface{}{
						"apiVersion":         taov1alpha1.GroupVersion.String(),
						"kind":               "TaoNode",
						"name":               tn.Name,
						"uid":                string(tn.UID),
						"controller":         true,
						"blockOwnerDeletion": true,
					},
				},
			},
			"spec": map[string]interface{}{
				"volumeSnapshotClassName": snapClass,
				"source": map[string]interface{}{
					"persistentVolumeClaimName": pvcName,
				},
			},
		},
	}
	snap.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "snapshot.storage.k8s.io",
		Version: "v1",
		Kind:    "VolumeSnapshot",
	})

	if err := r.Create(ctx, snap); err != nil {
		return fmt.Errorf("create VolumeSnapshot %s: %w", snapshotName, err)
	}

	elapsed := time.Since(start).Seconds()
	snapshotDuration.WithLabelValues(tn.Name, tn.Namespace, reason).Observe(elapsed)

	r.Recorder.Eventf(tn, corev1.EventTypeNormal, "SnapshotTriggered",
		"VolumeSnapshot %s created (reason: %s)", snapshotName, reason)

	// Update snapshot status.
	now := metav1.Now()
	if tn.Status.Snapshot == nil {
		tn.Status.Snapshot = &taov1alpha1.SnapshotStatus{}
	}
	tn.Status.Snapshot.LastSnapshotTime = &now
	tn.Status.Snapshot.AvailableSnapshots++

	log.Info("VolumeSnapshot triggered",
		"snapshot", snapshotName,
		"pvc", pvcName,
		"reason", reason,
	)

	// Prune old snapshots if we exceed retention count.
	if err := r.pruneOldSnapshots(ctx, tn); err != nil {
		// Non-fatal: log and continue.
		log.Error(err, "Failed to prune old snapshots")
	}

	return nil
}

// pruneOldSnapshots lists existing snapshots for this TaoNode and deletes
// the oldest ones when the count exceeds spec.snapshotPolicy.retentionCount.
func (r *TaoNodeReconciler) pruneOldSnapshots(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	if tn.Spec.SnapshotPolicy == nil {
		return nil
	}

	snapList := &unstructured.UnstructuredList{}
	snapList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "snapshot.storage.k8s.io",
		Version: "v1",
		Kind:    "VolumeSnapshotList",
	})

	if err := r.List(ctx, snapList,
		client.InNamespace(tn.Namespace),
		client.MatchingLabels(r.labelsForNode(tn)),
	); err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}

	items := snapList.Items
	retention := int(tn.Spec.SnapshotPolicy.RetentionCount)
	if len(items) <= retention {
		return nil
	}

	// Sort by creation timestamp ascending (oldest first) and delete excess.
	sortSnapshotsByAge(items)
	toDelete := items[:len(items)-retention]

	for i := range toDelete {
		if err := r.Delete(ctx, &toDelete[i]); err != nil {
			return fmt.Errorf("delete snapshot %s: %w", toDelete[i].GetName(), err)
		}
	}
	return nil
}

// snapshotClassForDestination maps a SnapshotDestination type to a
// VolumeSnapshotClass name by convention.
func snapshotClassForDestination(dest taov1alpha1.SnapshotDestination) string {
	switch dest.Type {
	case "s3":
		return "csi-aws-vsc"
	case "gcs":
		return "csi-gce-pd-vsc"
	case "minio":
		return "csi-minio-vsc"
	default:
		return "csi-hostpath-snapclass"
	}
}

// sortSnapshotsByAge sorts unstructured VolumeSnapshots by creation timestamp ascending.
func sortSnapshotsByAge(items []unstructured.Unstructured) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0; j-- {
			ti := items[j].GetCreationTimestamp()
			tj := items[j-1].GetCreationTimestamp()
			if ti.Before(&tj) {
				items[j], items[j-1] = items[j-1], items[j]
			} else {
				break
			}
		}
	}
}

// recoverySnapshotRestore implements the snapshot-restore recovery strategy.
// It deletes the StatefulSet pod to force a pod restart, then attempts to
// restore from the most recent snapshot. Since snapshot restore is a storage-
// level operation requiring PVC recreation, this method annotates the TaoNode
// and emits an event for an administrator to act on (or an external controller).
func (r *TaoNodeReconciler) recoverySnapshotRestore(ctx context.Context, tn *taov1alpha1.TaoNode) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("Executing snapshot-restore recovery", "node", tn.Name)

	// First, take a pre-restore snapshot as a safety measure.
	if err := r.triggerSnapshot(ctx, tn, "pre-restore-backup"); err != nil {
		log.Error(err, "Pre-restore snapshot failed (continuing)")
	}

	// Delete pod-0 to force recreation from the PVC (existing data).
	// A full snapshot restore would require PVC deletion + recreation,
	// which is an admin operation surfaced via Event + annotation.
	podName := fmt.Sprintf("%s-0", tn.Name)
	pod := &corev1.Pod{}
	if err := r.Get(ctx, types.NamespacedName{Name: podName, Namespace: tn.Namespace}, pod); err == nil {
		if deleteErr := r.Delete(ctx, pod); deleteErr != nil {
			return ctrl.Result{}, fmt.Errorf("delete pod for snapshot-restore: %w", deleteErr)
		}
	}

	r.Recorder.Eventf(tn, corev1.EventTypeWarning, "SnapshotRestoreRequired",
		"Snapshot-restore recovery triggered. Manual PVC restoration from latest snapshot may be needed if restart does not resolve sync loss.")

	tn.Status.Phase = taov1alpha1.PhaseRecovering
	recoveryTotal.WithLabelValues(tn.Name, tn.Namespace, "snapshot-restore").Inc()

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}
