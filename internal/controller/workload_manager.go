// internal/controller/workload_manager.go
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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	ptr "k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

// ensureServiceAccount creates a per-TaoNode ServiceAccount if it does not exist.
// The StatefulSet pods run under this SA, scoped with minimal RBAC.
func (r *TaoNodeReconciler) ensureServiceAccount(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tn.Name + "-sa",
			Namespace: tn.Namespace,
			Labels:    r.labelsForNode(tn),
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sa, func() error {
		return ctrl.SetControllerReference(tn, sa, r.Scheme)
	})
	return err
}

// ensureHeadlessService creates the headless Service that provides stable DNS
// for the StatefulSet pods. The StatefulSet references this Service via
// spec.serviceName, which enables {pod}.{service}.{namespace}.svc.cluster.local DNS.
func (r *TaoNodeReconciler) ensureHeadlessService(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tn.Name + "-headless",
			Namespace: tn.Namespace,
			Labels:    r.labelsForNode(tn),
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.ClusterIP = "None" // headless
		svc.Spec.Selector = r.labelsForNode(tn)
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "p2p", Port: 30333, Protocol: corev1.ProtocolTCP},
			{Name: "rpc", Port: 9944, Protocol: corev1.ProtocolTCP},
			{Name: "ws", Port: 9945, Protocol: corev1.ProtocolTCP},
		}
		svc.Spec.PublishNotReadyAddresses = true // required for StatefulSet bootstrapping
		return ctrl.SetControllerReference(tn, svc, r.Scheme)
	})
	return err
}

// ensureNodeWorkload creates or updates the TaoNode's StatefulSet.
//
// StatefulSet (not a bare Pod) is correct for blockchain nodes because:
//  1. Stable hostname → peers reconnect after restarts
//  2. Ordered rollout → validators upgrade one at a time (OnDelete strategy)
//  3. Stable PVC binding → chain data survives rescheduling
//  4. Headless Service → direct pod DNS for RPC probes
func (r *TaoNodeReconciler) ensureNodeWorkload(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tn.Name,
			Namespace: tn.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sts, func() error {
		replicas := int32(1)
		sts.Labels = r.labelsForNode(tn)
		sts.Spec.Replicas = &replicas
		sts.Spec.ServiceName = tn.Name + "-headless"
		sts.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: r.labelsForNode(tn),
		}

		// Validators use OnDelete — human must explicitly trigger pod deletion to
		// apply an update. This prevents accidental double-signing during upgrades.
		if tn.Spec.Role == taov1alpha1.RoleValidator {
			sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.OnDeleteStatefulSetStrategyType,
			}
		} else {
			sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
			}
		}

		// Build affinity for GPU nodes if GPU is requested.
		var affinity *corev1.Affinity
		if tn.Spec.GPU != nil {
			nodeAffinity := buildGPUNodeAffinity(tn.Spec.GPU)
			if nodeAffinity != nil {
				affinity = &corev1.Affinity{NodeAffinity: nodeAffinity}
			}
		}

		// Merge user tolerations with GPU tolerations.
		tolerations := append(tn.Spec.Tolerations, buildGPUTolerations(tn.Spec.GPU)...)

		sts.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{
				Labels:      r.labelsForNode(tn),
				Annotations: r.annotationsForNode(tn),
			},
			Spec: corev1.PodSpec{
				SecurityContext: &corev1.PodSecurityContext{
					RunAsNonRoot: ptr.To(true),
					RunAsUser:    ptr.To(int64(1000)),
					FSGroup:      ptr.To(int64(1000)),
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
				ServiceAccountName:            tn.Name + "-sa",
				TerminationGracePeriodSeconds: ptr.To(int64(120)), // chain nodes need time to flush
				InitContainers:                r.buildInitContainers(tn),
				Containers:                    r.buildContainers(tn),
				Volumes:                       r.buildVolumes(tn),
				Tolerations:                   tolerations,
				NodeSelector:                  tn.Spec.NodeSelector,
				Affinity:                      affinity,
			},
		}

		// VolumeClaimTemplate for chain data — managed by StatefulSet lifecycle.
		// The PVC name becomes: chain-data-{sts-name}-{ordinal}
		sts.Spec.VolumeClaimTemplates = []corev1.PersistentVolumeClaim{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name:   "chain-data",
					Labels: r.labelsForNode(tn),
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					StorageClassName: &tn.Spec.ChainStorage.StorageClass,
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: tn.Spec.ChainStorage.Size,
						},
					},
				},
			},
		}

		return ctrl.SetControllerReference(tn, sts, r.Scheme)
	})
	return err
}

