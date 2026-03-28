// Package status implements the `tao-dr status` command.
// It lists DR backups in S3, calculates RPO compliance, and reports live cluster health.
package status

import (
	"context"
	"fmt"
	"os"
	"sort"
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
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// rpo is the Recovery Point Objective defined in the TaoNode sample CR (spec.disasterRecovery.rpo).
const rpo = 4 * time.Hour

type backupEntry struct {
	Key          string
	LastModified time.Time
	SizeBytes    int64
}

// NewCommand returns the `status` cobra.Command.
// The s3Bucket, s3Region, and kubeconfig pointers are bound to the root command's
// persistent flags and are populated by cobra before RunE executes.
func NewCommand(s3Bucket, s3Region, kubeconfig *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show DR backup inventory, RPO compliance, and cluster health",
		Long: `Queries the S3 bucket for all gzip backups under dr-backups/,
calculates the age of the latest backup against the 4h RPO,
and reports the readiness of K8s nodes and TaoNode phases.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd.Context(), *s3Bucket, *s3Region, *kubeconfig)
		},
	}
}

func run(ctx context.Context, bucket, region, kubeconfig string) error {
	if bucket == "" {
		return fmt.Errorf("--s3-bucket is required")
	}

	// Run both sections; collect errors so both are always printed.
	errBackup := printBackupStatus(ctx, bucket, region)
	fmt.Println()
	errCluster := printClusterHealth(ctx, kubeconfig)

	if errBackup != nil {
		return fmt.Errorf("backup status: %w", errBackup)
	}
	return errCluster
}

// ── Backup section ────────────────────────────────────────────────────────────

func printBackupStatus(ctx context.Context, bucket, region string) error {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return fmt.Errorf("loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(awsCfg)

	entries, err := listBackups(ctx, s3Client, bucket)
	if err != nil {
		return err
	}

	// Sort newest-first.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].LastModified.After(entries[j].LastModified)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintf(w, "DR BACKUP INVENTORY  (s3://%s)\n", bucket)
	fmt.Fprintln(w, "KEY\tSIZE\tAGE\tRPO")
	fmt.Fprintln(w, "───\t────\t───\t───")

	for _, e := range entries {
		age := time.Since(e.LastModified)
		rpoLabel := "✓ ok"
		if age > rpo {
			rpoLabel = "✗ BREACH"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			e.Key,
			humanBytes(e.SizeBytes),
			humanDuration(age),
			rpoLabel,
		)
	}

	if len(entries) == 0 {
		fmt.Fprintln(w, "(no backups found under dr-backups/)")
	} else {
		latestAge := time.Since(entries[0].LastModified)
		if latestAge > rpo {
			fmt.Fprintf(w, "\n  ⚠  RPO BREACH: latest backup is %s old (RPO = %s)\n",
				humanDuration(latestAge), rpo)
		}
	}

	return w.Flush()
}

func listBackups(ctx context.Context, s3Client *s3.Client, bucket string) ([]backupEntry, error) {
	var entries []backupEntry

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucket),
		Prefix: aws.String("dr-backups/"),
	})

	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing s3://%s/dr-backups/: %w", bucket, err)
		}
		for _, obj := range page.Contents {
			key := aws.ToString(obj.Key)
			if !strings.HasSuffix(key, ".json.gz") {
				continue
			}
			entries = append(entries, backupEntry{
				Key:          key,
				LastModified: aws.ToTime(obj.LastModified),
				SizeBytes:    aws.ToInt64(obj.Size),
			})
		}
	}

	return entries, nil
}

// ── Cluster health section ────────────────────────────────────────────────────

func printClusterHealth(ctx context.Context, kubeconfig string) error {
	restCfg, err := kubeRESTConfig(kubeconfig)
	if err != nil {
		fmt.Println("CLUSTER HEALTH  ✗ unreachable (kubeconfig not found or invalid)")
		return nil //nolint:nilerr — treat missing cluster as a warning, not a fatal error
	}

	k8sClient, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("building kubernetes client: %w", err)
	}
	dynClient, err := dynamic.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	// ── Nodes ────────────────────────────────────────────────────────────────
	nodes, err := k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing nodes: %w", err)
	}

	readyCount := 0
	for _, n := range nodes.Items {
		for _, c := range n.Status.Conditions {
			if string(c.Type) == "Ready" && string(c.Status) == "True" {
				readyCount++
			}
		}
	}

	// ── TaoNode CRs ───────────────────────────────────────────────────────────
	taoGVR := schema.GroupVersionResource{
		Group: "tao.guardian.io", Version: "v1alpha1", Resource: "taonodes",
	}

	taoList, err := dynClient.Resource(taoGVR).Namespace("").List(ctx, metav1.ListOptions{})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CLUSTER HEALTH")
	fmt.Fprintln(w, "──────────────────────────────────────────────")
	fmt.Fprintf(w, "  K8s API:\t✓ reachable\n")
	fmt.Fprintf(w, "  Nodes:\t%d Ready / %d Total\n", readyCount, len(nodes.Items))

	if err != nil {
		fmt.Fprintf(w, "  TaoNode CRD:\t✗ not installed (run 'make install' first)\n")
	} else {
		phaseCounts := taoNodePhaseCounts(taoList)
		fmt.Fprintf(w, "  TaoNodes:\t%d total\n", len(taoList.Items))
		for _, phase := range []string{"Synced", "Syncing", "Degraded", "Recovering", "Failed", "Pending", "Unknown"} {
			if n := phaseCounts[phase]; n > 0 {
				fmt.Fprintf(w, "    %-14s\t%d\n", phase+":", n)
			}
		}
	}

	return w.Flush()
}

func taoNodePhaseCounts(list *unstructured.UnstructuredList) map[string]int {
	counts := make(map[string]int)
	for _, item := range list.Items {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if phase == "" {
			phase = "Unknown"
		}
		counts[phase]++
	}
	return counts
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func kubeRESTConfig(kubeconfigPath string) (*rest.Config, error) {
	if kubeconfigPath != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	if cfg, err := rest.InClusterConfig(); err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	case d >= time.Hour:
		return fmt.Sprintf("%.0fh", d.Hours())
	default:
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
}
