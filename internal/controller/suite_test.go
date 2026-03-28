// internal/controller/suite_test.go
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

// Package controller_test contains integration tests for the TaoNode operator.
//
// Tests run against a real Kubernetes API server (envtest) with the full
// reconcile loop active. No mocking of the API server or controller logic.
//
// Prerequisites:
//
//	make envtest          # downloads setup-envtest binaries to bin/
//	make test             # sets KUBEBUILDER_ASSETS and runs go test ./...
//
// To run a single spec in isolation:
//
//	KUBEBUILDER_ASSETS="$(bin/setup-envtest use 1.31.0 --bin-dir bin/k8s -p path)" \
//	  go test ./internal/controller/... -v --ginkgo.focus "StatefulSet"
package controller_test

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/controller"
)

// Package-level variables are shared between suite_test.go and
// taonode_controller_test.go, which compile into the same test binary.
var (
	cfg       *rest.Config
	k8sClient client.Client // direct API server client, bypasses manager cache
	testEnv   *envtest.Environment

	cancelFunc context.CancelFunc // cancels the manager context on teardown
	mgrDone    chan struct{}       // closed when mgr.Start() returns
	mgrErr     error              // holds any non-nil error from mgr.Start()
)

// TestController is the Ginkgo bootstrap function that wires this package into
// the standard `go test` runner. Every Describe/It block in all *_test.go files
// in this package runs when TestController is invoked.
func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TaoNode Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping envtest")
	testEnv = &envtest.Environment{
		// CRD directory is relative to this test file's location.
		// Regenerate the CRD YAML with: make manifests (controller-gen).
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		// BinaryAssetsDirectory is omitted intentionally — envtest resolves the
		// kube-apiserver + etcd binaries from the KUBEBUILDER_ASSETS environment
		// variable, which is set by `make envtest` / `setup-envtest use`.
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// ── Scheme ────────────────────────────────────────────────────────────────
	// clientgoscheme registers all built-in API groups:
	//   core/v1 (Pod, Service, PVC, SA), apps/v1 (StatefulSet),
	//   coordination.k8s.io/v1 (Lease for leader election + validator singleton)
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(taov1alpha1.AddToScheme(scheme))

	// ── Direct client ─────────────────────────────────────────────────────────
	// Tests use this client to read/assert state. It bypasses the manager's
	// informer cache so assertions see immediately-consistent API server state.
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	// ── Manager ───────────────────────────────────────────────────────────────
	By("creating the controller manager")
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		// Binding to port "0" lets the OS assign an ephemeral port, preventing
		// "address already in use" failures when tests run in parallel CI jobs.
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// Leader election adds a ~15 s acquire delay that makes tests slow.
		// It is unnecessary in envtest because only one goroutine runs here.
		LeaderElection: false,
	})
	Expect(err).NotTo(HaveOccurred())

	// ── Reconciler ────────────────────────────────────────────────────────────
	By("registering the TaoNodeReconciler")
	err = (&controller.TaoNodeReconciler{
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("taonode-controller"),
		APIReader: mgr.GetAPIReader(),

		// Analytics and AI planes are nil — the reconciler is fully nil-safe for
		// both subsystems, so the core reconcile path runs without ClickHouse or
		// Ollama. This is the correct unit under test here.
		AnalyticsWriter:   nil,
		AnomalyDetector:   nil,
		ClickHouseCircuit: nil,
		AIAdvisor:         nil,

		RecoveryLimiter: controller.NewRecoveryRateLimiter(5, 2*time.Minute),
		DefaultImage:    "opentensor/subtensor:latest",

		// 1 ms timeout makes probeChainHealth (Step 4) fail fast in envtest
		// because no real pods exist. Crucially, Step 3 (resource creation) runs
		// BEFORE Step 4, so the StatefulSet exists even when the probe fails.
		// The controller handles probe failures gracefully and requeues — no panic.
		ProbeHTTPClient: &http.Client{Timeout: 1 * time.Millisecond},
	}).SetupWithManager(mgr)
	Expect(err).NotTo(HaveOccurred())

	// ── Start manager ─────────────────────────────────────────────────────────
	// The manager runs in a background goroutine for the duration of the suite.
	// mgrDone is closed when Start() returns, enabling ordered teardown in
	// AfterSuite: cancel → wait → stop envtest (prevents connection errors from
	// the manager hitting a torn-down API server).
	By("starting the manager")
	var ctx context.Context
	ctx, cancelFunc = context.WithCancel(context.Background())
	mgrDone = make(chan struct{})

	go func() {
		defer GinkgoRecover()
		defer close(mgrDone)
		mgrErr = mgr.Start(ctx)
	}()
})

var _ = AfterSuite(func() {
	By("cancelling the manager context")
	cancelFunc()

	// Wait for mgr.Start() to return before stopping the API server.
	// mgr.Start() returns nil after context cancellation, so mgrErr should
	// always be nil here — verified by the Expect below.
	By("waiting for the manager to stop")
	select {
	case <-mgrDone:
	case <-time.After(15 * time.Second):
		Fail("manager goroutine did not exit within 15 seconds")
	}
	Expect(mgrErr).NotTo(HaveOccurred())

	By("stopping envtest")
	Expect(testEnv.Stop()).To(Succeed())
})
