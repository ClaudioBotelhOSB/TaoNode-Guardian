output "instance_public_ip" {
  description = "Public IP address of the K3s instance."
  value       = aws_instance.k3s.public_ip
}

output "ssh_command" {
  description = "SSH command to connect to the instance as ubuntu user."
  value       = "ssh -i ~/.ssh/id_rsa ubuntu@${aws_instance.k3s.public_ip}"
}

output "kubeconfig_command" {
  description = <<-EOT
    Commands to extract the K3s kubeconfig and configure it locally.
    The 127.0.0.1 server address in the raw file must be replaced with the public IP.
  EOT
  value       = <<-EOT
    scp -i ~/.ssh/id_rsa ubuntu@${aws_instance.k3s.public_ip}:/etc/rancher/k3s/k3s.yaml ~/.kube/taonode-guardian.yaml
    sed -i 's|127.0.0.1|${aws_instance.k3s.public_ip}|g' ~/.kube/taonode-guardian.yaml
    export KUBECONFIG=~/.kube/taonode-guardian.yaml
    kubectl get nodes
  EOT
}

output "grafana_url" {
  description = <<-EOT
    Grafana URL (NodePort 30030). The Security Group restricts direct access.
    Open an SSH tunnel first:
      ssh -N -L 30030:localhost:30030 ubuntu@${aws_instance.k3s.public_ip}
    Then open: http://localhost:30030 (admin / guardian)
  EOT
  value       = "http://${aws_instance.k3s.public_ip}:30030"
}

output "argocd_url" {
  description = <<-EOT
    ArgoCD URL (NodePort 30080). The Security Group restricts direct access.
    Open an SSH tunnel first:
      ssh -N -L 30080:localhost:30080 ubuntu@${aws_instance.k3s.public_ip}
    Then open: https://localhost:30080
    Retrieve initial admin password:
      kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d
  EOT
  value       = "https://${aws_instance.k3s.public_ip}:30080"
}

output "opencost_url" {
  description = <<-EOT
    OpenCost UI (NodePort 30040). The Security Group restricts direct access.
    Open an SSH tunnel first:
      ssh -N -L 30040:localhost:30040 ubuntu@${aws_instance.k3s.public_ip}
    Then open: http://localhost:30040
  EOT
  value       = "http://${aws_instance.k3s.public_ip}:30040"
}

output "bootstrap_log" {
  description = "Command to tail the cloud-init bootstrap log on the instance."
  value       = "ssh -i ~/.ssh/id_rsa ubuntu@${aws_instance.k3s.public_ip} 'sudo tail -f /var/log/cloud-init-output.log'"
}
