# One reusable ECS service. Instantiated twice: identityd (stateless, scaled)
# and paymentd (stateful, single task).
#
# The ECS-specific lessons of the talk live in this file - see the comments on
# stop_timeout, the healthCheck block and service discovery.

variable "name" { type = string }
variable "cluster_id" { type = string }
variable "image" { type = string }
variable "container_port" { type = number }

# "grpc" for the three gRPC services, "http" for gatewayd. It decides the
# port mapping's appProtocol and which health-check mode the baked-in
# /healthcheck binary uses - distroless has no shell or curl, so both modes
# live in that one Go binary.
variable "protocol" {
  type    = string
  default = "grpc"

  validation {
    condition     = contains(["grpc", "http"], var.protocol)
    error_message = "protocol must be grpc or http."
  }
}
variable "metrics_port" { type = number }

variable "cpu" {
  type    = number
  default = 256
}
variable "memory" {
  type    = number
  default = 512
}
variable "desired_count" {
  type    = number
  default = 1
}
variable "cpu_architecture" {
  type    = string
  default = "ARM64" # Graviton: cheaper, and native on an M-series laptop
}
# Spot is opt-in rather than default, for two reasons:
#
#  1. capacity_provider_strategy produces a perpetual diff against the local
#     emulator, which does not echo the strategy back - and the AWS provider
#     then refuses the apply with "force_new_deployment should be true when
#     capacity_provider_strategy is being updated".
#  2. plain launch_type = FARGATE needs no strategy at all.
#
# Fargate Spot does support ARM64 (GA Oct 2024, platform 1.4.0+), so turning
# this on is safe where interruptions are acceptable.
# ECS Exec. Two uses:
#   - `aws ecs execute-command` to get a shell-ish probe into a running task
#   - SSM port forwarding, so Postman on your laptop can reach a task with NO
#     public IP and NO inbound security-group rule:
#
#       aws ssm start-session --document-name AWS-StartPortForwardingSession \
#         --target ecs:<cluster>_<taskId>_<runtimeId> \
#         --parameters "localPortNumber=50052,portNumber=50052"
#
# Requires ssmmessages permissions on the TASK role (see the iam module).
variable "enable_execute_command" {
  type    = bool
  default = false
}

variable "use_spot" {
  type    = bool
  default = false
}

variable "subnet_ids" { type = list(string) }
variable "security_group_ids" { type = list(string) }
variable "assign_public_ip" {
  type    = bool
  default = true # no NAT Gateway, so tasks need a public IP to reach ECR
}

variable "environment" {
  type    = map(string)
  default = {}
}
variable "secrets" {
  type        = map(string)
  default     = {}
  description = "env var name -> Secrets Manager ARN. Resolved by the execution role at task start."
}

variable "log_group_name" { type = string }
variable "aws_region" { type = string }
variable "execution_role_arn" { type = string }
variable "task_role_arn" { type = string }

variable "namespace_id" {
  type        = string
  default     = ""
  description = "Cloud Map namespace id."
}

# A separate flag rather than `namespace_id == ""`, because namespace_id comes
# from a resource that does not exist at plan time, and `count` must be
# statically known. Deriving it fails with "Invalid count argument".
variable "enable_service_discovery" {
  type    = bool
  default = true
}
variable "dns_ttl" {
  type        = number
  default     = 10
  description = "Low TTL so a scale-out is discovered quickly. Pair with the server's MaxConnectionAge."
}

# ECS sends SIGTERM, waits this long, then SIGKILLs. It MUST exceed the
# application's own drain deadline (SHUTDOWN_TIMEOUT) or the drain is cut short
# and in-flight RPCs die anyway - which defeats the graceful shutdown work.
variable "stop_timeout" {
  type    = number
  default = 30
}

variable "tags" {
  type    = map(string)
  default = {}
}

