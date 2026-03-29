terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
  zone    = var.zone
}

# Rede Customizada
resource "google_compute_network" "vpc" {
  name                    = "vpc-taonode"
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "subnet" {
  name          = "subnet-taonode"
  ip_cidr_range = "10.0.1.0/24"
  region        = var.region
  network       = google_compute_network.vpc.id
}

# Firewall Rule (O equivalente ao Security Group)
resource "google_compute_firewall" "allow_k3s" {
  name    = "fw-taonode-allow-k3s"
  network = google_compute_network.vpc.name

  allow {
    protocol = "tcp"
    ports    = ["22", "80", "443", "6443"]
  }

  source_ranges = var.admin_cidrs
  target_tags   = ["k3s-node"]
}

# Ler a chave SSH pública
data "local_file" "ssh_key" {
  filename = pathexpand("~/.ssh/taonode-demo.pub")
}

# A Instância EC2 (Compute Engine VM)
resource "google_compute_instance" "k3s" {
  name         = "vm-taonode-k3s"
  machine_type = var.machine_type
  zone         = var.zone
  tags         = ["k3s-node"]

  boot_disk {
    initialize_params {
      image = "ubuntu-os-cloud/ubuntu-2404-lts-amd64"
      size  = 60
      type  = "pd-ssd"
    }
  }

  network_interface {
    network    = google_compute_network.vpc.name
    subnetwork = google_compute_subnetwork.subnet.name
    access_config {
      # Isso garante que a VM receba um IP Público Efêmero
    }
  }

  metadata = {
    # Injeta a chave SSH para o usuário 'ubuntu'
    "ssh-keys" = "ubuntu:${data.local_file.ssh_key.content}"
  }

  # Executa o nosso bootstrap.sh no primeiro boot
  metadata_startup_script = file("${path.module}/bootstrap.sh")
}