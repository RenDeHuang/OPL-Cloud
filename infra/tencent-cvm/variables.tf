variable "region" {
  type = string
}

variable "availability_zone" {
  type = string
}

variable "workspace_id" {
  type = string
}

variable "workspace_slug" {
  type = string
}

variable "workspace_token" {
  type      = string
  sensitive = true
}

variable "workspace_domain" {
  type = string
}

variable "owner_account_id" {
  type = string
}

variable "package_id" {
  type    = string
  default = "basic"
  validation {
    condition     = contains(["basic", "pro"], var.package_id)
    error_message = "package_id must be basic or pro."
  }
}

variable "opl_image" {
  type    = string
  default = "ghcr.io/gaofeng21cn/one-person-lab-webui:latest"
}

variable "basic_instance_type" {
  type    = string
  default = "SA5.MEDIUM4"
}

variable "pro_instance_type" {
  type    = string
  default = "SA5.2XLARGE16"
}

variable "image_id" {
  type = string
}

variable "vpc_id" {
  type = string
}

variable "subnet_id" {
  type = string
}

variable "security_group_id" {
  type = string
}

variable "key_id" {
  type    = string
  default = ""
}

variable "disk_type" {
  type    = string
  default = "CLOUD_BSSD"
}

variable "system_disk_type" {
  type    = string
  default = "CLOUD_BSSD"
}

variable "internet_max_bandwidth_out" {
  type    = number
  default = 5
}
