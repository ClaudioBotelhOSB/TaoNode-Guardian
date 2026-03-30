output "instance_ip" {
  description = "IP publico da instancia OVH Public Cloud"
  value       = openstack_networking_floatingip_v2.k3s.address
}

output "ssh_command" {
  description = "Comando de acesso SSH direto"
  value       = "ssh -i ~/.ssh/taonode-demo ubuntu@${openstack_networking_floatingip_v2.k3s.address}"
}

output "grafana_url" {
  description = "URL de acesso direto ao Grafana (Klipper LoadBalancer porta 3000)"
  value       = "http://${openstack_networking_floatingip_v2.k3s.address}:3000"
}

output "argocd_url" {
  description = "URL de acesso ao ArgoCD (HTTP porta 8080)"
  value       = "http://${openstack_networking_floatingip_v2.k3s.address}:8080"
}

output "kubecost_url" {
  description = "URL de acesso ao Kubecost FinOps (porta 9090)"
  value       = "http://${openstack_networking_floatingip_v2.k3s.address}:9090"
}

output "clickhouse_url" {
  description = "URL de acesso ao ClickHouse HTTP (porta 8123)"
  value       = "http://${openstack_networking_floatingip_v2.k3s.address}:8123/play"
}
