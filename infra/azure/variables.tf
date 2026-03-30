variable "location" {
  description = "Azure region for the K3s VM."
  type        = string
  default     = "eastus"
}

variable "ssh_public_key" {
  description = "SSH public key material to inject into the VM. Pass the contents of ~/.ssh/id_rsa.pub or equivalent."
  type        = string
}

variable "vm_size" {
  description = "Azure VM size. Standard_D4s_v5 (4 vCPU / 16 GB RAM) for K3s single-node with ArgoCD, Grafana, ClickHouse, Kubecost and TaoNode Operator."
  type        = string
  default     = "Standard_D4s_v5"
}

variable "use_spot" {
  description = "Launch a Spot VM for ~65 % cost reduction. Spot evictions delete the VM with 30-second notice. Set to false for stable demo or production workloads."
  type        = bool
  default     = true
}

variable "spot_price" {
  description = "Maximum Spot bid price in USD/hr. Default $0.05 is above the typical Spot market price for Standard_D4s_v5 but below on-demand (~$0.192/hr)."
  type        = string
  default     = "0.05"
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
