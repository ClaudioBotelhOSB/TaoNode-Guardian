variable "project_id" {
  description = "ID do Projeto na GCP"
  type        = string
}

variable "region" {
  description = "Regiao da GCP"
  default     = "us-central1"
}

variable "zone" {
  description = "Zona da GCP"
  default     = "us-central1-a"
}

variable "machine_type" {
  description = "Tamanho da VM (Equivalente a t3.2xlarge)"
  default     = "e2-standard-8" # 8 vCPUs, 32GB RAM
}

variable "admin_cidrs" {
  description = "IPs permitidos. Use [\"0.0.0.0/0\"] para a demo."
  type        = list(string)
}