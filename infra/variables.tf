variable "aws_region" {
  description = "AWS region for the deployment."
  type        = string
}

variable "availability_zone_a" {
  description = "Primary AZ for private web/controller subnets and one ALB subnet."
  type        = string
}

variable "availability_zone_b" {
  description = "Second AZ for the second public ALB subnet."
  type        = string
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.20.0.0/16"
}

variable "public_subnet_a_cidr" {
  type    = string
  default = "10.20.1.0/24"
}

variable "public_subnet_b_cidr" {
  type    = string
  default = "10.20.2.0/24"
}

variable "private_web_subnet_cidr" {
  description = "Private application subnet in availability_zone_a."
  type        = string
  default     = "10.20.11.0/24"
}

variable "private_controller_subnet_cidr" {
  description = "Separate private controller subnet in availability_zone_a."
  type        = string
  default     = "10.20.12.0/24"
}

variable "app_ami_id" {
  description = "AMI containing the web application listening on port 80."
  type        = string
}

variable "controller_ami_id" {
  description = "AMI containing the controller binary and systemd unit."
  type        = string
}

variable "key_name" {
  description = "Existing EC2 key pair name."
  type        = string
}

variable "app_instance_type" {
  type    = string
  default = "t3.micro"
}

variable "controller_instance_type" {
  type    = string
  default = "t3.micro"
}

variable "project_name" {
  type    = string
  default = "autoscaler-controller"
}

variable "managed_instance_tag" {
  description = "Value of the role tag used by the controller to discover web instances."
  type        = string
  default     = "web-fleet"
}

variable "controller_state_path" {
  type    = string
  default = "/var/lib/autoscaler-controller/state.json"
}

variable "use_moving_average" {
  type    = bool
  default = false
}

variable "moving_average_window" {
  type    = number
  default = 3

  validation {
    condition     = var.moving_average_window >= 1
    error_message = "moving_average_window must be at least 1."
  }
}

variable "health_check_path" {
  type    = string
  default = "/"
}

variable "bastion_ami_id" {
  description = "AMI for the Bastion Host. If left empty, app_ami_id will be used."
  type        = string
  default     = ""
}

variable "bastion_instance_type" {
  description = "EC2 instance type for the Bastion Host."
  type        = string
  default     = "t2.micro"
}

variable "bastion_allowed_cidr" {
  description = "CIDR block allowed to access the Bastion Host via SSH."
  type        = string
  default     = "0.0.0.0/0"
}
