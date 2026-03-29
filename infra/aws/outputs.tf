output "instance_ip" {
  description = "IP publico da instancia EC2"
  value       = aws_instance.k3s.public_ip
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${aws_instance.k3s.public_ip}"
}

output "grafana_url" {
  description = "Instrucoes de acesso ao Grafana"
  value       = <<-EOT
    Grafana URL (NodePort 30030). O Security Group bloqueia acesso direto.
    Primeiro, abra o túnel SSH no WSL2:
      ssh -N -L 30030:localhost:30030 ubuntu@${aws_instance.k3s.public_ip}
    Depois abra no navegador do Windows: http://localhost:30030
  EOT
}

output "argocd_url" {
  description = "Instrucoes de acesso ao ArgoCD"
  value       = <<-EOT
    ArgoCD URL (NodePort 30080). O Security Group bloqueia acesso direto.
    Primeiro, abra o túnel SSH no WSL2:
      ssh -N -L 30080:localhost:30080 ubuntu@${aws_instance.k3s.public_ip}
    Depois abra no navegador do Windows: https://localhost:30080
  EOT
}

output "opencost_url" {
  description = "Instrucoes de acesso ao OpenCost"
  value       = <<-EOT
    OpenCost UI (NodePort 30040). O Security Group bloqueia acesso direto.
    Primeiro, abra o túnel SSH no WSL2:
      ssh -N -L 30040:localhost:30040 ubuntu@${aws_instance.k3s.public_ip}
    Depois abra no navegador do Windows: http://localhost:30040
  EOT
}