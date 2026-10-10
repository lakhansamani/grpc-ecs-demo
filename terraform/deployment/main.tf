# The whole deployment. envs/local and envs/aws both use this, unchanged.
#
# This file is the point of the talk: it is byte-identical between the two
# environments. The ONLY difference lives in each env's provider.tf - fake
# credentials and an `endpoints` block locally, nothing at all on AWS.

variable "name_prefix" {
  type    = string
  default = "ecom"
}
variable "aws_region" { type = string }
variable "availability_zones" { type = list(string) }
variable "namespace" {
  type    = string
  default = "ecom.local"
}
variable "environment" { type = string }

# Demo lever, not a production knob. "pick_first" redeploys orderd with
# grpc-go's DEFAULT client behaviour - one connection, one upstream task, no
# matter how many tasks Cloud Map returns. That is the bug the talk is about,
# and this is how you show it live instead of describing it.
variable "lb_policy" {
  type    = string
  default = "round_robin"

  validation {
    condition     = contains(["round_robin", "pick_first"], var.lb_policy)
    error_message = "lb_policy must be round_robin (the fix) or pick_first (the bug)."
  }
}

variable "user_image" { type = string }
variable "product_image" { type = string }
variable "order_image" { type = string }
variable "gateway_image" { type = string }

variable "user_desired_count" {
  type        = number
  default     = 1
  description = "Stateless - its SQLite file is baked into the image. Scale this one."
}

variable "product_desired_count" {
  type        = number
  default     = 1
  description = "Stateless and read-only. Browse and search reads dominate, so this is the other one you scale."
}

variable "gateway_desired_count" {
  type        = number
  default     = 1
  description = "Stores nothing at all, so it scales freely."
}
variable "order_desired_count" {
  type        = number
  default     = 1
  description = "MUST stay 1: orderd writes to SQLite on its own task filesystem."

  # The guard applies ONLY to the SQLite shape, where the database lives on the
  # task filesystem and N tasks would mean N divergent databases. With
  # use_rds = true the three services share one instance and orderd scales like
  # anything else, so the restriction lifts itself.
  validation {
    condition     = var.order_desired_count == 1 || var.use_rds
    error_message = "orderd keeps its database on the task filesystem, so N tasks would mean N divergent databases. Either scale userd/productsd instead, or set use_rds = true to give it a shared database."
  }
}

variable "operator_ingress_cidrs" {
  type        = list(string)
  default     = []
  description = "CIDRs allowed to reach the task ports directly, e.g. [\"203.0.113.4/32\"]."
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
            Secrets Manager permission. Every userd task still shares one
            secret, so scaling still works; you just lose the segment that
            shows the value never entering the image.
  EOT
}