// buildContainers returns the container list for the TaoNode pod:
//  1. Main node container (subtensor/miner/validator binary)
//  2. chain-probe sidecar (exposes :9616/health for probeChainHealth)
//  3. metrics sidecar (optional, when monitoring is enabled)
func (r *TaoNodeReconciler) buildContainers(tn *taov1alpha1.TaoNode) []corev1.Container {
	// Merge user resources with GPU resources.
	nodeResources := tn.Spec.Resources
	if tn.Spec.GPU != nil {
		gpuReqs := buildGPUResourceRequirements(tn.Spec.GPU)
		nodeResources = mergeResourceRequirements(nodeResources, gpuReqs)
	}

	containers := []corev1.Container{
		{
			Name:  "node",
			Image: r.imageForNode(tn),
			Args:  buildNodeArgs(tn),
			Ports: []corev1.ContainerPort{
				{Name: "p2p", ContainerPort: 30333, Protocol: corev1.ProtocolTCP},
				{Name: "rpc", ContainerPort: 9944, Protocol: corev1.ProtocolTCP},
				{Name: "ws", ContainerPort: 9945, Protocol: corev1.ProtocolTCP},
			},
			Resources: nodeResources,
			VolumeMounts: []corev1.VolumeMount{
				{Name: "chain-data", MountPath: "/data/chain"},
			},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(false), // chain node writes to /data
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			// Liveness: restarts if RPC stops responding entirely.
			LivenessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(9944),
					},
				},
				InitialDelaySeconds: 120, // chain nodes are slow to start
				PeriodSeconds:       30,
				TimeoutSeconds:      10,
				FailureThreshold:    5, // tolerate temporary RPC hangs
			},
			// Readiness: removed from Service endpoints when stuck.
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/health",
						Port: intstr.FromInt32(9944),
					},
				},
				InitialDelaySeconds: 30,
				PeriodSeconds:       15,
				TimeoutSeconds:      5,
				FailureThreshold:    3,
			},
		},
		// chain-probe sidecar: lightweight Go binary that translates
		// Subtensor JSON-RPC calls into a structured GET /health JSON response.
		// The controller's probeChainHealth() calls this, not the node RPC directly.
		{
			Name:  "chain-probe",
			Image: "ghcr.io/ClaudioBotelhOSB/taonode-guardian-probe:latest",
			Ports: []corev1.ContainerPort{
				{Name: "probe", ContainerPort: chainProbePort, Protocol: corev1.ProtocolTCP},
			},
			Env: []corev1.EnvVar{
				{Name: "NODE_RPC_URL", Value: "http://localhost:9944"},
				{Name: "PROBE_PORT", Value: fmt.Sprintf("%d", chainProbePort)},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				ReadOnlyRootFilesystem:   ptr.To(true),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			ReadinessProbe: &corev1.Probe{
				ProbeHandler: corev1.ProbeHandler{
					HTTPGet: &corev1.HTTPGetAction{
						Path: "/readyz",
						Port: intstr.FromInt32(chainProbePort),
					},
				},
				InitialDelaySeconds: 5,
				PeriodSeconds:       10,
			},
		},
	}

	// Optional metrics sidecar (Prometheus node-exporter style).
	if tn.Spec.Monitoring != nil && tn.Spec.Monitoring.Enabled {
		containers = append(containers, r.buildMetricsSidecar(tn))
	}

	return containers
}