locals {
  health_command = var.protocol == "http" ? [
    "CMD", "/healthcheck",
    "-http", "http://localhost:${var.container_port}/healthz",
    ] : [
    "CMD", "/healthcheck",
    "-addr", "localhost:${var.container_port}",
    "-service", var.name,
  ]
}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = var.execution_role_arn
  task_role_arn            = var.task_role_arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  container_definitions = jsonencode([{
    name      = var.name
    image     = var.image
    essential = true

    portMappings = [
      {
        name          = "grpc"
        containerPort = var.container_port
        protocol      = "tcp"
        # appProtocol grpc is what lets ECS Service Connect's proxy balance
        # per REQUEST rather than per connection. Harmless without Service
        # Connect, and required with it.
        appProtocol = var.protocol
      },
      {
        name          = "metrics"
        containerPort = var.metrics_port
        protocol      = "tcp"
      },
    ]

    environment = [for k, v in var.environment : { name = k, value = v }]
    secrets     = [for k, v in var.secrets : { name = k, valueFrom = v }]

    # The runtime image is distroless: no shell, no curl, no grpc-health-probe.
    # /healthcheck is a small Go binary baked in for exactly this.
    healthCheck = {
      command     = local.health_command
      interval    = 10
      timeout     = 3
      retries     = 3
      startPeriod = 10
    }

    stopTimeout = var.stop_timeout

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        "awslogs-group"         = var.log_group_name
        "awslogs-region"        = var.aws_region
        "awslogs-stream-prefix" = var.name
      }
    }
  }])

  tags = var.tags
}

# Cloud Map registration. A records are created and removed as tasks come and
# go, which is all "service discovery" means.
resource "aws_service_discovery_service" "this" {
  count = var.enable_service_discovery ? 1 : 0
  name  = var.name

  dns_config {
    namespace_id = var.namespace_id
    dns_records {
      type = "A"
      ttl  = var.dns_ttl
    }
    routing_policy = "MULTIVALUE" # return every healthy task, not just one
  }

  # ECS reports task health, so Cloud Map must not probe independently.
  #
  # DO NOT "clean up" the deprecated failure_threshold below. An EMPTY
  # health_check_custom_config block makes the provider send no custom health
  # config at all, and then Cloud Map never accepts ECS's health reports: every
  # instance stays AWS_INIT_HEALTH_STATUS=UNHEALTHY, is excluded from DNS
  # answers, and clients fail with grpc code 14 "no children to pick from".
  #
  # Verified on real AWS 2026-10-09. AWS forces the value to 1 regardless, so
  # the deprecation warning is cosmetic but the BLOCK is required.
  health_check_custom_config {
    failure_threshold = 1
  }

  tags = var.tags
}

resource "aws_ecs_service" "this" {
  name            = var.name
  cluster         = var.cluster_id
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.desired_count

  # Either a plain launch type or a capacity-provider strategy - never both.
  launch_type = var.use_spot ? null : "FARGATE"

  dynamic "capacity_provider_strategy" {
    for_each = var.use_spot ? [1] : []
    content {
      capacity_provider = "FARGATE_SPOT"
      weight            = 1
    }
  }

  # Required by the provider whenever the strategy changes.
  force_new_deployment = var.use_spot

  enable_execute_command = var.enable_execute_command

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = var.security_group_ids
    assign_public_ip = var.assign_public_ip
  }

  dynamic "service_registries" {
    for_each = var.enable_service_discovery ? [1] : []
    content {
      registry_arn = aws_service_discovery_service.this[0].arn
    }
  }

  # A single-task stateful service cannot run two copies at once, so a deploy
  # must stop the old task before starting the new one. For identityd the
  # defaults (200/100) give a rolling, zero-downtime deploy instead.
  deployment_minimum_healthy_percent = var.desired_count > 1 ? 100 : 0
  deployment_maximum_percent         = var.desired_count > 1 ? 200 : 100

  wait_for_steady_state = false # a stuck rollout should not hang the apply

  tags = var.tags

  lifecycle {
    # Leaves room for `make scale N=3` and autoscaling without terraform
    # fighting the change back on the next apply.
    ignore_changes = [desired_count]
  }
}

output "service_name" { value = aws_ecs_service.this.name }
output "task_definition_arn" { value = aws_ecs_task_definition.this.arn }
output "discovery_name" {
  value = var.enable_service_discovery ? aws_service_discovery_service.this[0].name : ""
}
