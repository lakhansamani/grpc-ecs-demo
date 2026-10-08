variable "name" { type = string }
variable "namespace" {
  type        = string
  description = "Cloud Map private DNS namespace, e.g. ecom.local"
}
variable "vpc_id" { type = string }
variable "log_retention_days" {
  type    = number
  default = 1 # a demo account should not accrue log storage
}
variable "enable_container_insights" {
  type        = bool
  default     = false # costs money; off for the demo
  description = "Container Insights. Leave off unless you need the metrics."
}
variable "tags" {
  type    = map(string)
  default = {}
}

resource "aws_ecs_cluster" "this" {
  name = var.name

  setting {
    name  = "containerInsights"
    value = var.enable_container_insights ? "enabled" : "disabled"
  }

  tags = var.tags
}

# Both Fargate on-demand and Spot. Verified: Fargate Spot supports ARM64 as of
# Oct 2024 on platform version 1.4.0+, so there is no architecture caveat and
# the per-service module needs no special casing.
resource "aws_ecs_cluster_capacity_providers" "this" {
  cluster_name       = aws_ecs_cluster.this.name
  capacity_providers = ["FARGATE", "FARGATE_SPOT"]

  default_capacity_provider_strategy {
    capacity_provider = "FARGATE"
    weight            = 1
  }
}

# Cloud Map private DNS namespace: this is what turns "identityd" into a
# resolvable hostname inside the VPC. Service discovery is, underneath, just
# DNS - which is exactly why the local emulator can be stood in for with
# Docker network aliases.
resource "aws_service_discovery_private_dns_namespace" "this" {
  name        = var.namespace
  vpc         = var.vpc_id
  description = "service discovery for ${var.name}"
  tags        = var.tags
}

variable "create_log_group" {
  type        = bool
  default     = true
  description = "false lets ECS create the group via awslogs-create-group instead."
}

resource "aws_cloudwatch_log_group" "this" {
  count             = var.create_log_group ? 1 : 0
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}

output "cluster_id" { value = aws_ecs_cluster.this.id }
output "cluster_name" { value = aws_ecs_cluster.this.name }
output "cluster_arn" { value = aws_ecs_cluster.this.arn }
output "namespace_id" { value = aws_service_discovery_private_dns_namespace.this.id }
output "namespace_name" { value = aws_service_discovery_private_dns_namespace.this.name }
output "log_group_name" { value = "/ecs/${var.name}" }
