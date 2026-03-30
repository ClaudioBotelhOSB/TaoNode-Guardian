variable "ovh_endpoint" {
  description = "OVH API endpoint. Use 'ovh-eu' for Europe, 'ovh-ca' for Canada, 'ovh-us' for US."
  type        = string
  default     = "ovh-eu"
}

variable "openstack_auth_url" {
  description = "OpenStack Keystone v3 authentication URL for OVH Public Cloud (e.g., https://auth.cloud.ovh.net/v3)."
  type        = string
}

variable "openstack_region" {
  description = "OVH Public Cloud region (e.g., GRA11, SBG5, BHS5, WAW1, DE1)."
  type        = string
  default     = "GRA11"
}

variable "openstack_project_id" {
  description = "OVH Public Cloud project ID (OpenStack tenant ID). Found in the OVH Manager under Public Cloud > Project Settings."
  type        = string
}

variable "ssh_public_key" {
  description = "SSH public key material to inject into the instance. Pass the contents of ~/.ssh/id_rsa.pub or equivalent."
  type        = string
}

variable "flavor_name" {
  description = "OVH Public Cloud instance flavor. b3-16 provides 4 vCPU / 16 GB RAM, equivalent to AWS t3.xlarge for K3s single-node with ArgoCD, Grafana, ClickHouse, Kubecost and TaoNode Operator."
  type        = string
  default     = "b3-16"
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
