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
  description = "EC2 instance type. t3.2xlarge (8 vCPU / 32 GB RAM) covers K3s control-plane + all stack components (Prometheus, ClickHouse, Ollama, Operator)."
  type        = string
  default     = "t3.2xlarge"
}

variable "use_spot" {
  description = "Launch a Spot instance for ~70 % cost reduction. Spot interruptions terminate with 2-minute notice. Set to false for stable demo or production workloads."
  type        = bool
  default     = true
}

variable "spot_price" {
  description = "Maximum Spot bid price in USD/hr. Default $0.15 is well above the typical Spot market price for t3.2xlarge (~$0.09) but below on-demand ($0.33)."
  type        = string
  default     = "0.15"
}

variable "admin_cidrs" {
  description = "Trusted CIDRs allowed to reach SSH and the K3s API. Use VPN/office /32s, never 0.0.0.0/0 in production."
  type        = list(string)
}
