// cmd/main.go
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

// TaoNode Guardian operator entrypoint.
//
// Dependency injection map (what this file wires together):
//
//   kubeconfig ──► ctrl.Manager
//                       │
//                       ├─ TaoNodeReconciler ◄── ClickHouseConn ──► BatchWriter
//                       │                    ◄── AnomalyDetector       │
//                       │                    ◄── CircuitBreaker  ───────┘
//                       │                    ◄── ai.Advisor ◄── OllamaHTTPClient
//                       │                    ◄── RecoveryRateLimiter
//                       │                    ◄── ProbeHTTPClient
//                       │
//                       ├─ BatchWriter (Runnable)   — async ClickHouse flush
//                       ├─ DRRunner    (Runnable)   — periodic gzip+upload backup
//                       └─ TaoNode webhook          — admission validation
//
// Feature gating via environment variables:
//   CLICKHOUSE_ENDPOINT  — enables the analytics plane (BatchWriter + AnomalyDetector)
//   OLLAMA_ENDPOINT      — enables the AI advisory plane
//   DR_BACKUP_BUCKET     — enables the DR background runner
package main

import (
	"context"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	// Load kubeconfig auth plugins for GKE (gcp), EKS (aws), AKS (azure).
	// The blank import activates their init() functions; without this,
	// out-of-cluster kubeconfigs for cloud clusters will fail authentication.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	taov1alpha1 "github.com/ClaudioBotelhOSB/taonode-guardian/api/v1alpha1"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/ai"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/analytics"
	"github.com/ClaudioBotelhOSB/taonode-guardian/internal/controller"
)

// scheme is the runtime.Scheme shared by the manager and all reconcilers.
// Populated in init() before main() runs.
var (
	// version is injected at build time via -ldflags "-X main.version=..."
	// When built without ldflags (e.g. `go run`), it defaults to "dev".
	version = "dev"

	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	// clientgoscheme registers all built-in Kubernetes API groups in one call:
	// core/v1 (Pod, Service, PVC), apps/v1 (StatefulSet), coordination/v1 (Lease),
	// and every other standard group. This is sufficient for all types the
	// controller watches, creates, or updates.
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	// Register our own CRD types (TaoNode + TaoNodeList) so the manager can
	// cache, watch, and reconcile them via the informer mechanism.
	utilruntime.Must(taov1alpha1.AddToScheme(scheme))
}

