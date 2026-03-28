// internal/controller/storage_manager.go
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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// evaluateDiskPressure checks the current disk usage against the AutoExpand
// threshold configured in spec.chainStorage.autoExpand. If usage exceeds the
// threshold and the current PVC size is below maxSize, it triggers an expansion.
//
// This method is non-fatal: errors are logged but do not fail the reconcile.
func (r *TaoNodeReconciler) evaluateDiskPressure(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	log := logf.FromContext(ctx)

	autoExpand := tn.Spec.ChainStorage.AutoExpand
	if autoExpand == nil {
		return nil // auto-expand not configured
	}

	diskPercent := tn.Status.Resources.DiskUsagePercent
	if int32(diskPercent) < autoExpand.ThresholdPercent {
		return nil // below threshold — no action needed
	}

	log.Info("Disk usage exceeds auto-expand threshold, evaluating expansion",
		"diskPercent", diskPercent,
		"threshold", autoExpand.ThresholdPercent,
	)

	if err := r.expandPVCPreemptively(ctx, tn); err != nil {
		return fmt.Errorf("auto-expand PVC: %w", err)
	}
	return nil
}

// expandPVCPreemptively increases the chain-data PVC capacity by
// spec.chainStorage.autoExpand.incrementPercent of the current size,
// capped at spec.chainStorage.autoExpand.maxSize.
//
// It only patches the PVC's storage request — the StorageClass must
// have allowVolumeExpansion=true for this to take effect.
func (r *TaoNodeReconciler) expandPVCPreemptively(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	log := logf.FromContext(ctx)
	autoExpand := tn.Spec.ChainStorage.AutoExpand
	if autoExpand == nil {
		return nil
	}

	// The StatefulSet PVC name follows the pattern: {volumeClaimTemplate.name}-{sts-name}-0
	pvcName := fmt.Sprintf("chain-data-%s-0", tn.Name)
	pvc := &corev1.PersistentVolumeClaim{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      pvcName,
		Namespace: tn.Namespace,
	}, pvc); err != nil {
		return fmt.Errorf("get PVC %s: %w", pvcName, err)
	}

	currentSize := pvc.Spec.Resources.Requests[corev1.ResourceStorage]

	// Compute new size: current + incrementPercent%, capped at maxSize.
	incrementMilliValue := currentSize.MilliValue() * int64(autoExpand.IncrementPercent) / 100
	newMilliValue := currentSize.MilliValue() + incrementMilliValue
	maxMilliValue := autoExpand.MaxSize.MilliValue()

	if newMilliValue > maxMilliValue {
		newMilliValue = maxMilliValue
	}

	// Already at max — nothing to do.
	if currentSize.MilliValue() >= maxMilliValue {
		log.Info("PVC already at maximum size, skipping auto-expand",
			"pvc", pvcName,
			"currentSize", currentSize.String(),
			"maxSize", autoExpand.MaxSize.String(),
		)
		return nil
	}

	newSize := resource.NewMilliQuantity(newMilliValue, resource.BinarySI)

	pvcCopy := pvc.DeepCopy()
	pvcCopy.Spec.Resources.Requests[corev1.ResourceStorage] = *newSize

	if err := r.Update(ctx, pvcCopy); err != nil {
		return fmt.Errorf("update PVC %s storage request: %w", pvcName, err)
	}

	r.Recorder.Eventf(tn, corev1.EventTypeNormal, "PVCExpanded",
		"Expanded chain-data PVC from %s to %s (disk at %d%%)",
		currentSize.String(), newSize.String(), tn.Status.Resources.DiskUsagePercent,
	)
	predictiveActionsTotal.WithLabelValues(tn.Name, tn.Namespace, "pvc-expand").Inc()
	log.Info("Chain-data PVC expanded preemptively",
		"pvc", pvcName,
		"from", currentSize.String(),
		"to", newSize.String(),
	)
	return nil
}
