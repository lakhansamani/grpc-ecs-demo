# AWS environment: the same stack, against the real thing.

variable "user_image" {
  type        = string
  description = "ECR image URI, e.g. 123456789012.dkr.ecr.ap-south-1.amazonaws.com/userd:0.1.0"
}
variable "product_image" { type = string }
variable "order_image" { type = string }
variable "gateway_image" { type = string }
variable "operator_ingress_cidrs" {
  type        = list(string)
  default     = []
  description = "Your public IP as a /32 so the demo client can reach gatewayd. Keep this tight."
}
# WHERE TO SET YOUR AWS PROFILE.
#
# Three options, and they are checked in this order:
#   1. this variable  -> terraform.tfvars:  aws_profile = "my-profile"
#                        or on the command line: -var aws_profile=my-profile
#   2. the environment -> export AWS_PROFILE=my-profile
#   3. default credentials
#
# Option 1 is the safe one for a demo account: the account is pinned in the
# config, so a stale `export AWS_PROFILE=something-else` cannot quietly point
# an apply at the wrong place. Always confirm with:
#   aws sts get-caller-identity
variable "aws_profile" {
  type        = string
  default     = ""
  description = "Named AWS profile to use. Empty means use the environment."
}

# Region lives here so you can switch without editing the stack. Fargate ARM64
# is available in all commercial regions, so any of them works.
variable "aws_region" {
  type    = string
  default = "us-east-1"
}

variable "user_desired_count" {
  type    = number
  default = 1
}
variable "product_desired_count" {
  type    = number
  default = 1
}
variable "gateway_desired_count" {
  type    = number
  default = 1
}

# ---- permission fallbacks, see docs/AWS_PERMISSIONS.md and minimal.tfvars ----
variable "existing_execution_role_arn" {
  type    = string
  default = ""
}
variable "use_secrets_manager" {
  type    = bool
  default = true
}
variable "enable_service_discovery" {
  type    = bool
  default = true
}
variable "create_log_group" {
  type    = bool
  default = true
}
variable "enable_execute_command" {
  type    = bool
  default = true
}

# OFF by default here, unlike envs/local, because on real AWS this one bills:
# a db.t4g.micro plus 20 GiB gp3 is ~$14/month if left running, or about six
# cents for a three-hour demo. Turn it on in terraform.tfvars or with
# -var use_rds=true. See ../../modules/rds for the arithmetic.
variable "use_rds" {
  type    = bool
  default = false
}

variable "lb_policy" {
  type        = string
  default     = "round_robin"
  description = "pick_first redeploys orderd with the load-balancing bug, for the live demo."
}

module "deployment" {
  source = "../../deployment"

  lb_policy = var.lb_policy
  use_rds   = var.use_rds

  environment        = "aws"
  aws_region         = var.aws_region
  availability_zones = ["${var.aws_region}a", "${var.aws_region}b"]

  user_image    = var.user_image
  product_image = var.product_image
  order_image   = var.order_image
  gateway_image = var.gateway_image

  user_desired_count    = var.user_desired_count
  product_desired_count = var.product_desired_count
  gateway_desired_count = var.gateway_desired_count
  order_desired_count   = 1

  operator_ingress_cidrs = var.operator_ingress_cidrs

  # No ADOT collector deployed, so tracing is disabled rather than failing to
  # export on every span.
  otlp_endpoint = ""

  # No Bedrock access on this deployment. See SPEC.md 6.8.

  existing_execution_role_arn = var.existing_execution_role_arn
  use_secrets_manager         = var.use_secrets_manager
  enable_service_discovery    = var.enable_service_discovery
  create_log_group            = var.create_log_group
  enable_execute_command      = var.enable_execute_command
}

output "cluster_name" { value = module.deployment.cluster_name }
output "namespace" { value = module.deployment.namespace }
output "service_addresses" { value = module.deployment.service_addresses }
output "ecr_repository_urls" { value = module.deployment.ecr_repository_urls }
output "jwt_secret_name" { value = module.deployment.jwt_secret_name }