func main() {
	// ── Flags ─────────────────────────────────────────────────────────────────
	var (
		metricsAddr       string
		probeAddr         string
		enableLeaderElect bool
		enableWebhooks    bool
		clusterID         string
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080",
		"TCP address for the Prometheus /metrics endpoint.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081",
		"TCP address for the /healthz and /readyz probe endpoints.")
	flag.BoolVar(&enableLeaderElect, "leader-elect", true,
		"Enable leader election. Set false for single-replica local development.")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", true,
		"Register admission webhooks. Set false for dev environments without cert-manager.")
	flag.StringVar(&clusterID, "cluster-id", "",
		"Logical cluster ID used as prefix in DR backup object-storage keys.\n"+
			"Defaults to the pod hostname (injected by the downward API in production).")

	// Zap logger tuning: --zap-log-level, --zap-encoder (json|console),
	// --zap-stacktrace-level. These are registered automatically by BindFlags.
	zapOpts := zap.Options{Development: false}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	// Fallback cluster ID: use the pod hostname set by Kubernetes downward API.
	if clusterID == "" {
		clusterID, _ = os.Hostname()
	}
	if clusterID == "" {
		clusterID = "taoguardian"
	}

	// ── Manager ───────────────────────────────────────────────────────────────
	// The manager owns the informer cache, leader election, and lifecycle of all
	// registered Reconcilers and Runnables. A single manager handles the full
	// TaoNode Guardian control plane.
	managerOpts := ctrl.Options{
		Scheme: scheme,
		// Prometheus metrics are scraped from :8080/metrics by default.
		// ServiceMonitor (created by the controller) points here.
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
		// Liveness and readiness probes for the operator Pod itself.
		HealthProbeBindAddress: probeAddr,
		// Leader election prevents split-brain if multiple operator replicas run.
		// Uses a Lease object named "tao.guardian.io" in the operator's namespace.
		LeaderElection:   enableLeaderElect,
		LeaderElectionID: "tao.guardian.io",
	}

	// The webhook server requires TLS certificates mounted at the standard path.
	// In production these are injected by cert-manager via a Certificate + Secret.
	// Skip the server entirely in dev mode (--enable-webhooks=false) to avoid
	// cert requirements when running `make run` locally.
	if enableWebhooks {
		managerOpts.WebhookServer = webhook.NewServer(webhook.Options{Port: 9443})
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), managerOpts)
	if err != nil {
		setupLog.Error(err, "Unable to create manager")
		os.Exit(1)
	}

	// ── Analytics plane ───────────────────────────────────────────────────────
	// All three components are nil when CLICKHOUSE_ENDPOINT is unset or when the
	// connection / schema migration fails. The reconciler checks for nil before
	// calling into this plane — no analytics = reactive-only mode, fully safe.
	batchWriter, anomalyDetector, circuitBreaker := initAnalytics()

	// BatchWriter implements manager.Runnable: the manager calls Start(ctx) in
	// its own goroutine and waits for completion on shutdown (orderly final flush).
	// Must be registered BEFORE mgr.Start() is called.
	if batchWriter != nil {
		if err := mgr.Add(batchWriter); err != nil {
			setupLog.Error(err, "Unable to register BatchWriter as manager Runnable")
			os.Exit(1)
		}
		setupLog.Info("BatchWriter registered (async ClickHouse flush goroutines will start with manager)")
	}

	// ── AI advisory plane ─────────────────────────────────────────────────────
	// Advisor is nil when OLLAMA_ENDPOINT is not set. The reconciler calls
	// maybeRequestAIAdvisory() which checks for nil before launching goroutines,
	// so no code path reaches Ollama unless it's explicitly configured.
	aiAdvisor := initAI()

	// ── Reconciler ───────────────────────────────────────────────────────────
	// All optional fields (AnalyticsWriter, AnomalyDetector, ClickHouseCircuit,
	// AIAdvisor) are nil-safe. The controller checks each before use, enabling
	// per-cluster feature gating purely through environment variables.
	reconciler := &controller.TaoNodeReconciler{
		// Core controller-runtime dependencies — always present.
		Client:    mgr.GetClient(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorderFor("taonode-controller"),
		APIReader: mgr.GetAPIReader(), // cache-bypassing reader for strongly consistent reads

		// Analytics plane — nil when CLICKHOUSE_ENDPOINT is unset.
		AnalyticsWriter:   batchWriter,
		AnomalyDetector:   anomalyDetector,
		ClickHouseCircuit: circuitBreaker,

		// AI advisory — nil when OLLAMA_ENDPOINT is unset.
		AIAdvisor: aiAdvisor,

		// RecoveryRateLimiter caps concurrent fleet-wide recoveries at 5
		// and enforces a 2-minute per-node cooldown between retry attempts.
		// This prevents API-server overload during mass sync-loss events.
		RecoveryLimiter: controller.NewRecoveryRateLimiter(5, 2*time.Minute),

		// DefaultImage is the container image used when spec.image is empty.
		DefaultImage: getEnvOrDefault("DEFAULT_NODE_IMAGE",
			"ghcr.io/opentensor/subtensor:latest"),

		// ProbeHTTPClient is shared across all chain-health probe calls.
		// 5 s timeout is intentionally tight: a hanging probe signals a node issue.
		ProbeHTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", "TaoNode")
		os.Exit(1)
	}

	// ── Admission webhook ─────────────────────────────────────────────────────
	// The ValidatingWebhook blocks creation of misconfigured TaoNodes before they
	// reach the reconciler (e.g., validator without hotKeySecret, storage too small).
	// Webhook path: /validate-tao-guardian-io-v1alpha1-taonode
	if enableWebhooks {
		if err := (&taov1alpha1.TaoNode{}).SetupWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "Unable to register TaoNode webhook", "webhook", "TaoNode")
			os.Exit(1)
		}
		setupLog.Info("TaoNode admission webhook registered",
			"path", "/validate-tao-guardian-io-v1alpha1-taonode")
	}

	// ── DR Runner ────────────────────────────────────────────────────────────
	// DRRunner implements manager.Runnable and executes periodic
	// gzip+upload backups of all TaoNode CRs.
	//
	// Object store: defaults to a no-op logger implementation.
	// Configure a real backend (S3 / GCS / MinIO) in cmd/tao-dr/ or by
	// implementing controller.ObjectStoreClient and injecting it here.
	//
	// NOTE: DRRunner does not implement LeaderElectionRunnable, so it runs on
	// ALL replicas. In production, either implement NeedLeaderElection() == true
	// on DRRunner or deploy the operator with replicas=1 until that is done.
	if drBackupBucket := os.Getenv("DR_BACKUP_BUCKET"); drBackupBucket != "" {
		drRunner := &controller.DRRunner{
			Client:      mgr.GetClient(),
			ObjectStore: newObjectStore(drBackupBucket),
			ClusterID:   clusterID,
			Interval:    getDRInterval(),
			Metrics:     controller.NewDRMetrics(),
		}
		if err := mgr.Add(drRunner); err != nil {
			setupLog.Error(err, "Unable to register DRRunner as manager Runnable")
			os.Exit(1)
		}
	} else {
		setupLog.Info("DR_BACKUP_BUCKET not set — DR runner disabled")
	}

	// ── Health checks ────────────────────────────────────────────────────────
	// Kubernetes probes the /healthz and /readyz endpoints defined here.
	// healthz.Ping returns 200 OK as long as the process is alive.
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register /healthz check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Unable to register /readyz check")
		os.Exit(1)
	}

	// ── Start ────────────────────────────────────────────────────────────────
	setupLog.Info("Starting TaoNode Guardian",
		"version", version,
		"metricsAddr", metricsAddr,
		"probeAddr", probeAddr,
		"leaderElect", enableLeaderElect,
		"webhooks", enableWebhooks,
		"clusterID", clusterID,
		"analyticsEnabled", batchWriter != nil,
		"aiEnabled", aiAdvisor != nil,
		"drInterval", getDRInterval(),
	)

	// SetupSignalHandler returns a context cancelled on SIGTERM or SIGINT.
	// The manager orchestrates orderly shutdown: it cancels all Runnables,
	// waits for them to return, then exits.
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "Manager exited with error")
		os.Exit(1)
	}
}

