// Package failover implements the `tao-dr failover` command.
// It orchestrates Terraform (init → plan → apply) via tfexec to provision
// a replacement cluster. Use --dry-run to print the plan and exit without
// making any infrastructure changes.
package failover

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/hashicorp/terraform-exec/tfexec"
	"github.com/spf13/cobra"
)

// NewCommand returns the `failover` cobra.Command.
// The s3Bucket, s3Region, and kubeconfig pointers are accepted for API
// consistency with the other subcommands but are not used by failover itself.
func NewCommand(s3Bucket, s3Region, kubeconfig *string) *cobra.Command {
	var (
		tfDir   string
		tfVars  []string
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "failover",
		Short: "Provision a replacement cluster via Terraform",
		Long: `Runs Terraform in the supplied --tf-dir to stand up a replacement
cluster. The workflow is:

  1. terraform init   — download providers and modules
  2. terraform plan   — compute the change set (always printed)
  3. terraform apply  — execute changes (skipped when --dry-run is set)

Pass Terraform variables with --tf-var key=value (repeatable).
Use --dry-run to inspect the plan without touching infrastructure.`,
		Example: `  # Dry-run: show the Terraform plan only
  tao-dr failover --tf-dir infra/terraform --dry-run

  # Full failover with variable overrides
  tao-dr failover \
    --tf-dir infra/terraform \
    --tf-var region=eu-west-1 \
    --tf-var instance_type=t3.xlarge`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if tfDir == "" {
				return fmt.Errorf("--tf-dir is required")
			}
			return run(cmd.Context(), tfDir, tfVars, dryRun)
		},
	}

	cmd.Flags().StringVar(&tfDir, "tf-dir", "",
		"Path to the Terraform working directory (required)")
	cmd.Flags().StringArrayVar(&tfVars, "tf-var", nil,
		"Terraform variable in key=value format (repeatable)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"Print the Terraform plan and exit without applying")

	_ = cmd.MarkFlagRequired("tf-dir")

	return cmd
}

func run(ctx context.Context, tfDir string, tfVarPairs []string, dryRun bool) error {
	// ── 1. Locate terraform binary ─────────────────────────────────────────────
	tfBin, err := exec.LookPath("terraform")
	if err != nil {
		return fmt.Errorf("terraform binary not found in PATH: %w\n"+
			"Install Terraform from https://developer.hashicorp.com/terraform/downloads", err)
	}
	fmt.Printf("[failover] Using terraform binary: %s\n", tfBin)

	// ── 2. Resolve working directory ───────────────────────────────────────────
	absDir, err := resolvePath(tfDir)
	if err != nil {
		return fmt.Errorf("resolving --tf-dir %q: %w", tfDir, err)
	}

	tf, err := tfexec.NewTerraform(absDir, tfBin)
	if err != nil {
		return fmt.Errorf("initializing tfexec: %w", err)
	}

	// Stream Terraform output directly to the user's terminal.
	tf.SetStdout(os.Stdout)
	tf.SetStderr(os.Stderr)

	// ── 3. Parse --tf-var flags into tfexec options ────────────────────────────
	planVarOpts, applyVarOpts, err := buildVarOpts(tfVarPairs)
	if err != nil {
		return err
	}

	// ── 4. terraform init ──────────────────────────────────────────────────────
	fmt.Println("[failover] Running: terraform init")
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		return fmt.Errorf("terraform init: %w", err)
	}

	// ── 5. terraform plan ──────────────────────────────────────────────────────
	fmt.Println("[failover] Running: terraform plan")
	planFile := ""
	if !dryRun {
		// Save the plan to a file so apply uses the exact same change set.
		planFile = "tao-dr-failover.tfplan"
	}

	planOpts := append([]tfexec.PlanOption{}, planVarOpts...)
	if planFile != "" {
		planOpts = append(planOpts, tfexec.Out(planFile))
	}

	hasChanges, err := tf.Plan(ctx, planOpts...)
	if err != nil {
		return fmt.Errorf("terraform plan: %w", err)
	}

	if !hasChanges {
		fmt.Println("[failover] Plan: no infrastructure changes required.")
		return nil
	}

	// ── 6. Dry-run: exit after plan ────────────────────────────────────────────
	if dryRun {
		fmt.Println("\n[dry-run] Plan complete. No changes applied (--dry-run).")
		return nil
	}

	// ── 7. terraform apply ─────────────────────────────────────────────────────
	fmt.Println("[failover] Running: terraform apply")
	applyOpts := append([]tfexec.ApplyOption{tfexec.DirOrPlan(planFile)}, applyVarOpts...)
	if err := tf.Apply(ctx, applyOpts...); err != nil {
		return fmt.Errorf("terraform apply: %w", err)
	}

	fmt.Println("[failover] Failover complete. Verify the new cluster with 'tao-dr status'.")
	return nil
}

// buildVarOpts converts "key=value" strings into separate Plan and Apply
// option slices (tfexec does not share option types between the two calls).
func buildVarOpts(pairs []string) ([]tfexec.PlanOption, []tfexec.ApplyOption, error) {
	var planOpts []tfexec.PlanOption
	var applyOpts []tfexec.ApplyOption

	for _, pair := range pairs {
		k, v, found := strings.Cut(pair, "=")
		if !found || k == "" {
			return nil, nil, fmt.Errorf("invalid --tf-var %q: expected key=value format", pair)
		}
		planOpts = append(planOpts, tfexec.Var(fmt.Sprintf("%s=%s", k, v)))
		applyOpts = append(applyOpts, tfexec.Var(fmt.Sprintf("%s=%s", k, v)))
	}

	return planOpts, applyOpts, nil
}

// resolvePath returns the absolute path for dir, creating it if absent.
func resolvePath(dir string) (string, error) {
	info, err := os.Stat(dir)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("directory does not exist: %s", dir)
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	// os.Stat already resolved symlinks; return the cleaned path.
	abs, err := os.Getwd()
	if err != nil {
		return dir, nil
	}
	if strings.HasPrefix(dir, "/") || (len(dir) > 1 && dir[1] == ':') {
		return dir, nil // already absolute
	}
	return abs + "/" + dir, nil
}
