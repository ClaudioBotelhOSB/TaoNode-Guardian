output "instance_ip" {
  description = "IP publico da VM na GCP"
  value       = google_compute_instance.k3s.network_interface[0].access_config[0].nat_ip
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${google_compute_instance.k3s.network_interface[0].access_config[0].nat_ip}"
}

output "grafana_url" {
  description = "Instrucoes de acesso ao Grafana"
  value       = <<-EOT
    Grafana URL (NodePort 30030).
    Abra o túnel SSH no WSL2:
      ssh -N -L 30030:localhost:30030 ubuntu@${google_compute_instance.k3s.network_interface[0].access_config[0].nat_ip}
    Navegador: http://localhost:30030
  EOT
}

output "argocd_url" {
  description = "Instrucoes de acesso ao ArgoCD"
  value       = <<-EOT
    ArgoCD URL (NodePort 30080).
    Abra o túnel SSH no WSL2:
      ssh -N -L 30080:localhost:30080 ubuntu@${google_compute_instance.k3s.network_interface[0].access_config[0].nat_ip}
    Navegador: https://localhost:30080
  EOT
}

output "opencost_url" {
  description = "Instrucoes de acesso ao OpenCost"
  value       = <<-EOT
    OpenCost UI (NodePort 30040).
    Abra o túnel SSH no WSL2:
      ssh -N -L 30040:localhost:30040 ubuntu@${google_compute_instance.k3s.network_interface[0].access_config[0].nat_ip}
    Navegador: http://localhost:30040
  EOT
}