// ── Analytics plane initialisation ───────────────────────────────────────────

// initAnalytics creates the ClickHouse connection, applies the DDL schema, and
// returns the three analytics-plane components.
//
// Credentials are read from the projected Secret volume at /secrets/clickhouse/
// (keys: endpoint, username, password, database). The volume is optional so the
// pod starts cleanly when analytics are disabled.
//
// Returns (nil, nil, nil) when:
//   - /secrets/clickhouse/endpoint is absent or empty (analytics intentionally disabled), or
//   - The TCP dial or schema migration fails (operator starts in reactive-only mode
//     and logs a descriptive error — no panic or os.Exit).
func initAnalytics() (
	writer *analytics.BatchWriter,
	detector *analytics.AnomalyDetector,
	cb *analytics.CircuitBreaker,
) {
	endpoint := readClickHouseSecret("endpoint", "")
	if endpoint == "" {
		setupLog.Info("ClickHouse endpoint not configured — analytics plane disabled (reactive-only mode)")
		return nil, nil, nil
	}

	conn, err := analytics.NewClickHouseConn(analytics.ClickHouseConfig{
		Endpoint:   endpoint,
		Username:   readClickHouseSecret("username", "default"),
		Password:   readClickHouseSecret("password", ""),
		Database:   readClickHouseSecret("database", "taoguardian"),
		TLSEnabled: os.Getenv("CLICKHOUSE_TLS") == "true",
	})
	if err != nil {
		setupLog.Error(err, "ClickHouse connection failed — analytics plane disabled",
			"endpoint", endpoint)
		return nil, nil, nil
	}

	// Apply DDL idempotently at startup (CREATE TABLE IF NOT EXISTS).
	// If this fails, disable analytics entirely rather than starting with an
	// incomplete schema that would cause flush errors on every batch write.
	applyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := analytics.ApplySchema(applyCtx, conn); err != nil {
		setupLog.Error(err, "ClickHouse schema migration failed — analytics plane disabled",
			"schemaVersion", analytics.SchemaVersion)
		return nil, nil, nil
	}

	// Circuit breaker: open after 5 consecutive failures, reset probe after 30 s,
	// close after 3 successful probes. Keeps the reconcile loop fast when
	// ClickHouse is degraded or restarting.
	cb = analytics.NewCircuitBreaker(5, 30*time.Second, 3)

	setupLog.Info("Analytics plane initialised",
		"endpoint", endpoint,
		"schemaVersion", analytics.SchemaVersion,
	)
	return analytics.NewBatchWriter(conn), analytics.NewAnomalyDetector(conn), cb
}

