// internal/controller/gpu_advisor.go
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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	ptr "k8s.io/utils/ptr"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// gpuResourceName returns the resource key for the GPU type.
// Defaults to the standard NVIDIA GPU resource if the type is not recognized.
func gpuResourceName(gpuType string) corev1.ResourceName {
	switch gpuType {
	case "amd-gpu":
		return "amd.com/gpu"
	default:
		return "nvidia.com/gpu"
	}
}

// buildGPUResourceRequirements returns a ResourceRequirements object that
// requests the exact GPU count specified in spec.gpu. This is merged with
// the user's resource requests/limits.
func buildGPUResourceRequirements(gpu *taov1alpha1.GPUSpec) corev1.ResourceRequirements {
	if gpu == nil {
		return corev1.ResourceRequirements{}
	}
	gpuRes := gpuResourceName(gpu.Type)
	qty := resource.NewQuantity(int64(gpu.Count), resource.DecimalSI)
	return corev1.ResourceRequirements{
		Limits: corev1.ResourceList{
			gpuRes: *qty,
		},
		Requests: corev1.ResourceList{
			gpuRes: *qty,
		},
	}
}

// buildGPUTolerations returns tolerations required to schedule on GPU nodes.
// Standard NVIDIA device-plugin taint: nvidia.com/gpu=present:NoSchedule.
func buildGPUTolerations(gpu *taov1alpha1.GPUSpec) []corev1.Toleration {
	if gpu == nil {
		return nil
	}

	tolerations := []corev1.Toleration{
		{
			Key:      "nvidia.com/gpu",
			Operator: corev1.TolerationOpExists,
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}

	// Spot/preemptible taint tolerance.
	if gpu.SpotTolerant {
		tolerations = append(tolerations,
			corev1.Toleration{
				Key:      "cloud.google.com/gke-spot",
				Operator: corev1.TolerationOpEqual,
				Value:    "true",
				Effect:   corev1.TaintEffectNoSchedule,
			},
			corev1.Toleration{
				Key:      "kubernetes.azure.com/scalesetpriority",
				Operator: corev1.TolerationOpEqual,
				Value:    "spot",
				Effect:   corev1.TaintEffectNoSchedule,
			},
		)
	}

	return tolerations
}

// buildGPUNodeAffinity returns a NodeAffinity that targets nodes with the
// specified GPU type label (set by the NVIDIA GPU feature-discovery plugin).
func buildGPUNodeAffinity(gpu *taov1alpha1.GPUSpec) *corev1.NodeAffinity {
	if gpu == nil {
		return nil
	}

	required := &corev1.NodeSelectorRequirement{
		Key:      "nvidia.com/gpu.product",
		Operator: corev1.NodeSelectorOpIn,
		Values:   []string{gpu.Type},
	}

	affinity := &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{*required},
				},
			},
		},
	}

	// If fallbackToCPU is enabled, add a preferred term that also matches
	// nodes WITHOUT a GPU — this allows scheduling even when GPU nodes are
	// unavailable, at the cost of reduced performance.
	if gpu.FallbackToCPU {
		affinity.RequiredDuringSchedulingIgnoredDuringExecution = nil
		affinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
			{
				Weight: 100,
				Preference: corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{*required},
				},
			},
		}
	}

	return affinity
}

// mergeResourceRequirements merges GPU resource requirements into the user's
// resource requests/limits, with GPU limits taking precedence.
func mergeResourceRequirements(base corev1.ResourceRequirements, gpu corev1.ResourceRequirements) corev1.ResourceRequirements {
	result := base.DeepCopy()
	if result.Requests == nil {
		result.Requests = corev1.ResourceList{}
	}
	if result.Limits == nil {
		result.Limits = corev1.ResourceList{}
	}
	for k, v := range gpu.Requests {
		result.Requests[k] = v
	}
	for k, v := range gpu.Limits {
		result.Limits[k] = v
	}
	return *result
}

// updateGPUStatus writes GPU metrics to the TaoNode status and Prometheus.
func updateGPUStatus(tn *taov1alpha1.TaoNode, health *ChainHealthResult) {
	if health.GPUUtilPercent > 0 {
		util := int32(health.GPUUtilPercent)
		tn.Status.Resources.GPUUtilizationPercent = ptr.To(util)
		gpuUtilization.WithLabelValues(tn.Name, tn.Namespace).Set(float64(util))
	}
}
