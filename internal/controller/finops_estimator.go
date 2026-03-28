// internal/controller/finops_estimator.go
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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// Approximate on-demand pricing constants (USD/hour) for common instance types.
// These are used for rough FinOps estimation only; real pricing comes from
// the analytics pipeline (internal/analytics/finops_calculator.go in Phase 3).
const (
	cpuCostPerCorePerHour     = 0.05  // ~$0.05/vCPU-hour (compute-optimized)
	memCostPerGiBPerHour      = 0.006 // ~$0.006/GiB-hour
	storageCostPerGiBPerMonth = 0.10  // ~$0.10/GiB/month (gp3 EBS)
	gpuCostPerGPUPerHour      = 2.50  // ~$2.50/GPU-hour (T4 on-demand)
	hoursPerMonth             = 730.0
)

// estimateMonthlyCostUSD computes a rough monthly infrastructure cost estimate
// for a TaoNode based on its resource requests and storage configuration.
// The result is stored in the TaoNode status and exported as a Prometheus gauge.
func estimateMonthlyCostUSD(tn *taov1alpha1.TaoNode) float64 {
	var total float64

	// Compute cost.
	cpuReq := resourceValue(tn.Spec.Resources.Requests, corev1.ResourceCPU, "0")
	memReq := resourceGiB(tn.Spec.Resources.Requests, corev1.ResourceMemory)

	total += cpuReq * cpuCostPerCorePerHour * hoursPerMonth
	total += memReq * memCostPerGiBPerHour * hoursPerMonth

	// Storage cost.
	storageGiB := quantityToGiB(tn.Spec.ChainStorage.Size)
	total += storageGiB * storageCostPerGiBPerMonth

	// GPU cost (if configured).
	if tn.Spec.GPU != nil {
		total += float64(tn.Spec.GPU.Count) * gpuCostPerGPUPerHour * hoursPerMonth
		// Spot discount (roughly 70% cheaper).
		if tn.Spec.GPU.SpotTolerant {
			total -= float64(tn.Spec.GPU.Count) * gpuCostPerGPUPerHour * hoursPerMonth * 0.70
		}
	}

	return total
}

// formatMonthlyCost formats a float64 USD estimate as a human-readable string.
func formatMonthlyCost(usd float64) string {
	if usd >= 1000 {
		return fmt.Sprintf("$%.0f/mo", usd)
	}
	return fmt.Sprintf("$%.2f/mo", usd)
}

// updateFinOpsStatus writes the cost estimate into TaoNode status and metrics.
func updateFinOpsStatus(tn *taov1alpha1.TaoNode) {
	cost := estimateMonthlyCostUSD(tn)
	tn.Status.Resources.EstimatedMonthlyUSD = formatMonthlyCost(cost)
	estimatedMonthlyCost.WithLabelValues(tn.Name, tn.Namespace, tn.Spec.Network).Set(cost)
}

// resourceValue extracts a float64 core count from a ResourceList.
func resourceValue(reqs corev1.ResourceList, res corev1.ResourceName, defaultVal string) float64 {
	if reqs == nil {
		return parseQuantityFloat(resource.MustParse(defaultVal))
	}
	if q, ok := reqs[res]; ok {
		return parseQuantityFloat(q)
	}
	return parseQuantityFloat(resource.MustParse(defaultVal))
}

// resourceGiB extracts memory in GiB from a ResourceList.
func resourceGiB(reqs corev1.ResourceList, res corev1.ResourceName) float64 {
	if reqs == nil {
		return 0
	}
	if q, ok := reqs[res]; ok {
		return float64(q.Value()) / (1024 * 1024 * 1024)
	}
	return 0
}

// quantityToGiB converts a resource.Quantity to GiB as a float64.
func quantityToGiB(q resource.Quantity) float64 {
	return float64(q.Value()) / (1024 * 1024 * 1024)
}

// parseQuantityFloat converts a resource.Quantity to float64 (CPU cores).
func parseQuantityFloat(q resource.Quantity) float64 {
	return float64(q.MilliValue()) / 1000.0
}
