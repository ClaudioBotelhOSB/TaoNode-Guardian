variable "region" {
  description = "AWS region for the K3s instance."
  type        = string
  default     = "us-east-1"
}

variable "ssh_public_key" {
  description = "SSH public key material to inject into the EC2 instance. Pass the contents of ~/.ssh/id_rsa.pub or equivalent."
  type        = string
}

variable "instance_type" {
  description = "EC2 instance type. t3.medium (2 vCPU / 4 GB RAM) for lightweight K3s single-node with ArgoCD, Grafana and Kubecost."
  type        = string
  default     = "t3.medium"
}

variable "use_spot" {
  description = "Launch a Spot instance for ~70 % cost reduction. Spot interruptions terminate with 2-minute notice. Set to false for stable demo or production workloads."
  type        = bool
  default     = true
}

variable "spot_price" {
  description = "Maximum Spot bid price in USD/hr. Default $0.03 is above the typical Spot market price for t3.medium (~$0.013) but below on-demand ($0.042)."
  type        = string
  default     = "0.03"
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
