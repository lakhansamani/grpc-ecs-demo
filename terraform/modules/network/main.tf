# VPC with PUBLIC subnets only, and no NAT Gateway.
#
# That is a deliberate cost decision, not an oversight. A NAT Gateway is about
# $32/month plus data processing and is the single most common way a demo
# account quietly bleeds money. Tasks get public IPs so they can reach ECR and
# Secrets Manager directly.
#
# For production you would instead use private subnets plus VPC endpoints for
# ecr.api, ecr.dkr, s3, logs and secretsmanager - cheaper than NAT at this
# scale and it keeps traffic off the internet. Say that out loud on stage.

variable "name" { type = string }
variable "cidr_block" {
  type    = string
  default = "10.0.0.0/16"
}
variable "availability_zones" { type = list(string) }
variable "grpc_ports" {
  type        = list(number)
  description = "Container ports that tasks listen on."
  default     = [50051, 50052]
}
variable "ingress_cidrs" {
  type        = list(string)
  description = "Who may reach the task ports from outside the VPC. Lock this to the operator's IP."
  default     = []
}
variable "tags" {
  type    = map(string)
  default = {}
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr_block
  enable_dns_support   = true
  enable_dns_hostnames = true # required for Cloud Map private DNS
  tags                 = merge(var.tags, { Name = var.name })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(var.tags, { Name = var.name })
}

resource "aws_subnet" "public" {
  count                   = length(var.availability_zones)
  vpc_id                  = aws_vpc.this.id
  cidr_block              = cidrsubnet(var.cidr_block, 8, count.index)
  availability_zone       = var.availability_zones[count.index]
  map_public_ip_on_launch = true
  tags                    = merge(var.tags, { Name = "${var.name}-public-${count.index}" })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = merge(var.tags, { Name = "${var.name}-public" })
}

resource "aws_route_table_association" "public" {
  count          = length(aws_subnet.public)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# One security group for all tasks. Task-to-task traffic is allowed by the
# self-referencing rule, which is how orderd reaches userd and productsd privately.
resource "aws_security_group" "tasks" {
  name        = "${var.name}-tasks"
  description = "ECS tasks"
  vpc_id      = aws_vpc.this.id
  tags        = merge(var.tags, { Name = "${var.name}-tasks" })
}

resource "aws_vpc_security_group_ingress_rule" "self" {
  for_each                     = toset([for p in var.grpc_ports : tostring(p)])
  security_group_id            = aws_security_group.tasks.id
  referenced_security_group_id = aws_security_group.tasks.id
  from_port                    = tonumber(each.value)
  to_port                      = tonumber(each.value)
  ip_protocol                  = "tcp"
  description                  = "task-to-task gRPC on ${each.value}"
}

# Optional external access, e.g. so a laptop can drive the demo. Empty by default.
resource "aws_vpc_security_group_ingress_rule" "external" {
  for_each = {
    for pair in setproduct(var.grpc_ports, var.ingress_cidrs) :
    "${pair[0]}-${pair[1]}" => { port = pair[0], cidr = pair[1] }
  }
  security_group_id = aws_security_group.tasks.id
  cidr_ipv4         = each.value.cidr
  from_port         = each.value.port
  to_port           = each.value.port
  ip_protocol       = "tcp"
  description       = "operator access to ${each.value.port}"
}

resource "aws_vpc_security_group_egress_rule" "all" {
  security_group_id = aws_security_group.tasks.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
  description       = "pull images, read secrets, ship logs"
}

output "vpc_id" { value = aws_vpc.this.id }
output "subnet_ids" { value = aws_subnet.public[*].id }
output "security_group_id" { value = aws_security_group.tasks.id }
