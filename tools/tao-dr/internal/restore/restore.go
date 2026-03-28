// Package restore implements the `tao-dr restore` command.
// It downloads a gzip-compressed JSON backup from S3, validates the payload,
// and applies resources to the cluster via Server-Side Apply.
//
// CRDs are applied and validated as "Established" BEFORE any CRs are applied.
// Supports --dry-run to preview resources without touching the cluster.
package restore

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// drBackup mirrors the operator's DRBackup struct.
// Nodes and CRDs are decoded as RawMessage to avoid importing operator types.
type drBackup struct {
	Timestamp     time.Time         `json:"timestamp"`
	ClusterID     string            `json:"clusterID"`
	SchemaVersion string            `json:"schemaVersion"`
	CRDs          []json.RawMessage `json:"crds,omitempty"`
	Nodes         []json.RawMessage `json:"nodes"`
}

var (
	crdGVR = schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
	taoGVR = schema.GroupVersionResource{
		Group: "tao.guardian.io", Version: "v1alpha1", Resource: "taonodes",
	}
)

// NewCommand returns the `restore` cobra.Command.
func NewCommand(s3Bucket, s3Region, kubeconfig *string) *cobra.Command {
	var (
		backupKey         string
		dryRun            bool
		namespaceOverride string
		forceOwnership    bool
	)

	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore TaoNode CRs (and optionally CRD) from an S3 backup",
		Long: `Downloads a gzip-compressed JSON backup from S3 and applies it
to the target cluster. Resources are applied in two phases:

  Phase 1 — CRDs (apiextensions.k8s.io)
    Applied first; restore waits until the CRD reports condition Established=True
    before proceeding. This prevents 'no kind is registered' errors on Phase 2.

  Phase 2 — TaoNode CRs (tao.guardian.io)
    Applied via Server-Side Apply with field manager 'tao-dr'.

Use --dry-run to print all resources that would be applied without touching the cluster.`,
		Example: `  # Preview what would be restored
  tao-dr restore --s3-bucket my-bucket \
    --backup-key dr-backups/prod/2024-01-15T10-00-00.json.gz \
    --dry-run

  # Restore to a different namespace
  tao-dr restore --s3-bucket my-bucket \
    --backup-key dr-backups/prod/2024-01-15T10-00-00.json.gz \
    --namespace taonode-guardian-system-dr`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if *s3Bucket == "" {
				return fmt.Errorf("--s3-bucket is required")
			}
			if backupKey == "" {
				return fmt.Errorf("--backup-key is required (run 'tao-dr status' to list available backups)")
			}
			return run(cmd.Context(), *s3Bucket, *s3Region, *kubeconfig,
				backupKey, namespaceOverride, dryRun, forceOwnership)
		},
	}

	cmd.Flags().StringVar(&backupKey, "backup-key", "",
		"S3 key of the .json.gz backup manifest (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print resources that would be applied without touching the cluster")
	cmd.Flags().StringVarP(&namespaceOverride, "namespace", "n", "",
		"Override the namespace for restored CRs (empty = use namespace from backup)")
	cmd.Flags().BoolVar(&forceOwnership, "force-ownership", false,
		"Pass Force=true to Server-Side Apply, claiming fields owned by other managers")

	_ = cmd.MarkFlagRequired("backup-key")

	return cmd
}

func run(ctx context.Context, bucket, region, kubeconfig, backupKey, nsOverride string, dryRun, force bool) error {
	// ── 1. Download ───────────────────────────────────────────────────────────
	fmt.Printf("[restore] Downloading s3://%s/%s\n", bucket, backupKey)

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	resp, err := s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(backupKey),
	})
	if err != nil {
		return fmt.Errorf("downloading backup: %w", err)
	}
	defer resp.Body.Close()

	// ── 2. Decompress ─────────────────────────────────────────────────────────
	var reader io.Reader = resp.Body
	if strings.HasSuffix(backupKey, ".gz") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("creating gzip reader: %w", err)
		}
		defer gz.Close()
		reader = gz
	}

	raw, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("reading backup body: %w", err)
	}

	// ── 3. Validate JSON ──────────────────────────────────────────────────────
	var backup drBackup
	if err := json.Unmarshal(raw, &backup); err != nil {
		return fmt.Errorf("parsing backup JSON: %w", err)
	}

	if backup.SchemaVersion == "" {
		return fmt.Errorf("invalid backup: missing schemaVersion field")
	}
	if len(backup.Nodes) == 0 && len(backup.CRDs) == 0 {
		return fmt.Errorf("backup contains no resources to restore")
	}

	fmt.Printf("[restore] Backup: clusterID=%s timestamp=%s schemaVersion=%s crds=%d nodes=%d\n",
		backup.ClusterID,
		backup.Timestamp.Format(time.RFC3339),
		backup.SchemaVersion,
		len(backup.CRDs),
		len(backup.Nodes),
	)

	// ── 4. Parse resources ────────────────────────────────────────────────────
	crds, err := parseObjects(backup.CRDs, "", false) // CRDs are cluster-scoped, no namespace
	if err != nil {
		return fmt.Errorf("parsing CRD resources: %w", err)
	}
	nodes, err := parseObjects(backup.Nodes, nsOverride, true)
	if err != nil {
		return fmt.Errorf("parsing TaoNode resources: %w", err)
	}

	// ── 5. Dry-run: print and exit ─────────────────────────────────────────────
	if dryRun {
		printDryRun(crds, nodes)
		return nil
	}

	// ── 6. Connect to cluster ─────────────────────────────────────────────────
	restCfg, err := kubeRESTConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubeconfig: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	// ── 7. Phase 1: Apply CRDs ────────────────────────────────────────────────
	if len(crds) > 0 {
		fmt.Printf("[restore] Phase 1 — applying %d CRD(s)\n", len(crds))
		for _, crd := range crds {
			if err := applyResource(ctx, dynClient, crdGVR, crd, force); err != nil {
				return fmt.Errorf("applying CRD %s: %w", crd.GetName(), err)
			}
			fmt.Printf("[restore]   ✓ CRD %s applied\n", crd.GetName())
		}

		// Wait for all CRDs to reach Established=True before proceeding.
		for _, crd := range crds {
			fmt.Printf("[restore]   ⏳ Waiting for CRD %s to be Established…\n", crd.GetName())
			if err := waitCRDEstablished(ctx, dynClient, crd.GetName()); err != nil {
				return fmt.Errorf("CRD %s did not become Established: %w", crd.GetName(), err)
			}
			fmt.Printf("[restore]   ✓ CRD %s is Established\n", crd.GetName())
		}
	}

	// ── 8. Phase 2: Apply CRs ──────────────────────────────────────────────────
	fmt.Printf("[restore] Phase 2 — applying %d TaoNode CR(s)\n", len(nodes))
	for _, node := range nodes {
		ns := node.GetNamespace()
		if ns == "" {
			ns = "taonode-guardian-system"
			node.SetNamespace(ns)
		}
		if err := applyResource(ctx, dynClient, taoGVR, node, force); err != nil {
			return fmt.Errorf("applying TaoNode %s/%s: %w", ns, node.GetName(), err)
		}
		fmt.Printf("[restore]   ✓ TaoNode %s/%s applied\n", ns, node.GetName())
	}

	fmt.Printf("[restore] Restore complete: %d CRD(s), %d TaoNode(s)\n", len(crds), len(nodes))
	return nil
}

