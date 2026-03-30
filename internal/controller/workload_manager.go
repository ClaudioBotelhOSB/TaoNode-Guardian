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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ptr "k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
//
// Note: ws:9945 is removed (Substrate post-v1.0 unified RPC — FIX #2).
// metrics:9615 is added for Prometheus scraping.
func (r *TaoNodeReconciler) ensureHeadlessService(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tn.Name + "-headless",
			Namespace: tn.Namespace,
			Labels:    r.labelsForNode(tn),
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		svc.Spec.ClusterIP = "None"
		svc.Spec.Selector = r.labelsForNode(tn)
		svc.Spec.Ports = []corev1.ServicePort{
			{Name: "p2p", Port: 30333, Protocol: corev1.ProtocolTCP},
			{Name: "rpc", Port: 9944, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", Port: 9615, Protocol: corev1.ProtocolTCP},
		}
		svc.Spec.PublishNotReadyAddresses = true
		return ctrl.SetControllerReference(tn, svc, r.Scheme)
	})
	return err
}

// ensureNodeWorkload reconciles the TaoNode StatefulSet.
//
// FIX #4: VolumeClaimTemplate drift and structural immutable field changes
// (serviceName, selector) are handled via orphan-delete-recreate rather than
// surfacing an error to the caller. If the StatefulSet is already being
// deleted, the Create is attempted immediately and retried on the next reconcile.
func (r *TaoNodeReconciler) ensureNodeWorkload(ctx context.Context, tn *taov1alpha1.TaoNode) error {
	desired, err := r.desiredStatefulSetForNode(tn)
	if err != nil {
		return fmt.Errorf("build desired StatefulSet: %w", err)
	}
	key := client.ObjectKeyFromObject(desired)

	current := &appsv1.StatefulSet{}
	if err := r.Get(ctx, key, current); err != nil {
		if apierrors.IsNotFound(err) {
			return r.Create(ctx, desired)
		}
		return err
	}

	// FIX #4: orphan-delete + recreate for immutable field changes.
	if vctDriftDetected(current, desired) || statefulSetStructuralChanges(current, desired) {
		if current.DeletionTimestamp.IsZero() {
			orphan := metav1.DeletePropagationOrphan
			if err := r.Delete(ctx, current, &client.DeleteOptions{PropagationPolicy: &orphan}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("orphan-delete StatefulSet %s: %w", current.Name, err)
			}
		}
		desired.ResourceVersion = ""
		return r.Create(ctx, desired)
	}

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &appsv1.StatefulSet{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		if !statefulSetNeedsMutableUpdate(latest, desired) {
			return nil
		}
		copyMutableStatefulSetFields(latest, desired)
		return r.Update(ctx, latest)
	})
}

func (r *TaoNodeReconciler) desiredStatefulSetForNode(
	tn *taov1alpha1.TaoNode,
) (*appsv1.StatefulSet, error) {
	replicas := int32(1)
	labels := r.labelsForNode(tn)
	image := r.imageForNode(tn)

	// FIX #3: resolve canonical --chain value (e.g. "testnet" → "test_finney").
	chainSpec, needsExternalSpec := ResolveChainSpec(tn.Spec.Network)

	// FIX #2: build CLI args with unified RPC — no deprecated --ws-port.
	cliArgs := (&SubstrateArgs{
		ChainSpec:      chainSpec,
		Role:           string(tn.Spec.Role),
		RPCPort:        9944,
		P2PPort:        30333,
		PrometheusPort: 9615,
		NodeName:       tn.Name,
	}).Build()

	nodeResources := tn.Spec.Resources
	if tn.Spec.GPU != nil {
		gpuReqs := buildGPUResourceRequirements(tn.Spec.GPU)
		nodeResources = mergeResourceRequirements(nodeResources, gpuReqs)
	}

	var affinity *corev1.Affinity
	if tn.Spec.GPU != nil {
		nodeAffinity := buildGPUNodeAffinity(tn.Spec.GPU)
		if nodeAffinity != nil {
			affinity = &corev1.Affinity{NodeAffinity: nodeAffinity}
		}
	}

	tolerations := append([]corev1.Toleration{}, tn.Spec.Tolerations...)
	tolerations = append(tolerations, buildGPUTolerations(tn.Spec.GPU)...)

	// Annotate pod template with a hash of CLI args so that argument changes
	// trigger rolling updates even when no other StatefulSet field changed.
	annotations := r.annotationsForNode(tn)
	annotations["tao.guardian.io/args-hash"] = hashArgs(cliArgs)

	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tn.Name,
			Namespace: tn.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: tn.Name + "-headless",
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:   chainDataVolume,
						Labels: labels,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						StorageClassName: ptr.To(tn.Spec.ChainStorage.StorageClass),
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: tn.Spec.ChainStorage.Size,
							},
						},
					},
				},
			},
		},
	}

	if tn.Spec.Role == taov1alpha1.RoleValidator {
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type: appsv1.OnDeleteStatefulSetStrategyType,
		}
	} else {
		sts.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{
			Type: appsv1.RollingUpdateStatefulSetStrategyType,
		}
	}

	sts.Spec.Template = corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:        ptr.To(true),
				RunAsUser:           ptr.To(subtensorUID),
				FSGroup:             ptr.To(subtensorGID),
				FSGroupChangePolicy: ptrFSGroupChangePolicy(corev1.FSGroupChangeOnRootMismatch),
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			ServiceAccountName:            tn.Name + "-sa",
			TerminationGracePeriodSeconds: ptr.To(int64(120)),
			// FIX #1: init container pre-chowns /data so main container needs zero caps.
			InitContainers: []corev1.Container{buildInitContainer(image)},
			Containers:     r.buildContainers(tn, image, nodeResources, cliArgs, needsExternalSpec),
			Volumes:        r.buildVolumes(tn, needsExternalSpec),
			Tolerations:    tolerations,
			NodeSelector:   tn.Spec.NodeSelector,
			Affinity:       affinity,
		},
	}

	if err := ctrl.SetControllerReference(tn, sts, r.Scheme); err != nil {
		return nil, err
	}
	return sts, nil
}

