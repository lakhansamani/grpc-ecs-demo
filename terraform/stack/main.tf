# The whole deployment, shared verbatim by envs/local and envs/aws.
#
# This file is the point of the talk: it is byte-identical between the two
# environments. The ONLY difference lives in each env's provider.tf - fake
# credentials and an `endpoints` block locally, nothing at all on AWS.

variable "name_prefix" {
  type    = string
  default = "payments"
}
variable "aws_region" { type = string }
variable "availability_zones" { type = list(string) }
variable "namespace" {
  type    = string
  default = "ecom.local"
}
variable "environment" { type = string }

variable "identity_image" { type = string }
variable "payment_image" { type = string }

variable "identity_desired_count" {
  type        = number
  default     = 1
  description = "Scale this one. It is stateless - its SQLite file is baked into the image."
}
variable "payment_desired_count" {
  type        = number
  default     = 1
  description = "MUST stay 1: paymentd holds writable state on its task filesystem."

  validation {
    condition     = var.payment_desired_count == 1
    error_message = "paymentd keeps its database on the task filesystem, so N tasks would mean N divergent databases. Scale identityd instead (SPEC.md 6.4)."
  }
}

variable "operator_ingress_cidrs" {
  type        = list(string)
  default     = []
  description = "CIDRs allowed to reach the task ports directly, e.g. [\"203.0.113.4/32\"]."
}

variable "llm_provider" {
  type        = string
  default     = "template"
  description = "template (no network, no credentials) or bedrock. The deployment has no Bedrock access, so template."
}
variable "otlp_endpoint" {
  type        = string
  default     = ""
  description = "host:port for OTLP gRPC. Empty disables tracing rather than failing."
}
# ---- permission fallbacks -------------------------------------------------
# These exist so the demo degrades gracefully in an account with a restricted
# role, rather than not deploying at all. See docs/AWS_PERMISSIONS.md.

variable "existing_execution_role_arn" {
  type        = string
  default     = ""
  description = "Reuse an existing task execution role instead of creating one (for accounts without iam:CreateRole). iam:PassRole is still mandatory."
}

variable "use_secrets_manager" {
  type        = bool
  default     = true
  description = <<-EOT
    true  - JWT_SECRET is injected from Secrets Manager via the task
            definition's `secrets` block. The real pattern, and the version to
            demo if the account allows it.
    false - JWT_SECRET is passed as a plain environment value. Needs no
            Secrets Manager permission. Every identityd task still shares one
            secret, so scaling still works; you just lose the segment that
            shows the value never entering the image.
  EOT
}

variable "jwt_secret_plain" {
  type        = string
  default     = ""
  sensitive   = true
  description = "Used only when use_secrets_manager = false. Leave empty to generate one."
}

variable "create_log_group" {
  type        = bool
  default     = true
  description = "false relies on ECS creating the group via awslogs-create-group, for accounts without logs:CreateLogGroup. The EXECUTION ROLE then needs logs:CreateLogGroup, which the AWS managed policy does not include."
}

variable "use_spot" {
  type        = bool
  default     = false
  description = "Fargate Spot (~70% cheaper, interruptible). Off by default - see the ecs-service module for why."
}

variable "enable_service_discovery" {
  type        = bool
  default     = true
  description = <<-EOT
    Register services in Cloud Map.

    MUST be false against the local emulator. Ministack's CreateService
    requires a top-level NamespaceId, while the Terraform AWS provider sends it
    nested inside DnsConfig (which is what real AWS accepts), so the call fails
    with InvalidInput. Verified 2026-10-08.

    Locally, `make dns` stands in by adding Docker network aliases, which works
    because service discovery is only DNS underneath. On AWS this is true and
    Cloud Map does it for real.
  EOT
}

variable "tags" {
  type    = map(string)
  default = {}
}

locals {
  identity_port = 50051
  payment_port  = 50052
  tags = merge(var.tags, {
    Project     = var.name_prefix
    Environment = var.environment
    ManagedBy   = "terraform"
  })
}

module "network" {
  source             = "../modules/network"
  name               = "${var.name_prefix}-${var.environment}"
  availability_zones = var.availability_zones
  grpc_ports         = [local.identity_port, local.payment_port]
  ingress_cidrs      = var.operator_ingress_cidrs
  tags               = local.tags
}

module "ecr" {
  source = "../modules/ecr"
  names  = ["identityd", "paymentd"]
  tags   = local.tags
}

module "cluster" {
  source           = "../modules/ecs-cluster"
  name             = "${var.name_prefix}-${var.environment}"
  namespace        = var.namespace
  vpc_id           = module.network.vpc_id
  create_log_group = var.create_log_group
  tags             = local.tags
}

