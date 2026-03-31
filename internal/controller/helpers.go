// internal/controller/helpers.go
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
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// labelsForNode returns the standard label set applied to all resources
// owned by a TaoNode. These labels are also used by selectors.
func (r *TaoNodeReconciler) labelsForNode(tn *taov1alpha1.TaoNode) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "taonode-guardian",
		"app.kubernetes.io/instance":   tn.Name,
		"app.kubernetes.io/managed-by": "taonode-guardian",
		"tao.guardian.io/network":      tn.Spec.Network,
		"tao.guardian.io/subnet":       fmt.Sprintf("%d", tn.Spec.SubnetID),
		"tao.guardian.io/role":         string(tn.Spec.Role),
	}
}

// annotationsForNode returns annotations applied to TaoNode-owned pods.
// These are used for Prometheus auto-discovery and versioning.
func (r *TaoNodeReconciler) annotationsForNode(tn *taov1alpha1.TaoNode) map[string]string {
	ann := map[string]string{
		"tao.guardian.io/node-name": tn.Name,
		"tao.guardian.io/network":   tn.Spec.Network,
	}
	if tn.Spec.Version != "" {
		ann["tao.guardian.io/version"] = tn.Spec.Version
	}
	if tn.Spec.Monitoring != nil && tn.Spec.Monitoring.Enabled {
		ann["prometheus.io/scrape"] = "true"
		ann["prometheus.io/port"] = fmt.Sprintf("%d", tn.Spec.Monitoring.Port)
	}
	return ann
}

// setCondition upserts a condition in the TaoNode status conditions array.
// It follows the KEP-1623 convention: only updates LastTransitionTime when
// the Status value actually changes, preserving stability for `kubectl wait`.
func (r *TaoNodeReconciler) setCondition(
	tn *taov1alpha1.TaoNode,
	condType string,
	status metav1.ConditionStatus,
	reason, message string,
) {
	now := metav1.Now()
	existingCondition := meta.FindStatusCondition(tn.Status.Conditions, condType)

	newCondition := metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: tn.Generation,
		LastTransitionTime: now,
	}

	// Preserve LastTransitionTime if the status hasn't changed.
	if existingCondition != nil && existingCondition.Status == status {
		newCondition.LastTransitionTime = existingCondition.LastTransitionTime
	}

	meta.SetStatusCondition(&tn.Status.Conditions, newCondition)
}



// imageForNode returns the container image to use for the node workload.
// Spec.Image takes precedence; falls back to the operator's DefaultImage.
func (r *TaoNodeReconciler) imageForNode(tn *taov1alpha1.TaoNode) string {
	if tn.Spec.Image != "" {
		return tn.Spec.Image
	}
	if r.DefaultImage != "" {
		return r.DefaultImage
	}
	return "opentensor/subtensor:latest"
}
