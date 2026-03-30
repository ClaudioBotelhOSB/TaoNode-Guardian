// internal/controller/statefulset_builder.go
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

// Package controller contains pure builder functions for the TaoNode StatefulSet.
//
// This file resolves 4 P0 integration failures between the Go controller and
// the Substrate/Subtensor Docker image (documented in ADR-statefulset-redesign.md):
//
//   FIX #1 – SecurityContext: InitContainer + surgical CAP_CHOWN pattern
//   FIX #2 – CLI Args: Substrate post-v1.0 unified RPC (no --ws-port)
//   FIX #3 – Chain Spec: canonical name registry (testnet → test_finney)
//   FIX #4 – VCT Immutability: orphan-delete-recreate helper (vctDriftDetected)

package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
	ptr "k8s.io/utils/ptr"
)

// =============================================================================
// FIX #3 — Chain Spec Registry
// =============================================================================
// Subtensor accepts only built-in spec names or a FILE PATH for --chain.
// Passing "testnet" causes the binary to look for a file named "testnet" and
// fail with OS Error 2. This map is the single source of truth.

// SubtensorChainSpec maps CRD network values to Subtensor-native built-in names.
var SubtensorChainSpec = map[string]string{
	"mainnet":     "finney",
	"finney":      "finney",
	"testnet":     "test_finney",
	"test_finney": "test_finney",
	"test-finney": "test_finney",
	"local":       "local",
	"dev":         "dev",
	"devnet":      "dev",
}

// ResolveChainSpec translates a CRD network name into the correct --chain value.
// Returns the spec string and whether an external raw_spec.json ConfigMap is required.
// For all built-in networks (mainnet, testnet, devnet), needsExternalSpec is false.
func ResolveChainSpec(network string) (spec string, needsExternalSpec bool) {
	normalized := strings.ToLower(strings.TrimSpace(network))
	if builtIn, ok := SubtensorChainSpec[normalized]; ok {
		return builtIn, false
	}
	// Unknown network: controller expects a ConfigMap <taonode-name>-chainspec
	// with key raw_spec.json, mounted at /data/chain-spec/.
	return "/data/chain-spec/raw_spec.json", true
}

// =============================================================================
// FIX #2 — Versioned CLI Argument Builder
// =============================================================================
// Substrate post-Polkadot v1.0 removed --ws-port, --ws-external, --rpc-cors.
// All RPC (HTTP + WebSocket) is unified on a single --rpc-port.
// The binary exits immediately on unknown flags — no deprecation warnings.

// SubstrateArgs encapsulates all CLI knowledge for the Subtensor binary.
// Callers never construct CLI strings directly.
type SubstrateArgs struct {
	ChainSpec      string
	Role           string // "validator", "miner", "subtensor", "archive"
	RPCPort        int32
	P2PPort        int32
	PrometheusPort int32
	NodeName       string
	ExtraArgs      []string // escape hatch for future flags
}

// Build produces the complete argument vector for the Subtensor container.
func (s *SubstrateArgs) Build() []string {
	args := []string{
		fmt.Sprintf("--chain=%s", s.ChainSpec),
		"--base-path=/data",
		// FIX #2: unified RPC — replaces removed --ws-port / --ws-external / --rpc-cors
		fmt.Sprintf("--rpc-port=%d", s.RPCPort),
		"--rpc-external",    // bind 0.0.0.0 (required in K8s pods)
		"--rpc-methods=Safe", // safe subset only
		fmt.Sprintf("--port=%d", s.P2PPort),
		fmt.Sprintf("--prometheus-port=%d", s.PrometheusPort),
		"--prometheus-external",
	}

	switch strings.ToLower(s.Role) {
	case "validator":
		args = append(args, "--validator")
	case "archive":
		args = append(args, "--pruning=archive", "--sync=full")
	// "subtensor", "miner", "full": default full-node behavior, no extra flags
	}

	if s.NodeName != "" {
		args = append(args, fmt.Sprintf("--name=%s", s.NodeName))
	}

	return append(args, s.ExtraArgs...)
}

// =============================================================================
// FIX #1 — SecurityContext Constants
// =============================================================================

const (
	// subtensorUID / subtensorGID: the UID/GID the main container runs as.
	// The init container chowns /data to this UID so the entrypoint's own
	// chown becomes a no-op (no CAP_CHOWN needed by the main container).
	subtensorUID int64 = 1000
	subtensorGID int64 = 1000

	// chainDataVolume is the VolumeClaimTemplate name shared across init, main,
	// and future sidecar containers.
	chainDataVolume = "chain-data"
)