// ── AI advisory plane initialisation ─────────────────────────────────────────

// initAI creates the Ollama-backed Advisor when OLLAMA_ENDPOINT is configured.
// Returns nil and logs at Info level when the var is absent — this is the
// expected state for clusters that do not have a local LLM.
func initAI() *ai.Advisor {
	ollamaURL := os.Getenv("OLLAMA_ENDPOINT")
	if ollamaURL == "" {
		setupLog.Info("OLLAMA_ENDPOINT not set — AI advisory plane disabled")
		return nil
	}

	model := getEnvOrDefault("OLLAMA_MODEL", "llama3.2")
	advisor, err := ai.NewAdvisor(ai.Config{
		OllamaEndpoint: ollamaURL,
		Model:          model,
		// RequestTimeout must be >= the reconciler's AI advisory timeout (30 s).
		// Increase this for larger models that require more inference time.
		RequestTimeout:    30 * time.Second,
		MaxCallsPerMinute: 5,
	})
	if err != nil {
		setupLog.Error(err, "AI Advisor creation failed — AI advisory plane disabled")
		return nil
	}

	setupLog.Info("AI advisory plane initialised", "endpoint", ollamaURL, "model", model)
	return advisor
}

// ── DR object store ───────────────────────────────────────────────────────────

// noopObjectStore satisfies controller.ObjectStoreClient without performing
// any real network I/O. It logs the upload intention at Info level so that
// DR backup activity is visible in operator logs even without a real bucket.
//
// Production deployment: replace with a concrete backend implementing
// controller.ObjectStoreClient (e.g. AWS S3, Google Cloud Storage, MinIO).
// The implementation can live in cmd/tao-dr/ or pkg/objectstore/.
type noopObjectStore struct{ bucket string }

func (n *noopObjectStore) Upload(_ context.Context, key string, data []byte) error {
	setupLog.Info("DR backup completed (noop store)",
		"bucket", n.bucket, "key", key, "sizeBytes", len(data))
	return nil
}

// newObjectStore returns the active ObjectStoreClient.
// Extend this function to select between S3/GCS/MinIO based on env vars.
func newObjectStore(bucket string) controller.ObjectStoreClient {
	return &noopObjectStore{bucket: bucket}
}

// ── Utilities ─────────────────────────────────────────────────────────────────

// getDRInterval parses the DR_INTERVAL environment variable as a Go duration.
// Accepts standard Go duration strings (e.g. "30m", "2h", "24h").
// Defaults to 1 hour if the variable is absent or syntactically invalid.
func getDRInterval() time.Duration {
	s := os.Getenv("DR_INTERVAL")
	if s == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		setupLog.Info("DR_INTERVAL is invalid, using default 1h", "value", s)
		return time.Hour
	}
	return d
}

// getEnvOrDefault returns the value of the named environment variable, or
// fallback when the variable is absent or empty. Used for optional configuration
// that has a sensible production default.
func getEnvOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// readClickHouseSecret reads a ClickHouse credential from the projected Secret
// volume mounted at /secrets/clickhouse/<name>.
//
// Returns defaultVal when the file is absent or empty (analytics disabled, dev
// environment without the Secret). This matches the optional: true on the volume.
func readClickHouseSecret(name, defaultVal string) string {
	data, err := os.ReadFile(filepath.Join("/secrets/clickhouse", name))
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return defaultVal
	}
	return strings.TrimSpace(string(data))
}
