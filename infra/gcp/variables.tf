variable "project_id" {
  description = "GCP project ID where resources will be created."
  type        = string
}

variable "region" {
  description = "GCP region for the K3s instance."
  type        = string
  default     = "us-east1"
}

variable "zone" {
  description = "GCP zone for the K3s instance."
  type        = string
  default     = "us-east1-b"
}

variable "ssh_public_key" {
  description = "SSH public key material to inject into the GCE instance. Pass the contents of ~/.ssh/id_rsa.pub or equivalent."
  type        = string
}

variable "machine_type" {
  description = "GCE machine type. n2-standard-4 (4 vCPU / 16 GB RAM) for K3s single-node with ArgoCD, Grafana, ClickHouse, Kubecost and TaoNode Operator."
  type        = string
  default     = "n2-standard-4"
}

variable "use_spot" {
  description = "Launch a Spot (preemptible) VM for ~60-80 % cost reduction. Spot VMs can be reclaimed with 30 s notice. Set to false for stable demo or production workloads."
  type        = bool
  default     = true
}

variable "github_token" {
  description = "GitHub fine-grained PAT with 'Contents: Read' scope — used to clone the private repo during bootstrap."
  type        = string
  sensitive   = true
}

variable "ghcr_pat" {
  description = "GitHub PAT with 'read:packages' scope — used to pull ghcr.io/claudiobotelhosb/taonode-guardian:latest. Injected as the imagePullSecret 'ghcr-login-secret' in taonode-guardian-system namespace."
  type        = string
  sensitive   = true
}

variable "admin_cidrs" {
  description = "CIDR blocks allowed to access all ports. Use [\"0.0.0.0/0\"] for public demo access."
  type        = list(string)
  default     = ["0.0.0.0/0"]

  validation {
    condition     = length(var.admin_cidrs) > 0
    error_message = "admin_cidrs must contain at least one CIDR."
  }
}
