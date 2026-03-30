terraform {
  required_version = ">= 1.9"
  required_providers {
    ovh = {
      source  = "ovh/ovh"
      version = "~> 1.0"
    }
    openstack = {
      source  = "terraform-provider-openstack/openstack"
      version = "~> 3.0"
    }
  }
}

provider "ovh" {
  endpoint = var.ovh_endpoint
}

provider "openstack" {
  auth_url    = var.openstack_auth_url
  region      = var.openstack_region
  tenant_id   = var.openstack_project_id
}

# ── Networking ───────────────────────────────────────────────────────────────

resource "openstack_networking_network_v2" "k3s" {
  name           = "taonode-guardian-k3s-net"
  admin_state_up = true
}

resource "openstack_networking_subnet_v2" "k3s" {
  name            = "taonode-guardian-k3s-subnet"
  network_id      = openstack_networking_network_v2.k3s.id
  cidr            = "10.10.0.0/24"
  ip_version      = 4
  dns_nameservers = ["213.186.33.99", "1.1.1.1", "8.8.8.8"]
}

data "openstack_networking_network_v2" "ext_net" {
  name = "Ext-Net"
}

resource "openstack_networking_router_v2" "k3s" {
  name                = "taonode-guardian-k3s-router"
  admin_state_up      = true
  external_network_id = data.openstack_networking_network_v2.ext_net.id
}

resource "openstack_networking_router_interface_v2" "k3s" {
  router_id = openstack_networking_router_v2.k3s.id
  subnet_id = openstack_networking_subnet_v2.k3s.id
}

# ── Security Group ────────────────────────────────────────────────────────────

resource "openstack_networking_secgroup_v2" "k3s" {
  name        = "taonode-guardian-k3s-sg"
  description = "K3s single-node - SSH, HTTP, HTTPS, K3s API, Grafana, ArgoCD, Kubecost."
}

resource "openstack_networking_secgroup_rule_v2" "ssh" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "SSH"
}

resource "openstack_networking_secgroup_rule_v2" "http" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 80
  port_range_max    = 80
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "HTTP"
}

resource "openstack_networking_secgroup_rule_v2" "https" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "HTTPS"
}

resource "openstack_networking_secgroup_rule_v2" "grafana" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 3000
  port_range_max    = 3000
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "Grafana"
}

resource "openstack_networking_secgroup_rule_v2" "grafana_alt" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 3001
  port_range_max    = 3001
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "Grafana alt"
}

resource "openstack_networking_secgroup_rule_v2" "k3s_api" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 6443
  port_range_max    = 6443
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "K3s API server"
}

resource "openstack_networking_secgroup_rule_v2" "argocd" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 8080
  port_range_max    = 8080
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "ArgoCD HTTP"
}

resource "openstack_networking_secgroup_rule_v2" "clickhouse_http" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 8123
  port_range_max    = 8123
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "ClickHouse HTTP (admin only)"
}

resource "openstack_networking_secgroup_rule_v2" "clickhouse_native" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 9000
  port_range_max    = 9000
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "ClickHouse Native (admin only)"
}

resource "openstack_networking_secgroup_rule_v2" "kubecost" {
  for_each          = toset(var.admin_cidrs)
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 9090
  port_range_max    = 9090
  remote_ip_prefix  = each.value
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "Kubecost"
}

resource "openstack_networking_secgroup_rule_v2" "egress_v4" {
  direction         = "egress"
  ethertype         = "IPv4"
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "Allow all outbound IPv4"
}

resource "openstack_networking_secgroup_rule_v2" "egress_v6" {
  direction         = "egress"
  ethertype         = "IPv6"
  security_group_id = openstack_networking_secgroup_v2.k3s.id
  description       = "Allow all outbound IPv6"
}

# ── Floating IP (Public IP) ──────────────────────────────────────────────────

resource "openstack_networking_floatingip_v2" "k3s" {
  pool = data.openstack_networking_network_v2.ext_net.name
}

# ── SSH Key Pair ──────────────────────────────────────────────────────────────

resource "openstack_compute_keypair_v2" "deployer" {
  name       = "taonode-guardian-deployer"
  public_key = var.ssh_public_key
}

# ── Compute Instance ─────────────────────────────────────────────────────────
# user_data runs bootstrap.sh as root via cloud-init on first boot.

data "openstack_images_image_v2" "ubuntu" {
  name        = "Ubuntu 22.04"
  most_recent = true
}

resource "openstack_compute_instance_v2" "k3s" {
  name            = "taonode-guardian-k3s"
  flavor_name     = var.flavor_name
  key_pair        = openstack_compute_keypair_v2.deployer.name
  security_groups = [openstack_networking_secgroup_v2.k3s.name]

  block_device {
    uuid                  = data.openstack_images_image_v2.ubuntu.id
    source_type           = "image"
    destination_type      = "volume"
    volume_size           = 100
    boot_index            = 0
    delete_on_termination = true
  }

  network {
    uuid = openstack_networking_network_v2.k3s.id
  }

  user_data = <<-EOF
    #!/bin/bash
    # Injected by Terraform — do NOT commit these values to Git.
    export GITHUB_TOKEN="${var.github_token}"
    export GHCR_PAT="${var.ghcr_pat}"
    ${file("${path.module}/scripts/bootstrap.sh")}
  EOF

  depends_on = [
    openstack_networking_router_interface_v2.k3s,
  ]
}

# ── Floating IP Association ──────────────────────────────────────────────────

resource "openstack_compute_floatingip_associate_v2" "k3s" {
  floating_ip = openstack_networking_floatingip_v2.k3s.address
  instance_id = openstack_compute_instance_v2.k3s.id
}
