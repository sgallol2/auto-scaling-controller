data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

locals {
  common_tags = {
    Project             = var.project_name
    ManagedBy           = "terraform"
    AutoscalerController = "true"
  }
}

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(local.common_tags, { Name = "${var.project_name}-vpc" })
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.common_tags, { Name = "${var.project_name}-igw" })
}

resource "aws_subnet" "public_a" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_a_cidr
  availability_zone       = var.availability_zone_a
  map_public_ip_on_launch = true
  tags                    = merge(local.common_tags, { Name = "${var.project_name}-public-a" })
}

resource "aws_subnet" "public_b" {
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_b_cidr
  availability_zone       = var.availability_zone_b
  map_public_ip_on_launch = true
  tags                    = merge(local.common_tags, { Name = "${var.project_name}-public-b" })
}

resource "aws_subnet" "private_web" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = var.private_web_subnet_cidr
  availability_zone = var.availability_zone_a
  tags              = merge(local.common_tags, { Name = "${var.project_name}-private-web-a" })
}

resource "aws_subnet" "private_controller" {
  vpc_id            = aws_vpc.main.id
  cidr_block        = var.private_controller_subnet_cidr
  availability_zone = var.availability_zone_a
  tags              = merge(local.common_tags, { Name = "${var.project_name}-private-controller-a" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.common_tags, { Name = "${var.project_name}-public-rt" })
}

resource "aws_route" "public_default" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.main.id
}

resource "aws_route_table_association" "public_a" {
  subnet_id      = aws_subnet.public_a.id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "public_b" {
  subnet_id      = aws_subnet.public_b.id
  route_table_id = aws_route_table.public.id
}

resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = merge(local.common_tags, { Name = "${var.project_name}-nat-eip" })
}

resource "aws_nat_gateway" "main" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public_a.id
  depends_on    = [aws_internet_gateway.main]
  tags          = merge(local.common_tags, { Name = "${var.project_name}-nat" })
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.main.id
  tags   = merge(local.common_tags, { Name = "${var.project_name}-private-rt" })
}

resource "aws_route" "private_default" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.main.id
}

resource "aws_route_table_association" "private_web" {
  subnet_id      = aws_subnet.private_web.id
  route_table_id = aws_route_table.private.id
}

resource "aws_route_table_association" "private_controller" {
  subnet_id      = aws_subnet.private_controller.id
  route_table_id = aws_route_table.private.id
}

resource "aws_security_group" "bastion" {
  name        = "${var.project_name}-bastion-sg"
  description = "Security group for Bastion Host allowing SSH access"
  vpc_id      = aws_vpc.main.id
  tags        = merge(local.common_tags, { Name = "${var.project_name}-bastion-sg" })

  ingress {
    protocol    = "tcp"
    from_port   = 22
    to_port     = 22
    cidr_blocks = [var.bastion_allowed_cidr]
  }

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "alb" {
  name        = "${var.project_name}-alb-sg"
  description = "Public ALB traffic"
  vpc_id      = aws_vpc.main.id
  tags        = merge(local.common_tags, { Name = "${var.project_name}-alb-sg" })

  ingress {
    protocol    = "tcp"
    from_port   = 80
    to_port     = 80
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "app" {
  name        = "${var.project_name}-app-sg"
  description = "Web instances reachable from ALB (port 80) and Bastion (port 22)"
  vpc_id      = aws_vpc.main.id
  tags        = merge(local.common_tags, { Name = "${var.project_name}-app-sg" })

  ingress {
    protocol        = "tcp"
    from_port       = 80
    to_port         = 80
    security_groups = [aws_security_group.alb.id]
  }

  ingress {
    protocol        = "tcp"
    from_port       = 22
    to_port         = 22
    security_groups = [aws_security_group.bastion.id]
  }

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_security_group" "controller" {
  name        = "${var.project_name}-controller-sg"
  description = "Controller outbound AWS API access and SSH from Bastion"
  vpc_id      = aws_vpc.main.id
  tags        = merge(local.common_tags, { Name = "${var.project_name}-controller-sg" })

  ingress {
    protocol        = "tcp"
    from_port       = 22
    to_port         = 22
    security_groups = [aws_security_group.bastion.id]
  }

  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_lb_target_group" "web" {
  name                 = "${var.project_name}-web-tg"
  port                 = 80
  protocol             = "HTTP"
  target_type          = "instance"
  vpc_id               = aws_vpc.main.id
  deregistration_delay = 30

  health_check {
    path                = var.health_check_path
    protocol            = "HTTP"
    matcher             = "200-399"
    healthy_threshold   = 2
    unhealthy_threshold = 2
    interval            = 15
    timeout             = 5
  }

  tags = local.common_tags
}

resource "aws_lb" "web" {
  name               = "${var.project_name}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb.id]
  subnets            = [aws_subnet.public_a.id, aws_subnet.public_b.id]
  tags               = local.common_tags
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.web.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.web.arn
  }
}

resource "aws_launch_template" "web" {
  name_prefix            = "${var.project_name}-web-"
  image_id               = var.app_ami_id
  instance_type          = var.app_instance_type
  key_name               = var.key_name

  network_interfaces {
    associate_public_ip_address = false
    subnet_id                   = aws_subnet.private_web.id
    security_groups             = [aws_security_group.app.id]
  }

  tag_specifications {
    resource_type = "instance"

    tags = merge(local.common_tags, {
      role = var.managed_instance_tag
    })
  }

  tags = merge(local.common_tags, { Name = "${var.project_name}-web-template" })
}

resource "aws_instance" "initial_web" {
  ami                         = var.app_ami_id
  instance_type               = var.app_instance_type
  subnet_id                   = aws_subnet.private_web.id
  vpc_security_group_ids      = [aws_security_group.app.id]
  key_name                    = var.key_name
  associate_public_ip_address = false

  tags = merge(local.common_tags, {
    Name = "${var.project_name}-initial-web"
    role = var.managed_instance_tag
  })
}

resource "aws_lb_target_group_attachment" "initial_web" {
  target_group_arn = aws_lb_target_group.web.arn
  target_id        = aws_instance.initial_web.id
  port             = 80
}

resource "aws_instance" "controller" {
  ami                         = var.controller_ami_id
  instance_type               = var.controller_instance_type
  subnet_id                   = aws_subnet.private_controller.id
  vpc_security_group_ids      = [aws_security_group.controller.id]
  key_name                    = var.key_name
  iam_instance_profile       = "LabInstanceProfile"
  associate_public_ip_address = false

  user_data = templatefile("${path.module}/controller-user-data.sh.tftpl", {
    target_group_arn   = aws_lb_target_group.web.arn
    launch_template_id = aws_launch_template.web.id
    state_path         = var.controller_state_path
    use_moving_average = tostring(var.use_moving_average)
    moving_average_window = var.moving_average_window
  })

  tags = merge(local.common_tags, { Name = "${var.project_name}-controller" })
}

resource "aws_instance" "bastion" {
  ami                         = var.bastion_ami_id != "" ? var.bastion_ami_id : var.app_ami_id
  instance_type               = var.bastion_instance_type
  subnet_id                   = aws_subnet.public_a.id
  vpc_security_group_ids      = [aws_security_group.bastion.id]
  key_name                    = var.key_name
  associate_public_ip_address = true

  tags = merge(local.common_tags, { Name = "${var.project_name}-bastion" })
}