// statefulSetStructuralChanges returns true if serviceName or selector changed.
// Both fields are immutable; changes require orphan-delete-recreate (FIX #4).
func statefulSetStructuralChanges(current, desired *appsv1.StatefulSet) bool {
	return current.Spec.ServiceName != desired.Spec.ServiceName ||
		!equality.Semantic.DeepEqual(current.Spec.Selector, desired.Spec.Selector)
}

func statefulSetNeedsMutableUpdate(current, desired *appsv1.StatefulSet) bool {
	return !equality.Semantic.DeepEqual(current.Labels, desired.Labels) ||
		!equality.Semantic.DeepEqual(current.Spec.Replicas, desired.Spec.Replicas) ||
		!equality.Semantic.DeepEqual(current.Spec.UpdateStrategy, desired.Spec.UpdateStrategy) ||
		!equality.Semantic.DeepEqual(current.Spec.Template, desired.Spec.Template)
}

func copyMutableStatefulSetFields(dst, src *appsv1.StatefulSet) {
	dst.Labels = src.Labels
	dst.Spec.Replicas = src.Spec.Replicas
	dst.Spec.UpdateStrategy = src.Spec.UpdateStrategy
	dst.Spec.Template = src.Spec.Template
}

// buildContainers returns the container list for the TaoNode pod:
//  1. Main node container — built by buildMainContainer (FIX #1 + #2 + #3)
//  2. chain-probe sidecar — exposes :9616/health for probeChainHealth
//  3. metrics sidecar — optional, when monitoring is enabled
func (r *TaoNodeReconciler) buildContainers(
	tn *taov1alpha1.TaoNode,
	image string,
	resources corev1.ResourceRequirements,
	cliArgs []string,
	needsExternalSpec bool,
) []corev1.Container {
	containers := []corev1.Container{
		buildMainContainer(image, resources, cliArgs, needsExternalSpec),
		{
			Name:  "chain-probe",
			Image: "ghcr.io/claudiobotelhosb/taonode-guardian-probe:latest",
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

	if tn.Spec.Monitoring != nil && tn.Spec.Monitoring.Enabled {
		containers = append(containers, r.buildMetricsSidecar(tn))
	}

	return containers
}

// buildVolumes returns extra volumes for the TaoNode pod.
// The chain-data volume comes from the VolumeClaimTemplate, not listed here.
func (r *TaoNodeReconciler) buildVolumes(tn *taov1alpha1.TaoNode, needsExternalSpec bool) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name:         "probe-tmp",
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		},
	}

	// FIX #3: for unknown networks the caller must create a ConfigMap named
	// <taonode-name>-chainspec with key raw_spec.json. The main container
	// mounts it read-only at /data/chain-spec/ (see buildMainContainer).
	if needsExternalSpec {
		volumes = append(volumes, corev1.Volume{
			Name: "chain-spec",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: tn.Name + "-chainspec",
					},
				},
			},
		})
	}

	if tn.Spec.Validator != nil && tn.Spec.Validator.HotKeySecret != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "validator-hotkey",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  tn.Spec.Validator.HotKeySecret,
					DefaultMode: ptr.To(int32(0400)),
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
