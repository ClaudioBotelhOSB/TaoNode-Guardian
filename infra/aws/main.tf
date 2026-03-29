terraform {
  required_version = ">= 1.9"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.60"
    }
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project   = "taonode-guardian"
      ManagedBy = "terraform"
      Tier      = "k3s-dev"
    }
  }
}

# ── AMI: Ubuntu 24.04 LTS (gp3-backed HVM) ───────────────────────────────────

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-*"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

# ── Network ───────────────────────────────────────────────────────────────────

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "k3s" {
  cidr_block           = "10.10.0.0/16"
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = "taonode-guardian-k3s-vpc" }
}

resource "aws_internet_gateway" "k3s" {
  vpc_id = aws_vpc.k3s.id
  tags   = { Name = "taonode-guardian-k3s-igw" }
}

resource "aws_subnet" "public" {
  vpc_id                  = aws_vpc.k3s.id
  cidr_block              = "10.10.0.0/24"
  availability_zone       = data.aws_availability_zones.available.names[0]
  map_public_ip_on_launch = true
  tags                    = { Name = "taonode-guardian-k3s-public" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.k3s.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.k3s.id
  }

  tags = { Name = "taonode-guardian-k3s-rt" }
}

resource "aws_route_table_association" "public" {
  subnet_id      = aws_subnet.public.id
  route_table_id = aws_route_table.public.id
}

# ── Security Group ────────────────────────────────────────────────────────────
# Ingress is restricted to the four ports required for SSH, K3s API, and
# standard web traffic. NodePort services (Grafana, ArgoCD, OpenCost) are
# accessed via SSH tunnels, not direct internet exposure.

resource "aws_security_group" "k3s" {
  name        = "taonode-guardian-k3s-sg"
  description = "K3s single-node - ingress on 22 (SSH), 6443 (K3s API), 80 (HTTP), 443 (HTTPS)."
  vpc_id      = aws_vpc.k3s.id

  ingress {
    description = "SSH"
    from_port   = 22
    to_port     = 22
    protocol    = "tcp"
    cidr_blocks = var.admin_cidrs
  }

  ingress {
    description = "K3s API server"
    from_port   = 6443
    to_port     = 6443
    protocol    = "tcp"
    cidr_blocks = var.admin_cidrs
  }

  ingress {
    description = "HTTP"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  ingress {
    description = "HTTPS"
    from_port   = 443
    to_port     = 443
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "Allow all outbound"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = { Name = "taonode-guardian-k3s-sg" }
}

# ── SSH Key Pair ──────────────────────────────────────────────────────────────

resource "aws_key_pair" "deployer" {
  key_name   = "taonode-guardian-deployer"
  public_key = var.ssh_public_key
}

# ── EC2 Instance ──────────────────────────────────────────────────────────────
# user_data runs bootstrap.sh as root via cloud-init on first boot.
# Use file() — not templatefile() — to avoid Terraform interpolating bash ${} syntax.

resource "aws_instance" "k3s" {
  ami                    = data.aws_ami.ubuntu.id
  instance_type          = var.instance_type
  key_name               = aws_key_pair.deployer.key_name
  subnet_id              = aws_subnet.public.id
  vpc_security_group_ids = [aws_security_group.k3s.id]

  user_data = file("${path.module}/scripts/bootstrap.sh")

  root_block_device {
    volume_type           = "gp3"
    volume_size           = 100
    iops                  = 3000
    throughput            = 125
    encrypted             = true
    delete_on_termination = true
  }

  # FinOps: Spot reduces cost by ~70 % vs on-demand for t3.2xlarge (~$0.33/hr → ~$0.09/hr).
  # Spot interruption triggers a 2-minute warning; pair with PodDisruptionBudgets in production.
  dynamic "instance_market_options" {
    for_each = var.use_spot ? [1] : []
    content {
      market_type = "spot"
      spot_options {
        max_price                      = var.spot_price
        instance_interruption_behavior = "terminate"
        spot_instance_type             = "one-time"
      }
    }
  }

  tags = { Name = "taonode-guardian-k3s" }
}