// buildInitContainer creates the "volume-permissions" init container (FIX #1).
//
// Architecture:
//   - Runs as root (UID 0) for ~200 ms
//   - Holds ONLY CAP_CHOWN + CAP_DAC_OVERRIDE
//   - Chowns /data to subtensorUID:subtensorGID
//   - Main container's entrypoint chown becomes a no-op → zero capabilities needed
//
// Uses the same image as the main container to avoid an extra pull layer.
func buildInitContainer(image string) corev1.Container {
	return corev1.Container{
		Name:    "volume-permissions",
		Image:   image,
		Command: []string{"/bin/sh", "-c"},
		Args: []string{
			fmt.Sprintf("mkdir -p /data/chains && chown -R %d:%d /data", subtensorUID, subtensorGID),
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: chainDataVolume, MountPath: "/data"},
		},
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptr.To[int64](0),
			RunAsGroup:               ptr.To[int64](0),
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
				Add:  []corev1.Capability{"CHOWN", "DAC_OVERRIDE"},
			},
		},
		Resources: corev1.ResourceRequirements{
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	}
}

// buildMainContainer creates the primary Subtensor container (FIX #1 + #2 + #3).
//
// Security posture: non-root, zero capabilities, read-only root FS.
// This is achievable because buildInitContainer pre-established /data ownership.
func buildMainContainer(
	image string,
	resources corev1.ResourceRequirements,
	cliArgs []string,
	needsExternalSpec bool,
) corev1.Container {
	volumeMounts := []corev1.VolumeMount{
		{Name: chainDataVolume, MountPath: "/data"},
	}
	if needsExternalSpec {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "chain-spec",
			MountPath: "/data/chain-spec",
			ReadOnly:  true,
		})
	}

	return corev1.Container{
		Name:  "node",
		Image: image,
		Args:  cliArgs,
		Ports: []corev1.ContainerPort{
			{Name: "rpc", ContainerPort: 9944, Protocol: corev1.ProtocolTCP},
			{Name: "p2p", ContainerPort: 30333, Protocol: corev1.ProtocolTCP},
			{Name: "metrics", ContainerPort: 9615, Protocol: corev1.ProtocolTCP},
		},
		Resources:    resources,
		VolumeMounts: volumeMounts,
		SecurityContext: &corev1.SecurityContext{
			RunAsUser:                ptr.To(subtensorUID),
			RunAsGroup:               ptr.To(subtensorGID),
			RunAsNonRoot:             ptr.To(true),
			AllowPrivilegeEscalation: ptr.To(false),
			ReadOnlyRootFilesystem:   ptr.To(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		// StartupProbe: Substrate nodes take up to 30 min for initial chain sync.
		// 180 failures × 10 s = 1800 s = 30 min before liveness takes over.
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromString("rpc"),
				},
			},
			InitialDelaySeconds: 10,
			PeriodSeconds:       10,
			FailureThreshold:    180,
		},
		// LivenessProbe: P2P connectivity as the liveness signal.
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				TCPSocket: &corev1.TCPSocketAction{
					Port: intstr.FromString("p2p"),
				},
			},
			InitialDelaySeconds: 60,
			PeriodSeconds:       30,
			TimeoutSeconds:      5,
			FailureThreshold:    5,
		},
		// ReadinessProbe: Substrate JSON-RPC /health endpoint.
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   "/health",
					Port:   intstr.FromString("rpc"),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 30,
			PeriodSeconds:       15,
			TimeoutSeconds:      5,
			FailureThreshold:    3,
		},
	}
}

// =============================================================================
// FIX #4 — VCT Immutability Helper
// =============================================================================

// vctDriftDetected returns true if the VolumeClaimTemplate specs differ between
// the existing and desired StatefulSet. Used to trigger orphan-delete-recreate.
func vctDriftDetected(existing, desired *appsv1.StatefulSet) bool {
	if len(existing.Spec.VolumeClaimTemplates) != len(desired.Spec.VolumeClaimTemplates) {
		return true
	}
	for i := range existing.Spec.VolumeClaimTemplates {
		if !equality.Semantic.DeepEqual(
			existing.Spec.VolumeClaimTemplates[i].Spec,
			desired.Spec.VolumeClaimTemplates[i].Spec,
		) {
			return true
		}
	}
	return false
}

// =============================================================================
// Utilities
// =============================================================================

// hashArgs creates a deterministic 16-char hex hash of CLI arguments.
// Used as a PodTemplate annotation so that argument changes trigger rolling updates
// even when no other StatefulSet field changed (standard Helm/ArgoCD pattern).
func hashArgs(args []string) string {
	sorted := make([]string, len(args))
	copy(sorted, args)
	sort.Strings(sorted)
	h := sha256.Sum256([]byte(strings.Join(sorted, "\x00")))
	return hex.EncodeToString(h[:8])
}

// ptrFSGroupChangePolicy returns a pointer to a PodFSGroupChangePolicy.
func ptrFSGroupChangePolicy(p corev1.PodFSGroupChangePolicy) *corev1.PodFSGroupChangePolicy {
	return &p
}