# RDS is opt-in, and OFF by default, so an accidental apply never creates a
# billable database. On: one db.t4g.micro with a separate database per service
# (~$14/month if left running, ~6 cents for a three-hour demo). Off: SQLite on
# the task filesystem, which is free but means orderd cannot scale.
variable "use_rds" {
  type    = bool
  default = false
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

variable "enable_execute_command" {
  type        = bool
  default     = false
  description = "ECS Exec + SSM port forwarding, so a laptop can reach a task with no public IP and no inbound SG rule. Needs ssmmessages on the task role."
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
  user_port    = 50051
  order_port   = 50052
  product_port = 50053
  gateway_port = 8080
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
  grpc_ports         = [local.user_port, local.order_port, local.product_port, local.gateway_port]
  ingress_cidrs      = var.operator_ingress_cidrs
  tags               = local.tags
}

module "ecr" {
  source = "../modules/ecr"
  names  = ["userd", "productsd", "orderd", "gatewayd"]
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

# One instance, one database per service. See modules/rds for the cost
# arithmetic and the three settings that make `destroy` actually stop the bill.
module "rds" {
  count  = var.use_rds ? 1 : 0
  source = "../modules/rds"

  name                     = "${var.name_prefix}-${var.environment}"
  vpc_id                   = module.network.vpc_id
  subnet_ids               = module.network.subnet_ids
  client_security_group_id = module.network.security_group_id
  databases                = ["userd", "productsd", "orderd"]
  tags                     = local.tags
}

locals {
  # Postgres when RDS is on, otherwise SQLite on the task. Services read
  # DB_DRIVER; DB_URL arrives as a SECRET, because it contains a password.
  db_env = var.use_rds ? {
    DB_DRIVER    = "postgres"
    SEED_ON_BOOT = "true" # no baked-in file to seed from, so seed at boot
  } : {}

  db_secrets = var.use_rds ? {
    userd     = { DB_URL = module.rds[0].db_url_secret_arns["userd"] }
    productsd = { DB_URL = module.rds[0].db_url_secret_arns["productsd"] }
    orderd    = { DB_URL = module.rds[0].db_url_secret_arns["orderd"] }
  } : { userd = {}, productsd = {}, orderd = {} }
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
# every userd task, which is the property that actually matters.
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

# ---------------------------------------------------------------------------
# Four services, three storage shapes. Each one scales for a different reason,
# and that reason is its storage - not its traffic.
# ---------------------------------------------------------------------------

# userd: database baked into the image, so every task answers reads
# identically. Called by gatewayd AND orderd, which makes it the busiest hop
# in the system and the one the load-balancing segment scales.
module "userd" {
  source = "../modules/ecs-service"

  name                   = "userd"
  cluster_id             = module.cluster.cluster_id
  image                  = var.user_image
  container_port         = local.user_port
  metrics_port           = 9091
  desired_count          = var.user_desired_count
  use_spot               = var.use_spot
  enable_execute_command = var.enable_execute_command

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = merge({
    ENVIRONMENT                 = var.environment
    GRPC_ADDR                   = ":${local.user_port}"
    METRICS_ADDR                = ":9091"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    # Recycle connections so clients re-resolve DNS and actually see new tasks
    # after a scale-out. Client-side round_robin alone is not enough.
    GRPC_MAX_CONNECTION_AGE = "30s"
    # Must stay below the task's stop_timeout (30s) or the drain is cut off.
    SHUTDOWN_TIMEOUT = "15s"
    },
    local.db_env,
    var.use_secrets_manager ? {} : { JWT_SECRET = local.jwt_plain_value }
  )

  # Every userd task must share one signing secret, or a token minted by one
  # task fails verification on another.
  secrets = merge(
    var.use_secrets_manager ? { JWT_SECRET = module.secrets[0].arn } : {},
    local.db_secrets.userd,
  )

  log_group_name           = module.cluster.log_group_name
  aws_region               = var.aws_region
  execution_role_arn       = module.iam.execution_role_arn
  task_role_arn            = module.iam.task_role_arn
  namespace_id             = module.cluster.namespace_id
  enable_service_discovery = var.enable_service_discovery
  tags                     = local.tags
}

# productsd: catalogue and FTS5 search index, also baked in. Read-only at
# runtime, so it scales out when browse traffic spikes.
module "productsd" {
  source = "../modules/ecs-service"

  name                   = "productsd"
  cluster_id             = module.cluster.cluster_id
  image                  = var.product_image
  container_port         = local.product_port
  metrics_port           = 9093
  desired_count          = var.product_desired_count
  use_spot               = var.use_spot
  enable_execute_command = var.enable_execute_command

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = merge({
    ENVIRONMENT                 = var.environment
    GRPC_ADDR                   = ":${local.product_port}"
    METRICS_ADDR                = ":9093"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    GRPC_MAX_CONNECTION_AGE     = "30s"
    SHUTDOWN_TIMEOUT            = "15s"
    },
    local.db_env,
  )

  secrets = local.db_secrets.productsd

  log_group_name           = module.cluster.log_group_name
  aws_region               = var.aws_region
  execution_role_arn       = module.iam.execution_role_arn
  task_role_arn            = module.iam.task_role_arn
  namespace_id             = module.cluster.namespace_id
  enable_service_discovery = var.enable_service_discovery
  tags                     = local.tags
}

# orderd: the only service that WRITES. Its database starts empty and lives on
# the task filesystem, so it stays at one task - enforced by the validation on
# order_desired_count. Makes two outbound hops per order.
module "orderd" {
  source = "../modules/ecs-service"

  name                   = "orderd"
  cluster_id             = module.cluster.cluster_id
  image                  = var.order_image
  container_port         = local.order_port
  metrics_port           = 9092
  desired_count          = var.order_desired_count
  use_spot               = var.use_spot
  enable_execute_command = var.enable_execute_command

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = merge({
    ENVIRONMENT = var.environment
    # Cloud Map names, not IPs. dns:/// resolution plus client-side
    # round_robin is what spreads load across the upstream tasks.
    USER_ADDR                   = "userd.${module.cluster.namespace_name}:${local.user_port}"
    PRODUCT_ADDR                = "productsd.${module.cluster.namespace_name}:${local.product_port}"
    GRPC_ADDR                   = ":${local.order_port}"
    METRICS_ADDR                = ":9092"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    GRPC_MAX_CONNECTION_AGE     = "30s"
    SHUTDOWN_TIMEOUT            = "15s"
    # Only read when it equals "pick_first"; see internal/platform/grpcclient.
    LB_POLICY = var.lb_policy
    },
    local.db_env,
  )

  secrets = local.db_secrets.orderd

  log_group_name           = module.cluster.log_group_name
  aws_region               = var.aws_region
  execution_role_arn       = module.iam.execution_role_arn
  task_role_arn            = module.iam.task_role_arn
  namespace_id             = module.cluster.namespace_id
  enable_service_discovery = var.enable_service_discovery
  tags                     = local.tags
}

# gatewayd: REST in, gRPC out. Stores nothing, so it is the easiest to scale
# and the right thing to put an ALB in front of. protocol = "http" switches
# the health check to the HTTP mode of the baked-in probe binary.
module "gatewayd" {
  source = "../modules/ecs-service"

  name                   = "gatewayd"
  cluster_id             = module.cluster.cluster_id
  image                  = var.gateway_image
  container_port         = local.gateway_port
  metrics_port           = 9094
  protocol               = "http"
  desired_count          = var.gateway_desired_count
  use_spot               = var.use_spot
  enable_execute_command = var.enable_execute_command

  subnet_ids         = module.network.subnet_ids
  security_group_ids = [module.network.security_group_id]

  environment = {
    ENVIRONMENT                 = var.environment
    USER_ADDR                   = "userd.${module.cluster.namespace_name}:${local.user_port}"
    PRODUCT_ADDR                = "productsd.${module.cluster.namespace_name}:${local.product_port}"
    ORDER_ADDR                  = "orderd.${module.cluster.namespace_name}:${local.order_port}"
    HTTP_ADDR                   = ":${local.gateway_port}"
    METRICS_ADDR                = ":9094"
    OTEL_EXPORTER_OTLP_ENDPOINT = var.otlp_endpoint
    SHUTDOWN_TIMEOUT            = "15s"
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
  source      = "../modules/iam"
  name_prefix = "${var.name_prefix}-${var.environment}"
  # The EXECUTION role resolves every injected secret before the container
  # starts, so it needs each DB_URL ARN as well as the JWT one. Miss this and
  # the task fails to start with an error that points at the secret, not here.
  secret_arns = concat(
    var.use_secrets_manager ? [module.secrets[0].arn] : [],
    var.use_rds ? values(module.rds[0].db_url_secret_arns) : [],
  )
  existing_execution_role_arn = var.existing_execution_role_arn
  enable_execute_command      = var.enable_execute_command
  tags                        = local.tags
}

output "cluster_name" { value = module.cluster.cluster_name }
output "namespace" { value = module.cluster.namespace_name }
output "ecr_repository_urls" { value = module.ecr.repository_urls }
output "jwt_secret_name" {
  value = var.use_secrets_manager ? module.secrets[0].name : "(plain env var - no Secrets Manager)"
}
output "service_addresses" {
  value = {
    userd     = "userd.${module.cluster.namespace_name}:${local.user_port}"
    productsd = "productsd.${module.cluster.namespace_name}:${local.product_port}"
    orderd    = "orderd.${module.cluster.namespace_name}:${local.order_port}"
    gatewayd  = "gatewayd.${module.cluster.namespace_name}:${local.gateway_port}"
  }
}