module "secrets" {
  source   = "../modules/secrets"
  count    = var.use_secrets_manager ? 1 : 0
  name     = "${var.name_prefix}-${var.environment}-jwt-secret"
  generate = var.jwt_secret_plain == ""
  value    = var.jwt_secret_plain
  tags     = local.tags
}

# Fallback secret for use_secrets_manager = false. Still ONE value shared by
# every identityd task, which is the property that actually matters.
resource "random_password" "jwt_plain" {
  count   = var.use_secrets_manager || var.jwt_secret_plain != "" ? 0 : 1
  length  = 48
  special = false
}

locals {
  jwt_plain_value = var.use_secrets_manager ? "" : (
    var.jwt_secret_plain != "" ? var.jwt_secret_plain : random_password.jwt_plain[0].result
  )
}

# identityd: stateless, scalable, and the service the load-balancing segment
# scales to three tasks.
module "identityd" {
  source = "../modules/ecs-service"

  name           = "identityd"
  use_spot       = var.use_spot
  cluster_id     = module.cluster.cluster_id
  image          = var.identity_image
  container_port = local.identity_port
  metrics_port   = 9091
  desired_count  = var.identity_desired_count

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = merge({
    ENVIRONMENT                 = var.environment
    GRPC_ADDR                   = ":${local.identity_port}"
    METRICS_ADDR                = ":9091"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    # Recycle connections so clients re-resolve DNS and actually see new tasks
    # after a scale-out. Without this, round_robin on the client is not enough.
    GRPC_MAX_CONNECTION_AGE = "30s"
    # Must stay below the task's stop_timeout (30s) or the drain gets cut off.
    SHUTDOWN_TIMEOUT = "15s"
    },
    var.use_secrets_manager ? {} : { JWT_SECRET = local.jwt_plain_value }
  )

  # Every identityd task must share one signing secret, or a token minted by
  # one task fails on another. This is why the demo wants Secrets Manager.
  secrets = var.use_secrets_manager ? { JWT_SECRET = module.secrets[0].arn } : {}

  log_group_name           = module.cluster.log_group_name
  aws_region               = var.aws_region
  execution_role_arn       = module.iam.execution_role_arn
  task_role_arn            = module.iam.task_role_arn
  namespace_id             = module.cluster.namespace_id
  enable_service_discovery = var.enable_service_discovery
  tags                     = local.tags
}

# paymentd: single task, writable state, and the demo's closing lesson.
module "paymentd" {
  source = "../modules/ecs-service"

  name           = "paymentd"
  use_spot       = var.use_spot
  cluster_id     = module.cluster.cluster_id
  image          = var.payment_image
  container_port = local.payment_port
  metrics_port   = 9092
  desired_count  = var.payment_desired_count

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = {
    ENVIRONMENT = var.environment
    # Cloud Map name, not an IP. dns:/// resolution plus round_robin on the
    # client is what spreads load across identityd's tasks.
    IDENTITY_ADDR               = "identityd.${module.cluster.namespace_name}:${local.identity_port}"
    GRPC_ADDR                   = ":${local.payment_port}"
    METRICS_ADDR                = ":9092"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    GRPC_MAX_CONNECTION_AGE     = "30s"
    SHUTDOWN_TIMEOUT            = "15s"
    LLM_PROVIDER                = var.llm_provider
    AWS_REGION                  = var.aws_region
  }

  log_group_name           = module.cluster.log_group_name
  aws_region               = var.aws_region
  execution_role_arn       = module.iam.execution_role_arn
  task_role_arn            = module.iam.task_role_arn
  namespace_id             = module.cluster.namespace_id
  enable_service_discovery = var.enable_service_discovery
  tags                     = local.tags
}

module "iam" {
  source                      = "../modules/iam"
  name_prefix                 = "${var.name_prefix}-${var.environment}"
  secret_arns                 = var.use_secrets_manager ? [module.secrets[0].arn] : []
  existing_execution_role_arn = var.existing_execution_role_arn
  tags                        = local.tags
}

output "cluster_name" { value = module.cluster.cluster_name }
output "namespace" { value = module.cluster.namespace_name }
output "ecr_repository_urls" { value = module.ecr.repository_urls }
output "jwt_secret_name" {
  value = var.use_secrets_manager ? module.secrets[0].name : "(plain env var - no Secrets Manager)"
}
output "identity_dns" { value = "identityd.${module.cluster.namespace_name}:${local.identity_port}" }
output "payment_dns" { value = "paymentd.${module.cluster.namespace_name}:${local.payment_port}" }
