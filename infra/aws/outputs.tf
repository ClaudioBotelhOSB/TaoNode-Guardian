output "instance_ip" {
  description = "IP publico da instancia EC2"
  value       = aws_instance.k3s.public_ip
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${aws_instance.k3s.public_ip}"
}

output "grafana_url" {
  description = "URL de acesso direto ao Grafana (Klipper LoadBalancer porta 80)"
  value       = "http://${aws_instance.k3s.public_ip}"
}

output "argocd_url" {
  description = "URL de acesso ao ArgoCD (HTTPS na porta 443)"
  value       = "https://${aws_instance.k3s.public_ip}"
}

output "kubecost_url" {
  description = "URL de acesso ao Kubecost (porta 9090)"
  value       = "http://${aws_instance.k3s.public_ip}:9090"
}