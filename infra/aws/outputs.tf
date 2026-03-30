output "instance_ip" {
  description = "IP publico da instancia EC2"
  value       = aws_instance.k3s.public_ip
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${aws_instance.k3s.public_ip}"
}

output "grafana_url" {
  description = "URL de acesso direto ao Grafana (Klipper LoadBalancer porta 3000)"
  value       = "http://${aws_instance.k3s.public_ip}:3000"
}

output "argocd_url" {
  description = "URL de acesso ao ArgoCD (HTTP porta 8080)"
  value       = "http://${aws_instance.k3s.public_ip}:8080"
}

output "kubecost_url" {
  description = "URL de acesso ao Kubecost FinOps (porta 9090)"
  value       = "http://${aws_instance.k3s.public_ip}:9090"
}

output "clickhouse_url" {
  description = "URL de acesso ao ClickHouse HTTP (porta 8123)"
  value       = "http://${aws_instance.k3s.public_ip}:8123/play"
}