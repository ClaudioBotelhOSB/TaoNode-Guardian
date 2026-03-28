variable "aws_region" {
  description = "AWS region where all resources are provisioned."
  type        = string
  default     = "us-east-1"
}

variable "cluster_name" {
  description = "Name of the EKS cluster. Used as a prefix for derived resource names."
  type        = string
  default     = "taonode-guardian"
}

variable "kubernetes_version" {
  description = "Kubernetes version for the EKS control plane."
  type        = string
  default     = "1.31"
}

variable "vpc_cidr" {
  description = "CIDR block for the dedicated VPC."
  type        = string
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "CIDR blocks for public subnets, one per AZ."
  type        = list(string)
  default     = ["10.0.0.0/24", "10.0.1.0/24"]
}

variable "private_subnet_cidrs" {
  description = "CIDR blocks for private subnets, one per AZ."
  type        = list(string)
  default     = ["10.0.10.0/23", "10.0.12.0/23"]
}

variable "node_instance_types" {
  description = "EC2 instance type candidates for the Spot node group. Multiple types widen the Spot capacity pool and reduce interruption risk."
  type        = list(string)
  default     = ["m5.xlarge", "m5a.xlarge", "m4.xlarge", "m5d.xlarge"]
}

variable "node_group_min_size" {
  description = "Minimum number of nodes in the managed node group."
  type        = number
  default     = 1
}

variable "node_group_desired_size" {
  description = "Desired number of nodes at cluster creation."
  type        = number
  default     = 2
}

variable "node_group_max_size" {
  description = "Maximum number of nodes the cluster autoscaler may scale up to."
  type        = number
  default     = 6
}

variable "clickhouse_chart_version" {
  description = "Version of the Bitnami ClickHouse Helm chart to install."
  type        = string
  default     = "6.2.13"
}

variable "clickhouse_storage_size" {
  description = "Persistent volume size for the ClickHouse data directory."
  type        = string
  default     = "100Gi"
}

variable "clickhouse_replicas" {
  description = "Number of ClickHouse shard replicas."
  type        = number
  default     = 1
}

variable "clickhouse_admin_password" {
  description = "Admin password for the ClickHouse instance. Supply via TF_VAR_clickhouse_admin_password or a secrets manager — never commit to source control."
  type        = string
  sensitive   = true
}

variable "default_tags" {
  description = "Tags applied to every resource via the AWS provider default_tags block. Enables cost allocation in AWS Cost Explorer without per-resource tagging."
  type        = map(string)
  default = {
    Environment = "production"
    Project     = "taonode-guardian"
    ManagedBy   = "terraform"
    Owner       = "platform-team"
    CostCenter  = "bittensor-infra"
  }
}
