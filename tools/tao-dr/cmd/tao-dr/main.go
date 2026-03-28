// tao-dr — Disaster Recovery CLI for the TaoNode Guardian Bittensor fleet.
//
// Standalone binary: does NOT import any code from the operator module.
// Uses client-go dynamic client to interact with K8s without needing
// the tao.guardian.io/v1alpha1 typed structs.
//
// Usage:
//   tao-dr status   --s3-bucket my-bucket
//   tao-dr backup   --s3-bucket my-bucket --cluster-id prod-us-east-1
//   tao-dr restore  --s3-bucket my-bucket --backup-key dr-backups/prod/2024-01-15T10-00-00.json.gz
//   tao-dr failover --tf-dir infra/terraform --dry-run
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ClaudioBotelhOSB/tao-dr/internal/failover"
	"github.com/ClaudioBotelhOSB/tao-dr/internal/restore"
	"github.com/ClaudioBotelhOSB/tao-dr/internal/status"
)

// drBackup mirrors the operator's DRBackup struct (internal/controller/dr_runner.go).
// Nodes are decoded as RawMessage because this binary does not import the operator's
// typed API (taov1alpha1.TaoNode). CRDs are an extension added by this CLI.
type drBackup struct {
	Timestamp     time.Time         `json:"timestamp"`
	ClusterID     string            `json:"clusterID"`
	SchemaVersion string            `json:"schemaVersion"`
	CRDs          []json.RawMessage `json:"crds,omitempty"` // CLI extension; absent in operator backups
	Nodes         []json.RawMessage `json:"nodes"`
}

// ── Global flags ──────────────────────────────────────────────────────────────

var (
	globalS3Bucket   string
	globalS3Region   string
	globalKubeconfig string
)

func main() {
	root := &cobra.Command{
		Use:   "tao-dr",
		Short: "Disaster Recovery CLI for the TaoNode Guardian Bittensor fleet",
		Long: `tao-dr provides four DR operations:

  status   — inspect S3 backup inventory and live cluster health
  backup   — snapshot TaoNode CRs + CRD to S3 (gzip-compressed JSON)
  restore  — apply a backup from S3 back into the cluster
  failover — provision a replacement cluster via Terraform`,
		SilenceUsage: true,
	}

	// Persistent global flags — inherited by all subcommands.
	root.PersistentFlags().StringVar(&globalS3Bucket, "s3-bucket", "",
		"S3 bucket for DR backups (required for backup, restore, status)")
	root.PersistentFlags().StringVar(&globalS3Region, "s3-region", "us-east-1",
		"AWS region of the S3 bucket")
	root.PersistentFlags().StringVar(&globalKubeconfig, "kubeconfig", "",
		"Path to kubeconfig file (defaults to in-cluster config, then ~/.kube/config)")

	root.AddCommand(
		status.NewCommand(&globalS3Bucket, &globalS3Region, &globalKubeconfig),
		restore.NewCommand(&globalS3Bucket, &globalS3Region, &globalKubeconfig),
		failover.NewCommand(&globalS3Bucket, &globalS3Region, &globalKubeconfig),
		newBackupCommand(),
	)

	if err := root.ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
}

// ── backup command ────────────────────────────────────────────────────────────

func newBackupCommand() *cobra.Command {
	var (
		namespace string
		clusterID string
		includeCRD bool
	)

	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot TaoNode CRs (and optionally the CRD) to S3",
		Long: `Reads all TaoNode custom resources from the cluster via the dynamic
client and uploads a gzip-compressed JSON backup to S3.

The backup format is compatible with the operator's DR runner
(internal/controller/dr_runner.go) so operator-generated backups
can be restored with 'tao-dr restore'.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if globalS3Bucket == "" {
				return fmt.Errorf("--s3-bucket is required")
			}
			return runBackup(cmd.Context(), globalS3Bucket, globalS3Region, globalKubeconfig,
				namespace, clusterID, includeCRD)
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "",
		"Namespace to back up (empty = all namespaces)")
	cmd.Flags().StringVar(&clusterID, "cluster-id", "taonode-guardian",
		"Logical cluster identifier used to namespace backups inside the S3 bucket")
	cmd.Flags().BoolVar(&includeCRD, "include-crd", true,
		"Include the TaoNode CRD definition in the backup for full self-contained restores")

	return cmd
}

func runBackup(ctx context.Context, bucket, region, kubeconfig, namespace, clusterID string, includeCRD bool) error {
	restCfg, err := kubeRESTConfig(kubeconfig)
	if err != nil {
		return fmt.Errorf("building kubeconfig: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	taoGVR := schema.GroupVersionResource{
		Group: "tao.guardian.io", Version: "v1alpha1", Resource: "taonodes",
	}

	nodeList, err := dynClient.Resource(taoGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing TaoNode resources: %w", err)
	}

	backup := drBackup{
		Timestamp:     time.Now().UTC(),
		ClusterID:     clusterID,
		SchemaVersion: "v1alpha1",
		Nodes:         make([]json.RawMessage, 0, len(nodeList.Items)),
	}

	for _, item := range nodeList.Items {
		cleanObject(&item)
		raw, err := json.Marshal(item.Object)
		if err != nil {
			return fmt.Errorf("marshaling TaoNode %s: %w", item.GetName(), err)
		}
		backup.Nodes = append(backup.Nodes, raw)
	}

	if includeCRD {
		crdGVR := schema.GroupVersionResource{
			Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
		}
		crd, err := dynClient.Resource(crdGVR).Get(ctx, "taonodes.tao.guardian.io", metav1.GetOptions{})
		if err != nil {
			fmt.Printf("[backup] WARNING: could not fetch TaoNode CRD: %v\n", err)
		} else {
			cleanObject(crd)
			raw, err := json.Marshal(crd.Object)
			if err != nil {
				return fmt.Errorf("marshaling CRD: %w", err)
			}
			backup.CRDs = append(backup.CRDs, raw)
		}
	}

	data, err := json.Marshal(backup)
	if err != nil {
		return fmt.Errorf("marshaling backup manifest: %w", err)
	}

	// gzip-compress to match the operator's upload format.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(data); err != nil {
		return fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("gzip close: %w", err)
	}

	key := fmt.Sprintf("dr-backups/%s/%s.json.gz",
		clusterID,
		time.Now().UTC().Format("2006-01-02T15-04-05"),
	)

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	_, err = s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(bucket),
		Key:                  aws.String(key),
		Body:                 bytes.NewReader(buf.Bytes()),
		ContentType:          aws.String("application/gzip"),
		ContentEncoding:      aws.String("gzip"),
		ServerSideEncryption: "AES256",
	})
	if err != nil {
		return fmt.Errorf("uploading backup to s3://%s/%s: %w", bucket, key, err)
	}

	fmt.Printf("[backup] %d TaoNode(s) backed up → s3://%s/%s (%d bytes compressed)\n",
		len(backup.Nodes), bucket, key, buf.Len())
	return nil
}

// cleanObject removes server-managed fields that must not be restored verbatim.
// Matches the operator's DRRunner.runBackup() logic.
func cleanObject(obj *unstructured.Unstructured) {
	obj.SetResourceVersion("")
	obj.SetUID("")
	obj.SetManagedFields(nil)
	obj.SetFinalizers(nil)
	obj.SetDeletionTimestamp(nil)
	obj.SetGeneration(0)
}

// kubeRESTConfig builds a *rest.Config: kubeconfig path > in-cluster > ~/.kube/config.
func kubeRESTConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}
