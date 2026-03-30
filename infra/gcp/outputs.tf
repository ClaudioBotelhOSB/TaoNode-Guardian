output "instance_ip" {
  description = "IP publico da instancia GCE"
  value       = google_compute_address.k3s.address
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${google_compute_address.k3s.address}"
}

output "grafana_url" {
  description = "URL de acesso direto ao Grafana (Klipper LoadBalancer porta 3000)"
  value       = "http://${google_compute_address.k3s.address}:3000"
}

output "argocd_url" {
  description = "URL de acesso ao ArgoCD (HTTP porta 8080)"
  value       = "http://${google_compute_address.k3s.address}:8080"
}

output "kubecost_url" {
  description = "URL de acesso ao Kubecost FinOps (porta 9090)"
  value       = "http://${google_compute_address.k3s.address}:9090"
}

output "clickhouse_url" {
  description = "URL de acesso ao ClickHouse HTTP (porta 8123)"
  value       = "http://${google_compute_address.k3s.address}:8123/play"
}
