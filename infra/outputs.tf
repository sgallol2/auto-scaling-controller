output "vpc_id" {
  value = aws_vpc.main.id
}

output "public_subnet_ids" {
  value = [aws_subnet.public_a.id, aws_subnet.public_b.id]
}

output "private_web_subnet_id" {
  value = aws_subnet.private_web.id
}

output "private_controller_subnet_id" {
  value = aws_subnet.private_controller.id
}

output "load_balancer_dns_name" {
  value = aws_lb.web.dns_name
}

output "target_group_arn" {
  value = aws_lb_target_group.web.arn
}

output "launch_template_id" {
  value = aws_launch_template.web.id
}

output "initial_web_instance_id" {
  value = aws_instance.initial_web.id
}

output "controller_instance_id" {
  value = aws_instance.controller.id
}

output "bastion_instance_id" {
  value = aws_instance.bastion.id
}

output "bastion_public_ip" {
  value = aws_instance.bastion.public_ip
}