// parseObjects converts a slice of json.RawMessage into Unstructured objects.
// If nsOverride is non-empty, it replaces the namespace for namespaced resources.
// If ensureTypeMeta is true, missing apiVersion/kind are inferred for TaoNode objects.
func parseObjects(raws []json.RawMessage, nsOverride string, ensureTypeMeta bool) ([]*unstructured.Unstructured, error) {
	var objs []*unstructured.Unstructured
	for i, raw := range raws {
		var objMap map[string]interface{}
		if err := json.Unmarshal(raw, &objMap); err != nil {
			return nil, fmt.Errorf("resource[%d]: invalid JSON: %w", i, err)
		}

		obj := &unstructured.Unstructured{Object: objMap}

		// Operator backups may omit apiVersion/kind for TaoNode CRs because
		// client-go's typed client strips TypeMeta on List responses.
		if ensureTypeMeta {
			if obj.GetAPIVersion() == "" {
				obj.SetAPIVersion("tao.guardian.io/v1alpha1")
			}
			if obj.GetKind() == "" {
				obj.SetKind("TaoNode")
			}
		}

		if nsOverride != "" && obj.GetNamespace() != "" {
			obj.SetNamespace(nsOverride)
		}

		objs = append(objs, obj)
	}
	return objs, nil
}

// applyResource applies obj via Kubernetes Server-Side Apply (SSA).
// SSA is idempotent: calling it on an existing resource updates it in place.
func applyResource(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, obj *unstructured.Unstructured, forceOwnership bool) error {
	data, err := json.Marshal(obj.Object)
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	patchOpts := metav1.PatchOptions{
		FieldManager: "tao-dr",
		Force:        &forceOwnership,
	}

	ns := obj.GetNamespace()
	if ns == "" {
		// Cluster-scoped resource (e.g., CRD, ClusterRole).
		_, err = dynClient.Resource(gvr).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, patchOpts)
	} else {
		_, err = dynClient.Resource(gvr).Namespace(ns).Patch(ctx, obj.GetName(), types.ApplyPatchType, data, patchOpts)
	}
	return err
}

// waitCRDEstablished polls until the CRD reports condition Established=True.
// Timeout: 90 seconds (sufficient for large CRDs with conversion webhooks).
func waitCRDEstablished(ctx context.Context, dynClient dynamic.Interface, crdName string) error {
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, 90*time.Second, true,
		func(ctx context.Context) (bool, error) {
			obj, err := dynClient.Resource(crdGVR).Get(ctx, crdName, metav1.GetOptions{})
			if err != nil {
				return false, nil // CRD not visible yet; keep polling
			}
			conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
			for _, c := range conditions {
				cond, ok := c.(map[string]interface{})
				if !ok {
					continue
				}
				if cond["type"] == "Established" && cond["status"] == "True" {
					return true, nil
				}
			}
			return false, nil
		},
	)
}

func printDryRun(crds, nodes []*unstructured.Unstructured) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "[dry-run] Resources that would be applied:")
	fmt.Fprintln(w, "PHASE\tKIND\tNAMESPACE\tNAME\tAPI VERSION")
	fmt.Fprintln(w, "─────\t────\t─────────\t────\t───────────")
	for _, crd := range crds {
		fmt.Fprintf(w, "1/CRD\t%s\t%s\t%s\t%s\n",
			crd.GetKind(), crd.GetNamespace(), crd.GetName(), crd.GetAPIVersion())
	}
	for _, node := range nodes {
		fmt.Fprintf(w, "2/CR\t%s\t%s\t%s\t%s\n",
			node.GetKind(), node.GetNamespace(), node.GetName(), node.GetAPIVersion())
	}
	_ = w.Flush()
	fmt.Println("\n[dry-run] No changes applied.")
}

func kubeRESTConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}
