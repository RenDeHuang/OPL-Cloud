terraform {
  required_version = ">= 1.6.0"

  required_providers {
    tencentcloud = {
      source  = "tencentcloudstack/tencentcloud"
      version = ">= 1.81.0"
    }
  }
}

provider "tencentcloud" {
  region = var.region
}

locals {
  package_specs = {
    basic = {
      instance_type = var.basic_instance_type
      disk_size_gb  = 10
    }
    pro = {
      instance_type = var.pro_instance_type
      disk_size_gb  = 100
    }
  }
  selected = local.package_specs[var.package_id]
  tags = {
    product      = "opl-cloud"
    workspace_id = var.workspace_id
    owner        = var.owner_account_id
  }
}

resource "tencentcloud_cbs_storage" "workspace_disk" {
  storage_name      = "opl-${var.workspace_slug}-disk"
  availability_zone = var.availability_zone
  storage_type      = var.disk_type
  storage_size      = local.selected.disk_size_gb
  tags              = local.tags
}

resource "tencentcloud_instance" "workspace_server" {
  instance_name              = "opl-${var.workspace_slug}"
  availability_zone          = var.availability_zone
  image_id                   = var.image_id
  instance_type              = local.selected.instance_type
  system_disk_type           = var.system_disk_type
  system_disk_size           = 50
  vpc_id                     = var.vpc_id
  subnet_id                  = var.subnet_id
  security_groups            = [var.security_group_id]
  internet_max_bandwidth_out = var.internet_max_bandwidth_out
  instance_charge_type       = "POSTPAID_BY_HOUR"
  key_ids                    = var.key_id == "" ? [] : [var.key_id]
  user_data_raw = templatefile("${path.module}/cloud-init.yml", {
    workspace_id    = var.workspace_id
    workspace_slug  = var.workspace_slug
    workspace_token = var.workspace_token
    opl_image       = var.opl_image
  })
  tags                       = local.tags
}

resource "tencentcloud_cbs_storage_attachment" "workspace_disk_attachment" {
  storage_id  = tencentcloud_cbs_storage.workspace_disk.id
  instance_id = tencentcloud_instance.workspace_server.id
}

output "server_id" {
  value = tencentcloud_instance.workspace_server.id
}

output "disk_id" {
  value = tencentcloud_cbs_storage.workspace_disk.id
}

output "public_ip" {
  value = tencentcloud_instance.workspace_server.public_ip
}

output "workspace_url" {
  value = "https://${var.workspace_slug}.${var.workspace_domain}/?token=${var.workspace_token}"
}
