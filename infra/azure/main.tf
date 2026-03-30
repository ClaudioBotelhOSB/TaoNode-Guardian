terraform {
  required_version = ">= 1.9"
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
}

locals {
  default_tags = {
    Project   = "taonode-guardian"
    ManagedBy = "terraform"
    Tier      = "k3s-dev"
  }
}

# -- Resource Group ------------------------------------------------------------

resource "azurerm_resource_group" "k3s" {
  name     = "taonode-guardian-rg"
  location = var.location
  tags     = local.default_tags
}

# -- Network ------------------------------------------------------------------

resource "azurerm_virtual_network" "k3s" {
  name                = "taonode-guardian-k3s-vnet"
  address_space       = ["10.10.0.0/16"]
  location            = azurerm_resource_group.k3s.location
  resource_group_name = azurerm_resource_group.k3s.name
  tags                = local.default_tags
}

resource "azurerm_subnet" "public" {
  name                 = "taonode-guardian-k3s-public"
  resource_group_name  = azurerm_resource_group.k3s.name
  virtual_network_name = azurerm_virtual_network.k3s.name
  address_prefixes     = ["10.10.0.0/24"]
}

# -- Network Security Group ---------------------------------------------------

resource "azurerm_network_security_group" "k3s" {
  name                = "taonode-guardian-k3s-nsg"
  location            = azurerm_resource_group.k3s.location
  resource_group_name = azurerm_resource_group.k3s.name
  tags                = local.default_tags

  security_rule {
    name                       = "SSH"
    priority                   = 100
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "22"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "HTTP"
    priority                   = 110
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "80"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "HTTPS"
    priority                   = 120
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "443"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "Grafana"
    priority                   = 130
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "3000"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "GrafanaAlt"
    priority                   = 140
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "3001"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "K3sAPI"
    priority                   = 150
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "6443"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "ArgoCDHTTP"
    priority                   = 160
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "8080"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "ClickHouseHTTP"
    priority                   = 170
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "8123"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "ClickHouseNative"
    priority                   = 180
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "9000"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "Kubecost"
    priority                   = 190
    direction                  = "Inbound"
    access                     = "Allow"
    protocol                   = "Tcp"
    source_port_range          = "*"
    destination_port_range     = "9090"
    source_address_prefixes    = var.admin_cidrs
    destination_address_prefix = "*"
  }

  security_rule {
    name                       = "AllowAllOutbound"
    priority                   = 100
    direction                  = "Outbound"
    access                     = "Allow"
    protocol                   = "*"
    source_port_range          = "*"
    destination_port_range     = "*"
    source_address_prefix      = "*"
    destination_address_prefix = "*"
  }
}

# -- Public IP -----------------------------------------------------------------

resource "azurerm_public_ip" "k3s" {
  name                = "taonode-guardian-k3s-pip"
  location            = azurerm_resource_group.k3s.location
  resource_group_name = azurerm_resource_group.k3s.name
  allocation_method   = "Static"
  sku                 = "Standard"
  tags                = local.default_tags
}

# -- NIC -----------------------------------------------------------------------

resource "azurerm_network_interface" "k3s" {
  name                = "taonode-guardian-k3s-nic"
  location            = azurerm_resource_group.k3s.location
  resource_group_name = azurerm_resource_group.k3s.name
  tags                = local.default_tags

  ip_configuration {
    name                          = "internal"
    subnet_id                     = azurerm_subnet.public.id
    private_ip_address_allocation = "Dynamic"
    public_ip_address_id          = azurerm_public_ip.k3s.id
  }
}

resource "azurerm_network_interface_security_group_association" "k3s" {
  network_interface_id      = azurerm_network_interface.k3s.id
  network_security_group_id = azurerm_network_security_group.k3s.id
}

# -- Linux VM ------------------------------------------------------------------
# custom_data runs bootstrap.sh as root via cloud-init on first boot.
# Standard_D4s_v5 = 4 vCPU / 16 GB RAM (equivalent to AWS t3.xlarge).

resource "azurerm_linux_virtual_machine" "k3s" {
  name                = "taonode-guardian-k3s"
  location            = azurerm_resource_group.k3s.location
  resource_group_name = azurerm_resource_group.k3s.name
  size                = var.vm_size
  admin_username      = "ubuntu"

  network_interface_ids = [
    azurerm_network_interface.k3s.id,
  ]

  admin_ssh_key {
    username   = "ubuntu"
    public_key = var.ssh_public_key
  }

  source_image_reference {
    publisher = "Canonical"
    offer     = "0001-com-ubuntu-server-jammy"
    sku       = "22_04-lts-gen2"
    version   = "latest"
  }

  os_disk {
    name                 = "taonode-guardian-k3s-osdisk"
    caching              = "ReadWrite"
    storage_account_type = "Premium_LRS"
    disk_size_gb         = 100

    encryption_at_host_enabled = false
  }

  custom_data = base64encode(<<-EOF
    #!/bin/bash
    # Injected by Terraform — do NOT commit these values to Git.
    export GITHUB_TOKEN="${var.github_token}"
    export GHCR_PAT="${var.ghcr_pat}"
    ${file("${path.module}/scripts/bootstrap.sh")}
  EOF
  )

  # FinOps: Spot reduces cost by ~65 % vs on-demand for Standard_D4s_v5.
  # Spot eviction triggers a 30-second warning; pair with PodDisruptionBudgets in production.
  priority        = var.use_spot ? "Spot" : "Regular"
  eviction_policy = var.use_spot ? "Delete" : null
  max_bid_price   = var.use_spot ? var.spot_price : null

  tags = merge(local.default_tags, {
    Name = "taonode-guardian-k3s"
  })
}
