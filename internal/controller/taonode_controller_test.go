// internal/controller/taonode_controller_test.go
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

package controller_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
)

var _ = Describe("TaoNodeReconciler", func() {
	const (
		// testNamespace isolates resources created by these tests.
		testNamespace = "taonode-ctrl-test"

		// timeout is the outer bound passed to Eventually.
		//
		// Why 30 s? Two full reconcile cycles must complete before the StatefulSet
		// exists:
		//   Cycle 1 — controller sees TaoNode without finalizer → adds finalizer,
		//             returns immediately (controller-runtime re-queues).
		//   Cycle 2 — Step 3 creates SA + Service + StatefulSet; Step 4 probe
		//             fails (no pods in envtest) → status updated, requeue 30 s.
		//
		// The informer cache can add ~1 s of watch latency on top of that.
		// 30 s is conservative and prevents flakiness in slow CI environments.
		timeout = 30 * time.Second

		// interval is the polling period for Eventually.
		// 100 ms gives fast feedback without flooding the envtest API server.
		interval = 100 * time.Millisecond

		// finalizerName must match the private constant in taonode_controller.go.
		// It is copied here because the test package is external (controller_test)
		// and cannot access unexported identifiers.
		finalizerName = "tao.guardian.io/finalizer"
	)

	ctx := context.Background()

	// Ensure the test namespace exists before each spec.
	// AlreadyExists is treated as success — the namespace is still present.
	BeforeEach(func() {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: testNamespace},
		}
		err := k8sClient.Create(ctx, ns)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			Expect(err).NotTo(HaveOccurred())
		}
	})

	// ── Spec builders ─────────────────────────────────────────────────────────

	// newMinerSpec returns a minimal, valid TaoNode spec for a miner.
	//
	// "devnet" + "miner" is the simplest combination:
	//   - devnet requires no mainnet/testnet bootstrap args
	//   - miner skips the validator singleton Lease (ensureValidatorSingleton)
	//   - no Monitoring → ServiceMonitor CRD is not required in envtest
	//   - no Analytics / AIAdvisor → ClickHouse and Ollama are not required
	newMinerSpec := func(name string) *taov1alpha1.TaoNode {
		return &taov1alpha1.TaoNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: testNamespace,
			},
			Spec: taov1alpha1.TaoNodeSpec{
				Network:  "devnet",
				SubnetID: 1,
				Role:     taov1alpha1.RoleMiner,
				Image:    "opentensor/subtensor:latest",
				ChainStorage: taov1alpha1.ChainStorageSpec{
					StorageClass: "standard",
					Size:         resource.MustParse("1Gi"),
				},
				SyncPolicy: taov1alpha1.SyncPolicySpec{
					MaxBlockLag:          100,
					RecoveryStrategy:     "restart",
					MaxRestartAttempts:   3,
					ProbeIntervalSeconds: 30,
					SyncTimeoutMinutes:   60,
				},
			},
		}
	}

	// newValidatorSpec extends newMinerSpec with the validator role and key config.
	newValidatorSpec := func(name string) *taov1alpha1.TaoNode {
		tn := newMinerSpec(name)
		tn.Spec.Role = taov1alpha1.RoleValidator
		tn.Spec.Validator = &taov1alpha1.ValidatorSpec{
			// Secret does not need to exist for StatefulSet spec assertions —
			// no pods are scheduled in envtest.
			HotKeySecret:            "validator-hotkey-placeholder",
			SlashingProtection:      true,
			MaxConcurrentValidators: 1,
		}
		return tn
	}

	// waitAndDeleteTaoNode is a helper used in DeferCleanup to delete a TaoNode
	// and wait for the controller to remove the finalizer and garbage-collect it.
	// It is safe to call even if the TaoNode was already deleted by the test.
	waitAndDeleteTaoNode := func(tn *taov1alpha1.TaoNode) {
		GinkgoHelper()
		_ = k8sClient.Delete(ctx, tn) // ignore NotFound — test may have deleted it
		Eventually(func(g Gomega) {
			err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tn), &taov1alpha1.TaoNode{})
			g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
				"TaoNode %s/%s should be fully deleted (finalizer cleared) by now",
				tn.Namespace, tn.Name)
		}, timeout, interval).Should(Succeed())
	}

	// ══════════════════════════════════════════════════════════════════════════
	// Scenario 1 — Miner: core provisioning assertions
	// ══════════════════════════════════════════════════════════════════════════
	Describe("provisioning a miner TaoNode", func() {
		var tn *taov1alpha1.TaoNode

		BeforeEach(func() {
			tn = newMinerSpec("miner-sn1")
			Expect(k8sClient.Create(ctx, tn)).To(Succeed(),
				"failed to create TaoNode %s/%s", tn.Namespace, tn.Name)
			DeferCleanup(waitAndDeleteTaoNode, tn)
		})

		It("adds the protection finalizer on the first reconcile cycle", func() {
			// The first reconcile only adds the finalizer and returns.
			// This asserts that the controller has processed the TaoNode at least once.
			Eventually(func(g Gomega) {
				current := &taov1alpha1.TaoNode{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tn), current)).To(Succeed())
				g.Expect(current.Finalizers).To(ContainElement(finalizerName),
					"finalizer %q must be present after first reconcile", finalizerName)
			}, timeout, interval).Should(Succeed())
		})

		It("creates a StatefulSet with the same name as the TaoNode", func() {
			// StatefulSet name == tn.Name — see ensureNodeWorkload (workload_manager.go:89).
			Eventually(func(g Gomega) {
				sts := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("sets exactly 1 replica on the StatefulSet", func() {
			// replicas is hard-coded to 1 in ensureNodeWorkload (workload_manager.go:95).
			// Blockchain nodes run as singletons — multiple replicas would cause
			// diverging chain state and peer-count inflation.
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.Replicas).NotTo(BeNil(),
					"Spec.Replicas must be explicitly set (not nil) to guarantee exactly 1 pod")
				g.Expect(*sts.Spec.Replicas).To(Equal(int32(1)))
			}, timeout, interval).Should(Succeed())
		})

		It("uses RollingUpdate strategy for a miner", func() {
			// Miners tolerate rolling restarts; validators use OnDelete to prevent
			// double-signing (tested in the validator scenario below).
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.UpdateStrategy.Type).To(
					Equal(appsv1.RollingUpdateStatefulSetStrategyType))
			}, timeout, interval).Should(Succeed())
		})

		It("creates a headless Service for stable StatefulSet pod DNS", func() {
			// Service name == tn.Name + "-headless" (workload_manager.go:63).
			// The StatefulSet's spec.serviceName references this Service, enabling
			// {pod}.{service}.{namespace}.svc.cluster.local DNS for peer discovery.
			svc := &corev1.Service{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name + "-headless",
					Namespace: testNamespace,
				}, svc)).To(Succeed())
				// ClusterIP "None" is the definition of a headless Service.
				g.Expect(svc.Spec.ClusterIP).To(Equal("None"))
			}, timeout, interval).Should(Succeed())
		})

		It("creates a per-node ServiceAccount for pod identity", func() {
			// SA name == tn.Name + "-sa" (workload_manager.go:43).
			// The StatefulSet's podSpec.serviceAccountName references this SA,
			// scoping RBAC permissions to the individual node workload.
			sa := &corev1.ServiceAccount{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name + "-sa",
					Namespace: testNamespace,
				}, sa)).To(Succeed())
			}, timeout, interval).Should(Succeed())
		})

		It("sets the TaoNode as the controller owner of the StatefulSet", func() {
			// Owner references enable Kubernetes garbage collection: when the
			// TaoNode is deleted, the StatefulSet (and its pods) are cleaned up
			// automatically. The finalizer test below validates this chain.
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
				g.Expect(sts.OwnerReferences).NotTo(BeEmpty(),
					"StatefulSet must have at least one OwnerReference")
			}, timeout, interval).Should(Succeed())

			Expect(sts.OwnerReferences[0].Kind).To(Equal("TaoNode"))
			Expect(sts.OwnerReferences[0].Name).To(Equal(tn.Name))
			Expect(*sts.OwnerReferences[0].Controller).To(BeTrue(),
				"OwnerReference must set Controller=true so k8s GC knows who owns this object")
		})

		It("applies the standard label set to the StatefulSet", func() {
			// Labels are used by the pod selector and by Prometheus ServiceMonitor
			// target discovery.
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			Expect(sts.Labels).To(HaveKeyWithValue("app.kubernetes.io/instance", tn.Name))
			Expect(sts.Labels).To(HaveKeyWithValue("tao.guardian.io/network", "devnet"))
			Expect(sts.Labels).To(HaveKeyWithValue("tao.guardian.io/role", "miner"))
		})
	})

	// ══════════════════════════════════════════════════════════════════════════
	// Scenario 2 — Validator: OnDelete update strategy (slashing protection)
	// ══════════════════════════════════════════════════════════════════════════
	Describe("provisioning a validator TaoNode", func() {
		var tn *taov1alpha1.TaoNode

		BeforeEach(func() {
			// validateHotkeySecret calls r.Get() on the Secret before building the
			// StatefulSet. The Secret must exist in the test namespace or the
			// reconciler aborts (NotFound) before creating any workload resources.
			hotKeySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "validator-hotkey-placeholder",
					Namespace: testNamespace,
				},
				Data: map[string][]byte{
					// 32 zero-bytes satisfy the ">20 bytes" size check in the
					// key-injector init container and the len() check in the operator.
					"hotkey": make([]byte, 32),
				},
			}
			err := k8sClient.Create(ctx, hotKeySecret)
			if err != nil && !apierrors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}
			DeferCleanup(func() {
				_ = k8sClient.Delete(ctx, hotKeySecret)
			})

			tn = newValidatorSpec("validator-sn1")
			Expect(k8sClient.Create(ctx, tn)).To(Succeed())
			DeferCleanup(waitAndDeleteTaoNode, tn)
		})

		It("uses OnDelete strategy to prevent accidental double-signing during upgrades", func() {
			// validators MUST use OnDelete: a rolling restart could briefly run two
			// validator instances simultaneously, triggering a slashing condition.
			// With OnDelete, a human must explicitly delete the pod to trigger an
			// upgrade — providing a deliberate safety gate.
			//
			// See ensureNodeWorkload (workload_manager.go:105-113).
			sts := &appsv1.StatefulSet{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name:      tn.Name,
					Namespace: testNamespace,
				}, sts)).To(Succeed())
				g.Expect(sts.Spec.UpdateStrategy.Type).To(
					Equal(appsv1.OnDeleteStatefulSetStrategyType),
					"validators require OnDelete strategy to avoid double-signing")
			}, timeout, interval).Should(Succeed())
		})
	})

	// ══════════════════════════════════════════════════════════════════════════
	// Scenario 3 — Deletion: finalizer-aware lifecycle
	// ══════════════════════════════════════════════════════════════════════════
	Describe("deleting a TaoNode", func() {
		It("removes the finalizer so Kubernetes can garbage-collect the object", func() {
			tn := newMinerSpec("miner-deletion-test")
			Expect(k8sClient.Create(ctx, tn)).To(Succeed())
			DeferCleanup(waitAndDeleteTaoNode, tn)

			// Phase 1: wait for the controller to add the finalizer.
			// Without this gate, the delete below might race with the first
			// reconcile and the TaoNode could be deleted before the finalizer
			// is ever set, making this test trivially pass without exercising
			// the handleFinalization code path.
			Eventually(func(g Gomega) {
				current := &taov1alpha1.TaoNode{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tn), current)).To(Succeed())
				g.Expect(current.Finalizers).To(ContainElement(finalizerName))
			}, timeout, interval).Should(Succeed())

			// Phase 2: delete the TaoNode.
			// The API server sets DeletionTimestamp and waits for the finalizer
			// to be cleared. The controller's handleFinalization() does cleanup
			// (snapshot check, validator Lease deletion) then removes the finalizer.
			Expect(k8sClient.Delete(ctx, tn)).To(Succeed())

			// Phase 3: the object must disappear once the finalizer is cleared.
			// If handleFinalization panics or never removes the finalizer, this
			// assertion will time out and the test will fail with a clear message.
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tn), &taov1alpha1.TaoNode{})
				g.Expect(apierrors.IsNotFound(err)).To(BeTrue(),
					"TaoNode must be fully removed after finalizer is cleared")
			}, timeout, interval).Should(Succeed())
		})
	})
})
