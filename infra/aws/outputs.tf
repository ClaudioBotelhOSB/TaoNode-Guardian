output "instance_ip" {
  description = "IP publico da instancia EC2"
  value       = aws_instance.k3s.public_ip
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${aws_instance.k3s.public_ip}"
}

output "grafana_url" {
  description = "URL de acesso direto ao Grafana (NodePort 30030)"
  value       = "http://${aws_instance.k3s.public_ip}:30030"
}

output "argocd_url" {
  description = "URL de acesso ao ArgoCD (HTTPS na porta 443)"
  value       = "https://${aws_instance.k3s.public_ip}"
}

output "kubecost_url" {
  description = "URL de acesso ao Kubecost (porta 9090)"
  value       = "http://${aws_instance.k3s.public_ip}:9090"
}