// buildInitContainers returns init containers for the TaoNode pod.
// Currently: a data-permission-fix container that ensures /data/chain is
// writable by the non-root user (uid 1000).
func (r *TaoNodeReconciler) buildInitContainers(tn *taov1alpha1.TaoNode) []corev1.Container {
	return []corev1.Container{
		{
			Name:  "init-chain-data",
			Image: "busybox:1.36",
			Command: []string{
				"sh", "-c",
				"chown -R 1000:1000 /data/chain && chmod 750 /data/chain",
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "chain-data", MountPath: "/data/chain"},
			},
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:                ptr.To(int64(0)), // runs as root to chown
				AllowPrivilegeEscalation: ptr.To(false),
			},
		},
	}
}

// buildVolumes returns extra volumes for the TaoNode pod.
// The chain-data volume comes from the VolumeClaimTemplate, not listed here.
func (r *TaoNodeReconciler) buildVolumes(tn *taov1alpha1.TaoNode) []corev1.Volume {
	volumes := []corev1.Volume{
		// Temp directory for the chain-probe sidecar (read-only root filesystem).
		{
			Name: "probe-tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Mount validator hotkey secret if configured.
	if tn.Spec.Validator != nil && tn.Spec.Validator.HotKeySecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "validator-hotkey",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  tn.Spec.Validator.HotKeySecret,
					DefaultMode: ptr.To(int32(0400)), // read-only, owner only
				},
			},
		})
	}

	return volumes
}

// buildMetricsSidecar returns the Prometheus metrics exporter sidecar container.
// It exposes Subtensor node metrics on the configured monitoring port.
func (r *TaoNodeReconciler) buildMetricsSidecar(tn *taov1alpha1.TaoNode) corev1.Container {
	port := int32(9615)
	if tn.Spec.Monitoring != nil {
		port = tn.Spec.Monitoring.Port
	}
	return corev1.Container{
		Name:  "metrics",
		Image: "prom/node-exporter:v1.8.1",
		Ports: []corev1.ContainerPort{
			{Name: "metrics", ContainerPort: port, Protocol: corev1.ProtocolTCP},
		},
		Args: []string{
			"--web.listen-address=:" + fmt.Sprintf("%d", port),
			"--collector.disable-defaults",
			"--collector.textfile",
			"--collector.textfile.directory=/var/lib/node-exporter",
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("25m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
	}
}

// buildNodeArgs constructs the command-line arguments for the Subtensor binary
// based on the TaoNode spec.
func buildNodeArgs(tn *taov1alpha1.TaoNode) []string {
	args := []string{
		"--base-path=/data/chain",
		"--rpc-port=9944",
		"--ws-port=9945",
		"--port=30333",
	}

	switch tn.Spec.Network {
	case "mainnet":
		args = append(args, "--chain=finney")
	case "testnet":
		args = append(args, "--chain=test")
	case "devnet":
		args = append(args, "--chain=local", "--dev")
	}

	if tn.Spec.Role == taov1alpha1.RoleValidator {
		args = append(args, "--validator")
	}

	return args
}

// ensureServiceMonitor creates a Prometheus ServiceMonitor for the TaoNode
// using an unstructured object to avoid importing the prometheus-operator CRD
// as a direct dependency. If the ServiceMonitor CRD is not installed, the
// error is logged and ignored (non-fatal).
func (r *TaoNodeReconciler) ensureServiceMonitor(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	if tn.Spec.Monitoring == nil || !tn.Spec.Monitoring.Enabled {
		return nil
	}

	sm := &unstructured.Unstructured{}
	sm.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "monitoring.coreos.com",
		Version: "v1",
		Kind:    "ServiceMonitor",
	})
	sm.SetName(tn.Name)
	sm.SetNamespace(tn.Namespace)

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, sm, func() error {
		sm.SetLabels(r.labelsForNode(tn))
		sm.Object["spec"] = map[string]interface{}{
			"selector": map[string]interface{}{
				"matchLabels": convertLabels(r.labelsForNode(tn)),
			},
			"endpoints": []interface{}{
				map[string]interface{}{
					"port":     "metrics",
					"interval": "30s",
					"path":     "/metrics",
				},
			},
		}
		return ctrl.SetControllerReference(tn, sm, r.Scheme)
	})
	if err != nil {
		return fmt.Errorf("create or update ServiceMonitor: %w", err)
	}
	return nil
}

// convertLabels converts map[string]string to map[string]interface{} for
// use in unstructured object construction.
func convertLabels(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
