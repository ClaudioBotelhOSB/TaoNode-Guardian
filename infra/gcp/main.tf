terraform {
  required_version = ">= 1.9"
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

# -- VPC Network ---------------------------------------------------------------

resource "google_compute_network" "k3s" {
  name                    = "taonode-guardian-k3s-vpc"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "k3s" {
  name          = "taonode-guardian-k3s-subnet"
  ip_cidr_range = "10.10.0.0/24"
  region        = var.region
  network       = google_compute_network.k3s.id
}

# -- Firewall Rules ------------------------------------------------------------
# Same ports as AWS security group: 22, 80, 443, 3000, 3001, 6443, 8080, 8123, 9000, 9090

resource "google_compute_firewall" "k3s_ssh" {
  name    = "taonode-guardian-k3s-allow-ssh"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["22"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "SSH access"
}

resource "google_compute_firewall" "k3s_http_https" {
  name    = "taonode-guardian-k3s-allow-http-https"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["80", "443"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "HTTP and HTTPS access"
}

resource "google_compute_firewall" "k3s_grafana" {
  name    = "taonode-guardian-k3s-allow-grafana"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["3000", "3001"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "Grafana and Grafana alt"
}

resource "google_compute_firewall" "k3s_api" {
  name    = "taonode-guardian-k3s-allow-k3s-api"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["6443"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "K3s API server"
}

resource "google_compute_firewall" "k3s_argocd" {
  name    = "taonode-guardian-k3s-allow-argocd"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["8080"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "ArgoCD HTTP"
}

resource "google_compute_firewall" "k3s_clickhouse" {
  name    = "taonode-guardian-k3s-allow-clickhouse"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["8123", "9000"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "ClickHouse HTTP and Native (admin only)"
}

resource "google_compute_firewall" "k3s_kubecost" {
  name    = "taonode-guardian-k3s-allow-kubecost"
  network = google_compute_network.k3s.name

  allow {
    protocol = "tcp"
    ports    = ["9090"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
  description   = "Kubecost"
}

resource "google_compute_firewall" "k3s_egress" {
  name      = "taonode-guardian-k3s-allow-egress"
  network   = google_compute_network.k3s.name
  direction = "EGRESS"

  allow {
    protocol = "all"
  }

  destination_ranges = ["0.0.0.0/0"]
  target_tags        = ["k3s-node"]
  description        = "Allow all outbound"
}

# -- Static External IP -------------------------------------------------------

resource "google_compute_address" "k3s" {
  name         = "taonode-guardian-k3s-ip"
  region       = var.region
  address_type = "EXTERNAL"
}

# -- Compute Instance ----------------------------------------------------------
# n2-standard-4 = 4 vCPU / 16 GB RAM (equivalent to AWS t3.xlarge)

resource "google_compute_instance" "k3s" {
  name         = "taonode-guardian-k3s"
  machine_type = var.machine_type
  zone         = var.zone
  tags         = ["k3s-node"]

  labels = {
    project    = "taonode-guardian"
    managed-by = "terraform"
    tier       = "k3s-dev"
  }

  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2204-lts"
      size  = 100
      type  = "pd-ssd"
    }
  }

  network_interface {
    network    = google_compute_network.k3s.name
    subnetwork = google_compute_subnetwork.k3s.name
    access_config {
      nat_ip = google_compute_address.k3s.address
    }
  }

  metadata = {
    "ssh-keys" = "ubuntu:${var.ssh_public_key}"
  }

  # user_data equivalent: metadata_startup_script runs as root on first boot.
  # Tokens are injected via heredoc — do NOT commit real values to Git.
  metadata_startup_script = <<-EOF
    #!/bin/bash
    # Injected by Terraform — do NOT commit these values to Git.
    export GITHUB_TOKEN="${var.github_token}"
    export GHCR_PAT="${var.ghcr_pat}"
    ${file("${path.module}/scripts/bootstrap.sh")}
  EOF

  # FinOps: Preemptible / Spot reduces cost by ~60-80 % vs on-demand.
  # Spot VMs can be reclaimed with 30 s notice; pair with PodDisruptionBudgets in production.
  dynamic "scheduling" {
    for_each = var.use_spot ? [1] : []
    content {
      preemptible                 = true
      provisioning_model          = "SPOT"
      automatic_restart           = false
      instance_termination_action = "DELETE"
    }
  }

  dynamic "scheduling" {
    for_each = var.use_spot ? [] : [1]
    content {
      automatic_restart = true
    }
  }
}
